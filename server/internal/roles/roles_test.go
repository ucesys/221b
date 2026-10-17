package roles

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "write deploy/helm/221b/files/roles.json")

const shippedFile = "../../../deploy/helm/221b/files/roles.json"

// The chart installs what is in the file, so the file must be what the
// definitions say (RB-01, RB-29).
func TestShippedFileMatchesDefinitions(t *testing.T) {
	want, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(shippedFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shippedFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(shippedFile)
	if err != nil {
		t.Fatalf("%v; run: go test ./internal/roles -run Shipped -update", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is out of date; run: go test ./internal/roles -run Shipped -update", shippedFile)
	}
}

func writes(rules []Rule) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.Writes() {
			out = append(out, r)
		}
	}
	return out
}

func TestReadOnlyDefinitionsHaveNoWrite(t *testing.T) {
	for _, id := range All {
		for _, o := range []Options{{}, {Secrets: true}} {
			d := Definition(id, ReadOnly, o)
			if w := writes(d.Rules); len(w) > 0 {
				t.Errorf("%s read-only has write rules: %+v", id, w)
			}
		}
	}
	for _, id := range []ID{Viewer, SuperViewer} {
		if w := writes(Definition(id, Full, Options{Secrets: true}).Rules); len(w) > 0 {
			t.Errorf("%s has write rules: %+v", id, w)
		}
	}
}

func TestSuperViewerHasNoSecrets(t *testing.T) {
	for _, r := range Definition(SuperViewer, ReadOnly, Options{Secrets: true}).Rules {
		if slices.Contains(r.Resources, "secrets") || r.Wildcard() {
			t.Errorf("SuperViewer has %+v", r)
		}
	}
}

func TestSecretsFollowTheOption(t *testing.T) {
	has := func(id ID, tpl Template, o Options) bool {
		for _, r := range Definition(id, tpl, o).Rules {
			if slices.Contains(r.Resources, "secrets") {
				return true
			}
		}
		return false
	}
	for _, id := range []ID{Viewer, Admin} {
		if has(id, Full, Options{}) {
			t.Errorf("%s has a Secret rule with secrets.read off", id)
		}
		if !has(id, Full, Options{Secrets: true}) {
			t.Errorf("%s has no Secret rule with secrets.read on", id)
		}
	}
}

func TestAdminHasNoRBACNoExecNothingClusterWide(t *testing.T) {
	for _, tpl := range []Template{Full, ReadOnly} {
		for _, r := range writes(Definition(Admin, tpl, Options{Secrets: true}).Rules) {
			for _, res := range r.Resources {
				if strings.HasPrefix(res, "pods/exec") || strings.Contains(res, "role") || res == "nodes" || res == "namespaces" || res == "pods/eviction" {
					t.Errorf("Admin %s writes %s", tpl, res)
				}
			}
			for _, v := range r.Verbs {
				if v == "*" || v == "escalate" || v == "bind" || v == "impersonate" || v == "create" && !slices.Contains(r.Resources, "secrets") {
					t.Errorf("Admin %s has verb %s on %v", tpl, v, r.Resources)
				}
			}
		}
	}
	for _, r := range Definition(Admin, Full, Options{}).Rules {
		for _, res := range Watched {
			if !res.Namespaced && slices.Contains(r.Resources, res.Resource) && slices.Contains(r.APIGroups, res.Group) {
				t.Errorf("Admin reads the cluster-scoped %s", res.Resource)
			}
		}
	}
}

func TestAdminFullCoversWatsonsChanges(t *testing.T) {
	d := Definition(Admin, Full, Options{Secrets: true}).Rules
	has := func(want string) bool {
		for _, g := range Grants(d) {
			if g.String() == want {
				return true
			}
		}
		return false
	}
	for _, g := range []string{
		"update deployments/scale.apps", "patch deployments.apps", "patch statefulsets.apps", "patch daemonsets.apps",
		"delete pods", "patch pods", "create secrets", "update secrets",
	} {
		if !has(g) {
			t.Errorf("Admin full lacks %q", g)
		}
	}
	for _, g := range Grants(Definition(Admin, ReadOnly, Options{Secrets: true}).Rules) {
		if g.String() == "delete pods" {
			t.Error("Admin read-only deletes pods")
		}
	}
}

func TestSuperAdmin(t *testing.T) {
	full := Definition(SuperAdmin, Full, Options{})
	if len(full.Rules) != 2 || !full.Rules[0].Wildcard() || full.Rules[1].NonResourceURLs[0] != "*" {
		t.Errorf("SuperAdmin full = %+v", full.Rules)
	}
	ro := Definition(SuperAdmin, ReadOnly, Options{})
	if len(writes(ro.Rules)) != 0 {
		t.Errorf("SuperAdmin read-only writes: %+v", ro.Rules)
	}
	if !slices.Contains(ro.Rules[1].NonResourceURLs, "*") || ro.Rules[1].Verbs[0] != "get" {
		t.Errorf("non-resource rule = %+v", ro.Rules[1])
	}
}

func TestEveryWatchedKindIsReadByViewerOrSuperViewer(t *testing.T) {
	reads := func(id ID, w Resource) bool {
		return covered(Grants(Definition(id, ReadOnly, Options{}).Rules), Grant{Verb: "get", Group: w.Group, Resource: w.Resource})
	}
	for _, w := range Watched {
		if !reads(SuperViewer, w) {
			t.Errorf("SuperViewer cannot read %s %s", w.Group, w.Resource)
		}
		if has := reads(Viewer, w); has != w.Namespaced {
			t.Errorf("Viewer reads %s %s = %v, namespaced is %v", w.Group, w.Resource, has, w.Namespaced)
		}
	}
}

func TestTemplatesOf(t *testing.T) {
	if got := (Templates{}).Of(Admin); got != Full {
		t.Errorf("default Admin = %s", got)
	}
	if got := (Templates{}).Of(Viewer); got != ReadOnly {
		t.Errorf("Viewer = %s", got)
	}
	if (Templates{Admin: ReadOnly}).ReadOnlyOnly() || !(Templates{Admin: ReadOnly, SuperAdmin: ReadOnly}).ReadOnlyOnly() {
		t.Error("ReadOnlyOnly")
	}
}

func TestFlags(t *testing.T) {
	cases := []struct {
		r    Rule
		want []string
	}{
		{Rule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}, []string{"wildcard"}},
		{Rule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}, []string{"Secrets"}},
		{Rule{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}}, []string{"pods/exec"}},
		{Rule{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles"}, Verbs: []string{"bind", "escalate"}}, []string{"escalate", "bind"}},
		{Rule{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}, nil},
	}
	for _, c := range cases {
		if got := c.r.Flags(); !slices.Equal(got, c.want) {
			t.Errorf("Flags(%+v) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestCompare(t *testing.T) {
	shipped := Definition(Admin, Full, Options{}).Rules
	if d := Compare(shipped, Normalized(shipped)); !d.Empty() {
		t.Errorf("same rules differ: %+v", d)
	}
	widened := append(slices.Clone(shipped), Rule{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"delete"}})
	d := Compare(shipped, widened)
	if !d.MoreThanShipped() || len(d.Extra) != 1 || d.Extra[0] != "delete nodes" {
		t.Errorf("widened = %+v", d)
	}
	narrowed := shipped[1:]
	d = Compare(shipped, narrowed)
	if d.MoreThanShipped() || len(d.Missing) == 0 {
		t.Errorf("narrowed = %+v", d)
	}
	if lines := d.Lines(); len(lines) == 0 || !strings.Contains(lines[0], "lacks") {
		t.Errorf("lines = %v", lines)
	}
}
