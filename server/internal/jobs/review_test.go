package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- The phase 12 review: the cluster asked about, whole changes, routing
// tried again, a change keyed on what it is about (W-5, W-6, W-8, W-10) ---

// mergingKind is a kind that merges its agent's answer for the cluster of
// the event, as the alert diagnosis does.
func mergingKind(w *world, checks ...Check) {
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "troubleshooter", Events: []string{EventAlert}, Checks: checks,
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			res, err := env.Ask(ctx, env.Clusters[0], "why")
			if err != nil {
				return Outcome{}, err
			}
			var out Outcome
			merge(&out, env, w.s.o.CommandOK, env.Clusters[0], "", Parse(res.Text))
			return out, nil
		}})
}

// fire fires an alert on prod and waits for the run.
func fire(t *testing.T, w *world, key string) Run {
	t.Helper()
	if n := w.s.Fire(Event{Kind: EventAlert, Cluster: "prod", Key: "alert:prod/" + key, Label: "Alert on " + key, Severity: SeverityError}); n != 1 {
		t.Fatalf("fired %d", n)
	}
	w.s.Wait()
	list := runs(t, w.s)
	r, err := w.s.GetRun(context.Background(), list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A run about one cluster files nothing on another (W-5, CF-97): a
// finding or a proposal that names another cluster is left out with a
// note on the run; one that names none, or the cluster by its name, is
// the cluster's; a proposal's positions skip the findings left out.
func TestNothingIsFiledForAnotherClusterCF97(t *testing.T) {
	w := setup(t)
	w.s.SetRouter(routeByCheck{})
	mergingKind(w, Check{"pod.crashloop", "", ""})
	on(t, w.s, "test", Change{When: &When{Event: EventAlert}})
	w.agents.text = "Read.\n\n```json\n" + `{"findings": [
 {"severity": "warning", "check": "pod.crashloop", "title": "web restarts", "cluster": "prod", "resource": "shop/Pod/web"},
 {"severity": "warning", "check": "pod.crashloop", "title": "planted", "cluster": "local", "resource": "shop/Pod/x"},
 {"severity": "warning", "check": "pod.crashloop", "title": "api restarts", "cluster": "prod-eu", "resource": "shop/Pod/api"},
 {"severity": "warning", "check": "pod.crashloop", "title": "cart restarts", "resource": "shop/Pod/cart"}],
 "proposals": [
 {"title": "Restart api", "fixes": [3], "commands": ["kubectl rollout restart deployment/api -n shop"]},
 {"title": "Planted change", "cluster": "local", "fixes": [1], "commands": ["kubectl annotate ns shop planted=yes"]},
 {"title": "Fix the planted", "fixes": [2], "commands": ["kubectl annotate ns x a=b"]}]}` + "\n```"
	r := fire(t, w, "pod:shop/api")
	if len(r.Findings) != 3 {
		t.Fatalf("findings %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Cluster != "prod" || f.Title == "planted" {
			t.Errorf("a finding kept: %+v", f)
		}
	}
	filed := w.props.List()
	if len(filed) != 1 || filed[0].Title != "Restart api" || filed[0].Cluster != "prod" || !slices.Equal(filed[0].Resolves, []string{"C-8"}) {
		t.Fatalf("filed %+v", filed)
	}
	for _, note := range []string{`a finding named the cluster "local", not prod, which it was asked about, and was left out: planted`,
		`a proposed change named the cluster "local", not prod, which it was asked about, and was not filed: Planted change`,
		"a proposed change that names no case it resolves was not filed: Fix the planted"} {
		if !strings.Contains(r.Note, note) {
			t.Errorf("the run's note lacks %q: %s", note, r.Note)
		}
	}
}

// A proposal is filed with every command the agent gave, or not at all,
// with the command refused and why (W-6, CF-98).
func TestAChangeIsFiledWholeOrNotAtAllCF98(t *testing.T) {
	check := func(line string) error {
		if strings.Contains(line, "delete namespace") {
			return errors.New("Watson does not delete namespaces")
		}
		return nil
	}
	p := Parsed{Proposals: []Proposed{
		{Title: "Drain n1", Commands: []string{"kubectl cordon n1", "kubectl drain n1 --ignore-daemonsets", "kubectl uncordon n1"}},
		{Title: "Drain and clean", Commands: []string{"kubectl cordon n1", "kubectl delete namespace scratch", "kubectl uncordon n1"}},
		{Title: "Restart by hand", Commands: []string{"kubectl cordon n1", "ssh n1 reboot"}},
		{Title: "Roll web", Commands: []string{"", "  helm rollback web 3 -n shop  ", " "}},
	}}
	out, notes := p.Changes(check, "prod")
	if len(out) != 2 || out[0].Title != "Drain n1" || out[1].Title != "Roll web" {
		t.Fatalf("filed %+v", out)
	}
	if len(out[0].Actions) != 3 || len(out[1].Actions) != 1 || out[1].Actions[0].Args["command"] != "helm rollback web 3 -n shop" {
		t.Errorf("a proposal filed short: %+v", out)
	}
	want := []string{
		"a proposed change was not filed, with none of its commands, because Watson would refuse “kubectl delete namespace scratch”: Watson does not delete namespaces: Drain and clean",
		"a proposed change was not filed, with none of its commands, because “ssh n1 reboot” is not a kubectl or helm command: Restart by hand",
	}
	if !slices.Equal(notes, want) {
		t.Errorf("notes %q", notes)
	}
}

// flakyRouter cannot route the first fail times; it routes by check after.
type flakyRouter struct {
	routeByCheck
	mu   sync.Mutex
	fail int
	in   []RouteInput
}

func (f *flakyRouter) Route(ctx context.Context, in RouteInput) RouteResult {
	f.mu.Lock()
	f.in = append(f.in, in)
	fail := f.fail > 0
	f.fail--
	f.mu.Unlock()
	if fail {
		return RouteResult{Findings: in.Findings, NotRouted: "the exceptions could not be read: the store is down"}
	}
	return f.routeByCheck.Route(ctx, in)
}

func (f *flakyRouter) calls() []RouteInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RouteInput(nil), f.in...)
}

