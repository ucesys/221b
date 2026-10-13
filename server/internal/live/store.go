// Package live keeps the rows the browser shows and streams their changes.
// It recomputes only the rows of nodes that changed, sends each browser just
// the rows that differ, and falls back to a full snapshot when a browser
// falls behind.
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
)

// Source is where rows come from; kube.Cluster implements it.
type Source interface {
	Info() kube.Info
	NodeNames() []string
	Node(name string) *corev1.Node
	PodsOn(node string) []*corev1.Pod
	Unscheduled() int
	Usage(node string) (nodes.Usage, bool)
}

// Delta is one change sent to browsers. Versions increase by one per delta;
// a browser that sees a gap reconnects and gets a new snapshot.
type Delta struct {
	Version     uint64            `json:"version"`
	At          time.Time         `json:"at"`
	Cluster     *kube.Info        `json:"cluster,omitempty"`
	Upserts     []json.RawMessage `json:"upserts,omitempty"`
	Removes     []string          `json:"removes,omitempty"`
	Unscheduled *int              `json:"unscheduled,omitempty"`
}

// Options tune a Store.
type Options struct {
	// Debounce collects changes for this long before publishing them.
	Debounce time.Duration
	// Refresh recomputes every row this often, for problems that depend on
	// time passing (a pod stuck starting) rather than on an event.
	Refresh time.Duration
	Logger  *slog.Logger
	Now     func() time.Time
}

// Store holds the current rows and the browsers subscribed to changes.
type Store struct {
	src  Source
	opts Options

	mu          sync.Mutex
	rows        map[string][]byte
	info        kube.Info
	infoJSON    []byte
	unscheduled int
	version     uint64
	at          time.Time
	subs        map[*Sub]struct{}

	pendingMu  sync.Mutex
	dirty      map[string]struct{}
	dirtyAll   bool
	since      time.Time // when the oldest unpublished change arrived
	kick       chan struct{}
	recomputes uint64
	delays     Histogram
}

// DelayBuckets are the upper bounds, in seconds, of the change delay
// histogram.
var DelayBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10}

// Histogram counts how long changes took to reach browsers.
type Histogram struct {
	// Counts[i] is the number of changes that took at most DelayBuckets[i];
	// the last element counts every change.
	Counts []uint64
	Sum    float64
}

func (h *Histogram) observe(seconds float64) {
	if h.Counts == nil {
		h.Counts = make([]uint64, len(DelayBuckets)+1)
	}
	for i, b := range DelayBuckets {
		if seconds <= b {
			h.Counts[i]++
		}
	}
	h.Counts[len(DelayBuckets)]++
	h.Sum += seconds
}

// New creates a Store reading from src.
func New(src Source, o Options) *Store {
	if o.Debounce <= 0 {
		o.Debounce = 250 * time.Millisecond
	}
	if o.Refresh <= 0 {
		o.Refresh = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Store{
		src:   src,
		opts:  o,
		rows:  map[string][]byte{},
		subs:  map[*Sub]struct{}{},
		dirty: map[string]struct{}{},
		kick:  make(chan struct{}, 1),
	}
}

// Notify records a change; Run publishes it after the debounce interval.
func (s *Store) Notify(ch kube.Change) {
	s.pendingMu.Lock()
	for _, n := range ch.Nodes {
		s.dirty[n] = struct{}{}
	}
	s.dirtyAll = s.dirtyAll || ch.All
	if s.since.IsZero() {
		s.since = s.opts.Now()
	}
	s.pendingMu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run publishes changes until ctx is done.
func (s *Store) Run(ctx context.Context) {
	refresh := time.NewTicker(s.opts.Refresh)
	defer refresh.Stop()
	s.Notify(kube.Change{All: true, Info: true})
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			s.Notify(kube.Change{All: true})
		case <-s.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.opts.Debounce):
			}
			s.Process()
		}
	}
}

