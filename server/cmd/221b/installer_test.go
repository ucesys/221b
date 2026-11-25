package main

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

// The Star us on GitHub dialog asks the SuperAdmins the Helm values name,
// never one made in the console (CU-21). Without the local accounts the
// first-run page's is not known; the next test has it.
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
	is := installerOf(cfg, nil)
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

// In local mode the SuperAdmin the first-run page created installed the
// console and is asked as well; a SuperAdmin made later in the console is
// not, nor the first-run one once it is no longer a SuperAdmin (CU-21).
func TestTheFirstRunSuperAdminIsAnInstaller(t *testing.T) {
	ctx := context.Background()
	backend := &auth.MemoryBackend{}
	acc := auth.NewAccounts(backend, nil, nil, false, nil)
	if err := acc.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Setup(ctx, auth.NewAccount{Username: "first", Password: "a long password 1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Create(ctx, auth.NewAccount{Username: "later", Password: "a long password 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.SetSuper(ctx, "later", []roles.ID{roles.SuperAdmin}); err != nil {
		t.Fatal(err)
	}
	// The mark is saved with the account: another replica, or a restart,
	// reads it back.
	again := auth.NewAccounts(backend, nil, nil, false, nil)
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := auth.Config{Mode: auth.ModeLocal}
	is := installerOf(cfg, func() *auth.Accounts { return again })
	local := func(name string, admin bool) auth.Session {
		return auth.Session{Admin: admin, Method: "local", User: auth.User{Subject: "local:" + name, Username: name}}
	}
	if !is(local("first", true)) {
		t.Error("the first-run SuperAdmin is not asked")
	}
	if is(local("later", true)) {
		t.Error("a SuperAdmin made later in the console is asked")
	}
	if is(local("first", false)) {
		t.Error("the first-run account is asked though no longer a SuperAdmin")
	}
	if is(auth.Session{Admin: true, Method: "oidc", User: auth.User{Username: "first"}}) {
		t.Error("an OIDC person of the same name is asked")
	}
	if installerOf(cfg, nil)(local("first", true)) || installerOf(cfg, func() *auth.Accounts { return nil })(local("first", true)) {
		t.Error("asked without local accounts")
	}
}
