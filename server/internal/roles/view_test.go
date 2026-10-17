package roles

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func shippedAll(t Templates, o Options) map[ID]Deployed {
	m := map[ID]Deployed{}
	for _, id := range All {
		m[id] = Deployed{Present: true, Rules: t.For(id, o).Rules}
	}
	return m
}

func TestCheckMatchesWhatShipsAndFindsWidening(t *testing.T) {
	tpl, o := Defaults(), Options{Secrets: true}
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	dep := shippedAll(tpl, o)
	if d := Check(tpl, o, dep, at); d.State != "match" || len(d.Roles) != 0 || d.More {
		t.Fatalf("shipped roles differ: %+v", d)
	}
	// Order inside a rule does not matter.
	rev := dep[Admin]
	rev.Rules = slices.Clone(rev.Rules)
	slices.Reverse(rev.Rules)
	dep[Admin] = rev
	if d := Check(tpl, o, dep, at); d.State != "match" {
		t.Fatalf("a reordered role differs: %+v", d)
	}
	// Someone adds delete on nodes to 221b-admin.
	dep = shippedAll(tpl, o)
	a := dep[Admin]
	a.Rules = append(slices.Clone(a.Rules), Rule{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"delete"}})
	dep[Admin] = a
	d := Check(tpl, o, dep, at)
	if d.State != "differs" || !d.More || len(d.Roles) != 1 || d.Roles[0].ID != Admin || !strings.Contains(d.Roles[0].Differences[0], "delete nodes") {
		t.Fatalf("widened role: %+v", d)
	}
	// A role that is gone is missing, not wider.
	dep = shippedAll(tpl, o)
	dep[Viewer] = Deployed{}
	if d := Check(tpl, o, dep, at); d.State != "differs" || d.More {
		t.Fatalf("missing role: %+v", d)
	}
	if d := Check(tpl, o, map[ID]Deployed{}, at); d.State != "missing" {
		t.Fatalf("nothing deployed: %+v", d)
	}
	// A read-only deployment compared with the full definition is narrower, and still shown.
	ro := Templates{Admin: ReadOnly, SuperAdmin: ReadOnly}
	d = Check(tpl, o, shippedAll(ro, o), at)
	if d.State != "differs" || d.More {
		t.Fatalf("read-only against full: %+v", d)
	}
}

func TestMonitorRefusesOnlyWhenStrictAndWider(t *testing.T) {
	var m Monitor
	tpl, o := Defaults(), Options{}
	dep := shippedAll(tpl, o)
	a := dep[Admin]
	a.Rules = append(slices.Clone(a.Rules), Rule{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"delete"}})
	dep[Admin] = a
	d := Check(tpl, o, dep, time.Now())
	if !m.Set("local", d) || m.Set("local", d) {
		t.Fatal("Set must report a change once")
	}
	if m.Refusal("local", "off") != "" || m.Refusal("other", "strict") != "" {
		t.Error("refused when it should not")
	}
	msg := m.Refusal("local", "strict")
	if !strings.Contains(msg, "refused") || !strings.Contains(msg, "delete nodes") || !strings.Contains(msg, "Admin") {
		t.Errorf("message: %s", msg)
	}
	m.Set("local", Check(tpl, o, shippedAll(tpl, o), time.Now()))
	if m.Refusal("local", "strict") != "" {
		t.Error("still refusing after the roles match")
	}
}

