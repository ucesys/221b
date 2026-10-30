// Package live keeps the rows the browsers show and streams their changes.
// Changes from the watches are collected and published on a shared refresh
// tick, so every browser sees the same update at the same moment and the
// cluster is read once however many browsers are open. Rows are grouped in
// views, one per tab: a browser subscribes to the views it shows, only the
// rows that changed are recomputed, each browser gets just the rows that
// differ, and a browser that falls behind gets a full snapshot.
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/views"
)

// Source is where rows come from; kube.Cluster implements it.
type Source interface {
	views.Source
	Info() kube.Info
	Keys(kind string) []string
	Node(name string) *corev1.Node
	PodsOn(node string) []*corev1.Pod
	Unscheduled() int
	Usage(node string) (nodes.Usage, bool)
	BootedAt(node string) (time.Time, bool)
	PodUsage() map[string]nodes.Usage
	// WantPodUsage says whether pod CPU and memory should be read on the
	// next polls; the store wants them while a browser shows pods.
	WantPodUsage(bool)
	// Poll reads what is not watched, once per refresh, within ctx.
	Poll(ctx context.Context)
}

// The views browsers can subscribe to. A namespace's objects are the view
// "ns:" + its name, which exists while a browser shows it.
const (
	ViewNodes      = "nodes"
	ViewPods       = "pods"
	ViewNamespaces = "namespaces"
	ViewServices   = "services"
	ViewHelm       = "helm"
	// ViewAlerts are the objects with something wrong (P4-07).
	ViewAlerts = "alerts"
	// ViewFleets are the cluster's fleets with their members (P4-01).
	ViewFleets = "fleets"
	// ViewAccess are the roles, bindings and ServiceAccounts the Access
	// tab reads (P4-03).
	ViewAccess = "access"
	// ViewPolicies are what the Policies tab reads (P4-04 to P4-06).
	ViewPolicies = "policies"
	// ViewNetworks are the Networks tab's traces and nodes (P4-02).
	ViewNetworks = "networks"
	// ViewStorage is the Storage tab's classes, volumes, claims and
	// storage quotas (ST-01 to ST-12).
	ViewStorage = "storage"
	// NamespacePrefix starts the view of one namespace's objects.
	NamespacePrefix = "ns:"
)

// Rows is one change to a view, sent to the browsers showing it. Versions
// increase by one per change of a view; a browser that sees a gap
// reconnects and gets a new snapshot.
type Rows struct {
	View    string            `json:"view"`
	Version uint64            `json:"version"`
	At      time.Time         `json:"at"`
	Upserts []json.RawMessage `json:"upserts,omitempty"`
	Removes []string          `json:"removes,omitempty"`
	// Usage carries CPU and memory in use by row key, for views that send
	// them apart from the rows (pods); UsageRemoves the keys that lost it.
	Usage        map[string]json.RawMessage `json:"usage,omitempty"`
	UsageRemoves []string                   `json:"usageRemoves,omitempty"`
}

// Meta is a change to what belongs to no view: the cluster details, the
// settings and the totals.
type Meta struct {
	At          time.Time       `json:"at"`
	Cluster     *kube.Info      `json:"cluster,omitempty"`
	Settings    json.RawMessage `json:"settings,omitempty"`
	Unscheduled *int            `json:"unscheduled,omitempty"`
	// Counts are cluster-wide totals by view name, for the tabs.
	Counts map[string]int `json:"counts,omitempty"`
}

// Refresh is sent on every tick, with or without changes, so browsers show
// when the last refresh was and notice if they missed one.
type Refresh struct {
	At       time.Time         `json:"at"`
	Versions map[string]uint64 `json:"versions"`
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
	// Idle is how long a view nobody shows is kept before it is dropped.
	Idle   time.Duration
	Logger *slog.Logger
	Now    func() time.Time
	// Cluster is the connection's id, under which the settings keep its
	// fleets.
	Cluster string
	// Active says whether this replica reads the cluster (ADR-70): the
	// source is polled only while it does, and what it last read is kept.
	// nil polls on every tick.
	Active func() bool
}

