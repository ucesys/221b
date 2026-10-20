package leader_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/leader"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSingleAlwaysLeads(t *testing.T) {
	e := leader.New(leader.Options{})
	if !e.Single() || !e.IsLeader() || e.LeaderAddr() != "" {
		t.Fatalf("single %v leader %v addr %q", e.Single(), e.IsLeader(), e.LeaderAddr())
	}
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIdentity(t *testing.T) {
	pod, addr := leader.ParseIdentity(leader.Identity("console-0", "10.42.0.7:8080"))
	if pod != "console-0" || addr != "10.42.0.7:8080" {
		t.Fatalf("%q %q", pod, addr)
	}
	if _, addr := leader.ParseIdentity("someone-else"); addr != "" {
		t.Fatalf("an identity without an address gave %q", addr)
	}
	if _, addr := leader.ParseIdentity("x@not an address"); addr != "" {
		t.Fatalf("a bad address gave %q", addr)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Two replicas share one Lease: one leads, the other learns its address,
// and when the leader shuts down the other takes over at once.
func TestTwoReplicasHandOver(t *testing.T) {
	client := fake.NewClientset()
	opts := func(pod, addr string, started *atomic.Int32) leader.Options {
		return leader.Options{
			Client: client, Namespace: "221b", Name: "console-leader", Pod: pod, Addr: addr,
			LeaseDuration: 3 * time.Second, RenewDeadline: 2 * time.Second, RetryPeriod: 100 * time.Millisecond,
			OnStartedLeading: func() { started.Add(1) },
			Logger:           quiet,
		}
	}
	var aStarted, bStarted atomic.Int32
	a := leader.New(opts("a", "10.0.0.1:8080", &aStarted))
	ctxA, stopA := context.WithCancel(context.Background())
	doneA := make(chan struct{})
	go func() { _ = a.Run(ctxA); close(doneA) }()
	waitFor(t, "a to lead", a.IsLeader)

	b := leader.New(opts("b", "10.0.0.2:8080", &bStarted))
	ctxB, stopB := context.WithCancel(context.Background())
	defer stopB()
	go func() { _ = b.Run(ctxB) }()
	waitFor(t, "b to see a", func() bool { return b.LeaderAddr() == "10.0.0.1:8080" })
	if b.IsLeader() {
		t.Fatal("both replicas lead")
	}

	// a shuts down and gives the lease up; b takes it well before the
	// lease would have run out.
	start := time.Now()
	stopA()
	<-doneA
	if a.IsLeader() {
		t.Fatal("a still leads after shutting down")
	}
	waitFor(t, "b to lead", b.IsLeader)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("hand-over took %s", took)
	}
	waitFor(t, "b to see itself as leader", func() bool { return b.LeaderAddr() == "10.0.0.2:8080" })
	if aStarted.Load() != 1 || bStarted.Load() != 1 {
		t.Fatalf("started leading: a %d, b %d", aStarted.Load(), bStarted.Load())
	}
}

func TestRunNeedsAnAddress(t *testing.T) {
	e := leader.New(leader.Options{Client: fake.NewClientset(), Namespace: "221b", Name: "l"})
	if err := e.Run(context.Background()); err == nil {
		t.Fatal("ran without the pod's name and address")
	}
}

// The chart creates the Lease empty, so helm uninstall removes it; the
// first replica takes it as it is.
func TestTakesTheChartsEmptyLease(t *testing.T) {
	client := fake.NewClientset(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "console-leader", Namespace: "221b"}})
	e := leader.New(leader.Options{Client: client, Namespace: "221b", Name: "console-leader", Pod: "a", Addr: "10.0.0.1:8080",
		LeaseDuration: 3 * time.Second, RenewDeadline: 2 * time.Second, RetryPeriod: 100 * time.Millisecond, Logger: quiet})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()
	waitFor(t, "the leader", e.IsLeader)
	l, err := client.CoordinationV1().Leases("221b").Get(ctx, "console-leader", metav1.GetOptions{})
	if err != nil || l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity != "a@10.0.0.1:8080" {
		t.Fatalf("lease %+v %v", l.Spec, err)
	}
}