func TestBuildRolesClassifiesSubjectsByPrefix(t *testing.T) {
	in := RolesInput{
		Templates: Defaults(), Deployed: shippedAll(Defaults(), Options{}), Prefixes: Prefixes{Local: "221b-local:", LDAP: "221b-ldap:", OIDCGroup: "oidc:"}, Visible: true,
		Members: func(g string) int { return map[string]int{"checkout-ops": 2}[g] },
		Exists:  func(u string) bool { return u != "ghost" },
		Bindings: []Binding{
			{Role: "221b-admin", Namespace: "web", Managed: true, Subjects: []BoundSubject{{"Group", "221b-ldap:team-a-ops"}, {"Group", "221b-local:checkout-ops"}}},
			{Role: "221b-admin", Namespace: "api", Managed: true, Subjects: []BoundSubject{{"Group", "221b-ldap:team-a-ops"}}},
			{Role: "221b-admin", Namespace: "web", Subjects: []BoundSubject{{"User", "carol"}, {"ServiceAccount", "ci-deployer"}, {"User", "221b-local:ghost"}, {"Group", "oidc:ops"}}},
			{Role: "221b-viewer", Namespace: "web", Managed: true, Subjects: []BoundSubject{{"Group", "221b-local:web-readers"}}},
			{Role: "somebody-elses-role", Namespace: "web", Subjects: []BoundSubject{{"Group", "x"}}},
		},
	}
	out := BuildRoles(in)
	if out.Message != "" || len(out.Roles) != 4 || out.Roles[0].ID != SuperAdmin || out.Roles[3].ID != Viewer {
		t.Fatalf("%+v", out)
	}
	var admin RoleRow
	for _, r := range out.Roles {
		if r.ID == Admin {
			admin = r
		}
	}
	got := map[string]SubjectRow{}
	for _, s := range admin.Subjects {
		got[s.Kind+"/"+s.Name] = s
	}
	if s := got["Group/team-a-ops"]; s.Source != "ldap" || !slices.Equal(s.Namespaces, []string{"api", "web"}) || !s.Managed {
		t.Errorf("ldap group: %+v", s)
	}
	if s := got["Group/checkout-ops"]; s.Source != "local" || s.Members != 2 {
		t.Errorf("local group: %+v", s)
	}
	if s := got["User/carol"]; s.Source != "other" || s.Managed {
		t.Errorf("a hand-made binding: %+v", s)
	}
	if s := got["User/ghost"]; s.Source != "local" || !s.Orphaned {
		t.Errorf("an orphaned local user: %+v", s)
	}
	if s := got["Group/ops"]; s.Source != "oidc" {
		t.Errorf("oidc group: %+v", s)
	}
	if s := got["ServiceAccount/ci-deployer"]; s.Source != "other" {
		t.Errorf("service account: %+v", s)
	}
	if len(admin.Subjects) != 6 {
		t.Errorf("another role's binding leaked in: %+v", admin.Subjects)
	}

	// Not allowed to list bindings: nothing about who holds what.
	in.Visible = false
	if out := BuildRoles(in); out.Visible || len(out.Roles[2].Subjects) != 0 {
		t.Errorf("hidden: %+v", out.Roles[2])
	}
	in.Deployed = nil
	if out := BuildRoles(in); !strings.Contains(out.Message, "not on this cluster") || out.Roles[0].Present {
		t.Errorf("nothing deployed: %q", out.Message)
	}
}

func TestBuildPermissionsSplitsReadAndWriteAndScopes(t *testing.T) {
	tpl := Templates{Admin: Full, SuperAdmin: ReadOnly}
	o := Options{Secrets: true}
	nodes := Nodes()
	out := BuildPermissions(tpl, shippedAll(tpl, o), Deployed{Present: true, Rules: nodes.Rules}, "strict", Drift{State: "match", CheckedAt: time.Now()})
	if out.Enforce != "strict" || out.CheckedAt == nil || out.Definitions.SuperAdmin != ReadOnly || out.Definitions.Admin != Full {
		t.Fatalf("%+v", out)
	}
	find := func(id ID, group, res string) *PermRule {
		for _, r := range out.Roles {
			if r.ID != id {
				continue
			}
			for i := range r.Rules {
				if r.Rules[i].Group == group && r.Rules[i].Resource == res {
					return &r.Rules[i]
				}
			}
		}
		return nil
	}
	var deletes bool
	for _, r := range out.Roles[2].Rules {
		if r.Resource == "pods" && r.Kind == "write" && r.Scope == "bound" && slices.Contains(r.Verbs, "delete") {
			deletes = true
		}
	}
	if out.Roles[2].ID != Admin || !deletes {
		t.Errorf("Admin pods: %+v", out.Roles[2].Rules)
	}
	if r := find(Admin, "", "secrets"); r == nil || !slices.Contains(r.Flags, "Secrets") {
		t.Errorf("Admin secrets: %+v", r)
	}
	if r := find(Admin, "", "namespaces"); r == nil || r.Scope != "own" || r.Kind != "read" {
		t.Errorf("own namespaces: %+v", r)
	}
	if r := find(Viewer, "", "nodes"); r == nil || r.Scope != "cluster" {
		t.Errorf("nodes for a Viewer: %+v", r)
	}
	if r := find(SuperViewer, "", "namespaces"); r == nil || r.Scope != "cluster" {
		t.Errorf("SuperViewer namespaces: %+v", r)
	}
	if r := find(SuperViewer, "", "pods"); r == nil || r.Scope != "all" {
		t.Errorf("SuperViewer pods: %+v", r)
	}
	if r := find(SuperAdmin, "*", "*"); r == nil || r.Kind != "read" || !slices.Contains(r.Flags, "wildcard") || slices.Contains(r.Verbs, "*") {
		t.Errorf("read-only SuperAdmin: %+v", r)
	}
	for _, r := range out.Roles {
		if r.ID == Viewer || r.ID == SuperViewer {
			for _, rule := range r.Rules {
				if rule.Kind == "write" {
					t.Errorf("%s has a write rule: %+v", r.ID, rule)
				}
			}
		}
	}
	// An absent role has nothing to list, and says so.
	dep := shippedAll(tpl, o)
	dep[Admin] = Deployed{}
	out = BuildPermissions(tpl, dep, Deployed{}, "", Drift{State: "unknown"})
	for _, r := range out.Roles {
		if r.ID == Admin && (r.Present || len(r.Rules) != 0) {
			t.Errorf("absent role: %+v", r)
		}
	}
	if out.Enforce != "off" {
		t.Errorf("enforce = %q", out.Enforce)
	}
}
