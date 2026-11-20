package cards

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The proposed changes' repository in the database (DB-79). Each
// operation reads and writes the rows of the changes it names: a
// decision, a check, a logged command, a linked case or an expiry reads
// one change's rows, takes its row's lock (SELECT … FOR UPDATE), and
// writes what changed of it; a proposal reads the changes that wait.
// Writes of two changes do not wait for each other; two of one change
// take turns, so of two people approving one change from two replicas at
// once one approves it and the other is told it is decided.
//
// The changes are still one document for what saves them whole (the
// one-time copy from the ConfigMap of before, a clean start's filter):
// CardsPG is that document too (db.Doc), and such a save takes the turn
// of every write here, which take a shared turn of it (db.ShareDoc).

// CardsPG keeps the proposed changes in the database: the repository the
// store uses, and the document the copy and the clean start save.
type CardsPG struct {
	*db.Doc[record]
}

// NewCardsPG keeps the proposed changes in d.
func NewCardsPG(d *db.DB) *CardsPG {
	return &CardsPG{&db.Doc[record]{DB: d, Key: CardsKey, Read: readCards, Write: writeCards}}
}

// openWhere selects the changes that wait for a decision (Open).
const openWhere = "WHERE state = '" + Queued + "'"

func (p *CardsPG) All(ctx context.Context) (record, error) {
	var out record
	err := p.DB.Snapshot(ctx, func(q db.Querier) (err error) {
		out, _, err = readCards(ctx, q)
		return err
	})
	return out, err
}

func (p *CardsPG) Get(ctx context.Context, id string) (Card, error) {
	var out []Card
	err := p.DB.Snapshot(ctx, func(q db.Querier) (err error) {
		out, _, err = readChanges(ctx, q, "WHERE change_key = $1", cardid.Canon(id))
		return err
	})
	if err != nil {
		return Card{}, err
	}
	if len(out) == 0 {
		return Card{}, ErrNotFound
	}
	return out[0], nil
}

func (p *CardsPG) Edit(ctx context.Context, id string, edit func(*Card) error) (Card, error) {
	var out Card
	id = cardid.Canon(id)
	err := p.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.ShareDoc(ctx, tx, CardsKey); err != nil {
			return err
		}
		// The change's turn: a second edit of it waits here, and reads
		// what the first kept.
		var row string
		if err := tx.QueryRow(ctx, "SELECT change_id::text FROM changes WHERE change_key = $1 FOR UPDATE", id).Scan(&row); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		cards, kept, err := readChanges(ctx, tx, "WHERE change_id = $1", row)
		if err != nil {
			return err
		}
		if len(cards) == 0 {
			return ErrNotFound
		}
		was := cards[0]
		out = cloneCard(was)
		if err := edit(&out); err != nil {
			return err
		}
		b := &pgx.Batch{}
		queueCardChange(b, row, &was, out, kept[0].position, kept[0].position)
		if b.Len() == 0 {
			return nil
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		return p.DB.Notify(ctx, tx, "doc/"+CardsKey)
	})
	return out, err
}

func (p *CardsPG) Add(ctx context.Context, add func(open []Card, next int) (*Card, []Card, error)) error {
	return p.DB.Tx(ctx, func(tx pgx.Tx) error {
		// Proposals take turns, and no change is edited meanwhile: the
		// waiting changes read are the ones written.
		if err := db.LockDoc(ctx, tx, CardsKey); err != nil {
			return err
		}
		next, _, err := readNext(ctx, tx)
		if err != nil {
			return err
		}
		open, kept, err := readChanges(ctx, tx, openWhere)
		if err != nil {
			return err
		}
		given := make([]Card, len(open))
		for i, c := range open {
			given[i] = cloneCard(c)
		}
		c, changed, err := add(given, next+1)
		if err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, x := range changed {
			for i, o := range open {
				if o.ID == x.ID {
					queueCardChange(b, kept[i].id, &o, x, kept[i].position, kept[i].position)
				}
			}
		}
		if c != nil {
			if err := saveNext(ctx, tx, next+1); err != nil {
				return err
			}
			var last int64
			if err := tx.QueryRow(ctx, "SELECT coalesce(max(position), 0) FROM changes").Scan(&last); err != nil {
				return err
			}
			queueCard(b, *c, last+1, db.NewID(), true)
			// The newest decided changes are kept, keepFinished of them.
			b.Queue(`DELETE FROM changes WHERE change_id IN (SELECT change_id FROM changes WHERE state NOT IN ($1, $2) ORDER BY position DESC OFFSET $3)`,
				Queued, Approved, keepFinished)
		}
		if b.Len() == 0 {
			return nil
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		return p.DB.Notify(ctx, tx, "doc/"+CardsKey)
	})
}

func (p *CardsPG) Change(ctx context.Context, edit func(*record) error) error {
	return p.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.LockDoc(ctx, tx, CardsKey); err != nil {
			return err
		}
		old, _, err := readCards(ctx, tx)
		if err != nil {
			return err
		}
		next := record{Next: old.Next, Cards: make([]Card, len(old.Cards))}
		for i, c := range old.Cards {
			next.Cards[i] = cloneCard(c)
		}
		if err := edit(&next); err != nil {
			return err
		}
		keepRecent(&next)
		if err := writeCards(ctx, tx, old, next); err != nil {
			return err
		}
		return p.DB.Notify(ctx, tx, "doc/"+CardsKey)
	})
}

