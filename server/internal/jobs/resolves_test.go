package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// --- A proposed change names the cases it resolves (Phase 12, wave 6b) ---

// routeByCheck routes findings as the cases would, by their check: a
// crashloop opens a case (C-7 for web, C-8 for api), a nearly full volume
// is a known issue's, missing limits are excepted, a node not ready is
// capped. It refuses C-99 as something a proposal resolves.
type routeByCheck struct{}

func (routeByCheck) Route(_ context.Context, in RouteInput) RouteResult {
	out := RouteResult{Findings: append([]Finding(nil), in.Findings...)}
	for i := range out.Findings {
		f := &out.Findings[i]
		switch {
		case f.Key == nil:
			f.Routing = RoutedInvalid
		case f.Check == "pod.crashloop" && strings.Contains(f.Title, "api"):
			f.Routing, f.Case = RoutedOpened, "C-8"
		case f.Check == "pod.crashloop":
			f.Routing, f.Case = RoutedOpened, "C-7"
		case f.Check == "pvc.nearly-full":
			f.Routing, f.Case, f.KnownIssue = RoutedKnown, "C-3", "KI-2"
		case f.Check == "pod.no-limits":
			f.Routing, f.Exception = RoutedExcepted, "EX-1"
		default:
			f.Routing = RoutedCapped
		}
		out.Routed.Add(f.Routing)
	}
	return out
}

func (routeByCheck) KeysInUse(context.Context, string) ([]KeyInUse, error) { return nil, nil }

func (routeByCheck) Resolvable(_ context.Context, _ string, id string) error {
	if id == "C-99" {
		return errors.New("there is no case C-99")
	}
	return nil
}

const answerLocal = "Local.\n\n```json\n" + `{"findings": [
 {"severity": "warning", "check": "pod.crashloop", "title": "web restarts", "resource": "shop/Pod/web"},
 {"severity": "warning", "check": "pod.crashloop", "title": "", "resource": "shop/Pod/web"},
 {"severity": "warning", "check": "pvc.nearly-full", "title": "data is 90% full", "resource": "shop/pvc/data"},
 {"severity": "warning", "check": "rbac.nonsense", "title": "a check not in the catalogue", "resource": ""}],
 "proposals": [
 {"title": "Raise web's memory", "fixes": [1, 3], "commands": ["kubectl set resources deployment/web -n shop --limits=memory=512Mi"]},
 {"title": "Grow data", "fixes": [2], "commands": ["kubectl annotate pvc data -n shop grow=yes"]},
 {"title": "Fix the check", "fixes": [4], "commands": ["kubectl annotate ns shop x=y"]},
 {"title": "Also the cases named", "fixes": ["c-9", "C-99", "KI-5", 7, {"odd": true}], "commands": ["kubectl annotate ns shop z=y"]}]}` + "\n```"

const answerProd = "Prod.\n\n```json\n" + `{"findings": [
 {"severity": "warning", "check": "pod.no-limits", "title": "no limits", "resource": "shop/Deployment/web"},
 {"severity": "warning", "check": "node.not-ready", "title": "node down", "resource": "Node/n1"},
 {"severity": "critical", "check": "pod.crashloop", "title": "api restarts", "resource": "shop/Pod/api"}],
 "proposals": [
 {"title": "Set web's limits", "fixes": [1], "commands": ["kubectl annotate deploy web -n shop a=b"]},
 {"title": "Restart the node", "fixes": [2], "commands": ["kubectl uncordon n1"]},
 {"title": "Restart api", "fixes": [3], "commands": ["kubectl rollout restart deployment/api -n shop"]},
 {"title": "Something unasked", "commands": ["kubectl annotate ns shop q=r"]}]}` + "\n```"

