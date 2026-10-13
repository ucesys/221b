package live

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
)

type snapshot struct {
	At          time.Time       `json:"at"`
	RefreshedAt *time.Time      `json:"refreshedAt"`
	Cluster     *kube.Info      `json:"cluster"`
	Settings    json.RawMessage `json:"settings"`
	Unscheduled int             `json:"unscheduled"`
	Counts      map[string]int  `json:"counts"`
	Views       map[string]struct {
		Version uint64                     `json:"version"`
		Rows    []json.RawMessage          `json:"rows"`
		Usage   map[string]json.RawMessage `json:"usage"`
	} `json:"views"`
}

func decodeSnapshot(t *testing.T, b []byte) snapshot {
	t.Helper()
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, b)
	}
	return s
}

func rowName(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var r struct{ Name string }
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r.Name
}

// taken decodes the events waiting for a subscriber.
type taken struct {
	rows    []Rows
	meta    []Meta
	refresh []Refresh
}

func take(t *testing.T, sub *Sub) taken {
	t.Helper()
	events, resync := sub.Take()
	if resync {
		t.Fatal("unexpected resync")
	}
	var out taken
	for _, e := range events {
		switch e.Name {
		case "rows":
			var r Rows
			if err := json.Unmarshal(e.Data, &r); err != nil {
				t.Fatalf("rows event is not JSON: %v", err)
			}
			out.rows = append(out.rows, r)
		case "meta":
			var m Meta
			if err := json.Unmarshal(e.Data, &m); err != nil {
				t.Fatalf("meta event is not JSON: %v", err)
			}
			out.meta = append(out.meta, m)
		case "refresh":
			var r Refresh
			if err := json.Unmarshal(e.Data, &r); err != nil {
				t.Fatalf("refresh event is not JSON: %v", err)
			}
			out.refresh = append(out.refresh, r)
		default:
			t.Fatalf("unknown event %q", e.Name)
		}
	}
	return out
}

func listed() kube.Change {
	ch := kube.Change{Info: true, Totals: true}
	for _, v := range []string{ViewNodes, ViewPods, ViewNamespaces, ViewServices, ViewHelm} {
		ch.Mark(v, kube.AllRows)
	}
	return ch
}

func pod(ns, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app", Image: "app:1"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
}

