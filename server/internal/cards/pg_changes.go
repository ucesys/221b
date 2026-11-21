package cards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
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
// from, which is of no row, is a settings scope. A change's row is changed
// in place and keeps its key while the change is kept (DB-56). The waiting
// changes' conversations with Sherlock are rows of chats and
// chat_messages, joined to the change by that key
// (sherlock.ReadChangeChats, DB-46, DB-57).

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

// readNext reads the number the last change was given, and whether it
// is kept.
func readNext(ctx context.Context, q db.Querier) (int, bool, error) {
	var n struct {
		Next int `json:"next"`
	}
	b, _, err := db.LoadScope(ctx, q, changesScope)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	if err := json.Unmarshal(b, &n); err != nil {
		return 0, false, err
	}
	return n.Next, true, nil
}

func saveNext(ctx context.Context, tx pgx.Tx, next int) error {
	v, _ := json.Marshal(struct {
		Next int `json:"next"`
	}{next})
	_, err := db.SaveScope(ctx, tx, changesScope, v, db.AnyVersion, "")
	return err
}

func readCards(ctx context.Context, q db.Querier) (record, bool, error) {
	var rec record
	next, found, err := readNext(ctx, q)
	if err != nil {
		return rec, false, err
	}
	rec.Next = next
	rec.Cards, _, err = readChanges(ctx, q, "")
	return rec, found || len(rec.Cards) > 0, err
}

// keptChange is a change as read, with its row's key and place.
type keptChange struct {
	id       string
	position int64
}

