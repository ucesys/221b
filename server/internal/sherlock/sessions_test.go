package sherlock_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The sweep of conversations asks the sign-in sessions which of them are
// alive, as the console's wiring does (cmd/221b: Sweep(Sessions().Alive)).
// A session that cannot be asked about, because the session store is away
// and this replica never used it, is not one that ended: its conversation
// is kept.

// sessionStore is a shared store of sessions that can be away.
type sessionStore struct {
	mu   sync.Mutex
	recs map[string][]byte
	err  error
}

func (s *sessionStore) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *sessionStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, false, s.err
	}
	b, ok := s.recs[key]
	return b, ok, nil
}

func (s *sessionStore) Put(_ context.Context, key string, b []byte, replace func([]byte) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if replace != nil && !replace(s.recs[key]) {
		return false, nil
	}
	s.recs[key] = b
	return true, nil
}

func (s *sessionStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.recs, key)
	return nil
}

func (s *sessionStore) List(context.Context) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := map[string][]byte{}
	for k, v := range s.recs {
		out[k] = v
	}
	return out, nil
}

func TestTheSweepKeepsAConversationWhoseSessionCannotBeAskedAbout(t *testing.T) {
	for name, fail := range map[string]error{
		"the database away": fmt.Errorf("reading the session: %w", db.ErrUnavailable),
		"a read that fails": errors.New("secrets is forbidden"),
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			store := &sessionStore{recs: map[string][]byte{}}
			replica := func() *auth.Sessions {
				s := auth.NewSessions(8*time.Hour, 7*24*time.Hour, clock)
				s.Share(store, nil)
				return s
			}
			// Two replicas. The people signed in through the first, which
			// holds their sessions; the second has never been asked about
			// them. Each has conversations of both people.
			first, second := replica(), replica()
			ann := first.Start(auth.Session{User: auth.User{Subject: "local:ann", Username: "ann"}, Method: "local"})
			bob := first.Start(auth.Session{User: auth.User{Subject: "local:bob", Username: "bob"}, Method: "local"})
			annKey := sherlock.Key{Session: ann.ID, Cluster: "local"}
			bobKey := sherlock.Key{Session: bob.ID, Cluster: "local"}
			held, svc := sherlock.New(sherlock.Options{Now: clock}), sherlock.New(sherlock.Options{Now: clock})
			held.Conversation(annKey)
			held.Conversation(bobKey)
			annConv, bobConv := svc.Conversation(annKey), svc.Conversation(bobKey)
			if svc.Count() != 2 || held.Count() != 2 {
				t.Fatalf("%d and %d conversations", svc.Count(), held.Count())
			}

			// The store goes away before the second replica's first sweep: it
			// cannot say that either session ended. The first replica holds
			// them, and its own sweep of sessions, which cannot list the
			// store, forgets neither.
			store.fail(fail)
			for range 3 {
				now = now.Add(time.Minute)
				first.Sweep()
				second.Sweep()
				held.Sweep(first.Alive)
				svc.Sweep(second.Alive)
			}
			if svc.Count() != 2 || svc.Conversation(annKey) != annConv || svc.Conversation(bobKey) != bobConv {
				t.Fatalf("a conversation was dropped while its session could not be asked about: %d left", svc.Count())
			}
			if held.Count() != 2 {
				t.Fatalf("a conversation was dropped on the replica that holds its session: %d left", held.Count())
			}

			// The store is back, the replicas notice, and ann signs out on the
			// first replica: her conversations go, bob's stay.
			store.fail(nil)
			now = now.Add(time.Minute)
			first.Delete(ann.ID)
			svc.Sweep(second.Alive)
			held.Sweep(first.Alive)
			if svc.Count() != 1 || svc.Conversation(bobKey) != bobConv || held.Count() != 1 {
				t.Fatalf("after the store came back: %d and %d conversations", svc.Count(), held.Count())
			}
		})
	}
}