func TestSnapshotAndRows(t *testing.T) {
	src := fake.New("b", "a")
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()

	snap := decodeSnapshot(t, s.Snapshot([]string{ViewNodes}))
	v := snap.Views[ViewNodes]
	if v.Version != 1 || len(v.Rows) != 2 || snap.Cluster == nil || snap.Cluster.Name != "test" {
		t.Fatalf("snapshot %+v", snap)
	}
	if snap.Counts[ViewNodes] != 2 || snap.Counts[ViewHelm] != 0 {
		t.Fatalf("counts %v", snap.Counts)
	}
	if rowName(t, v.Rows[0]) != "a" || rowName(t, v.Rows[1]) != "b" {
		t.Fatal("rows are not sorted by name")
	}

	sub, start, err := s.Subscribe([]string{ViewNodes})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unsubscribe(sub)
	if decodeSnapshot(t, start).Views[ViewNodes].Version != 1 {
		t.Fatal("subscription should start from the current snapshot")
	}

	// Recomputing an unchanged node publishes nothing.
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {"a"}}})
	s.Process()
	if got := take(t, sub); len(got.rows) != 0 || len(got.meta) != 0 {
		t.Fatalf("unexpected events %+v", got)
	}

	// A cordoned node is sent as an upsert of its row only.
	src.Update("nodes", "", "a", func(o runtime.Object) { o.(*corev1.Node).Spec.Unschedulable = true })
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {"a"}}})
	s.Process()
	got := take(t, sub)
	if len(got.rows) != 1 || len(got.meta) != 0 {
		t.Fatalf("got %+v", got)
	}
	r := got.rows[0]
	if r.View != ViewNodes || r.Version != 2 || len(r.Upserts) != 1 || rowName(t, r.Upserts[0]) != "a" || len(r.Removes) != 0 {
		t.Fatalf("rows %+v", r)
	}

	// A deleted node is removed, and the counts follow.
	src.Delete("nodes", "", "b")
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {"b"}}})
	s.Process()
	got = take(t, sub)
	r = got.rows[0]
	if r.Version != 3 || len(r.Removes) != 1 || r.Removes[0] != "b" {
		t.Fatalf("rows %+v", r)
	}
	if len(got.meta) != 1 || got.meta[0].Counts[ViewNodes] != 1 {
		t.Fatalf("meta %+v", got.meta)
	}
	if _, ok := s.Row(ViewNodes, "b"); ok {
		t.Fatal("removed row still served")
	}

	// A connection change carries the cluster details, apart from rows.
	src.SetInfo(func(i *kube.Info) { i.State = kube.StateDisconnected })
	s.Notify(kube.Change{Info: true})
	s.Process()
	got = take(t, sub)
	if len(got.rows) != 0 || len(got.meta) != 1 || got.meta[0].Cluster == nil || got.meta[0].Cluster.State != kube.StateDisconnected {
		t.Fatalf("got %+v", got)
	}

	// Unscheduled pods change the count.
	src.Put(pod("default", "p", ""))
	s.Notify(kube.Change{Totals: true})
	s.Process()
	got = take(t, sub)
	if len(got.meta) != 1 || got.meta[0].Unscheduled == nil || *got.meta[0].Unscheduled != 1 {
		t.Fatalf("got %+v", got)
	}

	// A boot time read later reaches the row.
	src.SetBoot("a", time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {"a"}}})
	s.Process()
	r = take(t, sub).rows[0]
	if len(r.Upserts) != 1 || !json.Valid(r.Upserts[0]) {
		t.Fatalf("rows %+v", r)
	}
	var row nodes.Row
	_ = json.Unmarshal(r.Upserts[0], &row)
	if row.BootedAt == nil || !row.BootedAt.Equal(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("row %+v lacks the boot time", row)
	}
}

func TestSubscribersGetOnlyTheirViews(t *testing.T) {
	src := fake.New("a")
	src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
	src.Put(pod("default", "p1", "a"))
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()

	nodesSub, _, _ := s.Subscribe([]string{ViewNodes})
	defer s.Unsubscribe(nodesSub)
	both, start, err := s.Subscribe([]string{ViewNodes, ViewPods})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unsubscribe(both)
	snap := decodeSnapshot(t, start)
	if len(snap.Views) != 2 || len(snap.Views[ViewPods].Rows) != 1 || len(snap.Views[ViewNodes].Rows) != 1 {
		t.Fatalf("snapshot %+v", snap)
	}
	if !src.WantsPodUsage() {
		t.Fatal("pod usage should be wanted while someone shows pods")
	}

	src.Put(pod("default", "p2", "a"))
	s.Notify(kube.Change{Views: map[string][]string{ViewPods: {"default/p2"}, ViewNodes: {"a"}}})
	s.Process()
	if got := take(t, nodesSub); len(got.rows) != 1 || got.rows[0].View != ViewNodes {
		t.Fatalf("nodes subscriber got %+v", got)
	}
	got := take(t, both)
	if len(got.rows) != 2 || got.rows[0].View != ViewNodes || got.rows[1].View != ViewPods || rowName(t, got.rows[1].Upserts[0]) != "p2" {
		t.Fatalf("both subscriber got %+v", got)
	}

	s.Unsubscribe(both)
	if src.WantsPodUsage() {
		t.Fatal("pod usage should not be wanted once nobody shows pods")
	}
}