// viewDef says how a view's rows are built.
type viewDef struct {
	name string
	// lazy views exist only while a browser shows them.
	lazy bool
	// keys lists every row; build renders one, or ok=false when it is gone.
	keys  func(src Source) []string
	build func(src Source, key string, now time.Time) (any, bool)
	// buildFrom renders one row with the store's settings at hand, in
	// place of build: an alert reads the Pending grace period.
	buildFrom func(s *Store, key string, now time.Time) (any, bool)
	// whole renders every row at once, for views whose rows are not rebuilt
	// one by one.
	whole func(s *Store, now time.Time) map[string]any
	// usage reads CPU and memory in use by row key, sent apart from the
	// rows because it changes on every read.
	usage func(src Source) map[string]nodes.Usage
	// decorate adds to a built row what is not in the cluster: an alert's
	// acknowledgement.
	decorate func(s *Store, key string, row any) any
}

// view is one view's current rows.
type view struct {
	def      viewDef
	rows     map[string][]byte
	usage    map[string][]byte
	version  uint64
	at       time.Time
	subs     int
	idle     time.Time // when the last subscriber left
	lastFull time.Time
	built    bool
}

// Store holds the current rows and the browsers subscribed to changes.
type Store struct {
	src  Source
	opts Options

	// buildMu serialises building rows, by the tick and by subscriptions.
	buildMu sync.Mutex

	mu          sync.Mutex
	views       map[string]*view
	info        kube.Info
	infoJSON    []byte
	settings    []byte
	unscheduled int
	counts      map[string]int
	countsJSON  []byte
	at          time.Time
	refreshedAt time.Time
	subs        map[*Sub]struct{}
	refresh     time.Duration
	// fleets are this cluster's fleets, from the settings.
	fleets atomic.Pointer[[]fleets.Fleet]
	acks   atomic.Pointer[map[string]acks.Ack]
	// grace is the Pending grace period of the alerts, in nanoseconds.
	grace      atomic.Int64
	recomputes uint64
	delays     Histogram

	pendingMu   sync.Mutex
	dirty       map[string]map[string]struct{}
	dirtyInfo   bool
	dirtyTotals bool
	built       bool      // rows have been built since the cluster was listed
	rowsNow     bool      // a change someone made here: publish rows without waiting for the tick
	since       time.Time // when the oldest unpublished change arrived
	kick        chan struct{}
	reset       chan struct{}
	refreshNow  chan struct{}
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
	if o.Idle <= 0 {
		o.Idle = time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Store{
		src:        src,
		opts:       o,
		views:      map[string]*view{},
		subs:       map[*Sub]struct{}{},
		refresh:    o.Refresh,
		dirty:      map[string]map[string]struct{}{},
		kick:       make(chan struct{}, 1),
		reset:      make(chan struct{}, 1),
		refreshNow: make(chan struct{}, 1),
	}
	s.grace.Store(int64(settings.DefaultPendingGrace))
	for _, d := range staticViews {
		if !d.lazy {
			s.views[d.name] = &view{def: d, rows: map[string][]byte{}, usage: map[string][]byte{}}
		}
	}
	return s
}

// staticViews are the views with a fixed name.
var staticViews = []viewDef{
	{
		name: ViewNodes,
		keys: func(src Source) []string { return src.Keys("nodes") },
		build: func(src Source, key string, now time.Time) (any, bool) {
			node := src.Node(key)
			if node == nil {
				return nil, false
			}
			var usage *nodes.Usage
			if u, ok := src.Usage(key); ok {
				usage = &u
			}
			var booted *time.Time
			if b, ok := src.BootedAt(key); ok {
				booted = &b
			}
			return nodes.BuildRow(node, src.PodsOn(key), usage, booted, now), true
		},
	},
	{
		name: ViewPods,
		lazy: true,
		keys: func(src Source) []string { return src.Keys("pods") },
		build: func(src Source, key string, now time.Time) (any, bool) {
			ns, name, _ := strings.Cut(key, "/")
			p, _ := src.Object("pods", ns, name).(*corev1.Pod)
			if p == nil {
				return nil, false
			}
			return views.BuildPodRow(src, p, now), true
		},
		usage: func(src Source) map[string]nodes.Usage { return src.PodUsage() },
	},
	{
		name: ViewNamespaces,
		keys: func(src Source) []string { return src.Keys("namespaces") },
		build: func(src Source, key string, now time.Time) (any, bool) {
			ns, _ := src.Object("namespaces", "", key).(*corev1.Namespace)
			if ns == nil {
				return nil, false
			}
			return views.BuildNamespaceRow(src, ns, now), true
		},
	},
	{
		name: ViewServices,
		keys: func(src Source) []string { return src.Keys("services") },
		build: func(src Source, key string, now time.Time) (any, bool) {
			ns, name, _ := strings.Cut(key, "/")
			svc, _ := src.Object("services", ns, name).(*corev1.Service)
			if svc == nil {
				return nil, false
			}
			return views.BuildServiceRow(src, svc, now), true
		},
	},
	{
		name: ViewHelm,
		keys: func(src Source) []string {
			if !src.SecretsRead() {
				return nil
			}
			return views.ReleaseKeys(src)
		},
		build: func(src Source, key string, now time.Time) (any, bool) {
			if !src.SecretsRead() {
				return nil, false
			}
			ns, name, _ := strings.Cut(key, "/")
			return views.BuildReleaseRow(src, ns, name, now)
		},
	},
	{
		name: ViewAlerts,
		keys: func(src Source) []string { return views.AlertKeys(src) },
		buildFrom: func(s *Store, key string, now time.Time) (any, bool) {
			return views.BuildAlert(s.src, key, now, time.Duration(s.grace.Load()))
		},
		decorate: decorateAlert,
	},
	{
		name:  ViewFleets,
		lazy:  true,
		whole: buildFleets,
	},
	{
		name:  ViewPolicies,
		lazy:  true,
		keys:  func(src Source) []string { return views.PolicyKeys(src) },
		build: func(src Source, key string, now time.Time) (any, bool) { return views.BuildPolicy(src, key, now) },
	},
	{
		name:  ViewNetworks,
		lazy:  true,
		keys:  func(src Source) []string { return views.NetworkKeys(src) },
		build: func(src Source, key string, now time.Time) (any, bool) { return views.BuildNetwork(src, key, now) },
	},
	{
		name:  ViewStorage,
		lazy:  true,
		whole: buildStorage,
	},
	{
		name:  ViewAccess,
		lazy:  true,
		keys:  func(src Source) []string { return views.AccessKeys(src) },
		build: func(src Source, key string, now time.Time) (any, bool) { return views.BuildAccess(src, key, now) },
	},
}

// viewDefFor returns the definition of a view name, or an error for a name
// that is not a view.
func viewDefFor(name string) (viewDef, error) {
	for _, d := range staticViews {
		if d.name == name {
			return d, nil
		}
	}
	if ns, ok := strings.CutPrefix(name, NamespacePrefix); ok && ns != "" && !strings.ContainsAny(ns, "/ ") {
		return viewDef{
			name: name,
			lazy: true,
			whole: func(s *Store, now time.Time) map[string]any {
				rows := views.BuildObjectRows(s.src, ns, now)
				out := make(map[string]any, len(rows))
				for _, r := range rows {
					out[r.Key] = r
				}
				return out
			},
		}, nil
	}
	return viewDef{}, fmt.Errorf("no view named %q", name)
}

// ValidViews reports an error when any name is not a view.
func ValidViews(names []string) error {
	for _, n := range names {
		if _, err := viewDefFor(n); err != nil {
			return err
		}
	}
	return nil
}

// Notify records a change. Rows wait for the next refresh; news about the
// connection goes out promptly, as do the first rows after the cluster is
// listed.
func (s *Store) Notify(ch kube.Change) {
	s.pendingMu.Lock()
	for name, keys := range ch.Views {
		d := s.dirty[name]
		if d == nil {
			d = map[string]struct{}{}
			s.dirty[name] = d
		}
		for _, k := range keys {
			d[k] = struct{}{}
		}
	}
	s.dirtyInfo = s.dirtyInfo || ch.Info
	s.dirtyTotals = s.dirtyTotals || ch.Totals
	if s.since.IsZero() {
		s.since = s.opts.Now()
	}
	prompt := ch.Info || (len(ch.Views) > 0 && !s.built)
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
	if changed {
		s.publishMetaLocked(Meta{Settings: json.RawMessage(raw)})
	}
	s.mu.Unlock()
	if changed {
		var in struct {
			Fleets              map[string][]fleets.Fleet `json:"fleets"`
			Acks                map[string][]acks.Ack     `json:"acks"`
			PendingGraceSeconds *int                      `json:"pendingGraceSeconds"`
		}
		_ = json.Unmarshal(raw, &in)
		if g := int64(settings.Settings{PendingGraceSeconds: in.PendingGraceSeconds}.PendingGrace()); g != s.grace.Swap(g) {
			s.Notify(kube.Change{Views: map[string][]string{ViewAlerts: {kube.AllRows}}})
			s.PublishSoon()
		}
		list := in.Fleets[s.opts.Cluster]
		if old := s.fleets.Load(); old == nil || !reflect.DeepEqual(*old, list) {
			s.fleets.Store(&list)
			// Someone saved them here: they see them at once, not a refresh later.
			s.Notify(kube.Change{Views: map[string][]string{ViewFleets: {kube.AllRows}}})
			s.PublishSoon()
		}
		ackd := acks.Index(in.Acks[s.opts.Cluster])
		if old := s.acks.Load(); (old == nil && len(ackd) > 0) || (old != nil && !reflect.DeepEqual(*old, ackd)) {
			s.acks.Store(&ackd)
			s.Notify(kube.Change{Views: map[string][]string{ViewAlerts: {kube.AllRows}}})
			s.PublishSoon()
		}
	}
	select {
	case s.reset <- struct{}{}:
	default:
	}
}

// PublishSoon publishes the pending rows within the prompt delay instead
// of at the next refresh, for changes made through the console itself.
func (s *Store) PublishSoon() {
	s.pendingMu.Lock()
	s.rowsNow = true
	s.pendingMu.Unlock()
	select {
	case s.kick <- struct{}{}:
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
		case <-s.refreshNow:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			s.Refresh(ctx)
			timer.Reset(s.RefreshInterval())
		case <-s.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.opts.Prompt):
			}
			s.pendingMu.Lock()
			rows := !s.built || s.rowsNow
			s.rowsNow = false
			s.pendingMu.Unlock()
			s.process(rows)
		}
	}
}

