package kv

import (
	"context"
	"slices"
	"strconv"
	"sync"
)

// --- One flow for findings and cases (Phase 12, wave 6) ---

// Objects keeps many small objects of one kind, each a few keys: a case
// each, in a ConfigMap of its own (<release>-case-<n>), so that a change
// to one rewrites only that one. Every object has a version of its own;
// a save is conditional on it like a Backend's.
type Objects interface {
	// Load reads an object's keys and its version; ErrNotFound when there
	// is none.
	Load(ctx context.Context, name string) (map[string][]byte, string, error)
	// Save writes one key of an object at version and returns the new
	// version. Version "" creates the object, and is ErrConflict when it
	// is there already, so that two writers never take one name.
	Save(ctx context.Context, name, key string, b []byte, version string) (string, error)
	// Delete removes an object; one already gone is no error.
	Delete(ctx context.Context, name string) error
	// List names every object there is.
	List(ctx context.Context) ([]string, error)
}

// MemoryObjects keeps objects until the server restarts: tests, and a
// console without a ConfigMap of its own.
type MemoryObjects struct {
	mu      sync.Mutex
	objects map[string]*memoryObject
	version int
	// Writes counts the saves and deletes, for tests.
	Writes int
}

type memoryObject struct {
	keys    map[string][]byte
	version string
}

func (m *MemoryObjects) Load(_ context.Context, name string) (map[string][]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[name]
	if !ok {
		return nil, "", ErrNotFound
	}
	out := make(map[string][]byte, len(o.keys))
	for k, v := range o.keys {
		out[k] = append([]byte(nil), v...)
	}
	return out, o.version, nil
}

func (m *MemoryObjects) Save(_ context.Context, name, key string, b []byte, version string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[name]
	switch {
	case version == "" && ok, version != "" && (!ok || o.version != version):
		return "", ErrConflict
	}
	if !ok {
		if m.objects == nil {
			m.objects = map[string]*memoryObject{}
		}
		o = &memoryObject{keys: map[string][]byte{}}
		m.objects[name] = o
	}
	m.version++
	m.Writes++
	o.keys[key] = append([]byte(nil), b...)
	o.version = strconv.Itoa(m.version)
	return o.version, nil
}

func (m *MemoryObjects) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[name]; ok {
		delete(m.objects, name)
		m.Writes++
	}
	return nil
}

func (m *MemoryObjects) List(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objects))
	for n := range m.objects {
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}

// Keys says what an object holds, for tests: its keys' values as text.
func (m *MemoryObjects) Keys(name string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[name]
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range o.keys {
		out[k] = string(v)
	}
	return out
}
