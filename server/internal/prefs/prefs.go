// Package prefs keeps each signed-in person's own preferences, such as the
// columns each table shows (asked at the Phase 4 review). Someone who is
// not signed in keeps theirs in the browser; someone who is finds theirs
// in any browser. They are kept next to the shared settings, one key per
// person, named by a hash of the person's subject so the ConfigMap names
// nobody.
package prefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
)

// ErrNotFound is what a Backend returns for a key never saved.
var ErrNotFound = errors.New("no preferences saved")

// Backend keeps one value per key: one key of the settings ConfigMap, or
// memory.
type Backend interface {
	LoadKey(ctx context.Context, key string) ([]byte, error)
	SaveKey(ctx context.Context, key string, b []byte) error
}

// Limits keep one person from filling the ConfigMap the settings share.
const (
	MaxBytes = 16 << 10
	MaxKeys  = 200
)

var keyRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,120}$`)

// Prefs are a person's preferences by name: "221b.pods.hidden" → ["ip"].
type Prefs map[string]json.RawMessage

// Validate reports what is wrong with a set of preferences.
func Validate(p Prefs) error {
	if len(p) > MaxKeys {
		return fmt.Errorf("at most %d preferences", MaxKeys)
	}
	for k := range p {
		if !keyRE.MatchString(k) {
			return fmt.Errorf("%q is not a preference name", k)
		}
	}
	b, _ := json.Marshal(p)
	if len(b) > MaxBytes {
		return fmt.Errorf("preferences are kept to %d KiB", MaxBytes>>10)
	}
	return nil
}

// KeyFor is the key a person's preferences are kept under.
func KeyFor(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return "prefs." + hex.EncodeToString(sum[:12]) + ".json"
}

// Store reads and writes people's preferences, keeping what it has read.
type Store struct {
	backend Backend
	mu      sync.Mutex
	cache   map[string]Prefs
	writeMu sync.Mutex
}

// New returns a Store over the backend; nil keeps them in memory.
func New(b Backend) *Store {
	if b == nil {
		b = &Memory{}
	}
	return &Store{backend: b, cache: map[string]Prefs{}}
}

// Get returns a person's preferences; none saved is an empty set.
func (s *Store) Get(ctx context.Context, subject string) (Prefs, error) {
	key := KeyFor(subject)
	s.mu.Lock()
	p, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return p, nil
	}
	b, err := s.backend.LoadKey(ctx, key)
	switch {
	case errors.Is(err, ErrNotFound):
		p = Prefs{}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &p); err != nil || p == nil {
			p = Prefs{}
		}
	}
	s.mu.Lock()
	s.cache[key] = p
	s.mu.Unlock()
	return p, nil
}

// Put replaces a person's preferences.
func (s *Store) Put(ctx context.Context, subject string, p Prefs) error {
	if p == nil {
		p = Prefs{}
	}
	if err := Validate(p); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	key := KeyFor(subject)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.backend.SaveKey(ctx, key, b); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = p
	s.mu.Unlock()
	return nil
}

// Memory keeps preferences until the server restarts.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (m *Memory) LoadKey(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (m *Memory) SaveKey(_ context.Context, key string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string][]byte{}
	}
	m.m[key] = append([]byte(nil), b...)
	return nil
}
