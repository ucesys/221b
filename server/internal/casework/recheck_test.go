package casework

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- Verification rests on what a run re-checked (phase 12 review, W-1
// to W-4, CF-94 to CF-96) ---

// fixed opens an agent-found case of a job on prod and closes it as
// fixed, so its fix waits for verification; it returns the case's id.
func (w *world) fixed(t *testing.T, job string, f jobs.Finding) string {
	t.Helper()
	res := w.route(t, job, true, f)
	id := res.Findings[0].Case
	if _, err := w.svc.Fix(context.Background(), id, FixInput{Text: "Fixed it."}, olivia); err != nil {
		t.Fatal(err)
	}
	if !w.report(t, id).VerifyPending {
		t.Fatalf("%s does not wait for verification", id)
	}
	return id
}

// W-1 (a): an alert diagnosis's run reads the objects of the alerts it
// diagnosed, nothing else: a waiting fix on another object of the same
// cluster and job stays untouched, even when the agent says it is gone;
// the one on the alert's object is verified.
func TestAnAlertRunVerifiesOnlyItsObjectW1(t *testing.T) {
	w := setup(t)
	x := w.fixed(t, "alert-diagnosis", finding("pod.crashloop", "shop/Pod/web-1", "web-1 restarts"))
	y := w.fixed(t, "alert-diagnosis", finding("pod.crashloop", "shop/Pod/api-1", "api-1 restarts"))
	w.now = w.now.Add(time.Hour)
	scope := &jobs.Scope{Objects: []jobs.ScopedObj{{Cluster: "prod", Object: "shop/Pod/web-1"}}}
	res := w.routeIn(t, "alert-diagnosis", true, scope, []jobs.Recheck{
		recheck("pod.crashloop", "shop/Pod/web-1", false, "web-1 is running, 0 restarts in an hour"),
		recheck("pod.crashloop", "shop/Pod/api-1", false, "api-1 looks fine"),
	})
	if len(res.Verified) != 1 || res.Verified[0] != x || res.NotRechecked != 0 {
		t.Fatalf("verified %v, not re-checked %d", res.Verified, res.NotRechecked)
	}
	if r := w.report(t, y); !r.VerifyPending || r.Verified != nil || r.Unchecked != 0 {
		t.Errorf("the fix on another object was touched: %+v", r)
	}
	c := w.get(t, x)
	if c.VerifiedBy == nil || c.VerifiedBy.Run != "run-3" || c.VerifiedBy.Evidence != "web-1 is running, 0 restarts in an hour" || c.Verified == nil || c.VerifyWait != nil {
		t.Errorf("what the verification rested on: %+v %+v", c.VerifiedBy, c.VerifyWait)
	}
	if !strings.Contains(messages(c), "The fix is verified: run-3 (Nightly audit) re-checked it: web-1 is running, 0 restarts in an hour.") {
		t.Errorf("no note: %s", messages(c))
	}
}

// W-1 (b): a run without a scope (the daily digest, every kind but the
// audit and the alert diagnosis) verifies nothing, whatever it says.
func TestADigestRunVerifiesNothingW1(t *testing.T) {
	w := setup(t)
	id := w.fixed(t, "daily-digest", finding("digest.failed-runs", "", "Runs failed"))
	res := w.routeIn(t, "daily-digest", true, nil, []jobs.Recheck{recheck("digest.failed-runs", "", false, "no run failed")})
	if len(res.Verified) != 0 || res.NotRechecked != 0 {
		t.Fatalf("a digest verified: %+v", res)
	}
	if r := w.report(t, id); !r.VerifyPending || r.Verified != nil {
		t.Errorf("the fix: %+v", r)
	}
}

// W-1 (c): an audit verifies a waiting fix only when it re-checked it and
// found it gone; re-checked and still there with no finding, the case
// reopens, the fix did not hold.
func TestAnAuditVerifiesOnlyWhatItRecheckedW1(t *testing.T) {
	w := setup(t)
	gone := w.fixed(t, "nightly-audit", finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	still := w.fixed(t, "nightly-audit", finding("pod.no-probes", "shop/Deployment/api", "api has no probes"))
	w.now = w.now.Add(time.Hour)
	res := w.routeWith(t, "nightly-audit", true, []jobs.Recheck{
		recheck("pod.no-limits", "shop/Deployment/web", false, "web's containers have limits"),
		recheck("pod.no-probes", "shop/Deployment/api", true, "api still has no readiness probe"),
	})
	if len(res.Verified) != 1 || res.Verified[0] != gone {
		t.Fatalf("verified %v", res.Verified)
	}
	if c := w.get(t, gone); c.Verified == nil || c.VerifiedBy == nil || c.VerifiedBy.Evidence != "web's containers have limits" {
		t.Errorf("not verified: %+v", c.Brief)
	}
	c := w.get(t, still)
	if c.Status != issues.StatusNew || c.Outcome != "" || w.report(t, still).VerifyPending {
		t.Fatalf("still there, not reopened: %+v", c.Brief)
	}
	if !strings.Contains(messages(c), "Reopened: the fix did not hold; run-3 (Nightly audit) re-checked it: api still has no readiness probe.") {
		t.Errorf("no note: %s", messages(c))
	}
}

// W-1 (d): an audit that lists nothing for a waiting key in its scope
// leaves the case waiting, counts the run on it, and says how many fixes
// it did not re-check; the drawer says since when it waits and how many
// runs passed (I-1).
func TestAFixNotRecheckedKeepsWaitingW1(t *testing.T) {
	w := setup(t)
	id := w.fixed(t, "nightly-audit", finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	since := w.now
	for range 2 {
		w.now = w.now.Add(24 * time.Hour)
		if res := w.route(t, "nightly-audit", true); len(res.Verified) != 0 || res.NotRechecked != 1 {
			t.Fatalf("a run that did not re-check: %+v", res)
		}
	}
	c := w.get(t, id)
	if c.Verified != nil || c.Status != issues.StatusClosed || c.VerifyWait == nil || !c.VerifyWait.Since.Equal(since) || c.VerifyWait.Unchecked != 2 {
		t.Fatalf("waiting: %+v %+v", c.Brief, c.VerifyWait)
	}
	// A failed run, or one of another job, counts nothing.
	w.route(t, "nightly-audit", false)
	w.route(t, "alert-diagnosis", true)
	if n := w.report(t, id).Unchecked; n != 2 {
		t.Errorf("counted %d", n)
	}
	// Re-checked at last: verified, and the count goes with the wait.
	w.routeWith(t, "nightly-audit", true, []jobs.Recheck{recheck("pod.no-limits", "shop/Deployment/web", false, "limits set")})
	if c = w.get(t, id); c.Verified == nil || c.VerifyWait != nil || c.VerifiedBy == nil {
		t.Errorf("verified: %+v %+v", c.Brief, c.VerifyWait)
	}
}

// W-3: a finding left out past the run's cap is neither seen nor not
// seen: a waiting fix with its key is not verified, whatever the agent
// says, and not counted as not re-checked.
func TestAFindingLeftOutVerifiesNothingW3(t *testing.T) {
	w := setup(t)
	id := w.fixed(t, "nightly-audit", finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	w.runs++
	res := w.svc.Route(context.Background(), jobs.RouteInput{Run: "run-9", Job: "nightly-audit", Kind: jobs.KindAudit, Label: "Nightly audit", Clusters: []string{"prod"}, OK: true,
		Scope: &jobs.Scope{Clusters: []string{"prod"}}, Rechecked: []jobs.Recheck{recheck("pod.no-limits", "shop/Deployment/web", false, "looks fine")},
		LeftOut: []jobs.Key{{Cluster: "prod", Object: "shop/Deployment/web", Check: "pod.no-limits"}}})
	if len(res.Verified) != 0 || res.NotRechecked != 0 {
		t.Fatalf("a key left out: %+v", res)
	}
	if r := w.report(t, id); !r.VerifyPending || r.Unchecked != 0 {
		t.Errorf("the fix: %+v", r)
	}
}

// auditAgents answer the nightly audit: the cluster local fails, prod
// answers with what it re-checked.
type auditAgents struct{ answer string }

func (a auditAgents) Run(_ context.Context, req jobs.AgentRequest) jobs.AgentResult {
	if req.Cluster == "local" {
		return jobs.AgentResult{Err: errors.New("the cluster did not answer")}
	}
	return jobs.AgentResult{Text: "Reviewed the cluster.\n\n```json\n" + a.answer + "\n```", CostUSD: 0.01}
}

func (auditAgents) Usable(string, string) string { return "" }

// W-2: in an audit of two clusters, one of which fails, no fix on the
// failed cluster is verified, the other's is, and the run says which
// cluster failed.
func TestAFailedClusterVerifiesNothingW2(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.jobs.SetRouter(w.svc)
	w.jobs.RegisterBuiltins()
	onProd := w.fixed(t, jobs.KindAudit, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	f := finding("pod.no-limits", "shop/Deployment/web", "web has no limits on local")
	f.Cluster, f.Key.Cluster = "local", "local"
	onLocal := w.fixed(t, jobs.KindAudit, f)
	w.jobs.SetAgents(auditAgents{answer: `{"findings": [], "proposals": [], "rechecked": [{"check": "pod.no-limits", "resource": "shop/Deployment/web", "still": false, "evidence": "limits set"},
		{"check": "pod.no-limits", "resource": "shop/Deployment/web", "cluster": "local", "still": false, "evidence": "not read"}]}`})
	yes := true
	if _, err := w.jobs.Update(ctx, jobs.KindAudit, jobs.Change{On: &yes}, jobs.Person{Name: "admin"}); err != nil {
		t.Fatal(err)
	}
	b, err := w.jobs.RunNow(ctx, jobs.KindAudit, jobs.Person{Email: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	w.jobs.Wait()
	r, _ := w.jobs.GetRun(ctx, b.ID)
	if r.Status != jobs.StatusOK || len(r.FailedClusters) != 1 || r.FailedClusters[0] != "local" || !strings.Contains(r.Note, "local: the cluster did not answer") {
		t.Fatalf("the run: %s %v %q", r.Status, r.FailedClusters, r.Note)
	}
	if len(r.VerifiedCases) != 1 || r.VerifiedCases[0] != onProd {
		t.Errorf("verified %v", r.VerifiedCases)
	}
	if c := w.report(t, onLocal); !c.VerifyPending || c.Verified != nil {
		t.Errorf("a fix on the failed cluster was verified: %+v", c)
	}
}
