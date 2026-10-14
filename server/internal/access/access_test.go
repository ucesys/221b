package access_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// checker is a fake authorizer: it allows pods in web, nothing in
// kube-system, nodes to nobody, and counts the questions.
type checker struct {
	asked atomic.Int64
	fail  atomic.Bool
}

func (c *checker) CanI(_ context.Context, p kube.Person, verb, group, resource, namespace string) (bool, error) {
	c.asked.Add(1)
	if c.fail.Load() {
		return false, errors.New("the API server is away")
	}
	if verb != "list" {
		return false, nil
	}
	switch {
	case namespace == "":
		return false, nil
	case namespace == "kube-system":
		return false, nil
	default:
		return true, nil
	}
}

func TestPairsCoverTheViews(t *testing.T) {
	pairs := access.Pairs([]string{live.ViewNodes, live.ViewPods, live.ViewHelm, "ns:web"}, []string{"web", "kube-system"})
	want := map[access.Pair]bool{
		{Resource: "nodes"}:                                                   true,
		{Resource: "pods", Namespace: "web"}:                                  true,
		{Resource: "pods", Namespace: "kube-system"}:                          true,
		{Resource: "secrets", Namespace: "web"}:                               true,
		{Resource: "secrets", Namespace: "kube-system"}:                       true,
		{Group: "apps", Resource: "deployments", Namespace: "web"}:            true,
		{Group: "networking.k8s.io", Resource: "ingresses", Namespace: "web"}: true,
	}
	got := map[access.Pair]bool{}
	for _, p := range pairs {
		got[p] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("missing %+v", p)
		}
	}
	if got[access.Pair{Group: "apps", Resource: "deployments", Namespace: "kube-system"}] {
		t.Error("a one-namespace view asked about another namespace")
	}
	// No duplicates.
	if len(got) != len(pairs) {
		t.Errorf("%d pairs, %d distinct", len(pairs), len(got))
	}
}

func TestAccessAsksOnceAndAgainAfterTheTTL(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := &checker{}
	cache := access.NewCache(access.Options{TTL: time.Minute, Now: clock, Parallel: 4})
	dev := kube.Person{User: "dev@corp.example", Groups: []string{"developers"}}
	if cache.For("local", c, kube.Person{}) != nil {
		t.Fatal("the console itself got a filter")
	}
	a := cache.For("local", c, dev)
	if a != cache.For("local", c, dev) || a == cache.For("other", c, dev) {
		t.Fatal("access is per person and cluster")
	}
	// Unknown pairs are denied until asked.
	if a.Namespaced("pods", "web") || a.Allow(live.ViewPods, "web/shop") {
		t.Fatal("allowed before asking")
	}
	pairs := access.Pairs([]string{live.ViewNodes, live.ViewPods}, []string{"web", "kube-system"})
	changed, err := a.Prepare(context.Background(), pairs, 0)
	if err != nil || !changed {
		t.Fatalf("prepare: %v %v", changed, err)
	}
	if !a.Allow(live.ViewPods, "web/shop") || a.Allow(live.ViewPods, "kube-system/dns") || a.Allow(live.ViewNodes, "n1") || !a.AllowNamespace("web") || a.AllowCluster("nodes") {
		t.Fatal("verdicts are wrong")
	}
	asked := c.asked.Load()
	if asked != int64(len(pairs)) {
		t.Fatalf("asked %d for %d pairs", asked, len(pairs))
	}
	// Asked again at once: nothing to ask.
	if changed, _ := a.Prepare(context.Background(), pairs, 0); changed || c.asked.Load() != asked || a.Stale(pairs) {
		t.Fatal("asked again before the TTL")
	}
	// After the TTL, everything is asked again; the same answers are no change.
	now = now.Add(2 * time.Minute)
	if !a.Stale(pairs) {
		t.Fatal("not stale after the TTL")
	}
	if changed, _ := a.Prepare(context.Background(), pairs, 0); changed || c.asked.Load() != 2*asked {
		t.Fatalf("after the TTL: changed=%v asked=%d", changed, c.asked.Load())
	}
	v := a.Version()
	// A failing review leaves the old verdict, and the error comes back.
	now = now.Add(2 * time.Minute)
	c.fail.Store(true)
	if _, err := a.Prepare(context.Background(), pairs, 0); err == nil {
		t.Fatal("no error from a failing review")
	}
	if !a.Allow(live.ViewPods, "web/shop") || a.Version() != v {
		t.Fatal("a failing review changed the verdicts")
	}
	// People who have not asked for a while are forgotten.
	if cache.Count() != 2 {
		t.Fatalf("%d people kept", cache.Count())
	}
	now = now.Add(2 * time.Hour)
	cache.Sweep(time.Hour)
	if cache.Count() != 0 {
		t.Fatalf("%d people kept after the sweep", cache.Count())
	}
}

func TestScopeOfEveryView(t *testing.T) {
	for _, tc := range []struct {
		view, key, resource, ns string
		cluster                 bool
	}{
		{live.ViewNodes, "n1", "nodes", "", true},
		{live.ViewNamespaces, "web", "pods", "web", false},
		{live.ViewPods, "web/shop", "pods", "web", false},
		{live.ViewServices, "web/api", "services", "web", false},
		{live.ViewHelm, "web/shop", "secrets", "web", false},
		{"ns:web", "Deployment/api", "deployments", "web", false},
		{"ns:web", "Secret/creds", "secrets", "web", false},
		{"ns:web", "Unknown/x", "pods", "web", false},
	} {
		r, ns, cl := live.Scope(tc.view, tc.key)
		if r != tc.resource || ns != tc.ns || cl != tc.cluster {
			t.Errorf("Scope(%s, %s) = %s %s %v", tc.view, tc.key, r, ns, cl)
		}
	}
}
