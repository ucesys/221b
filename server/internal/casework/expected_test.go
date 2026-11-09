package casework

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- Alerts that are not a problem (Phase 12, wave 6, CF-82 to CF-84) ---

// keptAcks keeps the jobs' acknowledgements as the settings would.
type keptAcks struct {
	mu  sync.Mutex
	got map[string]jobs.AlertAck
}

func (k *keptAcks) Ack(_ context.Context, a jobs.AlertAck) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.got == nil {
		k.got = map[string]jobs.AlertAck{}
	}
	k.got[a.Key] = a
	return true, nil
}

func (k *keptAcks) of(key string) (jobs.AlertAck, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	a, ok := k.got[key]
	return a, ok
}

// concludingJob registers an alert diagnosis job, routed into the cases,
// that concludes on each alert as its name says: asleep… is expected, info
// below the job's lowest priority, excepted covered by an exception,
// invalid names no check of its catalogue, failed fails, none finds
// nothing; any other is a crash loop that opens a case.
func (w *world) concludingJob(t *testing.T) {
	t.Helper()
	w.jobs.SetRouter(w.svc)
	w.jobs.Kinds().Register(jobs.Kind{ID: jobs.KindAlert, Label: "Alert diagnosis", Agent: "troubleshooter", AgentName: "Troubleshooter", Events: []string{jobs.EventAlert}, Checks: jobs.AlertChecks,
		Run: func(_ context.Context, env *jobs.RunEnv) (jobs.Outcome, error) {
			a := env.Event.Data.(jobs.Alert)
			f := jobs.Finding{Severity: jobs.FindingCritical, Check: "pod.crashloop", Title: a.Name + " restarts", Cluster: a.Cluster, Resource: a.CaseObject()}
			switch {
			case strings.HasPrefix(a.Name, "asleep"):
				f.Check, f.Expected, f.ExpectedWhy = "node.not-ready", true, "Its node is annotated nodepower/status: asleep and was cordoned before it stopped reporting."
			case a.Name == "info":
				f.Severity = jobs.FindingInfo
			case a.Name == "excepted":
				f.Check = "pod.oomkilled"
			case a.Name == "invalid":
				f.Check = "no.such-check"
			case a.Name == "failed":
				return jobs.Outcome{}, errors.New("the provider is down")
			case a.Name == "none":
				return jobs.Outcome{}, nil
			}
			return jobs.Outcome{Findings: []jobs.Finding{f}}, nil
		}})
	yes := true
	if _, err := w.jobs.Update(context.Background(), jobs.KindAlert, jobs.Change{On: &yes, When: &jobs.When{Event: jobs.EventAlert, Severity: jobs.SeverityWarning}}, jobs.Person{Name: "admin"}); err != nil {
		t.Fatal(err)
	}
}

func criticalAlert(name string) jobs.Alert {
	return jobs.Alert{Cluster: "prod", Key: "pod:shop/" + name, Kind: "pod", Severity: jobs.SeverityError, Name: name, Namespace: "shop", Status: "Error", Fingerprint: "failed|Error"}
}

