package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/schedule"
)

// The console writes an object one way (CF-05): aliases and case resolved
// to the canonical kind, namespace/Kind/name or Kind/name, a cluster-scoped
// kind without a namespace; "" is the cluster as a whole; anything else is
// refused.
func TestNormalizeObject(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"cluster":                      "",
		"shop/deploy/web":              "shop/Deployment/web",
		"shop/Deployment/web":          "shop/Deployment/web",
		"deploy/shop/web":              "shop/Deployment/web",
		"crb/everyone":                 "ClusterRoleBinding/everyone",
		"clusterrolebinding/everyone":  "ClusterRoleBinding/everyone",
		"ns/shop":                      "Namespace/shop",
		"shop/Namespace/shop":          "Namespace/shop",
		"shop/svc/api":                 "shop/Service/api",
		"shop/cm/conf":                 "shop/ConfigMap/conf",
		"shop/sa/builder":              "shop/ServiceAccount/builder",
		"shop/po/web-1":                "shop/Pod/web-1",
		"kube-system/ds/kube-proxy":    "kube-system/DaemonSet/kube-proxy",
		"db/sts/pg":                    "db/StatefulSet/pg",
		"shop/rb/edit":                 "shop/RoleBinding/edit",
		"cr/view":                      "ClusterRole/view",
		"shop/pvc/data":                "shop/PersistentVolumeClaim/data",
		"pv/pv-1":                      "PersistentVolume/pv-1",
		"no/worker-3":                  "Node/worker-3",
		"node/worker-3":                "Node/worker-3",
		"NODES/worker-3":               "Node/worker-3",
		"shop/node/worker-3":           "Node/worker-3",
		"shop/ing/web":                 "shop/Ingress/web",
		"batch/cj/nightly":             "batch/CronJob/nightly",
		"shop/widget/w1":               "shop/Widget/w1",
		"monitoring/Certificate/tls-1": "monitoring/Certificate/tls-1",
	} {
		o, err := NormalizeObject(in)
		if err != nil || o.String() != want {
			t.Errorf("%q: %q %v, want %q", in, o.String(), err, want)
		}
	}
	for _, bad := range []string{"web", "a/b/c/d", "shop//web", "the disk of db-1", "shop/deploy/Web App"} {
		if _, err := NormalizeObject(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

// A finding's check is one of its kind's catalogue, or other.<slug>
// (lower case, digits, dashes, at most 48); anything else is refused.
func TestValidCheck(t *testing.T) {
	k := Kind{Label: "Nightly audit", Checks: AuditChecks}
	for _, ok := range []string{"rbac.cluster-admin-binding", "pod.privileged", "image.latest-tag", "cert.expiring", "node.disk-pressure", "other.etcd-slow", "other.x", "other." + strings.Repeat("a", 48)} {
		if why := ValidCheck(k, ok); why != "" {
			t.Errorf("%q refused: %s", ok, why)
		}
	}
	for _, bad := range []string{"", "rbac.wildcard-binding", "Other.etcd", "other.Etcd", "other.etcd_slow", "other.-x", "other." + strings.Repeat("a", 49), "pod.crashloop"} {
		if why := ValidCheck(k, bad); why == "" {
			t.Errorf("%q taken", bad)
		}
	}
}

// fakeRouter routes every valid finding as opened and says one key in use.
type fakeRouter struct {
	mu sync.Mutex
	in []RouteInput
}

func (f *fakeRouter) Route(_ context.Context, in RouteInput) RouteResult {
	f.mu.Lock()
	f.in = append(f.in, in)
	f.mu.Unlock()
	out := RouteResult{Findings: append([]Finding(nil), in.Findings...), NewChecks: []string{"pod.privileged"}}
	for i := range out.Findings {
		if out.Findings[i].Routing != RoutedInvalid {
			out.Findings[i].Routing, out.Findings[i].Case = RoutedOpened, "C-1"
		}
		out.Routed.Add(out.Findings[i].Routing)
	}
	return out
}

func (f *fakeRouter) KeysInUse(context.Context, string) ([]KeyInUse, error) {
	return []KeyInUse{{Key: Key{Cluster: "local", Object: "shop/Pod/web", Check: "pod.no-limits"}, What: "case C-9", Title: "No limits on web"}}, nil
}

// Each finding gets its key, the object normalised and checked on the
// cluster when it answers; one whose check or object is not valid stays
// on the run as invalid, and the run says what its findings did (CF-05,
// CF-01). The task lists the catalogue and the keys in use (CF-10).
func TestFindingsAreKeyedAndRouted(t *testing.T) {
	w := setup(t)
	router := &fakeRouter{}
	w.s.SetRouter(router)
	w.s.SetObjects(func(_ context.Context, cluster string, o Object) (bool, bool) {
		switch o.Name {
		case "gone":
			return false, true
		case "far":
			return false, false
		}
		return true, true
	})
	var task string
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", Schedulable: true, Checks: AuditChecks,
		Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			task = env.Keys(ctx, "local")
			res, err := env.Ask(ctx, "local", task)
			if err != nil {
				return Outcome{}, err
			}
			p := Parse(res.Text)
			return Outcome{Report: p.Report, Findings: p.Findings}, nil
		}})
	w.agents.text = "Found.\n\n```json\n{\"findings\": [" +
		`{"severity": "critical", "check": "pod.privileged", "title": "Privileged web", "cluster": "local", "resource": "shop/po/web", "checkWhy": "not for a rule given"},` +
		`{"severity": "warning", "check": "other.etcd-slow", "title": "etcd is slow", "cluster": "local", "resource": "", "checkWhy": "etcd answers late. ` + strings.Repeat("No rule is about etcd. ", 40) + `"},` +
		`{"severity": "warning", "check": "rbac.wildcard-binding", "title": "Not in the catalogue", "cluster": "local", "resource": "crb/x"},` +
		`{"severity": "warning", "check": "pod.no-limits", "title": "Not an object", "cluster": "local", "resource": "the web pods"},` +
		`{"severity": "warning", "check": "pod.no-limits", "title": "Gone", "cluster": "local", "resource": "shop/Pod/gone"},` +
		`{"severity": "info", "check": "pod.no-limits", "title": "Far", "cluster": "local", "resource": "shop/Pod/far"}` +
		"]}\n```"
	on(t, w.s, "test", Change{})
	ctx := context.Background()
	// Rebuild now is a Wiki sync's (KN-29): an audit has nothing to rebuild.
	if _, err := w.s.RebuildNow(ctx, "test", Person{Email: "bob"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an audit rebuilt: %v", err)
	}
	b, err := w.s.RunNow(ctx, "test", Person{Email: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	r, _ := w.s.GetRun(ctx, b.ID)
	if len(r.Findings) != 6 {
		t.Fatalf("findings: %+v", r.Findings)
	}
	f := r.Findings
	if f[0].Key == nil || f[0].Key.Object != "shop/Pod/web" || f[0].Key.Check != "pod.privileged" || f[0].Routing != RoutedOpened || f[0].Case != "C-1" {
		t.Errorf("a valid finding: %+v", f[0])
	}
	if f[1].Key == nil || f[1].Key.Object != "" || f[1].Routing != RoutedOpened {
		t.Errorf("a cluster-wide finding: %+v", f[1])
	}
	// The description of an id the agent made up is kept, cut short; one
	// with a rule given is not (CF-86).
	if !strings.HasPrefix(f[1].CheckWhy, "etcd answers late.") || len([]rune(f[1].CheckWhy)) != maxCheckWhy || f[0].CheckWhy != "" {
		t.Errorf("checkWhy: %q / %q", f[1].CheckWhy, f[0].CheckWhy)
	}
	for i, why := range map[int]string{2: "not one of the test job", 3: "namespace/Kind/name", 4: "there is no shop/Pod/gone"} {
		if f[i].Routing != RoutedInvalid || f[i].Key != nil || !strings.Contains(f[i].InvalidReason, why) {
			t.Errorf("finding %d: %+v", i, f[i])
		}
	}
	if !f[5].Unverified || f[5].Key == nil || f[5].Routing != RoutedOpened {
		t.Errorf("an object not verified: %+v", f[5])
	}
	if r.Routed.Opened != 3 || r.Routed.Invalid != 3 || len(r.NewChecks) != 1 || r.Open() != 3 {
		t.Errorf("routed: %+v %v", r.Routed, r.NewChecks)
	}
	if len(router.in) != 1 || router.in[0].Run != r.ID || router.in[0].Job != "test" || !router.in[0].OK || len(router.in[0].Clusters) == 0 {
		t.Errorf("router input: %+v", router.in)
	}
	for _, want := range []string{"<check-ids>", "- pod.privileged:", "other.<slug>", "<keys-in-use>", "pod.no-limits on shop/Pod/web: No limits on web (case C-9)"} {
		if !strings.Contains(task, want) {
			t.Errorf("the task lacks %q: %s", want, task)
		}
	}
	if !strings.Contains(w.audit.String(), "3 opened") {
		t.Errorf("audit: %s", w.audit.String())
	}
	// A kind without a catalogue makes no findings to route: they stay as
	// they came.
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", Schedulable: true,
		Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			if env.Keys(ctx, "local") != "" {
				t.Error("a kind without a catalogue lists check ids")
			}
			return Outcome{Findings: []Finding{{Severity: "warning", Title: "About case C-4", Resource: "case C-4"}}}, nil
		}})
	b, _ = w.s.RunNow(ctx, "test", Person{Email: "bob"})
	w.s.Wait()
	r, _ = w.s.GetRun(ctx, b.ID)
	if len(r.Findings) != 1 || r.Findings[0].Routing != "" || r.Findings[0].Key != nil || len(router.in) != 1 {
		t.Errorf("no catalogue: %+v", r.Findings)
	}
	// The runs kept with a finding keyed, for the validation rules to
	// count again (CF-85): the first run, not this one.
	if kept, err := w.s.FoundRuns(ctx); err != nil || len(kept) != 1 || kept[0].ID == r.ID || len(kept[0].Findings) != 6 {
		t.Errorf("found runs: %+v %v", kept, err)
	}
}

