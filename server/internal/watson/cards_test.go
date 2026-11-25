package watson

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A durable card's change (Phase 12, ADR-100) is read with the console's
// account for its fingerprint, which changes when its object does; once
// approved it runs as the approver, in their panel, with no card of its
// own, and the end hook hears how it went.
func TestADurableCardsChangeIsFingerprintedAndRunsAsItsApprover(t *testing.T) {
	var mu sync.Mutex
	var ended []Item
	svc, cl, s, srv, buf := setup(t, Options{})
	svc.SetOnEnd(func(it Item) {
		mu.Lock()
		ended = append(ended, it)
		mu.Unlock()
	})
	ctx := context.Background()
	if _, err := svc.Inspect(ctx, cl, "kubectl get nodes", nil); !errors.Is(err, ErrNotChange) {
		t.Fatalf("a read: %v", err)
	}
	first, err := svc.Inspect(ctx, cl, "kubectl cordon worker-001", nil)
	if err != nil || first.Fingerprint == "" || len(first.Targets) != 1 || first.Targets[0] != "node worker-001" {
		t.Fatalf("inspected %+v %v", first, err)
	}
	if again, _ := svc.Inspect(ctx, cl, "kubectl cordon worker-001", nil); again.Fingerprint != first.Fingerprint {
		t.Fatal("the fingerprint moved with nothing changed")
	}
	resp, err := http.Post(srv.URL+"/sim/nodes/worker-001/cordon", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if changed, _ := svc.Inspect(ctx, cl, "kubectl cordon worker-002", nil); changed.Fingerprint == first.Fingerprint {
		t.Fatal("two nodes, one fingerprint")
	}
	// The simulator applies the cordon on its next turn.
	moved := false
	for i := 0; i < 200 && !moved; i++ {
		changed, err := svc.Inspect(ctx, cl, "kubectl cordon worker-001", nil)
		moved = changed.Fingerprint != first.Fingerprint
		if err != nil {
			t.Fatal(err)
		}
		waitABit()
	}
	if !moved {
		t.Fatal("the node changed and its fingerprint did not")
	}
	it, err := svc.RunApproved(cl, bob(), "kubectl cordon worker-002", "Proposal c1 by Troubleshooter", "Cordon worker-002", &first)
	if err != nil {
		t.Fatal(err)
	}
	done, ok := svc.WaitDone(ctx, it.ID)
	if !ok || done.Status != StatusDone || !s.Node("worker-002").Spec.Unschedulable || s.LastImpersonated() != "bob@corp.example" {
		t.Fatalf("ran %+v, as %q", done, s.LastImpersonated())
	}
	if items := svc.Items(cl, bob()); len(items) != 1 || items[0].From != "Proposal c1 by Troubleshooter" {
		t.Fatalf("bob's panel: %+v", items)
	}
	var actions []string
	for _, e := range buf.events() {
		actions = append(actions, e.Action)
	}
	if strings.Join(actions, ",") != "watson.command,watson.approve,watson.result" {
		t.Fatalf("audit %v", actions)
	}
	// A read-only cluster still refuses it.
	ro := cl
	ro.ReadOnly = true
	if _, err := svc.RunApproved(ro, bob(), "kubectl cordon worker-003", "Proposal c2", "", nil); err == nil {
		t.Fatal("ran on a read-only cluster")
	}
	// A card that something else took the place of goes, and the hook
	// hears of it.
	pending := svc.Submit(ctx, cl, alice(), "kubectl uncordon worker-002", "Sherlock", "", false)[0]
	svc.Supersede(pending.ID, "Approved as proposal c3 in AIOps › Inbox.")
	if got := wait(t, svc, pending.ID, func(i Item) bool { return finished(i.Status) }); got.Status != StatusDiscarded {
		t.Fatalf("superseded: %s", got.Status)
	}
	deadline := 0
	for {
		mu.Lock()
		n := len(ended)
		mu.Unlock()
		if n >= 3 || deadline > 200 {
			break
		}
		deadline++
		waitABit()
	}
	mu.Lock()
	defer mu.Unlock()
	statuses := map[string]string{}
	for _, e := range ended {
		statuses[e.ID] = e.Status
	}
	if statuses[it.ID] != StatusDone || statuses[pending.ID] != StatusDiscarded {
		t.Fatalf("heard %v", statuses)
	}
}

func waitABit() { time.Sleep(10 * time.Millisecond) }

// A durable card asked for by a person reads what it shows as them: an
// object their access does not reach is left out of its impact, while its
// fingerprint, read with the console's account, is the same whoever asked.
func TestADurableCardShowsWhatItsRequesterMaySee(t *testing.T) {
	svc, cl, _, srv, _ := setup(t, Options{})
	ctx := context.Background()
	post := func(path, body string) {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s: %s", path, resp.Status)
		}
	}
	post("/api/v1/namespaces/default/secrets", `{"metadata":{"name":"db-creds","annotations":{"note":"value-only-some-may-read"}},"data":{"k":"c2VjcmV0LXZhbHVl"}}`)
	post("/sim/deny", `{"only":{"alice@corp.example":["web"]}}`)
	line := "kubectl annotate secret db-creds -n default note-"
	own, err := svc.Inspect(ctx, cl, line, nil)
	if err != nil || !strings.Contains(strings.Join(own.Impact, "\n"), "value-only-some-may-read") {
		t.Fatalf("an agent's own card: %+v %v", own, err)
	}
	as := alice().Person
	asked, err := svc.Inspect(ctx, cl, line, &as)
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(append(asked.Targets, asked.Impact...), "\n"); strings.Contains(text, "value-only-some-may-read") || !strings.Contains(text, "may not see") {
		t.Fatalf("the requester's card: %+v", asked)
	}
	if asked.Fingerprint == "" || asked.Fingerprint != own.Fingerprint {
		t.Fatalf("fingerprints %q and %q", asked.Fingerprint, own.Fingerprint)
	}
	if fp, err := svc.Fingerprint(ctx, cl, line); err != nil || fp.Fingerprint != own.Fingerprint || len(fp.Impact)+len(fp.Targets) != 0 {
		t.Fatalf("fingerprint only: %+v %v", fp, err)
	}
}
