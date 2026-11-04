package kv

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A key routed to a backend of its own is read where it was until its
// first save there, which drops the old copy; the rest stay put.
func TestARoutedKeyMovesOnItsFirstSave(t *testing.T) {
	ctx := context.Background()
	old, own := &Memory{}, &Memory{}
	old.SaveKey(ctx, "log", []byte("before"), "")
	old.SaveKey(ctx, "settings", []byte("s"), "")
	r := &Routed{Default: old, Routes: map[string]Backend{"log": own}}
	b, version, err := r.LoadKey(ctx, "log")
	if err != nil || string(b) != "before" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := r.SaveKey(ctx, "log", []byte("after"), version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := old.LoadKey(ctx, "log"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old copy stayed: %v", err)
	}
	if b, _, _ := own.LoadKey(ctx, "log"); string(b) != "after" {
		t.Fatalf("%q", b)
	}
	if b, _, _ := r.LoadKey(ctx, "settings"); string(b) != "s" {
		t.Fatalf("an unrouted key: %q", b)
	}
}

// Once a key has moved, the place it left reports no size for it.
func TestAMovedKeyLeavesNoSizeBehind(t *testing.T) {
	ctx := context.Background()
	old := &Limited{Backend: &Memory{}, Name: "settings"}
	old.SaveKey(ctx, "log", []byte("before"), "")
	r := &Routed{Default: old, Routes: map[string]Backend{"log": &Memory{}}}
	_, version, _ := r.LoadKey(ctx, "log")
	if _, err := r.SaveKey(ctx, "log", []byte("after"), version); err != nil {
		t.Fatal(err)
	}
	if _, ok := old.Sizes()["log"]; ok {
		t.Fatalf("%v", old.Sizes())
	}
}

// A save past its key's limit is refused, saying what is full and what to
// do, and nothing is written; sizes are kept for the metrics.
func TestASavePastItsLimitIsRefusedClearly(t *testing.T) {
	ctx := context.Background()
	m := &Memory{}
	l := &Limited{Backend: m, Name: "ns/cm", Limits: map[string]Limit{"settings.json": {Bytes: 10, Hint: "remove fleets"}}}
	if _, err := l.SaveKey(ctx, "settings.json", []byte("0123456789"), ""); err != nil {
		t.Fatal(err)
	}
	_, err := l.SaveKey(ctx, "settings.json", []byte("0123456789x"), "")
	if !errors.Is(err, ErrFull) || !strings.Contains(err.Error(), "settings.json would be") || !strings.Contains(err.Error(), "remove fleets") {
		t.Fatalf("%v", err)
	}
	if b, _, _ := m.LoadKey(ctx, "settings.json"); len(b) != 10 {
		t.Fatalf("the refused save was written: %q", b)
	}
	if _, err := l.SaveKey(ctx, "other", []byte("anything at all"), ""); err != nil {
		t.Fatalf("a key without a limit: %v", err)
	}
	if got := l.Sizes()["settings.json"]; got != 10 {
		t.Fatalf("size %d", got)
	}
}