// Process applies the recorded changes now. Run calls it; tests call it
// directly.
func (s *Store) Process() {
	s.pendingMu.Lock()
	dirty, all, since := s.dirty, s.dirtyAll, s.since
	s.dirty, s.dirtyAll, s.since = map[string]struct{}{}, false, time.Time{}
	s.pendingMu.Unlock()

	info := s.src.Info()
	infoJSON, _ := json.Marshal(info)
	now := s.opts.Now().UTC()

	var names []string
	if info.Synced {
		if all {
			names = s.src.NodeNames()
		} else {
			for n := range dirty {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)

	type built struct {
		name string
		row  []byte // nil when the node is gone
	}
	results := make([]built, 0, len(names))
	present := make(map[string]struct{}, len(names))
	for _, name := range names {
		node := s.src.Node(name)
		if node == nil {
			results = append(results, built{name: name})
			continue
		}
		present[name] = struct{}{}
		var usage *nodes.Usage
		if u, ok := s.src.Usage(name); ok {
			usage = &u
		}
		row := nodes.BuildRow(node, s.src.PodsOn(name), usage, now)
		b, err := json.Marshal(row)
		if err != nil {
			s.opts.Logger.Error("encoding a node row", "node", name, "err", err)
			continue
		}
		results = append(results, built{name: name, row: b})
	}
	unscheduled := 0
	if info.Synced {
		unscheduled = s.src.Unscheduled()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputes++
	d := Delta{}
	for _, r := range results {
		old, had := s.rows[r.name]
		switch {
		case r.row == nil && had:
			delete(s.rows, r.name)
			d.Removes = append(d.Removes, r.name)
		case r.row != nil && !bytes.Equal(old, r.row):
			s.rows[r.name] = r.row
			d.Upserts = append(d.Upserts, json.RawMessage(r.row))
		}
	}
	if all && info.Synced {
		for name := range s.rows {
			if _, ok := present[name]; !ok {
				delete(s.rows, name)
				d.Removes = append(d.Removes, name)
			}
		}
		sort.Strings(d.Removes)
	}
	if !bytes.Equal(infoJSON, s.infoJSON) {
		s.info, s.infoJSON = info, infoJSON
		d.Cluster = &info
	}
	if unscheduled != s.unscheduled {
		s.unscheduled = unscheduled
		d.Unscheduled = &unscheduled
	}
	if d.Cluster == nil && d.Unscheduled == nil && len(d.Upserts) == 0 && len(d.Removes) == 0 {
		return
	}
	s.version++
	s.at = now
	d.Version, d.At = s.version, now
	msg, err := json.Marshal(d)
	if err != nil {
		s.opts.Logger.Error("encoding a delta", "err", err)
		return
	}
	for sub := range s.subs {
		sub.push(msg)
	}
	if !since.IsZero() {
		s.delays.observe(s.opts.Now().Sub(since).Seconds())
	}
}

// Delays returns how long published changes took from their watch event
// to the browsers' streams.
func (s *Store) Delays() Histogram {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := Histogram{Counts: append([]uint64(nil), s.delays.Counts...), Sum: s.delays.Sum}
	if h.Counts == nil {
		h.Counts = make([]uint64, len(DelayBuckets)+1)
	}
	return h
}

// snapshotLocked encodes every row. s.mu must be held.
func (s *Store) snapshotLocked() []byte {
	names := make([]string, 0, len(s.rows))
	for n := range s.rows {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteString(`{"version":`)
	b.WriteString(jsonNumber(s.version))
	b.WriteString(`,"at":`)
	at, _ := json.Marshal(s.at)
	b.Write(at)
	b.WriteString(`,"cluster":`)
	if s.infoJSON == nil {
		b.WriteString("null")
	} else {
		b.Write(s.infoJSON)
	}
	b.WriteString(`,"unscheduled":`)
	b.WriteString(jsonNumber(uint64(s.unscheduled)))
	b.WriteString(`,"rows":[`)
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(s.rows[n])
	}
	b.WriteString("]}")
	return b.Bytes()
}

// Snapshot returns every row and the cluster details as JSON.
func (s *Store) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// Row returns one node's row as JSON.
func (s *Store) Row(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[name]
	return r, ok
}

// Stats reports figures for the metrics endpoint.
func (s *Store) Stats() (version uint64, rows, subscribers int, recomputes uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version, len(s.rows), len(s.subs), s.recomputes
}

// Subscribe registers a browser and returns the snapshot it starts from.
// Deltas published after the snapshot arrive through the Sub.
func (s *Store) Subscribe() (*Sub, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &Sub{wake: make(chan struct{}, 1)}
	s.subs[sub] = struct{}{}
	return sub, s.snapshotLocked()
}

// Unsubscribe removes a browser.
func (s *Store) Unsubscribe(sub *Sub) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

// maxPending is how many deltas a slow browser may fall behind before it
// is sent a full snapshot instead.
const maxPending = 64

// Sub is one subscribed browser.
type Sub struct {
	mu      sync.Mutex
	pending [][]byte
	resync  bool
	wake    chan struct{}
}

// Wake is signalled when there is something to take.
func (sub *Sub) Wake() <-chan struct{} { return sub.wake }

// Take returns the deltas waiting for this browser, or resync=true when it
// fell too far behind and needs a new snapshot.
func (sub *Sub) Take() (deltas [][]byte, resync bool) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	deltas, resync = sub.pending, sub.resync
	sub.pending, sub.resync = nil, false
	return deltas, resync
}

func (sub *Sub) push(msg []byte) {
	sub.mu.Lock()
	if !sub.resync {
		if len(sub.pending) >= maxPending {
			sub.pending, sub.resync = nil, true
		} else {
			sub.pending = append(sub.pending, msg)
		}
	}
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

func jsonNumber(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
