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
}

func newFake(names ...string) *fakeSource {
	f := &fakeSource{
		info:  kube.Info{Name: "test", State: kube.StateConnected, Synced: true, Warnings: []string{}},
		nodes: map[string]*corev1.Node{},
		pods:  map[string][]*corev1.Pod{},
		usage: map[string]nodes.Usage{},
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

type snapshot struct {
	Version     uint64            `json:"version"`
	Cluster     *kube.Info        `json:"cluster"`
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

func decodeDelta(t *testing.T, b []byte) Delta {
	t.Helper()
	var d Delta
	if err := json.Unmarshal(b, &d); err != nil {
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
	if d, resync := sub.Take(); len(d) != 0 || resync {
		t.Fatalf("unexpected delta %s", d)
	}

	// A cordoned node is sent as an upsert of its row only.
	src.mu.Lock()
	src.nodes["a"].Spec.Unschedulable = true
	src.mu.Unlock()
	s.Notify(kube.Change{Nodes: []string{"a"}})
	s.Process()
	deltas, _ := sub.Take()
	if len(deltas) != 1 {
		t.Fatalf("got %d deltas", len(deltas))
	}
	d := decodeDelta(t, deltas[0])
	if d.Version != 2 || len(d.Upserts) != 1 || rowName(t, d.Upserts[0]) != "a" || d.Cluster != nil {
		t.Fatalf("delta %+v", d)
	}

	// A deleted node is removed.
	src.mu.Lock()
	delete(src.nodes, "b")
	src.mu.Unlock()
	s.Notify(kube.Change{Nodes: []string{"b"}})
	s.Process()
	deltas, _ = sub.Take()
	d = decodeDelta(t, deltas[0])
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
	deltas, _ = sub.Take()
	d = decodeDelta(t, deltas[0])
	if d.Cluster == nil || d.Cluster.State != kube.StateDisconnected || len(d.Upserts) != 0 {
		t.Fatalf("delta %+v", d)
	}

	// Unscheduled pods change the count.
	src.mu.Lock()
	src.pods[""] = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}}}
	src.mu.Unlock()
	s.Notify(kube.Change{Info: true})
	s.Process()
	deltas, _ = sub.Take()
	d = decodeDelta(t, deltas[0])
	if d.Unscheduled == nil || *d.Unscheduled != 1 {
		t.Fatalf("delta %+v", d)
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
	deltas, resync := sub.Take()
	if !resync || deltas != nil {
		t.Fatalf("resync %v with %d deltas", resync, len(deltas))
	}
	if deltas, resync = sub.Take(); resync || len(deltas) != 0 {
		t.Fatal("resync should clear after Take")
	}
}

func TestRunDebouncesChanges(t *testing.T) {
	src := newFake("a")
	s := New(src, Options{Debounce: 20 * time.Millisecond})
	sub, _ := s.Subscribe()
	defer s.Unsubscribe(sub)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-sub.Wake():
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not publish the first rows")
	}
	deltas, _ := sub.Take()
	if len(deltas) != 1 {
		t.Fatalf("got %d deltas", len(deltas))
	}
	if v, rows, subs, _ := s.Stats(); v != 1 || rows != 1 || subs != 1 {
		t.Fatalf("stats %d %d %d", v, rows, subs)
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
	want := []uint64{0, 0, 1, 1, 1, 1, 1, 1}
	for i := range want {
		if d.Counts[i] != want[i] {
			t.Fatalf("counts %v, want %v", d.Counts, want)
		}
	}
	if d.Sum < 0.29 || d.Sum > 0.31 {
		t.Fatalf("sum %v", d.Sum)
	}
}
