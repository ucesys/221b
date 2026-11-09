package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// --- Alerts and cases (Phase 12, wave 6, CF-77 to CF-79) ---

// fakeAcker keeps the jobs' acknowledgements as the settings would, and
// marks an alert a job acknowledged while it is as it was.
type fakeAcker struct {
	mu   sync.Mutex
	acks map[string]AlertAck
	n    int
}

func (f *fakeAcker) Ack(_ context.Context, a AlertAck) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acks == nil {
		f.acks = map[string]AlertAck{}
	}
	f.acks[a.Cluster+"/"+a.Key] = a
	f.n++
	return true, nil
}

func (f *fakeAcker) get(key string) (AlertAck, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acks[key], f.n
}

// holds says the job's acknowledgement holds for the alert as it is now.
func (f *fakeAcker) holds(a Alert) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.acks[a.Cluster+"/"+a.Key]
	return ok && x.Fingerprint == a.Fingerprint
}

const crashFinding = "Found it.\n\n```json\n" + `{"findings": [
 {"severity": "critical", "check": "pod.crashloop", "title": "web restarts", "resource": "shop/Pod/web-7f9"}], "proposals": []}` + "\n```"

// An alert diagnosis run that ended well and sent its findings to a case
// acknowledges its alert in the job's name, with the run and the case,
// using the alert as the run saw it, and audited as a person's is
// (CF-79). The alert jobs leave it while it holds; the alert grown more
// severe changes, the acknowledgement lapses, the job runs again and
// acknowledges it again with the new run. A run that failed or sent
// nothing to a case acknowledges nothing.
func TestAJobAcknowledgesTheAlertItHandledCF79(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	w.s.SetRouter(routeByCheck{})
	acker := &fakeAcker{}
	w.s.SetAcker(acker)
	w.agents.text = crashFinding
	var mu sync.Mutex
	alerts := map[string]Alert{}
	w.s.o.Alerts = func(context.Context) []Alert {
		mu.Lock()
		defer mu.Unlock()
		var out []Alert
		for _, a := range alerts {
			a.JobAck = acker.holds(a)
			out = append(out, a)
		}
		return out
	}
	set := func(a Alert) {
		mu.Lock()
		alerts[a.Key] = a
		mu.Unlock()
	}
	tick := func() {
		w.s.Tick(context.Background())
		w.s.Wait()
	}
	on(t, w.s, KindAlert, Change{When: &When{Event: EventAlert, Severity: SeverityWarning}})
	tick()
	web := Alert{Cluster: "prod", Key: "pod:shop/web-7f9", Kind: "pod", Severity: SeverityWarning, Name: "web-7f9", Namespace: "shop", Status: "Pending", Reason: "BackOff", Fingerprint: "pending|Pending"}
	set(web)
	tick()
	a, n := acker.get("prod/pod:shop/web-7f9")
	if n != 1 || a.Run != "run-1" || a.Case != "C-7" || a.By != "Alert diagnosis" || a.Fingerprint != "pending|Pending" || a.Key != "pod:shop/web-7f9" {
		t.Fatalf("the job's acknowledgement %+v (%d)", a, n)
	}
	if out := w.audit.String(); !strings.Contains(out, `"action":"alert.ack"`) || !strings.Contains(out, "acknowledged by Alert diagnosis, run run-1; its findings went to case C-7") {
		t.Fatalf("not audited: %s", out)
	}
	// While it holds, the alert jobs leave the alert alone.
	tick()
	tick()
	if list := runs(t, w.s); len(list) != 1 {
		t.Fatalf("an acknowledged alert ran again: %+v", list)
	}
	// It grows more severe: its fingerprint changes, the acknowledgement
	// lapses, and the job runs again, within the day of the first run.
	web.Severity, web.Status, web.Fingerprint = SeverityError, "CrashLoopBackOff", "failed|CrashLoopBackOff"
	set(web)
	tick()
	list := runs(t, w.s)
	if len(list) != 2 || list[0].Subject != "alert:prod/pod:shop/web-7f9" || list[1].Repeats != 0 {
		t.Fatalf("the escalated alert did not run again: %+v", list)
	}
	if a, n = acker.get("prod/pod:shop/web-7f9"); n != 2 || a.Run != list[0].ID || a.Fingerprint != "failed|CrashLoopBackOff" {
		t.Fatalf("acknowledged again %+v (%d)", a, n)
	}
	if r, _ := w.s.GetRun(context.Background(), list[0].ID); r.Severity != SeverityError {
		t.Errorf("the run keeps the alert's severity: %q", r.Severity)
	}
	// A run that finds nothing for a case, or fails, acknowledges nothing.
	w.agents.text = "All is well.\n\n```json\n{\"findings\": [], \"proposals\": []}\n```"
	set(Alert{Cluster: "prod", Key: "pod:shop/api-1", Kind: "pod", Severity: SeverityError, Name: "api-1", Namespace: "shop", Status: "Error", Fingerprint: "failed|Error"})
	tick()
	w.agents.mu.Lock()
	w.agents.err, w.agents.text = errors.New("the provider is down"), crashFinding
	w.agents.mu.Unlock()
	set(Alert{Cluster: "prod", Key: "pod:shop/db-0", Kind: "pod", Severity: SeverityError, Name: "db-0", Namespace: "shop", Status: "Error", Fingerprint: "failed|Error"})
	tick()
	if list := runs(t, w.s); len(list) != 4 || list[0].Status != StatusFailed || list[1].Status != StatusOK {
		t.Fatalf("runs %+v", list)
	}
	if _, n := acker.get(""); n != 2 {
		t.Fatalf("a run that sent nothing to a case, or failed, acknowledged: %d", n)
	}
	// The cases the alert runs went to, by the alert's key, on its cluster.
	cases := w.s.AlertCases(context.Background(), "prod")
	if got := cases["pod:shop/web-7f9"]; len(got) != 1 || got[0] != "C-7" || len(cases) != 1 {
		t.Fatalf("the alert runs' cases %+v", cases)
	}
	if other := w.s.AlertCases(context.Background(), "local"); len(other) != 0 {
		t.Fatalf("another cluster's %+v", other)
	}
}

