// Package kv is the small key-value storage the console keeps next to its
// settings: one key of the settings ConfigMap each, or memory on a laptop.
// Sherlock's spend is kept this way.
//
// Several replicas write the same keys (P7-02), so every value comes with
// the version of what holds it, and a save can be made conditional on it:
// a writer that read before another one saved is told so, reads again and
// applies its change to what is there, instead of putting back what it
// read.
package kv

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// ErrNotFound is what a Backend returns for a key never saved.
var ErrNotFound = errors.New("nothing saved")

// ErrConflict is what a conditional save returns when the value changed
// since the version it was given.
var ErrConflict = errors.New("changed since it was read")

// Backend keeps one value per key.
type Backend interface {
	// LoadKey returns a key's value and the version of what holds it. A
	// key never saved is ErrNotFound, still with the version, so the
	// first save can be conditional too.
	LoadKey(ctx context.Context, key string) ([]byte, string, error)
	// SaveKey writes a key when what holds it is still at version ("" saves
	// whatever it holds) and returns the new version; ErrConflict when
	// another writer came first.
	SaveKey(ctx context.Context, key string, b []byte, version string) (string, error)
}

// Memory keeps values until the server restarts. Like a ConfigMap, it has
// one version for every key.
type Memory struct {
	mu      sync.Mutex
	m       map[string][]byte
	version int
}

func (m *Memory) LoadKey(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.m[key]
	if !ok {
		return nil, strconv.Itoa(m.version), ErrNotFound
	}
	return append([]byte(nil), b...), strconv.Itoa(m.version), nil
}

func (m *Memory) SaveKey(_ context.Context, key string, b []byte, version string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version != "" && version != strconv.Itoa(m.version) {
		return "", ErrConflict
	}
	if m.m == nil {
		m.m = map[string][]byte{}
	}
	m.m[key] = append([]byte(nil), b...)
	m.version++
	return strconv.Itoa(m.version), nil
}
