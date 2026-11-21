package casework

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The two keys this package keeps in a case's object, in the database
// (Phase 13, DB-53): the work on the case (issues.WorkKey) is a row of
// case_work with the analysis, the RCA, the history, the proposals, the
// drafts and the commands in tables of their own, every field a column;
// the case's conversation with Sherlock (ChatKey) is rows of the message
// tables, under the case's session (DB-46). Both hang from the case's
// row and share its version (issues.CasesPG).

// CaseParts are the keys of a case's object this package keeps.
func CaseParts() map[string]issues.CasePart {
	return map[string]issues.CasePart{issues.WorkKey: workPart{}, ChatKey: chatPart{}}
}

func wAt(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func wAtOrNone(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return wAt(t)
}

func wTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func wList(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func wListOf(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

type workPart struct{}

// The tables that hold the work on a case, each keyed by the case.
var workTables = []string{"case_commands", "case_drafts", "case_proposals", "case_events", "rcas", "case_analyses", "case_work"}

func (workPart) Drop(context.Context, pgx.Tx, string, string) error { return nil }

func (workPart) Read(ctx context.Context, q db.Querier, id, _ string) ([]byte, bool, error) {
	var w Work
	var applied, updated *time.Time
	var runs []string
	var note Note
	// The case's number is its row's (DB-90: case_work no longer repeats it).
	err := q.QueryRow(ctx, `SELECT (SELECT c.case_number FROM cases c WHERE c.case_id = case_work.case_id), source_kind, finding_key, run_key, job_id, check_name, severity, alert, alert_key, runs, fix,
			fix_applied_at, fix_applied_body, fix_applied_by, fix_applied_change_key, updated_at
		FROM case_work WHERE case_id = $1`, id).Scan(&w.Number, &w.Source.Kind, &w.Source.Finding, &w.Source.Run, &w.Source.Job, &w.Source.Check, &w.Source.Severity,
		&w.Source.Alert, &w.Source.AlertKey, &runs, &w.Fix, &applied, &note.Text, &note.By, &note.Change, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	w.Runs, w.Updated = wListOf(runs), wTime(updated)
	if applied != nil {
		note.At = applied.UTC()
		w.FixApplied = &note
	}
	var a Analysis
	var at time.Time
	var evidence, commands, ruledOut, leftOut []string
	err = q.QueryRow(ctx, `SELECT run_key, analyzed_at, summary, cause, evidence, fix_summary, fix_commands, confidence, ruled_out, is_expected, expected_why, error, left_out
		FROM case_analyses WHERE case_id = $1`, id).Scan(&a.Run, &at, &a.Summary, &a.Cause, &evidence, &a.Fix.Summary, &commands, &a.Confidence, &ruledOut, &a.Expected,
		&a.ExpectedWhy, &a.Error, &leftOut)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, false, err
	default:
		a.At, a.Evidence, a.Fix.Commands, a.RuledOut, a.LeftOut = at.UTC(), wListOf(evidence), wListOf(commands), wListOf(ruledOut), wListOf(leftOut)
		w.Analysis = &a
	}
	var r RCA
	var cases []string
	err = q.QueryRow(ctx, `SELECT rca_key, case_key, ARRAY(SELECT x.linked_case_key FROM rca_cases x WHERE x.case_id = rcas.case_id ORDER BY x.position),
			title, cluster_id, resource, priority, cause, fix, prevention, body, written_by, written_at
		FROM rcas WHERE case_id = $1`, id).Scan(&r.ID, &r.Case, &cases, &r.Title, &r.Cluster, &r.Resource, &r.Priority, &r.Cause, &r.Fix, &r.Prevention, &r.Text, &r.By, &at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, false, err
	default:
		r.Cases, r.At = wListOf(cases), at.UTC()
		w.RCA = &r
	}
	rows, err := q.Query(ctx, "SELECT at, what, by_name, by_kind FROM case_events WHERE case_id = $1 ORDER BY position", id)
	if err != nil {
		return nil, false, err
	}
	var ev Event
	if _, err := pgx.ForEachRow(rows, []any{&at, &ev.What, &ev.By.Name, &ev.By.Kind}, func() error {
		x := ev
		x.At = at.UTC()
		w.History = append(w.History, x)
		return nil
	}); err != nil {
		return nil, false, err
	}
	rows, err = q.Query(ctx, "SELECT change_key, is_listed, linked_by, handled FROM case_proposals WHERE case_id = $1 ORDER BY position", id)
	if err != nil {
		return nil, false, err
	}
	var key string
	var listed bool
	var linkedBy, handled *string
	if _, err := pgx.ForEachRow(rows, []any{&key, &listed, &linkedBy, &handled}, func() error {
		if listed {
			w.Proposals = append(w.Proposals, key)
		}
		if linkedBy != nil {
			if w.LinkedBy == nil {
				w.LinkedBy = map[string]string{}
			}
			w.LinkedBy[key] = *linkedBy
		}
		if handled != nil {
			if w.Handled == nil {
				w.Handled = map[string]string{}
			}
			w.Handled[key] = *handled
		}
		return nil
	}); err != nil {
		return nil, false, err
	}
	// The drafts: the case's own, and each person's.
	type owner struct {
		shared bool
		key    string
	}
	drafters := map[owner]map[string][]string{}
	rows, err = q.Query(ctx, "SELECT is_shared, owner_key, part, drafter FROM case_draft_drafters WHERE case_id = $1 ORDER BY is_shared, owner_key, part, position", id)
	if err != nil {
		return nil, false, err
	}
	var o owner
	var part, who string
	if _, err := pgx.ForEachRow(rows, []any{&o.shared, &o.key, &part, &who}, func() error {
		if drafters[o] == nil {
			drafters[o] = map[string][]string{}
		}
		drafters[o][part] = append(drafters[o][part], who)
		return nil
	}); err != nil {
		return nil, false, err
	}
	rows, err = q.Query(ctx, `SELECT is_shared, owner_key, reply, fix, close_outcome, close_explanation, close_original, rca_cause, rca_fix, rca_prevention, rca_body, by_name, updated_at, at
		FROM case_drafts WHERE case_id = $1 ORDER BY is_shared, owner_key`, id)
	if err != nil {
		return nil, false, err
	}
	var d Draft
	var closeOutcome, rcaCause *string
	var cl Closing
	var comp Completion
	var dUpdated, dAt *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&o.shared, &o.key, &d.Reply, &d.Fix, &closeOutcome, &cl.Explanation, &cl.Original, &rcaCause, &comp.Fix, &comp.Prevention, &comp.Text,
		&d.By, &dUpdated, &dAt}, func() error {
		x := d
		x.Close, x.RCA, x.Updated, x.At, x.Drafters = nil, nil, wTime(dUpdated), wTime(dAt), drafters[o]
		if closeOutcome != nil {
			c := cl
			c.Outcome = issues.Outcome(*closeOutcome)
			x.Close = &c
		}
		if rcaCause != nil {
			c := comp
			c.Cause = *rcaCause
			x.RCA = &c
		}
		if o.shared {
			w.Draft = &x
			return nil
		}
		if w.Drafts == nil {
			w.Drafts = map[string]Draft{}
		}
		w.Drafts[o.key] = x
		return nil
	}); err != nil {
		return nil, false, err
	}
	rows, err = q.Query(ctx, "SELECT watson_item, line, by_name, at, is_change, status, exit_code, approved_by, finished_at FROM case_commands WHERE case_id = $1 ORDER BY position", id)
	if err != nil {
		return nil, false, err
	}
	var cmd Command
	var exit *int
	var finished *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&cmd.ID, &cmd.Line, &cmd.By, &at, &cmd.Change, &cmd.Status, &exit, &cmd.Approver, &finished}, func() error {
		x := cmd
		x.At, x.Exit, x.Finished = at.UTC(), nil, nil
		if exit != nil {
			n := *exit
			x.Exit = &n
		}
		if finished != nil {
			t := finished.UTC()
			x.Finished = &t
		}
		w.Commands = append(w.Commands, x)
		return nil
	}); err != nil {
		return nil, false, err
	}
	b, err := json.Marshal(&w)
	return b, true, err
}

