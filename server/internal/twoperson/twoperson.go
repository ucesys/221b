// Package twoperson keeps the changes one SuperAdmin asks for and another
// must approve: a cluster's environment taken off production, and a local
// user made a SuperAdmin. Either would let one person loosen what needs
// two, so each waits, in the settings ConfigMap, until a different
// SuperAdmin approves or rejects it.
package twoperson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// Key is where the requests are kept.
const Key = "two-person.json"

// Kinds of request.
const (
	// KindEnvironment takes a cluster's environment off production.
	KindEnvironment = "environment"
	// KindSuperAdmin makes a local user a SuperAdmin, by the role itself
	// or by groups that give it.
	KindSuperAdmin = "superadmin"
)

// States of a request.
const (
	Waiting  = "waiting"
	Approved = "approved"
	Rejected = "rejected"
)

// Limits.
const (
	// MaxComment is the longest reason a decision may give.
	MaxComment = 500
	// keep is how many decided requests stay listed, and maxBytes the
	// most the record holds, the oldest decided ones going first.
	keep     = 200
	maxBytes = 48 << 10
	tries    = 12
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("no such request")
	ErrOwn      = errors.New("someone else must approve it")
	ErrInvalid  = errors.New("not valid")
	// ErrWaits is a request for what already has one waiting.
	ErrWaits = errors.New("a request for it already waits")
)

// Who is a person: Key tells people apart, Name is shown.
type Who struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Request is one change that waits for a second SuperAdmin, or did.
type Request struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
	// Cluster, its name, and the environments from and to, for an
	// environment change.
	Cluster     string `json:"cluster,omitempty"`
	ClusterName string `json:"clusterName,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	// User is the local user made a SuperAdmin; Super says by the role,
	// Groups by the groups that give it.
	User   string   `json:"user,omitempty"`
	Super  bool     `json:"super,omitempty"`
	Groups []string `json:"groups,omitempty"`

	By        Who        `json:"by"`
	At        time.Time  `json:"at"`
	State     string     `json:"state"`
	DecidedBy *Who       `json:"decidedBy,omitempty"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	// Comment is the reason given with a rejection.
	Comment string `json:"comment,omitempty"`
}

// What says what the request changes, in words.
func (r Request) What() string {
	switch r.Kind {
	case KindEnvironment:
		name := r.ClusterName
		if name == "" {
			name = r.Cluster
		}
		return fmt.Sprintf("%s: environment %s → %s", name, r.From, r.To)
	case KindSuperAdmin:
		var how []string
		if r.Super {
			how = append(how, "the SuperAdmin role")
		}
		if len(r.Groups) > 0 {
			how = append(how, "groups "+strings.Join(r.Groups, ", "))
		}
		return fmt.Sprintf("%s becomes a SuperAdmin (%s)", r.User, strings.Join(how, " and "))
	}
	return r.Kind
}

// about says whether two requests change the same thing.
func (r Request) about(o Request) bool {
	if r.Kind != o.Kind {
		return false
	}
	if r.Kind == KindEnvironment {
		return r.Cluster == o.Cluster
	}
	return r.User == o.User
}

// Store keeps the requests in the key-value storage.
type Store struct {
	mu  sync.Mutex
	now func() time.Time
	// bmu guards backend, which SetBackend may change once at start.
	bmu     sync.Mutex
	backend kv.Backend
}

// New makes a store; b is memory and now time.Now when nil.
func New(b kv.Backend, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	if b == nil {
		b = &kv.Memory{}
	}
	return &Store{backend: b, now: now}
}

// SetBackend gives the store the storage that outlasts a restart, which
// the console opens after sign-in is set up.
func (s *Store) SetBackend(b kv.Backend) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	s.backend = b
}

func (s *Store) storage() kv.Backend {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return s.backend
}

type record struct {
	Next     int       `json:"next"`
	Requests []Request `json:"requests"`
}

func (s *Store) load(ctx context.Context) (record, string, error) {
	var rec record
	b, version, err := s.storage().LoadKey(ctx, Key)
	if errors.Is(err, kv.ErrNotFound) {
		return rec, version, nil
	}
	if err != nil {
		return rec, "", err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		// Read as empty, the next save would wipe what waits.
		return rec, "", fmt.Errorf("%s is not readable: %w", Key, err)
	}
	return rec, version, nil
}