func TestPodUsageIsSentApartFromRows(t *testing.T) {
	src := fake.New("a")
	src.Put(pod("default", "p1", "a"))
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()
	sub, start, _ := s.Subscribe([]string{ViewPods})
	defer s.Unsubscribe(sub)
	if snap := decodeSnapshot(t, start); len(snap.Views[ViewPods].Rows) != 1 || len(snap.Views[ViewPods].Usage) != 0 {
		t.Fatalf("snapshot %+v", snap)
	}

	// A usage reading changes nothing in the rows.
	src.SetPodUsage(map[string]nodes.Usage{"default/p1": {CPUMilli: 20, MemoryBytes: 1 << 20}, "default/gone": {CPUMilli: 1}})
	s.Notify(kube.Change{Views: map[string][]string{ViewPods: {kube.UsageOnly}}})
	s.Process()
	got := take(t, sub)
	if len(got.rows) != 1 {
		t.Fatalf("got %+v", got)
	}
	r := got.rows[0]
	if len(r.Upserts) != 0 || len(r.Removes) != 0 || len(r.Usage) != 1 || string(r.Usage["default/p1"]) != `{"cpuMilli":20,"memoryBytes":1048576}` {
		t.Fatalf("rows %+v", r)
	}
	if snap := decodeSnapshot(t, s.Snapshot([]string{ViewPods})); len(snap.Views[ViewPods].Usage) != 1 {
		t.Fatalf("snapshot %+v", snap)
	}

	// The same reading again publishes nothing; a reading gone is removed.
	s.Notify(kube.Change{Views: map[string][]string{ViewPods: {kube.UsageOnly}}})
	s.Process()
	if got := take(t, sub); len(got.rows) != 0 {
		t.Fatalf("got %+v", got)
	}
	src.SetPodUsage(nil)
	s.Notify(kube.Change{Views: map[string][]string{ViewPods: {kube.UsageOnly}}})
	s.Process()
	r = take(t, sub).rows[0]
	if len(r.UsageRemoves) != 1 || r.UsageRemoves[0] != "default/p1" {
		t.Fatalf("rows %+v", r)
	}
}

func TestNamespaceViewsExistWhileShown(t *testing.T) {
	src := fake.New("a")
	src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
	src.Put(pod("shop", "web", "a"))
	src.Put(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "shop"}, Data: map[string]string{"k": "v"}})
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := New(src, Options{Idle: time.Minute, Now: func() time.Time { return now }})
	s.Notify(listed())
	s.Process()
	if views := s.Views(); strings.Join(views, ",") != "helm,namespaces,nodes,services" {
		t.Fatalf("views %v", views)
	}

	if _, _, err := s.Subscribe([]string{"ns:"}); err == nil {
		t.Fatal("an empty namespace should be refused")
	}
	if _, _, err := s.Subscribe([]string{"bogus"}); err == nil {
		t.Fatal("an unknown view should be refused")
	}
	sub, start, err := s.Subscribe([]string{ViewNamespaces, "ns:shop"})
	if err != nil {
		t.Fatal(err)
	}
	snap := decodeSnapshot(t, start)
	rows := snap.Views["ns:shop"].Rows
	if len(rows) != 2 || rowName(t, rows[0]) != "cfg" || rowName(t, rows[1]) != "web" {
		t.Fatalf("namespace rows %s", rows)
	}
	if len(snap.Views[ViewNamespaces].Rows) != 1 {
		t.Fatalf("namespaces %+v", snap.Views[ViewNamespaces])
	}

	// A change in the namespace rebuilds its objects.
	src.Put(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1"}})
	s.Notify(kube.Change{Views: map[string][]string{"ns:shop": {kube.AllRows}, ViewNamespaces: {"shop"}, ViewServices: {"shop/web"}}})
	s.Process()
	got := take(t, sub)
	if len(got.rows) != 2 {
		t.Fatalf("got %+v", got)
	}
	var keys []string
	for _, r := range got.rows {
		keys = append(keys, r.View)
	}
	if strings.Join(keys, ",") != "namespaces,ns:shop" {
		t.Fatalf("views changed: %v", keys)
	}

	// Once nobody shows it, the view is dropped after a while.
	s.Unsubscribe(sub)
	s.Notify(kube.Change{Views: map[string][]string{"ns:shop": {kube.AllRows}}})
	s.Process()
	if views := s.Views(); !strings.Contains(strings.Join(views, ","), "ns:shop") {
		t.Fatalf("the view should be kept a while: %v", views)
	}
	now = now.Add(2 * time.Minute)
	s.Process()
	if views := s.Views(); strings.Contains(strings.Join(views, ","), "ns:shop") {
		t.Fatalf("the idle view should be dropped: %v", views)
	}
}

