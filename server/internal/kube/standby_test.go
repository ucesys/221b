package kube_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// A standby watches without polling (ADR-70): with RunWatches alone the
// cluster lists and follows changes but never checks the connection, so
// its state stays "connecting"; RunPollers checks it, and its context
// stops the checks again. A cold standby, which runs neither, asks the
// API server for nothing at all.
func TestAStandbyWatchesWithoutPolling(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 4, Pods: 8})
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	newCluster := func() *kube.Cluster {
		cl, err := kube.New(&rest.Config{Host: srv.URL}, kube.Options{
			Name: "sim", Mode: kube.ModeURL, HealthInterval: 50 * time.Millisecond,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Cold: built, and nothing asked of the API server until the Lease is won.
	cold := newCluster()
	time.Sleep(300 * time.Millisecond)
	if cold.Ready() || cold.Watching() || requests.Load() != 0 {
		t.Fatalf("a cold standby: ready=%v watching=%v after %d requests", cold.Ready(), cold.Watching(), requests.Load())
	}

	// Warm: the watches list and follow changes; the connection is never
	// checked, so the state stays as built.
	warm := newCluster()
	warm.RunWatches(ctx)
	waitFor(t, "the first list", warm.Ready)
	if !warm.Watching() || requests.Load() == 0 {
		t.Fatal("the watches did not run")
	}
	post(t, srv.URL+"/sim/nodes/worker-001/cordon")
	waitFor(t, "a change on a standby", func() bool { n := warm.Node("worker-001"); return n != nil && n.Spec.Unschedulable })
	time.Sleep(200 * time.Millisecond)
	if info := warm.Info(); info.State != kube.StateConnecting || info.Version != "" {
		t.Fatalf("a standby checked the connection: state %q, version %q", info.State, info.Version)
	}

	// Leading: the pollers check the connection; RunWatches again does
	// nothing; the pollers' context stops them, so an outage goes unseen
	// while the watches go on.
	pctx, pcancel := context.WithCancel(ctx)
	warm.RunPollers(pctx)
	waitFor(t, "a health check", func() bool { return warm.Info().State == kube.StateConnected })
	warm.RunWatches(ctx)
	pcancel()
	time.Sleep(100 * time.Millisecond)
	post(t, srv.URL+"/sim/down?on=true")
	time.Sleep(300 * time.Millisecond)
	if st := warm.Info().State; st != kube.StateConnected {
		t.Fatalf("the health check went on after its context ended: state %q", st)
	}
	post(t, srv.URL+"/sim/down?on=false")
	if !warm.Ready() {
		t.Fatal("stopping the pollers stopped the watches")
	}
}
