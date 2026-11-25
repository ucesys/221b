package auth

import (
	"context"
	"testing"
	"time"
)

// Cut by the review's cap (pg_sessions.go:320): a row made under a key
// another replica has just found nothing under (the stored session a
// signed one is exchanged for, whose key every replica looks up) is
// announced, so that replica does not answer from its held miss.
func TestARowMadeWhereAnotherReplicaFoundNoneIsAnnounced(t *testing.T) {
	da, dbb := twoOnOneDatabase(t, time.Hour, true)
	frozen := time.Now()
	a, b := NewPGSessions(da, nil), NewPGSessions(dbb, func() time.Time { return frozen })
	ctx := context.Background()
	key := keyOf(randomID())
	if _, ok, err := b.Get(ctx, key); err != nil || ok {
		t.Fatalf("before the row: %v %v", ok, err)
	}
	rec, _ := encodeSession(Session{User: User{Username: "ann"}, Method: "local", Created: frozen, LastSeen: frozen, GroupsAt: frozen, Expires: frozen.Add(time.Hour)}, nil)
	if _, err := a.Put(ctx, key, rec, func(cur []byte) bool { return cur == nil }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok, err := b.Get(ctx, key); err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the other replica answers from the miss it held: the new row was not announced")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
