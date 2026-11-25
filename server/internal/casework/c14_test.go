package casework

import (
	"context"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/jobs"
)

// The safety net's cases are listed while they are open (C-14 on the
// owner's console): two critical alerts that pass the minutes at the same
// look open two cases at the same moment, one with the alert's message as
// its text; each is analysed and finds no fix; both are in the cases' list
// and in its pending list, pending, beside a person's open case about the
// same kind of problem.
func TestTheSafetyNetsCasesAreListedC14(t *testing.T) {
	w := setup(t)
	w.jobOn(t, true)
	ctx := context.Background()
	w.agents.answer = `{"summary": "Not sure.", "cause": "", "evidence": [], "fix": {"summary": "", "commands": []}, "proposals": []}`
	if _, err := w.svc.Create(ctx, NewCase{Title: "Node not ready: node is not ready", Detail: "Node is not ready", Cluster: "prod", Resource: "Node/n9"}, olivia); err != nil {
		t.Fatal(err)
	}
	node := jobs.Alert{Cluster: "prod", Key: "node:n1", Kind: "node", Severity: jobs.SeverityError, Name: "n1", Status: "NotReady", Message: "Node is not ready", Fingerprint: "failed|NotReady"}
	pod := jobs.Alert{Cluster: "prod", Key: "pod:shop/web-1", Kind: "pod", Severity: jobs.SeverityError, Name: "web-1", Namespace: "shop", Status: "Error", Fingerprint: "failed|Error"}
	for range 16 {
		w.svc.WatchAlerts(ctx, func() []jobs.Alert { return []jobs.Alert{node, pod} })
		w.svc.Wait()
		w.now = w.now.Add(time.Minute)
	}
	for _, id := range []string{"C-2", "C-3"} {
		if c := w.get(t, id); c.CreatedBy == nil || c.CreatedBy.Kind != "system" || len(c.History) < 3 {
			t.Fatalf("%s is not the safety net's analysed case: %+v %+v", id, c.Brief, c.History)
		}
	}
	if c := w.get(t, "C-2"); c.Detail != "Node is not ready" {
		t.Fatalf("C-2 has no text: %+v", c.Brief)
	}
	for _, f := range []Filter{{}, {Pending: true}} {
		list, err := w.svc.List(ctx, olivia, f)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, b := range list {
			seen[b.ID] = b.Pending
		}
		for _, id := range []string{"C-1", "C-2", "C-3"} {
			if !seen[id] {
				t.Errorf("filter %+v: %s is not listed as pending: %v", f, id, seen)
			}
		}
	}
}
