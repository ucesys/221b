package sherlock

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The conversations Sherlock keeps for another record in the database
// (Phase 13, DB-46): a waiting change's and a case's are a row of chats,
// named by Sherlock's session ("proposal:PC-12", "case:C-12"), with a row
// of chat_messages a message. What is asked of a message is a column: its
// number, role, text, note, status, error, who asked, the agent, the
// model, the times, the cost and the token counts. Its parts (the steps,
// references, view changes, things shown and hand-overs) are one JSON
// value, as the owner decided on November 19, 2026: a chat is read and
// written whole, never asked for by its parts, and the parts change shape
// as Sherlock gains features. The packages that keep such a chat (cards,
// casework) read and write it through here, so the tables are written
// once.

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

// ReadChats returns the kept conversations whose session starts with
// prefix, by session: "proposal:" for the waiting changes'.
func ReadChats(ctx context.Context, q db.Querier, prefix string) (map[string]KeptChat, error) {
	return readChats(ctx, q, "starts_with(session, $1)", prefix)
}

// ReadChat returns the conversation kept under exactly one session, nil
// when none is: a case's ("case:C-1" is also how "case:C-12" starts).
func ReadChat(ctx context.Context, q db.Querier, session string) (*KeptChat, error) {
	kept, err := readChats(ctx, q, "session = $1", session)
	if err != nil {
		return nil, err
	}
	if c, ok := kept[session]; ok {
		return &c, nil
	}
	return nil, nil
}

// readChats reads the conversations whose session meets a condition on $1.
func readChats(ctx context.Context, q db.Querier, where, arg string) (map[string]KeptChat, error) {
	out := map[string]KeptChat{}
	rows, err := q.Query(ctx, "SELECT session, cluster_id, updated_at FROM chats WHERE "+where, arg)
	if err != nil {
		return nil, err
	}
	var session, cluster string
	var updated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&session, &cluster, &updated}, func() error {
		c := KeptChat{Cluster: cluster}
		if updated != nil {
			c.Updated = updated.UTC()
		}
		out[session] = c
		return nil
	}); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	rows, err = q.Query(ctx, `SELECT c.session, m.message_number, m.role, m.body, m.note, m.status, m.error, m.asked_by, m.agent_id, m.model, m.at, m.first_at, m.done_at,
			m.cost_usd, m.tokens_in, m.tokens_out, m.tokens_cache_write, m.tokens_cache_read, m.parts
		FROM chat_messages m JOIN chats c USING (chat_id) WHERE `+strings.Replace(where, "session", "c.session", 1)+` ORDER BY c.session, m.position`, arg)
	if err != nil {
		return nil, err
	}
	var m Msg
	var at time.Time
	var first, done *time.Time
	var in *int
	var u Usage
	var parts []byte
	if _, err := pgx.ForEachRow(rows, []any{&session, &m.ID, &m.Role, &m.Text, &m.Note, &m.Status, &m.Error, &m.Asker, &m.Agent, &m.Model, &at, &first, &done,
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
		c := out[session]
		c.Messages = append(c.Messages, x)
		out[session] = c
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// QueueChat queues what changes a session's kept conversation from old to
// next; nil is no conversation, so a nil next removes it. A message that
// did not change is left alone.
func QueueChat(b *pgx.Batch, session string, old, next *KeptChat) {
	if next == nil {
		if old != nil {
			b.Queue("DELETE FROM chats WHERE session = $1", session)
		}
		return
	}
	var updated *time.Time
	if !next.Updated.IsZero() {
		updated = chatTime(next.Updated)
	}
	var was []Msg
	if old == nil {
		b.Queue("INSERT INTO chats (chat_id, session, cluster_id, updated_at) VALUES ($1, $2, $3, $4)", db.NewID(), session, next.Cluster, updated)
	} else {
		was = old.Messages
		if old.Cluster != next.Cluster || !old.Updated.Equal(next.Updated.Truncate(time.Microsecond)) {
			b.Queue("UPDATE chats SET cluster_id = $2, updated_at = $3 WHERE session = $1", session, next.Cluster, updated)
		}
	}
	for i, m := range next.Messages {
		row := msgRow(m)
		if i < len(was) {
			if sameRow(msgRow(was[i]), row) {
				continue
			}
			b.Queue("DELETE FROM chat_messages WHERE chat_id = (SELECT chat_id FROM chats WHERE session = $1) AND position = $2", session, i)
		}
		b.Queue(`INSERT INTO chat_messages (chat_id, position, message_number, role, body, note, status, error, asked_by, agent_id, model, at, first_at, done_at,
				cost_usd, tokens_in, tokens_out, tokens_cache_write, tokens_cache_read, parts)
			VALUES ((SELECT chat_id FROM chats WHERE session = $1), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
			append([]any{session, i}, row...)...)
	}
	if len(next.Messages) < len(was) {
		b.Queue("DELETE FROM chat_messages WHERE chat_id = (SELECT chat_id FROM chats WHERE session = $1) AND position >= $2", session, len(next.Messages))
	}
}
