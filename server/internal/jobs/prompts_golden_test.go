package jobs

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
)

// The tasks of the nightly audit, the alert diagnosis and the daily
// digest, as their runs give them to the agents with sample data, are
// kept in the golden directory (P-3).
func TestGoldenPrompts(t *testing.T) {
	w := setup(t)
	w.s.SetRouter(&fakeRouter{})
	w.s.o.Alerts = func(context.Context) []Alert {
		return []Alert{{Cluster: "local", Kind: "pods", Severity: SeverityError, Name: "web-0", Namespace: "shop", Status: "CrashLoopBackOff", Reason: "BackOff", Message: "Back-off restarting failed container web"}}
	}
	w.s.RegisterBuiltins()
	w.agents.text = "Fine.\n\n```json\n{\"findings\": []}\n```"
	ctx := context.Background()
	for _, kind := range []string{KindAudit, KindAlert, KindDigest} {
		on(t, w.s, kind, Change{Clusters: &[]string{"local"}})
		if _, err := w.s.RunNow(ctx, kind, Person{Email: "bob"}); err != nil {
			t.Fatal(err)
		}
		w.s.Wait()
		w.agents.mu.Lock()
		task := w.agents.calls[len(w.agents.calls)-1].Task
		w.agents.mu.Unlock()
		prompttest.Check(t, "job."+kind, task)
	}
}
