package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fake"
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

// The Roles and Permissions pages are for everyone who signs in, show what
// the cluster has, and report a role that allows more than the shipped one.
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
		ObjectMeta: metav1.ObjectMeta{Name: "web-readers", Namespace: "web", Labels: map[string]string{managedLabel: "true", managedByLabel: "Helm"}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: roles.Viewer.ClusterRole()},
		Subjects:   []rbacv1.Subject{{Kind: "Group", Name: "221b-local:web-readers"}},
	})

	// A Viewer sees both pages (RB-41), but not who holds the super roles (RB-42).
	var seen roles.RolesResponse
	if code := getJSON(t, viewer, srv.URL+"/api/v1/admin/roles", &seen); code != http.StatusOK || len(seen.Roles) != 4 {
		t.Errorf("a Viewer's roles page = %d %+v, want 200 with the four roles", code, seen)
	}
	for _, row := range seen.Roles {
		if row.Holders != (roles.Holders{}) {
			t.Errorf("a Viewer sees the holders of %s: %+v", row.ID, row.Holders)
		}
	}
	if code := getJSON(t, viewer, srv.URL+"/api/v1/admin/permissions", nil); code != http.StatusOK {
		t.Errorf("a Viewer's permissions page = %d, want 200", code)
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

// A replica that has just become active has often listed nothing yet: its
// check reads nothing and comes back in seconds, not in five minutes.
func TestARoleCheckThatReadNothingComesBackSoon(t *testing.T) {
	active := true
	local := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	s := New(Options{Cluster: local, Active: func() bool { return active }, Logger: quietLog()})
	if next := s.roleCheckNow(); next != driftSoon {
		t.Fatalf("nothing read: next in %s", next)
	}
	// A standby is told when it becomes active; it has no need to look.
	active = false
	if next := s.roleCheckNow(); next != driftEvery {
		t.Fatalf("not active: next in %s", next)
	}
	active = true
	for _, id := range roles.All {
		local.Put(clusterRoleOf(id.ClusterRole(), (roles.Templates{}).For(id, roles.Options{}).Rules))
	}
	if next := s.roleCheckNow(); next != driftEvery {
		t.Fatalf("read: next in %s", next)
	}
	if _, ok := s.monitor().Get(clusters.LocalID); !ok {
		t.Fatal("the check kept nothing")
	}
}

// Roles review, finding 3: the nodes role, which everyone who signs in
// holds, was never compared, so widening it tripped neither the report nor
// rbac.enforce strict.
func TestWideningTheNodesRoleIsADrift(t *testing.T) {
	local := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	s := New(Options{Cluster: local, Logger: quietLog(), Roles: RolesOptions{Enforce: "strict"}})
	for _, id := range roles.All {
		local.Put(clusterRoleOf(id.ClusterRole(), (roles.Templates{}).For(id, roles.Options{}).Rules))
	}
	local.Put(clusterRoleOf(roles.NodesClusterRole, roles.Nodes().Rules))
	if d, _, _, ok := s.checkRoles(clusters.LocalID, local); !ok || d.State != "match" || s.RoleRefusal(clusters.LocalID) != "" {
		t.Fatalf("shipped roles: %+v, refusal %q", d, s.RoleRefusal(clusters.LocalID))
	}
	local.Put(clusterRoleOf(roles.NodesClusterRole, append(roles.Nodes().Rules, roles.Rule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}})))
	d, _, _, _ := s.checkRoles(clusters.LocalID, local)
	if d.State != "differs" || !d.More || len(d.Roles) != 1 || d.Roles[0].ID != roles.NodesID {
		t.Fatalf("a widened nodes role: %+v", d)
	}
	if why := s.RoleRefusal(clusters.LocalID); !strings.Contains(why, "221b-nodes allows") {
		t.Fatalf("strict with a widened nodes role: %q", why)
	}
}

