package kube_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// ST-08: the server reads each kubelet's Summary API for the claims its
// pods mount, one request per node, and keeps the figures by claim.
func TestVolumeStatsComeFromTheKubelets(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 10, Pods: 60, KubeletStats: true})
	waitFor(t, "the claims", func() bool { return cl.Count("persistentvolumeclaims", "") > 0 })
	cl.ReadVolumesNow(context.Background())
	v, ok := cl.VolumeStats("monitoring", "prometheus-db-prometheus-0")
	if !ok || v.CapacityBytes == 0 || v.Node == "" {
		t.Fatalf("no stats for prometheus: %+v %v", v, ok)
	}
	if pct := float64(v.UsedBytes) * 100 / float64(v.CapacityBytes); pct < 85 || pct > 87 {
		t.Errorf("prometheus is %.1f%% full, want 86%%", pct)
	}
	// A claim no running pod mounts has none.
	if _, ok := cl.VolumeStats("analytics", "data-cache-2"); ok {
		t.Error("a pending claim has stats")
	}
}

// With kubeletStats off nothing is read.
func TestNoVolumeStatsWithoutKubeletStats(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 10, Pods: 60, KubeletStats: false})
	cl.ReadVolumesNow(context.Background())
	if _, ok := cl.VolumeStats("monitoring", "prometheus-db-prometheus-0"); ok {
		t.Error("stats without permission to read the kubelets")
	}
}

// The read of the volumes has its own time: the refresh's budget may be
// long gone when it finishes.
func TestTheVolumesAreReadWhateverTheRefreshsBudget(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 10, Pods: 60, KubeletStats: true})
	waitFor(t, "the claims", func() bool { return cl.Count("persistentvolumeclaims", "") > 0 })
	spent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cl.Poll(spent)
	waitFor(t, "the volume stats", func() bool {
		_, ok := cl.VolumeStats("monitoring", "prometheus-db-prometheus-0")
		return ok
	})
}

// A read that runs out of time keeps what it read, and the next goes on
// from where it stopped, so a large cluster's last nodes are reached too.
func TestAReadOfTheVolumesThatRunsOutOfTimeKeepsWhatItRead(t *testing.T) {
	var (
		mu    sync.Mutex
		asked []string
		hang  map[string]bool // the kubelets that do not answer; nil is none
	)
	s := sim.New(sim.Options{Nodes: 30, Pods: 400, KubeletStats: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if node, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/proxy/stats/summary"); ok {
			mu.Lock()
			asked = append(asked, node)
			stuck := hang != nil && hang[node]
			mu.Unlock()
			if stuck {
				<-r.Context().Done()
				return
			}
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Without kubeletStats the boot times are not read, so only these reads
	// reach the kubelets.
	console := func() *kube.Cluster {
		cl, err := kube.New(&rest.Config{Host: srv.URL}, kube.Options{Name: "sim", Mode: kube.ModeURL, HealthInterval: 200 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		go cl.Run(ctx)
		waitFor(t, "the first list", func() bool { return cl.Ready() })
		waitFor(t, "the claims", func() bool { return cl.Count("persistentvolumeclaims", "") > 0 })
		return cl
	}
	read := func(cl *kube.Cluster, within time.Duration, stuck map[string]bool) []string {
		mu.Lock()
		asked, hang = nil, stuck
		mu.Unlock()
		short, stop := context.WithTimeout(ctx, within)
		defer stop()
		cl.ReadVolumesNow(short)
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
	claims := func(cl *kube.Cluster) (n int) {
		for _, o := range cl.Objects("persistentvolumeclaims", "") {
			c := o.(metav1.Object)
			if _, ok := cl.VolumeStats(c.GetNamespace(), c.GetName()); ok {
				n++
			}
		}
		return n
	}

	cl := console()
	waitFor(t, "every node's pods", func() bool {
		return slices.Equal(read(cl, 10*time.Second, nil), read(cl, 10*time.Second, nil)) && claims(cl) > 0
	})
	nodes := read(cl, 10*time.Second, nil)
	slices.Sort(nodes)
	if len(nodes) < 7 {
		t.Fatalf("%d nodes run a pod with a claim, too few for this test", len(nodes))
	}
	all := claims(cl)
	stuck := map[string]bool{}
	for _, n := range nodes {
		stuck[n] = true
	}

	// A read that reached nobody loses nothing.
	read(cl, 200*time.Millisecond, stuck)
	if got := claims(cl); got != all {
		t.Fatalf("%d claims known after a read that reached nobody, %d before", got, all)
	}

	// On a fresh console, the first node answers and the others hang: its
	// answer stands, and the next read starts past the nodes this one asked.
	fresh := console()
	waitFor(t, "every node's pods", func() bool {
		return len(fresh.PodsOn(nodes[len(nodes)-1])) > 0
	})
	delete(stuck, nodes[0])
	first := read(fresh, 400*time.Millisecond, stuck)
	if claims(fresh) == 0 {
		t.Fatal("what the read took in time was thrown away")
	}
	if len(first) >= len(nodes) {
		t.Fatalf("the read reached every node: %v", first)
	}
	second := read(fresh, 400*time.Millisecond, stuck)
	if len(second) == 0 || slices.Contains(first, second[0]) {
		t.Fatalf("the first read asked %v, the second %v", first, second)
	}
}

// Metrics that did not come in time say nothing about the pods.
func TestAnExpiredPollKeepsThePodsUsage(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 4, Pods: 20, Metrics: true})
	cl.WantPodUsage(true)
	pollUntil(t, cl, "pod usage", func() bool { return len(cl.PodUsage()) > 0 })
	spent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cl.Poll(spent)
	if len(cl.PodUsage()) == 0 {
		t.Fatal("an expired poll wiped the pods' usage")
	}
}
