// Package live keeps the rows the browser shows and streams their changes.
// Changes from the watches are collected and published on a shared refresh
// tick, so every browser sees the same update at the same moment and the
// cluster is read once however many browsers are open. Only the rows of
// nodes that changed are recomputed, each browser gets just the rows that
// differ, and a browser that falls behind gets a full snapshot.
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
	BootedAt(node string) (time.Time, bool)
	// Poll reads what is not watched, once per refresh, within ctx.
	Poll(ctx context.Context)
}

// Delta is one change sent to browsers. Versions increase by one per delta;
// a browser that sees a gap reconnects and gets a new snapshot.
type Delta struct {
	Version     uint64            `json:"version"`
	At          time.Time         `json:"at"`
	Cluster     *kube.Info        `json:"cluster,omitempty"`
	Settings    json.RawMessage   `json:"settings,omitempty"`
	Upserts     []json.RawMessage `json:"upserts,omitempty"`
	Removes     []string          `json:"removes,omitempty"`
	Unscheduled *int              `json:"unscheduled,omitempty"`
}

// Refresh is sent on every tick, with or without a delta, so browsers show
// when the last refresh was and notice if they missed one.
type Refresh struct {
	Version uint64    `json:"version"`
	At      time.Time `json:"at"`
}

// Options tune a Store.
type Options struct {
	// Refresh is how often changes are published and the source polled,
	// until SetSettings changes it.
	Refresh time.Duration
	// Recompute rebuilds every row this often, for problems that depend on
	// time passing (a pod stuck starting) rather than on an event.
	Recompute time.Duration
	// Prompt is how long news about the connection waits before it goes
	// out; it does not wait for the refresh tick.
	Prompt time.Duration
	Logger *slog.Logger
	Now    func() time.Time
}

// Store holds the current rows and the browsers subscribed to changes.
type Store struct {
	src  Source
	opts Options

	mu          sync.Mutex
	rows        map[string][]byte
	info        kube.Info
	infoJSON    []byte
	settings    []byte
	unscheduled int
	version     uint64
	at          time.Time
	refreshedAt time.Time
	subs        map[*Sub]struct{}
	refresh     time.Duration
	lastFull    time.Time

	pendingMu   sync.Mutex
	dirty       map[string]struct{}
	dirtyAll    bool
	dirtyInfo   bool
	dirtyTotals bool
	built       bool      // rows have been built since the cluster was listed
	since       time.Time // when the oldest unpublished change arrived
	kick        chan struct{}
	reset       chan struct{}
	recomputes  uint64
	delays      Histogram
}

// DelayBuckets are the upper bounds, in seconds, of the change delay
// histogram.
var DelayBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30}

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
	if o.Refresh <= 0 {
		o.Refresh = 3 * time.Second
	}
	if o.Recompute <= 0 {
		o.Recompute = 30 * time.Second
	}
	if o.Prompt <= 0 {
		o.Prompt = 250 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Store{
		src:     src,
		opts:    o,
		rows:    map[string][]byte{},
		subs:    map[*Sub]struct{}{},
		refresh: o.Refresh,
		dirty:   map[string]struct{}{},
		kick:    make(chan struct{}, 1),
		reset:   make(chan struct{}, 1),
	}
}

// Notify records a change. Rows wait for the next refresh; news about the
// connection goes out promptly, as do the first rows after the cluster is
// listed.
func (s *Store) Notify(ch kube.Change) {
	s.pendingMu.Lock()
	for _, n := range ch.Nodes {
		s.dirty[n] = struct{}{}
	}
	s.dirtyAll = s.dirtyAll || ch.All
	s.dirtyInfo = s.dirtyInfo || ch.Info
	s.dirtyTotals = s.dirtyTotals || ch.Totals
	if s.since.IsZero() {
		s.since = s.opts.Now()
	}
	prompt := ch.Info || (ch.All && !s.built)
	s.pendingMu.Unlock()
	if prompt {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// SetSettings publishes new shared settings to every browser and applies
// the refresh interval: the next refresh is one interval from now.
func (s *Store) SetSettings(refresh time.Duration, raw []byte) {
	s.mu.Lock()
	if refresh > 0 {
		s.refresh = refresh
	}
	changed := !bytes.Equal(raw, s.settings)
	s.settings = append([]byte(nil), raw...)
	s.mu.Unlock()
	if changed {
		s.publish(Delta{Settings: json.RawMessage(raw)}, time.Time{})
	}
	select {
	case s.reset <- struct{}{}:
	default:
	}
}

// RefreshInterval returns the interval in force.
func (s *Store) RefreshInterval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh
}

// Run publishes changes until ctx is done.
func (s *Store) Run(ctx context.Context) {
	// The cluster details go out at once, so a browser opened before the
	// first list sees the connection being made.
	s.Notify(kube.Change{Info: true})
	timer := time.NewTimer(s.RefreshInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.reset:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.RefreshInterval())
		case <-timer.C:
			s.Refresh(ctx)
			timer.Reset(s.RefreshInterval())
		case <-s.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.opts.Prompt):
			}
			s.pendingMu.Lock()
			rows := !s.built
			s.pendingMu.Unlock()
			s.process(rows)
		}
	}
}

