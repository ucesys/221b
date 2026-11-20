package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The move from ConfigMaps (DB-17): each record set is copied into the
// database once, by whichever replica gets there first, and the copy is
// written down in the same transaction. From then on the ConfigMap's key
// is not read again; it stays for one release, as it was at the copy, for
// a rollback.
//
// A record set that only grows is the exception (ImportNewer): its key is
// read at every start, and what is newer than the last copy comes over. So
// a console that was rolled back to a release without a database, and then
// brought forward again, keeps the audit events and the session records of
// the time in between. A record set that is edited in place cannot be
// merged that way: there the database's is the one that counts.

// ImportOnce runs copy in a transaction unless the record set named id was
// copied before, and says whether it ran. copy returns how many records it
// wrote; an error rolls everything back, and the next start tries again.
// The replicas take turns behind a lock, so two never copy the same set.
func (d *DB) ImportOnce(ctx context.Context, id, source string, copy func(ctx context.Context, tx pgx.Tx) (int, error)) (bool, error) {
	ran := false
	_, err := d.Sweep(ctx, "import/"+id, func(ctx context.Context) error {
		return d.Tx(ctx, func(tx pgx.Tx) error {
			var one int
			err := tx.QueryRow(ctx, "SELECT 1 FROM imports WHERE import_id = $1", id).Scan(&one)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			n, err := copy(ctx, tx)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO imports (import_id, source, record_count) VALUES ($1, $2, $3)", id, source, n); err != nil {
				return err
			}
			ran = true
			return nil
		})
	})
	return ran, err
}

// ImportNewer is ImportOnce for a record set that only grows and whose
// records carry a time. copy is given the time of the newest record a copy
// before this one took, the zero time when there was none, and returns how
// many records it wrote and the time of the newest it saw; it compares with
// After, since the database keeps microseconds. It runs at every start, and
// ImportNewer says how many records this run copied.
func (d *DB) ImportNewer(ctx context.Context, id, source string, copy func(ctx context.Context, tx pgx.Tx, since time.Time) (int, time.Time, error)) (int, error) {
	copied := 0
	_, err := d.Sweep(ctx, "import/"+id, func(ctx context.Context) error {
		return d.Tx(ctx, func(tx pgx.Tx) error {
			var since time.Time
			// A copy that found nothing, or one from before this column, has
			// no newest record: what was written after it is newer than it.
			err := tx.QueryRow(ctx, "SELECT COALESCE(newest_at, imported_at) FROM imports WHERE import_id = $1 FOR UPDATE", id).Scan(&since)
			first := errors.Is(err, pgx.ErrNoRows)
			if err != nil && !first {
				return err
			}
			n, newest, err := copy(ctx, tx, since)
			if err != nil {
				return err
			}
			var at *time.Time
			if !newest.IsZero() {
				t := newest.UTC()
				at = &t
			}
			if first {
				_, err = tx.Exec(ctx, "INSERT INTO imports (import_id, source, record_count, newest_at) VALUES ($1, $2, $3, $4)", id, source, n, at)
			} else if n > 0 {
				_, err = tx.Exec(ctx, `UPDATE imports SET record_count = record_count + $2,
					newest_at = GREATEST(COALESCE(newest_at, imported_at), COALESCE($3, imported_at)) WHERE import_id = $1`, id, n, at)
			}
			copied = n
			return err
		})
	})
	return copied, err
}

// After says whether t is later than since, a time read back from the
// database, which keeps microseconds.
func After(t, since time.Time) bool {
	return t.UTC().Truncate(time.Microsecond).After(since)
}

// Imported says whether the record set named id was copied.
func (d *DB) Imported(ctx context.Context, id string) (bool, error) {
	if err := d.gate(); err != nil {
		return false, err
	}
	var one int
	err := d.pool.QueryRow(ctx, "SELECT 1 FROM imports WHERE import_id = $1", id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, d.note(err)
}