// The safety net opens a case only when nobody concluded (CF-78 since
// CF-82): it leaves an alert a diagnosis run concluded on as it is (the
// agent found it expected, it is below the job's lowest priority, an
// exception covers it, or its finding reached a case a person has closed
// since) and one a job acknowledged; it still opens one for an alert whose
// run failed, found nothing, or found only what is not valid, or that no
// run diagnosed as it is now. The run's expected finding opens no case and
// joins none, whatever its severity, counted as expected; and the job
// acknowledges what it concluded on: the exception, or expected with the
// agent's reason (none below the lowest priority).
func TestTheSafetyNetLeavesWhatADiagnosisConcludedCF82(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.concludingJob(t)
	acker := &keptAcks{}
	w.jobs.SetAcker(acker)
	if _, err := w.lessons.AddException(ctx, lessons.NewException{Cluster: "prod", Resource: "shop/Pod/excepted", Check: "pod.oomkilled", Title: "excepted is OOMKilled", Reason: "a test app"}, lessons.Person{Name: "Olivia"}); err != nil {
		t.Fatal(err)
	}
	runs := map[string]jobs.Run{}
	var list []jobs.Alert
	for _, name := range []string{"asleep", "info", "excepted", "crash", "failed", "none", "invalid"} {
		a := criticalAlert(name)
		r, _, _ := w.jobs.RunEvent(ctx, jobs.KindAlert, jobs.Event{Kind: jobs.EventAlert, Cluster: "prod", Key: "alert:prod/" + a.Key, Severity: jobs.SeverityError, Label: "Alert " + name, Data: a})
		runs[name] = r
		list = append(list, a)
	}
	// The expected finding: no case, none joined, counted, its reason kept.
	r := runs["asleep"]
	if f := r.Findings[0]; f.Routing != jobs.RoutedExpected || f.Case != "" || !f.Expected || !strings.Contains(f.ExpectedWhy, "asleep") || r.Routed.Expected != 1 || r.Routed.Opened != 0 || r.Fingerprint != "failed|Error" {
		t.Fatalf("the expected finding %+v %+v", f, r.Routed)
	}
	if f := runs["info"].Findings[0]; f.Routing != jobs.RoutedIgnored {
		t.Fatalf("the info finding %+v", f)
	}
	if f := runs["excepted"].Findings[0]; f.Routing != jobs.RoutedExcepted || f.Exception != "EX-1" {
		t.Fatalf("the excepted finding %+v", f)
	}
	if f := runs["crash"].Findings[0]; f.Routing != jobs.RoutedOpened || f.Case != "C-1" {
		t.Fatalf("the crash's finding %+v", f)
	}
	if !strings.Contains(w.audit.String(), "1 expected") {
		t.Errorf("the routing's audit says nothing of the expected: %s", w.audit.String())
	}
	// What the job acknowledged.
	if a, ok := acker.of("pod:shop/asleep"); !ok || !a.Expected || !strings.Contains(a.Why, "nodepower/status: asleep") || a.Run != runs["asleep"].ID || a.Case != "" {
		t.Errorf("the expected alert's acknowledgement %+v", a)
	}
	if a, ok := acker.of("pod:shop/info"); !ok || !a.Expected || a.Why != "" || a.Run != runs["info"].ID {
		t.Errorf("the ignored alert's acknowledgement %+v", a)
	}
	if a, ok := acker.of("pod:shop/excepted"); !ok || a.Exception != "EX-1" || a.Expected || a.Case != "" {
		t.Errorf("the excepted alert's acknowledgement %+v", a)
	}
	if a, ok := acker.of("pod:shop/crash"); !ok || a.Case != "C-1" {
		t.Errorf("the crash's acknowledgement %+v", a)
	}
	for _, name := range []string{"failed", "none", "invalid"} {
		if a, ok := acker.of("pod:shop/" + name); ok {
			t.Errorf("%s was acknowledged: %+v", name, a)
		}
	}
	if !strings.Contains(w.audit.String(), "diagnosed as expected: Its node is annotated nodepower/status: asleep") || !strings.Contains(w.audit.String(), "covered by exception EX-1") {
		t.Errorf("the acknowledgements are not audited: %s", w.audit.String())
	}
	// A person closes the crash's case while its alert goes on.
	if _, err := w.svc.Close(ctx, "C-1", CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "It is a test pod."}, olivia); err != nil {
		t.Fatal(err)
	}
	// A job acknowledged one that no run kept here; another no run saw as
	// it is now: it changed since.
	acked := criticalAlert("acked")
	acked.JobAck = true
	changed := criticalAlert("asleep")
	changed.Key, changed.Name, changed.Status, changed.Fingerprint = "pod:shop/asleep-2", "asleep-2", "Unknown", "failed|Unknown"
	if _, _, err := w.jobs.RunEvent(ctx, jobs.KindAlert, jobs.Event{Kind: jobs.EventAlert, Cluster: "prod", Key: "alert:prod/pod:shop/asleep-2", Severity: jobs.SeverityError, Label: "Alert", Data: criticalAlert("asleep-2")}); err != nil {
		t.Fatal(err)
	}
	list = append(list, acked, changed)
	for range 16 {
		w.svc.WatchAlerts(ctx, func() []jobs.Alert { return list })
		w.now = w.now.Add(time.Minute)
	}
	lines, _ := w.iss.Index(ctx)
	var opened []string
	for _, l := range lines {
		if l.Number > 1 {
			opened = append(opened, l.Title)
		}
	}
	if len(opened) != 4 {
		t.Fatalf("the safety net opened %d cases: %q", len(opened), opened)
	}
	for _, name := range []string{"failed", "none", "invalid", "asleep-2"} {
		found := false
		for _, title := range opened {
			found = found || strings.HasSuffix(title, "(shop/Pod/"+name+")")
		}
		if !found {
			t.Errorf("no case for %s: %q", name, opened)
		}
	}
}

