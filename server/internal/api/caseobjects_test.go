package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/lessons"
)

// New exception suggests objects as the object is typed (Mycroft ›
// Exceptions): those the cluster's exceptions and cases name first, then
// the objects the console keeps of the cluster, best match first, each
// once; only what the person may list (P3-02), Secrets by name alone and
// only where they may list them; at most the limit; nothing before two
// characters; for the approvers alone; an unknown cluster refused.
func TestNewExceptionSuggestsObjects(t *testing.T) {
	ls := lessons.New(lessons.Options{})
	is := issues.New(issues.Options{Logger: quietLog()})
	deny := map[string]bool{"serviceaccounts/kube-system": true, "deployments/kube-system": true, "secrets/web": true}
	srv, admin, _, _, local, _ := multiSetupWith(t, "ops@corp.example", deny, func(o *Options) {
		o.Lessons, o.Issues = ls, is
	})
	local.Put(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "kevin"}})
	local.Put(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kevin-bot"}})
	local.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "sandbox-user-kevin"}})
	local.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "kevin-token"}, Data: map[string][]byte{"token": []byte("e2e-hunter2")}})
	local.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "kevin-cert"}, Data: map[string][]byte{"tls.key": []byte("e2e-hunter2")}})
	ctx := context.Background()
	by := lessons.Person{Name: "Ops"}
	for _, e := range []lessons.NewException{
		{Cluster: "local", Resource: "web/ServiceAccount/kevin", Check: "rbac.wide", Title: "Wide", Reason: "known"},
		{Cluster: "local", Resource: "ClusterRoleBinding/kevin-ops", Check: "rbac.wide", Title: "Wide", Reason: "known"},
		{Cluster: "prod", Resource: "web/ServiceAccount/kevin-prod", Check: "rbac.wide", Title: "Wide", Reason: "another cluster's"},
	} {
		if _, err := ls.AddException(ctx, e, by); err != nil {
			t.Fatal(err)
		}
	}
	agent := issues.Caller{Person: issues.Person{ID: "system:investigator", Name: "Investigator", System: true}, Operator: true}
	if _, err := is.Open(ctx, agent, issues.Opening{Source: issues.CaseFinding, Title: "Runs as root", Cluster: "local", Object: "kube-system/Deployment/kevin-dep",
		Key: &issues.Key{Cluster: "local", Object: "kube-system/Deployment/kevin-dep", Check: "pod.root-user"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := is.Open(ctx, agent, issues.Opening{Source: issues.CaseFinding, Title: "Wide", Cluster: "local", Object: "ClusterRole/kevin-all",
		Key: &issues.Key{Cluster: "local", Object: "ClusterRole/kevin-all", Check: "rbac.wide"}}); err != nil {
		t.Fatal(err)
	}

	get := func(c *http.Client, query string) (int, []CaseObject, string) {
		resp, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/cases/objects?"+query, "")
		var out struct{ Objects []CaseObject }
		json.Unmarshal([]byte(body), &out)
		return resp.StatusCode, out.Objects, body
	}
	objects := func(list []CaseObject) []string {
		var out []string
		for _, o := range list {
			out = append(out, o.Object+" "+o.From)
		}
		return out
	}

	code, got, body := get(admin, "cluster=local&q=Kevin")
	want := []string{
		"web/ServiceAccount/kevin cases",
		"ClusterRole/kevin-all cases",
		"ClusterRoleBinding/kevin-ops cases",
		"default/Secret/kevin-cert cluster",
		"ClusterRoleBinding/sandbox-user-kevin cluster",
	}
	if code != http.StatusOK || !slices.Equal(objects(got), want) {
		t.Fatalf("kevin: %d\n got %q\nwant %q", code, objects(got), want)
	}
	if got[0].Kind != "ServiceAccount" || got[0].Namespace != "web" || got[0].Name != "kevin" || got[1].Namespace != "" {
		t.Fatalf("the fields: %+v", got[:2])
	}
	if strings.Contains(body, "hunter2") || strings.Contains(body, "kevin-token") || strings.Contains(body, "kevin-bot") || strings.Contains(body, "kevin-dep") || strings.Contains(body, "kevin-prod") {
		t.Fatalf("offered what it should not: %s", body)
	}
	if _, got, _ := get(admin, "cluster=local&q=kevin&limit=2"); !slices.Equal(objects(got), want[:2]) {
		t.Fatalf("limit 2: %q", objects(got))
	}
	if _, got, _ := get(admin, "cluster=local&q=kevin&limit=500"); len(got) != len(want) {
		t.Fatalf("a large limit: %q", objects(got))
	}
	// A kind and name as typed; the namespace's objects by its name.
	if _, got, _ := get(admin, "cluster=local&q=sa/kev"); len(got) != 0 {
		t.Fatalf("an alias is not the kind's name: %q", objects(got))
	}
	if _, got, _ := get(admin, "cluster=local&q=serviceaccount/kev"); !slices.Equal(objects(got), []string{"web/ServiceAccount/kevin cases"}) {
		t.Fatalf("kind/name: %q", objects(got))
	}
	// Nothing before two characters.
	if code, got, body := get(admin, "cluster=local&q=k"); code != http.StatusOK || len(got) != 0 || !strings.Contains(body, `"objects":[]`) {
		t.Fatalf("one character: %d %s", code, body)
	}
	for query, want := range map[string]int{
		"cluster=nope&q=kevin":                           http.StatusBadRequest,
		"q=kevin":                                        http.StatusBadRequest,
		"cluster=local&q=kevin&limit=0":                  http.StatusBadRequest,
		"cluster=local&q=kevin&limit=x":                  http.StatusBadRequest,
		"cluster=local&q=" + strings.Repeat("k", 301):    http.StatusBadRequest,
		"cluster=local&q=" + strings.Repeat("k", 300):    http.StatusOK,
		"cluster=nope&q=k":                               http.StatusBadRequest,
		"cluster=local&q=no-such-object-anywhere-at-all": http.StatusOK,
	} {
		if code, _, body := get(admin, query); code != want {
			t.Errorf("%.60s: %d %s", query, code, body)
		}
	}
	// Only the approvers.
	viewer := signIn(t, srv, "viewer@corp.example")
	if code, _, body := get(viewer, "cluster=local&q=kevin"); code != http.StatusForbidden {
		t.Fatalf("a viewer: %d %s", code, body)
	}
}
