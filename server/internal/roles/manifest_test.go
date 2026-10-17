package roles

import (
	"strings"
	"testing"
)

func TestManifestHasTheRolesAndBindings(t *testing.T) {
	m := Manifest(ManifestInput{Secrets: true, Bindings: []BindingSpec{
		{Name: "221b-viewer-local-web-readers-1", Role: Viewer, Subject: "221b-local:web-readers", Namespace: "web"},
		{Name: "221b-admin-ldap-ops-2", Role: Admin, Subject: "221b-ldap:ops", Namespace: "api"},
		{Name: "bad", Role: SuperAdmin, Subject: "x", Namespace: "web"},
	}})
	for _, want := range []string{
		`kind: ClusterRole`, `name: "221b-admin"`, `name: "221b-super-admin"`, `name: "221b-super-viewer"`, `name: "221b-viewer"`, `name: "221b-nodes"`,
		`kind: ClusterRoleBinding`, `name: "221b:console-users"`, `name: "221b:superadmins"`, `name: "221b:superviewers"`,
		`kind: RoleBinding`, `namespace: "web"`, `name: "221b-local:web-readers"`, `name: "221b-ldap:ops"`,
		`app.kubernetes.io/managed-by: "221b"`, `221b/managed: "true"`, `apiGroups: [""]`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("the manifest lacks %s", want)
		}
	}
	if strings.Contains(m, `"x"`) {
		t.Error("a super role is never bound to a namespace")
	}
	if n := strings.Count(m, "kind: RoleBinding"); n != 2 {
		t.Errorf("%d RoleBindings, want 2", n)
	}
}

func TestManifestReadOnlyDefinitionsHaveNoWriteVerb(t *testing.T) {
	m := Manifest(ManifestInput{Templates: Templates{Admin: ReadOnly, SuperAdmin: ReadOnly}})
	for _, verb := range []string{`"create"`, `"update"`, `"patch"`, `"delete"`, `"*"`} {
		for _, line := range strings.Split(m, "\n") {
			if strings.Contains(line, "verbs:") && strings.Contains(line, verb) {
				t.Errorf("a read-only definition has %s: %s", verb, line)
			}
		}
	}
	if !strings.Contains(m, `221b/definition": "readOnly"`) {
		t.Error("the definition is not named")
	}
}
