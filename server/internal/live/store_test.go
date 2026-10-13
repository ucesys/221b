package live

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
)

type fakeSource struct {
	mu    sync.Mutex
	info  kube.Info
	nodes map[string]*corev1.Node
	pods  map[string][]*corev1.Pod
	usage map[string]nodes.Usage
	boots map[string]time.Time
	polls int
	// onPoll runs inside Poll, as metrics-server reads do.
	onPoll func()
}

func newFake(names ...string) *fakeSource {
	f := &fakeSource{
		info:  kube.Info{Name: "test", State: kube.StateConnected, Synced: true, Warnings: []string{}},
		nodes: map[string]*corev1.Node{},
		pods:  map[string][]*corev1.Pod{},
		usage: map[string]nodes.Usage{},
		boots: map[string]time.Time{},
	}
	for _, n := range names {
		f.nodes[n] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n}}
	}
	return f
}

func (f *fakeSource) Info() kube.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

func (f *fakeSource) NodeNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.nodes {
		out = append(out, n)
	}
	return out
}

func (f *fakeSource) Node(name string) *corev1.Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[name]
}

func (f *fakeSource) PodsOn(node string) []*corev1.Pod {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pods[node]
}

func (f *fakeSource) Unscheduled() int { return len(f.PodsOn("")) }

func (f *fakeSource) Usage(node string) (nodes.Usage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.usage[node]
	return u, ok
}

func (f *fakeSource) BootedAt(node string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boots[node]
	return b, ok
}

func (f *fakeSource) Poll(context.Context) {
	f.mu.Lock()
	f.polls++
	onPoll := f.onPoll
	f.mu.Unlock()
	if onPoll != nil {
		onPoll()
	}
}

type snapshot struct {
	Version     uint64            `json:"version"`
	RefreshedAt *time.Time        `json:"refreshedAt"`
	Cluster     *kube.Info        `json:"cluster"`
	Settings    json.RawMessage   `json:"settings"`
	Unscheduled int               `json:"unscheduled"`
	Rows        []json.RawMessage `json:"rows"`
}

func decodeSnapshot(t *testing.T, b []byte) snapshot {
	t.Helper()
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, b)
	}
	return s
}

func decodeDelta(t *testing.T, e Event) Delta {
	t.Helper()
	if e.Name != "delta" {
		t.Fatalf("event %s is not a delta", e.Name)
	}
	var d Delta
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("delta is not JSON: %v", err)
	}
	return d
}

func rowName(t *testing.T, raw json.RawMessage) string {
	var r struct{ Name string }
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r.Name
}

// deltas returns the deltas waiting for a subscriber, dropping refresh events.
func deltas(t *testing.T, sub *Sub) []Delta {
	t.Helper()
	events, resync := sub.Take()
	if resync {
		t.Fatal("unexpected resync")
	}
	var out []Delta
	for _, e := range events {
		if e.Name == "delta" {
			out = append(out, decodeDelta(t, e))
		}
	}
	return out
}

func TestSnapshotAndDeltas(t *testing.T) {
	src := newFake("b", "a")
	s := New(src, Options{})
	s.Notify(kube.Change{All: true, Info: true})
	s.Process()

	snap := decodeSnapshot(t, s.Snapshot())
	if snap.Version != 1 || len(snap.Rows) != 2 || snap.Cluster == nil || snap.Cluster.Name != "test" {
		t.Fatalf("snapshot %+v", snap)
	}
	if rowName(t, snap.Rows[0]) != "a" || rowName(t, snap.Rows[1]) != "b" {
		t.Fatal("rows are not sorted by name")
	}

	sub, start := s.Subscribe()
	defer s.Unsubscribe(sub)
	if decodeSnapshot(t, start).Version != 1 {
		t.Fatal("subscription should start from the current snapshot")
	}

	// Recomputing an unchanged node publishes nothing.
	s.Notify(kube.Change{Nodes: []string{"a"}})
	s.Process()
	if d := deltas(t, sub); len(d) != 0 {
		t.Fatalf("unexpected deltas %+v", d)
	}

	// A cordoned node is sent as an upsert of its row only.
	src.mu.Lock()
	src.nodes["a"].Spec.Unschedulable = true
	src.mu.Unlock()
	s.Notify(kube.Change{Nodes: []string{"a"}})
	s.Process()
	ds := deltas(t, sub)
	if len(ds) != 1 {
		t.Fatalf("got %d deltas", len(ds))
	}
	d := ds[0]
	if d.Version != 2 || len(d.Upserts) != 1 || rowName(t, d.Upserts[0]) != "a" || d.Cluster != nil {
		t.Fatalf("delta %+v", d)
	}

	// A deleted node is removed.
	src.mu.Lock()
	delete(src.nodes, "b")
	src.mu.Unlock()
	s.Notify(kube.Change{Nodes: []string{"b"}})
	s.Process()
	d = deltas(t, sub)[0]
	if d.Version != 3 || len(d.Removes) != 1 || d.Removes[0] != "b" {
		t.Fatalf("delta %+v", d)
	}
	if _, ok := s.Row("b"); ok {
		t.Fatal("removed row still served")
	}

	// A connection change carries the cluster details.
	src.mu.Lock()
	src.info.State = kube.StateDisconnected
	src.mu.Unlock()
	s.Notify(kube.Change{Info: true})
	s.Process()
	d = deltas(t, sub)[0]
	if d.Cluster == nil || d.Cluster.State != kube.StateDisconnected || len(d.Upserts) != 0 {
		t.Fatalf("delta %+v", d)
	}

	// Unscheduled pods change the count.
	src.mu.Lock()
	src.pods[""] = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}}}
	src.mu.Unlock()
	s.Notify(kube.Change{Totals: true})
	s.Process()
	d = deltas(t, sub)[0]
	if d.Unscheduled == nil || *d.Unscheduled != 1 {
		t.Fatalf("delta %+v", d)
	}

	// A boot time read later reaches the row.
	src.mu.Lock()
	src.boots["a"] = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	src.mu.Unlock()
	s.Notify(kube.Change{Nodes: []string{"a"}})
	s.Process()
	d = deltas(t, sub)[0]
	if len(d.Upserts) != 1 || !json.Valid(d.Upserts[0]) {
		t.Fatalf("delta %+v", d)
	}
	var row nodes.Row
	_ = json.Unmarshal(d.Upserts[0], &row)
	if row.BootedAt == nil || !row.BootedAt.Equal(src.boots["a"]) {
		t.Fatalf("row %+v lacks the boot time", row)
	}
}

