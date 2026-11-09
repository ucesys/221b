package casework

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- Alerts and cases (Phase 12, wave 6, CF-77, CF-78) ---

// alertJob registers an alert diagnosis job that finds a crashloop on
// shop/Deployment/web whatever the alert, routed into the cases.
func (w *world) alertJob(t *testing.T) {
	t.Helper()
	w.jobs.SetRouter(w.svc)
	w.jobs.Kinds().Register(jobs.Kind{ID: jobs.KindAlert, Label: "Alert diagnosis", Agent: "troubleshooter", AgentName: "Troubleshooter", Events: []string{jobs.EventAlert}, Checks: jobs.AlertChecks,
		Run: func(context.Context, *jobs.RunEnv) (jobs.Outcome, error) {
			return jobs.Outcome{Findings: []jobs.Finding{{Severity: jobs.FindingCritical, Check: "pod.crashloop", Title: "web restarts", Cluster: "prod", Resource: "shop/Deployment/web"}}}, nil
		}})
	yes := true
	if _, err := w.jobs.Update(context.Background(), jobs.KindAlert, jobs.Change{On: &yes, When: &jobs.When{Event: jobs.EventAlert, Severity: jobs.SeverityWarning}}, jobs.Person{Name: "admin"}); err != nil {
		t.Fatal(err)
	}
}

func numbers(list []CoverCase) []int {
	var out []int
	for _, c := range list {
		out = append(out, c.Number)
	}
	return out
}

