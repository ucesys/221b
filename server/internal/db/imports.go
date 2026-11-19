package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The move from ConfigMaps (DB-17): each record set is copied into the
// database once, by whichever replica gets there first, and the copy is
// written down in the same transaction. From then on the ConfigMap's key
// is not read again; it stays for one release, as it was at the copy, for
// a rollback.

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

// Imported says whether the record set named id was copied.
func (d *DB) Imported(ctx context.Context, id string) (bool, error) {
	var one int
	err := d.pool.QueryRow(ctx, "SELECT 1 FROM imports WHERE import_id = $1", id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, d.note(err)
}
