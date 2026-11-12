package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- The owner's feedback on Cases (Phase 12, wave 6c) ---

// lowestRouter routes as the cases do below the lowest priority: what it
// is given is what the test reads.
type lowestRouter struct {
	routeByCheck
	got *RouteInput
}

func (r lowestRouter) Route(ctx context.Context, in RouteInput) RouteResult {
	*r.got = in
	return r.routeByCheck.Route(ctx, in)
}

func (lowestRouter) Titles(_ context.Context, ids []string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if id == "C-7" {
			out[id] = "web restarts"
		}
	}
	return out
}

// CF-33: each job has a lowest priority, P3 by default, said in its view;
// it is changed with the job (audited with its edit), P0 to P4 only; a run
// gives it to the router with the agent's name.
func TestAJobsLowestPriorityCF33(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	v, err := w.s.Get(ctx, "test")
	if err != nil || v.MinPriority == nil || *v.MinPriority != DefaultMinPriority {
		t.Fatalf("the default: %+v %v", v.MinPriority, err)
	}
	two := 2
	v = on(t, w.s, "test", Change{MinPriority: &two})
	if *v.MinPriority != 2 || !strings.Contains(w.audit.String(), "lowest priority P3 → P2") {
		t.Fatalf("changed: %d, audit %s", *v.MinPriority, w.audit.String())
	}
	for _, bad := range []int{-1, 5} {
		if _, err := w.s.Update(ctx, "test", Change{MinPriority: &bad}, Person{Name: "a"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("P%d: %v", bad, err)
		}
	}
	var got RouteInput
	w.s.SetRouter(lowestRouter{got: &got})
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", AgentName: "Auditor", Schedulable: true, Checks: AuditChecks,
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			return Outcome{Findings: []Finding{{Severity: FindingInfo, Check: "pod.no-probes", Title: "no probes", Cluster: "local", Resource: "shop/Deployment/web"}}}, nil
		}})
	if _, err := w.s.RunNow(ctx, "test", Person{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	if got.MinPriority == nil || *got.MinPriority != 2 || got.Agent != "Auditor" {
		t.Errorf("the router was given %+v", got)
	}
	// Through the API: 400 outside P0 to P4, the number in the JSON.
	caller := Caller{Person: Person{Email: "sa@example.com"}, Admin: true, Read: true}
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return caller, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	put := func(body string) (int, string) {
		req := httptest.NewRequest("PUT", "/api/v1/jobs/test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := put(`{"minPriority": 4}`); code != http.StatusOK || !strings.Contains(body, `"minPriority":4`) {
		t.Errorf("P4: %d %s", code, body)
	}
	if code, _ := put(`{"minPriority": 7}`); code != http.StatusBadRequest {
		t.Errorf("P7: %d", code)
	}
}

// CF-33: a run counts its ignored findings.
func TestRoutedCountsIgnoredCF33(t *testing.T) {
	var r Routed
	r.Add(RoutedIgnored)
	r.Add(RoutedOpened)
	b, _ := json.Marshal(r)
	if r.Ignored != 1 || !strings.Contains(string(b), `"ignored":1`) {
		t.Errorf("%+v %s", r, b)
	}
}

// CF-37: each check of the catalogues has the type of the case it opens;
// other.<slug> and anything else is other.
func TestEachCheckHasATypeCF37(t *testing.T) {
	for _, list := range [][]Check{AuditChecks, AlertChecks, DigestChecks} {
		for _, c := range list {
			if c.Type == "" {
				t.Errorf("%s has no type", c.ID)
			}
		}
	}
	for check, want := range map[string]string{"rbac.cluster-admin-binding": "security", "pod.privileged": "security", "image.latest-tag": "security", "netpol.missing": "security",
		"node.disk-pressure": "capacity", "cert.expiring": "availability", "other.something": "other", "nonsense": "other"} {
		if got := CheckType(check); got != want {
			t.Errorf("%s: %s, want %s", check, got, want)
		}
	}
}

// CF-39: a run's proposals carry the titles of what they resolve when the
// run is read.
func TestARunsProposalsSayWhatTheyResolveCF39(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	var got RouteInput
	w.s.SetRouter(lowestRouter{got: &got})
	w.kinds.Register(Kind{ID: "resolving", Label: "Resolving job", Agent: "troubleshooter", Events: []string{EventAlert}, Checks: []Check{{"pod.crashloop", "", ""}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			var out Outcome
			merge(&out, env, nil, "local", "local", Parse(answerLocal))
			return out, nil
		}})
	on(t, w.s, "resolving", Change{When: &When{Event: EventAlert}})
	w.s.Fire(Event{Kind: EventAlert, Cluster: "local", Key: "alert:local/pod:shop/web", Label: "Alert", Severity: SeverityError})
	w.s.Wait()
	caller := Caller{Person: Person{Email: "sa@example.com"}, Admin: true, Read: true}
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return caller, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/runs/run-1", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"resolvesTitles":{"C-7":"web restarts"}`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	if r, _ := w.s.GetRun(ctx, "run-1"); len(r.Proposals) > 0 && r.Proposals[0].ResolvesTitles != nil {
		t.Error("the titles were kept on the run")
	}
}
