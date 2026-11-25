package watson

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/sim"
)

// heldSim is the simulator behind a server that holds the requests hold
// picks until release is closed, having sent the first line of a log.
func heldSim(t *testing.T, hold func(r *http.Request) bool, release <-chan struct{}) Target {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold(r) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("first line\n"))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	var d Discovery
	disc, err := d.For("held", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return Target{Config: cfg, Discovery: disc}
}

// lockedBuffer is a buffer written from one goroutine and read from another.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// kubectl logs runs without kubectl's signal handler, which ends the
// process with os.Exit(1) at SIGTERM: the console's own shutdown handles
// the signal (review of November 24, 2026, finding 9). The test catches
// SIGTERM itself; with kubectl's handler the test binary exits.
func TestSIGTERMDuringKubectlLogsDoesNotEndTheProcess(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)
	release := make(chan struct{})
	tg := heldSim(t, func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/log") }, release)
	c, err := Parse("kubectl logs cache-0 -n web")
	if err != nil {
		t.Fatal(err)
	}
	var out lockedBuffer
	done := make(chan int, 1)
	go func() { done <- runKubectl(context.Background(), c, tg, &out, &out) }()
	for i := 0; i < 500 && !strings.Contains(out.String(), "first line"); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "first line") {
		t.Fatalf("no log: %q", out.String())
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sigs:
	case <-time.After(5 * time.Second):
		t.Fatal("the signal did not arrive")
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}
