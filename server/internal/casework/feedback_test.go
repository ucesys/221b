package casework

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- The owner's feedback on Cases (Phase 12, wave 6c, CF-32 to CF-39) ---

// critical is a critical finding (P1) of a run, with its key.
func critical(check, object, title string) jobs.Finding {
	f := finding(check, object, title)
	f.Severity = jobs.FindingCritical
	return f
}

// routeAs routes one run of the nightly audit as the jobs do: with its
// agent and its lowest priority.
func (w *world) routeAs(t *testing.T, lowest *int, fs ...jobs.Finding) jobs.RouteResult {
	t.Helper()
	w.runs++
	run := fmt.Sprintf("run-%d", w.runs)
	for i := range fs {
		fs[i].ID = fmt.Sprintf("%s.%d", run, i+1)
	}
	return w.svc.Route(context.Background(), jobs.RouteInput{Run: run, Job: "nightly-audit", Kind: jobs.KindAudit, Label: "Nightly audit", Clusters: []string{"prod"}, OK: true,
		Findings: fs, Agent: "Auditor", MinPriority: lowest})
}

// gate holds the agent's answers until the test lets them go.
type gate struct {
	*fakeAgents
	open chan struct{}
}

func (g gate) Run(ctx context.Context, req jobs.AgentRequest) jobs.AgentResult {
	<-g.open
	return g.fakeAgents.Run(ctx, req)
}

func historyOf(v View) string {
	var b strings.Builder
	for _, e := range v.History {
		fmt.Fprintf(&b, "%s [%s/%s]\n", e.What, e.By.Name, e.By.Kind)
	}
	return b.String()
}