// The alerts are read once per look, on the leader, for the alert jobs
// and for what else watches them (the cases' safety net, CF-78), even with
// no alert job on; a replica that does not lead reads none.
func TestTheAlertWatcherSeesTheAlertsCF78(t *testing.T) {
	w := setup(t)
	reads := 0
	w.s.o.Alerts = func(context.Context) []Alert {
		reads++
		return []Alert{{Cluster: "prod", Key: "node:n1", Kind: "node", Severity: SeverityError, Name: "n1", Fingerprint: "error|NotReady"}}
	}
	var got [][]Alert
	w.s.SetAlertWatcher(func(_ context.Context, alerts func() []Alert) {
		got = append(got, alerts(), alerts())
	})
	w.s.Tick(context.Background())
	if len(got) != 2 || len(got[0]) != 1 || got[0][0].CaseObject() != "Node/n1" || reads != 1 {
		t.Fatalf("watched %+v, read %d times", got, reads)
	}
	on(t, w.s, "test", Change{When: &When{Event: EventAlert}})
	w.s.Tick(context.Background())
	w.s.Wait()
	if reads != 2 || len(got) != 4 {
		t.Fatalf("read %d times with an alert job on, watched %d", reads, len(got))
	}
	w.s.o.Leads = func() bool { return false }
	w.s.Tick(context.Background())
	if reads != 2 || len(got) != 4 {
		t.Fatal("a replica that does not lead watched the alerts")
	}
	if o := (Alert{Kind: "pvc", Namespace: "shop", Name: "data"}).CaseObject(); o != "shop/PersistentVolumeClaim/data" {
		t.Errorf("a claim's object %q", o)
	}
	if o := (Alert{Kind: "pod", Namespace: "shop", Name: "web-1"}).CaseObject(); o != "shop/Pod/web-1" {
		t.Errorf("a pod's object %q", o)
	}
}

// --- Alerts that are not a problem (Phase 12, wave 6, CF-82 to CF-84) ---

// expectRouter routes as the cases do the outcomes the jobs read: an
// expected finding opens nothing, a crash loop a case, a volume nearly full
// is excepted, a node's memory pressure is below the job's lowest
// priority, anything else over the run's cap.
type expectRouter struct{ routeByCheck }

func (expectRouter) Route(_ context.Context, in RouteInput) RouteResult {
	out := RouteResult{Findings: append([]Finding(nil), in.Findings...)}
	for i := range out.Findings {
		f := &out.Findings[i]
		switch {
		case f.Key == nil:
			f.Routing = RoutedInvalid
		case f.Expected:
			f.Routing = RoutedExpected
		case f.Check == "pod.crashloop":
			f.Routing, f.Case = RoutedOpened, "C-7"
		case f.Check == "pvc.nearly-full":
			f.Routing, f.Exception = RoutedExcepted, "EX-3"
		case f.Check == "node.memory-pressure":
			f.Routing = RoutedIgnored
		default:
			f.Routing = RoutedCapped
		}
		out.Routed.Add(f.Routing)
	}
	return out
}

