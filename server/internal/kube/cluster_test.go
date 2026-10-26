package kube_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// These tests run the real client-go informers against the simulator over
// HTTP, so list, watch, streaming lists and resumption are exercised.

func start(t *testing.T, o sim.Options) (*kube.Cluster, *httptest.Server, chan kube.Change) {
	t.Helper()
	s := sim.New(o)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cl, err := kube.New(&rest.Config{Host: srv.URL}, kube.Options{
		Name: "sim", Mode: kube.ModeURL, HealthInterval: 200 * time.Millisecond, KubeletStats: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan kube.Change, 10000)
	cl.OnChange(func(c kube.Change) {
		select {
		case changes <- c:
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go cl.Run(ctx)
	waitFor(t, "the first list", func() bool { return cl.Ready() })
	return cl, srv, changes
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// pollUntil polls the cluster, as the store does on every refresh, until
// cond holds.
func pollUntil(t *testing.T, cl *kube.Cluster, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cl.Poll(context.Background())
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func post(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %s", url, resp.Status)
	}
}

func TestListsAndWatches(t *testing.T) {
	cl, srv, _ := start(t, sim.Options{Nodes: 8, Pods: 120, Metrics: true, Problems: true})
	if got := len(cl.NodeNames()); got != 8 {
		t.Fatalf("nodes %d", got)
	}
	pods := 0
	for _, n := range cl.NodeNames() {
		pods += len(cl.PodsOn(n))
	}
	if pods+cl.Unscheduled() < 120 {
		t.Fatalf("pods on nodes %d + unscheduled %d", pods, cl.Unscheduled())
	}
	if cl.Unscheduled() < 2 {
		t.Fatalf("expected the simulated pending pods, got %d", cl.Unscheduled())
	}
	info := cl.Info()
	if info.State != kube.StateConnected || info.Version != "v1.37.1" || !info.Synced {
		t.Fatalf("info %+v", info)
	}
	pollUntil(t, cl, "metrics", func() bool { _, ok := cl.Usage("worker-001"); return ok })
	if cl.Info().Metrics != kube.MetricsAvailable || cl.Info().MetricsAt == nil {
		t.Fatalf("metrics info %+v", cl.Info())
	}

	// A change made after the list arrives through the watch.
	post(t, srv.URL+"/sim/nodes/worker-002/cordon")
	waitFor(t, "the cordon", func() bool { n := cl.Node("worker-002"); return n != nil && n.Spec.Unschedulable })

	post(t, srv.URL+"/sim/pods/kube-system/"+firstPod(t, cl, "worker-001")+"/crash")
	waitFor(t, "the crash", func() bool {
		for _, p := range cl.PodsOn("worker-001") {
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
					return true
				}
			}
		}
		return false
	})
}

func firstPod(t *testing.T, cl *kube.Cluster, node string) string {
	for _, p := range cl.PodsOn(node) {
		if p.Namespace == "kube-system" {
			return p.Name
		}
	}
	t.Fatalf("no system pod on %s", node)
	return ""
}

func TestTransformsKeepTheCacheSmall(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 4, Pods: 20})
	for _, n := range cl.NodeNames() {
		for _, p := range cl.PodsOn(n) {
			if p.ManagedFields != nil || p.Spec.Containers[0].LivenessProbe == nil || p.Spec.Containers[0].Image == "" {
				t.Fatalf("pod %s: the transform did not run as expected", p.Name)
			}
		}
	}
	// Secrets are cached as summaries: keys and sizes, never values.
	for _, o := range cl.Objects("secrets", "default") {
		sum, ok := o.(*kube.SecretSummary)
		if !ok {
			t.Fatalf("secret cached as %T", o)
		}
		if sum.Name == "planted-secret" && (len(sum.Keys) != 1 || sum.Keys[0].Key != "token" || sum.Keys[0].Bytes != len(sim.PlantedSecretValue)) {
			t.Fatalf("planted secret summary %+v", sum.Keys)
		}
	}
	// The long tail is metadata only; what the analysis tabs read is kept
	// whole, without managed fields.
	for _, o := range cl.Objects("leases", "") {
		if _, ok := o.(*metav1.PartialObjectMetadata); !ok {
			t.Fatalf("lease cached as %T", o)
		}
	}
	for _, o := range cl.Objects("roles", "") {
		r, ok := o.(*rbacv1.Role)
		if !ok || r.ManagedFields != nil || len(r.Rules) == 0 {
			t.Fatalf("role cached as %T %+v", o, o)
		}
	}
	if cl.Count("roles", "") == 0 || cl.Count("namespaces", "") == 0 || cl.Count("services", "") == 0 {
		t.Fatalf("kinds missing: %v", cl.CachedObjects())
	}
}

func TestConnectionLossAndRecovery(t *testing.T) {
	cl, srv, _ := start(t, sim.Options{Nodes: 4, Pods: 20, Metrics: true})
	post(t, srv.URL+"/sim/down?on=true")
	waitFor(t, "the disconnection", func() bool { return cl.Info().State == kube.StateDisconnected })
	if msg := cl.Info().Message; msg != "The connection closed before it answered." {
		t.Fatalf("message %q", msg)
	}
	// Last known state stays available while disconnected.
	if len(cl.NodeNames()) != 4 {
		t.Fatal("cache emptied on disconnection")
	}
	post(t, srv.URL+"/sim/down?on=false")
	waitFor(t, "the reconnection", func() bool { return cl.Info().State == kube.StateConnected })
	// Watches resume: a change after recovery arrives.
	post(t, srv.URL+"/sim/nodes/worker-001/cordon")
	waitFor(t, "a change after recovery", func() bool { n := cl.Node("worker-001"); return n != nil && n.Spec.Unschedulable })
}

// client-go's informers back off up to 30 to 60 seconds during an outage;
// the cluster makes them wait for the API server instead, so they catch up
// within seconds of its return (P1-10).
func TestCatchesUpQuicklyAfterALongOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 8 seconds")
	}
	cl, srv, _ := start(t, sim.Options{Nodes: 4, Pods: 20})
	post(t, srv.URL+"/sim/down?on=true")
	time.Sleep(8 * time.Second)
	// The informers waited rather than retrying lists and watches.
	if n := refused(t, srv.URL); n > 2 {
		t.Errorf("%d lists and watches were tried during the outage", n)
	}
	// A change made while 221B could not see it.
	post(t, srv.URL+"/sim/nodes/worker-002/cordon")
	post(t, srv.URL+"/sim/down?on=false")
	back := time.Now()
	waitFor(t, "the change made during the outage", func() bool { n := cl.Node("worker-002"); return n != nil && n.Spec.Unschedulable })
	if d := time.Since(back); d > 4*time.Second {
		t.Fatalf("caught up %v after the API server returned", d)
	} else {
		t.Logf("caught up %v after the API server returned", d)
	}
}

