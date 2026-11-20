package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The sign-in sessions in the database (DB-66) through an outage of a
// real database, taken away in the middle, through the handlers: two
// replicas, each with its own connections, its own listener and its own
// watch of the database's health, as two pods have.

// longestRequest is the most a request may take while the database is
// away. A database that is not there refuses at once; one that swallows
// what it is sent holds the one request that tries it for the 2 seconds a
// read or write of a session's row is given, and is then left alone.
const longestRequest = 4 * time.Second

// longestSweep is the most the sweep may take then. It is no request: it
// runs each minute beside them, and reads every session's row, which is
// given 5 seconds.
const longestSweep = 7 * time.Second

type databaseOutage struct {
	*outage
	away *dbtest.Away
	dbs  []*db.DB
	envs []*env
	// slowest is the longest request made while the database was away.
	slowest time.Duration
	// timedN is how many requests were made then, and waitedN how many of
	// them waited more than a second.
	timedN, waitedN int
	isAway          bool
}

func newDatabaseOutage(t *testing.T) *databaseOutage {
	t.Helper()
	// First: without a database to test against the test is skipped, before
	// anything is set up that checks what the test saw.
	server := dbtest.Config(t)
	o := &databaseOutage{outage: newOutage(t)}
	var cfg db.Config
	o.away, cfg = dbtest.NewAway(t, server)
	for i := range 2 {
		d := dbtest.OpenConfig(t, cfg)
		if i == 0 {
			if _, err := d.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go d.Listen(ctx)
		go d.Watch(ctx, 100*time.Millisecond)
		o.dbs = append(o.dbs, d)
		o.envs = append(o.envs, o.replica(outageConfig(t), func(c *auth.Config) { c.SessionStore = auth.NewPGSessions(d, nil) }))
	}
	return o
}

// timed runs a request and, while the database is away, holds it to
// longestRequest.
func (o *databaseOutage) timed(what string, fn func()) {
	o.t.Helper()
	start := time.Now()
	fn()
	took := time.Since(start)
	if !o.isAway {
		return
	}
	o.timedN++
	if took > 1500*time.Millisecond {
		o.waitedN++
	}
	if took > o.slowest {
		o.slowest = took
	}
	if took > longestRequest {
		o.t.Errorf("%s took %s while the database was away", what, took)
	}
}

func (o *databaseOutage) who(e *env, c *http.Client, what string) (w who, code int) {
	o.t.Helper()
	o.timed(what, func() { w, code = e.whoami(c) })
	return w, code
}

func (o *databaseOutage) signOut(e *env, c *http.Client) {
	o.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.app.URL+"/signout", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	o.timed("a sign-out", func() {
		resp, err := c.Do(req)
		if err != nil {
			o.t.Fatal(err)
		}
		resp.Body.Close()
	})
}

// comesBack brings the database back and does what the console's wiring
// does when its health says so (db.OnAvailable): the sessions are told.
func (o *databaseOutage) comesBack() {
	o.t.Helper()
	o.away.Back()
	o.isAway = false
	for _, d := range o.dbs {
		deadline := time.Now().Add(20 * time.Second)
		for !d.Health().Available {
			if time.Now().After(deadline) {
				o.t.Fatal("the database's health did not say it is back")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, e := range o.envs {
		e.svc.Sessions().StoreIsBack()
	}
}

func (o *databaseOutage) rows(where string, args ...any) int {
	o.t.Helper()
	var n int
	if err := o.dbs[0].Pool().QueryRow(context.Background(), "SELECT count(*) FROM auth_sessions WHERE "+where, args...).Scan(&n); err != nil {
		o.t.Fatal(err)
	}
	return n
}

// DB-66 with DB-65, the invariant of the design: with the database taken
// away, a person signed in before keeps working on the replicas that have
// seen them; a local SuperAdmin signs in and gets a signed session that
// every replica takes; when the database is back both go on without a new
// sign-in and the signed one has a row; a session signed out during the
// outage is refused after, on every replica; and no request waits longer
// than a few seconds, whether the database refuses or swallows what it is
// sent. No cookie's value is in a row or a log line.
func TestSessionsThroughAnOutageOfTheDatabase(t *testing.T) {
	for name, takeAway := range map[string]func(*dbtest.Away){
		"a database that is not there":             (*dbtest.Away).Cut,
		"a database that swallows what it is sent": (*dbtest.Away).Hang,
	} {
		t.Run(name, func(t *testing.T) {
			o := newDatabaseOutage(t)
			a, b := o.envs[0], o.envs[1]
			for _, name := range []string{"ann", "bob", "dan"} {
				o.create(a, name, "web-readers")
			}
			reread(t, b)

			// Before: three people sign in, each with a session that is a row.
			ann, bob, dan := a.mustSignIn("ann", goodPassword), a.mustSignIn("bob", goodPassword), a.mustSignIn("dan", goodPassword)
			annCookie, bobCookie := o.cookie(a, ann), o.cookie(a, bob)
			o.cookie(a, dan)
			if n := o.rows("true"); n != 3 {
				t.Fatalf("%d rows after three sign-ins", n)
			}
			// The other replica has seen ann and bob, and never dan.
			for _, c := range []*http.Client{ann, bob} {
				if w, code := o.who(b, c, ""); code != http.StatusOK || w.User == "" {
					t.Fatalf("a session signed in on one replica, on the other: %+v %d", w, code)
				}
			}

			takeAway(o.away)
			o.isAway = true
			// Past what a replica holds of a row, so every request from here
			// would ask the database if it were asked.
			time.Sleep(auth.SessionHeldFor + 200*time.Millisecond)

			// A person signed in before keeps working, on both replicas that
			// have seen them, request after request and minute after minute.
			for range 3 {
				for _, e := range []*env{a, b} {
					for range 4 {
						if w, code := o.who(e, ann, "a request of a person signed in before"); code != http.StatusOK || w.User != "ann" {
							t.Fatalf("a person signed in before the outage: %+v %d", w, code)
						}
					}
				}
				*o.now = o.now.Add(time.Minute)
			}
			// On a replica that never saw them nothing says who they are:
			// they are asked to sign in (and would get a signed session).
			if _, code := o.who(b, dan, "a request to a replica that never saw the session"); code != http.StatusUnauthorized {
				t.Fatalf("a session the replica never saw and cannot ask about: %d", code)
			}
			if w, code := o.who(a, dan, "a request"); code != http.StatusOK || w.User != "dan" {
				t.Fatalf("the replica that signed dan in: %+v %d", w, code)
			}

			// A local SuperAdmin signs in, through the form, on the replica
			// that has not seen them: a signed session, taken by both.
			var root *http.Client
			o.timed("a sign-in", func() { root = o.signInAway(b, "root", goodPassword) })
			signed := o.cookie(b, root)
			for _, e := range []*env{a, b} {
				if w, code := o.who(e, root, "a request of a signed session"); code != http.StatusOK || w.User != "root" || !w.Admin {
					t.Fatalf("the SuperAdmin signed in during the outage: %+v %d", w, code)
				}
				var page string
				o.timed("a page", func() {
					resp, err := root.Get(e.app.URL + "/")
					if err != nil {
						t.Fatal(err)
					}
					page = readAll(t, resp)
				})
				if !strings.Contains(page, "the app") {
					t.Fatalf("a page for the SuperAdmin signed in during the outage: %q", page)
				}
			}

			// bob signs out during the outage: refused at once where he did.
			o.signOut(a, bob)
			thief := o.holding(a, bobCookie)
			if _, code := o.who(a, thief, "a request with a signed-out cookie"); code != http.StatusUnauthorized {
				t.Fatalf("a session signed out during the outage, on the replica that signed it out: %d", code)
			}
			// The store is tried again every ten seconds by one request, and
			// the sweep runs: nothing this replica holds is dropped.
			var slowestSweep time.Duration
			for range 2 {
				*o.now = o.now.Add(11 * time.Second)
				for _, e := range []*env{a, b} {
					start := time.Now()
					e.svc.Sweep()
					if took := time.Since(start); took > longestSweep {
						t.Errorf("a sweep took %s while the database was away", took)
					} else if took > slowestSweep {
						slowestSweep = took
					}
					if _, code := o.who(e, ann, "a request that tries the database again"); code != http.StatusOK {
						t.Fatalf("a person signed in before the outage, later in it: %d", code)
					}
					if _, code := o.who(e, root, "a request of a signed session"); code != http.StatusOK {
						t.Fatalf("the SuperAdmin signed in during the outage, later in it: %d", code)
					}
				}
			}
			t.Logf("with %s: %d requests, %d of them waited more than 1.5 s, the slowest %s; the slowest sweep %s", name, o.timedN, o.waitedN, o.slowest, slowestSweep)
			if o.slowest == 0 {
				t.Fatal("no request was timed while the database was away")
			}

			o.comesBack()
			*o.now = o.now.Add(11 * time.Second)

			// The session signed out during the outage is refused on every
			// replica: at once where it was signed out, and on the other as
			// soon as what it holds of the row runs out, since the row is gone.
			if n := o.rows("username = 'bob'"); n != 0 {
				t.Fatalf("the signed-out session still has %d rows after the database is back", n)
			}
			if _, code := a.whoami(thief); code != http.StatusUnauthorized {
				t.Fatalf("a session signed out during the outage, after it: %d", code)
			}
			deadline := time.Now().Add(auth.SessionHeldFor + 2*time.Second)
			for {
				if _, code := b.whoami(thief); code == http.StatusUnauthorized {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the other replica still takes a session signed out during the outage %s after the database is back", auth.SessionHeldFor)
				}
				time.Sleep(50 * time.Millisecond)
			}

			// ann goes on, with the cookie she had, on both replicas.
			for _, e := range []*env{a, b} {
				if w, code := e.whoami(ann); code != http.StatusOK || w.User != "ann" {
					t.Fatalf("a person signed in before the outage, after it: %+v %d", w, code)
				}
			}
			if o.cookie(a, ann) != annCookie {
				t.Fatal("the person signed in before the outage was given another session")
			}
			// The SuperAdmin goes on too: the first request makes the signed
			// session a row and sets the usual cookie; nobody signs in again.
			if w, code := a.whoami(root); code != http.StatusOK || w.User != "root" || !w.Admin {
				t.Fatalf("the request that exchanges the signed session: %+v %d", w, code)
			}
			stored := o.cookie(a, root)
			if stored == signed || strings.Contains(stored, ".") || len(stored) != 43 {
				t.Fatalf("after the database is back the cookie is not a session's ID: %d characters", len(stored))
			}
			if n := o.rows("username = 'root' AND subject = 'local:root' AND method = 'local' AND is_admin"); n != 1 {
				t.Fatalf("the signed session has %d rows after the database is back", n)
			}
			*o.now = o.now.Add(2 * time.Hour)
			for _, e := range []*env{a, b} {
				if w, code := e.whoami(root); code != http.StatusOK || !w.Admin {
					t.Fatalf("two hours on, long past the signed session's half hour: %+v %d", w, code)
				}
			}
			// ann, dan and root: three rows, and bob's gone.
			if n := o.rows("true"); n != 3 {
				t.Fatalf("%d rows at the end", n)
			}

			// No cookie's value, and no part of a signed one, is in any row.
			// (The logs and the audit records are checked when the test ends.)
			for _, c := range o.cookies {
				parts := []string{c}
				if payload, mac, ok := auth.SplitSignedForTest(c); ok {
					parts = append(parts, payload, mac)
				}
				for _, part := range parts {
					if in := dbtest.Holding(t, o.dbs[0], part); len(in) != 0 {
						t.Errorf("a session cookie's value is in the database: %v", in)
					}
				}
			}
			if in := dbtest.Holding(t, o.dbs[0], "local:root"); in["auth_sessions"] != 1 {
				t.Fatalf("the search that found no cookie does not find the session's row either: %v", in)
			}
		})
	}
}
