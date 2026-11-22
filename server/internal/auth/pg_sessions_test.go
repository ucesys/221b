package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/roles"
)

// The sign-in sessions in the database (DB-66). The tests two stores
// share are in shared_test.go; these are the table's own.

// twoOnOneDatabase is two replicas' connections to one migrated database.
// poll is how often each listener looks without a notice; listens says
// whether the second replica hears notices at all.
func twoOnOneDatabase(t *testing.T, poll time.Duration, listens bool) (*db.DB, *db.DB) {
	t.Helper()
	cfg := dbtest.Config(t)
	cfg.Poll = poll
	open := func(listen bool) *db.DB {
		d := dbtest.OpenConfig(t, cfg)
		if listen {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			heard := make(chan struct{}, 1)
			d.Subscribe("the listener is up", func() {
				select {
				case heard <- struct{}{}:
				default:
				}
			})
			go d.Listen(ctx)
			select {
			case <-heard:
			case <-time.After(10 * time.Second):
				t.Fatal("the listener did not start")
			}
		}
		return d
	}
	a := open(true)
	if _, err := a.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, open(listens)
}

func sessionsOn(store SharedStore, now func() time.Time) *Sessions {
	s := NewSessions(8*time.Hour, 7*24*time.Hour, now)
	s.Share(store, nil)
	return s
}

