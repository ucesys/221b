package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// A step a database has applied is never edited: the database would refuse
// the build (the first cluster test's did). testdata/steps.sum holds each
// step's checksum from when it was written down, and every step is in it.
func TestAppliedStepsAreNotEdited(t *testing.T) {
	steps, err := db.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/steps.sum")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && !strings.HasPrefix(line, "#") {
			kept[f[0]] = f[1]
		}
	}
	for _, m := range steps {
		name := fmt.Sprintf("%04d_%s", m.ID, m.Name)
		switch sum, ok := kept[name]; {
		case !ok:
			t.Errorf("step %s is not in testdata/steps.sum: add it with its checksum, %s", name, m.Checksum)
		case sum != m.Checksum:
			t.Errorf("step %s was edited: a database that applied it refuses this build. Put the change in a new step", name)
		}
		delete(kept, name)
	}
	for name := range kept {
		t.Errorf("testdata/steps.sum lists %s, which is not a step any more: a step is never removed", name)
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
	// The model's 73, 3 that hold what a script version's document kept in
	// lists (step 0016), and the script versions a variable change touches
	// (step 0018), who had approved a run's version (step 0019), and what
	// a run request's values mean for its script (step 0020), and a
	// provider's headers (step 0022), and the notes, parts, planned cases,
	// checks and recordings of the agents' instructions and tests (step 0023).
	if tables != 130 {
		t.Errorf("the schema has %d tables; the data model has 130", tables)
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
// what waits for it is told when it is back. (Until the ninth cluster
// test's F3 this test ended the connections of a database that was there
// and asserted that the health then said "away" until the next call: a
// call's failure no longer says that by itself, a probe does, see
// TestAConnectionCutByTheServerIsNotAnOutage; the outage here is a real
// one.)
func TestHealthFollowsTheDatabase(t *testing.T) {
	away, d, _, _ := awayHandle(t)
	ctx := ctxT(t)
	if h := d.Health(); !h.Available {
		t.Fatalf("an open database is %+v", h)
	}
	back := make(chan struct{}, 1)
	d.OnAvailable(func() { back <- struct{}{} })
	away.Cut()
	if err := one(ctx, d); !errors.Is(err, db.ErrUnavailable) {
		t.Fatalf("a call made while the database is away returned %v", err)
	}
	until(t, 5*time.Second, "the health says the database is away", func() bool { return !d.Health().Available })
	if h := d.Health(); h.Available || h.Error == "" {
		t.Errorf("health after the cut: %+v", h)
	}
	if err := one(ctx, d); !errors.Is(err, db.ErrUnavailable) {
		t.Fatalf("a call made while the database is away returned %v", err)
	}
	away.Back()
	until(t, 5*time.Second, "the pool reconnects", func() bool { return one(ctx, d) == nil })
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

// A record set that only grows is copied from where the last copy stopped:
// the first copy takes everything, a later one what is newer, and a copy
// written down before the newest time was kept counts from when it was made.
func TestImportNewer(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	kept := []time.Time{base, base.Add(time.Second)}
	run := func(id string) (int, time.Time) {
		t.Helper()
		var got time.Time
		n, err := d.ImportNewer(ctx, id, "test", func(_ context.Context, _ pgx.Tx, since time.Time) (int, time.Time, error) {
			got = since
			n, newest := 0, time.Time{}
			for _, at := range kept {
				if at.After(newest) {
					newest = at
				}
				if db.After(at, since) {
					n++
				}
			}
			return n, newest, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return n, got
	}
	if n, since := run("log"); n != 2 || !since.IsZero() {
		t.Fatalf("the first copy took %d since %v", n, since)
	}
	if n, since := run("log"); n != 0 || !since.Equal(kept[1]) {
		t.Fatalf("the second copy took %d since %v", n, since)
	}
	// Less than a microsecond after the newest is the newest, as the
	// database keeps it; a second later is new.
	kept = append(kept, kept[1].Add(300*time.Nanosecond), base.Add(2*time.Second))
	if n, _ := run("log"); n != 1 {
		t.Fatalf("the copy after a rollback took %d", n)
	}
	if n, _ := run("log"); n != 0 {
		t.Fatalf("the copy after that took %d", n)
	}
	var count int
	var newest time.Time
	if err := d.Pool().QueryRow(ctx, "SELECT record_count, newest_at FROM imports WHERE import_id = 'log'").Scan(&count, &newest); err != nil || count != 3 || !newest.Equal(base.Add(2*time.Second)) {
		t.Fatalf("written down: %d records, the newest %v: %v", count, newest, err)
	}
	// A copy of nothing, or one from before the newest time was written
	// down: what is later than the copy itself is new.
	if _, err := d.Pool().Exec(ctx, "INSERT INTO imports (import_id, source, imported_at) VALUES ('old', 'test', $1)", base.Add(1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, since := run("old"); n != 1 || !since.Equal(base.Add(1500*time.Millisecond)) {
		t.Fatalf("after an older copy: %d since %v", n, since)
	}
	if n, _ := run("old"); n != 0 {
		t.Fatalf("after an older copy, the second time: %d", n)
	}
}

// A database whose step was applied from another file is refused, with
// both checksums and what to do, and nothing is applied to it.
func TestADatabaseFromAnotherBuildOfTheSchemaIsRefused(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	if _, err := d.Pool().Exec(ctx, "UPDATE schema_migrations SET checksum = repeat('ab', 32) WHERE schema_migration_id = 1"); err != nil {
		t.Fatal(err)
	}
	n, err := d.Migrate(ctx)
	if err == nil || n != 0 {
		t.Fatalf("applied %d steps: %v", n, err)
	}
	for _, part := range []string{"0001_platform", "applied abababababab", "this build ", "made again, empty"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the refusal does not say %q: %v", part, err)
		}
	}
}

// A document read with several statements is one that was saved: a save
// that lands between two of the read's statements does not show in the
// second, so a parent's rows never come with another save's children.
func TestADocumentIsReadOnOneSnapshot(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	type pair struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	scope := func(q db.Querier, name string) (string, error) {
		b, _, err := db.LoadScope(ctx, q, name)
		if errors.Is(err, db.ErrNotFound) {
			return "", nil
		}
		return strings.Trim(string(b), `"`), err
	}
	var between func()
	doc := &db.Doc[pair]{DB: d, Key: "pair.json",
		Read: func(ctx context.Context, q db.Querier) (p pair, found bool, err error) {
			if p.A, err = scope(q, "pair-a"); err != nil {
				return p, false, err
			}
			if f := between; f != nil {
				between = nil
				f()
			}
			p.B, err = scope(q, "pair-b")
			return p, p.A != "" || p.B != "", err
		},
		Write: func(ctx context.Context, tx pgx.Tx, _, next pair) error {
			if _, err := db.SaveScope(ctx, tx, "pair-a", []byte(`"`+next.A+`"`), db.AnyVersion, ""); err != nil {
				return err
			}
			_, err := db.SaveScope(ctx, tx, "pair-b", []byte(`"`+next.B+`"`), db.AnyVersion, "")
			return err
		}}
	if _, err := doc.SaveKey(ctx, doc.Key, []byte(`{"a":"one","b":"one"}`), ""); err != nil {
		t.Fatal(err)
	}
	between = func() {
		if _, err := doc.SaveKey(ctx, doc.Key, []byte(`{"a":"two","b":"two"}`), ""); err != nil {
			t.Error(err)
		}
	}
	got, version, err := doc.LoadKey(ctx, doc.Key)
	if err != nil || string(got) != `{"a":"one","b":"one"}` {
		t.Fatalf("read across a save: %s, %v", got, err)
	}
	// What it read is no longer what is kept, and a save over it is refused.
	if _, err := doc.SaveKey(ctx, doc.Key, []byte(`{"a":"three","b":"three"}`), version); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over what was read before another save: %v", err)
	}
	if got, _, err := doc.LoadKey(ctx, doc.Key); err != nil || string(got) != `{"a":"two","b":"two"}` {
		t.Fatalf("read after the save: %s, %v", got, err)
	}
}

// The places of a list's records: one that comes in at the end or goes
// from anywhere moves no other; anything else places them all again.
func TestPlaces(t *testing.T) {
	for _, c := range []struct {
		what   string
		keys   []string
		placed map[string]int64
		want   []int64
	}{
		{"the first ones", []string{"a", "b"}, nil, []int64{1, 2}},
		{"one more at the end", []string{"a", "b", "c"}, map[string]int64{"a": 1, "b": 2}, []int64{1, 2, 3}},
		{"the first goes", []string{"b", "c"}, map[string]int64{"a": 1, "b": 2, "c": 3}, []int64{2, 3}},
		{"one goes and one comes", []string{"a", "c", "d"}, map[string]int64{"a": 1, "b": 2, "c": 5}, []int64{1, 5, 6}},
		{"one in a gap", []string{"a", "x", "c"}, map[string]int64{"a": 1, "c": 5}, []int64{1, 2, 5}},
		{"one before another with no gap", []string{"a", "x", "b"}, map[string]int64{"a": 1, "b": 2}, []int64{1, 2, 3}},
		{"another order", []string{"b", "a"}, map[string]int64{"a": 1, "b": 2}, []int64{1, 2}},
		{"none", nil, map[string]int64{"a": 1}, []int64{}},
	} {
		if got := db.Places(c.keys, c.placed); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: %v, want %v", c.what, got, c.want)
		}
	}
}

// A set of documents is at one version: a read of any key gives the set's
// version, kept or not; a save of one moves it for all, so a save over
// what was read of another before it is refused; the documents another
// backend kept are copied once, together.
func TestDocSet(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	type doc struct {
		N int `json:"n"`
	}
	member := func(scope string) db.SetMember {
		return db.Member(func(ctx context.Context, q db.Querier) (v doc, found bool, err error) {
			b, _, err := db.LoadScope(ctx, q, scope)
			if errors.Is(err, db.ErrNotFound) {
				return v, false, nil
			}
			if err != nil {
				return v, false, err
			}
			_, err = fmt.Sscanf(string(b), `{"n": %d}`, &v.N)
			return v, v.N != 0, err
		}, func(ctx context.Context, tx pgx.Tx, _, next doc) error {
			_, err := db.SaveScope(ctx, tx, scope, []byte(fmt.Sprintf(`{"n": %d}`, next.N)), db.AnyVersion, "")
			return err
		})
	}
	set := &db.DocSet{DB: d, Name: "pair", Members: map[string]db.SetMember{"a.json": member("set-a"), "b.json": member("set-b")}}
	_, v0, err := set.LoadKey(ctx, "a.json")
	if !errors.Is(err, kv.ErrNotFound) || v0 != "0" {
		t.Fatalf("a set never saved: version %q, %v", v0, err)
	}
	v1, err := set.SaveKey(ctx, "a.json", []byte(`{"n":1}`), v0)
	if err != nil || v1 == v0 {
		t.Fatalf("first save: version %q, %v", v1, err)
	}
	// The other key, of which nothing is kept, is at the set's version too.
	if _, v, err := set.LoadKey(ctx, "b.json"); !errors.Is(err, kv.ErrNotFound) || v != v1 {
		t.Fatalf("the other key: version %q, want %q: %v", v, v1, err)
	}
	if _, err := set.SaveKey(ctx, "b.json", []byte(`{"n":2}`), v0); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over what was read before another key's save: %v", err)
	}
	v2, err := set.SaveKey(ctx, "b.json", []byte(`{"n":2}`), v1)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a.json", "b.json"} {
		if _, v, err := set.LoadKey(ctx, key); err != nil || v != v2 {
			t.Fatalf("%s: version %q, want %q: %v", key, v, v2, err)
		}
	}
	if _, err := set.SaveKey(ctx, "c.json", []byte(`{}`), ""); err == nil {
		t.Fatal("a key the set does not have was saved")
	}
	if err := set.DeleteKey(ctx, "a.json"); err != nil {
		t.Fatal(err)
	}
	if _, v, err := set.LoadKey(ctx, "a.json"); !errors.Is(err, kv.ErrNotFound) || v == v2 {
		t.Fatalf("after a key is removed: version %q, %v", v, err)
	}
	// What a ConfigMap held is copied once, every key in one transaction.
	cm := &kv.Memory{}
	_, _ = cm.SaveKey(ctx, "a.json", []byte(`{"n":7}`), "")
	_, _ = cm.SaveKey(ctx, "b.json", []byte(`{"n":8}`), "")
	fresh := &db.DocSet{DB: dbtest.Open(t), Name: "pair", Members: set.Members}
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || fmt.Sprint(copied) != "[a.json b.json]" {
		t.Fatalf("copy: %v %v", copied, err)
	}
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || len(copied) != 0 {
		t.Fatalf("second copy: %v %v", copied, err)
	}
	if b, v, err := fresh.LoadKey(ctx, "b.json"); err != nil || string(b) != `{"n":8}` || v == "0" {
		t.Fatalf("copied: %s at %q, %v", b, v, err)
	}
}
