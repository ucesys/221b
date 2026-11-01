package audit

import (
	"bytes"
	"context"
	"encoding/json"
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
	l.Import([]Event{{Action: "old", Time: time.Unix(0, 0)}})
	got, _ := l.Events(context.Background())
	if len(got) != 3 || got[0].Action != "old" || got[1].Action != "settings.update" || got[2].Action != "signin" {
		t.Fatalf("got %+v", got)
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
