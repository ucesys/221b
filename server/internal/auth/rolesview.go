package auth

import (
	"slices"

	"github.com/ucesys/221b/server/internal/roles"
)

// What the Roles page asks of the sign-in side (RB-41): who 221B holds the
// super roles for, how many local users a local group has, and whether a
// local user exists, so that a binding to a deleted user shows as orphaned.

// Holders counts, for each super role, the people and groups 221B holds it
// for: local accounts that are enabled, the usernames in the deployment's
// admin list, and the groups listed for the role.
func (s *Service) Holders() map[roles.ID]roles.Holders {
	out := map[roles.ID]roles.Holders{}
	for _, id := range []roles.ID{roles.SuperAdmin, roles.SuperViewer} {
		h := roles.Holders{Groups: len(s.cfg.Lists[id])}
		if id == roles.SuperAdmin {
			h.Groups += len(s.cfg.AdminGroups)
			h.Users += len(s.cfg.Admins)
		}
		if s.accounts != nil {
			for _, a := range s.accounts.List() {
				if !a.Disabled && slices.Contains(a.Super, id) {
					h.Users++
				}
			}
		}
		out[id] = h
	}
	return out
}

// Members counts the local users in a local group.
func (s *Service) Members(group string) int {
	if s.accounts == nil {
		return 0
	}
	return s.accounts.Members(group)
}

// UserExists says whether a local user of that name exists.
func (s *Service) UserExists(username string) bool {
	if s.accounts == nil {
		return false
	}
	_, ok := s.accounts.Find(username)
	return ok
}

// LocalSuperAdmin says whether a local account may still act as a
// SuperAdmin: it exists, is enabled, and is one by its roles, its groups
// or the admin list as they stand now.
func (s *Service) LocalSuperAdmin(username string) bool {
	if s == nil || s.accounts == nil {
		return false
	}
	a, ok := s.accounts.Find(username)
	return ok && !a.Disabled && s.accountAdmin(a)
}