// readChanges reads the changes whose rows of changes where selects ("" is
// every change), in order, each with its row's key and place. Only those
// changes' rows are read, of every table.
func readChanges(ctx context.Context, q db.Querier, where string, args ...any) ([]Card, []keptChange, error) {
	var out []Card
	var kept []keptChange
	of := ""
	if where != "" {
		of = " WHERE change_id IN (SELECT change_id FROM changes " + where + ")"
	}
	var id string

	evidence := map[string][]Evidence{}
	rows, err := q.Query(ctx, "SELECT change_id::text, kind, body, ref, at FROM change_evidence"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
	}
	var e Evidence
	var when *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &e.Kind, &e.Text, &e.Ref, &when}, func() error {
		x := e
		x.At = timeOf(when)
		evidence[id] = append(evidence[id], x)
		return nil
	}); err != nil {
		return nil, nil, err
	}

	actions := map[string][]Action{}
	rows, err = q.Query(ctx, "SELECT change_id::text, kind, args, impact, risk FROM change_actions"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	plans := map[string][]Plan{}
	rows, err = q.Query(ctx, "SELECT change_id::text, summary, targets, impact, fingerprint, unread, warning FROM change_plans"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
	}
	var p Plan
	var targets, impact []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &p.Summary, &targets, &impact, &p.Fingerprint, &p.Unread, &p.Warning}, func() error {
		x := p
		x.Targets, x.Impact = listOf(targets), listOf(impact)
		plans[id] = append(plans[id], x)
		return nil
	}); err != nil {
		return nil, nil, err
	}

	results := map[string][]Result{}
	rows, err = q.Query(ctx, "SELECT change_id::text, action_number, is_ok, summary, detail, ref, at FROM change_results"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
	}
	var r Result
	var stamp time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &r.Action, &r.OK, &r.Summary, &r.Detail, &r.Ref, &stamp}, func() error {
		x := r
		x.At = stamp.UTC()
		results[id] = append(results[id], x)
		return nil
	}); err != nil {
		return nil, nil, err
	}

	approvals, rejections := map[string][]Decision{}, map[string]Decision{}
	rows, err = q.Query(ctx, "SELECT change_id::text, verdict, by_key, by_name, by_kind, at, comment, approval_policy_id FROM change_decisions"+of+" ORDER BY change_id, verdict, position", args...)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	history := map[string][]Event{}
	rows, err = q.Query(ctx, "SELECT change_id::text, at, what, by_name, actor_name, actor_kind FROM change_events"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	commands := map[string][]Command{}
	rows, err = q.Query(ctx, "SELECT change_id::text, watson_item, line, by_name, at, is_change, status, exit_code, approved_by, finished_at FROM change_commands"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	resolves, resolvesBy := map[string][]string{}, map[string]map[string]string{}
	rows, err = q.Query(ctx, "SELECT change_id::text, resolved_key, is_listed, linked_by FROM change_resolves"+of+" ORDER BY change_id, position", args...)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	rows, err = q.Query(ctx, `SELECT change_id::text, change_key, coalesce(agent_id, ''), model, source_kind, source_key, source_label, source_set_by, source_job, cluster_id, title,
			reasoning, condition_kind, condition_args, dedup_key, expires_at, state, created_at, updated_at, requester_key, requester_name, requester_kind, is_production,
			needed_count, rechecked_at, recheck_is_ok, recheck_why, recheck_checked, recheck_warnings, reason, replaced_by_change_key, replaces_change_key,
			approval_policy_id, watson_item, proposed_by_name, proposed_by_kind, last_by_name, last_by_kind, last_at, position
		FROM changes `+where+` ORDER BY position`, args...)
	if err != nil {
		return nil, nil, err
	}
	var c Card
	var created, updated time.Time
	var expires, rechecked, lastAt *time.Time
	var requester, proposedBy, lastBy *string
	var requesterName, requesterKind, proposedKind, lastKind string
	var rc Recheck
	var checked, warnings []string
	var position int64
	if _, err := pgx.ForEachRow(rows, []any{&id, &c.ID, &c.Agent, &c.Model, &c.Source.Kind, &c.Source.ID, &c.Source.Label, &c.Source.SetBy, &c.Source.Job, &c.Cluster, &c.Title,
		&c.Reasoning, &c.Condition.Kind, &raw, &c.DedupKey, &expires, &c.State, &created, &updated, &requester, &requesterName, &requesterKind, &c.Production,
		&c.Needed, &rechecked, &rc.OK, &rc.Why, &checked, &warnings, &c.Reason, &c.ReplacedBy, &c.Replaces,
		&c.Standing, &c.WatsonItem, &proposedBy, &proposedKind, &lastBy, &lastKind, &lastAt, &position}, func() error {
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
		x.canon()
		out = append(out, x)
		kept = append(kept, keptChange{id: id, position: position})
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return out, kept, nil
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

// changeColumns are the columns of a change's row after its two keys, in
// the order changeRow gives their values.
var changeColumns = []string{"position", "agent_id", "model", "source_kind", "source_key", "source_label", "source_set_by", "source_job", "cluster_id", "title",
	"reasoning", "condition_kind", "condition_args", "dedup_key", "expires_at", "state", "created_at", "updated_at", "requester_key", "requester_name", "requester_kind", "is_production",
	"needed_count", "rechecked_at", "recheck_is_ok", "recheck_why", "recheck_checked", "recheck_warnings", "reason", "replaced_by_change_key", "replaces_change_key",
	"approval_policy_id", "watson_item", "proposed_by_name", "proposed_by_kind", "last_by_name", "last_by_kind", "last_at"}

// The tables that hang from a change's row, each keyed by it.
// resolvedKeys are the keys of what a change resolves, by the id people
// read at $3: a case's ("C-12") or a known issue's ("KI-3"), none where it
// has no row (DB-90).
const resolvedKeys = `(SELECT t.case_id FROM cases t WHERE $3 LIKE 'C-%' AND t.object_name = substr($3, 3)),
	(SELECT k.known_issue_id FROM known_issues k WHERE k.known_issue_key = $3)`

var changeTables = []string{"change_evidence", "change_actions", "change_plans", "change_results", "change_decisions", "change_events", "change_commands", "change_resolves"}

// insertChange makes a change's row ($1 its key, $2 the console's id);
// updateChange changes the row with the key $1 in place.
var insertChange, updateChange = func() (string, string) {
	holders, sets := make([]string, len(changeColumns)), make([]string, len(changeColumns))
	for i, c := range changeColumns {
		holders[i] = "$" + strconv.Itoa(i+3)
		sets[i] = c + " = $" + strconv.Itoa(i+2)
	}
	return "INSERT INTO changes (change_id, change_key, " + strings.Join(changeColumns, ", ") + ") VALUES ($1, $2, " + strings.Join(holders, ", ") + ")",
		"UPDATE changes SET " + strings.Join(sets, ", ") + " WHERE change_id = $1"
}()

// changeRow is a card as the columns of its row, in changeColumns' order.
func changeRow(c Card, position int64) []any {
	var agent *string
	if c.Agent != "" {
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
	return []any{position, agent, c.Model, c.Source.Kind, c.Source.ID, c.Source.Label, c.Source.SetBy, c.Source.Job, c.Cluster, c.Title,
		c.Reasoning, c.Condition.Kind, argsJSON(c.Condition.Args, false), c.DedupKey, expires, c.State, at(c.Created), at(c.Updated), requester, who.Name, who.Kind, c.Production,
		c.Needed, rechecked, rc.OK, rc.Why, list(rc.Checked), list(rc.Warnings), c.Reason, c.ReplacedBy, c.Replaces,
		c.Standing, c.WatsonItem, proposedBy, proposed.Kind, lastBy, last.Kind, timePtr(c.LastAt)}
}

// queueCard queues a card whole: its row, and everything that hangs from
// it. A change keeps its row, and so its key, for as long as it is kept
// (DB-56): a card that is there already (isNew false) has its row changed
// in place under the key id, and what hangs from it written again.
func queueCard(b *pgx.Batch, c Card, position int64, id string, isNew bool) {
	if isNew {
		queueAgent(b, c)
		b.Queue(insertChange, append([]any{id, c.ID}, changeRow(c, position)...)...)
		queueHanging(b, id, Card{}, c)
		return
	}
	queueCardChange(b, id, nil, c, -1, position)
}

// queueAgent makes sure the agent a change points at has a row.
func queueAgent(b *pgx.Batch, c Card) {
	if c.Agent != "" {
		b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", c.Agent)
	}
}

// queueCardChange queues what changes a kept change from was at wasAt
// (nil: not known, so everything) to c at position, under its row's key
// id (DB-79): its row
// updated in place when a column changed, and of each list that hangs
// from it only what changed: an entry added at the end is one insert
// (an approval, a line of history, a logged command), and a list that
// changed otherwise is written again. Nothing of another change is
// written.
func queueCardChange(b *pgx.Batch, id string, was *Card, c Card, wasAt, position int64) {
	if was == nil || wasAt != position || !sameJSON(changeRow(*was, position), changeRow(c, position)) {
		if was == nil || was.Agent != c.Agent {
			queueAgent(b, c)
		}
		b.Queue(updateChange, append([]any{id}, changeRow(c, position)...)...)
	}
	if was == nil {
		for _, t := range changeTables {
			b.Queue("DELETE FROM "+t+" WHERE change_id = $1", id)
		}
		queueHanging(b, id, Card{}, c)
		return
	}
	queueHanging(b, id, *was, c)
}

// grown says how a list changed from was to now: not at all, by entries
// added at its end (from is where they start), or otherwise (from -1).
func grown[T any](was, now []T) (same bool, from int) {
	if sameJSON(was, now) {
		return true, 0
	}
	if len(was) < len(now) && sameJSON(was, now[:len(was)]) {
		return false, len(was)
	}
	return false, -1
}

// queueList queues what changes one list hanging from the change id: the
// entries from where it grew, or the list written again (del deletes it).
func queueList[T any](b *pgx.Batch, id string, was, now []T, del string, put func(i int, v T)) {
	same, from := grown(was, now)
	if same {
		return
	}
	if from < 0 {
		b.Queue(del, id)
		from = 0
	}
	for i := from; i < len(now); i++ {
		put(i, now[i])
	}
}

// queueHanging queues what changes the lists hanging from the change id,
// from was's to c's: from none for a new change.
func queueHanging(b *pgx.Batch, id string, was, c Card) {
	queueList(b, id, was.Evidence, c.Evidence, "DELETE FROM change_evidence WHERE change_id = $1", func(i int, e Evidence) {
		var when *time.Time
		if !e.At.IsZero() {
			when = at(e.At)
		}
		b.Queue("INSERT INTO change_evidence (change_id, position, kind, body, ref, at) VALUES ($1, $2, $3, $4, $5, $6)", id, i, e.Kind, e.Text, e.Ref, when)
	})
	queueList(b, id, was.Actions, c.Actions, "DELETE FROM change_actions WHERE change_id = $1", func(i int, a Action) {
		b.Queue("INSERT INTO change_actions (change_id, position, kind, args, impact, risk) VALUES ($1, $2, $3, $4, $5, $6)", id, i, a.Kind, argsJSON(a.Args, true), a.Impact, a.Risk)
	})
	queueList(b, id, was.Plans, c.Plans, "DELETE FROM change_plans WHERE change_id = $1", func(i int, p Plan) {
		b.Queue("INSERT INTO change_plans (change_id, position, summary, targets, impact, fingerprint, unread, warning) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			id, i, p.Summary, list(p.Targets), list(p.Impact), p.Fingerprint, p.Unread, p.Warning)
	})
	queueList(b, id, was.Results, c.Results, "DELETE FROM change_results WHERE change_id = $1", func(i int, r Result) {
		b.Queue("INSERT INTO change_results (change_id, position, action_number, is_ok, summary, detail, ref, at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			id, i, r.Action, r.OK, r.Summary, r.Detail, r.Ref, at(r.At))
	})
	queueList(b, id, was.Approvals, c.Approvals, "DELETE FROM change_decisions WHERE change_id = $1 AND verdict = '"+verdictApproved+"'", func(i int, d Decision) {
		queueDecision(b, id, verdictApproved, i, d)
	})
	if !sameJSON(was.Rejection, c.Rejection) {
		if was.Rejection != nil {
			b.Queue("DELETE FROM change_decisions WHERE change_id = $1 AND verdict = '"+verdictRejected+"'", id)
		}
		if c.Rejection != nil {
			queueDecision(b, id, verdictRejected, 0, *c.Rejection)
		}
	}
	queueList(b, id, was.History, c.History, "DELETE FROM change_events WHERE change_id = $1", func(i int, e Event) {
		var name *string
		var a Actor
		if e.Actor != nil {
			a = *e.Actor
			name = &a.Name
		}
		b.Queue("INSERT INTO change_events (change_id, position, at, what, by_name, actor_name, actor_kind) VALUES ($1, $2, $3, $4, $5, $6, $7)", id, i, at(e.At), e.What, e.By, name, a.Kind)
	})
	queueList(b, id, was.Commands, c.Commands, "DELETE FROM change_commands WHERE change_id = $1", func(i int, x Command) {
		b.Queue(`INSERT INTO change_commands (change_id, position, watson_item, line, by_name, at, is_change, status, exit_code, approved_by, finished_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, i, x.ID, x.Line, x.By, at(x.At), x.Change, x.Status, x.Exit, x.Approver, timePtr(x.Finished))
	})
	if sameJSON(was.Resolves, c.Resolves) && sameJSON(was.ResolvesBy, c.ResolvesBy) {
		return
	}
	if len(was.Resolves)+len(was.ResolvesBy) > 0 {
		b.Queue("DELETE FROM change_resolves WHERE change_id = $1", id)
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
		b.Queue("INSERT INTO change_resolves (change_id, position, resolved_key, is_listed, linked_by, resolved_case_id, resolved_known_issue_id) VALUES ($1, $2, $3, true, $4, "+resolvedKeys+")", id, n, key, by)
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
		b.Queue("INSERT INTO change_resolves (change_id, position, resolved_key, is_listed, linked_by, resolved_case_id, resolved_known_issue_id) VALUES ($1, $2, $3, false, $4, "+resolvedKeys+")", id, n, key, c.ResolvesBy[key])
		n++
	}
}

// writeCards changes the rows from old to next: the copy from the
// ConfigMap of before, a whole document saved (the clean start), and
// Change. Each change is written as queueCardChange writes it, its id the
// one way (CF-56).
func writeCards(ctx context.Context, tx pgx.Tx, old, next record) error {
	next.canon()
	b := &pgx.Batch{}
	if old.Next != next.Next {
		if err := saveNext(ctx, tx, next.Next); err != nil {
			return err
		}
	}
	placed, err := db.Placed(ctx, tx, "changes", "change_key")
	if err != nil {
		return err
	}
	// The rows there are, by the console's id: a change keeps its own.
	rowOf := map[string]string{}
	rows, err := tx.Query(ctx, "SELECT change_key, change_id::text FROM changes")
	if err != nil {
		return err
	}
	var key, id string
	if _, err := pgx.ForEachRow(rows, []any{&key, &id}, func() error { rowOf[key] = id; return nil }); err != nil {
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
		id, had := rowOf[c.ID]
		if was, ok := before[c.ID]; had && ok && sameJSON(was, c) {
			if placed[c.ID] != places[i] {
				b.Queue("UPDATE changes SET position = $2 WHERE change_id = $1", id, places[i])
			}
			continue
		}
		if !had {
			queueCard(b, c, places[i], db.NewID(), true)
			continue
		}
		var w *Card
		if was, ok := before[c.ID]; ok {
			w = &was
		}
		queueCardChange(b, id, w, c, placed[c.ID], places[i])
	}
	// A change no longer in the document loses its row, and with the row
	// goes what hangs from it and its conversation with Sherlock.
	b.Queue("DELETE FROM changes WHERE NOT (change_key = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}

func readChats(ctx context.Context, q db.Querier) (chatsRecord, bool, error) {
	kept, err := sherlock.ReadChangeChats(ctx, q)
	if err != nil || len(kept) == 0 {
		return chatsRecord{}, false, err
	}
	rec := chatsRecord{Chats: map[string]chatRecord{}}
	for id, c := range kept {
		rec.Chats[id] = chatRecord{Cluster: c.Cluster, Messages: c.Messages, Updated: c.Updated}
	}
	return rec, true, nil
}

// writeChats changes the conversations' rows from old to next. A
// conversation is joined to its change's row (DB-57): one whose change
// has no row is not written, and reads back as none.
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
		sherlock.QueueChangeChat(b, id, kept(old, id), kept(next, id))
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}
