package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/kv"
)

// The store keeps the newest events under one key, merges what two
// replicas add, and trims to its limits (asked October 30, 2026).
func TestTheKVStoreMergesAndTrims(t *testing.T) {
	ctx := context.Background()
	backend := &kv.Memory{}
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	a := &KVStore{Backend: backend, Key: StoreKey, Keep: 5, Every: time.Hour}
	b := &KVStore{Backend: backend, Key: StoreKey, Keep: 5, Every: time.Hour}
	defer a.Close(ctx)
	defer b.Close(ctx)
	for i := range 4 {
		a.Add(Event{Time: at.Add(time.Duration(i) * time.Minute), Action: "a", Target: string(rune('0' + i))})
	}
	b.Add(Event{Time: at.Add(90 * time.Second), Action: "b"})
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 || list[0].Target != "3" || list[2].Action != "b" || list[4].Target != "0" {
		t.Fatalf("merged wrong: %+v", list)
	}
	// Over the cap the oldest go.
	a.Add(Event{Time: at.Add(10 * time.Minute), Action: "a", Target: "new"})
	list, _ = a.List(ctx)
	if len(list) != 5 || list[0].Target != "new" || list[4].Target != "1" {
		t.Fatalf("not trimmed to the cap: %+v", list)
	}
	// And over the bytes.
	c := &KVStore{Backend: &kv.Memory{}, Key: StoreKey, Keep: 100, MaxBytes: 300, Every: time.Hour}
	for i := range 20 {
		c.Add(Event{Time: at.Add(time.Duration(i) * time.Second), Action: "settings.update", Detail: "something that takes room"})
	}
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := c.Backend.LoadKey(ctx, StoreKey)
	var kept []Event
	_ = json.Unmarshal(raw, &kept)
	if len(raw) > 300 || len(kept) == 0 || len(kept) >= 20 {
		t.Fatalf("not trimmed to the bytes: %d bytes, %d events", len(raw), len(kept))
	}
}

// The log hands its store what was recorded before the store was set,
// and a backup's events go to the store alone.
func TestTheLogFeedsItsStore(t *testing.T) {
	l := New(Options{})
	l.Record(Event{Action: "signin"})
	if l.Stored() {
		t.Fatal("stored before a store was set")
	}
	st := &MemoryStore{}
	l.SetStore(st)
	l.Record(Event{Action: "settings.update"})
	taken := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	l.Import([]Event{{Action: "old", Time: time.Unix(0, 0)}}, taken)
	got, _ := l.Events(context.Background())
	if len(got) != 3 || got[0].Action != "old" || got[1].Action != "settings.update" || got[2].Action != "signin" {
		t.Fatalf("got %+v", got)
	}
	// What came from a backup says so; what was recorded here does not.
	if got[0].Restored != "2026-10-30T09:00:00Z" || got[1].Restored != "" || got[2].Restored != "" {
		t.Fatalf("restored marks: %q %q %q", got[0].Restored, got[1].Restored, got[2].Restored)
	}
}