// RefreshNow asks Run for a tick at once rather than at the timer: a
// replica that becomes the leader reads usage without waiting (ADR-70).
func (s *Store) RefreshNow() {
	select {
	case s.refreshNow <- struct{}{}:
	default:
	}
}

// Refresh is one tick: poll the source, rebuild the rows that changed,
// publish the changes to every browser and tell them the refresh happened.
// Run calls it; tests call it directly.
func (s *Store) Refresh(ctx context.Context) {
	if s.opts.Active == nil || s.opts.Active() {
		budget := min(10*time.Second, max(2*time.Second, s.RefreshInterval()))
		pollCtx, cancel := context.WithTimeout(ctx, budget)
		s.src.Poll(pollCtx)
		cancel()
	}
	if ctx.Err() != nil {
		return
	}
	s.process(true)
	now := s.opts.Now().UTC()
	s.mu.Lock()
	s.refreshedAt = now
	for sub := range s.subs {
		versions := make(map[string]uint64, len(sub.views))
		for _, name := range sub.views {
			if v := s.views[name]; v != nil {
				versions[name] = v.version
			}
		}
		if msg, err := json.Marshal(Refresh{At: now, Versions: versions}); err == nil {
			sub.push("refresh", msg)
		}
	}
	s.mu.Unlock()
}

