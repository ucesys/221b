package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube"
)

// failingSecrets are the session Secrets with an API server that fails
// what the test says.
type failingSecrets struct {
	sessionSecrets
	list, empty error
	// emptied counts the Secrets emptied through it.
	emptied int
}

func (f *failingSecrets) All(ctx context.Context) (map[string][]byte, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.sessionSecrets.All(ctx)
}

func (f *failingSecrets) Empty(ctx context.Context, key string) (bool, error) {
	if f.empty != nil {
		return false, f.empty
	}
	ok, err := f.sessionSecrets.Empty(ctx, key)
	if ok {
		f.emptied++
	}
	return ok, err
}

// lockedBuffer is a log the test reads while the console writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// DB-66 at a start, with the Secrets a console from before the move left:
// the sessions are copied into the database once, so whoever is signed in
// stays signed in; an expired one is not copied; each Secret the copy
// took, or that had expired, is emptied, and then deleted, since it holds
// no data (the owner, November 20, 2026, answer 1);
// a copy that fails leaves every Secret whole and does not stop the
// start; nothing is emptied when the database cannot say the copy was
// made; and from then on the console makes no session Secret, and a
// console from before the move cannot take up a session ended since.
func TestTheSessionsLeaveTheirSecretsAtAStart(t *testing.T) {
	ctx := t.Context()
	d := dbtest.Open(t)
	cs := fake.NewClientset()
	const ns, release = "221b", "k8s-console-221b"
	secretsOf := func() *kube.SessionStore {
		return kube.NewSessionStoreFor(cs, ns, release, map[string]string{"app.kubernetes.io/instance": release})
	}
	// The console from before the move: sessions in Secrets, watched.
	var clock struct {
		sync.Mutex
		back time.Duration
	}
	now := func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		return time.Now().Add(-clock.back)
	}
	ago := func(d time.Duration) {
		clock.Lock()
		defer clock.Unlock()
		clock.back = d
	}
	before := func() *auth.Sessions {
		store := secretsOf()
		wctx, cancel := context.WithCancel(ctx)
		t.Cleanup(cancel)
		go store.Run(wctx)
		if !store.Synced(wctx) {
			t.Fatal("the session cache did not sync")
		}
		s := auth.NewSessions(8*time.Hour, 7*24*time.Hour, now)
		s.Share(store, slog.New(slog.DiscardHandler))
		return s
	}
	old := before()
	// Signed in nine hours ago and not seen since; eight days ago, seen a
	// minute ago; and three people at work.
	ago(9 * time.Hour)
	idle := old.Start(auth.Session{User: auth.User{Username: "idle"}, Method: "local"})
	ago(8 * 24 * time.Hour)
	tooLong := old.Start(auth.Session{User: auth.User{Username: "too long"}, Method: "local"})
	ago(time.Hour)
	ann := old.Start(auth.Session{User: auth.User{Subject: "local:ann", Username: "ann", Email: "ann@example.com", Name: "Ann", Groups: []string{"ops"}, KubeName: "221b-local:ann", KubeGroups: []string{"221b-local:ops"}}, Method: "local", Admin: true})
	bob := old.Start(auth.Session{User: auth.User{Username: "bob"}, Method: "ldap", Ref: "uid=bob,dc=example"})
	cy := old.Start(auth.Session{User: auth.User{Username: "cy"}, Method: "oidc"})
	ago(0)
	cookies := []string{idle.ID, tooLong.ID, ann.ID, bob.ID, cy.ID}

	// The Secrets as the API server has them: which there are, and which
	// of them still hold a session.
	objects := func() (all, holding []string) {
		t.Helper()
		list, err := cs.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, sec := range list.Items {
			all = append(all, sec.Name)
			switch {
			case len(sec.Data) == 0:
			case len(sec.Data) == 1 && len(sec.Data["session.json"]) > 0:
				holding = append(holding, sec.Name)
			default:
				t.Fatalf("the Secret %s holds %d keys", sec.Name, len(sec.Data))
			}
			if sec.Labels["app.kubernetes.io/component"] != "session" {
				t.Fatalf("the Secret %s is labelled %v", sec.Name, sec.Labels)
			}
		}
		slices.Sort(all)
		slices.Sort(holding)
		return all, holding
	}
	five, holding := objects()
	if len(five) != 5 || len(holding) != 5 {
		t.Fatalf("before the move: %d Secrets, %d holding a session", len(five), len(holding))
	}
	rows := func(d *db.DB) int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM auth_sessions").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var logged lockedBuffer
	log := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	start := func(d *db.DB, secrets sessionSecrets) (*wiring, *auth.Sessions) {
		t.Helper()
		w := &wiring{database: d, log: log, authCfg: auth.Config{SessionIdle: 8 * time.Hour}}
		store := w.sessionsInDatabase(ctx, secrets)
		if _, ok := store.(*auth.PGSessions); !ok {
			t.Fatalf("with a database the sessions are kept in %T", store)
		}
		if !w.importIDs[auth.SessionsImportID] || len(w.importClash) != 0 {
			t.Fatalf("the copy's name %v (clash %v), the domains %v", w.importIDs, w.importClash, w.inDatabase)
		}
		s := auth.NewSessions(8*time.Hour, 7*24*time.Hour, nil)
		s.Share(store, log)
		return w, s
	}

	// A start that cannot read the Secrets: the console starts, with the
	// database's store, and every Secret is as it was.
	failed, _ := start(d, &failingSecrets{sessionSecrets: secretsOf(), list: errors.New("the API server is away")})
	if copied, err := d.Imported(ctx, auth.SessionsImportID); err != nil || copied {
		t.Fatalf("a copy that failed is recorded (%v): %v", copied, err)
	}
	if all, holding := objects(); !slices.Equal(all, five) || len(holding) != 5 || rows(d) != 0 || failed.sessionsLeft != nil {
		t.Fatalf("a copy that failed: %d Secrets, %d holding a session, %d rows", len(all), len(holding), rows(d))
	}
	// A database that cannot say whether the copy was made empties nothing.
	gone := dbtest.Open(t)
	gone.Close()
	start(gone, secretsOf())
	if all, holding := objects(); !slices.Equal(all, five) || len(holding) != 5 {
		t.Fatalf("on a database that does not answer: %d Secrets, %d holding a session", len(all), len(holding))
	}

	// The next start copies, but the API server refuses the emptying: the
	// people are signed in from the rows, the Secrets are whole, and the
	// emptying is left to be tried again.
	refusing := &failingSecrets{sessionSecrets: secretsOf(), empty: errors.New("secrets is forbidden")}
	w, s := start(d, refusing)
	if copied, err := d.Imported(ctx, auth.SessionsImportID); err != nil || !copied {
		t.Fatalf("the copy is not recorded (%v): %v", copied, err)
	}
	if n := rows(d); n != 3 {
		t.Fatalf("%d rows after the copy of three live sessions and two expired", n)
	}
	if _, holding := objects(); len(holding) != 5 || w.sessionsLeft == nil {
		t.Fatalf("an emptying that failed: %d Secrets hold a session, to try again: %v", len(holding), w.sessionsLeft != nil)
	}
	// Whoever was signed in is: the cookie from before is the session.
	got, ok := s.Get(ann.ID)
	if !ok {
		t.Fatal("a person signed in before the move is not signed in after it")
	}
	if got.User.Subject != "local:ann" || got.User.Username != "ann" || got.User.Email != "ann@example.com" || got.User.Name != "Ann" || !slices.Equal(got.User.Groups, []string{"ops"}) ||
		got.User.KubeName != "221b-local:ann" || !slices.Equal(got.User.KubeGroups, []string{"221b-local:ops"}) || got.Method != "local" || !got.Admin ||
		!got.Created.Equal(ann.Created.Truncate(time.Microsecond)) || !got.Expires.Equal(ann.Expires.Truncate(time.Microsecond)) {
		t.Fatalf("the session after the move:\n%+v\nbefore it:\n%+v", got, *ann)
	}
	if got, ok := s.Get(bob.ID); !ok || got.Method != "ldap" || got.Ref != "uid=bob,dc=example" {
		t.Fatalf("a directory person's session after the move: %+v %v", got, ok)
	}
	for _, expired := range []*auth.Session{idle, tooLong} {
		if _, ok := s.Get(expired.ID); ok {
			t.Fatalf("the expired session of %q was copied", expired.User.Username)
		}
	}
	// bob signs out while his Secret is still whole.
	s.Delete(bob.ID)
	if n := rows(d); n != 2 {
		t.Fatalf("%d rows after a sign-out", n)
	}
	// The emptying is tried again (each minute, startAuth) and now goes
	// through: every Secret the copy took or that had expired is emptied,
	// bob's among them although his row is gone, and then deleted.
	refusing.empty = nil
	if done := w.sessionsLeft(ctx); !done || refusing.emptied != 5 {
		t.Fatalf("the emptying tried again: done %v, %d emptied", done, refusing.emptied)
	}
	if all, holding := objects(); len(all) != 0 || len(holding) != 0 {
		t.Fatalf("after the emptying: %d Secrets of %d left, %d still holding a session", len(all), len(five), len(holding))
	}

	// From here on the console makes no session Secret and touches none:
	// a sign-in is a row, a sign-out deletes the row.
	dee := s.Start(auth.Session{User: auth.User{Username: "dee"}, Method: "local"})
	cookies = append(cookies, dee.ID)
	s.Delete(cy.ID)
	if all, holding := objects(); len(all) != 0 || len(holding) != 0 || rows(d) != 2 {
		t.Fatalf("after a sign-in and a sign-out with the database: %d Secrets, %d holding a session, %d rows", len(all), len(holding), rows(d))
	}

	// A console from before the move, started now (a rollback): no session
	// is taken up again, not the signed-out ones and not ann's, and its own
	// write of a late activity does not make a record again.
	rolledBack := before()
	for _, sess := range []*auth.Session{ann, bob, cy, idle, tooLong} {
		if _, ok := rolledBack.Get(sess.ID); ok {
			t.Fatalf("a console from before the move takes up the session of %q from its emptied Secret", sess.User.Username)
		}
		// The one still running hears of the emptied Secret by its watch.
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if _, ok := old.Get(sess.ID); !ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the console from before the move, still running, takes the session of %q", sess.User.Username)
			}
		}
	}
	if _, holding := objects(); len(holding) != 0 {
		t.Fatalf("a console from before the move wrote %d session records back", len(holding))
	}
	// What it starts itself after the copy is its own: this console never
	// had it, does not copy it, and leaves its Secret whole.
	late := rolledBack.Start(auth.Session{User: auth.User{Username: "late"}, Method: "local"})
	cookies = append(cookies, late.ID)
	w2, s2 := start(d, secretsOf())
	if _, ok := s2.Get(late.ID); ok {
		t.Fatal("a session started by a console from before the move, after the copy, is taken")
	}
	if all, holding := objects(); len(all) != 1 || len(holding) != 1 || w2.sessionsLeft != nil {
		t.Fatalf("after a later start: %d Secrets, %d holding a session", len(all), len(holding))
	}
	if _, ok := s2.Get(ann.ID); !ok {
		t.Fatal("a later start signed someone out")
	}

	// Neither a cookie's value nor anything but its hash is in a row or a
	// log line.
	text := logged.String()
	if !strings.Contains(text, "copied a record set from its Secret") || !strings.Contains(text, "the API server is away") {
		t.Fatalf("the log says nothing of the copy, so it proves nothing:\n%s", text)
	}
	for _, c := range cookies {
		if in := dbtest.Holding(t, d, c); len(in) != 0 {
			t.Errorf("a session cookie's value is in the database: %v", in)
		}
		if strings.Contains(text, c) {
			t.Error("a session cookie's value is in the log")
		}
	}
	if in := dbtest.Holding(t, d, "ann@example.com"); in["auth_sessions"] != 1 {
		t.Fatalf("the search that found no cookie does not find the session either: %v", in)
	}
}

