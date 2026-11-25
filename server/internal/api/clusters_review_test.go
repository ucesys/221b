package api

import (
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
)

// repointed is a connection "prod" re-pointed from cluster A to cluster B
// between two looks at it: the first look finds A's client, every later
// one B's, and its fingerprint is B's already. Nothing else of Clusters
// is asked by the role check.
type repointed struct {
	Clusters
	mu    sync.Mutex
	looks int
	a, b  *source
}

func (r *repointed) next() (*source, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.looks++
	if r.looks == 1 {
		return r.a, "fp-a"
	}
	return r.b, "fp-b"
}

func (r *repointed) Lookup(string) (*live.Store, clusters.Client, bool) {
	c, _ := r.next()
	return nil, c, true
}

// Fingerprint is the connection it runs now: B's.
func (r *repointed) Fingerprint(string) string { return "fp-b" }

func (r *repointed) Conn(string) (*live.Store, clusters.Client, string, bool) {
	c, of := r.next()
	return nil, c, of, true
}

func (r *repointed) List() []clusters.Summary {
	return []clusters.Summary{{Spec: clusters.Spec{ID: "prod", Name: "prod", Mode: clusters.ModeWrite}}}
}

// Clusters review, finding 1: under strict, a role check that held the
// client of the connection before a re-point kept that cluster's verdict
// under the new connection's fingerprint, so changes ran on cluster B on
// cluster A's "match" although B's roles were never read.
func TestAVerdictIsKeptWithTheConnectionItWasReadThrough(t *testing.T) {
	a := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	for _, id := range roles.All {
		a.Put(clusterRoleOf(id.ClusterRole(), (roles.Templates{}).For(id, roles.Options{}).Rules))
	}
	a.Put(clusterRoleOf(roles.NodesClusterRole, roles.Nodes().Rules))
	// B: its roles cannot be read (nothing cached), so nothing may pass.
	b := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	cl := &repointed{a: a, b: b}
	s := New(Options{Clusters: cl, Logger: quietLog(), Roles: RolesOptions{Enforce: "strict"}})
	// A check that looked the connection up before the re-point.
	s.checkChangedRoles()
	if d, ok := s.monitor().Get("prod"); !ok || d.State != "match" {
		t.Fatalf("A's verdict: %+v", d)
	}
	if why := s.RoleRefusal("prod"); why == "" {
		t.Fatal("a change on B passed on A's verdict")
	}
}
