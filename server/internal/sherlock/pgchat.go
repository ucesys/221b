package sherlock

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The conversations Sherlock keeps for another record in the database
// (Phase 13, DB-46, DB-57): a waiting change's and a case's are a row of
// chats, joined to the change or the case by its key (change_id, case_id;
// exactly one is set), which goes with that row. The session Sherlock
// names the conversation by ("proposal:PC-12", "case:C-12") is not kept:
// it is worked out from the key, the change's id or the case's number
// after the prefix. A message is a row of chat_messages. What is asked of
// a message is a column: its number, role, text, note, status, error, who
// asked, the agent, the model, the times, the cost and the token counts.
// Its parts (the steps, references, view changes, things shown and
// hand-overs) are one JSON value, as the owner decided on November 19,
// 2026: a chat is read and written whole, never asked for by its parts,
// and the parts change shape as Sherlock gains features. The packages
// that keep such a chat (cards, casework) read and write it through here,
// so the tables are written once.

// KeptChat is a conversation as the record that wants it keeps it.
type KeptChat struct {
	Cluster  string
	Messages []Msg
	Updated  time.Time
}

// msgParts is what chat_messages.parts holds of a message.
type msgParts struct {
	Steps []Step       `json:"steps,omitempty"`
	Refs  []Ref        `json:"refs,omitempty"`
	View  []ViewChange `json:"view,omitempty"`
	Show  []ViewShow   `json:"show,omitempty"`
	Hands []*Hand      `json:"hands,omitempty"`
}

// A time as a column takes it: UTC, to the microsecond, which is what
// PostgreSQL keeps.
func chatTime(t time.Time) *time.Time {
	u := t.UTC().Truncate(time.Microsecond)
	return &u
}

func chatTimeOrNone(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return chatTime(*t)
}

// msgRow is a message as the columns of its row, in the order the insert
// names them after the chat and the position.
func msgRow(m Msg) []any {
	var parts []byte
	if len(m.Steps)+len(m.Refs)+len(m.View)+len(m.Show)+len(m.Hands) > 0 {
		parts, _ = json.Marshal(msgParts{Steps: m.Steps, Refs: m.Refs, View: m.View, Show: m.Show, Hands: m.Hands})
	}
	var in *int
	var u Usage
	if m.Usage != nil {
		u = *m.Usage
		in = &u.InputTokens
	}
	return []any{m.ID, m.Role, m.Text, m.Note, m.Status, m.Error, m.Asker, m.Agent, m.Model, chatTime(m.At), chatTimeOrNone(m.FirstAt), chatTimeOrNone(m.DoneAt),
		m.CostUSD, in, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, parts}
}

