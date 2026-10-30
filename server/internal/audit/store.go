package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// Store keeps the newest events for Administration › Audit log (asked
// October 30, 2026): the stream on standard output and the webhook stay
// the full record; the store is what the page reads.
type Store interface {
	// Add keeps an event; it never blocks on storage.
	Add(e Event)
	// List returns the kept events, newest first.
	List(ctx context.Context) ([]Event, error)
	// Flush writes what Add has taken and not yet saved.
	Flush(ctx context.Context) error
}

// StoreKey is the key of the settings ConfigMap the events are kept under.
const StoreKey = "audit-log.json"

// Defaults of the KVStore.
const (
	DefaultKeep     = 2000
	DefaultMaxBytes = 600 << 10
	defaultEvery    = 2 * time.Second
)

// MemoryStore keeps events until the server restarts.
type MemoryStore struct {
	Keep int
	mu   sync.Mutex
	list []Event
}

func (m *MemoryStore) Add(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list = append([]Event{e}, m.list...)
	if m.Keep > 0 && len(m.list) > m.Keep {
		m.list = m.list[:m.Keep]
	}
}

func (m *MemoryStore) List(context.Context) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.list...), nil
}

func (m *MemoryStore) Flush(context.Context) error { return nil }

// KVStore keeps the events under one key of the settings ConfigMap, so
// every replica and a restarted console see them: each replica adds what
// it recorded to what the key holds, every couple of seconds, over the
// version it read (P7-02). The key holds at most Keep events and MaxBytes
// of JSON; the oldest go first.
type KVStore struct {
	Backend  kv.Backend
	Key      string
	Keep     int
	MaxBytes int
	Every    time.Duration
	Logger   *slog.Logger
	Now      func() time.Time

	mu      sync.Mutex
	pending []Event
	saving  sync.Mutex
	once    sync.Once
	stop    chan struct{}
	done    chan struct{}
}

// NewKVStore starts a store over a backend, flushing every two seconds.
func NewKVStore(b kv.Backend, logger *slog.Logger) *KVStore {
	s := &KVStore{Backend: b, Key: StoreKey, Keep: DefaultKeep, MaxBytes: DefaultMaxBytes, Every: defaultEvery, Logger: logger}
	s.start()
	return s
}

func (s *KVStore) start() {
	s.once.Do(func() {
		if s.Logger == nil {
			s.Logger = slog.Default()
		}
		if s.Now == nil {
			s.Now = time.Now
		}
		if s.Every <= 0 {
			s.Every = defaultEvery
		}
		s.stop, s.done = make(chan struct{}), make(chan struct{})
		go s.run()
	})
}

func (s *KVStore) run() {
	defer close(s.done)
	t := time.NewTicker(s.Every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := s.Flush(ctx); err != nil {
				s.Logger.Warn("saving the audit log failed; the events stay queued", "err", err)
			}
			cancel()
		case <-s.stop:
			return
		}
	}
}

// Close stops the flusher and writes what is queued.
func (s *KVStore) Close(ctx context.Context) error {
	s.start()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	return s.Flush(ctx)
}

func (s *KVStore) Add(e Event) {
	s.start()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, e)
	if keep := s.keep(); len(s.pending) > keep {
		s.pending = s.pending[len(s.pending)-keep:]
	}
}

func (s *KVStore) keep() int {
	if s.Keep > 0 {
		return s.Keep
	}
	return DefaultKeep
}

func (s *KVStore) maxBytes() int {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultMaxBytes
}

func (s *KVStore) load(ctx context.Context) ([]Event, string, error) {
	b, version, err := s.Backend.LoadKey(ctx, s.Key)
	if errors.Is(err, kv.ErrNotFound) {
		return nil, version, nil
	}
	if err != nil {
		return nil, "", err
	}
	var list []Event
	if err := json.Unmarshal(b, &list); err != nil {
		// What is there is not ours to keep: it is replaced.
		return nil, version, nil
	}
	return list, version, nil
}

// merge puts the pending events among the kept ones, newest first, and
// trims to the limits.
func (s *KVStore) merge(kept, pending []Event) ([]byte, []Event) {
	list := make([]Event, 0, len(kept)+len(pending))
	list = append(list, kept...)
	list = append(list, pending...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Time.After(list[j].Time) })
	if keep := s.keep(); len(list) > keep {
		list = list[:keep]
	}
	b, _ := json.Marshal(list)
	for len(b) > s.maxBytes() && len(list) > 1 {
		// Drop the oldest tenth at a time: a marshal per event would be slow.
		cut := len(list) / 10
		if cut == 0 {
			cut = 1
		}
		list = list[:len(list)-cut]
		b, _ = json.Marshal(list)
	}
	return b, list
}

func (s *KVStore) Flush(ctx context.Context) error {
	s.saving.Lock()
	defer s.saving.Unlock()
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	var err error
	for try := 0; try < 5; try++ {
		var kept []Event
		var version string
		kept, version, err = s.load(ctx)
		if err != nil {
			break
		}
		b, _ := s.merge(kept, pending)
		if _, err = s.Backend.SaveKey(ctx, s.Key, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		break
	}
	if err != nil {
		// Back to the queue, before anything added meanwhile.
		s.mu.Lock()
		s.pending = append(pending, s.pending...)
		if keep := s.keep(); len(s.pending) > keep {
			s.pending = s.pending[len(s.pending)-keep:]
		}
		s.mu.Unlock()
	}
	return err
}

func (s *KVStore) List(ctx context.Context) ([]Event, error) {
	if err := s.Flush(ctx); err != nil {
		s.Logger.Warn("saving the audit log before reading it failed", "err", err)
	}
	s.saving.Lock()
	defer s.saving.Unlock()
	list, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	pending := append([]Event(nil), s.pending...)
	s.mu.Unlock()
	if len(pending) > 0 {
		_, list = s.merge(list, pending)
	}
	return list, nil
}
