package lessons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The accepted exceptions in the database (Phase 13, DB-47): an exception
// is a row of exceptions, known by the id the console gives it, every
// field a column. The service is unchanged and reaches the rows through
// db.Doc; the counter the ids come from, which is of no row, is a
// settings scope. An exception that matched again is updated in its row,
// which keeps its id.

// exceptionsScope is the settings row with the next exception's number.
const exceptionsScope = "exceptions"

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func at(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func atOrNone(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return at(t)
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// NewExceptionsPG keeps the accepted exceptions in d.
func NewExceptionsPG(d *db.DB) *db.Doc[exceptionsRecord] {
	return &db.Doc[exceptionsRecord]{DB: d, Key: ExceptionsKey, Read: readExceptions, Write: writeExceptions}
}

func readExceptions(ctx context.Context, q db.Querier) (exceptionsRecord, bool, error) {
	var rec exceptionsRecord
	var n struct {
		Next int `json:"next"`
	}
	b, _, err := db.LoadScope(ctx, q, exceptionsScope)
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
	rows, err := q.Query(ctx, `SELECT exception_key, cluster_id, resource, check_name, title, detail, finding_key, run_key, job_id, reason, created_by, created_at, expires_at,
			removed_by, removed_at, wiki_path, wiki_error, case_key, match_count, last_matched_at, last_run_key, updated_at, duplicate_of_exception_key
		FROM exceptions ORDER BY position DESC`)
	if err != nil {
		return rec, false, err
	}
	var e Exception
	var created time.Time
	var expires, removed, matched, updated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&e.ID, &e.Cluster, &e.Resource, &e.Check, &e.Title, &e.Detail, &e.Finding, &e.Run, &e.Job, &e.Reason, &e.By, &created, &expires,
		&e.RemovedBy, &removed, &e.Wiki, &e.WikiError, &e.Case, &e.Matches, &matched, &e.LastRun, &updated, &e.DuplicateOf}, func() error {
		x := e
		x.At, x.Expires, x.RemovedAt, x.LastMatched, x.Updated = created.UTC(), timeOf(expires), timeOf(removed), timeOf(matched), timeOf(updated)
		rec.Items = append(rec.Items, x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Items) > 0, nil
}

func writeExceptions(ctx context.Context, tx pgx.Tx, old, next exceptionsRecord) error {
	if old.Next != next.Next {
		v, _ := json.Marshal(struct {
			Next int `json:"next"`
		}{next.Next})
		if _, err := db.SaveScope(ctx, tx, exceptionsScope, v, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	placed, err := db.Placed(ctx, tx, "exceptions", "exception_key")
	if err != nil {
		return err
	}
	before := map[string]Exception{}
	for _, e := range old.Items {
		before[e.ID] = e
	}
	// The newest exception is first in the list and has the greatest
	// position: one comes in at the front, which leaves the others' places
	// alone. So the places are worked out from the list's end.
	n := len(next.Items)
	kept := make([]string, n)
	for i, e := range next.Items {
		kept[n-1-i] = e.ID
	}
	fromEnd := db.Places(kept, placed)
	b := &pgx.Batch{}
	for i, e := range next.Items {
		place := fromEnd[n-1-i]
		if was, had := before[e.ID]; had && sameJSON(was, e) {
			if placed[e.ID] != place {
				b.Queue("UPDATE exceptions SET position = $2 WHERE exception_key = $1", e.ID, place)
			}
			continue
		}
		b.Queue(`INSERT INTO exceptions (exception_id, exception_key, position, cluster_id, resource, check_name, title, detail, finding_key, run_key, job_id, reason, created_by,
				created_at, expires_at, removed_by, removed_at, wiki_path, wiki_error, case_key, match_count, last_matched_at, last_run_key, updated_at, duplicate_of_exception_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
			ON CONFLICT (exception_key) DO UPDATE SET position = excluded.position, cluster_id = excluded.cluster_id, resource = excluded.resource,
				check_name = excluded.check_name, title = excluded.title, detail = excluded.detail, finding_key = excluded.finding_key, run_key = excluded.run_key,
				job_id = excluded.job_id, reason = excluded.reason, created_by = excluded.created_by, created_at = excluded.created_at, expires_at = excluded.expires_at,
				removed_by = excluded.removed_by, removed_at = excluded.removed_at, wiki_path = excluded.wiki_path, wiki_error = excluded.wiki_error,
				case_key = excluded.case_key, match_count = excluded.match_count, last_matched_at = excluded.last_matched_at, last_run_key = excluded.last_run_key,
				updated_at = excluded.updated_at, duplicate_of_exception_key = excluded.duplicate_of_exception_key`,
			db.NewID(), e.ID, place, e.Cluster, e.Resource, e.Check, e.Title, e.Detail, e.Finding, e.Run, e.Job, e.Reason, e.By,
			at(e.At), atOrNone(e.Expires), e.RemovedBy, atOrNone(e.RemovedAt), e.Wiki, e.WikiError, e.Case, e.Matches, atOrNone(e.LastMatched), e.LastRun, atOrNone(e.Updated), e.DuplicateOf)
	}
	b.Queue("DELETE FROM exceptions WHERE NOT (exception_key = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}
