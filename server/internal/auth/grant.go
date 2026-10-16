package auth

import (
	"slices"
	"strings"

	"github.com/ucesys/221b/server/internal/roles"
)

// RoleLists are the groups of each role, for a directory or an OIDC
// provider (RB-20): the names as the source has them, without a prefix.
type RoleLists map[roles.ID][]string

// Empty reports whether no group is listed.
func (l RoleLists) Empty() bool {
	for _, g := range l {
		if len(g) > 0 {
			return false
		}
	}
	return true
}

// Listed are all the listed groups, each once.
func (l RoleLists) Listed() []string {
	var out []string
	for _, id := range roles.All {
		for _, g := range l[id] {
			if !slices.Contains(out, g) {
				out = append(out, g)
			}
		}
	}
	return out
}

// Grant is what a person may be in 221B and what the clusters call them.
type Grant struct {
	// Roles are every role held, from the most to the least privileged.
	Roles []roles.ID
	// Super are the ones held in 221B: SuperAdmin and SuperViewer.
	Super []roles.ID
	// KubeName and KubeGroups are sent to the clusters (RB-15, RB-37).
	KubeName   string
	KubeGroups []string
	// Dropped are reserved group names the source gave, refused (RB-17).
	Dropped []string
}

// Admin reports whether the person is a SuperAdmin.
func (g Grant) Admin() bool { return slices.Contains(g.Super, roles.SuperAdmin) }

// Held reports whether the person holds a role.
func (g Grant) Held(id roles.ID) bool { return slices.Contains(g.Roles, id) }

// allowed lists the groups 221B may assert for each source (RB-37). A nil
// entry means no limit, which is how OIDC works when values list none.
func (s *Service) allowed() map[roles.Source][]string {
	out := map[roles.Source][]string{}
	var local []string
	for _, g := range s.cfg.LocalGroups {
		local = append(local, g.Name)
	}
	out[roles.Local] = local
	listed := s.cfg.Lists.Listed()
	switch s.cfg.Mode {
	case ModeLDAP:
		out[roles.LDAP] = listed
	case ModeOIDC:
		if !s.cfg.Lists.Empty() {
			out[roles.OIDC] = append(listed, s.cfg.AdminGroups...)
		}
	}
	return out
}

// sourceOf says where the people of this deployment come from.
func (s *Service) sourceOf(method string) roles.Source {
	switch method {
	case string(ModeLocal):
		return roles.Local
	case string(ModeLDAP):
		return roles.LDAP
	}
	return roles.OIDC
}

// GroupSet is what builds the groups asserted for a person.
func (s *Service) groupSet() roles.GroupSet {
	return roles.GroupSet{Prefixes: s.cfg.Prefixes, Allowed: s.allowed()}
}

// grant works out the roles of a person and the names the clusters know
// them by. Groups are the source's: a token's, a directory's, a local
// user's. supers are the roles given in 221B (a local account's).
func (s *Service) grant(src roles.Source, u User, supers []roles.ID) Grant {
	held := map[roles.ID]bool{}
	for _, r := range supers {
		held[r] = true
	}
	switch src {
	case roles.Local:
		for _, name := range u.Groups {
			for _, g := range s.cfg.LocalGroups {
				if g.Name == name {
					held[g.Role] = true
				}
			}
		}
	default:
		for id, groups := range s.cfg.Lists {
			for _, g := range groups {
				if slices.Contains(u.Groups, g) {
					held[id] = true
				}
			}
		}
	}
	if s.isAdmin(u) {
		held[roles.SuperAdmin] = true
	}
	var out Grant
	for _, id := range roles.All {
		if held[id] {
			out.Roles = append(out.Roles, id)
			if id.Super() {
				out.Super = append(out.Super, id)
			}
		}
	}
	out.KubeName = s.cfg.Prefixes.User(src, u.Username)
	out.KubeGroups, out.Dropped = s.groupSet().Build(roles.Identity{Source: src, Groups: u.Groups, Super: out.Super})
	return out
}

// apply puts a grant on a session.
func (g Grant) apply(sess *Session) {
	sess.Roles, sess.Super, sess.Admin = g.Roles, g.Super, g.Admin()
	sess.User.KubeName, sess.User.KubeGroups = g.KubeName, g.KubeGroups
}

// Login normalizes what a person typed as their name.
func Login(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// GrantFor works out the roles and cluster names of a person under a
// configuration, without a service: what the sign-in would give them. It is
// exported for tests and for the Permissions and Roles pages.
func GrantFor(cfg Config, src roles.Source, u User, supers []roles.ID) Grant {
	s := &Service{cfg: cfg}
	return s.grant(src, u, supers)
}
