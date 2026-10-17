package auth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

func TestFileConfig(t *testing.T) {
	write := func(s string) string {
		p := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f, err := auth.ReadFileConfig(write(`{"lists":{"admin":["team-a"],"superAdmin":["platform"]},"localGroups":[{"name":"ops","role":"admin","namespaces":["web"]}],
		"templates":{"admin":"readOnly","superAdmin":"readOnly"},"rbacEnforce":"strict","prefixes":{"local":"x-local:"},
		"ldap":{"url":"ldaps://ldap.example","userSearch":{"base":"ou=people"},"groupSearch":{"base":"ou=groups","nested":true},"syncInterval":"30m"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var c auth.Config
	f.Apply(&c)
	if len(c.Lists[roles.Admin]) != 1 || c.LocalGroups[0].Name != "ops" || c.Prefixes.Local != "x-local:" || !f.RoleTemplates().ReadOnlyOnly() || f.RBACEnforce != "strict" {
		t.Fatalf("%+v %+v", f, c)
	}
	if d, err := f.LDAP.SyncEvery(); err != nil || d.Minutes() != 30 {
		t.Fatalf("sync every: %v %v", d, err)
	}
	if f, err := auth.ReadFileConfig(""); err != nil || f.RoleTemplates().Of(roles.Admin) != roles.Full {
		t.Fatalf("defaults: %+v %v", f, err)
	}
	for _, bad := range []string{
		`{"lists":{"owner":["x"]}}`, `{"lists":{"admin":[""]}}`, `{"templates":{"admin":"half"}}`, `{"rbacEnforce":"maybe"}`, `{"surprise":1}`, `not json`,
	} {
		if _, err := auth.ReadFileConfig(write(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if _, err := (auth.LDAPFile{SyncInterval: "5s"}).SyncEvery(); err == nil || !strings.Contains(err.Error(), "syncInterval") {
		t.Error("a sync every 5 seconds was accepted")
	}
}

func TestFileConfigBindings(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f, err := auth.ReadFileConfig(write(`{"bindings":[{"name":"221b-viewer-x-1","role":"viewer","subject":"221b-local:x","group":"x","namespace":"web"}]}`))
	if err != nil || len(f.Bindings) != 1 || f.Bindings[0].Subject != "221b-local:x" {
		t.Fatalf("%+v %v", f.Bindings, err)
	}
	for _, bad := range []string{
		`{"bindings":[{"name":"n","role":"superAdmin","subject":"g","namespace":"web"}]}`,
		`{"bindings":[{"name":"n","role":"viewer","subject":"g"}]}`,
	} {
		if _, err := auth.ReadFileConfig(write(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
