package kube_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// A rebooted node does not show the uptime of its last boot while its
// new boot time is read (kube-cut-1).
func TestARebootedNodeDoesNotShowItsOldUptime(t *testing.T) {
	cl, srv, _ := start(t, sim.Options{Nodes: 3, Pods: 6, KubeletStats: true})
	waitFor(t, "boot times", func() bool { return cl.Info().Uptime == kube.UptimeAvailable })
	if _, ok := cl.BootedAt("worker-001"); !ok {
		t.Fatal("no boot time")
	}
	node := cl.Node("worker-001")
	post(t, srv.URL+"/sim/nodes/worker-001/reboot")
	waitFor(t, "the new boot ID", func() bool {
		n := cl.Node("worker-001")
		return n != nil && n.Status.NodeInfo.BootID != node.Status.NodeInfo.BootID
	})
	if at, ok := cl.BootedAt("worker-001"); ok {
		t.Fatalf("the old boot time %v is shown for a new boot", at)
	}
	cl.ReadBootsNow(context.Background())
	if _, ok := cl.BootedAt("worker-001"); !ok {
		t.Fatal("no boot time after the read")
	}
}

// A read stopped because the replica lost the Lease is not a failed read:
// the next leader, or this replica leading again, reads at once rather
// than five minutes later (kube-cut-2).
func TestAStoppedBootReadIsNotRetriedLater(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 2, Pods: 4, KubeletStats: true})
	var hang atomic.Bool
	hang.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/proxy/stats/summary") && hang.Load() {
			<-r.Context().Done()
			return
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cl, err := kube.New(&rest.Config{Host: srv.URL}, kube.Options{Name: "sim", Mode: kube.ModeURL, KubeletStats: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cl.RunWatches(ctx)
	waitFor(t, "the first list", cl.Ready)
	lease, lost := context.WithCancel(ctx)
	go func() { time.Sleep(200 * time.Millisecond); lost() }()
	cl.ReadBootsNow(lease)
	hang.Store(false)
	cl.ReadBootsNow(ctx)
	if _, ok := cl.BootedAt("worker-001"); !ok {
		t.Fatal("the read stopped by the lost Lease is retried only in five minutes")
	}
}