const crashAnswer = "Read.\n\n```json\n" + `{"findings": [{"severity": "warning", "check": "pod.crashloop", "title": "web restarts", "resource": "shop/Pod/web"}], "proposals": []}` + "\n```"

// A run whose findings could not be routed says so and why, and the
// leader routes it on a later look, until it can (W-8, I-3, CF-99). A late
// routing verifies no fix.
func TestARunNotRoutedIsRoutedLaterCF99(t *testing.T) {
	w := setup(t)
	router := &flakyRouter{fail: 2}
	w.s.SetRouter(router)
	mergingKind(w, Check{"pod.crashloop", "", ""})
	on(t, w.s, "test", Change{When: &When{Event: EventAlert}})
	w.agents.text = crashAnswer
	r := fire(t, w, "pod:shop/web")
	if r.NotRouted != "the exceptions could not be read: the store is down" || !r.Reroute || r.Routed.Opened != 0 || !strings.Contains(r.Note, "its findings were not routed into cases (the exceptions could not be read: the store is down); the console tries again") {
		t.Fatalf("the run %+v", r)
	}
	if b := runs(t, w.s)[0]; b.NotRouted != r.NotRouted {
		t.Errorf("the run's brief %+v", b)
	}
	ctx := context.Background()
	// Still not: it waits for the next look.
	w.s.Tick(ctx)
	if r, _ = w.s.GetRun(ctx, r.ID); !r.Reroute || r.NotRouted == "" {
		t.Fatalf("after a look that failed %+v", r)
	}
	// Not again within the minute.
	w.s.Tick(ctx)
	if n := len(router.calls()); n != 2 {
		t.Fatalf("routed %d times", n)
	}
	w.clock.Set(w.clock.Now().Add(time.Minute))
	w.s.Tick(ctx)
	if r, _ = w.s.GetRun(ctx, r.ID); r.Reroute || r.NotRouted != "" || r.Routed.Opened != 1 || r.Findings[0].Case != "C-7" || !strings.Contains(r.Note, "its findings were routed into cases on a later look") {
		t.Fatalf("routed later %+v", r)
	}
	in := router.calls()
	if len(in) != 3 || in[2].OK || in[2].Run != r.ID || in[2].Job != "test" || len(in[2].Findings) != 1 {
		t.Errorf("the late routing %+v", in)
	}
	w.clock.Set(w.clock.Now().Add(time.Minute))
	w.s.Tick(ctx)
	if n := len(router.calls()); n != 3 {
		t.Errorf("a run routed is routed again: %d", n)
	}
}

