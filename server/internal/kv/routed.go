package kv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// ErrFull is a save refused for its size: the key, or what holds it, would
// pass its limit. The error says which and by how much.
var ErrFull = errors.New("too large to keep")

// Deleter is a backend that can drop a key.
type Deleter interface {
	DeleteKey(ctx context.Context, key string) error
}

// Routed keeps some keys in backends of their own and the rest in Default,
// so that no one object holds them all (asked November 3, 2026, #46). A
// key not yet in its own backend is read where it was, and moves on its
// first save there; the old copy is then dropped.
type Routed struct {
	Default Backend
	Routes  map[string]Backend
	mu      sync.Mutex
	moved   map[string]bool
}

func (r *Routed) of(key string) Backend {
	if b, ok := r.Routes[key]; ok && b != nil {
		return b
	}
	return r.Default
}

func (r *Routed) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	b := r.of(key)
	v, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, ErrNotFound) && b != r.Default {
		// Not moved yet: what was saved where it was, over the new place's
		// version, so the first save there is conditional too.
		if old, _, oerr := r.Default.LoadKey(ctx, key); oerr == nil {
			return old, version, nil
		}
	}
	return v, version, err
}

func (r *Routed) SaveKey(ctx context.Context, key string, v []byte, version string) (string, error) {
	b := r.of(key)
	next, err := b.SaveKey(ctx, key, v, version)
	if err != nil || b == r.Default {
		return next, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.moved[key] {
		if d, ok := r.Default.(Deleter); ok {
			if derr := d.DeleteKey(ctx, key); derr == nil {
				if r.moved == nil {
					r.moved = map[string]bool{}
				}
				r.moved[key] = true
			}
		}
	}
	return next, nil
}

// DeleteKey drops a key from its backend.
func (r *Routed) DeleteKey(ctx context.Context, key string) error {
	if d, ok := r.of(key).(Deleter); ok {
		return d.DeleteKey(ctx, key)
	}
	return nil
}

// Limit is the most a key may hold, and what to tell a person who passes it.
type Limit struct {
	Bytes int
	// Hint says what to remove, ending the refusal.
	Hint string
}

// Limited refuses a save that would pass its key's limit, with ErrFull and
// a message that says what is full, instead of the API server's refusal of
// the whole object; it warns once a key passes four fifths of its limit,
// and keeps each key's size for the metrics.
type Limited struct {
	Backend Backend
	// Name is what holds the keys, for messages and metrics.
	Name   string
	Limits map[string]Limit
	Logger *slog.Logger
	mu     sync.Mutex
	sizes  map[string]int
	warned map[string]bool
}

func (l *Limited) note(key string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sizes == nil {
		l.sizes, l.warned = map[string]int{}, map[string]bool{}
	}
	l.sizes[key] = n
	lim := l.Limits[key].Bytes
	switch {
	case lim > 0 && n*5 > lim*4 && !l.warned[key]:
		l.warned[key] = true
		if l.Logger != nil {
			l.Logger.Warn("a stored record is past four fifths of its limit", "configmap", l.Name, "key", key, "bytes", n, "limit", lim)
		}
	case lim > 0 && n*5 <= lim*4:
		l.warned[key] = false
	}
}

func (l *Limited) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	b, version, err := l.Backend.LoadKey(ctx, key)
	if err == nil {
		l.note(key, len(b))
	}
	return b, version, err
}

func (l *Limited) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if lim, ok := l.Limits[key]; ok && lim.Bytes > 0 && len(b) > lim.Bytes {
		msg := fmt.Sprintf("%s would be %d KiB, over its %d KiB", key, (len(b)+1023)>>10, lim.Bytes>>10)
		if lim.Hint != "" {
			msg += ": " + lim.Hint
		}
		return "", fmt.Errorf("%w: %s", ErrFull, msg)
	}
	next, err := l.Backend.SaveKey(ctx, key, b, version)
	if err == nil {
		l.note(key, len(b))
	}
	return next, err
}

// DeleteKey drops a key, and its size from the metrics.
func (l *Limited) DeleteKey(ctx context.Context, key string) error {
	d, ok := l.Backend.(Deleter)
	if !ok {
		return nil
	}
	err := d.DeleteKey(ctx, key)
	if err == nil {
		l.mu.Lock()
		delete(l.sizes, key)
		l.mu.Unlock()
	}
	return err
}

// Sizes are the keys' sizes as last read or saved here.
func (l *Limited) Sizes() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int, len(l.sizes))
	for k, v := range l.sizes {
		out[k] = v
	}
	return out
}
