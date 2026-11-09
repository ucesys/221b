package api

import (
	"net/http"
	"slices"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- Case work (Phase 12, CW-10) ---
//
// The cases, served by package casework under /api/v1/cases/cases. The
// people who may approve proposed changes (cases.approverRoles, and a
// SuperAdmin) and, since the reports became cases (Phase 12, wave 5,
// CU-11), those who triage reports (issues.operatorRoles) work the
// cases; the console-wide roles and those who may edit jobs read them.

func (s *Server) caseworkRoutes() {
	for _, rt := range s.opts.Casework.Routes(s.caseworkCaller) {
		s.handle(rt.Pattern, rt.Name, rt.Handler)
	}
	// --- Troubleshooting a case in its drawer (wave 6f, CF-47, CF-48):
	// Sherlock's conversation on a case is kept with the case, and
	// forgotten when it closes; Watson's commands from a case are logged
	// there as they end.
	if s.opts.Sherlock != nil && s.opts.Settings != nil {
		s.opts.Sherlock.AddKeeper(s.opts.Casework.Chats())
		s.opts.Casework.SetForget(s.opts.Sherlock.ForgetSession)
		s.caseTroubleRoutes()
	}
	if s.opts.Watson != nil {
		s.opts.Watson.AddOnFinish(s.caseCommandEnded)
	}
}

// caseworkCaller is who a request is from, as the cases need them.
func (s *Server) caseworkCaller(r *http.Request) (casework.Caller, bool) {
	if s.opts.Auth == nil {
		return casework.Caller{Person: jobs.Person{Name: "anonymous", From: fromAddr(r)}, ID: "anonymous", Approver: true, Approve: true, Read: true, Admin: true}, true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return casework.Caller{}, false
	}
	c := casework.Caller{Person: jobs.Person{Subject: sess.User.Subject, Email: sess.User.Email, Name: firstOf(sess.User.Name, sess.User.Username), From: fromAddr(r)},
		ID: firstOf(sess.User.Username, sess.User.Subject), Approver: s.mayApproveCards(r), Admin: sess.Admin}
	// Accepting or removing an exception asks the approval permission
	// (cases.approverRoles, CF-03); triaging reports alone does not.
	c.Approve = c.Approver
	var held []string
	for _, id := range sess.Roles {
		held = append(held, string(id))
		c.Read = c.Read || id.Super()
		c.Approver = c.Approver || slices.Contains(s.opts.IssueRoles.Operate, id)
	}
	if !c.Read && s.opts.Jobs != nil {
		if set, err := s.opts.Jobs.Settings(r.Context()); err == nil {
			c.Read = set.MayEdit(sess.Admin, held)
		}
	}
	return c, true
}

// --- end of case work.
