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
// They belong with the runs, which have not moved: until they do, they are
// kept as they are, with the two counters, in the settings scope below.

const changesScope = "automation-changes"

// changesRest is what the scope holds: the counters, and for now the runs
// that wait.
type changesRest struct {
	Next    int          `json:"next"`
	NextRun int          `json:"nextRun,omitempty"`
	Runs    []WaitingRun `json:"runs,omitempty"`
}

// NewChangesPG keeps the variable changes in d.
func NewChangesPG(d *db.DB) *db.Doc[changesRecord] {
	return &db.Doc[changesRecord]{DB: d, Key: ChangesKey, Read: readChanges, Write: writeChanges}
}

func readChanges(ctx context.Context, q db.Querier) (changesRecord, bool, error) {
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
	items := map[string][]ChangeItem{}
	rows, err = q.Query(ctx, "SELECT variable_change_id::text, kind, node, name, old_value, new_value FROM variable_change_items ORDER BY variable_change_id, position")
	if err != nil {
		return rec, false, err
	}
	var it ChangeItem
	if _, err := pgx.ForEachRow(rows, []any{&id, &it.Kind, &it.Node, &it.Name, &it.Old, &it.New}, func() error {
		items[id] = append(items[id], it)
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
	return rec, found || len(rec.Changes) > 0, nil
}

func writeChanges(ctx context.Context, tx pgx.Tx, old, next changesRecord) error {
	rest := func(r changesRecord) changesRest { return changesRest{Next: r.Next, NextRun: r.NextRun, Runs: r.Runs} }
	if !sameJSON(rest(old), rest(next)) {
		b, err := json.Marshal(rest(next))
		if err != nil {
			return err
		}
		if _, err := db.SaveScope(ctx, tx, changesScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
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
			if _, err := tx.Exec(ctx, `INSERT INTO variable_change_items (variable_change_id, position, kind, node, name, old_value, new_value)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, i, it.Kind, it.Node, it.Name, it.Old, it.New); err != nil {
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
