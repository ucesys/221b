package automation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Automation's variable changes in the database (Phase 13, DB-34): a
// change is a row of variable_changes, what it alters rows of
// variable_change_items, and the script versions it touches rows of
// variable_change_impacts. A change read from here has, as its before and
// after, only what it alters (Sides); the store applies a change by its
// items, so that is all it needs.
//
// The same document holds the runs that wait for their values' approval.
// Those are rows of script_runs, the runs' own table (DB-37): one list has
// every run, the ones that wait, were rejected or expired among them, and
// a request that was approved and the run it started are one row. This
// document writes a request's own columns, and the columns a request
// shares with its run only until the run has started. The two counters
// are in the settings scope below.

const changesScope = "automation-changes"

// changesRest is what the scope holds: the counters.
type changesRest struct {
	Next    int `json:"next"`
	NextRun int `json:"nextRun,omitempty"`
	// Runs is where a build of November 19, 2026 kept the requests, whole:
	// read once more, and written as rows by the next save.
	Runs []WaitingRun `json:"runs,omitempty"`
}

// NewChangesPG keeps the variable changes in d.
func NewChangesPG(d *db.DB) *db.Doc[changesRecord] {
	return &db.Doc[changesRecord]{DB: d, Key: ChangesKey,
		Read: func(ctx context.Context, q db.Querier) (changesRecord, bool, error) {
			return readChanges(ctx, q, false)
		},
		Write: func(ctx context.Context, tx pgx.Tx, old, next changesRecord) error {
			return writeChanges(ctx, tx, sensitiveFor(d), old, next)
		}}
}

// UnmovedNew stands, in a change read from the tables, for a sensitive
// value the change would set that is still in its row: it differs from
// Unmoved, so the change still reads as one that alters the value, and
// it stands for nothing the Secret keeps, so the change cannot be
// approved until the start has moved its values.
const UnmovedNew = hiddenPrefix + "000000000000000000000001"