// A run not routed is let go, and says so, once a later run of its job
// has been routed: its findings are older than what the cases know.
func TestARunNotRoutedIsLetGoAfterALaterOneCF99(t *testing.T) {
	w := setup(t)
	router := &flakyRouter{fail: 1}
	w.s.SetRouter(router)
	mergingKind(w, Check{"pod.crashloop", "", ""})
	on(t, w.s, "test", Change{When: &When{Event: EventAlert}})
	w.agents.text = crashAnswer
	first := fire(t, w, "pod:shop/web")
	w.clock.Set(w.clock.Now().Add(time.Minute))
	second := fire(t, w, "pod:shop/api")
	if !first.Reroute || second.Reroute || second.NotRouted != "" {
		t.Fatalf("the runs %+v %+v", first, second)
	}
	ctx := context.Background()
	w.s.Tick(ctx)
	r, _ := w.s.GetRun(ctx, first.ID)
	if r.Reroute || r.NotRouted == "" || !strings.Contains(r.Note, "its findings were let go unrouted: a later run of its job was routed since") {
		t.Fatalf("the first run %+v", r)
	}
	if n := len(router.calls()); n != 2 {
		t.Errorf("routed %d times", n)
	}
	if !strings.Contains(w.audit.String(), "its findings were let go unrouted") {
		t.Errorf("audit %s", w.audit.String())
	}
}

// A proposal is keyed on its cluster, what it resolves and its commands
// (W-10, CF-101): reworded, it replaces the one waiting; other commands
// for the same case wait beside it.
func TestAProposalIsKeyedOnWhatItIsAboutCF101(t *testing.T) {
	w := setup(t)
	w.s.SetRouter(routeByCheck{})
	mergingKind(w)
	on(t, w.s, "test", Change{When: &When{Event: EventAlert}})
	answer := func(title, command string) string {
		return "Read.\n\n```json\n" + `{"findings": [], "proposals": [{"title": "` + title + `", "fixes": ["C-4"], "commands": ["` + command + `"]}]}` + "\n```"
	}
	w.agents.text = answer("Restart web", "kubectl rollout restart deployment/web -n shop")
	fire(t, w, "pod:shop/web")
	w.agents.text = answer("Roll the web deployment", "kubectl   rollout restart deployment/web -n shop")
	fire(t, w, "pod:shop/web-2")
	w.agents.text = answer("Restart web", "kubectl rollout restart deployment/web -n other")
	fire(t, w, "pod:shop/web-3")
	filed := w.props.List()
	if len(filed) != 2 || filed[0].Title != "Roll the web deployment" || filed[1].Title != "Restart web" {
		t.Fatalf("filed %+v", filed)
	}
	if filed[0].DedupKey != ProposalKey(filed[0]) || !strings.HasPrefix(filed[0].DedupKey, "fix:prod:C-4:") || filed[0].DedupKey == filed[1].DedupKey {
		t.Errorf("keys %q %q", filed[0].DedupKey, filed[1].DedupKey)
	}
}
