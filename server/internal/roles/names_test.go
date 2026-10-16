package roles

import (
	"slices"
	"testing"
)

func TestPrefixes(t *testing.T) {
	p := Prefixes{Local: "221b-local:", LDAP: "221b-ldap:", OIDCUser: "oidc:", OIDCGroup: "oidc-g:"}
	if got := p.User(Local, "alice"); got != "221b-local:alice" {
		t.Error(got)
	}
	if p.User(LDAP, "alice") == p.User(Local, "alice") {
		t.Error("local and LDAP alice are the same subject")
	}
	if got := p.Group(OIDC, "ops"); got != "oidc-g:ops" {
		t.Error(got)
	}
}

func TestReserved(t *testing.T) {
	p := DefaultPrefixes()
	for _, n := range []string{"system:masters", "system:authenticated", "221b:superadmins", "221b-local:x", "221b-ldap:y"} {
		if !p.Reserved(n) {
			t.Errorf("%s is not reserved", n)
		}
	}
	for _, n := range []string{"ops", "Platform Admins", "systemic", "221b", "team-221b:x"} {
		if p.Reserved(n) {
			t.Errorf("%s is reserved", n)
		}
	}
}

func TestUsernames(t *testing.T) {
	for _, n := range []string{"alice", "a.b-c_d", "x1"} {
		if !ValidUsername(n, true) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "Alice", "a b", "a:b", "system:masters", "a@b.c"} {
		if ValidUsername(n, true) {
			t.Errorf("%q allowed", n)
		}
	}
	if !ValidUsername("a@b.c", false) {
		t.Error("an email is refused for a user from values")
	}
}

func TestBuildAddsReservedAndPrefixes(t *testing.T) {
	g := GroupSet{Prefixes: DefaultPrefixes(), Allowed: map[Source][]string{LDAP: {"ops", "admins"}}}
	got, dropped := g.Build(Identity{Source: LDAP, Groups: []string{"ops", "other", "221b:superadmins", "system:masters"}, Super: []ID{SuperViewer}})
	want := []string{"221b-ldap:ops", GroupConsoleUsers, GroupSuperViewers}
	if !slices.Equal(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
	if !slices.Equal(dropped, []string{"221b:superadmins", "system:masters"}) {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestBuildNeverEmitsMasters(t *testing.T) {
	// Even with no list, and even through a prefix that makes the name.
	for _, p := range []Prefixes{DefaultPrefixes(), {OIDCGroup: "system:", Local: "221b-local:", LDAP: "221b-ldap:"}} {
		g := GroupSet{Prefixes: p}
		got, _ := g.Build(Identity{Source: OIDC, Groups: []string{"masters", "system:masters", "ops"}})
		if slices.Contains(got, "system:masters") {
			t.Errorf("emitted system:masters with %+v: %v", p, got)
		}
	}
}

func TestBuildOnlyListedGroups(t *testing.T) {
	g := GroupSet{Prefixes: DefaultPrefixes(), Allowed: map[Source][]string{Local: {"web-readers"}}}
	got, _ := g.Build(Identity{Source: Local, Groups: []string{"web-readers", "secret-club"}})
	if !slices.Equal(got, []string{"221b-local:web-readers", GroupConsoleUsers}) {
		t.Errorf("got %v", got)
	}
	// With no list, as OIDC without role lists, the source's groups pass.
	got, _ = GroupSet{Prefixes: Prefixes{OIDCGroup: "oidc:"}}.Build(Identity{Source: OIDC, Groups: []string{"anything"}})
	if !slices.Contains(got, "oidc:anything") {
		t.Errorf("got %v", got)
	}
}

func TestImpersonable(t *testing.T) {
	g := GroupSet{Prefixes: DefaultPrefixes(), Allowed: map[Source][]string{LDAP: {"ops"}, Local: {"ops"}}}
	got := g.Impersonable()
	for _, w := range []string{"221b-ldap:ops", "221b-local:ops", GroupConsoleUsers, GroupSuperAdmins, GroupSuperViewers, "system:authenticated"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %s in %v", w, got)
		}
	}
	if slices.Contains(got, "system:masters") {
		t.Error("system:masters is impersonable")
	}
}