// Where the sessions are kept, by what the console is given (DB-66):
// with Secrets, the database, and the Secrets are read once for the copy
// and not watched; without Secrets (a console
// run by hand), memory, and the copy's name is claimed all the same. A
// console always has its database (DB-69): the case of Secrets without
// one, which kept the sessions in Secrets, is gone with it.
func TestWhereTheSessionsAreKept(t *testing.T) {
	ctx := t.Context()
	// An API server that is not there: with Secrets the start reads them
	// and fails, which stops nothing.
	nowhere := &rest.Config{Host: "https://127.0.0.1:1", Timeout: 2 * time.Second}
	wire := func(d *db.DB, secrets string, mode auth.Mode) *wiring {
		t.Helper()
		w := &wiring{cfg: config.Config{ConnectionsSecrets: secrets}, log: slog.New(slog.DiscardHandler), auditLog: audit.New(audit.Options{Out: &bytes.Buffer{}}),
			restCfg: nowhere, database: d, authCfg: auth.Config{Mode: mode, SetupToken: "s3cret"}}
		if err := w.wireAuth(ctx); err != nil {
			t.Fatal(err)
		}
		return w
	}
	d := dbtest.Open(t)
	rows := func() int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM auth_sessions").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Secrets and a database: the database.
	w := wire(d, "221b/k8s-console-221b", auth.ModeLocal)
	if _, ok := w.authCfg.SessionStore.(*auth.PGSessions); !ok || !slices.Contains(w.inDatabase, "identity") || !w.importIDs[auth.SessionsImportID] {
		t.Fatalf("with Secrets and a database: the store %T, the domains %v", w.authCfg.SessionStore, w.inDatabase)
	}
	sess := w.authSvc.Sessions().Start(auth.Session{User: auth.User{Username: "ann"}, Method: "local"})
	if n := rows(); n != 1 || sess.Signed {
		t.Fatalf("a sign-in with Secrets and a database: %d rows, signed %v", n, sess.Signed)
	}
	// The copy failed (no API server), so nothing says it was made.
	if copied, err := d.Imported(ctx, auth.SessionsImportID); err != nil || copied {
		t.Fatalf("a copy that could not read the Secrets is recorded (%v): %v", copied, err)
	}

	// No Secrets: no start (DB-97), nothing kept in memory.
	nw := &wiring{cfg: config.Config{}, log: slog.New(slog.DiscardHandler), auditLog: audit.New(audit.Options{Out: &bytes.Buffer{}}),
		restCfg: nowhere, database: d, authCfg: auth.Config{Mode: auth.ModeLocal, SetupToken: "s3cret"}}
	if err := nw.wireAuth(ctx); !errors.Is(err, errNoRelease) || nw.authSvc != nil {
		t.Fatalf("without Secrets the sessions are kept somewhere: %v", err)
	}
	// Sign-in off: nobody signs in, and nothing is kept anywhere; the
	// session Secrets of before are copied all the same (decided by the
	// owner on November 21, 2026), here a copy that cannot read them.
	var logs bytes.Buffer
	w = &wiring{cfg: config.Config{ConnectionsSecrets: "221b/k8s-console-221b"}, log: slog.New(slog.NewTextHandler(&logs, nil)), auditLog: audit.New(audit.Options{Out: &bytes.Buffer{}}),
		restCfg: nowhere, database: d, authCfg: auth.Config{Mode: auth.ModeNone}}
	if err := w.wireAuth(ctx); err != nil {
		t.Fatal(err)
	}
	if w.authCfg.SessionStore != nil || slices.Contains(w.inDatabase, "identity") {
		t.Fatalf("with sign-in off: the store %T, the domains %v", w.authCfg.SessionStore, w.inDatabase)
	}
	if !w.importIDs[auth.SessionsImportID] || !strings.Contains(logs.String(), "copying a record set from its Secret into the database failed") {
		t.Fatalf("with sign-in off the session Secrets were not copied: the names claimed %v; the log:\n%s", w.importIDs, logs.String())
	}
}