// change edits the record over the version read, again on a conflict.
func (s *Store) change(ctx context.Context, edit func(*record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-time.After(rand.N(min(50*time.Millisecond<<try, time.Second))):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var rec record
		var version string
		if rec, version, err = s.load(ctx); err != nil {
			return err
		}
		if err = edit(&rec); err != nil {
			return err
		}
		b, _ := json.Marshal(rec)
		if _, err = s.storage().SaveKey(ctx, Key, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return err
}

// List returns the requests, newest first.
func (s *Store) List(ctx context.Context) ([]Request, error) {
	rec, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := rec.Requests
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Add records a request that waits. One for what already has one waiting
// is refused.
func (s *Store) Add(ctx context.Context, r Request) (Request, error) {
	if r.Kind != KindEnvironment && r.Kind != KindSuperAdmin {
		return r, fmt.Errorf("%w: kind %q", ErrInvalid, r.Kind)
	}
	if r.By.Key == "" {
		return r, fmt.Errorf("%w: nobody asked for it", ErrInvalid)
	}
	r.At, r.State, r.DecidedBy, r.DecidedAt, r.Comment = s.now().UTC(), Waiting, nil, nil, ""
	err := s.change(ctx, func(rec *record) error {
		for _, x := range rec.Requests {
			if x.State == Waiting && x.about(r) {
				return fmt.Errorf("%w: request %d, %s", ErrWaits, x.ID, x.What())
			}
		}
		rec.Next++
		r.ID = rec.Next
		rec.Requests = append(rec.Requests, r)
		prune(rec)
		return nil
	})
	return r, err
}

// prune keeps every waiting request and the newest decided ones, as many
// as fit in maxBytes.
func prune(rec *record) {
	decided := 0
	for i := len(rec.Requests) - 1; i >= 0; i-- {
		if rec.Requests[i].State == Waiting {
			continue
		}
		if decided++; decided > keep {
			rec.Requests = slices.Delete(rec.Requests, i, i+1)
		}
	}
	for {
		if b, _ := json.Marshal(rec); len(b) <= maxBytes {
			return
		}
		i := slices.IndexFunc(rec.Requests, func(r Request) bool { return r.State != Waiting })
		if i < 0 {
			return
		}
		rec.Requests = slices.Delete(rec.Requests, i, i+1)
	}
}

// Decide approves or rejects a waiting request. Whoever asked for it may
// reject it, which withdraws it, but never approve it; a rejection says
// why. An approval runs apply first, and stays undecided when it fails.
func (s *Store) Decide(ctx context.Context, id int, approve bool, comment string, by Who, apply func(Request) error) (Request, error) {
	comment = strings.TrimSpace(comment)
	if !approve && comment == "" {
		return Request{}, fmt.Errorf("%w: say why it is rejected", ErrInvalid)
	}
	if len(comment) > MaxComment {
		return Request{}, fmt.Errorf("%w: a comment is at most %d characters", ErrInvalid, MaxComment)
	}
	if by.Key == "" {
		return Request{}, fmt.Errorf("%w: nobody decides it", ErrInvalid)
	}
	rec, _, err := s.load(ctx)
	if err != nil {
		return Request{}, err
	}
	i := slices.IndexFunc(rec.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 {
		return Request{}, ErrNotFound
	}
	r := rec.Requests[i]
	if r.State != Waiting {
		return r, fmt.Errorf("%w: request %d is %s", ErrInvalid, id, r.State)
	}
	if approve && by.Key == r.By.Key {
		return r, fmt.Errorf("%w: you asked for this", ErrOwn)
	}
	if approve && apply != nil {
		if err := apply(r); err != nil {
			return r, err
		}
	}
	var out Request
	err = s.change(ctx, func(rec *record) error {
		for i := range rec.Requests {
			x := &rec.Requests[i]
			if x.ID != id {
				continue
			}
			if x.State != Waiting {
				return fmt.Errorf("%w: request %d is %s", ErrInvalid, id, x.State)
			}
			now := s.now().UTC()
			x.State, x.DecidedBy, x.DecidedAt, x.Comment = Rejected, &by, &now, comment
			if approve {
				x.State = Approved
			}
			out = *x
			return nil
		}
		return ErrNotFound
	})
	return out, err
}
