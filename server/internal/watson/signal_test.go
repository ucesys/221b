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

// A kubectl delete that waits for its object to go stops when its item is
// stopped, and SIGTERM meanwhile does not end the process: the wait is
// Watson's, under the item's context (review of November 24, 2026,
// finding 13). The pod here never goes: its deletion is answered, not made.
func TestStopEndsADeleteThatWaitsForDeletion(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)
	never := make(chan struct{})
	tg := heldSim(t, func(r *http.Request) bool { return false }, never)
	deleting := make(chan struct{}, 8)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/pods/") && r.URL.Query().Get("dryRun") == "" {
			deleting <- struct{}{}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
			return
		}
		proxy(w, r, tg.Config.Host)
	}))
	t.Cleanup(front.Close)
	svc := New(Options{CheckEvery: 50 * time.Millisecond})
	cl := Cluster{ID: "local", Name: "test", Env: "development", Config: &rest.Config{Host: front.URL}}
	it := svc.Submit(context.Background(), cl, alice(), "kubectl delete pod cache-0 -n web", "typed", "", false)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deleting:
	case <-time.After(20 * time.Second):
		t.Fatal("the pod was never deleted")
	}
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-sigs
	if got, _, _ := svc.Snapshot(it.ID, 0); got.Status != StatusRunning {
		t.Fatalf("not waiting: %s %s", got.Status, outputOf(svc, it.ID))
	}
	if _, err := svc.Stop(cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _, _ := svc.Snapshot(it.ID, 0); got.Status == StatusStopped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _, _ := svc.Snapshot(it.ID, 0)
	t.Fatalf("Stop did not end the wait: %s", got.Status)
}

// proxy passes a request on to the server at host.
func proxy(w http.ResponseWriter, r *http.Request, host string) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, host+r.URL.RequestURI(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
