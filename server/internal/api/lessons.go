package api

import (
	"net/http"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- Findings, exceptions and RCAs (Phase 12, FX-06, FX-10) ---
//
// The accepted exceptions, served by package lessons under
// /api/v1/cases/exceptions for AIOps › Mycroft (an RCA is its case's
// since wave 6e, CF-44).
// Those who see the cases read them: the approvers (cases.approverRoles),
// the console-wide roles and the job editors; the approvers remove an
// exception.

func (s *Server) lessonsRoutes() {
	for _, rt := range s.opts.Lessons.Routes(s.lessonsCaller) {
		s.handle(rt.Pattern, rt.Name, rt.Handler)
	}
	// What New exception suggests as the object is typed (caseobjects.go).
	s.handle("GET /api/v1/cases/objects", "cases-objects", s.caseObjects)
}

func (s *Server) lessonsCaller(r *http.Request) (lessons.Caller, bool) {
	if s.opts.Auth == nil {
		return lessons.Caller{Person: lessons.Person{Name: "anonymous", From: fromAddr(r)}, Read: true, Change: true}, true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return lessons.Caller{}, false
	}
	c := lessons.Caller{Person: lessons.Person{Subject: sess.User.Subject, Email: sess.User.Email, Name: firstOf(sess.User.Name, sess.User.Username), From: fromAddr(r)}}
	c.Change = s.mayApproveCards(r)
	c.Read = c.Change || sess.Admin
	var held []string
	for _, id := range sess.Roles {
		held = append(held, string(id))
		c.Read = c.Read || id.Super()
	}
	if !c.Read && s.opts.Jobs != nil {
		if set, err := s.opts.Jobs.Settings(r.Context()); err == nil {
			c.Read = set.MayEdit(false, held)
		}
	}
	return c, true
}

// readsExceptions says whether the person asking may read the accepted
// exceptions, as Mycroft › Exceptions decides: Sherlock's ask_knowledge
// gives them only then (KN-32).
func (s *Server) readsExceptions(r *http.Request) bool {
	c, ok := s.lessonsCaller(r)
	return ok && (c.Read || c.Change)
}
