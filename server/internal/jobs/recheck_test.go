package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/schedule"
)

// --- Verification rests on what a run re-checked (phase 12 review, W-1
// to W-4) ---

// clusterAgents answer each cluster with its own text, or fail it, and
// keep the tasks by cluster.
type clusterAgents struct {
	mu    sync.Mutex
	text  map[string]string
	fail  map[string]bool
	tasks map[string][]string
}

func (a *clusterAgents) Run(_ context.Context, req AgentRequest) AgentResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tasks == nil {
		a.tasks = map[string][]string{}
	}
	a.tasks[req.Cluster] = append(a.tasks[req.Cluster], req.Task)
	if a.fail[req.Cluster] {
		return AgentResult{Err: errors.New("the cluster did not answer")}
	}
	return AgentResult{Text: a.text[req.Cluster], CostUSD: 0.1}
}

func (a *clusterAgents) Usable(string, string) string { return "" }

func (a *clusterAgents) task(cluster string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.tasks[cluster], "\n---\n")
}

// recheckRouter is fakeRouter with, on each cluster, an open case, a fix
// of the nightly audit and one of the alert diagnosis waiting for
// verification (W-4).
type recheckRouter struct{ fakeRouter }

func (r *recheckRouter) KeysInUse(_ context.Context, cluster string) ([]KeyInUse, error) {
	return []KeyInUse{
		{Key: Key{Cluster: cluster, Object: "shop/Pod/web", Check: "pod.no-limits"}, What: "case C-1", Title: "No limits on web", Job: KindAudit},
		{Key: Key{Cluster: cluster, Object: "shop/Deployment/api", Check: "pod.no-probes"}, What: "fix applied, re-check: C-2", Title: "api has no probes", Job: KindAudit, Recheck: true},
		{Key: Key{Cluster: cluster, Object: "shop/Pod/web-7f9", Check: "pod.crashloop"}, What: "fix applied, re-check: C-3", Title: "web-7f9 restarts", Job: KindAlert, Recheck: true},
		{Key: Key{Cluster: cluster, Object: "shop/Pod/api-1", Check: "pod.crashloop"}, What: "fix applied, re-check: C-4", Title: "api-1 restarts", Job: KindAlert, Recheck: true},
	}, nil
}

func (r *recheckRouter) last(t *testing.T) RouteInput {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.in) == 0 {
		t.Fatal("nothing routed")
	}
	return r.in[len(r.in)-1]
}