// cloneCard is a copy of a card that shares nothing an edit changes in
// place with the card it is copied from: what the edit is compared with.
func cloneCard(c Card) Card {
	var out Card
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &out)
	return out
}

// ---------- the conversations ----------

// ChatsPG keeps the waiting changes' conversations with Sherlock in the
// database: one conversation read and written at a time, each joined to
// its change's row (DB-57, DB-79). It is also the document the one-time
// copy saves.
type ChatsPG struct {
	*db.Doc[chatsRecord]
}

// NewChatsPG keeps the waiting changes' conversations with Sherlock in d.
func NewChatsPG(d *db.DB) *ChatsPG {
	return &ChatsPG{&db.Doc[chatsRecord]{DB: d, Key: ChatsKey, Read: readChats, Write: writeChats}}
}

func (p *ChatsPG) Chat(ctx context.Context, id string) (chatRecord, error) {
	var kept *sherlock.KeptChat
	err := p.DB.Snapshot(ctx, func(q db.Querier) (err error) {
		kept, err = sherlock.ReadChangeChat(ctx, q, cardid.Canon(id))
		return err
	})
	if err != nil || kept == nil {
		return chatRecord{}, err
	}
	return chatRecord{Cluster: kept.Cluster, Messages: kept.Messages, Updated: kept.Updated}, nil
}

func (p *ChatsPG) SaveChat(ctx context.Context, id string, c chatRecord) error {
	id = cardid.Canon(id)
	return p.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.ShareDoc(ctx, tx, ChatsKey); err != nil {
			return err
		}
		// The conversation's turn, which may have no row yet.
		if err := db.LockName(ctx, tx, "chat/"+id); err != nil {
			return err
		}
		old, err := sherlock.ReadChangeChat(ctx, tx, id)
		if err != nil {
			return err
		}
		b := &pgx.Batch{}
		sherlock.QueueChangeChat(b, id, old, &sherlock.KeptChat{Cluster: c.Cluster, Messages: c.Messages, Updated: c.Updated})
		if b.Len() == 0 {
			return nil
		}
		return tx.SendBatch(ctx, b).Close()
	})
}

func (p *ChatsPG) DropChats(ctx context.Context, gone func(id string) bool) (int, error) {
	messages := 0
	err := p.DB.Tx(ctx, func(tx pgx.Tx) error {
		messages = 0
		if err := db.ShareDoc(ctx, tx, ChatsKey); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT g.change_key, (SELECT count(*) FROM chat_messages m WHERE m.chat_id = c.chat_id)
			FROM chats c JOIN changes g USING (change_id)`)
		if err != nil {
			return err
		}
		var key string
		var n int
		var drop []string
		if _, err := pgx.ForEachRow(rows, []any{&key, &n}, func() error {
			if gone(key) {
				drop = append(drop, key)
				messages += n
			}
			return nil
		}); err != nil {
			return err
		}
		if len(drop) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, "DELETE FROM chats WHERE change_id IN (SELECT change_id FROM changes WHERE change_key = ANY($1))", drop)
		return err
	})
	return messages, err
}