func refused(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Get(base + "/sim/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st struct{ Refused int }
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st.Refused
}

func TestMetricsUnavailable(t *testing.T) {
	cl, srv, _ := start(t, sim.Options{Nodes: 3, Pods: 6, Metrics: false})
	if cl.Info().Metrics != kube.MetricsUnknown {
		t.Fatalf("metrics state before the first poll: %q", cl.Info().Metrics)
	}
	pollUntil(t, cl, "metrics state", func() bool { return cl.Info().Metrics == kube.MetricsUnavailable })
	if !strings.Contains(cl.Info().MetricsMessage, "metrics-server is not installed") {
		t.Fatalf("message %q", cl.Info().MetricsMessage)
	}
	if _, ok := cl.Usage("cp-1"); ok {
		t.Fatal("usage without metrics")
	}
	post(t, srv.URL+"/sim/metrics?on=true")
	pollUntil(t, cl, "metrics to appear", func() bool { return cl.Info().Metrics == kube.MetricsAvailable })
}

// Boot times come from the kubelets through the API server's node proxy,
// once per node and boot.
func TestBootTimesFromTheKubelets(t *testing.T) {
	cl, srv, changes := start(t, sim.Options{Nodes: 4, Pods: 8, KubeletStats: true})
	// The state is set once the pass over the nodes is done, after the
	// entries themselves.
	waitFor(t, "boot times", func() bool { return cl.Info().Uptime == kube.UptimeAvailable })
	if _, ok := cl.BootedAt("worker-001"); !ok {
		t.Fatalf("no boot time for worker-001: %s", cl.Info().UptimeMessage)
	}
	booted, _ := cl.BootedAt("worker-001")
	node := cl.Node("worker-001")
	if !booted.Before(node.CreationTimestamp.Time) {
		t.Fatalf("booted %v is not before the node joined at %v", booted, node.CreationTimestamp.Time)
	}
	// A reboot changes the bootID; the new boot time is read within a poll.
	for len(changes) > 0 {
		<-changes
	}
	post(t, srv.URL+"/sim/nodes/worker-001/reboot")
	waitFor(t, "the new boot ID", func() bool {
		n := cl.Node("worker-001")
		return n != nil && n.Status.NodeInfo.BootID != node.Status.NodeInfo.BootID
	})
	cl.ReadBootsNow(context.Background())
	after, _ := cl.BootedAt("worker-001")
	if !after.After(booted) {
		t.Fatalf("boot time %v did not move after the reboot (was %v)", after, booted)
	}
}

func TestBootTimesForbidden(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 2, Pods: 4, KubeletStats: false})
	waitFor(t, "the uptime state", func() bool { return cl.Info().Uptime == kube.UptimeUnavailable })
	if !strings.Contains(cl.Info().UptimeMessage, "nodes/proxy") {
		t.Fatalf("message %q", cl.Info().UptimeMessage)
	}
	if _, ok := cl.BootedAt("cp-1"); ok {
		t.Fatal("a boot time without the permission")
	}
}

func TestNotifiesNodeChanges(t *testing.T) {
	cl, srv, changes := start(t, sim.Options{Nodes: 4, Pods: 8})
	_ = cl
	// Drain the changes from the first list.
	for len(changes) > 0 {
		<-changes
	}
	post(t, srv.URL+"/sim/nodes/worker-001/ready?status=False")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c := <-changes:
			for _, n := range c.Views["nodes"] {
				if n == "worker-001" {
					return
				}
			}
		case <-deadline:
			t.Fatal("no change for worker-001")
		}
	}
}

func TestANodeThatStopsReportingRebuildsItsPods(t *testing.T) {
	cl, srv, changes := start(t, sim.Options{Nodes: 2, Pods: 12})
	pods := cl.PodsOn("worker-001")
	if len(pods) == 0 {
		t.Fatal("no pods on worker-001")
	}
	want := pods[0].Namespace + "/" + pods[0].Name
	for len(changes) > 0 {
		<-changes
	}
	post(t, srv.URL+"/sim/nodes/worker-001/ready?status=Unknown")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c := <-changes:
			for _, k := range c.Views["pods"] {
				if k == want {
					return
				}
			}
		case <-deadline:
			t.Fatalf("no change for pod %s", want)
		}
	}
}