// Process rebuilds the rows that changed and publishes them now, without
// polling or a refresh event. Tests use it.
func (s *Store) Process() { s.process(true) }

// built is one rebuilt row.
type built struct {
	key string
	row []byte // nil when the row is gone
}

// rebuilt is the result of rebuilding one view.
type rebuilt struct {
	name    string
	all     bool
	rows    []built
	present map[string]struct{}
	usage   map[string][]byte
}

// process publishes what is pending: the cluster details always, the rows
// and totals when rows is true.
func (s *Store) process(rows bool) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()

	s.pendingMu.Lock()
	var dirty map[string]map[string]struct{}
	var totals bool
	if rows {
		dirty, totals = s.dirty, s.dirtyTotals
		s.dirty, s.dirtyTotals = map[string]map[string]struct{}{}, false
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

	// Decide what to rebuild, then build it without holding the lock.
	var results []rebuilt
	if rows && info.Synced {
		type job struct {
			def   viewDef
			all   bool
			usage bool
			keys  []string
		}
		var jobs []job
		s.mu.Lock()
		for name, v := range s.views {
			if v.def.lazy && v.subs == 0 && now.Sub(v.idle) >= s.opts.Idle {
				delete(s.views, name)
				continue
			}
			d := dirty[name]
			_, all := d[kube.AllRows]
			_, usage := d[kube.UsageOnly]
			if !v.built || now.Sub(v.lastFull) >= s.opts.Recompute {
				all = true
			}
			if all {
				v.lastFull = now
			} else if len(d) == 0 {
				continue
			}
			j := job{def: v.def, all: all, usage: all || usage}
			if !all {
				for k := range d {
					if k != kube.UsageOnly {
						j.keys = append(j.keys, k)
					}
				}
			}
			jobs = append(jobs, j)
		}
		s.mu.Unlock()
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].def.name < jobs[j].def.name })
		for _, j := range jobs {
			results = append(results, s.build(j.def, j.all, j.keys, j.usage, now))
		}
	}
	unscheduled := -1
	if rows && info.Synced && (totals || !s.isBuilt()) {
		unscheduled = s.src.Unscheduled()
	}
	var counts map[string]int
	if rows && info.Synced {
		counts = map[string]int{
			ViewNodes: s.src.Count("nodes", ""), ViewPods: s.src.Count("pods", ""),
			ViewNamespaces: s.src.Count("namespaces", ""), ViewServices: s.src.Count("services", ""),
		}
	}

	s.mu.Lock()
	if rows {
		s.recomputes++
	}
	published := false
	for _, r := range results {
		v := s.views[r.name]
		if v == nil {
			continue
		}
		if s.applyLocked(v, r, now) {
			published = true
		}
	}
	m := Meta{}
	if !bytes.Equal(infoJSON, s.infoJSON) {
		s.info, s.infoJSON = info, infoJSON
		m.Cluster = &info
	}
	if unscheduled >= 0 && unscheduled != s.unscheduled {
		s.unscheduled = unscheduled
		m.Unscheduled = &unscheduled
	}
	if counts != nil {
		if v := s.views[ViewHelm]; v != nil {
			counts[ViewHelm] = len(v.rows)
		}
		if b, _ := json.Marshal(counts); !bytes.Equal(b, s.countsJSON) {
			s.counts, s.countsJSON = counts, b
			m.Counts = counts
		}
	}
	if m.Cluster != nil || m.Unscheduled != nil || m.Counts != nil {
		s.publishMetaLocked(m)
	}
	if published && !since.IsZero() {
		s.delays.observe(s.opts.Now().UTC().Sub(since).Seconds())
	}
	s.mu.Unlock()
	if rows && info.Synced {
		s.pendingMu.Lock()
		s.built = true
		s.pendingMu.Unlock()
	}
}

