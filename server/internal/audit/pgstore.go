package audit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// PGStore keeps the audit log in the database's audit_events (Phase 13,
// DB-20): a ledger, each event one row that is added and never changed, so
// replicas adding at once never meet. Events wait in memory for a moment
// and are added together; while the database is away they stay queued, the
// newest Keep of them, and the stream and the webhook have them all.
type PGStore struct {
	DB *db.DB
	// Keep is how many of the newest events List returns, and how many
	// wait at most while the database is away.
	Keep   int
	Every  time.Duration
	Logger *slog.Logger

	mu      sync.Mutex
	pending []pgEvent
	saving  sync.Mutex
	once    sync.Once
	stop    chan struct{}
	done    chan struct{}
}

// pgEvent is an event with the key it will have, made when it is recorded
// so that the keys sort as the events happened.
type pgEvent struct {
	id string
	Event
}

// NewPGStore starts a store that adds what it is given every two seconds.
func NewPGStore(d *db.DB, logger *slog.Logger) *PGStore {
	s := &PGStore{DB: d, Keep: DefaultKeep, Every: defaultEvery, Logger: logger}
	s.start()
	return s
}

func (s *PGStore) start() {
	s.once.Do(func() {
		if s.Logger == nil {
			s.Logger = slog.Default()
		}
		if s.Every <= 0 {
			s.Every = defaultEvery
		}
		if s.Keep <= 0 {
			s.Keep = DefaultKeep
		}
		s.stop, s.done = make(chan struct{}), make(chan struct{})
		go func() {
			defer close(s.done)
			var quiet db.Quiet
			t := time.NewTicker(s.Every)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					// With the database away it fails every two seconds:
					// said once, and once when it works again (DB-15).
					if err := s.Flush(ctx); err != nil {
						quiet.Failed(s.Logger, "saving the audit log failed; the events stay queued", err)
					} else {
						quiet.Worked(s.Logger, "the audit log is saved again, with the events queued meanwhile")
					}
					cancel()
				case <-s.stop:
					return
				}
			}
		}()
	})
}

// Close stops the store and adds what still waits.
func (s *PGStore) Close(ctx context.Context) error {
	s.start()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	return s.Flush(ctx)
}

// Add queues an event.
func (s *PGStore) Add(e Event) {
	s.start()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, pgEvent{id: db.NewID(), Event: e})
	if len(s.pending) > s.Keep {
		s.pending = s.pending[len(s.pending)-s.Keep:]
	}
}

// Flush adds the queued events to the table, all or none.
func (s *PGStore) Flush(ctx context.Context) error {
	s.saving.Lock()
	defer s.saving.Unlock()
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	err := s.DB.Do(ctx, func(q db.Querier) error { return insertEvents(ctx, q, pending) })
	if err != nil {
		s.mu.Lock()
		s.pending = append(pending, s.pending...)
		if len(s.pending) > s.Keep {
			s.pending = s.pending[len(s.pending)-s.Keep:]
		}
		s.mu.Unlock()
	}
	return err
}

// insertEvents adds the events in one statement. An event whose key is
// already there is left alone: a statement that was sent, and whose answer
// was lost with the connection, is sent again.
func insertEvents(ctx context.Context, q db.Querier, list []pgEvent) error {
	n := len(list)
	ids, at, actor, subject, email := make([]string, n), make([]time.Time, n), make([]string, n), make([]string, n), make([]string, n)
	action, cluster, target, outcome, detail, from := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	restored := make([]*time.Time, n)
	for i, e := range list {
		ids[i], at[i], actor[i], subject[i], email[i] = e.id, e.Time.UTC(), e.Who.Name, e.Who.Subject, e.Who.Email
		action[i], cluster[i], target[i], outcome[i], detail[i], from[i] = e.Action, e.Cluster, e.Target, e.Outcome, e.Detail, e.From
		if t, err := time.Parse(time.RFC3339, e.Restored); err == nil {
			restored[i] = &t
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_events
		(audit_event_id, at, actor, actor_subject, actor_email, action, cluster_id, target, outcome, detail, source_ip, restored_at)
		SELECT * FROM unnest($1::uuid[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[],
			$7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::timestamptz[])
		ON CONFLICT (audit_event_id) DO NOTHING`,
		ids, at, actor, subject, email, action, cluster, target, outcome, detail, from, restored)
	return err
}

// List returns the newest Keep events, newest first, after adding what
// waits.
func (s *PGStore) List(ctx context.Context) ([]Event, error) {
	if err := s.Flush(ctx); err != nil {
		s.Logger.Warn("saving the audit log before reading it failed", "err", err)
	}
	var out []Event
	err := s.DB.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT at, actor, actor_subject, actor_email, action, cluster_id, target, outcome, detail, source_ip, restored_at
			FROM audit_events ORDER BY at DESC, audit_event_id DESC LIMIT $1`, s.Keep)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
			var e Event
			var restored *time.Time
			err := r.Scan(&e.Time, &e.Who.Name, &e.Who.Subject, &e.Who.Email, &e.Action, &e.Cluster, &e.Target, &e.Outcome, &e.Detail, &e.From, &restored)
			e.Time = e.Time.UTC()
			if restored != nil {
				e.Restored = restored.UTC().Format(time.RFC3339)
			}
			return e, err
		})
		return err
	})
	return out, err
}

