package fixes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The fixes in the database (Phase 13, DB-47): a fix is a row of fixes,
// known by the id the console gives it, every field a column, and the
// cases it was seen in rows of fix_cases. The service is unchanged and
// reaches the rows through db.Doc; the counter the ids come from, which
// is of no row, is a settings scope.

// fixesScope is the settings row with the next fix's number.
const fixesScope = "fixes"

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// A list as a column that is never null, and back: no elements is no
// list, as the document's JSON leaves an empty one out.
func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func listOf(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

func at(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

// NewPG keeps the fixes in d.
func NewPG(d *db.DB) *db.Doc[record] {
	return &db.Doc[record]{DB: d, Key: Key, Read: readFixes, Write: writeFixes}
}

func readFixes(ctx context.Context, q db.Querier) (record, bool, error) {
	var rec record
	var n struct {
		Next int `json:"next"`
	}
	b, _, err := db.LoadScope(ctx, q, fixesScope)
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
	seen := map[string][]Seen{}
	rows, err := q.Query(ctx, "SELECT fix_id::text, case_key, seen_at, cluster_id, seen_by FROM fix_cases ORDER BY fix_id, position")
	if err != nil {
		return rec, false, err
	}
	var id string
	var s Seen
	var when time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &s.Case, &when, &s.Cluster, &s.By}, func() error {
		x := s
		x.At = when.UTC()
		seen[id] = append(seen[id], x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	rows, err = q.Query(ctx, `SELECT fix_id::text, fix_key, title, problem_key, cluster_id, resource, symptoms, cause, steps, commands, prevention, status, reason,
			duplicate_of_fix_key, contradicts, accepted, created_at, updated_at, created_by, updated_by
		FROM fixes ORDER BY position`)
	if err != nil {
		return rec, false, err
	}
	var f Fix
	var commands, contradicts, accepted []string
	var created, updated time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &f.ID, &f.Title, &f.Key, &f.Cluster, &f.Resource, &f.Symptoms, &f.Cause, &f.Steps, &commands, &f.Prevention, &f.Status, &f.Reason,
		&f.DuplicateOf, &contradicts, &accepted, &created, &updated, &f.CreatedBy, &f.UpdatedBy}, func() error {
		x := f
		x.Commands, x.Contradicts, x.Accepted, x.Cases, x.Created, x.Updated = listOf(commands), listOf(contradicts), listOf(accepted), seen[id], created.UTC(), updated.UTC()
		rec.Items = append(rec.Items, x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Items) > 0, nil
}

func writeFixes(ctx context.Context, tx pgx.Tx, old, next record) error {
	if old.Next != next.Next {
		v, _ := json.Marshal(struct {
			Next int `json:"next"`
		}{next.Next})
		if _, err := db.SaveScope(ctx, tx, fixesScope, v, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	placed, err := db.Placed(ctx, tx, "fixes", "fix_key")
	if err != nil {
		return err
	}
	before := map[string]Fix{}
	for _, f := range old.Items {
		before[f.ID] = f
	}
	kept := make([]string, len(next.Items))
	for i, f := range next.Items {
		kept[i] = f.ID
	}
	// A fix comes in at the end, which leaves the others' places alone.
	places := db.Places(kept, placed)
	b := &pgx.Batch{}
	for i, f := range next.Items {
		was, had := before[f.ID]
		if had && sameJSON(was, f) {
			if placed[f.ID] != places[i] {
				b.Queue("UPDATE fixes SET position = $2 WHERE fix_key = $1", f.ID, places[i])
			}
			continue
		}
		// The row keeps its id; the cases it was seen in are written again.
		b.Queue(`INSERT INTO fixes (fix_id, fix_key, position, title, problem_key, cluster_id, resource, symptoms, cause, steps, commands, prevention, status, reason,
				duplicate_of_fix_key, contradicts, accepted, created_at, updated_at, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
			ON CONFLICT (fix_key) DO UPDATE SET position = excluded.position, title = excluded.title, problem_key = excluded.problem_key, cluster_id = excluded.cluster_id,
				resource = excluded.resource, symptoms = excluded.symptoms, cause = excluded.cause, steps = excluded.steps, commands = excluded.commands,
				prevention = excluded.prevention, status = excluded.status, reason = excluded.reason, duplicate_of_fix_key = excluded.duplicate_of_fix_key,
				contradicts = excluded.contradicts, accepted = excluded.accepted, created_at = excluded.created_at, updated_at = excluded.updated_at,
				created_by = excluded.created_by, updated_by = excluded.updated_by`,
			db.NewID(), f.ID, places[i], f.Title, f.Key, f.Cluster, f.Resource, f.Symptoms, f.Cause, f.Steps, list(f.Commands), f.Prevention, f.Status, f.Reason,
			f.DuplicateOf, list(f.Contradicts), list(f.Accepted), at(f.Created), at(f.Updated), f.CreatedBy, f.UpdatedBy)
		if had && sameJSON(was.Cases, f.Cases) {
			continue
		}
		b.Queue("DELETE FROM fix_cases WHERE fix_id = (SELECT fix_id FROM fixes WHERE fix_key = $1)", f.ID)
		for n, s := range f.Cases {
			b.Queue("INSERT INTO fix_cases (fix_id, position, case_key, seen_at, cluster_id, seen_by) VALUES ((SELECT fix_id FROM fixes WHERE fix_key = $1), $2, $3, $4, $5, $6)",
				f.ID, n, s.Case, at(s.At), s.Cluster, s.By)
		}
	}
	b.Queue("DELETE FROM fixes WHERE NOT (fix_key = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}
