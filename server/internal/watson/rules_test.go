package watson

import (
	"context"
	"strings"
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

// Turning standing approvals off ends the grants people hold, and lowering
// the limit shortens them (review of November 24, 2026, finding 15).
func TestTurningStandingApprovalsOffEndsTheGrants(t *testing.T) {
	var max atomic.Int64
	max.Store(int64(4 * time.Hour))
	svc, cl, _, _, buf := setup(t, Options{TwoPerson: twoPeople, ApproveAllMax: func(Cluster) time.Duration { return time.Duration(max.Load()) }})
	if _, err := svc.ApproveAll(cl, alice(), 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	max.Store(int64(10 * time.Minute))
	g, ok := svc.Standing(cl, alice())
	if !ok || g.Until.After(time.Now().Add(11*time.Minute)) {
		t.Fatalf("a lower limit: %+v %v", g, ok)
	}
	max.Store(0)
	if _, ok := svc.Standing(cl, alice()); ok {
		t.Fatal("the grant outlived the setting")
	}
	if _, err := svc.ApproveAll(cl, alice(), 0); err == nil {
		t.Fatal("granted with the setting off")
	}
	// Alice's grant no longer approves Bob's card.
	max.Store(int64(time.Hour))
	if _, err := svc.ApproveAll(cl, alice(), 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	max.Store(0)
	it := svc.Submit(context.Background(), cl, bob(), "kubectl cordon worker-004", "typed", "", false)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	time.Sleep(300 * time.Millisecond)
	if got, _, _ := svc.Snapshot(it.ID, 0); got.Status != StatusPending {
		t.Fatalf("approved under an ended grant: %s", got.Status)
	}
	ends := 0
	for _, e := range buf.events() {
		if e.Action == ActionApproveAll && strings.HasPrefix(e.Detail, "ended reason=") {
			ends++
		}
	}
	if ends != 2 {
		t.Fatalf("%d ends audited", ends)
	}
}
