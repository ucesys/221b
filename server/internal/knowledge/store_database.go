package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- The wiki's store in the database (DB-96) ---
//
// Decided by the owner on November 21, 2026: while Administration › AI ›
// Knowledge names no Git repository or S3 bucket, the wiki is kept in the
// console's database, every file a row of wiki_files and a run's page
// changes that wait for review rows of wiki_stages and wiki_staged_files
// (step 0050). It replaces the temporary ConfigMap store (CF-57), whose
// ConfigMap <release>-wiki the first start copies in once (Import) and the
// console then deletes with the other ConfigMaps of before (DB-72).

// DefaultWikiStoreBytes is the most the database store holds, the approved
// files and the staged changes together: 256 MiB, room for tens of
// thousands of pages and the sources' excerpts. A change that would pass it
// is refused whole; one that makes the store smaller always goes through.
const DefaultWikiStoreBytes = 256 << 20

// ErrStoreFull is a write the wiki's store refused for want of room.
var ErrStoreFull = errors.New("the wiki's store in the database is full")

// WikiImportID is the name the copy of the ConfigMap store is recorded
// under in imports.
const WikiImportID = "wiki-store"

// WikiKey is the one key of the ConfigMap <release>-wiki a release before
// 0.13 kept the wiki in (CF-57): every file of the wiki and the staged
// runs, in one JSON document. Only the one-time copy reads it.
const WikiKey = "wiki.json"

// DBStore keeps the wiki in the console's database. Every replica reads
// the same rows; a write takes one lock for the whole store, so a version
// checked is the version written over.
type DBStore struct {
	d     *db.DB
	limit int64
}

// NewDBStore keeps the wiki in d, in at most limit bytes (0: the
// default).
func NewDBStore(d *db.DB, limit int64) *DBStore {
	if limit <= 0 {
		limit = DefaultWikiStoreBytes
	}
	return &DBStore{d: d, limit: limit}
}

func (s *DBStore) Kind() string { return StoreDatabase }

// Limit is the most the store holds, in bytes.
func (s *DBStore) Limit() int64 { return s.limit }

const usedSQL = `SELECT (SELECT coalesce(sum(octet_length(path) + octet_length(body)), 0) FROM wiki_files)
	+ (SELECT coalesce(sum(octet_length(path) + octet_length(body)), 0) FROM wiki_staged_files)`

// Used is how many bytes the store holds now, of its limit (KN-37).
func (s *DBStore) Used(ctx context.Context) (int64, error) {
	var n int64
	err := s.d.Do(ctx, func(q db.Querier) error { return q.QueryRow(ctx, usedSQL).Scan(&n) })
	return n, err
}

// edit runs fn in a transaction that holds the store's lock, and refuses
// it whole when it leaves the store past its room and larger than before.
func (s *DBStore) edit(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return s.d.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('wiki-store'))"); err != nil {
			return err
		}
		var before, after int64
		if err := tx.QueryRow(ctx, usedSQL).Scan(&before); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, usedSQL).Scan(&after); err != nil {
			return err
		}
		if after > s.limit && after > before {
			return fmt.Errorf("%w (%d MiB): delete pages or uploads no longer needed, or move the wiki to Git or S3 in Administration › AI › Knowledge", ErrStoreFull, s.limit>>20)
		}
		return nil
	})
}

func (s *DBStore) Pull(ctx context.Context) (map[string]File, error) {
	out := map[string]File{}
	err := s.d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT path, body, version FROM wiki_files")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f File
			if err := rows.Scan(&f.Path, &f.Body, &f.Version); err != nil {
				return err
			}
			if wanted(f.Path) {
				out[f.Path] = f
			}
		}
		return rows.Err()
	})
	return out, err
}

// put writes one approved file with a new version.
func put(ctx context.Context, tx pgx.Tx, path string, body []byte) error {
	return putAt(ctx, tx, path, body, db.NewID())
}