// build renders a view's rows: every row when all is set, the given keys
// otherwise, and the usage when asked. buildMu must be held.
func (s *Store) build(d viewDef, all bool, keys []string, usage bool, now time.Time) rebuilt {
	r := rebuilt{name: d.name, all: all}
	encode := func(key string, row any) {
		b, err := json.Marshal(row)
		if err != nil {
			s.opts.Logger.Error("encoding a row", "view", d.name, "key", key, "err", err)
			return
		}
		r.rows = append(r.rows, built{key: key, row: b})
	}
	switch {
	case d.whole != nil && !all && len(keys) == 0:
	case d.whole != nil:
		r.all = true
		rows := d.whole(s, now)
		r.present = make(map[string]struct{}, len(rows))
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			r.present[k] = struct{}{}
			encode(k, rows[k])
		}
	default:
		if all {
			keys = d.keys(s.src)
		}
		sort.Strings(keys)
		r.present = make(map[string]struct{}, len(keys))
		for _, k := range keys {
			var row any
			var ok bool
			if d.buildFrom != nil {
				row, ok = d.buildFrom(s, k, now)
			} else {
				row, ok = d.build(s.src, k, now)
			}
			if !ok {
				r.rows = append(r.rows, built{key: k})
				continue
			}
			if d.decorate != nil {
				row = d.decorate(s, k, row)
			}
			r.present[k] = struct{}{}
			encode(k, row)
		}
	}
	if d.usage != nil && usage {
		usage := d.usage(s.src)
		r.usage = make(map[string][]byte, len(usage))
		for k, u := range usage {
			r.usage[k], _ = json.Marshal(u)
		}
	}
	return r
}