// W-1, W-2, W-4, P-2: the audit's task lists its own job's fixes waiting
// on the cluster to re-check, with the field to answer in; a cluster that
// fails is left out of the run's scope and said on the run, which reads
// "Done, with errors"; what the agent re-checked reaches the router keyed
// as the console writes keys, an entry that says nothing left out.
func TestTheAuditScopeIsTheClustersThatAnsweredW2(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	router := &recheckRouter{}
	w.s.SetRouter(router)
	agents := &clusterAgents{fail: map[string]bool{"local": true}, text: map[string]string{"prod": "Read it.\n\n```json\n" + `{"findings": [], "proposals": [], "rechecked": [
 {"check": "pod.no-probes", "resource": "shop/deploy/api", "still": "false", "evidence": "api has readiness and liveness probes"},
 {"check": "pod.no-limits", "resource": "shop/Pod/web", "evidence": "said nothing"},
 {"check": "pod.no-limits", "resource": "not an object", "still": false},
 {"check": "pod.no-limits", "resource": "shop/Deployment/web", "cluster": "local", "still": false, "evidence": "said of a cluster it was not asked about"}]}` + "\n```"}}
	w.s.SetAgents(agents)
	on(t, w.s, KindAudit, Change{})
	ctx := context.Background()
	b, err := w.s.RunNow(ctx, KindAudit, Person{Email: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	task := agents.task("prod")
	for _, want := range []string{"<recheck>", "- pod.no-probes on shop/Deployment/api: api has no probes (fix applied, re-check: C-2)", `give an entry in "rechecked" for every one`, `"rechecked": [{"check"`} {
		if !strings.Contains(task, want) {
			t.Errorf("the task lacks %q:\n%s", want, task)
		}
	}
	if recheck := between(task, "<recheck>", "</recheck>"); strings.Contains(recheck, "C-1") || strings.Contains(recheck, "C-3") {
		t.Errorf("the re-check lists more than the job's waiting fixes: %s", recheck)
	}
	in := router.last(t)
	if in.Scope == nil || fmt.Sprint(in.Scope.Clusters) != "[prod]" || len(in.Scope.Objects) != 0 || !in.OK {
		t.Fatalf("the scope: %+v", in.Scope)
	}
	if len(in.Rechecked) != 1 {
		t.Fatalf("re-checked: %+v", in.Rechecked)
	}
	x := in.Rechecked[0]
	if x.Key == nil || *x.Key != (Key{Cluster: "prod", Object: "shop/Deployment/api", Check: "pod.no-probes"}) || x.Still == nil || *x.Still || x.Evidence != "api has readiness and liveness probes" {
		t.Errorf("a re-check: %+v %+v", x, x.Key)
	}
	r, _ := w.s.GetRun(ctx, b.ID)
	if r.Status != StatusOK || fmt.Sprint(r.FailedClusters) != "[local]" || !strings.Contains(r.Note, "local: the cluster did not answer") {
		t.Errorf("the run: %s %v %q", r.Status, r.FailedClusters, r.Note)
	}
	// An answer about prod says nothing of a fix on local (second review).
	if !strings.Contains(r.Note, "1 re-check named another cluster than prod, which it was asked about, and was left out") {
		t.Errorf("the run does not say a re-check named another cluster: %q", r.Note)
	}
	if list := runs(t, w.s); len(list) != 1 || fmt.Sprint(list[0].FailedClusters) != "[local]" {
		t.Errorf("the list's row: %+v", list)
	}
}

// W-1: an alert diagnosis's scope is the objects of the alerts it
// diagnosed: its task lists for re-checking only its job's waiting fix on
// the alert's object, and the router is given that object alone.
func TestAnAlertRunScopeIsItsObjectW1(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	router := &recheckRouter{}
	w.s.SetRouter(router)
	agents := &clusterAgents{text: map[string]string{"prod": crashFinding}}
	w.s.SetAgents(agents)
	w.s.o.Alerts = func(context.Context) []Alert {
		return []Alert{{Cluster: "prod", Key: "pod:shop/web-7f9", Kind: "pod", Severity: SeverityError, Name: "web-7f9", Namespace: "shop", Status: "CrashLoopBackOff"}}
	}
	on(t, w.s, KindAlert, Change{When: &When{Event: EventAlert, Severity: SeverityWarning}})
	if _, err := w.s.RunNow(context.Background(), KindAlert, Person{Email: "bob"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	recheck := between(agents.task("prod"), "<recheck>", "</recheck>")
	if !strings.Contains(recheck, "C-3") || strings.Contains(recheck, "C-4") || strings.Contains(recheck, "C-2") {
		t.Errorf("the alert's re-check: %q", recheck)
	}
	in := router.last(t)
	if in.Scope == nil || len(in.Scope.Clusters) != 0 || fmt.Sprint(in.Scope.Objects) != "[{prod shop/Pod/web-7f9}]" {
		t.Fatalf("the scope: %+v", in.Scope)
	}
	if !in.Scope.Has("prod", "shop/pod/web-7f9") || in.Scope.Has("prod", "shop/Pod/api-1") || in.Scope.Has("prod", "") {
		t.Error("Has")
	}
}

// W-1: the daily digest has no scope: its task lists nothing to
// re-check, and the router is given none, so it verifies nothing.
func TestADigestHasNoScopeW1(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	router := &recheckRouter{}
	w.s.SetRouter(router)
	agents := &clusterAgents{text: map[string]string{"local": "Quiet day.\n\n```json\n{\"findings\": [], \"proposals\": []}\n```"}}
	w.s.SetAgents(agents)
	on(t, w.s, KindDigest, Change{})
	if _, err := w.s.RunNow(context.Background(), KindDigest, Person{Email: "bob"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	if strings.Contains(agents.task("local"), "<recheck>") {
		t.Error("the digest was given fixes to re-check")
	}
	if in := router.last(t); in.Scope != nil {
		t.Errorf("a digest's scope: %+v", in.Scope)
	}
}

// W-3: of 45 findings, the critical ones last, the run keeps 40 with
// every critical one, in the order they came; it says how many it left
// out and of which severity, and gives the router their keys.
func TestTheCapKeepsTheMostSevereW3(t *testing.T) {
	w := setup(t)
	router := &fakeRouter{}
	w.s.SetRouter(router)
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", Schedulable: true, Checks: AuditChecks,
		Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			var fs []Finding
			for i := range 40 {
				fs = append(fs, Finding{Severity: "info", Check: "pod.no-probes", Title: fmt.Sprintf("info %d", i), Cluster: "local", Resource: fmt.Sprintf("shop/Pod/p%d", i)})
			}
			for i := range 5 {
				fs = append(fs, Finding{Severity: "high", Check: "pod.privileged", Title: fmt.Sprintf("critical %d", i), Cluster: "local", Resource: fmt.Sprintf("shop/Pod/c%d", i)})
			}
			return Outcome{Findings: fs}, nil
		}})
	on(t, w.s, "test", Change{})
	ctx := context.Background()
	b, err := w.s.RunNow(ctx, "test", Person{Email: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	r, _ := w.s.GetRun(ctx, b.ID)
	if len(r.Findings) != maxFindings {
		t.Fatalf("kept %d", len(r.Findings))
	}
	critical := 0
	for _, f := range r.Findings {
		if f.Severity == FindingCritical {
			critical++
		}
	}
	if critical != 5 || r.Findings[0].Title != "info 0" || r.Findings[39].Title != "critical 4" || r.Findings[39].ID != b.ID+".40" {
		t.Errorf("kept %d critical; first %q, last %q %s", critical, r.Findings[0].Title, r.Findings[39].Title, r.Findings[39].ID)
	}
	if r.LeftOut == nil || r.LeftOut.Count != 5 || r.LeftOut.Severities.Info != 5 || r.LeftOut.Severities.Critical != 0 || r.LeftOut.Cap != maxFindings {
		t.Errorf("left out: %+v", r.LeftOut)
	}
	if !strings.Contains(r.Note, "5 findings were left out past the cap of 40 (5 info)") {
		t.Errorf("the note: %q", r.Note)
	}
	in := router.in[len(router.in)-1]
	if len(in.LeftOut) != 5 || in.LeftOut[0] != (Key{Cluster: "local", Object: "shop/Pod/p35", Check: "pod.no-probes"}) {
		t.Errorf("the keys left out: %+v", in.LeftOut)
	}
	if list := runs(t, w.s); list[0].LeftOut == nil || list[0].LeftOut.Count != 5 {
		t.Errorf("the list's row: %+v", list[0].LeftOut)
	}
}

// A re-check is about the cluster its answer was asked about, as a
// finding is (ADR-147): named by its id, its name or not at all it is
// that cluster's, by its id; one that names another is left out.
func TestARecheckIsAboutTheClusterAsked(t *testing.T) {
	yes := true
	in := []Recheck{
		{Check: "a", Resource: "shop/Pod/a", Still: &yes},
		{Check: "b", Resource: "shop/Pod/b", Cluster: " Production ", Still: &yes},
		{Check: "c", Resource: "shop/Pod/c", Cluster: "PROD", Still: &yes},
		{Check: "d", Resource: "shop/Pod/d", Cluster: "staging", Still: &yes},
	}
	out, others := withCluster(in, "prod", "Production")
	if others != 1 || len(out) != 3 {
		t.Fatalf("kept %d, left out %d: %+v", len(out), others, out)
	}
	for i, r := range out {
		if r.Cluster != "prod" || r.Check != in[i].Check {
			t.Errorf("re-check %d: %+v", i, r)
		}
	}
	if in[1].Cluster != " Production " {
		t.Error("the answer's own entries were changed")
	}
}

// P-2: the block's "rechecked" is read leniently: "still" as a boolean or
// as text, an entry of another shape is empty, and the rest of the block
// is read all the same.
func TestRecheckedIsReadLeniently(t *testing.T) {
	p := Parse("Done.\n\n```json\n" + `{"findings": [{"severity": "info", "title": "x"}], "proposals": [], "rechecked": [
 {"check": "pod.no-limits", "resource": "shop/Pod/web", "still": false, "evidence": "limits set"},
 {"check": "pod.no-probes", "resource": "shop/Pod/api", "still": "TRUE"},
 {"check": 4, "still": "maybe"},
 "odd"]}` + "\n```")
	if len(p.Findings) != 1 || len(p.Rechecked) != 4 {
		t.Fatalf("parsed: %+v", p)
	}
	if x := p.Rechecked[0]; x.Still == nil || *x.Still || x.Evidence != "limits set" || x.Resource != "shop/Pod/web" {
		t.Errorf("first: %+v", x)
	}
	if x := p.Rechecked[1]; x.Still == nil || !*x.Still {
		t.Errorf("second: %+v", x)
	}
	if p.Rechecked[2].Still != nil || p.Rechecked[2].Check != "" || p.Rechecked[3].Still != nil {
		t.Errorf("odd ones: %+v", p.Rechecked[2:])
	}
}

// between is the text between two marks, "" when they are not there.
func between(s, from, to string) string {
	_, rest, ok := strings.Cut(s, from)
	if !ok {
		return ""
	}
	in, _, _ := strings.Cut(rest, to)
	return in
}