func draftStmts(id string, shared bool, owner string, d Draft) []workStmt {
	var closeOutcome, rcaCause *string
	var cl Closing
	var comp Completion
	if d.Close != nil {
		cl = *d.Close
		o := string(cl.Outcome)
		closeOutcome = &o
	}
	if d.RCA != nil {
		comp = *d.RCA
		rcaCause = &comp.Cause
	}
	out := []workStmt{{`INSERT INTO case_drafts (case_id, is_shared, owner_key, reply, fix, close_outcome, close_explanation, close_original, rca_cause, rca_fix, rca_prevention, rca_body, by_name,
			updated_at, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		[]any{id, shared, owner, d.Reply, d.Fix, closeOutcome, cl.Explanation, cl.Original, rcaCause, comp.Fix, comp.Prevention, comp.Text, d.By, wAtOrNone(d.Updated), wAtOrNone(d.At)}}}
	parts := make([]string, 0, len(d.Drafters))
	for p := range d.Drafters {
		parts = append(parts, p)
	}
	sort.Strings(parts)
	for _, p := range parts {
		for i, who := range d.Drafters[p] {
			out = append(out, workStmt{"INSERT INTO case_draft_drafters (case_id, is_shared, owner_key, part, position, drafter) VALUES ($1, $2, $3, $4, $5, $6)",
				[]any{id, shared, owner, p, i, who}})
		}
	}
	return out
}

// workStmt is one statement that writes rows of the work on a case.
type workStmt struct {
	sql  string
	args []any
}

// wSameArgs says two statements' values are the same as the database
// keeps them: times to the microsecond.
func wSameArgs(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, isTime := a[i].(*time.Time)
		if y, ok := b[i].(*time.Time); isTime && ok {
			if (x == nil) != (y == nil) || x != nil && !x.Truncate(time.Microsecond).Equal(y.Truncate(time.Microsecond)) {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameStmts(a, b []workStmt) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].sql != b[i].sql || !wSameArgs(a[i].args, b[i].args) {
			return false
		}
	}
	return true
}

// workRowValues are the columns of the case's row of case_work after its
// key, in the order workColumns names them.
func workRowValues(w *Work) []any {
	var applied *time.Time
	var note Note
	if w.FixApplied != nil {
		note = *w.FixApplied
		applied = wAt(note.At)
	}
	return []any{w.Source.Kind, w.Source.Finding, w.Source.Run, w.Source.Job, w.Source.Check, w.Source.Severity, w.Source.Alert, w.Source.AlertKey, wList(w.Runs), w.Fix,
		applied, note.Text, note.By, note.Change, wAt(w.Updated)}
}

const workColumns = `source_kind, finding_key, run_key, job_id, check_name, severity, alert, alert_key, runs, fix, fix_applied_at,
	fix_applied_body, fix_applied_by, fix_applied_change_key, updated_at`

// A part of the work kept in tables of its own: the statement that
// deletes its rows, and those that write them.
type workSection struct {
	drop string
	rows func(id string, w *Work) []workStmt
}

// The parts of the work written whole when they change. An RCA's cases and
// a draft's drafters go with their RCA and their draft.
var workSections = []workSection{
	{"DELETE FROM case_analyses WHERE case_id = $1", analysisRows},
	{"DELETE FROM rcas WHERE case_id = $1", rcaRows},
	{"DELETE FROM case_proposals WHERE case_id = $1", proposalRows},
	{"DELETE FROM case_drafts WHERE case_id = $1", draftRows},
	{"DELETE FROM case_commands WHERE case_id = $1", commandRows},
}

func analysisRows(id string, w *Work) []workStmt {
	a := w.Analysis
	if a == nil {
		return nil
	}
	return []workStmt{{`INSERT INTO case_analyses (case_id, run_key, analyzed_at, summary, cause, evidence, fix_summary, fix_commands, confidence, ruled_out, is_expected, expected_why,
			error, left_out)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		[]any{id, a.Run, wAt(a.At), a.Summary, a.Cause, wList(a.Evidence), a.Fix.Summary, wList(a.Fix.Commands), a.Confidence, wList(a.RuledOut), a.Expected, a.ExpectedWhy, a.Error,
			wList(a.LeftOut)}}}
}