// putAt writes one approved file at the version given.
func putAt(ctx context.Context, tx pgx.Tx, path string, body []byte, version string) error {
	if body == nil {
		body = []byte{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO wiki_files (wiki_file_id, path, body, version, updated_at) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (path) DO UPDATE SET body = excluded.body, version = excluded.version, updated_at = excluded.updated_at`, db.NewID(), path, body, version)
	return err
}

func (s *DBStore) Write(ctx context.Context, files []Change, _ string) error {
	return s.edit(ctx, func(tx pgx.Tx) error {
		for _, f := range files {
			if f.Delete {
				if _, err := tx.Exec(ctx, "DELETE FROM wiki_files WHERE path = $1", f.Path); err != nil {
					return err
				}
				continue
			}
			if err := put(ctx, tx, f.Path, f.Body); err != nil {
				return err
			}
		}
		return nil
	})
}

// stage keeps a run's page changes, in place of any staged before.
func stage(ctx context.Context, tx pgx.Tx, run string, bodies map[string][]byte) error {
	if _, err := tx.Exec(ctx, "DELETE FROM wiki_stages WHERE run_key = $1", run); err != nil {
		return err
	}
	id := db.NewID()
	if _, err := tx.Exec(ctx, "INSERT INTO wiki_stages (wiki_stage_id, run_key, staged_at) VALUES ($1, $2, now())", id, run); err != nil {
		return err
	}
	for path, body := range bodies {
		if body == nil {
			body = []byte{}
		}
		if _, err := tx.Exec(ctx, "INSERT INTO wiki_staged_files (wiki_stage_id, path, body) VALUES ($1, $2, $3)", id, path, body); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBStore) Stage(ctx context.Context, run string, changes []Change, _ string) error {
	bodies := map[string][]byte{}
	for _, ch := range changes {
		bodies[ch.Path] = ch.Body
	}
	return s.edit(ctx, func(tx pgx.Tx) error { return stage(ctx, tx, run, bodies) })
}

func (s *DBStore) Publish(ctx context.Context, run string, changes []Change, _ string) ([]string, error) {
	var conflicts []string
	err := s.edit(ctx, func(tx pgx.Tx) error {
		conflicts = nil
		var stageID string
		err := tx.QueryRow(ctx, "SELECT wiki_stage_id::text FROM wiki_stages WHERE run_key = $1", run).Scan(&stageID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: nothing is staged for %s", ErrNotFound, run)
		}
		if err != nil {
			return err
		}
		for _, ch := range changes {
			var version string
			err := tx.QueryRow(ctx, "SELECT version FROM wiki_files WHERE path = $1", ch.Path).Scan(&version)
			found := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if (found && version != ch.Base) || (!found && ch.Base != "") {
				conflicts = append(conflicts, ch.Path)
				continue
			}
			var body []byte
			err = tx.QueryRow(ctx, "DELETE FROM wiki_staged_files WHERE wiki_stage_id = $1 AND path = $2 RETURNING body", stageID, ch.Path).Scan(&body)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s is not staged for run %s", ErrNotFound, ch.Path, run)
			}
			if err != nil {
				return err
			}
			if err := put(ctx, tx, ch.Path, body); err != nil {
				return err
			}
		}
		// A run all of whose changes are published is staged no more.
		_, err = tx.Exec(ctx, "DELETE FROM wiki_stages WHERE wiki_stage_id = $1 AND NOT EXISTS (SELECT 1 FROM wiki_staged_files WHERE wiki_stage_id = $1)", stageID)
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func (s *DBStore) Discard(ctx context.Context, run string) error {
	return s.d.Do(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, "DELETE FROM wiki_stages WHERE run_key = $1", run)
		return err
	})
}

func (s *DBStore) Check(ctx context.Context) (StoreCheck, error) {
	files, err := s.Pull(ctx)
	if err != nil {
		return StoreCheck{}, err
	}
	out := StoreCheck{Files: len(files)}
	if used, err := s.Used(ctx); err == nil && used*10 > s.limit*9 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("The wiki's store in the database is nearly full: %d MiB of its %d MiB used. Delete pages or uploads no longer needed, or move the wiki to Git or S3.", (used+(1<<20)-1)>>20, s.limit>>20))
	}
	return out, nil
}

// cmWiki is the document the ConfigMap store of before kept under
// WikiKey.
type cmWiki struct {
	Files map[string]struct {
		Body    string `json:"body"`
		Version string `json:"version"`
	} `json:"files"`
	// Staged are the runs' page changes waiting for review, by run and
	// path.
	Staged map[string]map[string]string `json:"staged,omitempty"`
}

// Import copies the wiki the ConfigMap store of a release before 0.13
// kept under WikiKey into the database, once, recorded as WikiImportID:
// every file, at the version it had there (so a run staged over it
// publishes as it would have; a file with none gets a new one), and every
// staged run. It says how many files and runs it copied, and whether it ran (not
// when the copy is recorded already). The room is not checked: what the
// ConfigMap held fits.
func (s *DBStore) Import(ctx context.Context, from kv.Backend, source string) (int, bool, error) {
	raw, _, err := from.LoadKey(ctx, WikiKey)
	if errors.Is(err, kv.ErrNotFound) {
		raw, err = nil, nil
	}
	if err != nil {
		return 0, false, err
	}
	var w cmWiki
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &w); err != nil {
			return 0, false, fmt.Errorf("the ConfigMap wiki store cannot be read: %w", err)
		}
	}
	n := 0
	ran, err := s.d.ImportOnce(ctx, WikiImportID, source, func(ctx context.Context, tx pgx.Tx) (int, error) {
		n = 0
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('wiki-store'))"); err != nil {
			return 0, err
		}
		paths := make([]string, 0, len(w.Files))
		for p := range w.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			version := w.Files[p].Version
			if version == "" {
				version = db.NewID()
			}
			if err := putAt(ctx, tx, p, []byte(w.Files[p].Body), version); err != nil {
				return 0, err
			}
			n++
		}
		for run, st := range w.Staged {
			bodies := map[string][]byte{}
			for p, body := range st {
				bodies[p] = []byte(body)
			}
			if err := stage(ctx, tx, run, bodies); err != nil {
				return 0, err
			}
			n++
		}
		return n, nil
	})
	return n, ran, err
}
