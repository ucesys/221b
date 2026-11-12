package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ucesys/221b/server/internal/roles"
)

// The Reporter role (Phase 12, IS-20) comes from issues.reporterGroups for
// a directory or a provider, and from a local group of the role reporter
// for local users; it is not one of the four, sends no group of its own to
// Kubernetes, and adds to a person's other roles.
func TestTheReporterRoleComesFromItsGroups(t *testing.T) {
	cfg := Config{Mode: ModeLDAP, Lists: RoleLists{roles.Viewer: {"readers"}}, ReporterGroups: []string{"staff"},
		LocalGroups: []LocalGroup{{Name: "helpdesk", Role: roles.Reporter}}, Prefixes: roles.DefaultPrefixes()}
	for _, c := range []struct {
		src    roles.Source
		groups []string
		want   string
	}{
		{roles.LDAP, []string{"staff"}, "[reporter]"},
		{roles.LDAP, []string{"staff", "readers"}, "[viewer reporter]"},
		{roles.LDAP, []string{"others"}, "[]"},
		{roles.OIDC, []string{"staff"}, "[reporter]"},
		{roles.Local, []string{"helpdesk"}, "[reporter]"},
	} {
		g := GrantFor(cfg, c.src, User{Username: "pat", Groups: c.groups}, nil)
		if fmt.Sprint(g.Roles) != c.want {
			t.Errorf("%s %v: %v, want %s", c.src, c.groups, g.Roles, c.want)
		}
		if g.Admin() || len(g.Super) > 0 {
			t.Errorf("%v: a super role", c.groups)
		}
		for _, k := range g.KubeGroups {
			if k == "221b-ldap:staff" {
				t.Errorf("the reporters' group was sent to Kubernetes: %v", g.KubeGroups)
			}
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	os.WriteFile(path, []byte(`{"lists":{},"localGroups":[{"name":"helpdesk","role":"reporter"}],"reporterGroups":["staff",""]}`), 0o600)
	if _, err := ReadFileConfig(path); err == nil {
		t.Fatal("an empty reporters' group was taken")
	}
	os.WriteFile(path, []byte(`{"lists":{},"localGroups":[{"name":"helpdesk","role":"reporter"}],"reporterGroups":["staff"]}`), 0o600)
	f, err := ReadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	f.Apply(&c)
	if fmt.Sprint(c.ReporterGroups) != "[staff]" {
		t.Fatalf("%v", c.ReporterGroups)
	}
}
