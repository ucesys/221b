package kube_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// The cluster says it is complete only once every kind is listed (kube#7):
// with nodes, pods and namespaces listed and the events still being read,
// a missing alert made of events says nothing yet. A kind the console may
// not list counts as settled, or the cluster would never be complete.
func TestCompleteWaitsForEveryKindButARefusedOne(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 3, Pods: 6})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Listed by a list or a streaming watch: both wait.
		case strings.HasPrefix(r.URL.Path, "/api/v1/events"):
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		case strings.HasPrefix(r.URL.Path, "/apis/coordination.k8s.io/v1/leases"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"leases is forbidden"}`)
			return
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cl, err := kube.New(&rest.Config{Host: srv.URL}, kube.Options{
		Name: "sim", Mode: kube.ModeURL, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cl.RunWatches(ctx)
	waitFor(t, "the core kinds", cl.Ready)
	time.Sleep(500 * time.Millisecond)
	if cl.Complete() || cl.Info().Complete {
		t.Fatal("complete while the events are still being listed")
	}
	close(release)
	waitFor(t, "every kind", cl.Complete)
	if !cl.Info().Complete {
		t.Fatal("Info does not say complete")
	}
}
