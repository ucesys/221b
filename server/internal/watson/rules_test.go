package watson

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A change queued behind another starts under the cluster's rules as they
// are then: a connection made read-only meanwhile runs none of it, and a
// card that becomes ready under a standing approval after that is not
// approved (review of November 24, 2026, finding 12).
func TestChangesStopWhenTheConnectionTurnsReadOnly(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var readOnly atomic.Bool
	var ran atomic.Int32
	blocked := blockedRun(release)
	svc, cl, _, _, _ := setup(t, Options{
		ApproveAllMax: func(Cluster) time.Duration { return time.Hour },
		Run: func(ctx context.Context, c *Command, tg Target, out, errOut ioWriter, extra ...string) int {
			if len(extra) == 0 {
				ran.Add(1)
			}
			return blocked(ctx, c, tg, out, errOut, extra...)
		},
	})
	svc.SetRules(func(id string) (Cluster, bool) {
		now := cl
		now.ReadOnly, now.ReadOnlyHow = readOnly.Load(), "Turned read-only by the test."
		return now, true
	})
	ctx := context.Background()
	first := svc.Submit(ctx, cl, alice(), "kubectl cordon worker-001", "typed", "", false)[0]
	wait(t, svc, first.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(ctx, cl, alice(), first.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, svc, first.ID, func(i Item) bool { return i.Status == StatusRunning })
	second := svc.Submit(ctx, cl, alice(), "kubectl cordon worker-002", "typed", "", false)[0]
	wait(t, svc, second.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(ctx, cl, alice(), second.ID); err != nil {
		t.Fatal(err)
	}
	readOnly.Store(true)
	release <- struct{}{}
	if done := wait(t, svc, second.ID, func(i Item) bool { return finished(i.Status) }); done.Status != StatusRefused || done.Rule != "read-only cluster" {
		t.Fatalf("the queued change: %s %q", done.Status, done.Reason)
	}
	if n := ran.Load(); n != 1 {
		t.Fatalf("%d changes ran", n)
	}
	// A card planned with the writable cluster its request saw, ready
	// after the connection turned read-only, is not approved.
	readOnly.Store(false)
	if _, err := svc.ApproveAll(cl, alice(), 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	readOnly.Store(true)
	third := svc.Submit(ctx, cl, alice(), "kubectl cordon worker-003", "typed", "", false)[0]
	wait(t, svc, third.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	time.Sleep(300 * time.Millisecond)
	if got, _, _ := svc.Snapshot(third.ID, 0); got.Status != StatusPending {
		t.Fatalf("approved on a read-only connection: %s", got.Status)
	}
}
