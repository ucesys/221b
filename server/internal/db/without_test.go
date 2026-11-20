package db_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The handle of a console started without its database (DB-67) refuses
// every call at once, says the database is away, and never makes a
// connection: not to a database that is away, and not to one that is
// there. It needs no database to be made.
func TestTheHandleOfAStartWithoutTheDatabaseRefusesEverything(t *testing.T) {
	// A server that counts the connections made to it, and answers none.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var dialed atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dialed.Add(1)
			_ = c.Close()
		}
	}()
	cfg := db.Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Name: "221b", User: "app_221b", SSLMode: "disable", MaxConns: 4}
	d, err := db.OpenWithout(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	refuses(t, d)
	if n := dialed.Load(); n != 0 {
		t.Errorf("%d connections were made to the database", n)
	}
	if _, err := db.OpenWithout(db.Config{Host: "db", Port: 5432, Name: "221b", User: "u", SSLMode: "sometimes", MaxConns: 4}, nil); err == nil {
		t.Error("settings that cannot work were taken")
	}
}

// The same over a database that answers: a console started without its
// database leaves it alone also once it is back.
func TestTheHandleOfAStartWithoutTheDatabaseLeavesADatabaseThatIsBackAlone(t *testing.T) {
	cfg := dbtest.Config(t)
	d, err := db.OpenWithout(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	refuses(t, d)
	// The database is there, and nothing was made in it.
	real := dbtest.OpenConfig(t, cfg)
	var n int
	if err := real.Do(context.Background(), func(q db.Querier) error {
		return q.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("the database holds %d tables: %v", n, err)
	}
	if real.StartedWithout() {
		t.Error("an open database says it was started without")
	}
}

func refuses(t *testing.T, d *db.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !d.StartedWithout() {
		t.Fatal("the handle does not say the console was started without its database")
	}
	called := false
	d.OnAvailable(func() { called = true })
	calls := map[string]func() error{
		"Tx":       func() error { return d.Tx(ctx, func(pgx.Tx) error { called = true; return nil }) },
		"Do":       func() error { return d.Do(ctx, func(db.Querier) error { called = true; return nil }) },
		"Snapshot": func() error { return d.Snapshot(ctx, func(db.Querier) error { called = true; return nil }) },
		"Migrate":  func() error { _, err := d.Migrate(ctx); return err },
		"Imported": func() error { _, err := d.Imported(ctx, "secret/clusters"); return err },
		"ImportOnce": func() error {
			_, err := d.ImportOnce(ctx, "secret/clusters", "test", func(context.Context, pgx.Tx) (int, error) { called = true; return 0, nil })
			return err
		},
		"ImportNewer": func() error {
			_, err := d.ImportNewer(ctx, "audit", "test", func(context.Context, pgx.Tx, time.Time) (int, time.Time, error) {
				called = true
				return 0, time.Time{}, nil
			})
			return err
		},
		"TryLock": func() error { _, _, err := d.TryLock(ctx, "sweep"); return err },
		"Sweep": func() error {
			_, err := d.Sweep(ctx, "sweep", func(context.Context) error { called = true; return nil })
			return err
		},
		"ForgetClaims":  func() error { _, err := d.ForgetClaims(ctx, time.Now()); return err },
		"SchemaVersion": func() error { _, err := d.SchemaVersion(ctx); return err },
	}
	for name, call := range calls {
		start := time.Now()
		err := call()
		if !errors.Is(err, db.ErrUnavailable) || !errors.Is(err, db.ErrStartedWithout) {
			t.Errorf("%s: %v", name, err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("%s took %s to refuse", name, took)
		}
	}
	// Whoever goes around the handle to its pool is refused a connection.
	if _, err := d.Pool().Exec(ctx, "SELECT 1"); !errors.Is(err, db.ErrStartedWithout) {
		t.Errorf("the pool: %v", err)
	}
	// It hears nothing and watches nothing: both return at once.
	done := make(chan struct{})
	go func() { d.Listen(ctx); d.Watch(ctx, time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the listener or the watch of a handle without a database runs")
	}
	if h := d.Health(); h.Available || h.Error == "" || h.Since.IsZero() {
		t.Errorf("the health reads %+v", h)
	}
	if called {
		t.Error("something was run, or told the database is back")
	}
}
