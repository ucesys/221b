package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// Repo keeps what the replicas share of the backups to S3: the settings
// and the runs (ADR-162). The scheduler knows nothing of where: KVRepo
// keeps them as two JSON documents beside the settings, as every release
// before Phase 13 did; PGRepo keeps the settings as the scope "backup" of
// the database's settings and each run as a row of backup_runs.
type Repo interface {
	// Settings are the saved settings, or the defaults and false.
	Settings(ctx context.Context) (S3Settings, bool, error)
	SaveSettings(ctx context.Context, set S3Settings) error
	// Status is the newest scheduled time handled, the newest runs and
	// the newest that worked.
	Status(ctx context.Context) (Status, error)
	// Claim marks the scheduled time due as handled and says whether this
	// replica was the first to: only the first runs it. missed, when given,
	// is the run that says the time was let go.
	Claim(ctx context.Context, due time.Time, missed *Run) (bool, error)
	// AddRun records a run that ended.
	AddRun(ctx context.Context, r Run) error
}

// KVRepo keeps the settings and the runs under two keys of a kv.Backend.
type KVRepo struct{ b kv.Backend }

// NewKVRepo keeps the backups' records in b.
func NewKVRepo(b kv.Backend) *KVRepo { return &KVRepo{b: b} }

func (r *KVRepo) Settings(ctx context.Context) (S3Settings, bool, error) {
	b, _, err := r.b.LoadKey(ctx, SettingsKey)
	if errors.Is(err, kv.ErrNotFound) {
		return DefaultSettings(), false, nil
	}
	if err != nil {
		return DefaultSettings(), false, err
	}
	return decodeSettings(b)
}

func decodeSettings(b []byte) (S3Settings, bool, error) {
	// The defaults fill what was never saved, except the encryption: SSE
	// is left out when empty, so saved settings without it chose the
	// bucket's default; SSE-S3 is only the default of a page never saved.
	set := DefaultSettings()
	set.SSE = ""
	if err := json.Unmarshal(b, &set); err != nil {
		return DefaultSettings(), false, fmt.Errorf("the saved settings of the backups to S3 cannot be read: %w", err)
	}
	return set, true, nil
}

func (r *KVRepo) SaveSettings(ctx context.Context, set S3Settings) error {
	b, _ := json.Marshal(set)
	_, err := r.b.SaveKey(ctx, SettingsKey, b, "")
	return err
}