// An alert is named by its reason (CF-84), where a case is opened from it:
// the safety net's case and an alert's New Case keep the reason and the
// alert's key, the title "<reason>: <object>".
func TestTheAlertsNameCF84(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	canal := jobs.Alert{Cluster: "prod", Key: "pod:kube-system/rke2-canal-n6fkh", Kind: "pod", Severity: jobs.SeverityError, Name: "rke2-canal-n6fkh", Namespace: "kube-system", Status: "Unknown", Node: "r620-worker", Fingerprint: "failed|Unknown"}
	id, err := w.svc.openForAlert(ctx, canal, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c := w.get(t, id)
	if c.Title != "Pod status Unknown: node r620-worker stopped reporting (kube-system/Pod/rke2-canal-n6fkh)" || c.Source.Alert != "Pod status Unknown: node r620-worker stopped reporting" ||
		c.Source.AlertKey != "pod:kube-system/rke2-canal-n6fkh" || !strings.Contains(c.History[0].What, "the critical alert Pod status Unknown: node r620-worker stopped reporting was still going") {
		t.Fatalf("the safety net's case %+v %+v", c.Brief, c.History)
	}
	v, err := w.svc.Create(ctx, NewCase{FromAlert: &AlertInput{Cluster: "prod", Key: "node:n1", Name: "Node not ready: kubelet stopped reporting", Severity: "critical", Object: "Node/n1"}}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Node not ready: kubelet stopped reporting (Node/n1)" || v.Source.AlertKey != "node:n1" || !strings.Contains(v.History[0].What, "Opened from the alert Node not ready: kubelet stopped reporting") {
		t.Fatalf("New Case from an alert %+v %+v", v.Brief, v.History)
	}
}

const expectedAnswer = `{"summary": "No change needed: the node sleeps on purpose.", "cause": "", "evidence": ["nodepower/status: asleep"], "fix": {"summary": "", "commands": []}, "proposals": [],
 "expected": true, "expectedWhy": "The node is annotated nodepower/status: asleep and was cordoned before it stopped reporting."}`

// The case analysis lowers the priority when it finds no problem (CF-83):
// a case the console or an agent opened at P0-P2 goes down to P3, with a
// line in its history and the audit log, its verdict kept on the analysis;
// never a case a person opened, never one whose priority a person set,
// never without the structured verdict and its reason, never up.
func TestTheAnalysisLowersThePriorityCF83(t *testing.T) {
	w := setup(t)
	w.jobOn(t, true)
	ctx := context.Background()
	w.agents.answer = expectedAnswer
	// The safety net's case, P0.
	net, err := w.svc.openForAlert(ctx, criticalAlert("canal"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	c := w.get(t, net)
	if c.Priority != 3 || c.Analysis == nil || !c.Analysis.Expected || !strings.Contains(c.Analysis.ExpectedWhy, "asleep") || c.Analysis.Summary != "No change needed: the node sleeps on purpose." {
		t.Fatalf("the safety net's case after its analysis %+v %+v", c.Brief, c.Analysis)
	}
	last := c.History[len(c.History)-1].What
	if !strings.HasPrefix(last, "Priority P0 → P3 by the case analysis: The node is annotated nodepower/status: asleep") || !strings.HasSuffix(last, "("+c.Analysis.Run+")") {
		t.Errorf("the history says %q", last)
	}
	if r := w.report(t, net); r.PriorityBy != "analysis" {
		t.Errorf("set by %q", r.PriorityBy)
	}
	if out := w.audit.String(); !strings.Contains(out, `"name":"case analysis"`) || !strings.Contains(out, "case "+net+": Priority P0 → P3 by the case analysis") {
		t.Errorf("not audited: %s", out)
	}
	// An agent's case, P1 from a critical finding, analysed again.
	f := finding("pod.crashloop", "shop/Pod/web-1", "web-1 restarts")
	f.Severity = jobs.FindingCritical
	res := w.route(t, "alert-diagnosis", true, f)
	agent := res.Findings[0].Case
	if _, err := w.svc.Analyze(ctx, agent, sam); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if c := w.get(t, agent); c.Priority != 3 {
		t.Errorf("the agent's case is P%d", c.Priority)
	}
	// A person's case from an alert stays P0.
	mine, err := w.svc.Create(ctx, NewCase{FromAlert: &AlertInput{Cluster: "prod", Key: "pod:shop/api-1", Name: "Error", Severity: "critical", Object: "shop/Pod/api-1"}}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if c := w.get(t, mine.ID); c.Priority != 0 || c.Analysis == nil || !c.Analysis.Expected {
		t.Errorf("a person's case %+v %+v", c.Brief, c.Analysis)
	}
	// The console's case whose priority a person set stays as they set it.
	w.agents.answer = fixAnswer
	other, err := w.svc.openForAlert(ctx, criticalAlert("db-0"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if c := w.get(t, other); c.Priority != 0 {
		t.Fatalf("an analysis that found a problem lowered it: P%d", c.Priority)
	}
	if _, err := w.svc.Update(ctx, other, Change{Priority: pr(1)}, olivia); err != nil {
		t.Fatal(err)
	}
	w.agents.answer = expectedAnswer
	if _, err := w.svc.Analyze(ctx, other, sam); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if c := w.get(t, other); c.Priority != 1 {
		t.Errorf("a priority a person set was changed: P%d", c.Priority)
	}
	// A verdict without its reason is no verdict.
	w.agents.answer = `{"summary": "Fine.", "cause": "", "evidence": [], "fix": {"summary": "", "commands": []}, "proposals": [], "expected": true, "expectedWhy": " "}`
	bare, err := w.svc.openForAlert(ctx, criticalAlert("web-9"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if c := w.get(t, bare); c.Priority != 0 || c.Analysis == nil || c.Analysis.Expected {
		t.Errorf("a verdict without a reason %+v %+v", c.Brief, c.Analysis)
	}
	// Never up: an agent's warning, P3, stays P3, and nothing is said.
	w.agents.answer = expectedAnswer
	low := w.route(t, "alert-diagnosis", true, finding("pod.pending", "shop/Pod/web-2", "web-2 waits")).Findings[0].Case
	if _, err := w.svc.Analyze(ctx, low, sam); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	c = w.get(t, low)
	if c.Priority != 3 || strings.Contains(c.History[len(c.History)-1].What, "Priority") {
		t.Errorf("a P3 case %+v %+v", c.Brief, c.History)
	}
}