func rcaRows(id string, w *Work) []workStmt {
	r := w.RCA
	if r == nil {
		return nil
	}
	out := []workStmt{{`INSERT INTO rcas (case_id, rca_key, case_key, title, cluster_id, resource, priority, cause, fix, prevention, body, written_by, written_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		[]any{id, r.ID, r.Case, r.Title, r.Cluster, r.Resource, r.Priority, r.Cause, r.Fix, r.Prevention, r.Text, r.By, wAt(r.At)}}}
	// The other cases it covers: each with its case's key while the case
	// has a row, and the id people read, which stays when the case goes
	// (DB-89).
	for i, c := range r.Cases {
		out = append(out, workStmt{`INSERT INTO rca_cases (case_id, position, linked_case_id, linked_case_key)
			VALUES ($1, $2, (SELECT t.case_id FROM cases t WHERE $3 LIKE 'C-%' AND t.object_name = substr($3, 3)), $3)`, []any{id, i, c}})
	}
	return out
}

// proposalRows are the changes the work lists, in order, each with who
// linked it and how it was handled; then those only one of the two is
// kept of.
func proposalRows(id string, w *Work) []workStmt {
	var out []workStmt
	listed := map[string]bool{}
	proposal := func(key string, isListed bool) {
		var by, handled *string
		if v, ok := w.LinkedBy[key]; ok {
			by = &v
		}
		if v, ok := w.Handled[key]; ok {
			handled = &v
		}
		out = append(out, workStmt{"INSERT INTO case_proposals (case_id, position, change_key, is_listed, linked_by, handled) VALUES ($1, $2, $3, $4, $5, $6)",
			[]any{id, len(out), key, isListed, by, handled}})
	}
	for _, key := range w.Proposals {
		listed[key] = true
		proposal(key, true)
	}
	rest := map[string]bool{}
	for key := range w.LinkedBy {
		rest[key] = !listed[key]
	}
	for key := range w.Handled {
		rest[key] = !listed[key]
	}
	others := make([]string, 0, len(rest))
	for key, only := range rest {
		if only {
			others = append(others, key)
		}
	}
	sort.Strings(others)
	for _, key := range others {
		proposal(key, false)
	}
	return out
}

// draftRows are the case's own draft and each person's.
func draftRows(id string, w *Work) []workStmt {
	var out []workStmt
	if w.Draft != nil {
		out = append(out, draftStmts(id, true, "", *w.Draft)...)
	}
	owners := make([]string, 0, len(w.Drafts))
	for o := range w.Drafts {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	for _, o := range owners {
		out = append(out, draftStmts(id, false, o, w.Drafts[o])...)
	}
	return out
}

func commandRows(id string, w *Work) []workStmt {
	var out []workStmt
	for i, c := range w.Commands {
		var finished *time.Time
		if c.Finished != nil {
			finished = wAt(*c.Finished)
		}
		out = append(out, workStmt{`INSERT INTO case_commands (case_id, position, watson_item, line, by_name, at, is_change, status, exit_code, approved_by, finished_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, []any{id, i, c.ID, c.Line, c.By, wAt(c.At), c.Change, c.Status, c.Exit, c.Approver, finished}})
	}
	return out
}