// readChanges reads the changes and the runs that wait. A sensitive value
// is read as its digest, which is what its row has (DB-71). A value under
// a sensitive name that is still in its row (the start could not move
// it) is not read: Unmoved stands for it, unless raw is set, which is for
// the move alone.
func readChanges(ctx context.Context, q db.Querier, raw bool) (changesRecord, bool, error) {
	var rec changesRecord
	b, _, err := db.LoadScope(ctx, q, changesScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return rec, false, err
	}
	if found {
		var rest changesRest
		if err := json.Unmarshal(b, &rest); err != nil {
			return rec, false, err
		}
		rec.Next, rec.NextRun, rec.Runs = rest.Next, rest.NextRun, rest.Runs
	}
	requests, err := readRequests(ctx, q, raw)
	if err != nil {
		return rec, false, err
	}
	if len(requests) > 0 {
		rec.Runs = requests
	}
	at := map[string]int{}
	rows, err := q.Query(ctx, `SELECT variable_change_id::text, change_number, cluster_id, fleet_key, fleet_name, requested_by, requested_by_user,
			requested_at, reason, changed, note, state, decided_by, decided_by_user, decided_at, comment
		FROM variable_changes ORDER BY change_number`)
	if err != nil {
		return rec, false, err
	}
	var id string
	var c VariableChange
	var asked, decided *time.Time
	var by, byUser *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &c.ID, &c.Cluster, &c.Fleet, &c.FleetName, &c.By.Name, &c.By.User, &asked, &c.Reason, &c.Changed,
		&c.Note, &c.State, &by, &byUser, &decided, &c.Comment}, func() error {
		x := c
		x.At, x.Impact = timeOf(asked), []ImpactItem{}
		// The row's own slice: the next row is read into the same variable.
		x.Changed = append([]string{}, c.Changed...)
		if by != nil {
			who := Who{Name: *by}
			if byUser != nil {
				who.User = *byUser
			}
			x.DecidedBy = &who
		}
		if decided != nil {
			t := decided.UTC()
			x.DecidedAt = &t
		}
		at[id] = len(rec.Changes)
		rec.Changes = append(rec.Changes, x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	var names func(cluster string) map[string]bool
	if !raw {
		if names, err = namesIn(ctx, q); err != nil {
			return rec, false, err
		}
	}
	items := map[string][]ChangeItem{}
	rows, err = q.Query(ctx, "SELECT variable_change_id::text, kind, node, name, coalesce(old_digest, old_value), coalesce(new_digest, new_value) FROM variable_change_items ORDER BY variable_change_id, position")
	if err != nil {
		return rec, false, err
	}
	var it ChangeItem
	if _, err := pgx.ForEachRow(rows, []any{&id, &it.Kind, &it.Node, &it.Name, &it.Old, &it.New}, func() error {
		x := it
		if i, ok := at[id]; ok && !raw && (x.Kind == ItemValue || x.Kind == ItemNodeValue) && names(rec.Changes[i].Cluster)[x.Name] {
			if x.Old != nil && !IsHidden(*x.Old) {
				x.Old = text(Unmoved)
			}
			if x.New != nil && !IsHidden(*x.New) {
				x.New = text(UnmovedNew)
			}
		}
		items[id] = append(items[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	for id, list := range items {
		if i, ok := at[id]; ok {
			rec.Changes[i].Before, rec.Changes[i].After = Sides(list)
		}
	}
	rows, err = q.Query(ctx, "SELECT variable_change_id::text, script, version_number, decision, why, decided_by FROM variable_change_impacts ORDER BY variable_change_id, position")
	if err != nil {
		return rec, false, err
	}
	var im ImpactItem
	if _, err := pgx.ForEachRow(rows, []any{&id, &im.Script, &im.Version, &im.Decision, &im.Why, &im.By}, func() error {
		if i, ok := at[id]; ok {
			rec.Changes[i].Impact = append(rec.Changes[i].Impact, im)
		}
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Changes) > 0 || len(rec.Runs) > 0, nil
}

// readRequests reads the runs that wait for approval, or did: the rows of
// script_runs that have a request's number, oldest first.
func readRequests(ctx context.Context, q db.Querier, raw bool) ([]WaitingRun, error) {
	return readRequestsWhere(ctx, q, raw, "")
}

// readRequestsWhere reads the requests that where picks, a condition on
// script_runs whose arguments are args; every request when where is "".
func readRequestsWhere(ctx context.Context, q db.Querier, raw bool, where string, args ...any) ([]WaitingRun, error) {
	var out []WaitingRun
	cond := ""
	if where != "" {
		cond = " AND (" + where + ")"
	}
	at := map[string]int{}
	rows, err := q.Query(ctx, `SELECT script_run_id::text, request_number, cluster_id, cluster_name, script_name, version_number, fleet_key, fleet_name,
			request_nodes, request_targets, rollout_mode, rollout_size, rollout_stop_after, rollout_timeout_minutes, coalesce(run_profile_id, ''),
			hooks_before, hooks_after, coalesce(retry_of_number, 0), request_client_key, requested_by, requested_by_user, person_user, person_groups,
			requested_at, request_method, request_changed, request_note, coalesce(request_state, ''), decided_by, decided_by_user, decided_at,
			decision_comment, coalesce(started_run_number, 0)
		FROM script_runs WHERE request_number IS NOT NULL`+cond+` ORDER BY request_number`, args...)
	if err != nil {
		return nil, err
	}
	var id string
	var w WaitingRun
	var nodes, targets, before, after, groups, changed []string
	var asked, decided *time.Time
	var by, byUser *string
	list := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return append([]string(nil), s...)
	}
	if _, err := pgx.ForEachRow(rows, []any{&id, &w.ID, &w.Cluster, &w.ClusterName, &w.Script, &w.Version, &w.Fleet, &w.FleetName, &nodes, &targets,
		&w.Rollout.Mode, &w.Rollout.Size, &w.Rollout.StopAfter, &w.Rollout.TimeoutMinutes, &w.Config, &before, &after, &w.RetryOf, &w.Request,
		&w.By.Name, &w.By.User, &w.Person.User, &groups, &asked, &w.Method, &changed, &w.Note, &w.State, &by, &byUser, &decided, &w.Comment, &w.Run}, func() error {
		x := w
		// The row's own slices: the next row is read into the same variables.
		x.Nodes, x.Targets, x.Hooks, x.Person.Groups = list(nodes), append([]string{}, targets...), HookRefs{Before: list(before), After: list(after)}, list(groups)
		x.Changed, x.Impact, x.Params, x.At = append([]string{}, changed...), []ImpactItem{}, map[string]string{}, timeOf(asked)
		x.DecidedBy, x.DecidedAt = nil, nil
		if by != nil {
			who := Who{Name: *by}
			if byUser != nil {
				who.User = *byUser
			}
			x.DecidedBy = &who
		}
		if decided != nil {
			t := decided.UTC()
			x.DecidedAt = &t
		}
		at[id] = len(out)
		out = append(out, x)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	only, of := "", []any{}
	if where != "" {
		ids := make([]string, 0, len(at))
		for id := range at {
			ids = append(ids, id)
		}
		only, of = " AND script_run_id = ANY($1::uuid[])", []any{ids}
	}
	var names func(cluster string) map[string]bool
	if !raw {
		if names, err = namesIn(ctx, q); err != nil {
			return nil, err
		}
	}
	rows, err = q.Query(ctx, "SELECT script_run_id::text, name, coalesce(value_digest, value) FROM script_run_values WHERE scope = 'request'"+only, of...)
	if err != nil {
		return nil, err
	}
	var name, value string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name, &value}, func() error {
		if i, ok := at[id]; ok {
			v := value
			if _, bound := Bound(v); !raw && !bound && !IsHidden(v) && names(out[i].Cluster)[name] {
				v = Unmoved
			}
			out[i].Params[name] = v
		}
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, "SELECT script_run_id::text, script, version_number, decision, why, decided_by FROM script_run_impacts WHERE true"+only+" ORDER BY script_run_id, position", of...)
	if err != nil {
		return nil, err
	}
	var im ImpactItem
	if _, err := pgx.ForEachRow(rows, []any{&id, &im.Script, &im.Version, &im.Decision, &im.Why, &im.By}, func() error {
		if i, ok := at[id]; ok {
			out[i].Impact = append(out[i].Impact, im)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// writeRequests writes the requests that changed. A request whose run has
// started shares its row: only the request's own columns are written then.
func writeRequests(ctx context.Context, tx pgx.Tx, old, next []WaitingRun) error {
	before := map[int]WaitingRun{}
	for _, w := range old {
		before[w.ID] = w
	}
	numbers := make([]int, 0, len(next))
	for _, w := range next {
		numbers = append(numbers, w.ID)
	}
	// A request that is no longer kept: its row goes when it never started
	// a run, and otherwise only what was the request's.
	if err := dropRequests(ctx, tx, `request_number IS NOT NULL AND NOT (request_number = ANY($1))`, numbers); err != nil {
		return err
	}
	for _, w := range next {
		if b, had := before[w.ID]; had && sameJSON(b, w) {
			continue
		}
		if err := writeRequest(ctx, tx, w); err != nil {
			return err
		}
	}
	return nil
}

// dropRequests removes the requests that gone picks, $1 its argument: a
// request's row goes when it never started a run, and otherwise only
// what was the request's.
func dropRequests(ctx context.Context, tx pgx.Tx, gone string, arg any) error {
	for _, q := range []string{
		"DELETE FROM script_run_values WHERE scope = 'request' AND script_run_id IN (SELECT script_run_id FROM script_runs WHERE " + gone + ")",
		"DELETE FROM script_run_impacts WHERE script_run_id IN (SELECT script_run_id FROM script_runs WHERE " + gone + ")",
		"DELETE FROM script_runs WHERE run_number IS NULL AND " + gone,
		`UPDATE script_runs SET request_number = NULL, request_state = NULL, request_client_key = '', requested_by = '', requested_by_user = '',
			requested_at = NULL, request_method = '', request_nodes = '{}', request_targets = '{}', person_user = '', person_groups = '{}',
			request_changed = '{}', request_note = '', decided_by = NULL, decided_by_user = NULL, decided_at = NULL, decision_comment = '',
			started_run_number = NULL WHERE ` + gone,
	} {
		if _, err := tx.Exec(ctx, q, arg); err != nil {
			return err
		}
	}
	return nil
}

// writeRequest writes one request's rows. A request whose run has started
// shares its row: only the request's own columns are written then.
func writeRequest(ctx context.Context, tx pgx.Tx, w WaitingRun) error {
	list := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	var id string
	err := tx.QueryRow(ctx, "SELECT script_run_id::text FROM script_runs WHERE request_number = $1", w.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// The run it started may be there already, without its request:
		// copied from a ConfigMap before the requests were.
		err = nil
		if w.Run != 0 {
			err = tx.QueryRow(ctx, "SELECT script_run_id::text FROM script_runs WHERE run_number = $1 AND request_number IS NULL AND cluster_id = $2", w.Run, w.Cluster).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			}
		}
		if err == nil && id == "" {
			id = db.NewID()
			_, err = tx.Exec(ctx, "INSERT INTO script_runs (script_run_id, request_number) VALUES ($1, $2)", id, w.ID)
		}
	}
	if err != nil {
		return err
	}
	var by, byUser *string
	if w.DecidedBy != nil {
		by, byUser = &w.DecidedBy.Name, &w.DecidedBy.User
	}
	var decided *time.Time
	if w.DecidedAt != nil {
		decided = timeOrNil(*w.DecidedAt)
	}
	var started *int
	if w.Run != 0 {
		started = &w.Run
	}
	if _, err := tx.Exec(ctx, `UPDATE script_runs SET request_number = $2, request_state = $3, request_client_key = $4, requested_by = $5,
			requested_by_user = $6, requested_at = $7, request_method = $8, request_nodes = $9, request_targets = $10, person_user = $11,
			person_groups = $12, request_changed = $13, request_note = $14, decided_by = $15, decided_by_user = $16, decided_at = $17,
			decision_comment = $18, started_run_number = $19 WHERE script_run_id = $1`,
		id, w.ID, w.State, w.Request, w.By.Name, w.By.User, timeOrNil(w.At), w.Method, list(w.Nodes), list(w.Targets), w.Person.User,
		list(w.Person.Groups), list(w.Changed), w.Note, by, byUser, decided, w.Comment, started); err != nil {
		return err
	}
	// What a request shares with its run is the run's once it has started.
	var config *string
	if w.Config != "" {
		config = &w.Config
	}
	var retry *int
	if w.RetryOf != 0 {
		retry = &w.RetryOf
	}
	if _, err := tx.Exec(ctx, `UPDATE script_runs SET state = $2, cluster_id = $3, cluster_name = $4, script_name = $5, version_number = $6,
			fleet_key = $7, fleet_name = $8, rollout_mode = $9, rollout_size = $10, rollout_stop_after = $11, rollout_timeout_minutes = $12,
			run_profile_id = $13, hooks_before = $14, hooks_after = $15, retry_of_number = $16,
			script_version_id = (SELECT v.script_version_id FROM script_versions v JOIN scripts s USING (script_id) WHERE s.name = $5 AND v.version_number = $6),
			fleet_id = (SELECT f.fleet_id FROM fleets f WHERE coalesce(f.cluster_id, '') = $3 AND f.fleet_key = $7)
		WHERE script_run_id = $1 AND run_number IS NULL`,
		id, w.State, w.Cluster, w.ClusterName, w.Script, w.Version, w.Fleet, w.FleetName, w.Rollout.Mode, w.Rollout.Size, w.Rollout.StopAfter,
		w.Rollout.TimeoutMinutes, config, list(w.Hooks.Before), list(w.Hooks.After), retry); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM script_run_values WHERE script_run_id = $1 AND scope = 'request'", id); err != nil {
		return err
	}
	for name, v := range w.Params {
		value, digest := columnsOf(v)
		if _, err := tx.Exec(ctx, "INSERT INTO script_run_values (script_run_value_id, script_run_id, scope, name, value, value_digest) VALUES ($1, $2, 'request', $3, $4, $5)", db.NewID(), id, name, value, digest); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM script_run_impacts WHERE script_run_id = $1", id); err != nil {
		return err
	}
	for i, im := range w.Impact {
		if _, err := tx.Exec(ctx, `INSERT INTO script_run_impacts (script_run_id, position, script, version_number, decision, why, decided_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, i, im.Script, im.Version, im.Decision, im.Why, im.By); err != nil {
			return err
		}
	}
	return nil
}

// columnsOf is a value of a run as its row keeps it: a digest in the
// digest's column, with no value beside it.
func columnsOf(v string) (value string, digest *string) {
	if IsHidden(v) {
		return "", &v
	}
	return v, nil
}

// writeChanges hides the sensitive values the changes and the waiting
// runs hold (sealChanges), writes their rows, has the Secret keep what a
// waiting one would use, and leaves the commit to the caller: a Secret
// that cannot be written leaves the rows as they were.
func writeChanges(ctx context.Context, tx pgx.Tx, sens SensitiveStore, old, next changesRecord) error {
	names, err := namesIn(ctx, tx)
	if err != nil {
		return err
	}
	sealed, err := sealChanges(newRing(ctx, sens), &next, names, nil)
	if err != nil {
		return err
	}
	if err := writeChangesRows(ctx, tx, old, next); err != nil {
		return err
	}
	return sealed.put(ctx, sens)
}

func writeChangesRows(ctx context.Context, tx pgx.Tx, old, next changesRecord) error {
	// The counters. The scope is written again, without the requests, when
	// it still has them from a build that kept them there.
	var inScope changesRest
	if b, _, err := db.LoadScope(ctx, tx, changesScope); err == nil {
		_ = json.Unmarshal(b, &inScope)
	} else if !errors.Is(err, db.ErrNotFound) {
		return err
	}
	if next.Next != old.Next || next.NextRun != old.NextRun || len(inScope.Runs) > 0 {
		b, err := json.Marshal(changesRest{Next: next.Next, NextRun: next.NextRun})
		if err != nil {
			return err
		}
		if _, err := db.SaveScope(ctx, tx, changesScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	asRows := old.Runs
	if len(inScope.Runs) > 0 {
		// Not rows yet: all of them are written.
		asRows = nil
	}
	if err := writeRequests(ctx, tx, asRows, next.Runs); err != nil {
		return err
	}
	// What is kept of a change in the database: what it alters, not the
	// fleet's variables whole, which a change copied from a ConfigMap has.
	kept := func(c VariableChange) VariableChange {
		c.Before, c.After = Sides(Items(c.Before, c.After))
		c.Overtaken = ""
		return c
	}
	put := func(c VariableChange) error {
		var by, byUser *string
		if c.DecidedBy != nil {
			by, byUser = &c.DecidedBy.Name, &c.DecidedBy.User
		}
		var decided *time.Time
		if c.DecidedAt != nil {
			decided = timeOrNil(*c.DecidedAt)
		}
		changed := c.Changed
		if changed == nil {
			changed = []string{}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO variable_changes (variable_change_id, change_number, cluster_id, fleet_key, fleet_name, requested_by,
				requested_by_user, requested_at, reason, changed, note, state, decided_by, decided_by_user, decided_at, comment)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			ON CONFLICT (change_number) DO UPDATE SET cluster_id = excluded.cluster_id, fleet_key = excluded.fleet_key, fleet_name = excluded.fleet_name,
				requested_by = excluded.requested_by, requested_by_user = excluded.requested_by_user, requested_at = excluded.requested_at,
				reason = excluded.reason, changed = excluded.changed, note = excluded.note, state = excluded.state, decided_by = excluded.decided_by,
				decided_by_user = excluded.decided_by_user, decided_at = excluded.decided_at, comment = excluded.comment
			RETURNING variable_change_id::text`,
			db.NewID(), c.ID, c.Cluster, c.Fleet, c.FleetName, c.By.Name, c.By.User, timeOrNil(c.At), c.Reason, changed, c.Note, c.State,
			by, byUser, decided, c.Comment).Scan(&id); err != nil {
			return err
		}
		for _, table := range []string{"variable_change_items", "variable_change_impacts"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE variable_change_id = $1", id); err != nil {
				return err
			}
		}
		for i, it := range Items(c.Before, c.After) {
			// A digest goes into the digest's column, and no value beside it.
			var oldDigest, newDigest *string
			if it.Old != nil && IsHidden(*it.Old) {
				it.Old, oldDigest = nil, it.Old
			}
			if it.New != nil && IsHidden(*it.New) {
				it.New, newDigest = nil, it.New
			}
			if _, err := tx.Exec(ctx, `INSERT INTO variable_change_items (variable_change_id, position, kind, node, name, old_value, new_value, old_digest, new_digest)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, i, it.Kind, it.Node, it.Name, it.Old, it.New, oldDigest, newDigest); err != nil {
				return err
			}
		}
		for i, im := range c.Impact {
			if _, err := tx.Exec(ctx, `INSERT INTO variable_change_impacts (variable_change_id, position, script, version_number, decision, why, decided_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, i, im.Script, im.Version, im.Decision, im.Why, im.By); err != nil {
				return err
			}
		}
		return nil
	}
	before := map[int]VariableChange{}
	for _, c := range old.Changes {
		before[c.ID] = c
	}
	numbers := make([]int, 0, len(next.Changes))
	for _, c := range next.Changes {
		numbers = append(numbers, c.ID)
		if b, had := before[c.ID]; had && sameJSON(kept(b), kept(c)) {
			continue
		}
		if err := put(c); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "DELETE FROM variable_changes WHERE NOT (change_number = ANY($1))", numbers)
	return err
}
