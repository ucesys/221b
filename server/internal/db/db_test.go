package db_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestConfigValidate(t *testing.T) {
	ok := db.Config{Host: "db", Port: 5432, Name: "221b", User: "app_221b", SSLMode: "verify-full", MaxConns: 10}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a complete configuration was refused: %v", err)
	}
	if err := (db.Config{}).Validate(); err != nil {
		t.Fatalf("no database at all is allowed: %v", err)
	}
	for name, edit := range map[string]func(*db.Config){
		"sslMode":     func(c *db.Config) { c.SSLMode = "prefer" },
		"name":        func(c *db.Config) { c.Name = "" },
		"port":        func(c *db.Config) { c.Port = 0 },
		"certificate": func(c *db.Config) { c.ClientCertFile = "/tls/tls.crt" },
		"connections": func(c *db.Config) { c.MaxConns = 1 },
	} {
		c := ok
		edit(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: a wrong value was accepted", name)
		}
	}
}

func TestMigrationsAreNumberedAndNamed(t *testing.T) {
	steps, err := db.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("no schema steps are embedded")
	}
	for i, m := range steps {
		if m.ID != i+1 || m.Name == "" || len(m.Checksum) != 64 || strings.TrimSpace(m.SQL) == "" {
			t.Errorf("step %d: %+v", i+1, m)
		}
	}
}

