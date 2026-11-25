package sessions

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Archive is a History the backup can also add a file's records to.
type Archive interface {
	History
	// Merge adds the records not kept yet, by id, and says how many.
	Merge(ctx context.Context, add []Record) (int, error)
}

// PGHistory keeps the ended sessions' records in the database's
// toolbox_sessions (Phase 13, DB-23), a row each, added when the session
// ends and never changed. The newest Keep are kept, as the Toolbox's
// settings say; the size of a ConfigMap no longer bounds them.
type PGHistory struct {
	DB     *db.DB
	Keep   int
	Logger *slog.Logger

	mu sync.Mutex
	// unsaved are the records the database did not take, shown from memory
	// and tried again with the next one.
	unsaved []Record
	// last is what List returned last, shown while the database is away.
	last []Record
}

const historyColumns = `toolbox_session_id, cluster_id, kind, summary, namespace, pod, container, node, owner, owner_user, approved_by, reason,
	created_at, started_at, ended_at, ended_by, end_note, exit_code, is_recorded, byte_count, connection_count, watched_by, card_key, ends_at`

func historyTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// insert adds the records not there yet, and says how many went in.
func insertRecords(ctx context.Context, q db.Querier, list []Record) (int, error) {
	n := 0
	for _, r := range list {
		watchers := r.Watchers
		if watchers == nil {
			watchers = []string{}
		}
		tag, err := q.Exec(ctx, "INSERT INTO toolbox_sessions ("+historyColumns+`, state)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, 'ended')
			ON CONFLICT (toolbox_session_id) DO NOTHING`,
			r.ID, r.Cluster, r.Tool, r.Summary, r.Namespace, r.Pod, r.Container, r.Node, r.Owner, r.OwnerUser, r.Approver, r.Reason,
			r.Created.UTC(), historyTime(r.Started), historyTime(r.Ended), r.EndedBy, r.EndNote, r.Exit, r.Recorded, r.Bytes, r.Conns, watchers, r.Card, historyTime(r.Ends))
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// trim removes the records past the newest Keep.
func (h *PGHistory) trim(ctx context.Context, q db.Querier) error {
	if h.Keep <= 0 {
		return nil
	}
	_, err := q.Exec(ctx, `DELETE FROM toolbox_sessions WHERE toolbox_session_id IN (
		SELECT toolbox_session_id FROM toolbox_sessions ORDER BY ended_at DESC NULLS LAST, toolbox_session_id OFFSET $1)`, h.Keep)
	return err
}

func (h *PGHistory) save(ctx context.Context, list []Record) (int, error) {
	n := 0
	err := h.DB.Tx(ctx, func(tx pgx.Tx) (err error) {
		if n, err = insertRecords(ctx, tx, list); err != nil {
			return err
		}
		return h.trim(ctx, tx)
	})
	return n, err
}

// Add keeps an ended session's record. A record the database does not
// take stays in memory, where List shows it, and goes in with the next.
func (h *PGHistory) Add(r Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	list := append(append([]Record(nil), h.unsaved...), r)
	if _, err := h.save(ctx, list); err != nil {
		if h.Logger != nil {
			h.Logger.Warn("the session record was not saved; the audit log has it", "session", r.ID, "err", err)
		}
		h.unsaved = list
		if h.Keep > 0 && len(h.unsaved) > h.Keep {
			h.unsaved = h.unsaved[len(h.unsaved)-h.Keep:]
		}
		return
	}
	h.unsaved = nil
}

// Merge adds a backup's records to those kept, by id.
func (h *PGHistory) Merge(ctx context.Context, add []Record) (int, error) {
	return h.save(ctx, add)
}

// List returns the records kept, the newest ended first.
func (h *PGHistory) List() []Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var list []Record
	// The newest Keep at most, should a trim not have run yet.
	limit := ""
	if h.Keep > 0 {
		limit = fmt.Sprintf(" LIMIT %d", h.Keep)
	}
	err := h.DB.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT "+historyColumns+" FROM toolbox_sessions WHERE state = 'ended' ORDER BY ended_at DESC NULLS LAST, toolbox_session_id"+limit)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Record, error) {
			var r Record
			var started, ended, ends *time.Time
			err := row.Scan(&r.ID, &r.Cluster, &r.Tool, &r.Summary, &r.Namespace, &r.Pod, &r.Container, &r.Node, &r.Owner, &r.OwnerUser, &r.Approver, &r.Reason,
				&r.Created, &started, &ended, &r.EndedBy, &r.EndNote, &r.Exit, &r.Recorded, &r.Bytes, &r.Conns, &r.Watchers, &r.Card, &ends)
			if ends != nil {
				r.Ends = ends.UTC()
			}
			r.Created = r.Created.UTC()
			if started != nil {
				r.Started = started.UTC()
			}
			if ended != nil {
				r.Ended = ended.UTC()
			}
			if len(r.Watchers) == 0 {
				r.Watchers = nil
			}
			return r, err
		})
		return err
	})
	if err == nil {
		h.last = list
	}
	// The newest first: what waits in memory ended after what is kept.
	out := make([]Record, 0, len(h.unsaved)+len(h.last))
	for i := len(h.unsaved) - 1; i >= 0; i-- {
		out = append(out, h.unsaved[i])
	}
	return append(out, h.last...)
}

// Import copies the records another history kept (DB-17). The records
// only grow, so every start copies those that ended after the newest of
// the last copy: none as a rule, and after a rollback to a release without
// a database the sessions that ended meanwhile.
func (h *PGHistory) Import(ctx context.Context, from History, source string) (int, error) {
	return h.DB.ImportNewer(ctx, HistoryKey, source, func(ctx context.Context, tx pgx.Tx, since time.Time) (int, time.Time, error) {
		var newest time.Time
		var later []Record
		for _, r := range from.List() {
			if r.Ended.After(newest) {
				newest = r.Ended
			}
			if db.After(r.Ended, since) {
				later = append(later, r)
			}
		}
		if len(later) == 0 {
			return 0, newest, nil
		}
		n, err := insertRecords(ctx, tx, later)
		if err != nil {
			return 0, newest, err
		}
		return n, newest, h.trim(ctx, tx)
	})
}