// CF-32: while the case analysis job is on, each new agent-found case at
// P0-P2 is queued for one analysis; a P3 case, a repeat, an excepted
// finding queue nothing; the case and its row say "queued". With the job
// off nothing is queued.
func TestNewAgentCasesAtP0ToP2AreQueuedCF32(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	res := w.routeAs(t, nil, critical("pod.privileged", "ci/DaemonSet/runner", "The runner is privileged"))
	if c := w.get(t, res.Findings[0].Case); c.AnalysisQueued || c.AnalysisState != "" {
		t.Fatalf("queued with the job off: %+v", c.Brief)
	}
	w.jobOn(t, true)
	res = w.routeAs(t, nil, critical("rbac.cluster-admin-binding", "ClusterRoleBinding/ci-admin", "ci has cluster-admin"), finding("pod.no-limits", "shop/Deployment/web", "web has no limits"),
		critical("pod.privileged", "ci/DaemonSet/runner", "The runner is privileged"))
	p1, p3, repeat := res.Findings[0], res.Findings[1], res.Findings[2]
	if repeat.Routing != jobs.RoutedSeen {
		t.Fatalf("the repeat: %+v", repeat)
	}
	list, err := w.svc.List(ctx, olivia, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	queued := map[string]bool{}
	for _, b := range list {
		if b.AnalysisState == "queued" && b.AnalysisQueued {
			queued[b.ID] = true
		}
	}
	if len(queued) != 1 || !queued[p1.Case] || queued[p3.Case] || queued[repeat.Case] {
		t.Fatalf("queued %v (P1 %s, P3 %s, repeat %s)", queued, p1.Case, p3.Case, repeat.Case)
	}
	b, _ := json.Marshal(w.get(t, p1.Case))
	if !strings.Contains(string(b), `"analysisQueued":true`) {
		t.Errorf("the case's view: %s", b)
	}
	if !strings.Contains(w.audit.String(), "1 queued for their analysis ["+p1.Case+"]") {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// CF-32: the leader runs the queue one analysis at a time, the highest
// priority first, then the oldest; a case closed or analysed by a person
// leaves it; with the day's budget spent the rest wait.
func TestTheQueueRunsOneAtATimeHighestFirstCF32(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.jobOn(t, true)
	g := gate{fakeAgents: w.agents, open: make(chan struct{})}
	w.jobs.SetAgents(g)
	a := w.routeAs(t, nil, critical("rbac.wildcard-rule", "ClusterRole/all", "A role grants everything")).Findings[0].Case
	w.now = w.now.Add(time.Minute)
	b := w.routeAs(t, nil, critical("rbac.pod-exec", "shop/Role/debug", "debug may exec")).Findings[0].Case
	w.now = w.now.Add(time.Minute)
	c := w.routeAs(t, nil, critical("rbac.secrets-read", "shop/Role/reader", "reader reads secrets")).Findings[0].Case
	w.now = w.now.Add(time.Minute)
	gone := w.routeAs(t, nil, critical("pod.root-user", "shop/Deployment/web", "web runs as root")).Findings[0].Case
	// b is raised to P0: it goes first.
	if _, err := w.svc.Update(ctx, b, Change{Priority: pr(0)}, olivia); err != nil {
		t.Fatal(err)
	}
	// One closed leaves the queue.
	if _, err := w.svc.Close(ctx, gone, CloseInput{Outcome: issues.OutcomeNoAction, Explanation: "It went away."}, olivia); err != nil {
		t.Fatal(err)
	}
	if w.get(t, gone).AnalysisQueued || w.report(t, gone).AnalysisQueued != nil {
		t.Fatal("a closed case stays queued")
	}
	w.svc.Tick(ctx)
	if v := w.get(t, b); !v.Analyzing || v.AnalysisQueued {
		t.Fatalf("P0 first: %+v", v.Brief)
	}
	// One at a time: the next tick starts nothing while it runs.
	w.svc.Tick(ctx)
	if w.get(t, a).Analyzing || !w.get(t, a).AnalysisQueued {
		t.Fatal("a second analysis started while the first ran")
	}
	close(g.open)
	w.svc.Wait()
	if v := w.get(t, b); v.Analyzing || v.Analysis == nil || !strings.Contains(historyOf(v), "Its turn in the queue came") {
		t.Fatalf("the analysis: %+v\n%s", v.Analysis, historyOf(v))
	}
	// A person analyses c: it leaves the queue.
	if _, err := w.svc.Analyze(ctx, c, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if w.get(t, c).AnalysisQueued {
		t.Fatal("analysed by a person and still queued")
	}
	// The day's budget is spent by now: a waits for tomorrow.
	day := jobs.Budget{RunUSD: 3, DayUSD: 0.01}
	if _, err := w.jobs.Update(ctx, jobs.KindCase, jobs.Change{Budget: &day}, jobs.Person{Name: "admin"}); err != nil {
		t.Fatal(err)
	}
	w.svc.Tick(ctx)
	w.svc.Wait()
	if v := w.get(t, a); v.Analyzing || !v.AnalysisQueued || v.Analysis != nil {
		t.Fatalf("ran past the day's budget: %+v", v.Brief)
	}
	// Paused, it waits too; on with budget, its turn comes.
	day.DayUSD = 0
	if _, err := w.jobs.Update(ctx, jobs.KindCase, jobs.Change{Budget: &day}, jobs.Person{Name: "admin"}); err != nil {
		t.Fatal(err)
	}
	w.jobOn(t, false)
	w.svc.Tick(ctx)
	w.svc.Wait()
	if !w.get(t, a).AnalysisQueued {
		t.Fatal("left the queue while the job was paused")
	}
	w.jobOn(t, true)
	w.svc.Tick(ctx)
	w.svc.Wait()
	if v := w.get(t, a); v.AnalysisQueued || v.Analysis == nil {
		t.Fatalf("its turn did not come: %+v", v.Brief)
	}
}

// CF-33: a finding whose priority is below the job's lowest opens no
// case, joins none, and stays on the run as ignored, counted.
func TestFindingsBelowTheLowestPriorityAreIgnoredCF33(t *testing.T) {
	w := setup(t)
	info := finding("pod.no-probes", "shop/Deployment/web", "web has no probes")
	info.Severity = jobs.FindingInfo
	lowest := 3
	res := w.routeAs(t, &lowest, info, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	if f := res.Findings[0]; f.Routing != jobs.RoutedIgnored || f.Case != "" || res.Routed.Ignored != 1 || res.Routed.Opened != 1 {
		t.Fatalf("P4 at lowest P3: %+v %+v", f, res.Routed)
	}
	// Not even a repeat of an open case joins it.
	open := w.routeAs(t, nil, info).Findings[0]
	lowest = 2
	res = w.routeAs(t, &lowest, info, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	if res.Routed.Ignored != 2 || w.get(t, open.Case).Seen.Count != 1 {
		t.Fatalf("lowest P2: %+v, seen %+v", res.Routed, w.get(t, open.Case).Seen)
	}
	lowest = 4
	if res = w.routeAs(t, &lowest, info); res.Findings[0].Routing != jobs.RoutedSeen {
		t.Fatalf("lowest P4: %+v", res.Findings[0])
	}
	if !strings.Contains(w.audit.String(), "2 ignored (lowest priority P2)") {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// CF-34: closing a case rejects each waiting change that resolves it and
// nothing else open, by whoever closed it, saying why; one that resolves
// another open case or an open known issue keeps waiting; a duplicate's
// waiting changes resolve its original instead, linked by the system.
func TestClosingACaseSettlesItsWaitingChangesCF34(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	st := w.withCards(t)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"), finding("pod.no-limits", "shop/Deployment/api", "api has no limits"))
	for _, title := range []string{"The shop is down", "The shop is down again"} {
		if _, err := w.svc.Create(ctx, NewCase{Title: title, Cluster: "prod"}, olivia); err != nil {
			t.Fatal(err)
		}
	}
	k, err := w.iss.SaveKnown(ctx, system, "", issues.KnownInput{Title: strPtr("Limits are set by hand")})
	if err != nil {
		t.Fatal(err)
	}
	only, both, withKI, applied := w.card("PC-1", cards.Queued, "C-1"), w.card("PC-2", cards.Queued, "C-1", "C-2"), w.card("PC-3", cards.Queued, "C-1", k.ID), w.card("PC-4", cards.Applied, "C-1")
	dup := w.card("PC-5", cards.Queued, "C-3")
	for _, c := range []*cards.Card{&only, &both, &withKI, &dup} {
		c.Expires = w.now.Add(time.Hour)
	}
	put(t, st, only, both, withKI, applied, dup)
	if _, err := w.svc.Close(ctx, "C-1", CloseInput{Outcome: issues.OutcomeNoAction, Explanation: "It went away."}, olivia); err != nil {
		t.Fatal(err)
	}
	got := func(id string) cards.Card {
		c, err := st.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := got("PC-1"); c.State != cards.Rejected || c.Rejection == nil || c.Rejection.By.Name != "Olivia" || c.Rejection.Comment != "Case C-1 was closed as no action needed" {
		t.Fatalf("the change resolving only C-1: %+v %+v", c.State, c.Rejection)
	}
	if c := got("PC-2"); c.State != cards.Queued {
		t.Errorf("the change resolving C-2 too: %s", c.State)
	}
	if c := got("PC-3"); c.State != cards.Queued {
		t.Errorf("the change resolving an open known issue too: %s", c.State)
	}
	if c := got("PC-4"); c.State != cards.Applied {
		t.Errorf("an applied change: %s", c.State)
	}
	// C-3 closes as a duplicate of C-4: its change resolves C-4 too.
	if _, err := w.svc.Close(ctx, "C-3", CloseInput{Outcome: issues.OutcomeDuplicate, Explanation: "Same.", Original: "C-4"}, otto); err != nil {
		t.Fatal(err)
	}
	if c := got("PC-5"); c.State != cards.Queued || fmt.Sprint(c.Resolves) != "[C-3 C-4]" || c.ResolvesBy["C-4"] != "System" {
		t.Fatalf("a duplicate's change: %s %v %v", c.State, c.Resolves, c.ResolvesBy)
	}
	if !strings.Contains(historyOf(w.get(t, "C-4")), "The change PC-5 now resolves this case too") {
		t.Errorf("the original's history:\n%s", historyOf(w.get(t, "C-4")))
	}
	// C-2 closes as fixed by a person: PC-2 resolves nothing open now.
	if _, err := w.svc.Fix(ctx, "C-2", FixInput{Text: "Set the limits."}, olivia); err != nil {
		t.Fatal(err)
	}
	if c := got("PC-2"); c.State != cards.Rejected || c.Rejection.Comment != "Case C-2 was closed as fixed" {
		t.Errorf("PC-2 after C-2 fixed: %s %+v", c.State, c.Rejection)
	}
	// A close the issue service makes itself is the system's.
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/db", "db has no limits"))
	last := w.card("PC-6", cards.Queued, "C-5")
	last.Expires = w.now.Add(time.Hour)
	put(t, st, last)
	status := issues.StatusClosed
	if _, err := w.iss.Triage(ctx, system, 5, issues.Triage{Status: &status, Outcome: issues.OutcomeNoAction, Explanation: "Gone."}); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if c := got("PC-6"); c.State != cards.Rejected || c.Rejection.By.Kind != cards.ActorSystem || c.Decider().Kind != cards.ActorSystem {
		t.Errorf("closed by the issue service: %s %+v", c.State, c.Rejection)
	}
}

func strPtr(s string) *string { return &s }

// CF-35: every change names its actor with its kind: a person, the agent
// that found the case, the system; on the history, and as the case's last
// change on its row.
func TestWhoDidItEverywhereCF35(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	res := w.routeAs(t, nil, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	id := res.Findings[0].Case
	c := w.get(t, id)
	agent := issues.Actor{Name: "Auditor", Kind: issues.ActorAgent}
	if c.CreatedBy == nil || *c.CreatedBy != agent || c.LastBy == nil || *c.LastBy != agent || c.LastAt == nil {
		t.Fatalf("an agent-found case: %+v %+v", c.CreatedBy, c.LastBy)
	}
	if _, err := w.svc.Update(ctx, id, Change{Priority: pr(2)}, olivia); err != nil {
		t.Fatal(err)
	}
	list, _ := w.svc.List(ctx, olivia, Filter{})
	if list[0].LastBy == nil || *list[0].LastBy != (issues.Actor{Name: "Olivia", Kind: issues.ActorPerson}) {
		t.Fatalf("the row's last change: %+v", list[0].LastBy)
	}
	if h := historyOf(w.get(t, id)); !strings.Contains(h, "Priority P3 → P2 [Olivia/person]") {
		t.Errorf("history:\n%s", h)
	}
	// The next run seeing it again is the agent's change.
	w.routeAs(t, nil, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	if c := w.get(t, id); c.LastBy == nil || *c.LastBy != agent {
		t.Errorf("seen again: %+v", c.LastBy)
	}
	// A case the system closes is the system's.
	st := w.withCards(t)
	w.applyTo(t, st, id)
	w.routeWith(t, "nightly-audit", true, []jobs.Recheck{recheck("pod.no-limits", "shop/Deployment/web", false, "limits set")})
	if c := w.get(t, id); c.Status != issues.StatusClosed || c.LastBy == nil || c.LastBy.Kind != issues.ActorSystem || !strings.Contains(historyOf(c), "[System/system]") {
		t.Errorf("closed by the system: %s %+v\n%s", c.Status, c.LastBy, historyOf(c))
	}
	// The JSON names.
	b, _ := json.Marshal(w.get(t, id))
	for _, want := range []string{`"createdBy":{"name":"Auditor","kind":"agent"}`, `"lastBy":{"name":"System","kind":"system"}`, `"by":{"name":"Olivia","kind":"person"}`, `"lastAt":"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %s in %s", want, b)
		}
	}
	// A history written before the kinds reads a name as a person.
	var e Event
	if err := json.Unmarshal([]byte(`{"at":"2026-11-06T10:00:00Z","what":"Closed","by":"Pat"}`), &e); err != nil || e.By != (issues.Actor{Name: "Pat", Kind: issues.ActorPerson}) {
		t.Errorf("an old line: %+v %v", e, err)
	}
}

// CF-36: an incident number, set at creation or later, cleared with "",
// 1 to 64 printable characters; matched by the search, audited, in the
// history; the reporter does not see it.
func TestIncidentNumbersCF36(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	v, err := w.svc.Create(ctx, NewCase{Title: "The shop is down", Incident: " INC0012345 "}, olivia)
	if err != nil || v.Incident != "INC0012345" {
		t.Fatalf("at creation: %q %v", v.Incident, err)
	}
	if list, _ := w.svc.List(ctx, olivia, Filter{Q: "inc0012345"}); len(list) != 1 {
		t.Errorf("the search finds %d", len(list))
	}
	inc := "INC0099"
	if v, err = w.svc.Update(ctx, v.ID, Change{Incident: &inc}, olivia); err != nil || v.Incident != inc || !strings.Contains(historyOf(v), "Incident number INC0012345 → INC0099") {
		t.Fatalf("changed: %q %v\n%s", v.Incident, err, historyOf(v))
	}
	if !strings.Contains(w.audit.String(), `"action":"case.incident"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
	none := ""
	if v, err = w.svc.Update(ctx, v.ID, Change{Incident: &none}, olivia); err != nil || v.Incident != "" {
		t.Fatalf("cleared: %q %v", v.Incident, err)
	}
	for _, bad := range []string{strings.Repeat("x", 65), "INC\x00001"} {
		if _, err := w.svc.Update(ctx, v.ID, Change{Incident: &bad}, olivia); err == nil {
			t.Errorf("%q taken", bad)
		}
		if _, err := w.svc.Create(ctx, NewCase{Title: "x", Incident: bad}, olivia); err == nil {
			t.Errorf("%q taken at creation", bad)
		}
	}
	// The issue API: support and tokens set it, a reporter may not, and a
	// reporter never sees it.
	if _, err := w.iss.Submit(ctx, rita, issues.Submission{Text: "Broken", Incident: "INC1"}); err == nil {
		t.Error("a reporter set an incident number")
	}
	tok := issues.Caller{Person: issues.Person{ID: "system:servicenow", Name: "ServiceNow", System: true}, Report: true, Scopes: []string{issues.ScopeOnBehalf}}
	rec, err := w.iss.Submit(ctx, tok, issues.Submission{Text: "The VPN drops", Incident: "INC7", Type: "availability", For: &issues.Person{ID: "rita", Name: "Rita"}})
	if err != nil || rec.Report.Incident != "INC7" || rec.Report.Type != issues.TypeAvailability {
		t.Fatalf("by a token: %+v %v", rec.Report, err)
	}
	h := w.iss.Handler(func(*http.Request) (issues.Caller, bool) { return rita, true })
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", fmt.Sprintf("%sreports/%d", issues.Prefix, rec.Report.Number), nil))
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "INC7") || !strings.Contains(rr.Body.String(), `"type":"availability"`) {
		t.Errorf("the reporter sees: %d %s", rr.Code, rr.Body.String())
	}
}

// CF-37: a case's type: an agent-found case's from its check, other for
// other.<slug>; New Case takes one (other by default), an alert's is an
// outage; changed by who works the cases, audited; a filter.
func TestCaseTypesCF37(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	res := w.routeAs(t, nil, critical("pod.privileged", "ci/DaemonSet/runner", "privileged"), finding("node.disk-pressure", "Node/node-2", "disk"),
		finding("cert.expiring", "shop/Certificate/tls", "tls"), finding("other.odd-thing", "", "odd"))
	for i, want := range []string{issues.TypeSecurity, issues.TypeCapacity, issues.TypeAvailability, issues.TypeOther} {
		if c := w.get(t, res.Findings[i].Case); c.Type != want {
			t.Errorf("%s: %s, want %s", res.Findings[i].Check, c.Type, want)
		}
	}
	v, err := w.svc.Create(ctx, NewCase{Title: "Plain"}, olivia)
	if err != nil || v.Type != issues.TypeOther {
		t.Fatalf("by hand: %q %v", v.Type, err)
	}
	a, err := w.svc.Create(ctx, NewCase{FromAlert: &AlertInput{Cluster: "prod", Name: "KubePodCrashLooping", Severity: "critical"}}, olivia)
	if err != nil || a.Type != issues.TypeOutage {
		t.Fatalf("from an alert: %q %v", a.Type, err)
	}
	q, err := w.svc.Create(ctx, NewCase{Title: "How do I", Type: "question"}, olivia)
	if err != nil || q.Type != issues.TypeQuestion {
		t.Fatalf("a question: %q %v", q.Type, err)
	}
	if _, err := w.svc.Create(ctx, NewCase{Title: "x", Type: "weird"}, olivia); err == nil {
		t.Error("a type that is none was taken")
	}
	perf := "performance"
	if v, err = w.svc.Update(ctx, v.ID, Change{Type: &perf}, olivia); err != nil || v.Type != perf || !strings.Contains(historyOf(v), "Type other → performance") {
		t.Fatalf("changed: %q %v", v.Type, err)
	}
	if !strings.Contains(w.audit.String(), `"action":"case.type"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
	if list, _ := w.svc.List(ctx, olivia, Filter{Type: issues.TypeSecurity}); len(list) != 1 || list[0].ID != res.Findings[0].Case {
		t.Errorf("type=security lists %+v", list)
	}
}

// CF-38: one action for many cases, each by its single action's code and
// rules, answered per case; at most 100; audited as cases.bulk.
func TestManyCasesAtOnceCF38(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	agentCase := res.Findings[0].Case
	for _, title := range []string{"One", "Two", "Three"} {
		if _, err := w.svc.Create(ctx, NewCase{Title: title}, olivia); err != nil {
			t.Fatal(err)
		}
	}
	out, err := w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-2", "C-3", "C-2"}, Action: BulkAssign, Assignee: &issues.Person{ID: "me"}}, olivia)
	if err != nil || len(out) != 2 || !out[0].OK || !out[1].OK || w.get(t, "C-3").Assignee == nil {
		t.Fatalf("assign: %+v %v", out, err)
	}
	// No action needed closes a person's case and an agent-found one
	// alike; notAProblem, its name before CF-90, is taken as it.
	out, err = w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-2", agentCase}, Action: BulkClose, Outcome: "notAProblem", Explanation: "Expected."}, olivia)
	if err != nil || !out[0].OK || !out[1].OK || w.get(t, "C-2").Outcome != issues.OutcomeNoAction || w.get(t, agentCase).Outcome != issues.OutcomeNoAction {
		t.Fatalf("close: %+v %v", out, err)
	}
	// A duplicate of a closed original is refused with what fits instead.
	out, _ = w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-3"}, Action: BulkDuplicate, Explanation: "Same.", Original: "C-2"}, olivia)
	if out[0].OK || len(out[0].Offers) == 0 || !strings.Contains(out[0].Error, "Not a duplicate") {
		t.Fatalf("duplicate of a closed case: %+v", out)
	}
	sec := issues.TypeSecurity
	if out, _ = w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-3", "C-4"}, Action: BulkType, Type: &sec}, olivia); !out[0].OK || w.get(t, "C-4").Type != sec {
		t.Fatalf("type: %+v", out)
	}
	// A known issue made once, linked to the rest.
	out, _ = w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-3", "C-4"}, Action: BulkKnown, Create: &NewKnown{Title: "Limits by hand"}}, olivia)
	if !out[0].OK || !out[1].OK || w.get(t, "C-3").KnownIssue == "" || w.get(t, "C-3").KnownIssue != w.get(t, "C-4").KnownIssue {
		t.Fatalf("known: %+v %s %s", out, w.get(t, "C-3").KnownIssue, w.get(t, "C-4").KnownIssue)
	}
	if _, err := w.svc.Bulk(ctx, BulkInput{Action: BulkAssign}, olivia); err == nil {
		t.Error("no cases taken")
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("C-%d", i+1)
	}
	if _, err := w.svc.Bulk(ctx, BulkInput{Cases: many, Action: BulkAnalyze}, olivia); err == nil {
		t.Error("101 cases taken")
	}
	if _, err := w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-2"}, Action: "explode"}, olivia); err == nil {
		t.Error("an action that is none taken")
	}
	if !strings.Contains(w.audit.String(), `"action":"cases.bulk"`) || !strings.Contains(w.audit.String(), "close on 2 cases: 2 done, 0 refused") {
		t.Errorf("audit: %s", w.audit.String())
	}
	// Analyse queues each for the job, which runs them in turn.
	w.jobOn(t, true)
	if _, err := w.svc.Create(ctx, NewCase{Title: "Five", Cluster: "prod"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	out, _ = w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-5", "C-2"}, Action: BulkAnalyze}, olivia)
	if !out[0].OK || out[1].OK || !w.get(t, "C-5").AnalysisQueued {
		t.Fatalf("analyse: %+v", out)
	}
	// Through HTTP: 200 with the results, 400 for none.
	mux := http.NewServeMux()
	for _, rt := range w.svc.Routes(func(*http.Request) (Caller, bool) { return olivia, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	do := func(body string) (int, string) {
		req := httptest.NewRequest("POST", Prefix+"/bulk", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := do(`{"cases": ["C-5"], "action": "priority", "priority": 1}`); code != http.StatusOK || !strings.Contains(body, `"results":[{"id":"C-5","ok":true}]`) {
		t.Errorf("HTTP: %d %s", code, body)
	}
	if code, _ := do(`{"cases": [], "action": "priority", "priority": 1}`); code != http.StatusBadRequest {
		t.Errorf("HTTP, none: %d", code)
	}
}

// CF-39: the titles of what a change resolves.
func TestTitlesOfWhatAChangeResolvesCF39(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	k, err := w.iss.SaveKnown(ctx, system, "", issues.KnownInput{Title: strPtr("Certificates renewed by hand")})
	if err != nil {
		t.Fatal(err)
	}
	got := w.svc.Titles(ctx, []string{"C-1", k.ID, "C-99"})
	if len(got) != 2 || got["C-1"] != "web has no limits" || got[k.ID] != "Certificates renewed by hand" {
		t.Errorf("titles %v", got)
	}
}

// --- end of the owner's feedback.

// Many cases fixed at once with one RCA keep it each, naming the others
// closed with it (CF-44); an agent-found case may be a duplicate of an
// open one (CF-42).
func TestOneRCAForManyCasesAndDuplicatesOfAgentCasesCF42(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	res := w.route(t, "nightly-audit", true,
		finding("pod.no-limits", "shop/Deployment/web", "web has no limits"),
		finding("pod.no-probes", "shop/Deployment/web", "web has no probes"),
		finding("image.latest-tag", "shop/Deployment/api", "api runs a latest tag"))
	a, b, d := res.Findings[0].Case, res.Findings[1].Case, res.Findings[2].Case
	// A person sees two agent-found cases are one problem.
	out, err := w.svc.Bulk(ctx, BulkInput{Cases: []string{b}, Action: BulkDuplicate, Explanation: "The same deployment, one fix.", Original: a}, olivia)
	if err != nil || !out[0].OK || w.get(t, b).DuplicateOf == "" {
		t.Fatalf("an agent-found duplicate: %+v %v", out, err)
	}
	out, err = w.svc.Bulk(ctx, BulkInput{Cases: []string{a, d}, Action: BulkFix, Text: "Set limits and pinned the image.", RCA: &Completion{Cause: "The chart had no defaults.", Prevention: "Defaults in the chart."}}, olivia)
	if err != nil || !out[0].OK || !out[1].OK {
		t.Fatalf("fix: %+v %v", out, err)
	}
	ra, rd := w.get(t, a).RCADetail, w.get(t, d).RCADetail
	if ra == nil || rd == nil || ra.Cause != "The chart had no defaults." || rd.Cause != ra.Cause || ra.Fix != "Set limits and pinned the image." || ra.Case != a || rd.Case != d {
		t.Fatalf("one RCA on both: %+v %+v", ra, rd)
	}
	if fmt.Sprint(ra.Cases) != "["+d+"]" || fmt.Sprint(rd.Cases) != "["+a+"]" {
		t.Fatalf("closed together: %v %v", ra.Cases, rd.Cases)
	}
	// One fixed alone names nobody.
	out, err = w.svc.Bulk(ctx, BulkInput{Cases: []string{b, "C-99"}, Action: BulkFix, Text: "x", RCA: &Completion{Cause: "y"}}, olivia)
	if err != nil || out[0].OK || out[1].OK {
		t.Fatalf("a duplicate and a case not there: %+v %v", out, err)
	}
}