// Write changes the work's rows to hold doc, touching of them only what
// changed (DB-88): the row of case_work updated in place, a part kept in
// tables of its own written again only when it changed, and the history
// by position, an event added being one insert. A case whose work has no
// row yet has every table of it written.
func (workPart) Write(ctx context.Context, tx pgx.Tx, id, _ string, doc []byte) error {
	var w Work
	if err := json.Unmarshal(doc, &w); err != nil {
		return err
	}
	old, had, err := workPart{}.Read(ctx, tx, id, "")
	if err != nil {
		return err
	}
	var was Work
	if had {
		if err := json.Unmarshal(old, &was); err != nil {
			return err
		}
	}
	b := &pgx.Batch{}
	row := workRowValues(&w)
	switch {
	case !had:
		for _, t := range workTables {
			b.Queue("DELETE FROM "+t+" WHERE case_id = $1", id)
		}
		b.Queue(`INSERT INTO case_work (case_id, `+workColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`, append([]any{id}, row...)...)
	case !wSameArgs(workRowValues(&was), row):
		b.Queue(`UPDATE case_work SET (`+workColumns+`) = ($2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) WHERE case_id = $1`, append([]any{id}, row...)...)
	}
	for _, sec := range workSections {
		next := sec.rows(id, &w)
		if had {
			if sameStmts(sec.rows(id, &was), next) {
				continue
			}
			b.Queue(sec.drop, id)
		}
		for _, s := range next {
			b.Queue(s.sql, s.args...)
		}
	}
	for i, e := range w.History {
		now := []any{wAt(e.At), e.What, e.By.Name, e.By.Kind}
		if i < len(was.History) {
			p := was.History[i]
			if wSameArgs([]any{wAt(p.At), p.What, p.By.Name, p.By.Kind}, now) {
				continue
			}
			b.Queue("UPDATE case_events SET at = $3, what = $4, by_name = $5, by_kind = $6 WHERE case_id = $1 AND position = $2", append([]any{id, i}, now...)...)
			continue
		}
		b.Queue("INSERT INTO case_events (case_id, position, at, what, by_name, by_kind) VALUES ($1, $2, $3, $4, $5, $6)", append([]any{id, i}, now...)...)
	}
	if len(was.History) > len(w.History) {
		b.Queue("DELETE FROM case_events WHERE case_id = $1 AND position >= $2", id, len(w.History))
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

// chatPart keeps a case's conversation with Sherlock in the message
// tables, joined to the case by its row's key (DB-57): it is read and
// written by that key, never by the session's name, and goes with the
// case's row.
type chatPart struct{}

func (chatPart) Read(ctx context.Context, q db.Querier, id, _ string) ([]byte, bool, error) {
	c, err := sherlock.ReadCaseChat(ctx, q, id)
	if err != nil || c == nil {
		return nil, false, err
	}
	b, err := json.Marshal(chatRecord{Cluster: c.Cluster, Messages: c.Messages, Updated: c.Updated})
	return b, true, err
}

func (chatPart) Write(ctx context.Context, tx pgx.Tx, id, _ string, doc []byte) error {
	var rec chatRecord
	if err := json.Unmarshal(doc, &rec); err != nil {
		return err
	}
	old, err := sherlock.ReadCaseChat(ctx, tx, id)
	if err != nil {
		return err
	}
	// An emptied conversation ("{}") is none.
	var next *sherlock.KeptChat
	if rec.Cluster != "" || len(rec.Messages) > 0 || !rec.Updated.IsZero() {
		next = &sherlock.KeptChat{Cluster: rec.Cluster, Messages: rec.Messages, Updated: rec.Updated}
	}
	b := &pgx.Batch{}
	sherlock.QueueCaseChat(b, id, old, next)
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

// The conversation goes with the case's row.
func (chatPart) Drop(context.Context, pgx.Tx, string, string) error { return nil }
