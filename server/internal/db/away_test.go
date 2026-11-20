package db_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// While the database is away nothing waits for it (DB-14): the tests of
// the rule in away.go, on a real PostgreSQL behind a relay that is taken
// away while the handle is in use.

func one(ctx context.Context, d *db.DB) error {
	return d.Do(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT 1"); return err })
}

// until waits for ok, and says how long it took.
func until(t *testing.T, within time.Duration, what string, ok func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !ok() {
		if time.Since(start) > within {
			t.Fatalf("%s: not in %s", what, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(start)
}

// awayHandle is a handle on a database of the test's own, behind a relay.
func awayHandle(t *testing.T) (*dbtest.Away, *db.DB, db.Config, db.Config) {
	t.Helper()
	direct := dbtest.Config(t)
	away, relayed := dbtest.NewAway(t, direct)
	return away, dbtest.OpenConfig(t, relayed), relayed, direct
}

// everyCall is one of each kind of call a repository makes.
func everyCall(d *db.DB) map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"a read": func(ctx context.Context) error { return one(ctx, d) },
		"a transaction": func(ctx context.Context) error {
			return d.Tx(ctx, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, "SELECT 1"); return err })
		},
		"a snapshot": func(ctx context.Context) error {
			return d.Snapshot(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT 1"); return err })
		},
		"a lock": func(ctx context.Context) error {
			l, _, err := d.TryLock(ctx, "away-test")
			l.Unlock()
			return err
		},
		"a sweep": func(ctx context.Context) error {
			_, err := d.Sweep(ctx, "away-test-sweep", func(context.Context) error { return nil })
			return err
		},
		"the claims": func(ctx context.Context) error {
			_, err := d.ForgetClaims(ctx, time.Now())
			return err
		},
		"a copy's record": func(ctx context.Context) error {
			_, err := d.Imported(ctx, "nothing")
			return err
		},
		"the schema's version": func(ctx context.Context) error {
			_, err := d.SchemaVersion(ctx)
			return err
		},
	}
}

// With the database gone, by either way of going, the first call may wait
// (for the probe, or two seconds and the probe); from then on every call
// of every kind returns ErrUnavailable at once, whatever time it was
// given, and none; and when the database is back the calls go through
// again within a few seconds, the listener hears again, and what waited is
// told.
func TestNothingWaitsForADatabaseThatIsAway(t *testing.T) {
	for name, takeAway := range map[string]func(*dbtest.Away){
		"a database that is not there":             (*dbtest.Away).Cut,
		"a database that swallows what it is sent": (*dbtest.Away).Hang,
	} {
		t.Run(name, func(t *testing.T) {
			away, d, relayed, direct := awayHandle(t)
			if _, err := d.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var heard atomic.Int32
			d.Subscribe("away-test", func() { heard.Add(1) })
			go d.Listen(ctx)
			var told atomic.Int32
			d.OnAvailable(func() { told.Add(1) })
			until(t, 5*time.Second, "the listener listens", func() bool { return heard.Load() > 0 })
			// A few connections are open and idle, as in a console in use.
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_ = d.Do(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT pg_sleep(0.2)"); return err })
				}()
			}
			wg.Wait()

			takeAway(away)
			// The first calls, several at once and given no time of their
			// own: a request's context has none.
			start := time.Now()
			firsts := make(chan error, 6)
			for range 6 {
				go func() { firsts <- one(context.Background(), d) }()
			}
			for range 6 {
				select {
				case err := <-firsts:
					if !errors.Is(err, db.ErrUnavailable) {
						t.Fatalf("a call that met the outage returned %v", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("a call given no time of its own was left waiting for a database that is away")
				}
			}
			first := time.Since(start)
			if first > 6*time.Second {
				t.Errorf("the first calls waited %s", first)
			}
			found := until(t, 6*time.Second, "the health says the database is away", func() bool { return !d.Health().Available })
			h := d.Health()
			if h.Error == "" || h.Since.IsZero() {
				t.Errorf("the health of a database that is away: %+v", h)
			}
			// From here nothing waits, whatever is asked and however.
			var slowest time.Duration
			for range 5 {
				for what, call := range everyCall(d) {
					for _, cctx := range []context.Context{context.Background(), ctx} {
						begun := time.Now()
						err := call(cctx)
						took := time.Since(begun)
						if !errors.Is(err, db.ErrUnavailable) {
							t.Fatalf("%s while the database is away returned %v", what, err)
						}
						if took > slowest {
							slowest = took
						}
						if took > 100*time.Millisecond {
							t.Errorf("%s waited %s for a database known to be away", what, took)
						}
					}
				}
				time.Sleep(700 * time.Millisecond)
			}
			if d.Health().Available {
				t.Fatal("the health says the database is back while it is away")
			}
			if n := told.Load(); n != 0 {
				t.Errorf("what waits for the database was told %d times while it was away", n)
			}

			away.Back()
			back := until(t, 6*time.Second, "the health says the database is back", func() bool { return d.Health().Available })
			for what, call := range everyCall(d) {
				if err := call(ctx); err != nil {
					t.Errorf("%s after the database is back: %v", what, err)
				}
			}
			until(t, 5*time.Second, "what waits for the database is told", func() bool { return told.Load() == 1 })
			// The listener listens again: a notice sent by another console
			// is heard.
			other := dbtest.OpenConfig(t, direct)
			was := heard.Load()
			until(t, 10*time.Second, "the listener hears a notice after the outage", func() bool {
				_ = other.Tx(ctx, func(tx pgx.Tx) error { return other.Notify(ctx, tx, "away-test") })
				time.Sleep(50 * time.Millisecond)
				return heard.Load() > was+1
			})
			t.Logf("%s: the first calls returned after %s, the health said away after %s more, the slowest call after that took %s, and the database was back %s after it returned", name, first.Round(time.Millisecond), found.Round(time.Millisecond), slowest.Round(time.Microsecond), back.Round(time.Millisecond))
			_ = relayed
		})
	}
}

// A slow query on a database that answers is that query's own affair: the
// health does not change, no other call is refused or held, and a query
// given the time it needs finishes.
func TestASlowQueryDoesNotTakeTheDatabaseAway(t *testing.T) {
	_, d, _, _ := awayHandle(t)
	ctx := context.Background()
	var told atomic.Int32
	d.OnAvailable(func() { told.Add(1) })
	stop := make(chan struct{})
	var refused, made atomic.Int32
	var slowest atomic.Int64
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			begun := time.Now()
			if err := one(ctx, d); err != nil {
				refused.Add(1)
			}
			if took := int64(time.Since(begun)); took > slowest.Load() {
				slowest.Store(took)
			}
			made.Add(1)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	// One that runs out of its own time, and one given the time it needs,
	// which is longer than a call waits before the probe is asked.
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err := d.Do(short, func(q db.Querier) error { _, err := q.Exec(short, "SELECT pg_sleep(5)"); return err })
	cancel()
	if !errors.Is(err, db.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a query that ran out of its time returned %v", err)
	}
	begun := time.Now()
	if err := d.Do(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT pg_sleep(3.5)"); return err }); err != nil {
		t.Errorf("a slow query given its time: %v", err)
	}
	if took := time.Since(begun); took < 3*time.Second {
		t.Fatalf("the slow query took %s", took)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	if !d.Health().Available || told.Load() != 0 {
		t.Errorf("a slow query changed the health: %+v, told %d times", d.Health(), told.Load())
	}
	if refused.Load() != 0 || made.Load() < 100 {
		t.Errorf("%d of %d calls made beside the slow queries failed", refused.Load(), made.Load())
	}
	if s := time.Duration(slowest.Load()); s > time.Second {
		t.Errorf("a call beside the slow queries took %s", s)
	}
}

// Connections ended by the server, with the server there (an
// administrator's pg_terminate_backend, a failover's old leader): the call
// that was cut says so, the probe finds the database, and nothing else is
// refused.
func TestAConnectionCutByTheServerIsNotAnOutage(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	var told atomic.Int32
	d.OnAvailable(func() { told.Add(1) })
	err := d.Do(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database()")
		return err
	})
	if !errors.Is(err, db.ErrUnavailable) {
		t.Fatalf("a call cut off by the server returned %v", err)
	}
	for range 50 {
		if err := one(ctx, d); err != nil {
			t.Fatalf("the pool did not reconnect: %v", err)
		}
		if !d.Health().Available {
			t.Fatal("a connection cut by a server that is there was taken for an outage")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if told.Load() != 0 {
		t.Error("the database was said to be back although it never left")
	}
}

// What the health says of an outage is the console's sentence: the
// driver's text, with the database's user, its name and its address, is
// for the log.
func TestTheHealthNeverShowsTheDriversText(t *testing.T) {
	for name, takeAway := range map[string]func(*dbtest.Away){"cut": (*dbtest.Away).Cut, "hang": (*dbtest.Away).Hang} {
		t.Run(name, func(t *testing.T) {
			away, d, relayed, _ := awayHandle(t)
			takeAway(away)
			_ = one(context.Background(), d)
			until(t, 8*time.Second, "the health says the database is away", func() bool { return !d.Health().Available })
			why := d.Health().Error
			for what, s := range map[string]string{"the user": relayed.User, "the database's name": relayed.Name, "the host": relayed.Host, "the port": strconv.Itoa(relayed.Port), "the driver's words": "failed to connect"} {
				if strings.Contains(why, s) {
					t.Errorf("the health's reason holds %s: %q", what, why)
				}
			}
			if why == "" {
				t.Error("the health gives no reason")
			}
		})
	}
}

// A call given no time of its own, on a database that goes away just
// after a probe was answered (a slow query a moment before), is not left
// waiting: it asks for the probe again for as long as it waits.
func TestACallLeftWaitingAsksForTheProbeAgain(t *testing.T) {
	away, d, _, _ := awayHandle(t)
	ctx := context.Background()
	// A slow query, whose probe is answered.
	if err := d.Do(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT pg_sleep(2.3)"); return err }); err != nil {
		t.Fatal(err)
	}
	away.Hang()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- one(ctx, d) }()
	select {
	case err := <-done:
		if !errors.Is(err, db.ErrUnavailable) {
			t.Fatalf("the call returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a call given no time of its own was left waiting")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the call waited %s", took)
	} else {
		t.Logf("the call waited %s", took.Round(time.Millisecond))
	}
}
