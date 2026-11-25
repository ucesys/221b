package watson

import (
	"context"
	"strings"
	"sync"
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

// A person whose roles change nothing holds no standing approval: what
// they approved would run as them and fail, a second person's approval
// used up (review of November 24, 2026, cut by the cap: service.go:673).
func TestAViewerHoldsNoStandingApproval(t *testing.T) {
	var demoted atomic.Bool
	svc, cl, _, _, _ := setup(t, Options{TwoPerson: twoPeople, ApproveAllMax: func(Cluster) time.Duration { return time.Hour }})
	svc.SetReread(func(w Who) (Who, bool) {
		w.ViewOnly = demoted.Load()
		return w, true
	})
	viewer := alice()
	viewer.ViewOnly = true
	if _, err := svc.ApproveAll(cl, viewer, 10*time.Minute); err == nil {
		t.Fatal("a Viewer was given a standing approval")
	}
	if _, err := svc.ApproveAll(cl, alice(), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	demoted.Store(true)
	it := svc.Submit(context.Background(), cl, bob(), "kubectl cordon worker-004", "typed", "", false)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	time.Sleep(300 * time.Millisecond)
	if got, _, _ := svc.Snapshot(it.ID, 0); got.Status != StatusPending {
		t.Fatalf("approved by a Viewer's grant: %s", got.Status)
	}
	if _, ok := svc.Standing(cl, alice()); ok {
		t.Fatal("the grant stands")
	}
}

// Refused commands and reads never run are trimmed like finished ones
// (review of November 24, 2026, cut by the cap: service.go:821).
func TestRefusedAndUnrunItemsAreTrimmed(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	svc, cl, _, _, _ := setup(t, Options{Now: clock})
	ctx := context.Background()
	refused := svc.Submit(ctx, cl, alice(), "kubectl exec -it api -- sh", "typed", "", false)[0]
	unrun := svc.Submit(ctx, cl, alice(), "kubectl get nodes", "typed", "", false)[0]
	if refused.Status != StatusRefused || unrun.Status != StatusPending {
		t.Fatalf("%s %s", refused.Status, unrun.Status)
	}
	mu.Lock()
	now = now.Add(13 * time.Hour)
	mu.Unlock()
	svc.Submit(ctx, cl, bob(), "kubectl exec -it api -- sh", "typed", "", false)
	if svc.Get(refused.ID) != nil || svc.Get(unrun.ID) != nil {
		t.Fatal("kept past their time")
	}
}

// A card's dry run keeps at most 64 KB of output (review of November 24,
// 2026, cut by the cap: service.go:883).
func TestACardsDryRunOutputIsBounded(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{Run: func(ctx context.Context, c *Command, tg Target, out, errOut ioWriter, extra ...string) int {
		out.Write([]byte(strings.Repeat("x", 4<<20) + "\n"))
		errOut.Write([]byte(strings.Repeat("y", 1<<20) + "\n"))
		return 0
	}})
	got := card(t, svc, cl, alice(), "kubectl cordon worker-001")
	size := 0
	for _, l := range got.Plan.DryRun {
		size += len(l.T)
	}
	if size > 65<<10 {
		t.Fatalf("%d bytes kept", size)
	}
}

// One request makes at most MaxCommands cards; the rest are refused in one
// item (review of November 24, 2026, sweep).
func TestOneRequestMakesABoundedNumberOfCards(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	text := strings.Repeat("kubectl cordon worker-001\n", 500)
	items := svc.Submit(context.Background(), cl, alice(), text, "typed", "", false)
	if len(items) != 21 || items[20].Status != StatusRefused || !strings.Contains(items[20].Reason, "480") {
		t.Fatalf("%d items, the last %+v", len(items), items[len(items)-1])
	}
}