// A run's proposals resolve the cases and known issues of the findings
// they name by position, counted on across the run's answers and past the
// findings dropped; an excepted, invalid, capped or dropped finding
// resolves nothing; the ids the agent gives are checked with the router,
// each once (CF-26). A proposal that resolves nothing is kept on the run
// with its error and a note, never filed (CF-27).
func TestProposalsResolveWhatTheirFixesNameCF26CF27(t *testing.T) {
	w := setup(t)
	w.s.SetRouter(routeByCheck{})
	checks := []Check{{"pod.crashloop", "", ""}, {"pvc.nearly-full", "", ""}, {"pod.no-limits", "", ""}, {"node.not-ready", "", ""}}
	w.kinds.Register(Kind{ID: "resolving", Label: "Resolving job", Agent: "troubleshooter", Events: []string{EventAlert}, Checks: checks,
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			var out Outcome
			merge(&out, env, nil, "local", "local", Parse(answerLocal))
			merge(&out, env, nil, "prod", "prod", Parse(answerProd))
			return out, nil
		}})
	on(t, w.s, "resolving", Change{When: &When{Event: EventAlert}})
	if n := w.s.Fire(Event{Kind: EventAlert, Cluster: "prod", Key: "alert:prod/pod:shop/api", Label: "Alert BackOff on shop/api", Severity: SeverityError}); n != 1 {
		t.Fatalf("fired %d", n)
	}
	w.s.Wait()
	r, err := w.s.GetRun(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 6 {
		t.Fatalf("findings %+v", r.Findings)
	}
	want := []struct {
		title    string
		resolves []string
	}{
		{"Raise web's memory", []string{"C-7", "KI-2"}},
		{"Grow data", nil},
		{"Fix the check", nil},
		{"Also the cases named", []string{"C-9", "KI-5"}},
		{"Set web's limits", nil},
		{"Restart the node", nil},
		{"Restart api", []string{"C-8"}},
		{"Something unasked", nil},
	}
	if len(r.Proposals) != len(want) {
		t.Fatalf("proposals %+v", r.Proposals)
	}
	for i, x := range want {
		p := r.Proposals[i]
		if p.Title != x.title || !slices.Equal(p.Resolves, x.resolves) {
			t.Errorf("proposal %d: %+v, want %s resolving %v", i+1, p, x.title, x.resolves)
		}
		if x.resolves == nil && (p.ID != "" || p.Error != NamesNothing) {
			t.Errorf("proposal %d was filed, or says the wrong thing: %+v", i+1, p)
		}
		if x.resolves != nil && (p.ID == "" || p.Error != "") {
			t.Errorf("proposal %d was not filed: %+v", i+1, p)
		}
	}
	filed := w.props.List()
	if len(filed) != 3 || !slices.Equal(filed[0].Resolves, []string{"C-7", "KI-2"}) || !slices.Equal(filed[2].Resolves, []string{"C-8"}) || filed[2].Cluster != "prod" {
		t.Fatalf("filed %+v", filed)
	}
	for _, note := range []string{"a proposed change that names no case it resolves was not filed: Grow data", "named C-99, which it cannot resolve (there is no case C-99)"} {
		if !strings.Contains(r.Note, note) {
			t.Errorf("the run's note lacks %q: %s", note, r.Note)
		}
	}
}

// Without a router (a console without the cases), the ids a proposal
// names are kept as they are, and positions resolve nothing (CF-26).
func TestWithoutARouterIDsAreKept(t *testing.T) {
	w := setup(t)
	w.kinds.Register(Kind{ID: "plain", Label: "Plain job", Agent: "troubleshooter", Events: []string{EventAlert},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			var out Outcome
			merge(&out, env, nil, "local", "", Parse(answerLocal))
			return out, nil
		}})
	on(t, w.s, "plain", Change{When: &When{Event: EventAlert}})
	w.s.Fire(Event{Kind: EventAlert, Cluster: "local", Key: "alert:local/x", Label: "x", Severity: SeverityError})
	w.s.Wait()
	r, _ := w.s.GetRun(context.Background(), "run-1")
	var filed []string
	for _, p := range r.Proposals {
		if p.ID != "" {
			filed = append(filed, p.Title+" "+strings.Join(p.Resolves, ","))
		}
	}
	if !slices.Equal(filed, []string{"Also the cases named C-9,C-99,KI-5"}) {
		t.Fatalf("filed %v", filed)
	}
}

// What a proposal fixes is read from numbers, numbers as text and ids;
// anything else is nothing, and the block stays readable (CF-26).
func TestFixesAreReadForgivinglyCF26(t *testing.T) {
	p := Parse("x\n\n```json\n" + `{"findings": [], "proposals": [{"title": "t", "fixes": [1, "2", "#3", "c-12", "KI-3", {"x": 1}, 0, 1.5, null, true], "commands": ["kubectl get pods"]}]}` + "\n```")
	if len(p.Proposals) != 1 {
		t.Fatalf("the block was not read: %+v", p)
	}
	var pos []int
	var ids []string
	for _, f := range p.Proposals[0].Fixes {
		if f.Position > 0 {
			pos = append(pos, f.Position)
		}
		if f.ID != "" {
			ids = append(ids, f.ID)
		}
	}
	if !slices.Equal(pos, []int{1, 2, 3}) || !slices.Equal(ids, []string{"C-12", "KI-3"}) {
		t.Fatalf("positions %v ids %v", pos, ids)
	}
	if !strings.Contains(OutputFormat, `"fixes": [1]`) || !strings.Contains(OutputFormat, "a proposal that fixes nothing you found is not filed") {
		t.Errorf("the output format does not ask for fixes: %s", OutputFormat)
	}
	// An id the agent makes up comes with its description (CF-86).
	if !strings.Contains(OutputFormat, `"checkWhy": ""`) || !strings.Contains(OutputFormat, "what your id means") || !strings.Contains(OutputFormat, "why none of the ids given fits") {
		t.Errorf("the output format does not ask for checkWhy: %s", OutputFormat)
	}
}
