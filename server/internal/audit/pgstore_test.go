package audit

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

func pgStore(t *testing.T) *PGStore {
	t.Helper()
	// Flushed by the test, not by the clock.
	return &PGStore{DB: dbtest.Open(t), Keep: 5, Every: time.Hour, Logger: slog.New(slog.DiscardHandler)}
}

func eventAt(sec int, action string) Event {
	return Event{Time: time.Date(2026, 11, 18, 12, 0, sec, 0, time.UTC), Action: action, Who: Who{Subject: "oidc|ada", Email: "ada@corp.example", Name: "Ada"},
		Cluster: "prod", Target: "deployment/web", Detail: "scaled to 3", Outcome: "ok", From: "10.0.0.7"}
}

// Every field of an event comes back as it went in, newest first, and no
// more than Keep of them.
func TestPGStoreKeepsEvents(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	restored := eventAt(1, "restore")
	restored.Restored = "2026-11-17T08:00:00Z"
	s.Add(eventAt(0, "signin"))
	s.Add(restored)
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{restored, eventAt(0, "signin")}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("got  %+v\nwant %+v", list, want)
	}
	for i := 2; i < 9; i++ {
		s.Add(eventAt(i, "command"))
	}
	if list, err = s.List(ctx); err != nil || len(list) != 5 || list[0].Time.Second() != 8 {
		t.Fatalf("%d events, the newest at second %d: %v", len(list), list[0].Time.Second(), err)
	}
}

// Two replicas add at once and both see everything: no version to lose a
// race on.
func TestPGStoreTwoReplicas(t *testing.T) {
	a := pgStore(t)
	b := &PGStore{DB: a.DB, Keep: 100, Every: time.Hour, Logger: a.Logger}
	a.Keep = 100
	ctx := context.Background()
	for i := range 20 {
		a.Add(eventAt(i, "a"))
		b.Add(eventAt(i, "b"))
	}
	done := make(chan error, 2)
	go func() { done <- a.Flush(ctx) }()
	go func() { done <- b.Flush(ctx) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if list, err := a.List(ctx); err != nil || len(list) != 40 {
		t.Fatalf("%d events of 40: %v", len(list), err)
	}
}

func TestPGStoreTrim(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	for i := range 4 {
		s.Add(eventAt(i, "command"))
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Trim(ctx, eventAt(2, "").Time); err != nil || n != 2 {
		t.Fatalf("trimmed %d: %v", n, err)
	}
	if list, _ := s.List(ctx); len(list) != 2 {
		t.Fatalf("%d left", len(list))
	}
}

// The ConfigMap's events are copied once, in their order; a second start
// copies nothing, also when the first copy's events were trimmed since, and
// a later one copies what the ConfigMap got since.
func TestPGStoreImportsOnce(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	old := &MemoryStore{}
	for i := range 3 {
		old.Add(eventAt(i, "old"))
	}
	if n, err := s.Import(ctx, old, "configmap consoles/221b-audit"); err != nil || n != 3 {
		t.Fatalf("copied %d: %v", n, err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 3 || list[0].Time.Second() != 2 || list[2].Time.Second() != 0 {
		t.Fatalf("after the copy: %+v %v", list, err)
	}
	if _, err := s.Trim(ctx, eventAt(59, "").Time); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Import(ctx, old, "configmap consoles/221b-audit"); err != nil || n != 0 {
		t.Fatalf("copied %d the second time: %v", n, err)
	}
	if list, _ := s.List(ctx); len(list) != 0 {
		t.Fatalf("%d events came back", len(list))
	}
	// A console rolled back to a release without a database went on writing
	// to the ConfigMap: the next start with one copies those events, and
	// only those, once.
	old.Add(eventAt(3, "rolled back"))
	old.Add(eventAt(4, "rolled back"))
	if n, err := s.Import(ctx, old, "configmap consoles/221b-audit"); err != nil || n != 2 {
		t.Fatalf("copied %d after the rollback: %v", n, err)
	}
	if n, err := s.Import(ctx, old, "configmap consoles/221b-audit"); err != nil || n != 0 {
		t.Fatalf("copied %d the time after: %v", n, err)
	}
	if list, _ := s.List(ctx); len(list) != 2 || list[0].Time.Second() != 4 {
		t.Fatalf("after the rollback: %+v", list)
	}
}

// A zero byte or text that is not UTF-8 in any field, which PostgreSQL
// refuses, does not hold up the queue: the text is saved made valid, and
// the events after it are saved too (the review of the auth package,
// finding 8: a typed user name of "x%00" stopped every flush).
func TestPGStoreSavesTextPostgreSQLWouldRefuse(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	bad := eventAt(0, "signin.failed")
	bad.Who = Who{Email: "x\x00", Name: "\xff\xfe", Subject: "a\x00b"}
	bad.Target, bad.Detail, bad.From, bad.Cluster, bad.Outcome = "t\x00", "the provider refused: \x00\xc3", "10.0.0.7\x00", "c\x00", "failed\x00"
	s.Add(bad)
	s.Add(eventAt(1, "signin"))
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("the flush: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("%d events: %v", len(list), err)
	}
	got := list[1]
	for _, f := range []string{got.Who.Email, got.Who.Name, got.Who.Subject, got.Target, got.Detail, got.From, got.Cluster, got.Outcome} {
		if strings.ContainsRune(f, 0) || !utf8.ValidString(f) {
			t.Errorf("saved %q", f)
		}
	}
	if got.Who.Email != "x�" {
		t.Errorf("the email is saved as %q", got.Who.Email)
	}
}
