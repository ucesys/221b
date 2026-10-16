// Package kv is the small key-value storage the console keeps next to its
// settings: one key of the settings ConfigMap each, or memory on a laptop.
// Sherlock's spend is kept this way.
package kv

import (
	"context"
	"errors"
	"sync"
)

// ErrNotFound is what a Backend returns for a key never saved.
var ErrNotFound = errors.New("nothing saved")

// Backend keeps one value per key.
type Backend interface {
	LoadKey(ctx context.Context, key string) ([]byte, error)
	SaveKey(ctx context.Context, key string, b []byte) error
}

// Memory keeps values until the server restarts.
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