func sameRow(a, b []any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// chatOf is a kind of record a conversation is kept for: the column of
// chats that holds the record's key, and how the key is found from what
// the keeping package knows the record by ($1), null when it has no row.
type chatOf struct {
	column string
	key    string
}

var (
	// A change is known by the id the console gives it.
	chatOfChange = chatOf{column: "change_id", key: "(SELECT change_id FROM changes WHERE change_key = $1)"}
	// A case by its row's key.
	chatOfCase = chatOf{column: "case_id", key: "$1::uuid"}
)

// ReadChangeChats returns the waiting changes' kept conversations, by the
// change's id ("PC-12").
func ReadChangeChats(ctx context.Context, q db.Querier) (map[string]KeptChat, error) {
	return readChats(ctx, q, "g.change_key", "JOIN changes g USING (change_id)")
}

// ReadChangeChat returns the conversation kept for the change with the
// id changeKey ("PC-12"), nil when none is.
func ReadChangeChat(ctx context.Context, q db.Querier, changeKey string) (*KeptChat, error) {
	kept, err := readChats(ctx, q, "g.change_key", "JOIN changes g USING (change_id) WHERE g.change_key = $1", changeKey)
	if err != nil {
		return nil, err
	}
	if c, ok := kept[changeKey]; ok {
		return &c, nil
	}
	return nil, nil
}

// ReadCaseChat returns the conversation kept for the case whose row has
// the key caseID, nil when none is.
func ReadCaseChat(ctx context.Context, q db.Querier, caseID string) (*KeptChat, error) {
	kept, err := readChats(ctx, q, "c.case_id::text", "WHERE c.case_id = $1", caseID)
	if err != nil {
		return nil, err
	}
	if c, ok := kept[caseID]; ok {
		return &c, nil
	}
	return nil, nil
}

// readChats reads the conversations of the rows of chats (c) that rest
// selects, each under what name gives for it.
func readChats(ctx context.Context, q db.Querier, name, rest string, args ...any) (map[string]KeptChat, error) {
	out := map[string]KeptChat{}
	rows, err := q.Query(ctx, "SELECT "+name+", c.cluster_id, c.updated_at FROM chats c "+rest, args...)
	if err != nil {
		return nil, err
	}
	var of, cluster string
	var updated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&of, &cluster, &updated}, func() error {
		c := KeptChat{Cluster: cluster}
		if updated != nil {
			c.Updated = updated.UTC()
		}
		out[of] = c
		return nil
	}); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	rows, err = q.Query(ctx, `SELECT `+name+`, m.message_number, m.role, m.body, m.note, m.status, m.error, m.asked_by, m.agent_id, m.model, m.at, m.first_at, m.done_at,
			m.cost_usd, m.tokens_in, m.tokens_out, m.tokens_cache_write, m.tokens_cache_read, m.parts
		FROM chat_messages m JOIN chats c USING (chat_id) `+rest+` ORDER BY 1, m.position`, args...)
	if err != nil {
		return nil, err
	}
	var m Msg
	var at time.Time
	var first, done *time.Time
	var in *int
	var u Usage
	var parts []byte
	if _, err := pgx.ForEachRow(rows, []any{&of, &m.ID, &m.Role, &m.Text, &m.Note, &m.Status, &m.Error, &m.Asker, &m.Agent, &m.Model, &at, &first, &done,
		&m.CostUSD, &in, &u.OutputTokens, &u.CacheCreationInputTokens, &u.CacheReadInputTokens, &parts}, func() error {
		x := m
		x.At, x.FirstAt, x.DoneAt, x.Usage = at.UTC(), nil, nil, nil
		if first != nil {
			t := first.UTC()
			x.FirstAt = &t
		}
		if done != nil {
			t := done.UTC()
			x.DoneAt = &t
		}
		if in != nil {
			y := u
			y.InputTokens = *in
			x.Usage = &y
		}
		if len(parts) > 0 {
			var p msgParts
			if err := json.Unmarshal(parts, &p); err != nil {
				return err
			}
			x.Steps, x.Refs, x.View, x.Show, x.Hands = p.Steps, p.Refs, p.View, p.Show, p.Hands
		}
		c := out[of]
		c.Messages = append(c.Messages, x)
		out[of] = c
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// QueueChangeChat queues what changes the kept conversation of the change
// with the id changeKey ("PC-12") from old to next; nil is no
// conversation, so a nil next removes it. A message that did not change
// is left alone. A change that has no row keeps no conversation: nothing
// is written for it, and it reads back as none.
func QueueChangeChat(b *pgx.Batch, changeKey string, old, next *KeptChat) {
	queueChat(b, chatOfChange, changeKey, old, next)
}

// QueueCaseChat queues what changes the kept conversation of the case
// whose row has the key caseID, as QueueChangeChat does a change's. The
// case's row is there: its object's first save makes it.
func QueueCaseChat(b *pgx.Batch, caseID string, old, next *KeptChat) {
	queueChat(b, chatOfCase, caseID, old, next)
}

func queueChat(b *pgx.Batch, o chatOf, of string, old, next *KeptChat) {
	mine := o.column + " = " + o.key
	if next == nil {
		if old != nil {
			b.Queue("DELETE FROM chats WHERE "+mine, of)
		}
		return
	}
	var updated *time.Time
	if !next.Updated.IsZero() {
		updated = chatTime(next.Updated)
	}
	var was []Msg
	if old == nil {
		b.Queue(`INSERT INTO chats (chat_id, `+o.column+`, cluster_id, updated_at)
			SELECT $2::uuid, k.id, $3::text, $4::timestamptz FROM (SELECT `+o.key+` AS id) k WHERE k.id IS NOT NULL`, of, db.NewID(), next.Cluster, updated)
	} else {
		was = old.Messages
		if old.Cluster != next.Cluster || !old.Updated.Equal(next.Updated.Truncate(time.Microsecond)) {
			b.Queue("UPDATE chats SET cluster_id = $2, updated_at = $3 WHERE "+mine, of, next.Cluster, updated)
		}
	}
	chat := "(SELECT chat_id FROM chats WHERE " + mine + ")"
	for i, m := range next.Messages {
		row := msgRow(m)
		if i < len(was) {
			if sameRow(msgRow(was[i]), row) {
				continue
			}
			b.Queue("DELETE FROM chat_messages WHERE chat_id = "+chat+" AND position = $2", of, i)
		}
		// From the conversation's row: no row, as for a change that has
		// none, and no message.
		b.Queue(`INSERT INTO chat_messages (chat_id, position, message_number, role, body, note, status, error, asked_by, agent_id, model, at, first_at, done_at,
				cost_usd, tokens_in, tokens_out, tokens_cache_write, tokens_cache_read, parts)
			SELECT chat_id, $2::integer, $3::integer, $4::text, $5::text, $6::text, $7::text, $8::text, $9::text, $10::text, $11::text, $12::timestamptz, $13::timestamptz,
				$14::timestamptz, $15::double precision, $16::integer, $17::integer, $18::integer, $19::integer, $20::json
			FROM chats WHERE `+mine,
			append([]any{of, i}, row...)...)
	}
	if len(next.Messages) < len(was) {
		b.Queue("DELETE FROM chat_messages WHERE chat_id = "+chat+" AND position >= $2", of, len(next.Messages))
	}
}