// applyLocked stores rebuilt rows and publishes what differs, reporting
// whether anything did. s.mu must be held.
func (s *Store) applyLocked(v *view, r rebuilt, now time.Time) bool {
	ev := Rows{View: v.def.name}
	// The keys of the upserts, in order, for the filters.
	var upsertKeys []string
	for _, b := range r.rows {
		old, had := v.rows[b.key]
		switch {
		case b.row == nil && had:
			delete(v.rows, b.key)
			ev.Removes = append(ev.Removes, b.key)
		case b.row != nil && !bytes.Equal(old, b.row):
			v.rows[b.key] = b.row
			ev.Upserts = append(ev.Upserts, json.RawMessage(b.row))
			upsertKeys = append(upsertKeys, b.key)
		}
	}
	if r.all {
		for key := range v.rows {
			if _, ok := r.present[key]; !ok {
				delete(v.rows, key)
				ev.Removes = append(ev.Removes, key)
			}
		}
		sort.Strings(ev.Removes)
	}
	if r.usage != nil {
		for key, u := range r.usage {
			if _, ok := v.rows[key]; !ok {
				continue // usage of a pod not (yet) in the rows
			}
			if !bytes.Equal(v.usage[key], u) {
				v.usage[key] = u
				if ev.Usage == nil {
					ev.Usage = map[string]json.RawMessage{}
				}
				ev.Usage[key] = json.RawMessage(u)
			}
		}
		for key := range v.usage {
			if _, ok := r.usage[key]; !ok {
				delete(v.usage, key)
				ev.UsageRemoves = append(ev.UsageRemoves, key)
			} else if _, ok := v.rows[key]; !ok {
				delete(v.usage, key)
				ev.UsageRemoves = append(ev.UsageRemoves, key)
			}
		}
		sort.Strings(ev.UsageRemoves)
	}
	v.built = true
	if len(ev.Upserts) == 0 && len(ev.Removes) == 0 && len(ev.Usage) == 0 && len(ev.UsageRemoves) == 0 {
		return false
	}
	v.version++
	v.at = now
	s.at = now
	ev.Version, ev.At = v.version, now
	msg, err := json.Marshal(ev)
	if err != nil {
		s.opts.Logger.Error("encoding a change", "view", v.def.name, "err", err)
		return false
	}
	// A browser with a filter gets the change with only the rows it may
	// see; browsers sharing a filter share the encoding.
	filtered := map[Filter][]byte{}
	for sub := range s.subs {
		if !sub.has[v.def.name] {
			continue
		}
		if sub.filter == nil {
			sub.push("rows", msg)
			continue
		}
		fm, ok := filtered[sub.filter]
		if !ok {
			fm = filterRows(ev, upsertKeys, sub.filter)
			filtered[sub.filter] = fm
		}
		if fm != nil {
			sub.push("rows", fm)
		}
	}
	return true
}

