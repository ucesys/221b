package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The schema, one file per step, applied in the order of their numbers
// (DB-07). A file that has shipped in a release is never edited; a change
// is a new file. Within a release line the steps only add, so the replicas
// of the release before go on working while the new ones roll out
// (docs/upgrading.md).
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLock is the advisory lock the replicas take turns behind to
// migrate: "221B" and "schema" as two 32-bit halves.
const migrationLock int64 = 0x32323142_5343484d

// Migration is one step of the schema.
type Migration struct {
	// ID is the file's number, Name the rest of its name.
	ID   int
	Name string
	SQL  string
	// Checksum is the SHA-256 of the file, kept with the step once applied
	// so an edited file is noticed.
	Checksum string
}

// Migrations are the steps this build knows, in order.
func Migrations() ([]Migration, error) {
	return loadMigrations(migrationFiles, "migrations")
}

func loadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		num, name, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		id, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("migration %s: name it NNNN_what.sql", e.Name())
		}
		b, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{ID: id, Name: name, SQL: string(b), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i, m := range out {
		if m.ID != i+1 {
			return nil, fmt.Errorf("migration %04d_%s: the numbers must run from 1 without gaps", m.ID, m.Name)
		}
	}
	return out, nil
}

// Migrate brings the schema to this build's. Every replica calls it at
// start; one of them does the work while the others wait on the lock, then
// find nothing left to do. It returns how many steps it applied.
//
// A database whose schema is newer than this build's is left alone and is
// not an error: during a rollout the older replicas run against the newer
// schema, which only added.
func (d *DB) Migrate(ctx context.Context) (int, error) {
	if d.without {
		return 0, refused()
	}
	steps, err := Migrations()
	if err != nil {
		return 0, err
	}
	n, err := d.migrate(ctx, steps)
	return n, d.note(err)
}

func (d *DB) migrate(ctx context.Context, steps []Migration) (int, error) {
	// The lock belongs to a session, so the whole migration runs on one
	// connection, a direct one.
	conn, err := d.direct.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return 0, fmt.Errorf("waiting for the schema's lock: %w", err)
	}
	defer func() {
		// Without ctx: the lock is given back also when ctx ended.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		schema_migration_id integer PRIMARY KEY,
		name text NOT NULL,
		checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return 0, fmt.Errorf("making the list of schema steps: %w", err)
	}
	applied := map[int]string{}
	rows, err := conn.Query(ctx, "SELECT schema_migration_id, checksum FROM schema_migrations")
	if err != nil {
		return 0, err
	}
	var id int
	var sum string
	if _, err := pgx.ForEachRow(rows, []any{&id, &sum}, func() error { applied[id] = sum; return nil }); err != nil {
		return 0, err
	}
	done := 0
	for _, m := range steps {
		if sum, ok := applied[m.ID]; ok {
			if sum != m.Checksum {
				return done, fmt.Errorf("schema step %04d_%s was applied to this database from a different file than this build's (applied %.12s, this build %.12s). A step is never edited once a database has applied it, so this database was made by another build of the schema: a development build's database is made again, empty; one made by a release is a fault to report", m.ID, m.Name, sum, m.Checksum)
			}
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return done, err
		}
		// A step is several statements, which only the simple protocol
		// takes in one call.
		if _, err := tx.Conn().PgConn().Exec(ctx, m.SQL).ReadAll(); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return done, fmt.Errorf("schema step %04d_%s: %w", m.ID, m.Name, describe(err))
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (schema_migration_id, name, checksum) VALUES ($1, $2, $3)", m.ID, m.Name, m.Checksum); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return done, err
		}
		if err := tx.Commit(ctx); err != nil {
			return done, fmt.Errorf("schema step %04d_%s: %w", m.ID, m.Name, describe(err))
		}
		d.log.Info("applied a schema step", "step", fmt.Sprintf("%04d_%s", m.ID, m.Name))
		done++
	}
	return done, nil
}

// SchemaVersion is the newest step the database has, 0 before the first.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.pool.QueryRow(ctx, "SELECT coalesce(max(schema_migration_id), 0) FROM schema_migrations").Scan(&v)
	return v, d.note(err)
}