// fakeRules offers pod.host-path and pod.no-limits; pod.root-user is
// retired; pod.restart-loop is replaced by pod.crashloop.
type fakeRules struct{}

func (fakeRules) Offered(Kind) []Check {
	return []Check{{ID: "pod.no-limits", Says: "no limits at all"}, {ID: "pod.host-path", Says: "a pod mounts a host path"}}
}
func (fakeRules) Known(_ Kind, id string) bool {
	return slices.Contains([]string{"pod.no-limits", "pod.host-path", "pod.root-user", "pod.restart-loop"}, id)
}
func (fakeRules) Canon(id string) string {
	if id == "pod.restart-loop" {
		return "pod.crashloop"
	}
	return id
}

// The rules as they stand (ADR-138) replace a kind's built-in catalogue:
// the task lists the rules offered, with their words, and no other; a
// finding under a retired rule is valid, one under a replaced rule is
// keyed by the rule that replaced it (and keeps its own check), and one
// under a built-in no longer on the list is refused.
func TestTheRulesAsTheyStand(t *testing.T) {
	w := setup(t)
	router := &fakeRouter{}
	w.s.SetRouter(router)
	w.s.SetRules(func(context.Context) Rules { return fakeRules{} })
	var task string
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", Schedulable: true, Checks: AuditChecks,
		Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			task = env.Keys(ctx, "local")
			return Outcome{Findings: []Finding{
				{Severity: "warning", Check: "pod.host-path", Title: "Host path", Cluster: "local", Resource: "shop/Pod/web"},
				{Severity: "warning", Check: "pod.root-user", Title: "Root", Cluster: "local", Resource: "shop/Pod/web"},
				{Severity: "warning", Check: "pod.restart-loop", Title: "Restarts", Cluster: "local", Resource: "shop/Pod/web"},
				{Severity: "warning", Check: "pod.privileged", Title: "Privileged", Cluster: "local", Resource: "shop/Pod/web"},
			}}, nil
		}})
	on(t, w.s, "test", Change{})
	ctx := context.Background()
	b, err := w.s.RunNow(ctx, "test", Person{Email: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	r, _ := w.s.GetRun(ctx, b.ID)
	f := r.Findings
	if len(f) != 4 {
		t.Fatalf("findings: %+v", f)
	}
	if f[0].Key == nil || f[0].Key.Check != "pod.host-path" || f[1].Key == nil || f[1].Key.Check != "pod.root-user" {
		t.Errorf("an added and a retired rule: %+v / %+v", f[0], f[1])
	}
	if f[2].Key == nil || f[2].Key.Check != "pod.crashloop" || f[2].Check != "pod.restart-loop" {
		t.Errorf("a replaced rule: %+v", f[2])
	}
	if f[3].Routing != RoutedInvalid || f[3].Key != nil {
		t.Errorf("a rule not on the list: %+v", f[3])
	}
	for _, want := range []string{"- pod.no-limits: no limits at all", "- pod.host-path: a pod mounts a host path"} {
		if !strings.Contains(task, want) {
			t.Errorf("the task lacks %q", want)
		}
	}
	if strings.Contains(task, "- pod.privileged:") {
		t.Error("the task lists a built-in no longer offered")
	}
}