// The schema holds the model's tables, each key named for its table
// (DB-08), and migrating twice changes nothing.
func TestMigrate(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	n, err := d.Migrate(ctx)
	if err != nil || n != len(steps) {
		t.Fatalf("first migration applied %d of %d steps: %v", n, len(steps), err)
	}
	if n, err := d.Migrate(ctx); err != nil || n != 0 {
		t.Fatalf("second migration applied %d steps: %v", n, err)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != len(steps) {
		t.Fatalf("schema version %d, want %d: %v", v, len(steps), err)
	}
	var tables int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name NOT IN ('schema_migrations', 'imports')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 73 {
		t.Errorf("the schema has %d tables; the data model has 73", tables)
	}
	// No bare "id" column anywhere, and every uuid or text key column that
	// is a table's whole key ends in _id, scope or path.
	rows, err := d.Pool().Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name = 'id'`)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var tbl, col string
		err := r.Scan(&tbl, &col)
		return tbl + "." + col, err
	})
	if err != nil || len(bare) != 0 {
		t.Errorf("bare id columns: %v %v", bare, err)
	}
}

// Replicas that start together migrate one after another, and each step
// is applied once.
func TestMigrateTogether(t *testing.T) {
	cfg := dbtest.Config(t)
	ctx := ctxT(t)
	var wg sync.WaitGroup
	var applied atomic.Int64
	for range 4 {
		d := dbtest.OpenConfig(t, cfg)
		wg.Go(func() {
			n, err := d.Migrate(ctx)
			if err != nil {
				t.Errorf("migrating: %v", err)
			}
			applied.Add(int64(n))
		})
	}
	wg.Wait()
	steps, _ := db.Migrations()
	if int(applied.Load()) != len(steps) {
		t.Errorf("four replicas applied %d steps between them, want %d", applied.Load(), len(steps))
	}
}

func TestLedgersAreAppendOnly(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	id := db.NewID()
	if _, err := d.Pool().Exec(ctx, "INSERT INTO audit_events (audit_event_id, actor, action) VALUES ($1, 'ada', 'signin')", id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, "UPDATE audit_events SET actor = 'eve' WHERE audit_event_id = $1", id); err == nil {
		t.Error("an audit event was changed")
	}
	if _, err := d.Pool().Exec(ctx, "DELETE FROM audit_events WHERE audit_event_id = $1", id); err != nil {
		t.Errorf("retention could not delete an audit event: %v", err)
	}
}

func TestNewIDSortsByTime(t *testing.T) {
	a := db.NewID()
	time.Sleep(2 * time.Millisecond)
	b := db.NewID()
	if len(a) != 36 || a[14] != '7' || !(a < b) {
		t.Errorf("ids %s then %s: want UUIDv7s in order", a, b)
	}
}

func TestTxRollsBack(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	boom := errors.New("boom")
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO settings (scope, value) VALUES ('general', '{}')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM settings").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d rows after a rolled back transaction: %v", n, err)
	}
}

// One replica holds a sweep's lock at a time, and a lock is free again
// once given back.
func TestSweepRunsOnOneReplica(t *testing.T) {
	cfg := dbtest.Config(t)
	a, b := dbtest.OpenConfig(t, cfg), dbtest.OpenConfig(t, cfg)
	ctx := ctxT(t)
	inside, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.Sweep(ctx, "retention", func(context.Context) error {
			close(inside)
			<-release
			return nil
		})
		done <- err
	}()
	<-inside
	ran, err := b.Sweep(ctx, "retention", func(context.Context) error { return nil })
	if err != nil || ran {
		t.Errorf("a second replica ran the sweep (%v): %v", ran, err)
	}
	if ran, err := b.Sweep(ctx, "node-sweep", func(context.Context) error { return nil }); err != nil || !ran {
		t.Errorf("another sweep was held back (%v): %v", ran, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ran, err := b.Sweep(ctx, "retention", func(context.Context) error { return nil }); err != nil || !ran {
		t.Errorf("the sweep stayed locked after it ended (%v): %v", ran, err)
	}
}

// Of the replicas that try for one slot, one gets it.
func TestClaimOnce(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	job := db.NewID()
	if _, err := d.Pool().Exec(ctx, "INSERT INTO jobs (job_id, kind, name) VALUES ($1, 'scan', 'Nightly scan')", job); err != nil {
		t.Fatal(err)
	}
	slot := time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	var got atomic.Int64
	for i := range 8 {
		wg.Go(func() {
			ok, err := d.Claim(ctx, d.Pool(), job, slot, "replica-"+string(rune('a'+i)))
			if err != nil {
				t.Errorf("claiming: %v", err)
			}
			if ok {
				got.Add(1)
			}
		})
	}
	wg.Wait()
	if got.Load() != 1 {
		t.Errorf("%d replicas got the slot", got.Load())
	}
	if ok, err := d.Claim(ctx, d.Pool(), job, slot.Add(time.Hour), "replica-a"); err != nil || !ok {
		t.Errorf("the next slot was refused (%v): %v", ok, err)
	}
	if n, err := d.ForgetClaims(ctx, slot.Add(30*time.Minute)); err != nil || n != 1 {
		t.Errorf("forgot %d claims: %v", n, err)
	}
}

// A notice reaches the other replica when its transaction commits, not
// when it is rolled back; and a replica looks anyway every Poll.
func TestNotifyAndPoll(t *testing.T) {
	cfg := dbtest.Config(t)
	a, b := dbtest.OpenConfig(t, cfg), dbtest.OpenConfig(t, cfg)
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	heard := make(chan struct{}, 64)
	var others atomic.Int64
	b.Subscribe("settings", func() { heard <- struct{}{} })
	b.Subscribe("hosts", func() { others.Add(1) })
	go b.Listen(ctx)
	// The first call is the listener starting; then polls, every 200 ms.
	wait := func(what string) {
		t.Helper()
		select {
		case <-heard:
		case <-time.After(5 * time.Second):
			t.Fatalf("nothing heard: %s", what)
		}
	}
	wait("the listener starting")
	wait("a poll")
	drain := func() {
		for {
			select {
			case <-heard:
			default:
				return
			}
		}
	}
	// A rolled back notice is not delivered: count what arrives in less
	// than one poll.
	_ = a.Tx(ctx, func(tx pgx.Tx) error {
		_ = a.Notify(ctx, tx, "settings")
		return errors.New("rolled back")
	})
	wait("the next poll")
	drain()
	start := time.Now()
	if err := a.Tx(ctx, func(tx pgx.Tx) error { return a.Notify(ctx, tx, "settings") }); err != nil {
		t.Fatal(err)
	}
	wait("a committed notice")
	if took := time.Since(start); took > 150*time.Millisecond {
		t.Logf("the notice took %s; it may have been a poll", took)
	}
	if others.Load() == 0 {
		t.Error("the other topic was never polled")
	}
}

// Calls made while the database is away say so, the health follows, and
// what waits for it is told when it is back.
func TestHealthFollowsTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	if h := d.Health(); !h.Available {
		t.Fatalf("an open database is %+v", h)
	}
	back := make(chan struct{}, 1)
	d.OnAvailable(func() { back <- struct{}{} })
	// Every connection of this database is ended from under the pool.
	err := d.Do(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database()")
		return err
	})
	if !errors.Is(err, db.ErrUnavailable) {
		t.Fatalf("a call cut off by the server returned %v", err)
	}
	if h := d.Health(); h.Available || h.Error == "" {
		t.Errorf("health after the cut: %+v", h)
	}
	if err := d.Do(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT 1"); return err }); err != nil {
		t.Fatalf("the pool did not reconnect: %v", err)
	}
	select {
	case <-back:
	case <-time.After(5 * time.Second):
		t.Error("nobody was told the database is back")
	}
	if h := d.Health(); !h.Available {
		t.Errorf("health after reconnecting: %+v", h)
	}
}

// A scope is saved over the version read: of two writers that read the
// same version, the second is told.
func TestScopes(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	q := d.Pool()
	if _, v, err := db.LoadScope(ctx, q, "backup"); !errors.Is(err, db.ErrNotFound) || v != 0 {
		t.Fatalf("a scope never saved: version %d, %v", v, err)
	}
	v1, err := db.SaveScope(ctx, q, "backup", []byte(`{"bucket":"a"}`), 0, "ada")
	if err != nil || v1 != 1 {
		t.Fatalf("first save: version %d, %v", v1, err)
	}
	if _, err := db.SaveScope(ctx, q, "backup", []byte(`{"bucket":"b"}`), 0, "bob"); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a second first save: %v", err)
	}
	v2, err := db.SaveScope(ctx, q, "backup", []byte(`{"bucket":"c"}`), v1, "ada")
	if err != nil || v2 != 2 {
		t.Fatalf("save over version 1: version %d, %v", v2, err)
	}
	if _, err := db.SaveScope(ctx, q, "backup", []byte(`{"bucket":"d"}`), v1, "bob"); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	if v, err := db.SaveScope(ctx, q, "backup", []byte(`{"bucket":"e"}`), db.AnyVersion, "bob"); err != nil || v != 3 {
		t.Fatalf("a save over any version: version %d, %v", v, err)
	}
	b, v, err := db.LoadScope(ctx, q, "backup")
	if err != nil || v != 3 || !strings.Contains(string(b), `"e"`) {
		t.Fatalf("read back %s at version %d: %v", b, v, err)
	}
}

// A service's singleton document is a scope behind the kv.Backend it
// already uses: conditional on the version read, first save included, and
// copied from the old backend once.
func TestScopeKV(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	s := db.NewScopeKV(d, map[string]string{"setup.json": "setup", "cases-mail.json": "email"})
	if _, _, err := s.LoadKey(ctx, "other.json"); err == nil {
		t.Fatal("a key that is no scope was read")
	}
	_, v0, err := s.LoadKey(ctx, "setup.json")
	if !errors.Is(err, kv.ErrNotFound) || v0 != "0" {
		t.Fatalf("never saved: version %q, %v", v0, err)
	}
	v1, err := s.SaveKey(ctx, "setup.json", []byte(`{"dismissed":["a"]}`), v0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveKey(ctx, "setup.json", []byte(`{"dismissed":["b"]}`), v0); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a second first save: %v", err)
	}
	if _, err := s.SaveKey(ctx, "setup.json", []byte(`{"dismissed":["a","c"]}`), v1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveKey(ctx, "setup.json", []byte(`{}`), v1); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	if b, _, err := s.LoadKey(ctx, "setup.json"); err != nil || !strings.Contains(string(b), `"c"`) {
		t.Fatalf("read back %s: %v", b, err)
	}
	old := &kv.Memory{}
	if _, err := old.SaveKey(ctx, "cases-mail.json", []byte(`{"host":"smtp.corp.example"}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := old.SaveKey(ctx, "setup.json", []byte(`{"dismissed":["old"]}`), ""); err != nil {
		t.Fatal(err)
	}
	copied, err := s.Import(ctx, old, "test")
	if err != nil || len(copied) != 2 {
		t.Fatalf("copied %v: %v", copied, err)
	}
	if b, _, _ := s.LoadKey(ctx, "cases-mail.json"); !strings.Contains(string(b), "smtp.corp.example") {
		t.Fatalf("the copy holds %s", b)
	}
	if copied, err := s.Import(ctx, old, "test"); err != nil || len(copied) != 0 {
		t.Fatalf("copied %v the second time: %v", copied, err)
	}
	if err := s.DeleteKey(ctx, "cases-mail.json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoadKey(ctx, "cases-mail.json"); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("after deleting: %v", err)
	}
}