// Refresh is one tick: poll the source, rebuild the rows that changed,
// publish one delta to every browser and tell them the refresh happened.
// Run calls it; tests call it directly.
func (s *Store) Refresh(ctx context.Context) {
	budget := min(10*time.Second, max(2*time.Second, s.RefreshInterval()))
	pollCtx, cancel := context.WithTimeout(ctx, budget)
	s.src.Poll(pollCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	s.process(true)
	now := s.opts.Now().UTC()
	s.mu.Lock()
	s.refreshedAt = now
	msg, err := json.Marshal(Refresh{Version: s.version, At: now})
	if err == nil {
		for sub := range s.subs {
			sub.push("refresh", msg)
		}
	}
	s.mu.Unlock()
}

// Process rebuilds the rows that changed and publishes them now, without
// polling or a refresh event. Tests use it.
func (s *Store) Process() { s.process(true) }

// process publishes what is pending: the cluster details always, the rows
// and totals when rows is true.
func (s *Store) process(rows bool) {
	s.pendingMu.Lock()
	var dirty map[string]struct{}
	var all, totals bool
	if rows {
		dirty, all, totals = s.dirty, s.dirtyAll, s.dirtyTotals
		s.dirty, s.dirtyAll, s.dirtyTotals = map[string]struct{}{}, false, false
	}
	s.dirtyInfo = false
	// The delay histogram measures changes to rows, which wait for the tick.
	var since time.Time
	if rows {
		since, s.since = s.since, time.Time{}
	}
	s.pendingMu.Unlock()

	info := s.src.Info()
	infoJSON, _ := json.Marshal(info)
	now := s.opts.Now().UTC()

	var names []string
	if rows && info.Synced {
		s.mu.Lock()
		if !all && now.Sub(s.lastFull) >= s.opts.Recompute {
			all = true
		}
		if all {
			s.lastFull = now
		}
		s.mu.Unlock()
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
		var booted *time.Time
		if b, ok := s.src.BootedAt(name); ok {
			booted = &b
		}
		row := nodes.BuildRow(node, s.src.PodsOn(name), usage, booted, now)
		b, err := json.Marshal(row)
		if err != nil {
			s.opts.Logger.Error("encoding a node row", "node", name, "err", err)
			continue
		}
		results = append(results, built{name: name, row: b})
	}
	unscheduled := -1
	if rows && info.Synced && (all || totals || !s.isBuilt()) {
		unscheduled = s.src.Unscheduled()
	}

	s.mu.Lock()
	if rows {
		s.recomputes++
	}
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
	if unscheduled >= 0 && unscheduled != s.unscheduled {
		s.unscheduled = unscheduled
		d.Unscheduled = &unscheduled
	}
	s.mu.Unlock()
	if rows && info.Synced {
		s.pendingMu.Lock()
		s.built = true
		s.pendingMu.Unlock()
	}
	s.publish(d, since)
}

func (s *Store) isBuilt() bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.built
}

// publish sends a delta to every browser, unless it is empty. since, when
// set, is when the oldest change it carries arrived.
func (s *Store) publish(d Delta, since time.Time) {
	if d.Cluster == nil && d.Settings == nil && d.Unscheduled == nil && len(d.Upserts) == 0 && len(d.Removes) == 0 {
		return
	}
	now := s.opts.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	s.at = now
	d.Version, d.At = s.version, now
	msg, err := json.Marshal(d)
	if err != nil {
		s.opts.Logger.Error("encoding a delta", "err", err)
		return
	}
	for sub := range s.subs {
		sub.push("delta", msg)
	}
	if !since.IsZero() {
		s.delays.observe(now.Sub(since).Seconds())
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
	b.WriteString(`,"refreshedAt":`)
	if s.refreshedAt.IsZero() {
		b.WriteString("null")
	} else {
		r, _ := json.Marshal(s.refreshedAt)
		b.Write(r)
	}
	b.WriteString(`,"cluster":`)
	if s.infoJSON == nil {
		b.WriteString("null")
	} else {
		b.Write(s.infoJSON)
	}
	b.WriteString(`,"settings":`)
	if s.settings == nil {
		b.WriteString("null")
	} else {
		b.Write(s.settings)
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

// Snapshot returns every row, the cluster details and the settings as JSON.
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

// maxPending is how many events a slow browser may fall behind before it
// is sent a full snapshot instead.
const maxPending = 64

// Event is one server-sent event waiting for a browser.
type Event struct {
	Name string // "delta" or "refresh"
	Data []byte
}

// Sub is one subscribed browser.
type Sub struct {
	mu      sync.Mutex
	pending []Event
	resync  bool
	wake    chan struct{}
}

// Wake is signalled when there is something to take.
func (sub *Sub) Wake() <-chan struct{} { return sub.wake }

// Take returns the events waiting for this browser, or resync=true when it
// fell too far behind and needs a new snapshot.
func (sub *Sub) Take() (events []Event, resync bool) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	events, resync = sub.pending, sub.resync
	sub.pending, sub.resync = nil, false
	return events, resync
}

func (sub *Sub) push(name string, msg []byte) {
	sub.mu.Lock()
	if !sub.resync {
		if len(sub.pending) >= maxPending {
			sub.pending, sub.resync = nil, true
		} else {
			sub.pending = append(sub.pending, Event{Name: name, Data: msg})
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