// On a console without storage the page lists what is kept in memory,
// and Stored says it goes with a restart, for the page to say so.
func TestALogKeptInMemoryIsNotStored(t *testing.T) {
	l := New(Options{})
	l.Record(Event{Action: "signin"})
	l.KeepInMemory(10)
	l.Record(Event{Action: "settings.update"})
	if l.Stored() {
		t.Fatal("events in memory count as stored")
	}
	if got, _ := l.Events(context.Background()); len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

// One record too large to keep goes by itself: the older ones stay.
func TestARecordTooLargeToKeepCostsNoOtherRecordItsPlace(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s := &KVStore{Backend: &kv.Memory{}, Key: StoreKey, Keep: 100, MaxBytes: 64 << 10, Every: time.Hour}
	defer s.Close(ctx)
	for i := range 20 {
		s.Add(Event{Time: at.Add(time.Duration(i) * time.Second), Action: "settings.update", Target: string(rune('a' + i))})
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	s.Add(Event{Time: at.Add(time.Hour), Action: "signin.failed", Who: Who{Email: strings.Repeat("x", 100<<10)}})
	s.Add(Event{Time: at.Add(2 * time.Hour), Action: "signin", Target: "after"})
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 21 || list[0].Target != "after" || list[20].Target != "a" {
		t.Fatalf("kept %d events, newest %q, oldest %q", len(list), list[0].Target, list[len(list)-1].Target)
	}
	for _, e := range list {
		if e.Action == "signin.failed" {
			t.Fatal("the oversized record was kept")
		}
	}
}

// The names in a record are bounded in the stream too, whatever was sent.
func TestARecordsNamesAreBounded(t *testing.T) {
	var out bytes.Buffer
	l := New(Options{Out: &out})
	long := strings.Repeat("é", MaxName)
	l.Record(Event{Action: ActionSignInFailed, Who: Who{Subject: long, Email: long, Name: long}})
	var e Event
	if err := json.Unmarshal(out.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{e.Who.Subject, e.Who.Email, e.Who.Name} {
		if len(s) > MaxName || !utf8.ValidString(s) || s == "" {
			t.Fatalf("%d bytes: %q", len(s), s)
		}
	}
}

// Whichever part of the console writes a record, it reaches no sink with a
// credential or a host key in it.
func TestARecordHoldsNoSecret(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleExample"
	var out bytes.Buffer
	l := New(Options{Out: &out})
	l.KeepInMemory(10)
	l.Record(Event{Action: "watson.command", Target: `run in pod a/b: mysql -phunter2`, Detail: `command="kubectl create secret generic db --from-literal=name=s3cret-literal"`})
	l.Record(Event{Action: "watson.command", Detail: `command="helm upgrade web ./c --set image.tag=1.2,db.password=hunter2"`})
	l.Record(Event{Action: "watson.command", Detail: `command="curl -u ada:hunter2 https://example.com"`})
	l.Record(Event{Action: "session.end", Detail: "output=\"221B-HOSTKEY " + key + "\\n221B-PAIRED root\\n\""})
	kept, err := l.Events(context.Background())
	if err != nil || len(kept) != 4 {
		t.Fatalf("%d events, %v", len(kept), err)
	}
	b, _ := json.Marshal(kept)
	for name, text := range map[string]string{"the stream": out.String(), "the store": string(b)} {
		for _, secret := range []string{"hunter2", "s3cret-literal", "AAAAC3Nza"} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds %s: %s", name, secret, text)
			}
		}
		if !strings.Contains(text, "image.tag=1.2") || !strings.Contains(text, "221B-PAIRED root") {
			t.Errorf("%s lost what is no secret: %s", name, text)
		}
	}
}

// What an older console kept is cleaned once, and what is clean stays as
// it was.
func TestSweepCleansWhatWasKept(t *testing.T) {
	b := &kv.Memory{}
	old := []Event{{Action: "session.end", Detail: `output="221B-HOSTKEY ssh-rsa AAAAB3NzaC1yc2EExample"`}, {Action: "signin", Detail: "local"}}
	raw, _ := json.Marshal(old)
	if _, err := b.SaveKey(context.Background(), StoreKey, raw, ""); err != nil {
		t.Fatal(err)
	}
	s := NewKVStore(b, slog.Default())
	defer s.Close(context.Background())
	if err := s.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _, err := b.LoadKey(context.Background(), StoreKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "AAAAB3") || !strings.Contains(string(got), `"local"`) {
		t.Fatalf("kept: %s", got)
	}
	_, version, _ := b.LoadKey(context.Background(), StoreKey)
	if err := s.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := b.LoadKey(context.Background(), StoreKey); again != version {
		t.Fatal("a clean log was written again")
	}
}

// A big run's commands go before anything else when the log is full, so
// they never push decisions out.
func TestRunCommandsGoFirstWhenTheLogIsFull(t *testing.T) {
	now := time.Now()
	var list []Event
	for i := 0; i < 10; i++ {
		action := ActionAutomationCommand
		if i%5 == 0 {
			action = "automation.approve"
		}
		list = append(list, Event{Action: action, Time: now.Add(-time.Duration(i) * time.Minute)})
	}
	got := trimKeep(list, 4)
	approvals := 0
	for _, e := range got {
		if e.Action == "automation.approve" {
			approvals++
		}
	}
	if len(got) != 4 || approvals != 2 || got[0].Time != now {
		t.Fatalf("%+v", got)
	}
}

// fullBackend refuses saves over a size, as a full ConfigMap does.
type fullBackend struct {
	kv.Memory
	room int
}

func (f *fullBackend) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if len(b) > f.room {
		return "", kv.ErrFull
	}
	return f.Memory.SaveKey(ctx, key, b, version)
}

// When what holds the log is full, the log keeps less rather than stop.
func TestAFullStorageMakesTheLogKeepLess(t *testing.T) {
	b := &fullBackend{room: 100 << 10}
	s := NewKVStore(b, slog.Default())
	defer s.Close(context.Background())
	for i := 0; i < 1500; i++ {
		s.Add(Event{Action: "signin", Time: time.Now().Add(time.Duration(i) * time.Millisecond), Detail: strings.Repeat("d", 100)})
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _, err := b.LoadKey(context.Background(), StoreKey)
	if err != nil || len(got) == 0 || len(got) > 100<<10 {
		t.Fatalf("%d bytes kept, %v", len(got), err)
	}
}