// Roles review, finding 4: under strict, a verdict kept from before was
// used however old it was, and whatever the connection now reached.
func TestStrictDoesNotTrustAnOldVerdict(t *testing.T) {
	local := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	s := New(Options{Cluster: local, Logger: quietLog(), Roles: RolesOptions{Enforce: "strict"}})
	for _, id := range roles.All {
		rules := (roles.Templates{}).For(id, roles.Options{}).Rules
		if id == roles.Admin {
			rules = append(rules, roles.Rule{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"delete"}})
		}
		local.Put(clusterRoleOf(id.ClusterRole(), rules))
	}
	local.Put(clusterRoleOf(roles.NodesClusterRole, roles.Nodes().Rules))
	// A "match" an hour old, from before the role was widened.
	s.monitor().Set(clusters.LocalID, roles.Drift{State: "match", Roles: []roles.RoleDrift{}, CheckedAt: time.Now().Add(-time.Hour)})
	if why := s.RoleRefusal(clusters.LocalID); !strings.Contains(why, "delete nodes") {
		t.Fatalf("an hour-old match was trusted: %q", why)
	}
}

// Roles review, finding 10: a cluster-wide binding beside a namespaced one
// vanished from the Roles page, and ServiceAccounts of the same name in two
// namespaces became one row.
func TestRolesPageShowsClusterWideBindingsAndEachServiceAccount(t *testing.T) {
	local := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	admin := rbacv1.RoleRef{Kind: "ClusterRole", Name: roles.Admin.ClusterRole()}
	local.Put(&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: "web", Labels: map[string]string{managedLabel: "true"}}, RoleRef: admin,
		Subjects: []rbacv1.Subject{{Kind: "Group", Name: "221b-ldap:team-a-ops"}}})
	local.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "x"}, RoleRef: admin, Subjects: []rbacv1.Subject{{Kind: "Group", Name: "221b-ldap:team-a-ops"}}})
	for _, ns := range []string{"a", "b"} {
		local.Put(&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: ns}, RoleRef: admin, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "default", Namespace: ns}}})
	}
	page := roles.BuildRoles(roles.RolesInput{Bindings: bindingsOf(local), Prefixes: roles.DefaultPrefixes(), Visible: true})
	var rows []byte
	for _, r := range page.Roles {
		if r.ID == roles.Admin {
			rows, _ = json.Marshal(r.Subjects)
		}
	}
	var subjects []map[string]any
	_ = json.Unmarshal(rows, &subjects)
	sas, wide := 0, false
	for _, s := range subjects {
		if s["kind"] == "ServiceAccount" {
			sas++
		}
		if s["name"] == "team-a-ops" && s["clusterWide"] == true {
			wide = true
		}
	}
	if !wide {
		t.Errorf("the cluster-wide binding is not shown: %s", rows)
	}
	if sas != 2 {
		t.Errorf("%d ServiceAccount rows for default in a and in b: %s", sas, rows)
	}
}

// Roles review, finding 12: the Roles page's lines ignored the options,
// so with rbac.roles.admin.exec it said Admin cannot open a shell, and
// without secrets.read that it rolls back Helm releases.
func TestRolesPageFollowsTheOptions(t *testing.T) {
	srv, ops, _, _, local, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Roles = RolesOptions{Prefixes: roles.DefaultPrefixes(), Exec: true, Secrets: false}
	})
	for _, id := range roles.All {
		local.Put(clusterRoleOf(id.ClusterRole(), (roles.Templates{}).For(id, roles.Options{Exec: true}).Rules))
	}
	local.Put(clusterRoleOf(roles.NodesClusterRole, roles.Nodes().Rules))
	var page roles.RolesResponse
	if code := getJSON(t, ops, srv.URL+"/api/v1/admin/roles", &page); code != http.StatusOK {
		t.Fatalf("roles = %d", code)
	}
	for _, r := range page.Roles {
		if r.ID != roles.Admin {
			continue
		}
		can, cannot := strings.Join(r.Can, "; "), strings.Join(r.Cannot, "; ")
		if strings.Contains(cannot, "shell") || !strings.Contains(can, "shell") {
			t.Errorf("Admin with exec: can %q, cannot %q", can, cannot)
		}
		if strings.Contains(can, "Helm") {
			t.Errorf("Admin without Secrets rolls back Helm releases: %q", can)
		}
	}
}
