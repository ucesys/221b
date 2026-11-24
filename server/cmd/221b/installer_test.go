package main

import (
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

// The Star us on GitHub dialog asks the SuperAdmins the Helm values name,
// never one made in the console (CU-21).
func TestTheInstallersAreTheValuesSuperAdmins(t *testing.T) {
	cfg := auth.Config{
		Admins:      []string{"ann@example.com", "sub-42"},
		AdminGroups: []string{"platform"},
		Lists:       auth.RoleLists{roles.SuperAdmin: {"ldap-admins"}, roles.Admin: {"ops"}},
		Local: []auth.LocalUser{
			{Email: "Ops@Example.com", Roles: []string{"superAdmin"}},
			{Email: "viewer@example.com", Roles: []string{"superViewer"}},
		},
	}
	is := installerOf(cfg)
	for _, c := range []struct {
		name string
		sess auth.Session
		want bool
	}{
		{"in auth.admins by email", auth.Session{Admin: true, Method: "oidc", User: auth.User{Username: "ann", Email: "ANN@example.com"}}, true},
		{"in auth.admins by subject", auth.Session{Admin: true, Method: "oidc", User: auth.User{Username: "bob", Subject: "sub-42"}}, true},
		{"in auth.adminGroups", auth.Session{Admin: true, Method: "oidc", User: auth.User{Username: "cid", Groups: []string{"platform"}}}, true},
		{"a group given superAdmin in the values", auth.Session{Admin: true, Method: "ldap", User: auth.User{Username: "dee", Groups: []string{"ldap-admins"}}}, true},
		{"a local user of the values with superAdmin", auth.Session{Admin: true, Method: "local", User: auth.User{Username: "ops@example.com"}}, true},
		{"a local account made a SuperAdmin in the console", auth.Session{Admin: true, Method: "local", User: auth.User{Username: "eve@example.com"}}, false},
		{"a local group named as an LDAP role's", auth.Session{Admin: true, Method: "local", User: auth.User{Username: "fay", Groups: []string{"ldap-admins"}}}, false},
		{"not a SuperAdmin now", auth.Session{Admin: false, Method: "oidc", User: auth.User{Email: "ann@example.com"}}, false},
		{"a group of another role", auth.Session{Admin: true, Method: "oidc", User: auth.User{Username: "gus", Groups: []string{"ops"}}}, false},
	} {
		if got := is(c.sess); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
