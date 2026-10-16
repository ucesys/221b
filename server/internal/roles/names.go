package roles

import (
	"slices"
	"strings"
)

// Source is where a person or group comes from (RB-15).
type Source string

// The sources. The value is what the API calls them.
const (
	Local Source = "local"
	LDAP  Source = "ldap"
	OIDC  Source = "oidc"
)

// Prefixes are put before the names Kubernetes sees (RB-15). Local and LDAP
// have fixed ones; OIDC keeps the API servers' own.
type Prefixes struct {
	Local string
	LDAP  string
	// OIDCUser and OIDCGroup are auth.kubernetes.userPrefix and groupPrefix.
	OIDCUser  string
	OIDCGroup string
}

// DefaultPrefixes are the chart's.
func DefaultPrefixes() Prefixes { return Prefixes{Local: "221b-local:", LDAP: "221b-ldap:"} }

func (p Prefixes) of(s Source, group bool) string {
	switch s {
	case Local:
		return p.Local
	case LDAP:
		return p.LDAP
	}
	if group {
		return p.OIDCGroup
	}
	return p.OIDCUser
}

// User is a person's name as Kubernetes sees it.
func (p Prefixes) User(s Source, name string) string { return p.of(s, false) + name }

// Group is a group's name as Kubernetes sees it.
func (p Prefixes) Group(s Source, name string) string { return p.of(s, true) + name }

// The reserved groups (RB-05, RB-07). 221B adds them; no source can.
const (
	GroupConsoleUsers = "221b:console-users"
	GroupSuperAdmins  = "221b:superadmins"
	GroupSuperViewers = "221b:superviewers"
)

// SuperGroup is the reserved group a super role puts a person in.
func SuperGroup(id ID) string {
	switch id {
	case SuperAdmin:
		return GroupSuperAdmins
	case SuperViewer:
		return GroupSuperViewers
	}
	return ""
}

// Reserved reports whether a name from any source may not be used (RB-17):
// it starts with system: or 221b:, or already carries a prefix of 221B's.
func (p Prefixes) Reserved(name string) bool {
	if strings.HasPrefix(name, "system:") || strings.HasPrefix(name, "221b:") {
		return true
	}
	for _, pre := range []string{p.Local, p.LDAP, "221b-local:", "221b-ldap:"} {
		if pre != "" && strings.HasPrefix(name, pre) {
			return true
		}
	}
	return false
}

// ValidUsername reports whether a local user name is allowed (RB-16):
// lowercase letters, digits, dot, dash and underscore. Users defined in
// values keep their email as the name, so the one place that reads them
// passes strict=false and allows an @ as well.
func ValidUsername(name string, strict bool) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		case c == '@' && !strict:
		default:
			return false
		}
	}
	return true
}

// ValidGroupName reports whether a local group's name is allowed.
func ValidGroupName(name string) bool { return ValidUsername(name, true) }

// Identity is what 221B knows about a signed-in person's groups.
type Identity struct {
	Source Source
	// Groups are the names the source gave, unprefixed: a token's groups
	// claim, a directory's groups, a local user's groups.
	Groups []string
	// Super are the roles held in 221B (RB-05).
	Super []ID
}

// GroupSet builds the groups 221B asserts for a person, in access reviews
// and in impersonated calls alike (RB-37). It can emit only these: a
// source's group that is allowed and not reserved, with its prefix; the
// reserved groups; never system:masters.
type GroupSet struct {
	Prefixes Prefixes
	// Allowed lists the groups, by source and unprefixed name, that values
	// name. A nil Allowed asserts every group the source gives, which is
	// how OIDC works today when no role lists are set.
	Allowed map[Source][]string
}

// Build returns the groups to assert, and the reserved names it dropped so
// that the caller can audit them (RB-17, RB-54).
func (g GroupSet) Build(id Identity) (groups, dropped []string) {
	for _, name := range id.Groups {
		switch {
		case name == "":
		case g.Prefixes.Reserved(name):
			dropped = append(dropped, name)
		case g.Allowed != nil && !slices.Contains(g.Allowed[id.Source], name):
		default:
			full := g.Prefixes.Group(id.Source, name)
			if full != "system:masters" && !slices.Contains(groups, full) {
				groups = append(groups, full)
			}
		}
	}
	groups = append(groups, GroupConsoleUsers)
	for _, r := range id.Super {
		if sg := SuperGroup(r); sg != "" && !slices.Contains(groups, sg) {
			groups = append(groups, sg)
		}
	}
	return groups, dropped
}

// Impersonable are the groups the chart lets the console impersonate when
// values list every group (RB-37): the listed ones with their prefixes, the
// reserved ones and system:authenticated.
func (g GroupSet) Impersonable() []string {
	out := []string{GroupConsoleUsers, GroupSuperAdmins, GroupSuperViewers, "system:authenticated"}
	for src, names := range g.Allowed {
		for _, n := range names {
			out = append(out, g.Prefixes.Group(src, n))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// LocalGroupName is the subject of a local group in a binding.
func (p Prefixes) LocalGroupName(name string) string { return p.Group(Local, name) }