func TestFullRecomputeRemovesVanishedNodes(t *testing.T) {
	src := newFake("a", "b")
	s := New(src, Options{})
	s.Notify(kube.Change{All: true})
	s.Process()
	src.mu.Lock()
	delete(src.nodes, "a")
	src.mu.Unlock()
	s.Notify(kube.Change{All: true})
	s.Process()
	snap := decodeSnapshot(t, s.Snapshot())
	if len(snap.Rows) != 1 || rowName(t, snap.Rows[0]) != "b" {
		t.Fatalf("rows %d", len(snap.Rows))
	}
}

func TestNoRowsBeforeSync(t *testing.T) {
	src := newFake("a")
	src.info.Synced = false
	s := New(src, Options{})
	s.Notify(kube.Change{All: true, Info: true})
	s.Process()
	snap := decodeSnapshot(t, s.Snapshot())
	if len(snap.Rows) != 0 || snap.Cluster == nil || snap.Cluster.Synced {
		t.Fatalf("snapshot %+v", snap)
	}
}

func TestSlowSubscriberResyncs(t *testing.T) {
	src := newFake("a")
	s := New(src, Options{})
	sub, _ := s.Subscribe()
	defer s.Unsubscribe(sub)
	for i := 0; i < maxPending+5; i++ {
		src.mu.Lock()
		src.usage["a"] = nodes.Usage{CPUMilli: int64(i + 1)}
		src.mu.Unlock()
		s.Notify(kube.Change{All: true})
		s.Process()
	}
	events, resync := sub.Take()
	if !resync || events != nil {
		t.Fatalf("resync %v with %d events", resync, len(events))
	}
	if events, resync = sub.Take(); resync || len(events) != 0 {
		t.Fatal("resync should clear after Take")
	}
}

// Rows wait for the refresh tick; the first rows and connection news do
// not.
func TestRowsWaitForTheRefresh(t *testing.T) {
	src := newFake("a")
	s := New(src, Options{Refresh: 300 * time.Millisecond, Prompt: 10 * time.Millisecond})
	sub, _ := s.Subscribe()
	defer s.Unsubscribe(sub)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// The first list goes out promptly.
	s.Notify(kube.Change{All: true, Info: true})
	waitWake(t, sub)
	if ds := deltas(t, sub); len(ds) != 1 || len(ds[0].Upserts) != 1 {
		t.Fatalf("first rows: %+v", ds)
	}
	if v, rows, subs, _ := s.Stats(); v != 1 || rows != 1 || subs != 1 {
		t.Fatalf("stats %d %d %d", v, rows, subs)
	}

	// A row change waits for the tick, which also polls the source.
	src.mu.Lock()
	src.nodes["a"].Spec.Unschedulable = true
	src.mu.Unlock()
	changed := time.Now()
	s.Notify(kube.Change{Nodes: []string{"a"}})
	var got []Delta
	for len(got) == 0 {
		waitWake(t, sub)
		got = deltas(t, sub)
	}
	if since := time.Since(changed); since < 200*time.Millisecond {
		t.Fatalf("the row went out %v after the change, before the tick", since)
	}
	if len(got[0].Upserts) != 1 {
		t.Fatalf("delta %+v", got[0])
	}
	src.mu.Lock()
	polls := src.polls
	src.mu.Unlock()
	if polls == 0 {
		t.Fatal("the tick did not poll the source")
	}

	// Connection news goes out without waiting.
	src.mu.Lock()
	src.info.State = kube.StateDisconnected
	src.mu.Unlock()
	changed = time.Now()
	s.Notify(kube.Change{Info: true})
	got = nil
	for len(got) == 0 {
		waitWake(t, sub)
		got = deltas(t, sub)
	}
	if since := time.Since(changed); since > 150*time.Millisecond {
		t.Fatalf("connection news took %v", since)
	}
	if got[0].Cluster == nil || got[0].Cluster.State != kube.StateDisconnected {
		t.Fatalf("delta %+v", got[0])
	}
}

