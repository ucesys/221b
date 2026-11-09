package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/roles"
)

// Mycroft's knowledge (Phase 12, ADR-94 to ADR-98) has an API of its own,
// mounted here with who is calling: SuperAdmins change the sources, the
// store and the Confluence publisher; Admins and SuperAdmins review the
// wiki's changes; anyone signed in to the console reads the approved
// pages. Every route runs on the leader, where the proxy forwards it.

func (s *Server) knowledgeRoutes() {
	h := s.opts.Knowledge.Handler(s.knowledgeCaller)
	for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
		s.handle(m+" "+knowledge.Prefix, "knowledge", h.ServeHTTP)
	}
}

// knowledgeCaller is the person a request is from, as the knowledge API
// needs them.
func (s *Server) knowledgeCaller(r *http.Request) (knowledge.Caller, bool) {
	if s.opts.Auth == nil {
		return knowledge.Caller{ID: "anonymous", Name: "Anonymous", SuperAdmin: true, Reviewer: true, Reader: true, From: fromAddr(r)}, true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return knowledge.Caller{}, false
	}
	u := sess.User
	id := u.Username
	if id == "" {
		id = u.Subject
	}
	c := knowledge.Caller{ID: id, Name: u.Name, Email: u.Email, SuperAdmin: sess.Admin, From: fromAddr(r)}
	c.Reviewer = sess.Admin || slices.Contains(sess.Roles, roles.Admin) || slices.Contains(sess.Roles, roles.SuperAdmin)
	c.Reader = slices.ContainsFunc(sess.Roles, func(id roles.ID) bool { return slices.Contains(roles.All, id) }) || sess.Admin
	return c, true
}

// knowledgeSummary is Settings › AI's line on Mycroft's knowledge.
func (s *Server) knowledgeSummary() string {
	k := s.opts.Knowledge
	if k == nil {
		return "Mycroft's knowledge is not offered here; ask_knowledge answers that no knowledge is connected."
	}
	pages := k.Pages()
	if len(pages) == 0 {
		return "No approved wiki pages yet: connect Mycroft's sources and choose where the wiki is kept, in Knowledge."
	}
	stale := 0
	for _, p := range pages {
		if p.Stale {
			stale++
		}
	}
	return fmt.Sprintf("%d approved wiki pages, %d of them stale; agents ask them with ask_knowledge.", len(pages), stale)
}