// Trim removes the events older than before, and says how many: the
// table's retention, run by whichever replica holds the sweep's lock.
func (s *PGStore) Trim(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	_, err := s.DB.Sweep(ctx, "audit-retention", func(ctx context.Context) error {
		return s.DB.Do(ctx, func(q db.Querier) error {
			tag, err := q.Exec(ctx, "DELETE FROM audit_events WHERE at < $1", before.UTC())
			n = tag.RowsAffected()
			return err
		})
	})
	return n, err
}

// Import copies the events another store kept (DB-17): the audit log a
// release before Phase 13 kept in a ConfigMap. The log only grows, so every
// start copies what is newer than the last copy: nothing as a rule, and
// after a rollback to that release the events it wrote meanwhile. It says
// how many it copied.
func (s *PGStore) Import(ctx context.Context, from Store, source string) (int, error) {
	return s.DB.ImportNewer(ctx, StoreKey, source, func(ctx context.Context, tx pgx.Tx, since time.Time) (int, time.Time, error) {
		var newest time.Time
		list, err := from.List(ctx)
		if err != nil || len(list) == 0 {
			return 0, newest, err
		}
		// Oldest first, so the keys sort as the events happened.
		events := make([]pgEvent, 0, len(list))
		for i := len(list) - 1; i >= 0; i-- {
			e := list[i]
			if e.Time.After(newest) {
				newest = e.Time
			}
			if db.After(e.Time, since) {
				events = append(events, pgEvent{Event: e, id: db.NewID()})
			}
		}
		if len(events) == 0 {
			return 0, newest, nil
		}
		return len(events), newest, insertEvents(ctx, tx, events)
	})
}

// Rewrite writes again the events whose action starts with prefix and
// whose detail fix changes, in tx: each is added anew with the detail
// fixed, under a new key and with everything else as it was, and the
// event as it was is deleted. The ledger refuses a change of a row, and
// this is not one: what a release wrote before it hid a value is made to
// read as that release writes it now (DB-71, DB-74). like narrows the
// rows read to those whose detail is LIKE one of its patterns. It says
// how many it wrote again; the texts are compared here, so no statement
// carries a value.
func Rewrite(ctx context.Context, tx pgx.Tx, prefix string, like []string, fix func(detail string) string) (int, error) {
	rows, err := tx.Query(ctx, "SELECT audit_event_id::text, detail FROM audit_events WHERE starts_with(action, $1) AND detail LIKE ANY($2)", prefix, like)
	if err != nil {
		return 0, err
	}
	type change struct{ id, detail string }
	var changes []change
	var id, detail string
	if _, err := pgx.ForEachRow(rows, []any{&id, &detail}, func() error {
		if d := fix(detail); d != detail {
			changes = append(changes, change{id, d})
		}
		return nil
	}); err != nil {
		return 0, err
	}
	for _, c := range changes {
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events (audit_event_id, at, actor, actor_subject, actor_email, action, cluster_id, target, outcome, detail, source_ip, restored_at)
			SELECT $2, at, actor, actor_subject, actor_email, action, cluster_id, target, outcome, $3, source_ip, restored_at FROM audit_events WHERE audit_event_id = $1`,
			c.id, db.NewID(), c.detail); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM audit_events WHERE audit_event_id = $1", c.id); err != nil {
			return 0, err
		}
	}
	return len(changes), nil
}