func block(findings string) string {
	return "Read it.\n\n```json\n{\"findings\": [" + findings + "], \"proposals\": []}\n```"
}

// A run concludes on its alert when it ended well and a finding reached a
// case, an exception covers it, or it is expected or below the job's
// lowest priority (CF-82): it acknowledges the alert so, the exception or
// expected with the agent's reason, and AlertConcluded says so for the
// alert as the run saw it, within the day; a run whose findings were only
// capped or not valid concludes nothing and acknowledges nothing. An
// expected finding without a reason is not expected; the run counts the
// expected ones.
func TestARunConcludesOnItsAlertCF82(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	w.s.SetRouter(expectRouter{})
	acker := &fakeAcker{}
	w.s.SetAcker(acker)
	on(t, w.s, KindAlert, Change{When: &When{Event: EventAlert, Severity: SeverityWarning}})
	ctx := context.Background()
	run := func(name, text string) Run {
		t.Helper()
		w.agents.mu.Lock()
		w.agents.text = text
		w.agents.mu.Unlock()
		a := Alert{Cluster: "prod", Key: "pod:shop/" + name, Kind: "pod", Severity: SeverityError, Name: name, Namespace: "shop", Status: "Unknown", Node: "n1", Fingerprint: "failed|Unknown"}
		r, _, err := w.s.RunEvent(ctx, KindAlert, Event{Kind: EventAlert, Cluster: "prod", Key: "alert:prod/" + a.Key, Severity: SeverityError, Label: "Alert", Data: a})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	asleep := run("asleep", block(`{"severity": "critical", "check": "node.not-ready", "title": "n1 sleeps", "resource": "Node/n1", "expected": true, "expectedWhy": "n1 is annotated nodepower/status: asleep."}`))
	if f := asleep.Findings[0]; f.Routing != RoutedExpected || !f.Expected || f.ExpectedWhy != "n1 is annotated nodepower/status: asleep." || asleep.Routed.Expected != 1 || asleep.Fingerprint != "failed|Unknown" {
		t.Fatalf("the expected finding %+v %+v", f, asleep.Routed)
	}
	if a, _ := acker.get("prod/pod:shop/asleep"); !a.Expected || a.Why != "n1 is annotated nodepower/status: asleep." || a.Run != asleep.ID || a.Case != "" || a.Exception != "" {
		t.Errorf("the expected alert's acknowledgement %+v", a)
	}
	if !strings.Contains(w.audit.String(), "acknowledged by Alert diagnosis, run "+asleep.ID+"; diagnosed as expected: n1 is annotated") {
		t.Errorf("not audited: %s", w.audit.String())
	}
	bare := run("bare", block(`{"severity": "critical", "check": "pod.crashloop", "title": "bare restarts", "resource": "shop/Pod/bare", "expected": true, "expectedWhy": "  "}`))
	if f := bare.Findings[0]; f.Expected || f.Routing != RoutedOpened {
		t.Errorf("an expected finding without a reason %+v", f)
	}
	excepted := run("full", block(`{"severity": "warning", "check": "pvc.nearly-full", "title": "data is full", "resource": "shop/PersistentVolumeClaim/data"}`))
	if a, _ := acker.get("prod/pod:shop/full"); a.Exception != "EX-3" || a.Expected || a.Run != excepted.ID {
		t.Errorf("the excepted alert's acknowledgement %+v", a)
	}
	low := run("low", block(`{"severity": "info", "check": "node.memory-pressure", "title": "n1 is short of memory", "resource": "Node/n1"}`))
	if a, _ := acker.get("prod/pod:shop/low"); !a.Expected || a.Why != "" || a.Run != low.ID {
		t.Errorf("the ignored alert's acknowledgement %+v", a)
	}
	// A case wins over the rest, the finding on the alert's object first.
	mixed := run("mixed", block(`{"severity": "info", "check": "node.memory-pressure", "title": "n1", "resource": "Node/n1"}, {"severity": "critical", "check": "pod.crashloop", "title": "mixed restarts", "resource": "shop/Pod/mixed"}`))
	if a, _ := acker.get("prod/pod:shop/mixed"); a.Case != "C-7" || a.Expected || a.Run != mixed.ID {
		t.Errorf("the mixed alert's acknowledgement %+v", a)
	}
	_, before := acker.get("")
	run("capped", block(`{"severity": "critical", "check": "pod.oomkilled", "title": "capped", "resource": "shop/Pod/capped"}, {"severity": "critical", "check": "no.such", "title": "invalid", "resource": "shop/Pod/capped"}`))
	if _, n := acker.get(""); n != before {
		t.Errorf("a run whose findings were only capped or not valid acknowledged the alert")
	}
	for name, want := range map[string]bool{"asleep": true, "full": true, "low": true, "mixed": true, "bare": true, "capped": false, "nothing": false} {
		if _, got := w.s.AlertConcluded(ctx, "prod", "pod:shop/"+name, "failed|Unknown"); got != want {
			t.Errorf("%s concluded: %v", name, got)
		}
	}
	if id, ok := w.s.AlertConcluded(ctx, "prod", "pod:shop/asleep", "failed|Unknown"); !ok || id != asleep.ID {
		t.Errorf("concluded by %q", id)
	}
	// Another fingerprint, another cluster: not what the run saw.
	if _, ok := w.s.AlertConcluded(ctx, "prod", "pod:shop/asleep", "failed|CrashLoopBackOff"); ok {
		t.Error("a run concluded on the alert as it is not")
	}
	if _, ok := w.s.AlertConcluded(ctx, "local", "pod:shop/asleep", "failed|Unknown"); ok {
		t.Error("a run on another cluster concluded")
	}
	// A day on, the run no longer stands for the alert.
	w.clock.Set(w.clock.Now().Add(DedupWindow))
	if _, ok := w.s.AlertConcluded(ctx, "prod", "pod:shop/asleep", "failed|Unknown"); ok {
		t.Error("a run a day old still concluded")
	}
}

// An alert is named by its reason, short (CF-84): a pod whose node stopped
// reporting, a crash loop without the kubelet's times and names, a node
// not ready by its problem, a node's other problem, a pending pod's why,
// the status when nothing says why; at most 120 characters.
func TestAnAlertsNameIsItsReasonCF84(t *testing.T) {
	for _, c := range []struct {
		a    Alert
		want string
	}{
		{Alert{Kind: "pod", Status: "Unknown", Node: "r620-worker"}, "Pod status Unknown: node r620-worker stopped reporting"},
		{Alert{Kind: "pod", Status: "Unknown"}, "Pod status Unknown: its node stopped reporting"},
		{Alert{Kind: "pod", Status: "CrashLoopBackOff", Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container=web pod=web-1_shop(0f9c)"}, "CrashLoopBackOff: back-off restarting failed container"},
		{Alert{Kind: "pod", Status: "ImagePullBackOff", Reason: "ImagePullBackOff", Message: `Back-off pulling image "registry.example/web:9"`}, "ImagePullBackOff: back-off pulling image"},
		{Alert{Kind: "node", Status: "Unknown", Reason: "Kubelet stopped reporting", Message: "The node controller has not heard from the kubelet for 5m. Pods on it are not being managed."}, "Node not ready: kubelet stopped reporting"},
		{Alert{Kind: "node", Status: "Not ready", Reason: "Not ready", Message: "The kubelet reports the node is not ready, so no new pods start here."}, "Node not ready: the kubelet reports the node is not ready, so no new pods start here"},
		{Alert{Kind: "node", Status: "Ready", Reason: "Disk pressure", Message: "Disk space or inodes are running low. The kubelet evicts pods."}, "Disk pressure: disk space or inodes are running low"},
		{Alert{Kind: "pod", Status: "Pending", Message: "0/3 nodes are available: 3 Insufficient cpu."}, "Pending: 0/3 nodes are available: 3 Insufficient cpu"},
		{Alert{Kind: "pod", Status: "Error", Reason: "OOMKilled"}, "Error: OOMKilled"},
		{Alert{Kind: "pvc", Status: "Pending"}, "Pending"},
		{Alert{Kind: "pod"}, "pod"},
	} {
		if got := c.a.Named(); got != c.want {
			t.Errorf("%+v is named %q, not %q", c.a, got, c.want)
		}
	}
	long := Alert{Kind: "pod", Status: "Error", Message: strings.Repeat("a very long message ", 20)}
	if got := []rune(long.Named()); len(got) != 120 || got[119] != '…' {
		t.Errorf("a long name: %d runes %q", len(got), string(got))
	}
}
