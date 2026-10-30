package live

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/nodes"
)

// Only the leader polls the source (ADR-70): an inactive store leaves the
// source alone and keeps what it holds; one that becomes active polls on
// its next tick, and Run ticks at once when asked rather than at the timer.
func TestAnInactiveStoreDoesNotPoll(t *testing.T) {
	src := fake.New("a")
	src.SetPodUsage(map[string]nodes.Usage{"ns/p": {CPUMilli: 5}})
	var active atomic.Bool
	s := New(src, Options{Refresh: time.Hour, Active: active.Load})
	s.Notify(listed())
	s.Refresh(context.Background())
	if n := src.Polls(); n != 0 {
		t.Fatalf("an inactive store polled %d times", n)
	}
	if u := src.PodUsage(); u["ns/p"].CPUMilli != 5 {
		t.Fatal("the usage the source held was lost while inactive")
	}
	active.Store(true)
	s.Refresh(context.Background())
	if n := src.Polls(); n != 1 {
		t.Fatalf("an active store polled %d times, want 1", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	s.RefreshNow()
	deadline := time.Now().Add(3 * time.Second)
	for src.Polls() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("RefreshNow did not poll within the second: %d polls", src.Polls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