func TestHelmViewIsEmptyWithSecretsOff(t *testing.T) {
	src := fake.New("a")
	src.SetSecrets(false)
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()
	snap := decodeSnapshot(t, s.Snapshot([]string{ViewHelm}))
	if len(snap.Views[ViewHelm].Rows) != 0 || snap.Cluster.Secrets != "off" {
		t.Fatalf("snapshot %+v", snap)
	}
}

func TestFullRecomputeRemovesVanishedNodes(t *testing.T) {
	src := fake.New("a", "b")
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()
	src.Delete("nodes", "", "a")
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {kube.AllRows}}})
	s.Process()
	snap := decodeSnapshot(t, s.Snapshot([]string{ViewNodes}))
	if rows := snap.Views[ViewNodes].Rows; len(rows) != 1 || rowName(t, rows[0]) != "b" {
		t.Fatalf("rows %d", len(rows))
	}
}

func TestNoRowsBeforeSync(t *testing.T) {
	src := fake.New("a")
	src.SetInfo(func(i *kube.Info) { i.Synced = false })
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()
	sub, start, _ := s.Subscribe([]string{ViewNodes, ViewPods})
	defer s.Unsubscribe(sub)
	snap := decodeSnapshot(t, start)
	if len(snap.Views[ViewNodes].Rows) != 0 || len(snap.Views[ViewPods].Rows) != 0 || snap.Cluster == nil || snap.Cluster.Synced {
		t.Fatalf("snapshot %+v", snap)
	}
	// Once listed, the rows arrive, for the lazy view too.
	src.SetInfo(func(i *kube.Info) { i.Synced = true })
	src.Put(pod("default", "p", "a"))
	s.Notify(listed())
	s.Process()
	got := take(t, sub)
	if len(got.rows) != 2 || len(got.rows[0].Upserts) != 1 || len(got.rows[1].Upserts) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestSlowSubscriberResyncs(t *testing.T) {
	src := fake.New("a")
	s := New(src, Options{})
	sub, _, _ := s.Subscribe([]string{ViewNodes})
	defer s.Unsubscribe(sub)
	for i := 0; i < maxPending+5; i++ {
		src.SetUsage("a", nodes.Usage{CPUMilli: int64(i + 1)})
		s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {kube.AllRows}}})
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
	src := fake.New("a")
	s := New(src, Options{Refresh: 300 * time.Millisecond, Prompt: 10 * time.Millisecond})
	sub, _, _ := s.Subscribe([]string{ViewNodes})
	defer s.Unsubscribe(sub)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// The first list goes out promptly.
	s.Notify(listed())
	waitWake(t, sub)
	if got := take(t, sub); len(got.rows) != 1 || len(got.rows[0].Upserts) != 1 {
		t.Fatalf("first rows: %+v", got)
	}
	views, subs, _ := s.Stats()
	if subs != 1 || len(views) != 4 {
		t.Fatalf("stats %+v %d", views, subs)
	}
	for _, v := range views {
		if v.Name == ViewNodes && (v.Version != 1 || v.Rows != 1 || v.Subscribers != 1) {
			t.Fatalf("nodes stats %+v", v)
		}
	}

	// A row change waits for the tick, which also polls the source.
	src.Update("nodes", "", "a", func(o runtime.Object) { o.(*corev1.Node).Spec.Unschedulable = true })
	changed := time.Now()
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {"a"}}})
	var got taken
	for len(got.rows) == 0 {
		waitWake(t, sub)
		got = take(t, sub)
	}
	if since := time.Since(changed); since < 200*time.Millisecond {
		t.Fatalf("the row went out %v after the change, before the tick", since)
	}
	if len(got.rows[0].Upserts) != 1 {
		t.Fatalf("rows %+v", got.rows[0])
	}
	if src.Polls() == 0 {
		t.Fatal("the tick did not poll the source")
	}

	// Connection news goes out without waiting.
	src.SetInfo(func(i *kube.Info) { i.State = kube.StateDisconnected })
	changed = time.Now()
	s.Notify(kube.Change{Info: true})
	got = taken{}
	for len(got.meta) == 0 {
		waitWake(t, sub)
		got = take(t, sub)
	}
	if since := time.Since(changed); since > 150*time.Millisecond {
		t.Fatalf("connection news took %v", since)
	}
	if got.meta[0].Cluster == nil || got.meta[0].Cluster.State != kube.StateDisconnected {
		t.Fatalf("meta %+v", got.meta[0])
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
	src := fake.New("a")
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := New(src, Options{Now: func() time.Time { return now }})
	s.SetSettings(10*time.Second, []byte(`{"refreshSeconds":10,"storage":"memory"}`))
	if s.RefreshInterval() != 10*time.Second {
		t.Fatalf("interval %v", s.RefreshInterval())
	}
	s.Notify(listed())
	s.Process()
	sub, start, _ := s.Subscribe([]string{ViewNodes, ViewPods})
	defer s.Unsubscribe(sub)
	snap := decodeSnapshot(t, start)
	if string(snap.Settings) != `{"refreshSeconds":10,"storage":"memory"}` || snap.RefreshedAt != nil || snap.Views[ViewNodes].Version != 1 {
		t.Fatalf("snapshot %+v", snap)
	}

	// A refresh with nothing to publish still tells browsers it happened,
	// with the versions of the views they show.
	s.Refresh(context.Background())
	got := take(t, sub)
	if len(got.refresh) != 1 || len(got.rows) != 0 || len(got.meta) != 0 {
		t.Fatalf("got %+v", got)
	}
	r := got.refresh[0]
	if !r.At.Equal(now) || len(r.Versions) != 2 || r.Versions[ViewNodes] != 1 || r.Versions[ViewPods] != 0 {
		t.Fatalf("refresh %+v", r)
	}
	if snap := decodeSnapshot(t, s.Snapshot([]string{ViewNodes})); snap.RefreshedAt == nil || !snap.RefreshedAt.Equal(now) {
		t.Fatalf("snapshot %+v", snap)
	}

	// New settings reach every browser.
	s.SetSettings(2*time.Second, []byte(`{"refreshSeconds":2,"storage":"memory"}`))
	got = take(t, sub)
	if len(got.meta) != 1 || string(got.meta[0].Settings) != `{"refreshSeconds":2,"storage":"memory"}` {
		t.Fatalf("got %+v", got)
	}
	// The same settings again publish nothing.
	s.SetSettings(2*time.Second, []byte(`{"refreshSeconds":2,"storage":"memory"}`))
	if got := take(t, sub); len(got.meta) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestRefreshPollsBeforeBuildingRows(t *testing.T) {
	src := fake.New("a")
	s := New(src, Options{})
	s.Notify(listed())
	s.Process()
	sub, _, _ := s.Subscribe([]string{ViewNodes})
	defer s.Unsubscribe(sub)
	// The poll brings a new usage reading, as metrics-server does, and the
	// same refresh publishes rows built from it.
	src.OnPoll = func() {
		src.SetUsage("a", nodes.Usage{CPUMilli: 250})
		s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {kube.AllRows}}, Info: true})
	}
	s.Refresh(context.Background())
	got := take(t, sub)
	if len(got.rows) != 1 || len(got.rows[0].Upserts) != 1 {
		t.Fatalf("got %+v", got)
	}
	var row nodes.Row
	_ = json.Unmarshal(got.rows[0].Upserts[0], &row)
	if row.CPU.Used == nil || *row.CPU.Used != 250 {
		t.Fatalf("row %+v", row.CPU)
	}
}

func TestDelaysMeasureEventToPublication(t *testing.T) {
	src := fake.New("a")
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := New(src, Options{Now: func() time.Time { return now }})
	s.Notify(listed())
	now = now.Add(300 * time.Millisecond)
	s.Process()

	// A change that publishes nothing is not counted.
	s.Notify(kube.Change{Views: map[string][]string{ViewNodes: {"a"}}})
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
