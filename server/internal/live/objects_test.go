package live

import (
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
)

func names(ms []ObjectMatch) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.String()
	}
	return out
}

// The New exception form's suggestions (Mycroft › Exceptions): the
// objects the console keeps, by name and kind in any case, the exact
// match first, then those that start with what was typed, then those in a
// namespace that does, then the rest; at most the limit; events and the
// like left out.
func TestFindObjectsRanksAndLimits(t *testing.T) {
	src := fake.New("worker-3")
	for _, ns := range []string{"sandbox-users", "kevin-tools"} {
		src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	src.Put(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "sandbox-users", Name: "kevin"}})
	src.Put(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "sandbox-users", Name: "old-kevin"}})
	src.Put(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "kevin-tools", Name: "default"}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "sandbox-user-kevin"}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "kevin-admin"}})
	src.Put(&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "sandbox-users", Name: "kevin.1234"}})
	s := New(src, Options{})

	got := names(s.FindObjects("KEVIN", 20))
	want := []string{
		"sandbox-users/ServiceAccount/kevin",
		"ClusterRoleBinding/kevin-admin",
		"Namespace/kevin-tools",
		"kevin-tools/ServiceAccount/default",
		"sandbox-users/ServiceAccount/old-kevin",
		"ClusterRoleBinding/sandbox-user-kevin",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kevin:\n got %q\nwant %q", got, want)
	}
	if got := names(s.FindObjects("kevin", 2)); !slices.Equal(got, want[:2]) {
		t.Fatalf("limit 2: %q", got)
	}
	// The kind and name as typed, and the whole object.
	if got := names(s.FindObjects("serviceaccount/kev", 20)); !slices.Equal(got, []string{"sandbox-users/ServiceAccount/kevin"}) {
		t.Fatalf("kind/name: %q", got)
	}
	if got := names(s.FindObjects("sandbox-users/ServiceAccount/kevin", 20)); !slices.Equal(got, []string{"sandbox-users/ServiceAccount/kevin"}) {
		t.Fatalf("whole: %q", got)
	}
	if got := names(s.FindObjects("worker", 20)); !slices.Equal(got, []string{"Node/worker-3"}) {
		t.Fatalf("node: %q", got)
	}
	if got := s.FindObjects("nothing-like-it", 20); len(got) != 0 {
		t.Fatalf("no match: %q", names(got))
	}
	if got := s.FindObjects("  ", 20); got != nil {
		t.Fatalf("blank: %q", names(got))
	}

	// An object added later is found once a change was told and the index
	// has aged.
	now := time.Now()
	s = New(src, Options{Now: func() time.Time { return now }})
	s.FindObjects("kevin", 20)
	src.Put(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "sandbox-users", Name: "kevin-2"}})
	s.Notify(kube.Change{Totals: true})
	if got := names(s.FindObjects("kevin-2", 20)); len(got) != 0 {
		t.Fatalf("rebuilt before it aged: %q", got)
	}
	now = now.Add(objectIndexAge)
	if got := names(s.FindObjects("kevin-2", 20)); !slices.Equal(got, []string{"sandbox-users/ServiceAccount/kevin-2"}) {
		t.Fatalf("after a change: %q", got)
	}
}

func TestRankObject(t *testing.T) {
	for _, c := range []struct {
		object, q string
		want      int
	}{
		{"sandbox-users/ServiceAccount/kevin", "kevin", rankExact},
		{"sandbox-users/ServiceAccount/kevin", "ServiceAccount/Kevin", rankExact},
		{"ClusterRoleBinding/kevin-admin", "kev", rankPrefix},
		{"ClusterRoleBinding/kevin-admin", "clusterrolebinding/k", rankPrefix},
		{"sandbox-users/ServiceAccount/old", "sandbox", rankNamespace},
		{"ClusterRoleBinding/sandbox-user-kevin", "kevin", rankAnywhere},
		{"Node/worker-3", "pod", -1},
		{"Node/worker-3", "", -1},
	} {
		if got := RankObject(c.object, c.q); got != c.want {
			t.Errorf("%s for %q: %d, want %d", c.object, c.q, got, c.want)
		}
	}
}

// manyKeys is a cluster of tens of thousands of objects, by key alone.
type manyKeys struct {
	*fake.Source
	keys map[string][]string
}

func (m *manyKeys) Keys(kind string) []string { return m.keys[kind] }

func bigStore(namespaces, perKind int) *Store {
	src := &manyKeys{Source: fake.New(), keys: map[string][]string{}}
	for _, kind := range []string{"pods", "configmaps", "serviceaccounts", "deployments", "replicasets", "services", "rolebindings", "secrets"} {
		for n := range namespaces {
			for i := range perKind {
				src.keys[kind] = append(src.keys[kind], fmt.Sprintf("team-%03d/%s-%s-%04d", n, kind[:len(kind)-1], "app", i))
			}
		}
	}
	for i := range namespaces * perKind {
		src.keys["clusterrolebindings"] = append(src.keys["clusterrolebindings"], fmt.Sprintf("binding-%05d", i))
	}
	src.keys["events"] = src.keys["pods"]
	return New(src, Options{})
}

// A cluster of tens of thousands of objects is searched in a few
// milliseconds once the index is built, whatever was typed, and the
// search stops at the limit.
func TestFindObjectsInALargeCluster(t *testing.T) {
	s := bigStore(100, 60) // 54,000 objects, events left out
	start := time.Now()
	s.FindObjects("warm", 20)
	built := time.Since(start)
	if n := len(s.objects.Load().items); n != 54000 {
		t.Fatalf("indexed %d objects", n)
	}
	var worst time.Duration
	for _, q := range []string{"ap", "app-0042", "team-050/", "binding-0", "serviceaccount/service", "zz-not-there", "e"} {
		start := time.Now()
		got := s.FindObjects(q, 20)
		if d := time.Since(start); d > worst {
			worst = d
		}
		if q != "zz-not-there" && len(got) != 20 {
			t.Fatalf("%q: %d found", q, len(got))
		}
	}
	t.Logf("index built in %v; slowest search %v", built, worst)
	// Generous for a loaded CI host; a search takes about a millisecond.
	if worst > 50*time.Millisecond {
		t.Fatalf("a search took %v", worst)
	}
	if got := names(s.FindObjects("binding-00042", 20)); !slices.Equal(got, []string{"ClusterRoleBinding/binding-00042"}) {
		t.Fatalf("exact in many: %q", got)
	}
}

func BenchmarkFindObjects(b *testing.B) {
	s := bigStore(100, 60)
	s.FindObjects("warm", 20)
	for _, q := range []string{"app-0042", "ap", "zz-not-there"} {
		b.Run(q, func(b *testing.B) {
			for b.Loop() {
				s.FindObjects(q, 20)
			}
		})
	}
}
