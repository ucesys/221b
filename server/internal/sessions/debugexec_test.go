package sessions

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// askingFake answers access reviews: everything but what deny names
// ("create pods/exec").
type askingFake struct {
	*fakeBackend
	deny map[string]bool
}

func (a askingFake) Can(_ context.Context, verb, _, resource, sub, _, _ string) (bool, string, error) {
	res := resource
	if sub != "" {
		res += "/" + sub
	}
	if a.deny[verb+" "+res] {
		return false, "RBAC: denied", nil
	}
	return true, "", nil
}

func (a askingFake) Node(context.Context, string) (*corev1.Node, error) { return &corev1.Node{}, nil }

// Roles review, finding 6 (the owner, November 23): a debug pod opens its
// terminal with pods/exec. Without it the plan says so before any dry run
// of the Job, and the session ends before the Job is created.
func TestDebugPodWithoutExecCreatesNothing(t *testing.T) {
	m, f, _, _ := setup(t)
	a := askingFake{fakeBackend: f, deny: map[string]bool{"create pods/exec": true}}
	m.opts.Backend = func(Cluster, kube.Person) (Backend, error) { return a, nil }
	req := Request{Tool: toolbox.ToolDebugPod, Namespace: "web", Image: "general"}
	p := m.PlanFor(context.Background(), Cluster{ID: "c1"}, req, kube.Person{User: "alice"}, "Alice")
	if p.DryRunExit == 0 || len(p.DryRun) == 0 || !strings.Contains(p.DryRun[0], "pods/exec") || f.dryRuns != 0 {
		t.Fatalf("plan without pods/exec = %+v (dry runs %d)", p, f.dryRuns)
	}
	s, err := m.Start(Cluster{ID: "c1"}, req, alice(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, s.ID, StateEnded)
	f.mu.Lock()
	jobs := len(f.jobs)
	f.mu.Unlock()
	if got.EndedBy != EndFailed || !strings.Contains(got.EndNote, "pods/exec") || jobs != 0 {
		t.Fatalf("ended by %s (%s), %d jobs", got.EndedBy, got.EndNote, jobs)
	}
	// With pods/exec it plans as before.
	a.deny = nil
	if p := m.PlanFor(context.Background(), Cluster{ID: "c1"}, req, kube.Person{User: "alice"}, "Alice"); p.DryRunExit != 0 {
		t.Fatalf("plan with pods/exec = %+v", p)
	}
}