// filterRows encodes a change with only the rows a filter allows; nil
// when nothing of it may be sent. Removes are filtered too: a key names
// its namespace and object, which a person who may not see them must not
// learn from its going (P3-02).
func filterRows(ev Rows, upsertKeys []string, f Filter) []byte {
	out := Rows{View: ev.View, Version: ev.Version, At: ev.At}
	for _, key := range ev.Removes {
		if f.Allow(ev.View, key) {
			out.Removes = append(out.Removes, key)
		}
	}
	for _, key := range ev.UsageRemoves {
		if f.Allow(ev.View, key) {
			out.UsageRemoves = append(out.UsageRemoves, key)
		}
	}
	for i, key := range upsertKeys {
		if f.Allow(ev.View, key) {
			out.Upserts = append(out.Upserts, Redact(ev.View, ev.Upserts[i], f))
		}
	}
	for key, u := range ev.Usage {
		if f.Allow(ev.View, key) {
			if out.Usage == nil {
				out.Usage = map[string]json.RawMessage{}
			}
			out.Usage[key] = u
		}
	}
	if len(out.Upserts) == 0 && len(out.Removes) == 0 && len(out.Usage) == 0 && len(out.UsageRemoves) == 0 {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

func (s *Store) isBuilt() bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.built
}

// publishMetaLocked sends cluster details, settings or totals to every
// browser. s.mu must be held.
func (s *Store) publishMetaLocked(m Meta) {
	now := s.opts.Now().UTC()
	s.at = now
	m.At = now
	msg, err := json.Marshal(m)
	if err != nil {
		s.opts.Logger.Error("encoding the cluster details", "err", err)
		return
	}
	// Totals are what each browser may see: a filtered browser gets its own.
	filtered := map[Filter][]byte{}
	for sub := range s.subs {
		if sub.filter == nil || m.Counts == nil {
			sub.push("meta", msg)
			continue
		}
		fm, ok := filtered[sub.filter]
		if !ok {
			own := m
			own.Counts = s.countsForLocked(sub.filter)
			fm, _ = json.Marshal(own)
			filtered[sub.filter] = fm
		}
		sub.push("meta", fm)
	}
}

// countsForLocked are the tab totals as a filter sees them: nodes when
// they may be listed, and namespaces, pods and services in the namespaces
// that are theirs. s.mu must be held.
func (s *Store) countsForLocked(f Filter) map[string]int {
	out := map[string]int{ViewNodes: 0, ViewNamespaces: 0, ViewPods: 0, ViewServices: 0}
	if f.AllowCluster("nodes") {
		out[ViewNodes] = s.counts[ViewNodes]
	}
	for _, ns := range s.src.Keys("namespaces") {
		if !f.AllowNamespace(ns) {
			continue
		}
		out[ViewNamespaces]++
		out[ViewPods] += s.src.Count("pods", ns)
		if f.Allow(ViewServices, ns+"/") {
			out[ViewServices] += s.src.Count("services", ns)
		}
	}
	if v := s.views[ViewHelm]; v != nil {
		for key := range v.rows {
			if f.Allow(ViewHelm, key) {
				out[ViewHelm]++
			}
		}
	}
	return out
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

// snapshotLocked encodes the rows of the named views, with the cluster
// details and the settings. s.mu must be held.
func (s *Store) snapshotLocked(names []string, f Filter) []byte {
	var b bytes.Buffer
	b.WriteString(`{"at":`)
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
	fmt.Fprintf(&b, `,"unscheduled":%d,"counts":`, s.unscheduled)
	switch {
	case s.countsJSON == nil:
		b.WriteString("{}")
	case f != nil:
		cb, _ := json.Marshal(s.countsForLocked(f))
		b.Write(cb)
	default:
		b.Write(s.countsJSON)
	}
	b.WriteString(`,"views":{`)
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		n, _ := json.Marshal(name)
		b.Write(n)
		b.WriteByte(':')
		v := s.views[name]
		if v == nil {
			b.WriteString(`{"version":0,"rows":[]}`)
			continue
		}
		fmt.Fprintf(&b, `{"version":%d,"rows":[`, v.version)
		keys := make([]string, 0, len(v.rows))
		for k := range v.rows {
			if f == nil || f.Allow(name, k) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(Redact(name, v.rows[k], f))
		}
		b.WriteByte(']')
		if v.def.usage != nil {
			b.WriteString(`,"usage":{`)
			keys = keys[:0]
			for k := range v.usage {
				if f == nil || f.Allow(name, k) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				kb, _ := json.Marshal(k)
				b.Write(kb)
				b.WriteByte(':')
				b.Write(v.usage[k])
			}
			b.WriteByte('}')
		}
		b.WriteByte('}')
	}
	b.WriteString("}}")
	return b.Bytes()
}

// Snapshot returns the rows of the named views, the cluster details and
// the settings as JSON, with the rows the filter allows (nil: every row).
// Views nobody showed are built first.
func (s *Store) Snapshot(names []string, f Filter) []byte {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureViewsLocked(names)
	return s.snapshotLocked(names, f)
}

// ensureViewsLocked builds the named views that do not exist yet. buildMu
// and s.mu must be held; s.mu is released while the rows are built.
func (s *Store) ensureViewsLocked(names []string) {
	now := s.opts.Now().UTC()
	var missing []viewDef
	for _, name := range names {
		if s.views[name] == nil {
			if d, err := viewDefFor(name); err == nil {
				missing = append(missing, d)
			}
		}
	}
	if len(missing) == 0 {
		return
	}
	synced := s.info.Synced
	s.mu.Unlock()
	var fresh []rebuilt
	if synced {
		for _, d := range missing {
			fresh = append(fresh, s.build(d, true, nil, true, now))
		}
	}
	s.mu.Lock()
	for _, d := range missing {
		if s.views[d.name] == nil {
			s.views[d.name] = &view{def: d, rows: map[string][]byte{}, usage: map[string][]byte{}, lastFull: now, idle: now}
		}
	}
	for _, r := range fresh {
		if v := s.views[r.name]; v != nil && v.version == 0 {
			s.applyLocked(v, r, now)
		}
	}
}

// Synced reports whether the store has taken the cluster's first full
// listing, so a snapshot has every row: the cluster says it is synced a
// moment before the rows reach the store.
func (s *Store) Synced() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info.Synced
}

// Row returns one row of a view as JSON.
func (s *Store) Row(view, key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.views[view]
	if v == nil {
		return nil, false
	}
	r, ok := v.rows[key]
	return r, ok
}

// ViewStats are a view's figures for the metrics endpoint.
type ViewStats struct {
	Name        string
	Version     uint64
	Rows        int
	Subscribers int
}