func (r *KVRepo) Status(ctx context.Context) (Status, error) {
	var st Status
	b, _, err := r.b.LoadKey(ctx, StatusKey)
	if errors.Is(err, kv.ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// update changes the status over the version read, again when another
// replica wrote it first.
func (r *KVRepo) update(ctx context.Context, f func(*Status)) error {
	for try := 0; try < 8; try++ {
		var st Status
		b, version, err := r.b.LoadKey(ctx, StatusKey)
		if err == nil {
			if err := json.Unmarshal(b, &st); err != nil {
				st = Status{}
			}
		} else if !errors.Is(err, kv.ErrNotFound) {
			return err
		}
		f(&st)
		if len(st.Recent) > recentRuns {
			st.Recent = st.Recent[:recentRuns]
		}
		out, _ := json.Marshal(st)
		if _, err = r.b.SaveKey(ctx, StatusKey, out, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return errors.New("the backups' status kept changing")
}

func (r *KVRepo) Claim(ctx context.Context, due time.Time, missed *Run) (bool, error) {
	claimed := false
	err := r.update(ctx, func(st *Status) {
		if claimed = st.Handled.Before(due); !claimed {
			return
		}
		st.Handled = due
		if missed != nil {
			st.Recent = append([]Run{*missed}, st.Recent...)
		}
	})
	return claimed && err == nil, err
}

func (r *KVRepo) AddRun(ctx context.Context, run Run) error {
	return r.update(ctx, func(st *Status) {
		st.Recent = append([]Run{run}, st.Recent...)
		if run.OK {
			rr := run
			st.LastOK = &rr
		}
	})
}

// settingsScope is the backups' row of the database's settings.
const settingsScope = "backup"

// keptRuns is how many runs the database keeps; the page shows the newest
// recentRuns of them.
const keptRuns = 500

// PGRepo keeps the settings and the runs in the database (Phase 13).
type PGRepo struct{ d *db.DB }

// NewPGRepo keeps the backups' records in d.
func NewPGRepo(d *db.DB) *PGRepo { return &PGRepo{d: d} }

func (r *PGRepo) Settings(ctx context.Context) (S3Settings, bool, error) {
	var b []byte
	err := r.d.Do(ctx, func(q db.Querier) (err error) {
		b, _, err = db.LoadScope(ctx, q, settingsScope)
		return err
	})
	if errors.Is(err, db.ErrNotFound) {
		return DefaultSettings(), false, nil
	}
	if err != nil {
		return DefaultSettings(), false, err
	}
	return decodeSettings(b)
}

func (r *PGRepo) SaveSettings(ctx context.Context, set S3Settings) error {
	b, _ := json.Marshal(set)
	return r.d.Do(ctx, func(q db.Querier) error {
		_, err := db.SaveScope(ctx, q, settingsScope, b, db.AnyVersion, set.By)
		return err
	})
}

const runColumns = "slot_at, started_by, started_at, ended_at, is_ok, is_missed, object_key, size_bytes, pruned_count, error"

func scanRun(row pgx.CollectableRow) (Run, error) {
	var r Run
	var slot, ended *time.Time
	var size int64
	err := row.Scan(&slot, &r.By, &r.Started, &ended, &r.OK, &r.Missed, &r.Key, &size, &r.Pruned, &r.Error)
	r.Started, r.Size = r.Started.UTC(), int(size)
	if slot != nil {
		r.Slot = slot.UTC()
	}
	if ended != nil {
		r.Ended = ended.UTC()
	}
	return r, err
}

func (r *PGRepo) Status(ctx context.Context) (Status, error) {
	var st Status
	err := r.d.Do(ctx, func(q db.Querier) error {
		var handled *time.Time
		if err := q.QueryRow(ctx, "SELECT max(slot_at) FROM backup_runs").Scan(&handled); err != nil {
			return err
		}
		if handled != nil {
			st.Handled = handled.UTC()
		}
		// A run still under way has no end yet and is not listed.
		rows, err := q.Query(ctx, "SELECT "+runColumns+" FROM backup_runs WHERE ended_at IS NOT NULL ORDER BY started_at DESC, backup_run_id DESC LIMIT $1", recentRuns)
		if err != nil {
			return err
		}
		if st.Recent, err = pgx.CollectRows(rows, scanRun); err != nil {
			return err
		}
		rows, err = q.Query(ctx, "SELECT "+runColumns+" FROM backup_runs WHERE is_ok ORDER BY started_at DESC, backup_run_id DESC LIMIT 1")
		if err != nil {
			return err
		}
		ok, err := pgx.CollectRows(rows, scanRun)
		if len(ok) == 1 {
			st.LastOK = &ok[0]
		}
		return err
	})
	if len(st.Recent) == 0 {
		st.Recent = nil
	}
	return st, err
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func insertRun(ctx context.Context, q db.Querier, run Run) error {
	_, err := q.Exec(ctx, "INSERT INTO backup_runs (backup_run_id, "+runColumns+") VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		db.NewID(), nullTime(run.Slot), run.By, run.Started.UTC(), nullTime(run.Ended), run.OK, run.Missed, run.Key, int64(run.Size), run.Pruned, run.Error)
	return err
}

// Claim takes the scheduled time by adding its row: the missed run, or a
// run under way that AddRun completes. The replicas take turns behind a
// lock for the length of the transaction, so of those that try for one
// time, one finds no row at or after it.
func (r *PGRepo) Claim(ctx context.Context, due time.Time, missed *Run) (bool, error) {
	claimed := false
	err := r.d.Tx(ctx, func(tx pgx.Tx) error {
		claimed = false
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('221b/backup-claim', 0))"); err != nil {
			return err
		}
		var handled *time.Time
		if err := tx.QueryRow(ctx, "SELECT max(slot_at) FROM backup_runs").Scan(&handled); err != nil {
			return err
		}
		if handled != nil && !handled.Before(due) {
			return nil
		}
		run := Run{Started: time.Now().UTC(), Slot: due, By: ScheduledBy}
		if missed != nil {
			run = *missed
			run.Slot = due
		}
		claimed = true
		return insertRun(ctx, tx, run)
	})
	return claimed && err == nil, err
}

// AddRun records a run that ended: over the row its claim added, for a
// scheduled one, else as a new row. The oldest runs past keptRuns go.
func (r *PGRepo) AddRun(ctx context.Context, run Run) error {
	return r.d.Tx(ctx, func(tx pgx.Tx) error {
		done := int64(0)
		if !run.Slot.IsZero() {
			tag, err := tx.Exec(ctx, `UPDATE backup_runs SET started_by = $2, started_at = $3, ended_at = $4, is_ok = $5, is_missed = $6,
				object_key = $7, size_bytes = $8, pruned_count = $9, error = $10 WHERE slot_at = $1 AND ended_at IS NULL`,
				run.Slot.UTC(), run.By, run.Started.UTC(), nullTime(run.Ended), run.OK, run.Missed, run.Key, int64(run.Size), run.Pruned, run.Error)
			if err != nil {
				return err
			}
			done = tag.RowsAffected()
		}
		if done == 0 {
			if err := insertRun(ctx, tx, run); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM backup_runs WHERE backup_run_id IN (
			SELECT backup_run_id FROM backup_runs ORDER BY started_at DESC, backup_run_id DESC OFFSET $1)`, keptRuns)
		return err
	})
}

// Import copies the settings and the runs another repository kept, once
// (DB-17), and says whether it copied.
func (r *PGRepo) Import(ctx context.Context, from Repo, source string) (bool, error) {
	return r.d.ImportOnce(ctx, SettingsKey, source, func(ctx context.Context, tx pgx.Tx) (int, error) {
		n := 0
		set, saved, err := from.Settings(ctx)
		if err != nil {
			return 0, err
		}
		if saved {
			b, _ := json.Marshal(set)
			if _, err := db.SaveScope(ctx, tx, settingsScope, b, db.AnyVersion, set.By); err != nil {
				return 0, err
			}
			n++
		}
		st, err := from.Status(ctx)
		if err != nil {
			return 0, err
		}
		runs := append([]Run(nil), st.Recent...)
		if st.LastOK != nil && (len(runs) == 0 || st.LastOK.Started.Before(runs[len(runs)-1].Started)) {
			runs = append(runs, *st.LastOK)
		}
		handled := false
		for _, run := range runs {
			handled = handled || run.Slot.Equal(st.Handled)
			if err := insertRun(ctx, tx, run); err != nil {
				return 0, err
			}
			n++
		}
		// The newest time handled, when none of the runs kept is for it.
		if !st.Handled.IsZero() && !handled {
			// Without an end it is not listed; it only holds the time.
			if err := insertRun(ctx, tx, Run{Started: st.Handled, Slot: st.Handled, By: ScheduledBy}); err != nil {
				return 0, err
			}
		}
		return n, nil
	})
}