// A sign-out on one replica is refused on the other as soon as its notice
// arrives: nothing but the notice can have told it here, since the
// listeners do not poll and the copy a replica holds lasts 5 seconds.
func TestASignOutElsewhereIsRefusedWhenItsNoticeArrives(t *testing.T) {
	da, dbb := twoOnOneDatabase(t, time.Hour, true)
	a, b := sessionsOn(NewPGSessions(da, nil), nil), sessionsOn(NewPGSessions(dbb, nil), nil)
	ann := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
	cy := a.Start(Session{User: User{Username: "cy"}, Method: "local"})
	for _, id := range []string{ann.ID, cy.ID} {
		if _, ok := b.Get(id); !ok {
			t.Fatal("a session signed in on one replica is unknown on the other")
		}
	}
	start := time.Now()
	a.Delete(ann.ID)
	for {
		if _, ok := b.Get(ann.ID); !ok {
			break
		}
		if time.Since(start) > SessionHeldFor-time.Second {
			t.Fatalf("the other replica still took a signed-out session %s after: the notice did not end it", time.Since(start))
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("a sign-out reached the other replica in %s", time.Since(start))
	if time.Since(start) > 2*time.Second {
		t.Errorf("a sign-out took %s to reach the other replica with its listener up", time.Since(start))
	}
	if _, ok := a.Get(ann.ID); ok {
		t.Fatal("the replica that signed the session out still takes it")
	}
	// Nobody else was signed out.
	for _, s := range []*Sessions{a, b} {
		if _, ok := s.Get(cy.ID); !ok {
			t.Fatal("another person's session ended with it")
		}
	}
	// A change to what a session holds is announced too: the groups read
	// again on one replica are the other's at once.
	a.Update(cy.ID, func(s *Session) { s.User.Groups = []string{"ops"}; s.Roles = []roles.ID{roles.Admin} })
	start = time.Now()
	for {
		got, ok := b.Get(cy.ID)
		if ok && slices.Equal(got.User.Groups, []string{"ops"}) && slices.Equal(got.Roles, []roles.ID{roles.Admin}) {
			break
		}
		if time.Since(start) > SessionHeldFor-time.Second {
			t.Fatalf("the other replica reads %+v %v %s after the change", got, ok, time.Since(start))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The bound when the notice does not arrive (the listener's connection is
// down): a replica answers from the row as it last read it for
// SessionHeldFor and not a moment longer, so a session signed out
// elsewhere is refused within that time. Within it the replica does not
// ask the database at all, which is the price and the point.
func TestASignOutElsewhereIsRefusedWithinTheBoundWithoutItsNotice(t *testing.T) {
	da, dbb := twoOnOneDatabase(t, time.Hour, false)
	c := newClock()
	a := sessionsOn(NewPGSessions(da, nil), nil)
	storeB := NewPGSessions(dbb, c.now)
	b := sessionsOn(storeB, nil)
	ann := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
	if _, ok := b.Get(ann.ID); !ok {
		t.Fatal("a session signed in on one replica is unknown on the other")
	}
	asked := storeB.asked.Load()
	if asked != 1 {
		t.Fatalf("the first request read the database %d times", asked)
	}
	for range 50 {
		if _, ok := b.Get(ann.ID); !ok {
			t.Fatal("the session ended")
		}
	}
	if n := storeB.asked.Load(); n != asked {
		t.Fatalf("50 requests inside %s read the database %d times", SessionHeldFor, n-asked)
	}

	a.Delete(ann.ID)
	// Not heard here. Just short of the bound the copy still answers.
	c.add(SessionHeldFor - time.Millisecond)
	if _, ok := b.Get(ann.ID); !ok {
		t.Fatal("the copy ran out before its time: the bound is not what the documents say")
	}
	if n := storeB.asked.Load(); n != asked {
		t.Fatalf("the database was read %d times inside the bound", n-asked)
	}
	c.add(time.Millisecond)
	if _, ok := b.Get(ann.ID); ok {
		t.Fatalf("a session signed out elsewhere is still taken %s after", SessionHeldFor)
	}
	if b.Alive(ann.ID) {
		t.Fatal("a session signed out elsewhere is still alive")
	}
	// A sign-in elsewhere is never held back: a key this replica has not
	// asked about is read at once.
	bob := a.Start(Session{User: User{Username: "bob"}, Method: "local"})
	if _, ok := b.Get(bob.ID); !ok {
		t.Fatal("a session signed in on one replica is unknown on the other")
	}
	// And this replica's own sign-out is its own at once.
	b.Delete(bob.ID)
	if _, ok := b.Get(bob.ID); ok {
		t.Fatal("a replica still takes the session it signed out")
	}
	if _, ok, err := NewPGSessions(da, nil).Get(context.Background(), keyOf(bob.ID)); err != nil || ok {
		t.Fatalf("the signed-out session's row: %v %v", ok, err)
	}
}

// Every field of a session is a column and reads back as it was written,
// the times to the microsecond the database keeps; and reading it back
// writes nothing: the row is not touched by requests until the last
// activity is due, 5 minutes on (the row's xmin says so).
func TestASessionsRowHoldsEveryFieldAndIsNotWrittenByReading(t *testing.T) {
	da, dbb := twoOnOneDatabase(t, 50*time.Millisecond, true)
	ctx := context.Background()
	c := newClock()
	c.add(123456789 * time.Nanosecond)
	a, b := sessionsOn(NewPGSessions(da, c.now), c.now), sessionsOn(NewPGSessions(dbb, c.now), c.now)
	read := c.now().Add(-90*time.Second - 987654321*time.Nanosecond)
	in := Session{
		User:  User{Subject: "ldap:ann", Username: "ann@example.com", Email: "ann@example.com", Name: "Ann Example", Groups: []string{"ops", "web readers"}, KubeName: "ldap:ann", KubeGroups: []string{"ldap:ops", "221b:superadmins"}},
		Admin: true, Roles: []roles.ID{roles.SuperAdmin, roles.Admin}, Super: []roles.ID{roles.SuperAdmin}, Method: "ldap", Ref: "uid=ann,ou=people,dc=example,dc=com", GroupsAt: read,
	}
	started := a.Start(in)
	key := keyOf(started.ID)
	got, ok := b.Get(started.ID)
	if !ok {
		t.Fatal("the session is unknown on the other replica")
	}
	us := func(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }
	want := *started
	if got.User.Subject != want.User.Subject || got.User.Username != want.User.Username || got.User.Email != want.User.Email || got.User.Name != want.User.Name ||
		!slices.Equal(got.User.Groups, want.User.Groups) || got.User.KubeName != want.User.KubeName || !slices.Equal(got.User.KubeGroups, want.User.KubeGroups) ||
		got.Admin != want.Admin || !slices.Equal(got.Roles, want.Roles) || !slices.Equal(got.Super, want.Super) || got.Method != want.Method || got.Ref != want.Ref {
		t.Fatalf("the other replica reads\n%+v\nof\n%+v", got, want)
	}
	if !got.GroupsAt.Equal(us(read)) || !got.Created.Equal(us(want.Created)) || !got.Expires.Equal(us(want.Expires)) {
		t.Fatalf("times read back: groups %s created %s expires %s, of %s %s %s", got.GroupsAt, got.Created, got.Expires, read, want.Created, want.Expires)
	}
	if want.Created.Equal(us(want.Created)) {
		t.Fatal("the test's clock has no nanoseconds: it proves nothing of the microseconds")
	}

	// The table's columns are the row's, and none of this row's is null.
	rows, err := da.Pool().Query(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auth_sessions'")
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, col)
	}
	mine := db.Columns(&sessionRow{})
	slices.Sort(cols)
	slices.Sort(mine)
	if !slices.Equal(cols, mine) {
		t.Fatalf("auth_sessions has the columns\n%v\nand the store writes\n%v", cols, mine)
	}
	var nulls int
	if err := da.Pool().QueryRow(ctx, `SELECT count(*) FROM auth_sessions s, jsonb_each(to_jsonb(s)) AS f(name, value) WHERE value = 'null'::jsonb`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("%d null columns in the session's row: %v", nulls, err)
	}

	xmin := func() string {
		t.Helper()
		var x string
		if err := da.Pool().QueryRow(ctx, "SELECT xmin::text FROM auth_sessions WHERE auth_session_id = $1", key).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	first := xmin()
	// 4 minutes 40 seconds of requests on both replicas, each a fresh read
	// of the row (the copies run out by the same clock).
	for range 14 {
		c.add(20 * time.Second)
		for _, s := range []*Sessions{a, b} {
			if _, ok := s.Get(started.ID); !ok {
				t.Fatal("the session ended")
			}
			if !s.Alive(started.ID) {
				t.Fatal("the session is not alive")
			}
		}
	}
	if now := xmin(); now != first {
		t.Fatalf("requests in under 5 minutes wrote the session's row (xmin %s, then %s)", first, now)
	}
	// Due: one write, by the replica that sees the request, and no more
	// after it.
	c.add(20 * time.Second)
	if _, ok := a.Get(started.ID); !ok {
		t.Fatal("the session ended")
	}
	second := xmin()
	if second == first {
		t.Fatal("the last activity was not written when it was due")
	}
	seen := c.now()
	for range 5 {
		c.add(20 * time.Second)
		if _, ok := a.Get(started.ID); !ok {
			t.Fatal("the session ended")
		}
	}
	if now := xmin(); now != second {
		t.Fatalf("the row was written again before the next 5 minutes (xmin %s, then %s)", second, now)
	}
	var last time.Time
	if err := da.Pool().QueryRow(ctx, "SELECT last_seen_at FROM auth_sessions WHERE auth_session_id = $1", key).Scan(&last); err != nil || !last.Equal(us(seen)) {
		t.Fatalf("the last activity stored is %s, of %s: %v", last, seen, err)
	}
}

// Neither a session's ID, which is the cookie's value, nor anything made
// of it but its hash is in any row or any log line, whatever happens to
// the session: started, used, changed, listed, swept, ended, and written
// while the database is away.
func TestNoSessionIDIsEverInTheDatabaseOrTheLog(t *testing.T) {
	away, cfg := dbtest.NewAway(t, dbtest.Config(t))
	d := dbtest.OpenConfig(t, cfg)
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	c := newClock()
	s := NewSessions(8*time.Hour, 7*24*time.Hour, c.now)
	s.Share(NewPGSessions(d, c.now), slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	var ids []string
	start := func(name string) *Session {
		sess := s.Start(Session{User: User{Username: name, Subject: "local:" + name}, Method: "local"})
		ids = append(ids, sess.ID)
		return sess
	}
	ann, bob, cy := start("ann"), start("bob"), start("cy")
	c.add(6 * time.Minute)
	for _, sess := range []*Session{ann, bob, cy} {
		if _, ok := s.Get(sess.ID); !ok {
			t.Fatal("the session ended")
		}
	}
	s.Update(ann.ID, func(x *Session) { x.User.Groups = []string{"ops"} })
	if n := len(s.Live()); n != 3 {
		t.Fatalf("%d sessions live", n)
	}
	s.Delete(bob.ID)
	s.Sweep()
	// The database goes: a read, a write, a sign-out and a sweep fail.
	away.Cut()
	c.add(6 * time.Minute)
	if _, ok := s.Get(ann.ID); !ok {
		t.Fatal("a session this replica holds ended with the database")
	}
	s.Update(ann.ID, func(x *Session) { x.Ref = "uid=ann" })
	s.Delete(cy.ID)
	s.Sweep()
	dee := start("dee")
	if !dee.Signed {
		t.Fatal("a session started while the database is away is not a signed one")
	}
	away.Back()
	c.add(11 * time.Second)
	eventuallyTrue(t, "the database did not come back", func() bool {
		return d.Do(context.Background(), func(q db.Querier) error { _, err := q.Exec(context.Background(), "SELECT 1"); return err }) == nil
	})
	s.StoreIsBack()
	s.Sweep()
	if _, ok := s.Get(ann.ID); !ok {
		t.Fatal("the session did not outlive the outage")
	}
	// Nine hours on, the sweep removes what is idle.
	c.add(9 * time.Hour)
	s.Sweep()

	text := logged.String()
	if !strings.Contains(text, "the session store is away") {
		t.Fatalf("nothing was logged of the outage, so the log proves nothing:\n%s", text)
	}
	for _, id := range ids {
		if in := dbtest.Holding(t, d, id); len(in) != 0 {
			t.Errorf("a session's ID is in the database: %v", in)
		}
		if strings.Contains(text, id) {
			t.Error("a session's ID is in the log")
		}
		if signedShape(id) {
			payload, mac, _ := SplitSignedForTest(id)
			for what, part := range map[string]string{"payload": payload, "MAC": mac} {
				if in := dbtest.Holding(t, d, part); len(in) != 0 {
					t.Errorf("a signed session's %s is in the database: %v", what, in)
				}
				if strings.Contains(text, part) {
					t.Errorf("a signed session's %s is in the log", what)
				}
			}
		}
	}
	// Not vacuous: the hash of an ID is what a row is kept under, and the
	// same search finds it.
	eve := start("eve")
	if in := dbtest.Holding(t, d, keyOf(eve.ID)); in["auth_sessions"] != 1 || len(in) != 1 {
		t.Fatalf("the hash of a session's ID is in %v, not in one row of auth_sessions", in)
	}
	if in := dbtest.Holding(t, d, eve.ID); len(in) != 0 {
		t.Errorf("a session's ID is in the database: %v", in)
	}
	// The table itself refuses a key that is not a hash, a session's ID
	// among them.
	if _, err := d.Pool().Exec(context.Background(), "INSERT INTO auth_sessions (auth_session_id, groups_read_at, last_seen_at, expires_at) VALUES ($1, now(), now(), now())", eve.ID); err == nil {
		t.Error("the table took a session's ID as a key")
	}
}

func eventuallyTrue(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// What the database's store returns while the database cannot be reached
// is what makes a signed session and keeps a replica's sessions standing
// (StoreAway): every call wraps db.ErrUnavailable, and the list is an
// error, never empty. A database that answers and refuses is not away.
func TestTheDatabasesStoreSaysAwayAndNeverAnEmptyList(t *testing.T) {
	away, cfg := dbtest.NewAway(t, dbtest.Config(t))
	d := dbtest.OpenConfig(t, cfg)
	ctx := context.Background()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p := NewPGSessions(d, nil)
	rec, _ := encodeSession(Session{User: User{Username: "ann"}, Method: "local", Created: time.Now(), LastSeen: time.Now(), Expires: time.Now().Add(time.Hour)}, nil)
	key := keyOf("a session")
	if wrote, err := p.Put(ctx, key, rec, func(cur []byte) bool { return cur == nil }); err != nil || !wrote {
		t.Fatalf("writing a session: %v %v", wrote, err)
	}
	calls := map[string]func() error{
		"Get":    func() error { _, _, err := p.Get(ctx, keyOf("another")); return err },
		"Put":    func() error { _, err := p.Put(ctx, key, rec, nil); return err },
		"Delete": func() error { return p.Delete(ctx, key) },
		"List": func() error {
			list, err := p.List(ctx)
			if list != nil {
				t.Errorf("a list that failed returned %d records, not none", len(list))
			}
			return err
		},
	}
	for name, mode := range map[string]func(){"a database that is not there": away.Cut, "a database that swallows what it is sent": away.Hang} {
		mode()
		for what, call := range calls {
			start := time.Now()
			err := call()
			if !errors.Is(err, db.ErrUnavailable) || !StoreAway(err) {
				t.Errorf("%s, with %s: %v", what, name, err)
			}
			if took := time.Since(start); took > 6*time.Second {
				t.Errorf("%s took %s with %s", what, took, name)
			}
		}
		away.Back()
		eventuallyTrue(t, "the database did not come back", func() bool {
			_, ok, err := p.Get(ctx, key)
			return err == nil && ok
		})
	}
	// Refusals: the database answered.
	if _, err := p.Put(ctx, "not-a-hash", rec, nil); err == nil || StoreAway(err) {
		t.Errorf("a key that is not a hash: %v", err)
	}
	if _, err := p.Put(ctx, key, []byte("{"), nil); err == nil || StoreAway(err) {
		t.Errorf("a record that cannot be read: %v", err)
	}
	if _, err := d.Pool().Exec(ctx, "ALTER TABLE auth_sessions RENAME TO auth_sessions_gone"); err != nil {
		t.Fatal(err)
	}
	for what, call := range calls {
		if err := call(); err == nil || StoreAway(err) {
			t.Errorf("%s on a table that is not there: %v", what, err)
		}
	}
}

// The copy out of the Secrets (DB-66): what is live and readable is
// copied, once; what has expired is not; and of what the Secrets still
// hold after it only what the copy took, or what has expired, is to be
// emptied.
func TestTheSessionsAreCopiedOutOfTheirSecretsOnce(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	// The copy's own time is the database's clock, so the test's is the
	// real one.
	now := time.Now().UTC().Truncate(time.Second)
	const idle = 8 * time.Hour
	rec := func(name string, seen, expires time.Time, change func(map[string]any)) []byte {
		b, err := encodeSession(Session{User: User{Username: name, Subject: "local:" + name, Groups: []string{"ops"}}, Method: "local", Roles: []roles.ID{roles.Viewer},
			GroupsAt: seen, Created: seen.Add(-time.Hour), LastSeen: seen, Expires: expires}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if change != nil {
			b = alterRecord(t, b, change)
		}
		return b
	}
	week := now.Add(6 * 24 * time.Hour)
	recs := map[string][]byte{
		keyOf("live"):     rec("live", now.Add(-time.Hour), week, nil),
		keyOf("just"):     rec("just", now.Add(-idle-4*time.Minute), week, nil),
		keyOf("idle"):     rec("idle", now.Add(-idle-6*time.Minute), week, nil),
		keyOf("too long"): rec("too long", now.Add(-time.Minute), now.Add(-time.Second), nil),
		keyOf("newer"):    rec("newer", now, week, func(m map[string]any) { m["version"] = 2 }),
		keyOf("more"):     rec("more", now, week, func(m map[string]any) { m["device"] = "laptop" }),
		keyOf("broken"):   []byte(`{"user":`),
		"not-a-hash":      rec("odd", now, week, nil),
		keyOf("format 0"): rec("format 0", now, week, func(m map[string]any) { delete(m, "version") }),
	}
	read := func(context.Context) (map[string][]byte, error) { return recs, nil }

	// Before the copy nothing may be emptied, and a copy that cannot read
	// the Secrets copies nothing and is not written down.
	if keys, err := SessionsToEmpty(ctx, d, recs, idle, now); err == nil {
		t.Fatalf("before the copy is recorded, to empty: %v", keys)
	}
	if n, err := CopySessions(ctx, d, func(context.Context) (map[string][]byte, error) { return nil, errors.New("the API server is away") }, idle, now); err == nil || n != 0 {
		t.Fatalf("a copy that could not read the Secrets: %d %v", n, err)
	}
	if copied, err := d.Imported(ctx, SessionsImportID); err != nil || copied {
		t.Fatalf("a copy that failed is recorded (%v): %v", copied, err)
	}

	n, err := CopySessions(ctx, d, read, idle, now)
	if err != nil || n != 3 {
		t.Fatalf("copied %d: %v", n, err)
	}
	p := NewPGSessions(d, nil)
	list, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for key, b := range list {
		sess, _, version, err := decodeSession(b)
		if err != nil || version != 1 || key != keyOf(sess.User.Username) {
			t.Fatalf("the row %s: %s %v", key, b, err)
		}
		names = append(names, sess.User.Username)
		want, _, _, _ := decodeSession(recs[key])
		if sess.User.Subject != want.User.Subject || !slices.Equal(sess.User.Groups, want.User.Groups) || !slices.Equal(sess.Roles, want.Roles) || sess.Method != want.Method ||
			!sess.Created.Equal(want.Created) || !sess.LastSeen.Equal(want.LastSeen) || !sess.Expires.Equal(want.Expires) || !sess.GroupsAt.Equal(want.GroupsAt) {
			t.Fatalf("the row of %s:\n%s\nof the record\n%s", sess.User.Username, b, recs[key])
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"format 0", "just", "live"}) {
		t.Fatalf("copied %v", names)
	}

	// Once: a second copy reads nothing and writes nothing.
	called := false
	if n, err := CopySessions(ctx, d, func(context.Context) (map[string][]byte, error) { called = true; return recs, nil }, idle, now); err != nil || n != 0 || called {
		t.Fatalf("a second copy: %d %v, the Secrets read: %v", n, err, called)
	}

	toEmpty := func(recs map[string][]byte) []string {
		t.Helper()
		keys, err := SessionsToEmpty(ctx, d, recs, idle, now)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, key := range keys {
			for _, name := range []string{"live", "just", "idle", "too long", "newer", "more", "broken", "format 0", "late"} {
				if key == keyOf(name) {
					out = append(out, name)
				}
			}
		}
		if len(out) != len(keys) {
			t.Fatalf("to empty: %v", keys)
		}
		slices.Sort(out)
		return out
	}
	// What the copy took and what has expired; not what this console
	// cannot read whole, which stays as it is.
	if got := toEmpty(recs); !slices.Equal(got, []string{"format 0", "idle", "just", "live", "too long"}) {
		t.Fatalf("to empty after the copy: %v", got)
	}
	// Signed out since, with its Secret still whole (the emptying failed):
	// it was started before the copy, so the copy took it, and it is
	// emptied all the same. Left whole it could be taken up again by a
	// console from before the move.
	if err := p.Delete(ctx, keyOf("live")); err != nil {
		t.Fatal(err)
	}
	if got := toEmpty(recs); !slices.Contains(got, "live") {
		t.Fatalf("a session signed out since the copy is not emptied: %v", got)
	}
	// A session a console from before the move started after the copy:
	// this console never had it, and leaves it.
	var copiedAt time.Time
	if err := d.Pool().QueryRow(ctx, "SELECT imported_at FROM imports WHERE import_id = $1", SessionsImportID).Scan(&copiedAt); err != nil {
		t.Fatal(err)
	}
	late, _ := encodeSession(Session{User: User{Username: "late"}, Method: "local", Created: copiedAt.Add(time.Minute), LastSeen: now, Expires: week, GroupsAt: now}, nil)
	if got := toEmpty(map[string][]byte{keyOf("late"): late}); len(got) != 0 {
		t.Fatalf("a session started after the copy by another console is to be emptied: %v", got)
	}
	// A database that cannot say names nothing.
	gone := dbtest.Open(t)
	gone.Close()
	if keys, err := SessionsToEmpty(ctx, gone, recs, idle, now); err == nil || len(keys) != 0 {
		t.Fatalf("on a database that does not answer, to empty: %v %v", keys, err)
	}
}

// alterRecord changes a stored record as JSON.
func alterRecord(t *testing.T, b []byte, change func(map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
