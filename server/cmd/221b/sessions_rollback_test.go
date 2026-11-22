package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube"
)

// The owner, November 20, 2026 (answer 2): after a rollback to a console
// without the database and a roll forward, the console ends the sessions
// kept from before the rollback itself. The sign it goes by is a session
// Secret holding a session the rolled-back console started more than
// auth.RollbackGrace after the copy was recorded. Without that sign
// nothing is ended; with it every stored session is ended once (one log
// line, one audit event), the row that says a signed session was ended
// stays, and a later start with the same sign ends nothing again.
func TestTheSessionsFromBeforeARollbackAreEnded(t *testing.T) {
	ctx := t.Context()
	d := dbtest.Open(t)
	cs := fake.NewClientset()
	const ns, release = "221b", "k8s-console-221b"
	secretsOf := func() *kube.SessionStore {
		return kube.NewSessionStoreFor(cs, ns, release, map[string]string{"app.kubernetes.io/instance": release})
	}
	var clock struct {
		sync.Mutex
		ahead time.Duration
	}
	now := func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		return time.Now().Add(clock.ahead)
	}
	at := func(d time.Duration) {
		clock.Lock()
		defer clock.Unlock()
		clock.ahead = d
	}
	// A console without the database, its sessions in Secrets.
	without := func() *auth.Sessions {
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
	var logged, audited lockedBuffer
	start := func() *auth.Sessions {
		t.Helper()
		w := &wiring{database: d, log: slog.New(slog.NewJSONHandler(&logged, nil)), auditLog: audit.New(audit.Options{Out: &audited}), authCfg: auth.Config{SessionIdle: 8 * time.Hour}}
		s := auth.NewSessions(8*time.Hour, 7*24*time.Hour, nil)
		s.Share(w.sessionsInDatabase(ctx, secretsOf()), slog.New(slog.DiscardHandler))
		return s
	}
	rows := func() int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM auth_sessions WHERE method <> 'ended'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Before the upgrade: ann signed in. The upgrade copies her session.
	old := without()
	ann := old.Start(auth.Session{User: auth.User{Username: "ann"}, Method: "local"})
	s := start()
	bob := s.Start(auth.Session{User: auth.User{Username: "bob"}, Method: "local"})
	if rows() != 2 {
		t.Fatalf("%d sessions after the upgrade", rows())
	}
	// A start with no sign ends nothing.
	start()
	if rows() != 2 {
		t.Fatalf("a start without a rollback ended sessions: %d left", rows())
	}
	// The row that says a signed session was ended (DB-65).
	for _, sql := range []string{
		"CREATE TEMP TABLE one AS SELECT * FROM auth_sessions LIMIT 1",
		"UPDATE one SET auth_session_id = repeat('e', 64), method = 'ended'",
		"INSERT INTO auth_sessions SELECT * FROM one",
	} {
		if _, err := d.Pool().Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// The rollback: a console without the database, where cy signs in
	// later than the grace after the copy.
	rolledBack := without()
	at(auth.RollbackGrace + time.Minute)
	rolledBack.Start(auth.Session{User: auth.User{Username: "cy"}, Method: "local"})
	at(0)

	// The roll forward: ann's and bob's sessions are ended, the ending row
	// stays, and so does cy's Secret, the rolled-back console's own.
	s = start()
	if rows() != 0 {
		t.Fatalf("%d sessions from before the rollback are left", rows())
	}
	for _, x := range []*auth.Session{ann, bob} {
		if _, ok := s.Get(x.ID); ok {
			t.Fatalf("the session of %q is taken after the roll forward", x.User.Username)
		}
	}
	var ended int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM auth_sessions WHERE method = 'ended'").Scan(&ended); err != nil || ended != 1 {
		t.Fatalf("the ending row: %d %v", ended, err)
	}
	if held, err := secretsOf().All(ctx); err != nil || len(held) != 1 {
		t.Fatalf("the rolled-back console's session Secret: %d held, %v", len(held), err)
	}
	if strings.Count(logged.String(), "a console without the database ran since the sessions were copied") != 1 || strings.Count(audited.String(), `"session.end-all"`) != 1 {
		t.Fatalf("the log:\n%s\nthe audit log:\n%s", logged.String(), audited.String())
	}

	// A later start with the same sign ends nothing again.
	dee := s.Start(auth.Session{User: auth.User{Username: "dee"}, Method: "local"})
	start()
	if _, ok := start().Get(dee.ID); !ok || rows() != 1 {
		t.Fatalf("a second start ended the sessions again: %d left", rows())
	}
	if strings.Count(logged.String(), "a console without the database ran since") != 1 || strings.Count(audited.String(), `"session.end-all"`) != 1 {
		t.Fatalf("said twice:\n%s", logged.String())
	}
}
