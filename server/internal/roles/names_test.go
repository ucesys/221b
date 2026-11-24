package roles

import (
	"slices"
	"strings"
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
	// A prefix that makes masters system:masters (refused at start, and
	// still filtered here), so the last check below can fail.
	p := DefaultPrefixes()
	p.OIDCGroup = "system:"
	g := GroupSet{Prefixes: p, Allowed: map[Source][]string{LDAP: {"ops"}, Local: {"ops"}, OIDC: {"masters", " padded"}}}
	got := g.Impersonable()
	for _, w := range []string{"221b-ldap:ops", "221b-local:ops", GroupConsoleUsers, GroupSuperAdmins, GroupSuperViewers, "system:authenticated"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %s in %v", w, got)
		}
	}
	if slices.Contains(got, "system:masters") || slices.Contains(got, "system: padded") {
		t.Errorf("a reserved name is impersonable: %v", got)
	}
}

// Finding 1 of the roles review: a name padded with white space reached the
// API server as the reserved one, since HTTP/1.1 trims header values.
func TestBuildDropsPaddedAndControlNames(t *testing.T) {
	p := Prefixes{Local: "221b-local:", LDAP: "221b-ldap:"}
	for _, n := range []string{" system:masters", "221b:superadmins ", "\tsystem:masters", "ops\n", "o\x00ps", " 221b:superviewers"} {
		if !p.Reserved(n) {
			t.Errorf("%q is not reserved", n)
		}
	}
	if p.Reserved("Domain Users") {
		t.Error("a space inside a name is refused")
	}
	got, dropped := GroupSet{Prefixes: p}.Build(Identity{Source: OIDC, Groups: []string{" system:masters", " 221b:superadmins", "ops"}})
	if !slices.Equal(got, []string{"ops", GroupConsoleUsers}) {
		t.Errorf("groups = %q", got)
	}
	if !slices.Equal(dropped, []string{" system:masters", " 221b:superadmins"}) {
		t.Errorf("dropped = %q", dropped)
	}
}

// Finding 2 of the roles review: a prefix that starts a reserved name space
// made an ordinary group a reserved one.
func TestBuildDropsNamesAPrefixMakesReserved(t *testing.T) {
	for _, p := range []Prefixes{{Local: "221b:", LDAP: "221b-ldap:"}, {Local: "221b-local:", LDAP: "system:"}, {Local: "221b-local:", LDAP: "221b-ldap:", OIDCGroup: "221b:"}, {Local: "221b-local:", LDAP: "221b-ldap:", OIDCGroup: "sys"}} {
		for _, src := range []Source{Local, LDAP, OIDC} {
			got, _ := GroupSet{Prefixes: p}.Build(Identity{Source: src, Groups: []string{"superadmins", "masters", "tem:masters"}})
			for _, g := range got {
				if g != GroupConsoleUsers && inReservedSpace(g) {
					t.Errorf("%+v, %s: asserted %s", p, src, g)
				}
			}
		}
		if p.Check() == nil {
			t.Errorf("%+v passes the check", p)
		}
	}
	_, dropped := GroupSet{Prefixes: Prefixes{Local: "221b:"}}.Build(Identity{Source: Local, Groups: []string{"superadmins"}})
	if !slices.Equal(dropped, []string{"221b:superadmins"}) {
		t.Errorf("dropped = %q", dropped)
	}
	for _, p := range []Prefixes{DefaultPrefixes(), {Local: "corp-local:", LDAP: "corp:", OIDCUser: "oidc:", OIDCGroup: "oidc:"}, {Local: "221b-local:", LDAP: "221b-ldap:", OIDCUser: "-"}} {
		if err := p.Check(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	if (Prefixes{OIDCUser: "oidc: "}).Check() == nil {
		t.Error("a padded prefix passes")
	}
}

// Roles review, finding 8: a user from values keeps an email as the name,
// and the check refused a '+' and anything past 64 characters, so a chart
// that rendered cleanly stopped the console at start.
func TestUsernamesFromValuesTakeAnEmail(t *testing.T) {
	long := strings.Repeat("a", 70) + "@corp.example"
	for _, n := range []string{"ops+k8s@corp.example", long} {
		if !ValidUsername(n, false) {
			t.Errorf("%q from values refused", n)
		}
		if ValidUsername(n, true) {
			t.Errorf("%q made in the console allowed", n)
		}
	}
	if ValidUsername(strings.Repeat("a", 255), false) || ValidUsername("a b@c", false) {
		t.Error("too long, or a space, allowed")
	}
	if ValidGroupName(strings.Repeat("g", 65)) {
		t.Error("a 65-character group name allowed")
	}
}

// Roles review, cut list: "." and ".." were valid user names, which a
// browser's per-user URL (/api/v1/admin/users/local:..) folds away, so such
// an account could not be disabled or deleted from the console.
func TestDotNamesAreNotUserNames(t *testing.T) {
	for _, n := range []string{".", ".."} {
		if ValidUsername(n, true) || ValidUsername(n, false) || ValidGroupName(n) {
			t.Errorf("%q allowed", n)
		}
	}
	if !ValidUsername("a.b", true) || !ValidUsername("...", true) {
		t.Error("a name with dots refused")
	}
}
