package auth

import (
	"fmt"
	"slices"

	"github.com/ucesys/221b/server/internal/roles"
)

// The reader identity (Phase 12, JB-08): an agent asked by a person reads
// as that person; an agent on its own, started by a job with nobody at the
// screen, reads as this identity, with a read-only role a SuperAdmin
// chooses (Viewer by default). Its name is reserved, so no source can sign
// a person in as it, and it holds no role that may change anything.

// ReaderName is the name the clusters know agents on their own by: bind a
// role to this user to let them read a namespace.
const ReaderName = "221b:agents"

// Reader is the session agents on their own read with: the role viewer or
// superViewer, and for viewer, optionally, a group of the role Viewer
// whose namespaces they then read too. It says false when sign-in is off
// and the console reads as itself.
func (s *Service) Reader(role roles.ID, group string) (Session, bool, error) {
	if s.cfg.Mode == ModeNone {
		return Session{}, false, nil
	}
	if role != roles.Viewer && role != roles.SuperViewer {
		return Session{}, false, fmt.Errorf("agents on their own read as a Viewer or a SuperViewer, not %s", role.Name())
	}
	var supers []roles.ID
	if role == roles.SuperViewer {
		supers, group = []roles.ID{roles.SuperViewer}, ""
	}
	if group != "" && !slices.Contains(s.ReaderGroups(), group) {
		return Session{}, false, fmt.Errorf("the group %q is not a group of the role Viewer", group)
	}
	var groups []string
	if group != "" {
		groups = []string{group}
	}
	src := s.sourceOf(string(s.cfg.Mode))
	g := s.grant(src, User{Username: ReaderName, Groups: groups}, supers)
	held := []roles.ID{role}
	for _, r := range g.Roles {
		if r != roles.Viewer && r != roles.SuperViewer {
			// A group that also gives a role that may change anything is
			// not one to read with.
			return Session{}, false, fmt.Errorf("the group %q gives the role %s, which may change things", group, r.Name())
		}
		if !slices.Contains(held, r) {
			held = append(held, r)
		}
	}
	sess := Session{ID: "agents", Method: "agent", Roles: held, Super: supers,
		User: User{Subject: ReaderName, Username: ReaderName, Name: "Agents on their own", Groups: groups, KubeName: ReaderName, KubeGroups: g.KubeGroups}}
	return sess, true, nil
}

// ReaderGroups are the groups agents on their own may read with: those
// whose role is Viewer: the local groups of that role with local sign-in,
// the groups values list for it with a directory or OIDC.
func (s *Service) ReaderGroups() []string {
	var out []string
	switch s.cfg.Mode {
	case ModeLocal:
		for _, g := range s.cfg.LocalGroups {
			if g.Role == roles.Viewer {
				out = append(out, g.Name)
			}
		}
	case ModeLDAP, ModeOIDC:
		out = append(out, s.cfg.Lists[roles.Viewer]...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
