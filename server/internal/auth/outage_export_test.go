package auth

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// What the tests of the session made while the session store is away
// (DB-65) share, in this package and in auth_test.

// OutageStore is a shared store in memory whose every call fails with an
// error while one is set, and which counts its calls.
type OutageStore struct {
	mu   sync.Mutex
	recs map[string][]byte
	err  error
	// The calls made, failed ones included.
	gets, puts, deletes, lists int
}

// NewOutageStore makes an empty store that answers.
func NewOutageStore() *OutageStore { return &OutageStore{recs: map[string][]byte{}} }

// Fail makes every call return err; nil makes the store answer again.
func (s *OutageStore) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Calls is how many calls were made, of every kind.
func (s *OutageStore) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets + s.puts + s.deletes + s.lists
}

// Gets is how many records were asked for.
func (s *OutageStore) Gets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

// Len is how many records are kept.
func (s *OutageStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

// Records returns what is kept, by key.
func (s *OutageStore) Records() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range s.recs {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// Plant keeps a record as it is given.
func (s *OutageStore) Plant(key string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[key] = b
}

func (s *OutageStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.err != nil {
		return nil, false, s.err
	}
	b, ok := s.recs[key]
	return b, ok, nil
}

func (s *OutageStore) Put(_ context.Context, key string, b []byte, replace func([]byte) bool) (bool, error) {
	s.mu.Lock()
	s.puts++
	if s.err != nil {
		defer s.mu.Unlock()
		return false, s.err
	}
	cur := s.recs[key]
	s.mu.Unlock()
	if replace != nil && !replace(cur) {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[key] = append([]byte(nil), b...)
	return true, nil
}

func (s *OutageStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	if s.err != nil {
		return s.err
	}
	delete(s.recs, key)
	return nil
}

func (s *OutageStore) List(context.Context) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	if s.err != nil {
		return nil, s.err
	}
	out := map[string][]byte{}
	for k, v := range s.recs {
		out[k] = v
	}
	return out, nil
}

// MemoryKeys is a KeyStore in memory: the Secret the session key is in.
type MemoryKeys struct {
	mu      sync.Mutex
	data    map[string][]byte
	version int
	loads   int
	err     error
}

// Fail makes every call return err; nil makes the store answer again.
func (m *MemoryKeys) Fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Loads is how many times the key was read.
func (m *MemoryKeys) Loads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loads
}

// Text is the key as the Secret holds it; Set puts one there by hand.
func (m *MemoryKeys) Text() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(m.data[SessionKeyData])
}

func (m *MemoryKeys) Set(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[SessionKeyData] = []byte(text)
	m.version++
}

func (m *MemoryKeys) LoadKey(_ context.Context, key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	if m.err != nil {
		return nil, "", false, m.err
	}
	b, ok := m.data[key]
	return b, strconv.Itoa(m.version), ok, nil
}

func (m *MemoryKeys) SaveKeyIf(_ context.Context, key string, b []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if version != strconv.Itoa(m.version) {
		return errors.New("the Secret changed")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte(nil), b...)
	m.version++
	return nil
}

// Tampering with a signed session, for the tests: the parts of its value,
// and a value sealed with any key.
func SplitSignedForTest(v string) (payload, mac string, ok bool) {
	_, payload, mac, ok = splitSigned(v)
	return payload, mac, ok
}

// MaxSignedForTest is the longest value a signed session's cookie may have.
const MaxSignedForTest = maxSigned