// An open case covers an alert when it is on the alert's cluster about
// its object, the object written any way the cases read, or when the
// alert's own diagnosis run sent its findings to it; the newest first. A
// closed case, one on another cluster or about another object covers
// nothing. Served to those who read the cases (CF-77).
func TestTheCasesThatCoverAnAlertCF77(t *testing.T) {
	w := setup(t)
	w.alertJob(t)
	ctx := context.Background()
	mk := func(cluster, resource string) string {
		t.Helper()
		v, err := w.svc.Create(ctx, NewCase{Title: "About " + resource, Cluster: cluster, Resource: resource}, olivia)
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	mk("prod", "shop/Pod/web-1")      // C-1
	node := mk("prod", "Node/n1")     // C-2, closed below
	mk("local", "shop/Pod/web-1")     // C-3, another cluster
	mk("prod", "shop/Deployment/web") // C-4, not what an alert is about
	if _, err := w.svc.Close(ctx, node, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "It came back."}, olivia); err != nil {
		t.Fatal(err)
	}
	// The alert's run sends its finding to a new agent-found case, C-5.
	a := jobs.Alert{Cluster: "prod", Key: "pod:shop/web-2", Kind: "pod", Severity: jobs.SeverityError, Name: "web-2", Namespace: "shop", Status: "CrashLoopBackOff", Fingerprint: "failed|CrashLoopBackOff"}
	r, _, err := w.jobs.RunEvent(ctx, jobs.KindAlert, jobs.Event{Kind: jobs.EventAlert, Cluster: "prod", Key: "alert:prod/pod:shop/web-2", Severity: jobs.SeverityError, Label: "Alert", Data: a})
	if err != nil || len(r.Findings) != 1 || r.Findings[0].Case != "C-5" {
		t.Fatalf("the alert's run %+v %v", r.Findings, err)
	}
	// The finding stays P1 (CF-15): only the safety net's case is P0.
	if c := w.get(t, "C-5"); c.Priority != 1 {
		t.Errorf("the agent's finding of a critical alert is P%d", c.Priority)
	}
	mk("prod", "shop/pod/web-1") // C-6, the same object written another way
	covers, err := w.svc.AlertCovers(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(covers.For("pod:shop/web-1", "shop/Pod/web-1")); len(got) != 2 || got[0] != 6 || got[1] != 1 {
		t.Errorf("web-1 is covered by %v", got)
	}
	if got := numbers(covers.For("pod:shop/web-2", "shop/Pod/web-2")); len(got) != 1 || got[0] != 5 {
		t.Errorf("web-2 is covered by %v", got)
	}
	if got := covers.For("node:n1", "Node/n1"); len(got) != 0 {
		t.Errorf("a closed case covers n1: %v", got)
	}
	if _, ok := covers.Objects["shop/deployment/web"]; ok {
		t.Error("a case about a deployment is listed for the alerts")
	}
	if got := covers.For("pod:shop/web-1", "shop/Pod/web-1"); got[0].ID != "C-6" || got[0].Title != "About shop/pod/web-1" || got[0].Priority != 2 {
		t.Errorf("the newest %+v", got[0])
	}
	// Through HTTP, to those who read the cases.
	caller, signed := olivia, true
	mux := http.NewServeMux()
	for _, rt := range w.svc.Routes(func(*http.Request) (Caller, bool) { return caller, signed }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	get := func(path string) (int, AlertCovers) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out AlertCovers
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := get(AlertsPath + "?cluster=prod"); code != http.StatusOK || len(out.Alerts["pod:shop/web-2"]) != 1 || len(out.Objects["shop/pod/web-1"]) != 2 {
		t.Errorf("served %d %+v", code, out)
	}
	if code, _ := get(AlertsPath); code != http.StatusBadRequest {
		t.Errorf("no cluster: %d", code)
	}
	caller = Caller{Person: Person{Name: "Nobody"}, ID: "nobody"}
	if code, _ := get(AlertsPath + "?cluster=prod"); code != http.StatusForbidden {
		t.Errorf("one who does not read the cases: %d", code)
	}
}

// The safety net (CF-78): a critical alert still going after Settings ›
// Cases' minutes opens a case like its New Case, P0, an outage from the
// alert, opened by the console and audited; not before, not for a
// warning, not when an open case covers it, not twice while it goes on,
// not for an alert a person acknowledged (the jobs leave it out of what
// they give, and its time starts again), and never when the minutes are 0.
func TestTheSafetyNetCF78(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if _, err := w.svc.Create(ctx, NewCase{Title: "api is down", Cluster: "prod", Resource: "shop/Pod/api-1"}, olivia); err != nil {
		t.Fatal(err)
	}
	web := jobs.Alert{Cluster: "prod", Key: "pod:shop/web-1", Kind: "pod", Severity: jobs.SeverityError, Name: "web-1", Namespace: "shop", Status: "CrashLoopBackOff", Message: "back-off restarting failed container", Fingerprint: "failed|CrashLoopBackOff"}
	api := jobs.Alert{Cluster: "prod", Key: "pod:shop/api-1", Kind: "pod", Severity: jobs.SeverityError, Name: "api-1", Namespace: "shop", Status: "Error", Fingerprint: "failed|Error"}
	node := jobs.Alert{Cluster: "prod", Key: "node:n2", Kind: "node", Severity: jobs.SeverityWarning, Name: "n2", Status: "DiskPressure", Fingerprint: "warning|DiskPressure"}
	list := []jobs.Alert{web, api, node}
	look := func(minutes int) {
		for range minutes {
			w.svc.WatchAlerts(ctx, func() []jobs.Alert { return list })
			w.now = w.now.Add(time.Minute)
		}
	}
	count := func() int {
		lines, _ := w.iss.Index(ctx)
		return len(lines)
	}
	look(15)
	if n := count(); n != 1 {
		t.Fatalf("a case was opened before 15 minutes: %d", n)
	}
	look(1)
	if n := count(); n != 2 {
		t.Fatalf("no case after 15 minutes: %d", n)
	}
	c := w.get(t, "C-2")
	if c.Priority != 0 || c.Type != issues.TypeOutage || c.Source.Kind != SourceAlert || c.Cluster != "prod" || c.Resource != "shop/Pod/web-1" || c.Title != "CrashLoopBackOff: back-off restarting failed container (shop/Pod/web-1)" || c.Source.AlertKey != "pod:shop/web-1" ||
		c.Detail != "back-off restarting failed container" || c.CreatedBy == nil || c.CreatedBy.Kind != issues.ActorSystem || !strings.Contains(c.History[0].What, "Opened by the console: the critical alert CrashLoopBackOff: back-off restarting failed container was still going after 15 minutes") {
		t.Fatalf("the safety net's case %+v %+v", c.Brief, c.History)
	}
	if out := w.audit.String(); !strings.Contains(out, `"action":"case.create"`) || !strings.Contains(out, `"name":"alert safety net"`) || !strings.Contains(out, "case C-2 opened by the console from the alert CrashLoopBackOff: back-off restarting failed container, critical for 15 minutes") {
		t.Fatalf("not audited: %s", out)
	}
	// Not twice while it goes on, even once its case is closed.
	if _, err := w.svc.Close(ctx, "C-2", CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "Watching it."}, olivia); err != nil {
		t.Fatal(err)
	}
	look(30)
	if n := count(); n != 2 {
		t.Fatalf("a second case for the same alert, or one for a warning or a covered alert: %d", n)
	}
	// It clears and comes back: a new occurrence, after its own minutes.
	list = []jobs.Alert{api, node}
	look(2)
	list = []jobs.Alert{web, api, node}
	look(15)
	if n := count(); n != 2 {
		t.Fatalf("too soon after it came back: %d", n)
	}
	look(1)
	if n := count(); n != 3 {
		t.Fatalf("the alert that came back opened no case: %d", n)
	}
	// A person acknowledges one: the jobs leave it out, and its time starts
	// again once the acknowledgement lapses.
	db := jobs.Alert{Cluster: "prod", Key: "pod:shop/db-0", Kind: "pod", Severity: jobs.SeverityError, Name: "db-0", Namespace: "shop", Status: "Error", Fingerprint: "failed|Error"}
	list = []jobs.Alert{web, api, node, db}
	look(10)
	list = []jobs.Alert{web, api, node}
	look(20)
	list = []jobs.Alert{web, api, node, db}
	look(10)
	if n := count(); n != 3 {
		t.Fatalf("an alert a person acknowledged opened a case: %d", n)
	}
	// 0 turns it off.
	set := DefaultSettings()
	zero := 0
	set.CriticalAlertMinutes = &zero
	if _, err := w.svc.SaveSettings(ctx, set, sam); err != nil {
		t.Fatal(err)
	}
	look(60)
	if n := count(); n != 3 {
		t.Fatalf("a case was opened with the safety net off: %d", n)
	}
	if got, _ := w.svc.Settings(ctx); got.CriticalAlertMinutes == nil || *got.CriticalAlertMinutes != 0 || got.AlertAfter() != 0 {
		t.Fatalf("off: %+v", got)
	}
}

// Settings › Cases' minutes for the safety net (CF-78): 15 by default,
// 0 to 1440, kept when settings are sent without them, and audited.
func TestTheSafetyNetsSettingCF78(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if got, _ := w.svc.Settings(ctx); got.CriticalAlertMinutes == nil || *got.CriticalAlertMinutes != 15 || got.AlertAfter() != 15*time.Minute {
		t.Fatalf("the default %+v", got)
	}
	for _, bad := range []int{-1, 1441} {
		s := DefaultSettings()
		s.CriticalAlertMinutes = &bad
		if _, err := w.svc.SaveSettings(ctx, s, sam); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d minutes: %v", bad, err)
		}
	}
	s := DefaultSettings()
	m := 30
	s.CriticalAlertMinutes = &m
	if got, err := w.svc.SaveSettings(ctx, s, sam); err != nil || *got.CriticalAlertMinutes != 30 {
		t.Fatalf("saved %+v %v", got, err)
	}
	if !strings.Contains(w.audit.String(), "a case for a critical alert after 15 minutes → 30 minutes") {
		t.Errorf("not audited: %s", w.audit.String())
	}
	s.CriticalAlertMinutes = nil
	s.LookBackDays = 7
	if got, err := w.svc.SaveSettings(ctx, s, sam); err != nil || *got.CriticalAlertMinutes != 30 || got.LookBackDays != 7 {
		t.Fatalf("sent without the minutes %+v %v", got, err)
	}
	var in Settings
	if err := json.Unmarshal([]byte(`{"keepAgentClosedDays":90,"lookBackDays":90,"exceptionStaleDays":90,"exceptionsPerAgent":50,"newCasesPerRun":50,"criticalAlertMinutes":0}`), &in); err != nil || in.CriticalAlertMinutes == nil || *in.CriticalAlertMinutes != 0 {
		t.Fatalf("0 is read as off, not as none: %+v %v", in, err)
	}
}