func waitWake(t *testing.T, sub *Sub) {
	t.Helper()
	select {
	case <-sub.Wake():
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was published")
	}
}

func TestRefreshEventsAndSettings(t *testing.T) {
	src := newFake("a")
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := New(src, Options{Now: func() time.Time { return now }})
	s.SetSettings(10*time.Second, []byte(`{"refreshSeconds":10,"storage":"memory"}`))
	if s.RefreshInterval() != 10*time.Second {
		t.Fatalf("interval %v", s.RefreshInterval())
	}
	s.Notify(kube.Change{All: true, Info: true})
	s.Process()
	sub, start := s.Subscribe()
	defer s.Unsubscribe(sub)
	snap := decodeSnapshot(t, start)
	if string(snap.Settings) != `{"refreshSeconds":10,"storage":"memory"}` || snap.RefreshedAt != nil || snap.Version != 2 {
		t.Fatalf("snapshot %+v", snap)
	}

	// A refresh with nothing to publish still tells browsers it happened.
	s.Refresh(context.Background())
	events, _ := sub.Take()
	if len(events) != 1 || events[0].Name != "refresh" {
		t.Fatalf("events %+v", events)
	}
	var r Refresh
	if err := json.Unmarshal(events[0].Data, &r); err != nil || r.Version != 2 || !r.At.Equal(now) {
		t.Fatalf("refresh %+v %v", r, err)
	}
	if snap := decodeSnapshot(t, s.Snapshot()); snap.RefreshedAt == nil || !snap.RefreshedAt.Equal(now) {
		t.Fatalf("snapshot %+v", snap)
	}

	// New settings reach every browser as a delta.
	s.SetSettings(2*time.Second, []byte(`{"refreshSeconds":2,"storage":"memory"}`))
	ds := deltas(t, sub)
	if len(ds) != 1 || string(ds[0].Settings) != `{"refreshSeconds":2,"storage":"memory"}` || ds[0].Version != 3 {
		t.Fatalf("deltas %+v", ds)
	}
	// The same settings again publish nothing.
	s.SetSettings(2*time.Second, []byte(`{"refreshSeconds":2,"storage":"memory"}`))
	if ds := deltas(t, sub); len(ds) != 0 {
		t.Fatalf("deltas %+v", ds)
	}
}

func TestRefreshPollsBeforeBuildingRows(t *testing.T) {
	src := newFake("a")
	s := New(src, Options{})
	s.Notify(kube.Change{All: true, Info: true})
	s.Process()
	sub, _ := s.Subscribe()
	defer s.Unsubscribe(sub)
	// The poll brings a new usage reading, as metrics-server does, and the
	// same refresh publishes rows built from it.
	src.onPoll = func() {
		src.mu.Lock()
		src.usage["a"] = nodes.Usage{CPUMilli: 250}
		src.mu.Unlock()
		s.Notify(kube.Change{All: true, Info: true})
	}
	s.Refresh(context.Background())
	ds := deltas(t, sub)
	if len(ds) != 1 || len(ds[0].Upserts) != 1 {
		t.Fatalf("deltas %+v", ds)
	}
	var row nodes.Row
	_ = json.Unmarshal(ds[0].Upserts[0], &row)
	if row.CPU.Used == nil || *row.CPU.Used != 250 {
		t.Fatalf("row %+v", row.CPU)
	}
}

func TestDelaysMeasureEventToPublication(t *testing.T) {
	src := newFake("a")
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := New(src, Options{Now: func() time.Time { return now }})
	s.Notify(kube.Change{All: true, Info: true})
	now = now.Add(300 * time.Millisecond)
	s.Process()

	// A change that publishes nothing is not counted.
	s.Notify(kube.Change{Nodes: []string{"a"}})
	now = now.Add(2 * time.Second)
	s.Process()

	d := s.Delays()
	if len(d.Counts) != len(DelayBuckets)+1 {
		t.Fatalf("counts %v", d.Counts)
	}
	// 0.3 s falls in the 0.5 s bucket and every larger one.
	want := []uint64{0, 0, 1, 1, 1, 1, 1, 1, 1}
	for i := range want {
		if d.Counts[i] != want[i] {
			t.Fatalf("counts %v, want %v", d.Counts, want)
		}
	}
	if d.Sum < 0.29 || d.Sum > 0.31 {
		t.Fatalf("sum %v", d.Sum)
	}
}
