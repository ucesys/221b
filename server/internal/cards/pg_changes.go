package cards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The proposed changes in the database (Phase 13, DB-45): a card is a row
// of changes, known by the id the console gives it ("PC-12"), with its
// evidence, actions, plans, results, decisions, history, commands and the
// cases it resolves in tables of their own, every field a column. An
// action's and a condition's arguments are whatever the kind takes, and
// stay one JSON value each (decided with the owner). The store is
// unchanged and reaches the rows through db.Doc; the counter the ids come
// from, which is of no row, is a settings scope. The waiting changes'
// conversations with Sherlock are rows of chats and chat_messages
// (sherlock.ReadChats, DB-46).

// changesScope is the settings row with the next card's number.
const changesScope = "changes"

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// A list read from an array column: no elements is no list, as the
// document's JSON leaves an empty one out.
func listOf(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

// A time the document always writes, as a column that is never null.
func at(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func timePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// argsJSON is an action's or a condition's arguments as their JSON column
// takes them: none for no arguments at all, and for a condition's empty
// ones, which its document leaves out.
func argsJSON(m map[string]any, keepEmpty bool) []byte {
	if m == nil || (len(m) == 0 && !keepEmpty) {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

func argsOf(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

// NewCardsPG keeps the proposed changes in d.
func NewCardsPG(d *db.DB) *db.Doc[record] {
	return &db.Doc[record]{DB: d, Key: CardsKey, Read: readCards, Write: writeCards}
}

func readCards(ctx context.Context, q db.Querier) (record, bool, error) {
	var rec record
	var n struct {
		Next int `json:"next"`
	}
	b, _, err := db.LoadScope(ctx, q, changesScope)
	found := err == nil
	switch {
	case errors.Is(err, db.ErrNotFound):
	case err != nil:
		return rec, false, err
	default:
		if err := json.Unmarshal(b, &n); err != nil {
			return rec, false, err
		}
	}
	rec.Next = n.Next
	var id string

	evidence := map[string][]Evidence{}
	rows, err := q.Query(ctx, "SELECT change_id::text, kind, body, ref, at FROM change_evidence ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var e Evidence
	var when *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &e.Kind, &e.Text, &e.Ref, &when}, func() error {
		x := e
		x.At = timeOf(when)
		evidence[id] = append(evidence[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	actions := map[string][]Action{}
	rows, err = q.Query(ctx, "SELECT change_id::text, kind, args, impact, risk FROM change_actions ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var a Action
	var raw []byte
	if _, err := pgx.ForEachRow(rows, []any{&id, &a.Kind, &raw, &a.Impact, &a.Risk}, func() error {
		x := a
		var err error
		if x.Args, err = argsOf(raw); err != nil {
			return err
		}
		actions[id] = append(actions[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	plans := map[string][]Plan{}
	rows, err = q.Query(ctx, "SELECT change_id::text, summary, targets, impact, fingerprint, unread, warning FROM change_plans ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var p Plan
	var targets, impact []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &p.Summary, &targets, &impact, &p.Fingerprint, &p.Unread, &p.Warning}, func() error {
		x := p
		x.Targets, x.Impact = listOf(targets), listOf(impact)
		plans[id] = append(plans[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	results := map[string][]Result{}
	rows, err = q.Query(ctx, "SELECT change_id::text, action_number, is_ok, summary, detail, ref, at FROM change_results ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var r Result
	var stamp time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &r.Action, &r.OK, &r.Summary, &r.Detail, &r.Ref, &stamp}, func() error {
		x := r
		x.At = stamp.UTC()
		results[id] = append(results[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	approvals, rejections := map[string][]Decision{}, map[string]Decision{}
	rows, err = q.Query(ctx, "SELECT change_id::text, verdict, by_key, by_name, by_kind, at, comment, approval_policy_id FROM change_decisions ORDER BY change_id, verdict, position")
	if err != nil {
		return rec, false, err
	}
	var d Decision
	var verdict string
	if _, err := pgx.ForEachRow(rows, []any{&id, &verdict, &d.By.Key, &d.By.Name, &d.By.Kind, &stamp, &d.Comment, &d.Standing}, func() error {
		x := d
		x.At = stamp.UTC()
		if verdict == verdictRejected {
			rejections[id] = x
		} else {
			approvals[id] = append(approvals[id], x)
		}
		return nil
	}); err != nil {
		return rec, false, err
	}

	history := map[string][]Event{}
	rows, err = q.Query(ctx, "SELECT change_id::text, at, what, by_name, actor_name, actor_kind FROM change_events ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var ev Event
	var actor *string
	var kind string
	if _, err := pgx.ForEachRow(rows, []any{&id, &stamp, &ev.What, &ev.By, &actor, &kind}, func() error {
		x := ev
		x.At, x.Actor = stamp.UTC(), nil
		if actor != nil {
			x.Actor = &Actor{Name: *actor, Kind: kind}
		}
		history[id] = append(history[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	commands := map[string][]Command{}
	rows, err = q.Query(ctx, "SELECT change_id::text, watson_item, line, by_name, at, is_change, status, exit_code, approved_by, finished_at FROM change_commands ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var cmd Command
	var exit *int
	var finished *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &cmd.ID, &cmd.Line, &cmd.By, &stamp, &cmd.Change, &cmd.Status, &exit, &cmd.Approver, &finished}, func() error {
		x := cmd
		x.At, x.Exit, x.Finished = stamp.UTC(), nil, timePtr(finished)
		if exit != nil {
			n := *exit
			x.Exit = &n
		}
		commands[id] = append(commands[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	resolves, resolvesBy := map[string][]string{}, map[string]map[string]string{}
	rows, err = q.Query(ctx, "SELECT change_id::text, resolved_key, is_listed, linked_by FROM change_resolves ORDER BY change_id, position")
	if err != nil {
		return rec, false, err
	}
	var key string
	var listed bool
	var by *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &key, &listed, &by}, func() error {
		if listed {
			resolves[id] = append(resolves[id], key)
		}
		if by != nil {
			if resolvesBy[id] == nil {
				resolvesBy[id] = map[string]string{}
			}
			resolvesBy[id][key] = *by
		}
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, `SELECT change_id::text, change_key, coalesce(agent_id, ''), model, source_kind, source_key, source_label, source_set_by, source_job, cluster_id, title,
			reasoning, condition_kind, condition_args, dedup_key, expires_at, state, created_at, updated_at, requester_key, requester_name, requester_kind, is_production,
			needed_count, rechecked_at, recheck_is_ok, recheck_why, recheck_checked, recheck_warnings, reason, replaced_by_change_key, replaces_change_key,
			approval_policy_id, watson_item, proposed_by_name, proposed_by_kind, last_by_name, last_by_kind, last_at
		FROM changes ORDER BY position`)
	if err != nil {
		return rec, false, err
	}
	var c Card
	var created, updated time.Time
	var expires, rechecked, lastAt *time.Time
	var requester, proposedBy, lastBy *string
	var requesterName, requesterKind, proposedKind, lastKind string
	var rc Recheck
	var checked, warnings []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &c.ID, &c.Agent, &c.Model, &c.Source.Kind, &c.Source.ID, &c.Source.Label, &c.Source.SetBy, &c.Source.Job, &c.Cluster, &c.Title,
		&c.Reasoning, &c.Condition.Kind, &raw, &c.DedupKey, &expires, &c.State, &created, &updated, &requester, &requesterName, &requesterKind, &c.Production,
		&c.Needed, &rechecked, &rc.OK, &rc.Why, &checked, &warnings, &c.Reason, &c.ReplacedBy, &c.Replaces,
		&c.Standing, &c.WatsonItem, &proposedBy, &proposedKind, &lastBy, &lastKind, &lastAt}, func() error {
		x := c
		var err error
		if x.Condition.Args, err = argsOf(raw); err != nil {
			return err
		}
		x.Expires, x.Created, x.Updated, x.LastAt = timeOf(expires), created.UTC(), updated.UTC(), timePtr(lastAt)
		x.Requester, x.Recheck, x.ProposedBy, x.LastBy, x.Rejection = nil, nil, nil, nil, nil
		if requester != nil {
			x.Requester = &Person{Key: *requester, Name: requesterName, Kind: requesterKind}
		}
		if rechecked != nil {
			y := rc
			y.At, y.Checked, y.Warnings = rechecked.UTC(), listOf(checked), listOf(warnings)
			x.Recheck = &y
		}
		if proposedBy != nil {
			x.ProposedBy = &Actor{Name: *proposedBy, Kind: proposedKind}
		}
		if lastBy != nil {
			x.LastBy = &Actor{Name: *lastBy, Kind: lastKind}
		}
		if rej, ok := rejections[id]; ok {
			x.Rejection = &rej
		}
		x.Evidence, x.Actions, x.Plans, x.Results, x.Approvals, x.History, x.Commands = evidence[id], actions[id], plans[id], results[id], approvals[id], history[id], commands[id]
		x.Resolves, x.ResolvesBy = resolves[id], resolvesBy[id]
		rec.Cards = append(rec.Cards, x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Cards) > 0, nil
}

// The two verdicts of change_decisions.
const (
	verdictApproved = "approved"
	verdictRejected = "rejected"
)

func queueDecision(b *pgx.Batch, id, verdict string, position int, d Decision) {
	b.Queue(`INSERT INTO change_decisions (change_id, verdict, position, by_key, by_name, by_kind, at, comment, approval_policy_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, verdict, position, d.By.Key, d.By.Name, d.By.Kind, at(d.At), d.Comment, d.Standing)
}

// queueCard queues a card whole: its row and everything that hangs from it.
func queueCard(b *pgx.Batch, c Card, position int64) {
	id := db.NewID()
	var agent *string
	if c.Agent != "" {
		// A change points at its agent's row.
		b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", c.Agent)
		agent = &c.Agent
	}
	var expires, rechecked *time.Time
	if !c.Expires.IsZero() {
		expires = at(c.Expires)
	}
	var requester, proposedBy, lastBy *string
	var who Person
	if c.Requester != nil {
		who = *c.Requester
		requester = &who.Key
	}
	var rc Recheck
	if c.Recheck != nil {
		rc = *c.Recheck
		rechecked = at(rc.At)
	}
	var proposed, last Actor
	if c.ProposedBy != nil {
		proposed = *c.ProposedBy
		proposedBy = &proposed.Name
	}
	if c.LastBy != nil {
		last = *c.LastBy
		lastBy = &last.Name
	}
	b.Queue(`INSERT INTO changes (change_id, change_key, position, agent_id, model, source_kind, source_key, source_label, source_set_by, source_job, cluster_id, title,
			reasoning, condition_kind, condition_args, dedup_key, expires_at, state, created_at, updated_at, requester_key, requester_name, requester_kind, is_production,
			needed_count, rechecked_at, recheck_is_ok, recheck_why, recheck_checked, recheck_warnings, reason, replaced_by_change_key, replaces_change_key,
			approval_policy_id, watson_item, proposed_by_name, proposed_by_kind, last_by_name, last_by_kind, last_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33,
			$34, $35, $36, $37, $38, $39, $40)`,
		id, c.ID, position, agent, c.Model, c.Source.Kind, c.Source.ID, c.Source.Label, c.Source.SetBy, c.Source.Job, c.Cluster, c.Title,
		c.Reasoning, c.Condition.Kind, argsJSON(c.Condition.Args, false), c.DedupKey, expires, c.State, at(c.Created), at(c.Updated), requester, who.Name, who.Kind, c.Production,
		c.Needed, rechecked, rc.OK, rc.Why, list(rc.Checked), list(rc.Warnings), c.Reason, c.ReplacedBy, c.Replaces,
		c.Standing, c.WatsonItem, proposedBy, proposed.Kind, lastBy, last.Kind, timePtr(c.LastAt))
	for i, e := range c.Evidence {
		var when *time.Time
		if !e.At.IsZero() {
			when = at(e.At)
		}
		b.Queue("INSERT INTO change_evidence (change_id, position, kind, body, ref, at) VALUES ($1, $2, $3, $4, $5, $6)", id, i, e.Kind, e.Text, e.Ref, when)
	}
	for i, a := range c.Actions {
		b.Queue("INSERT INTO change_actions (change_id, position, kind, args, impact, risk) VALUES ($1, $2, $3, $4, $5, $6)", id, i, a.Kind, argsJSON(a.Args, true), a.Impact, a.Risk)
	}
	for i, p := range c.Plans {
		b.Queue("INSERT INTO change_plans (change_id, position, summary, targets, impact, fingerprint, unread, warning) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			id, i, p.Summary, list(p.Targets), list(p.Impact), p.Fingerprint, p.Unread, p.Warning)
	}
	for i, r := range c.Results {
		b.Queue("INSERT INTO change_results (change_id, position, action_number, is_ok, summary, detail, ref, at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			id, i, r.Action, r.OK, r.Summary, r.Detail, r.Ref, at(r.At))
	}
	for i, d := range c.Approvals {
		queueDecision(b, id, verdictApproved, i, d)
	}
	if c.Rejection != nil {
		queueDecision(b, id, verdictRejected, 0, *c.Rejection)
	}
	for i, e := range c.History {
		var name *string
		var a Actor
		if e.Actor != nil {
			a = *e.Actor
			name = &a.Name
		}
		b.Queue("INSERT INTO change_events (change_id, position, at, what, by_name, actor_name, actor_kind) VALUES ($1, $2, $3, $4, $5, $6, $7)", id, i, at(e.At), e.What, e.By, name, a.Kind)
	}
	for i, x := range c.Commands {
		b.Queue(`INSERT INTO change_commands (change_id, position, watson_item, line, by_name, at, is_change, status, exit_code, approved_by, finished_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, i, x.ID, x.Line, x.By, at(x.At), x.Change, x.Status, x.Exit, x.Approver, timePtr(x.Finished))
	}
	// The cases and known issues it lists, in order, each with who linked
	// it; then whoever is named for one it does not list.
	listed := map[string]bool{}
	n := 0
	for _, key := range c.Resolves {
		listed[key] = true
		var by *string
		if who, ok := c.ResolvesBy[key]; ok {
			by = &who
		}
		b.Queue("INSERT INTO change_resolves (change_id, position, resolved_key, is_listed, linked_by) VALUES ($1, $2, $3, true, $4)", id, n, key, by)
		n++
	}
	var rest []string
	for key := range c.ResolvesBy {
		if !listed[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		b.Queue("INSERT INTO change_resolves (change_id, position, resolved_key, is_listed, linked_by) VALUES ($1, $2, $3, false, $4)", id, n, key, c.ResolvesBy[key])
		n++
	}
}

func writeCards(ctx context.Context, tx pgx.Tx, old, next record) error {
	b := &pgx.Batch{}
	if old.Next != next.Next {
		v, _ := json.Marshal(struct {
			Next int `json:"next"`
		}{next.Next})
		if _, err := db.SaveScope(ctx, tx, changesScope, v, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	placed, err := db.Placed(ctx, tx, "changes", "change_key")
	if err != nil {
		return err
	}
	before := map[string]Card{}
	keys := make([]string, len(next.Cards))
	for _, c := range old.Cards {
		before[c.ID] = c
	}
	for i, c := range next.Cards {
		keys[i] = c.ID
	}
	// A newer card has a greater position. Cards come in at the end and go
	// from anywhere, which leaves the others' places as they are.
	places := db.Places(keys, placed)
	kept := make([]string, 0, len(next.Cards))
	for i, c := range next.Cards {
		kept = append(kept, c.ID)
		p, had := placed[c.ID]
		if was, ok := before[c.ID]; had && ok && sameJSON(was, c) {
			if p != places[i] {
				b.Queue("UPDATE changes SET position = $2 WHERE change_key = $1", c.ID, places[i])
			}
			continue
		}
		// Whole: what hangs from it goes with the row.
		b.Queue("DELETE FROM changes WHERE change_key = $1", c.ID)
		queueCard(b, c, places[i])
	}
	b.Queue("DELETE FROM changes WHERE NOT (change_key = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}

// NewChatsPG keeps the waiting changes' conversations with Sherlock in d.
func NewChatsPG(d *db.DB) *db.Doc[chatsRecord] {
	return &db.Doc[chatsRecord]{DB: d, Key: ChatsKey, Read: readChats, Write: writeChats}
}

func readChats(ctx context.Context, q db.Querier) (chatsRecord, bool, error) {
	kept, err := sherlock.ReadChats(ctx, q, FromPrefix)
	if err != nil || len(kept) == 0 {
		return chatsRecord{}, false, err
	}
	rec := chatsRecord{Chats: map[string]chatRecord{}}
	for session, c := range kept {
		rec.Chats[strings.TrimPrefix(session, FromPrefix)] = chatRecord{Cluster: c.Cluster, Messages: c.Messages, Updated: c.Updated}
	}
	return rec, true, nil
}

func writeChats(ctx context.Context, tx pgx.Tx, old, next chatsRecord) error {
	ids := map[string]bool{}
	for id := range old.Chats {
		ids[id] = true
	}
	for id := range next.Chats {
		ids[id] = true
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	b := &pgx.Batch{}
	kept := func(rec chatsRecord, id string) *sherlock.KeptChat {
		c, ok := rec.Chats[id]
		if !ok {
			return nil
		}
		return &sherlock.KeptChat{Cluster: c.Cluster, Messages: c.Messages, Updated: c.Updated}
	}
	for _, id := range order {
		sherlock.QueueChat(b, FromPrefix+id, kept(old, id), kept(next, id))
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}
