package watson

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// hooks stands in for the toolbox: a plan and a start that record who
// asked for what.
type hooks struct {
	mu       sync.Mutex
	started  []string
	approver []string
	fail     error
}

func (h *hooks) set(svc *Service) {
	svc.SetSessionHooks(func(ctx context.Context, cl Cluster, it Item) *Plan {
		return &Plan{Targets: []string{"pod/api-1 -n web"}, Impact: []string{"Opens a terminal."}, DryRun: []Line{{K: "out", T: "Kubernetes allows Alice to create pods/exec in namespace web."}}, Object: "kind: Pod"}
	}, func(cl Cluster, it Item, approver Who) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.fail != nil {
			return "", h.fail
		}
		h.started = append(h.started, it.Requester.User)
		h.approver = append(h.approver, approver.User)
		return "sess01", nil
	})
}

func shellCard() SessionCard {
	return SessionCard{Tool: "shell", Summary: "shell in pod web/api-1", Request: map[string]string{"tool": "shell"}}
}

func TestASessionCardOpensItsSessionOnApproval(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	h := &hooks{}
	h.set(svc)
	it := svc.SubmitSession(cl, alice(), shellCard(), "toolbox")
	if it.Status != StatusPending || !it.Change || it.Line != "session: shell in pod web/api-1" {
		t.Fatalf("%s %v %q", it.Status, it.Change, it.Line)
	}
	got := wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if got.Plan.Object != "kind: Pod" || len(got.Plan.DryRun) != 1 {
		t.Fatalf("plan %+v", got.Plan)
	}
	// A session card is changed in its dialog, never as a line of text.
	if _, err := svc.Edit(context.Background(), cl, alice(), it.ID, "kubectl get pods"); !errors.Is(err, ErrState) {
		t.Fatalf("edit: %v", err)
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusDone })
	if done.Session.SessionID != "sess01" {
		t.Fatalf("session %+v", done.Session)
	}
	if !strings.Contains(outputOf(svc, it.ID), "Session sess01") {
		t.Fatalf("output %q", outputOf(svc, it.ID))
	}
	// The session runs as the person who asked (TB-37).
	if len(h.started) != 1 || h.started[0] != "alice@corp.example" {
		t.Fatalf("started as %v", h.started)
	}
}

func TestASessionThatCannotStartFailsItsCard(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	h := &hooks{fail: errors.New("pods \"api-1\" not found")}
	h.set(svc)
	it := svc.SubmitSession(cl, alice(), shellCard(), "toolbox")
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	got := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if got.Status != StatusFailed || !strings.Contains(outputOf(svc, it.ID)+got.Reason, "not found") {
		t.Fatalf("%s %q %q", got.Status, got.Reason, outputOf(svc, it.ID))
	}
}

func TestANodeShellNeedsASecondAdministrator(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	h := &hooks{}
	h.set(svc)
	card := SessionCard{Tool: "node-shell", Summary: "node shell on worker-001", NeedsTwo: true, Reason: "kubelet is restarting in a loop"}
	it := svc.SubmitSession(cl, alice(), card, "toolbox")
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	var r *Refusal
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); !errorsAs(err, &r) || r.Rule != "two people" {
		t.Fatalf("self-approval on a development cluster: %v", err)
	}
	// A non-admin neither sees the card nor approves it.
	if _, err := svc.Approve(context.Background(), cl, bob(), it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a non-admin approved: %v", err)
	}
	if r := svc.refusalFor(cl, alice(), ptrWho(bob()), true, true); r == nil || !strings.Contains(r.Message, "SuperAdmin") {
		t.Fatalf("the rule itself does not name a SuperAdmin: %v", r)
	}
	admin := bob()
	admin.Admin = true
	if _, err := svc.Approve(context.Background(), cl, admin, it.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusDone })
	if h.started[0] != "alice@corp.example" || h.approver[0] != "bob@corp.example" {
		t.Fatalf("started %v approved %v", h.started, h.approver)
	}
	// Without sign-in nobody can be told apart: refused at once.
	anon := Who{Session: "s-anon", Name: "Anonymous"}
	if it := svc.SubmitSession(cl, anon, card, "toolbox"); it.Status != StatusRefused {
		t.Fatalf("anonymous node shell: %s", it.Status)
	}
}

func TestASessionOnProductionWaitsForASecondPerson(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{TwoPerson: func(c Cluster) bool { return c.Env == "production" }})
	cl.Env = "production"
	h := &hooks{}
	h.set(svc)
	card := shellCard()
	card.Reason = "checking the cache warm-up"
	it := svc.SubmitSession(cl, alice(), card, "toolbox")
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err == nil {
		t.Fatal("alice approved her own production shell")
	}
	// The session opens as Alice, with her rights, so its second person is
	// a SuperAdmin (TB-07, TB-37): Bob, who is not one, neither sees the
	// card nor approves it.
	if svc.Visible(it, bob(), cl) {
		t.Fatal("a non-admin sees a session card waiting for its second person")
	}
	if _, err := svc.Approve(context.Background(), cl, bob(), it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a non-admin approved a production session: %v", err)
	}
	admin := bob()
	admin.Admin = true
	if !svc.Visible(it, admin, cl) {
		t.Fatal("a SuperAdmin does not see the card")
	}
	if _, err := svc.Approve(context.Background(), cl, admin, it.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusDone })
	if h.started[0] != "alice@corp.example" || h.approver[0] != "bob@corp.example" {
		t.Fatalf("started as %v, approved by %v", h.started, h.approver)
	}
	// A Watson command on the same cluster still takes anyone signed in as
	// its second person: it runs as the approver (P6-07).
	cmd := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-001", "typed", "", true)
	if len(cmd) != 1 || !svc.Visible(cmd[0], bob(), cl) {
		t.Fatalf("a command card is hidden from the second person: %+v", cmd)
	}
}

func TestANodeShellOnProductionIsApprovedByASuperAdmin(t *testing.T) {
	// With toolbox.nodeShell.approval cluster (the default) a node shell on
	// production follows the two-person rule, and its approver is a
	// SuperAdmin like any session's.
	svc, cl, _, _, _ := setup(t, Options{TwoPerson: func(c Cluster) bool { return c.Env == "production" }})
	cl.Env = "production"
	h := &hooks{}
	h.set(svc)
	card := SessionCard{Tool: "node-shell", Summary: "node shell on worker-001", Reason: "kubelet is restarting in a loop"}
	it := svc.SubmitSession(cl, alice(), card, "toolbox")
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(context.Background(), cl, bob(), it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a non-admin approved a node shell: %v", err)
	}
	admin := bob()
	admin.Admin = true
	if _, err := svc.Approve(context.Background(), cl, admin, it.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusDone })
}

func ptrWho(w Who) *Who { return &w }
