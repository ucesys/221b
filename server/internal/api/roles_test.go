package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

func clusterRoleOf(name string, rules []roles.Rule) *rbacv1.ClusterRole {
	cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}}
	for _, r := range rules {
		cr.Rules = append(cr.Rules, rbacv1.PolicyRule{APIGroups: r.APIGroups, Resources: r.Resources, ResourceNames: r.ResourceNames, NonResourceURLs: r.NonResourceURLs, Verbs: r.Verbs})
	}
	return cr
}

func getJSON(t *testing.T, c *http.Client, url string, into any) int {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("%s: %v\n%s", url, err, b)
		}
	}
	return resp.StatusCode
}

// The Roles and Permissions pages are for a SuperAdmin, show what the
// cluster has, and report a role that allows more than the shipped one.
func TestRolesPages(t *testing.T) {
	mods := func(o *Options) {
		o.Roles = RolesOptions{Prefixes: roles.DefaultPrefixes(), Enforce: "strict", Bindings: []roles.BindingSpec{
			{Name: "221b-viewer-local-web-readers-1", Role: roles.Viewer, Subject: "221b-local:web-readers", Namespace: "web"}}}
	}
	srv, ops, _, auditOut, local, _ := multiSetupWith(t, "ops@corp.example", nil, mods)
	viewer := signIn(t, srv, "viewer@corp.example")

	// Nothing read yet: no false "missing".
	if code := getJSON(t, ops, srv.URL+"/api/v1/admin/roles", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("roles with nothing read = %d, want 503", code)
	}

	for _, id := range roles.All {
		local.Put(clusterRoleOf(id.ClusterRole(), (roles.Templates{}).For(id, roles.Options{}).Rules))
	}
	local.Put(clusterRoleOf(roles.NodesClusterRole, roles.Nodes().Rules))
	local.Put(&rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "web-readers", Namespace: "web", Labels: map[string]string{managedByLabel: managedByValue}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: roles.Viewer.ClusterRole()},
		Subjects:   []rbacv1.Subject{{Kind: "Group", Name: "221b-local:web-readers"}},
	})

	if code := getJSON(t, viewer, srv.URL+"/api/v1/admin/roles", nil); code != http.StatusForbidden {
		t.Errorf("a Viewer's roles page = %d, want 403", code)
	}
	var page roles.RolesResponse
	if code := getJSON(t, ops, srv.URL+"/api/v1/admin/roles", &page); code != http.StatusOK {
		t.Fatalf("roles = %d", code)
	}
	if len(page.Roles) != 4 || page.Message != "" {
		t.Fatalf("roles page = %+v", page)
	}
	var viewerRow *roles.RoleRow
	for i := range page.Roles {
		if page.Roles[i].ID == roles.Viewer {
			viewerRow = &page.Roles[i]
		}
	}
	if viewerRow == nil || len(viewerRow.Subjects) != 1 || viewerRow.Subjects[0].Name != "web-readers" || !viewerRow.Subjects[0].Managed {
		t.Fatalf("Viewer subjects = %+v", viewerRow)
	}

	// The YAML for a cluster the chart is not installed in: SuperAdmin only.
	if code := getJSON(t, viewer, srv.URL+"/api/v1/admin/roles/manifest", nil); code != http.StatusForbidden {
		t.Errorf("a Viewer's manifest = %d, want 403", code)
	}
	if resp, err := ops.Get(srv.URL + "/api/v1/admin/roles/manifest"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("manifest: %v %v", resp, err)
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), `name: "221b-admin"`) || !strings.Contains(string(b), "221b-local:web-readers") {
			t.Errorf("the manifest lacks the roles or the bindings:\n%s", b)
		}
	}

	var perms roles.PermissionsResponse
	if code := getJSON(t, ops, srv.URL+"/api/v1/admin/permissions", &perms); code != http.StatusOK || perms.Drift.State != "match" {
		t.Fatalf("permissions = %d, drift %+v", code, perms.Drift)
	}

	// Someone widens the Viewer role by hand: it is reported, audited, and
	// with rbac.enforce strict every change on the cluster is refused.
	wide := clusterRoleOf(roles.Viewer.ClusterRole(), []roles.Rule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}})
	local.Put(wide)
	if code := getJSON(t, ops, srv.URL+"/api/v1/admin/permissions", &perms); code != http.StatusOK || perms.Drift.State != "differs" || len(perms.Drift.Roles) != 1 || len(perms.Drift.Roles[0].Differences) == 0 || !strings.Contains(perms.Drift.Roles[0].Differences[0], "which the shipped role does not") {
		t.Fatalf("after widening: %d, drift %+v", code, perms.Drift)
	}
	if !strings.Contains(auditOut.String(), "roles.drift") {
		t.Errorf("the drift was not audited:\n%s", auditOut.String())
	}
}

func TestPersonOfUsesWhatSignInWorkedOut(t *testing.T) {
	i := Impersonation{UserPrefix: "oidc:", GroupPrefix: "oidc:"}
	u := auth.User{Username: "ann", Groups: []string{"web"}, KubeName: "221b-ldap:ann", KubeGroups: []string{"221b:console-users", "221b-ldap:web"}}
	p := i.personOf(u)
	if p.User != "221b-ldap:ann" || len(p.Groups) != 3 || p.Groups[2] != "system:authenticated" {
		t.Errorf("person = %+v", p)
	}
	if p := i.personOf(auth.User{Username: "bob", Groups: []string{"web"}}); p.User != "oidc:bob" {
		t.Errorf("without a worked-out name the prefixes stand: %+v", p)
	}
}