// Stats reports figures for the metrics endpoint.
func (s *Store) Stats() (views []ViewStats, subscribers int, recomputes uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, v := range s.views {
		views = append(views, ViewStats{Name: name, Version: v.version, Rows: len(v.rows), Subscribers: v.subs})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, len(s.subs), s.recomputes
}

// Subscribe registers a browser showing the named views and returns the
// snapshot it starts from. Changes published after the snapshot arrive
// through the Sub, with the rows the filter allows (nil: every row).
// Views nobody showed are built first.
func (s *Store) Subscribe(names []string, f Filter) (*Sub, []byte, error) {
	if err := ValidViews(names); err != nil {
		return nil, nil, err
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureViewsLocked(names)
	sub := &Sub{views: append([]string(nil), names...), has: map[string]bool{}, wake: make(chan struct{}, 1), filter: f}
	for _, name := range names {
		if !sub.has[name] {
			sub.has[name] = true
			s.views[name].subs++
		}
	}
	s.subs[sub] = struct{}{}
	s.wantUsageLocked()
	return sub, s.snapshotLocked(names, f), nil
}

// Unsubscribe removes a browser. Views it alone showed are kept a while,
// in case it comes back, then dropped.
func (s *Store) Unsubscribe(sub *Sub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; !ok {
		return
	}
	delete(s.subs, sub)
	now := s.opts.Now().UTC()
	for name := range sub.has {
		if v := s.views[name]; v != nil {
			v.subs--
			if v.subs == 0 {
				v.idle = now
			}
		}
	}
	s.wantUsageLocked()
}

// wantUsageLocked tells the source whether anyone shows pods: the Pods
// tab, a Helm release's, or a namespace, whose workload drawers list
// theirs. s.mu must be held.
func (s *Store) wantUsageLocked() {
	want := false
	for name, v := range s.views {
		if v.subs > 0 && (name == ViewPods || name == ViewHelm || strings.HasPrefix(name, NamespacePrefix)) {
			want = true
		}
	}
	s.src.WantPodUsage(want)
}

// Views returns the names of the views that exist now.
func (s *Store) Views() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.views))
	for name := range s.views {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// maxPending is how many events a slow browser may fall behind before it
// is sent a full snapshot instead.
const maxPending = 64

// Event is one server-sent event waiting for a browser.
type Event struct {
	Name string // "rows", "meta" or "refresh"
	Data []byte
}

// Sub is one subscribed browser.
type Sub struct {
	views  []string
	has    map[string]bool
	filter Filter

	mu      sync.Mutex
	pending []Event
	resync  bool
	wake    chan struct{}
}

// Resync asks for a fresh snapshot in place of the pending events: what
// the browser may see has changed.
func (sub *Sub) Resync() {
	sub.mu.Lock()
	sub.pending, sub.resync = nil, true
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// Views returns the views the browser subscribed to.
func (sub *Sub) Views() []string { return sub.views }

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

// decorateAlert marks an alert acknowledged while it is still what was
// acknowledged.
func decorateAlert(s *Store, key string, row any) any {
	r, ok := row.(views.AlertRow)
	if !ok {
		return row
	}
	m := s.acks.Load()
	if m == nil {
		return row
	}
	if a, ok := (*m)[key]; ok && a.Fingerprint == views.AlertFingerprint(r) {
		r.Ack = &views.AlertAck{By: a.By, At: a.At}
	}
	return r
}

// buildFleets works out the cluster's fleets from its nodes as they are
// now (P4-01).
// buildStorage builds the storage view in one pass: a claim's row reads
// every pod that mounts it, which one pass over the pods finds for all of
// them (ST-25).
func buildStorage(s *Store, now time.Time) map[string]any {
	rows := views.BuildStorage(s.src, now)
	out := make(map[string]any, len(rows))
	for k, r := range rows {
		out[k] = r
	}
	return out
}

func buildFleets(s *Store, now time.Time) map[string]any {
	var defs []fleets.Fleet
	if p := s.fleets.Load(); p != nil {
		defs = *p
	}
	var ns []fleets.Node
	for _, name := range s.src.Keys("nodes") {
		node := s.src.Node(name)
		if node == nil {
			continue
		}
		r := nodes.BuildRow(node, s.src.PodsOn(name), nil, nil, now)
		n := fleets.Node{
			Name: name, Labels: node.Labels, Taints: node.Spec.Taints,
			CPU: r.CPU.Allocatable, Memory: r.Memory.Allocatable, CPUReq: r.CPU.Requested, MemoryReq: r.Memory.Requested,
			Pods: r.Pods.Running, Cordoned: node.Spec.Unschedulable,
		}
		for _, p := range r.Problems {
			switch {
			case p.Pods:
			case p.Severity == nodes.SeverityError:
				n.Errors++
			default:
				n.Warnings++
			}
		}
		ns = append(ns, n)
	}
	out := map[string]any{}
	for _, r := range fleets.Build(defs, ns) {
		out[r.Key] = r
	}
	return out
}
