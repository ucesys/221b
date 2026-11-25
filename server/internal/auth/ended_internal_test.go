package auth

import (
	"github.com/ucesys/221b/server/internal/roles"
	"sync/atomic"
	"testing"
	"time"
)

// The ending of a signed session, and of the stored session it was
// exchanged for, is kept in the shared store until the signed session
// would have expired (DB-65), in a record that is no session. Over both
// stores there are: the Secrets and the database's table.
func TestTheEndingOfASignedSessionIsKeptInTheSharedStore(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	eachSharedStore(t, now, func(t *testing.T, k sharedKit) {
		clock.Store(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).UnixNano())
		a, b := k.a, k.b
		key := NewSessionKey(nil, now)
		third := NewSessions(8*time.Hour, 7*24*time.Hour, now)
		third.Share(k.another(), nil)
		for _, s := range []*Sessions{a, b, third} {
			s.Outage(key, 0)
		}
		sign := func(name string) *Session {
			t.Helper()
			at := now()
			signed, err := a.sign(Session{User: User{Subject: "local:" + name, Username: name, Groups: []string{"web-readers"}}, Method: "local", Created: at, LastSeen: at, GroupsAt: at})
			if err != nil {
				t.Fatal(err)
			}
			return signed
		}

		// One that is exchanged, on b; a comes to the same stored session.
		ann := sign("ann")
		cur, ok := b.Get(ann.ID)
		if !ok {
			t.Fatal("the signed session is refused")
		}
		cur.Admin, cur.Roles = true, []roles.ID{roles.SuperAdmin}
		stored, made, ok := b.Exchange(cur)
		if !ok || !made || !stored.Expires.Equal(ann.Expires) || !stored.Created.Equal(ann.Created) {
			t.Fatalf("the exchange: %+v made=%v ok=%v; the signed session ends %s", stored, made, ok, ann.Expires)
		}
		curA, ok := a.Get(ann.ID)
		if !ok {
			t.Fatal("the signed session is refused where it was signed")
		}
		again, made, ok := a.Exchange(curA)
		if !ok || made || again.ID != stored.ID || k.kept() != 1 {
			t.Fatalf("the second exchange: made=%v ok=%v, the same session %v, %d records", made, ok, again != nil && again.ID == stored.ID, k.kept())
		}
		held, ok := b.Get(stored.ID)
		if !ok {
			t.Fatal("the stored session is unknown where it was started")
		}

		// It is ended on a: the record says so, and is no session.
		a.Delete(stored.ID)
		rec := k.record(keyOf(stored.ID))
		if !isEnded(rec) || k.kept() != 1 {
			t.Fatalf("after the sign-out the store keeps %d records, this one: %s", k.kept(), rec)
		}
		ended, _, _, err := decodeSession(rec)
		if err != nil || ended.Admin || len(ended.Roles)+len(ended.Super)+len(ended.User.Groups)+len(ended.User.KubeGroups) != 0 || ended.User.Username != "ann" || !ended.Expires.Equal(ann.Expires) {
			t.Fatalf("the ending's record: %+v %v", ended, err)
		}
		// A console that does not know the mark takes it for a session that
		// ran out long ago.
		if !a.expired(&ended, now()) {
			t.Fatal("the ending's record reads as a live session to a console that does not know it")
		}
		// Activity noted on the replica that has not heard does not undo it.
		b.store(keyOf(stored.ID), held, false)
		if !isEnded(k.record(keyOf(stored.ID))) {
			t.Fatal("a late write of the last activity undid the ending")
		}
		eventually(t, "the other replica still takes the ended session", func() bool { return !b.Alive(stored.ID) && !b.Alive(ann.ID) })
		for name, s := range map[string]*Sessions{"a": a, "b": b, "a replica that never saw it": third} {
			if _, ok := s.Get(ann.ID); ok {
				t.Fatalf("%s takes the signed cookie of an ended session", name)
			}
			if _, ok := s.Get(stored.ID); ok {
				t.Fatalf("%s takes the stored cookie of an ended session", name)
			}
			if n := len(s.Live()); n != 0 {
				t.Fatalf("%s lists %d sessions", name, n)
			}
			if n, _ := s.DeleteWhere(func(Session) bool { return true }); n != 0 {
				t.Fatalf("%s ended %d sessions where there is none", name, n)
			}
		}
		if k.kept() != 1 {
			t.Fatalf("%d records", k.kept())
		}

		// One that is never exchanged: b has looked for its ending and
		// found none, a ends it, and b hears.
		bob := sign("bob")
		if _, ok := b.Get(bob.ID); !ok {
			t.Fatal("the signed session is refused")
		}
		a.Delete(bob.ID)
		if k.kept() != 2 {
			t.Fatalf("%d records after the second ending", k.kept())
		}
		eventually(t, "the other replica still takes the signed session ended on the first", func() bool { return !b.Alive(bob.ID) })
		if _, ok := third.Get(bob.ID); ok {
			t.Fatal("a replica that never saw it takes a signed session ended elsewhere")
		}
		if cur, ok := b.Get(bob.ID); ok {
			t.Fatalf("taken again: %+v", cur)
		}
		if k.kept() != 2 {
			t.Fatal("a signed session that was ended was exchanged")
		}

		// The endings are removed once the signed sessions would have
		// expired, by any replica's sweep.
		clock.Add(int64(29 * time.Minute))
		a.Sweep()
		if k.kept() != 2 {
			t.Fatalf("the sweep removed an ending before its time: %d records", k.kept())
		}
		clock.Add(int64(2 * time.Minute))
		a.Sweep()
		if k.kept() != 0 {
			t.Fatalf("%d records after the half hour", k.kept())
		}
	})
}
