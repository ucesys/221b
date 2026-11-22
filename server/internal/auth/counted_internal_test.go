package auth

import (
	"context"
	"testing"
	"time"
)

// What the metrics read of the sessions (Counted) is the count at the last
// list, and asks the shared store nothing (DB-15: a scrape must be
// answered while the store is away).
func TestTheSessionsAreCountedWithoutAskingTheStore(t *testing.T) {
	c := newClock()
	store := NewOutageStore()
	key := NewSessionKey(nil, c.now)
	if err := key.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, b := outageSessions(c, store, key), outageSessions(c, store, key)
	a.Start(Session{User: User{Username: "ann"}, Method: "local"})
	gone := a.Start(Session{User: User{Username: "bob"}, Method: "local"})
	b.Start(Session{User: User{Username: "cy"}, Method: "local"})
	// Before the first list a replica counts what it holds.
	if n := a.Counted(); n != 2 {
		t.Fatalf("before the first list: %d", n)
	}
	a.Sweep()
	calls := store.Calls()
	if n := a.Counted(); n != 3 || store.Calls() != calls {
		t.Fatalf("after the sweep: %d sessions counted, %d calls to the store", n, store.Calls()-calls)
	}
	// The store goes: the count stands, and nothing is asked.
	store.Fail(errAwayDB)
	a.Delete(gone.ID)
	c.add(time.Minute)
	a.Sweep()
	calls = store.Calls()
	for range 10 {
		if n := a.Counted(); n != 3 {
			t.Fatalf("with the store away: %d", n)
		}
	}
	if store.Calls() != calls {
		t.Fatalf("counting asked the store %d times", store.Calls()-calls)
	}
	store.Fail(nil)
	c.add(11 * time.Second)
	a.StoreIsBack()
	a.Sweep()
	if n := a.Counted(); n != 2 {
		t.Fatalf("after the store is back and the sign-out is told: %d", n)
	}
	// The record of an ended session is no session.
	if n := a.Count(); n != 2 || a.Counted() != 2 {
		t.Fatalf("counted with the store: %d, and after it %d", n, a.Counted())
	}
}
