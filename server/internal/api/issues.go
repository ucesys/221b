package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/roles"
)

// The issue service (Phase 12, docs/issues.md) is a component of its own
// with its own API; this is where 221B mounts it and tells it who is
// calling. People report and follow their reports on the report page,
// operators triage through the same API, and a person who holds only the
// Reporter role sees nothing else of the console (IS-20).

// IssueRoles say which of the four roles may report problems and which
// triage them: the chart's issues.reportRoles and issues.operatorRoles
// (IS-20, IS-21). The Reporter role always reports.
type IssueRoles struct {
	Report  []roles.ID
	Operate []roles.ID
}

func (s *Server) issuesRoutes() {
	h := s.opts.Issues.Handler(s.issueCaller)
	// One pattern per method: "GET /api/" answers unknown paths, and a
	// pattern without a method would overlap it.
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		s.handle(m+" "+issues.Prefix, "issues", h.ServeHTTP)
	}
}

// issueCaller is the person a request is from, as the issue service needs
// them: their user name, and what their roles let them do there.
func (s *Server) issueCaller(r *http.Request) (issues.Caller, bool) {
	if s.opts.Auth == nil {
		// No sign-in at all (tests and the simulator): one person who may
		// do everything, as every other page treats them.
		return issues.Caller{Person: issues.Person{ID: "anonymous", Name: "Anonymous"}, Report: true, Operator: true, Admin: true, From: fromAddr(r)}, true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return issues.Caller{}, false
	}
	u := sess.User
	id := u.Username
	if id == "" {
		id = u.Subject
	}
	c := issues.Caller{Person: issues.Person{ID: id, Name: u.Name, Email: u.Email}, Admin: sess.Admin, From: fromAddr(r)}
	for _, held := range sess.Roles {
		c.Report = c.Report || held == roles.Reporter || slices.Contains(s.opts.IssueRoles.Report, held)
		c.Operator = c.Operator || slices.Contains(s.opts.IssueRoles.Operate, held)
	}
	return c, true
}

// onlyReporter says whether a person holds the Reporter role and none of
// the four.
func onlyReporter(held []roles.ID) bool {
	return slices.Contains(held, roles.Reporter) && !slices.ContainsFunc(held, func(id roles.ID) bool { return slices.Contains(roles.All, id) })
}

// reporterPath says whether a path is one a person who only reports may
// open: the report page and its files, the issue API, who they are, and
// signing in and out.
func reporterPath(p string) bool {
	switch {
	case p == "/report", p == "/report.html", strings.HasPrefix(p, "/report/"), strings.HasPrefix(p, "/api/issues/"):
		return true
	case p == "/api/v1/me", p == "/signout", p == "/signin", strings.HasPrefix(p, "/signin/"), p == "/signin.css", p == "/signin.js":
		return true
	case strings.HasPrefix(p, "/assets/"), p == "/favicon.svg", p == "/favicon.ico", p == "/manifest.webmanifest", p == "/healthz", p == "/readyz":
		return true
	}
	return false
}

// reporterOnly keeps a person who holds only the Reporter role on the
// report page (IS-20): the console's pages send them there, and its API
// refuses them. It says whether it answered.
func (s *Server) reporterOnly(w http.ResponseWriter, r *http.Request) bool {
	sess, ok := auth.SessionFrom(r.Context())
	if !ok || !onlyReporter(sess.Roles) || reporterPath(r.URL.Path) {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
		s.observeOnly("reporter-only", http.StatusForbidden)
		jsonError(w, http.StatusForbidden, "You may report problems and follow them, on the report page, and nothing else here.")
		return true
	}
	s.observeOnly("reporter-only", http.StatusFound)
	http.Redirect(w, r, "/report", http.StatusFound)
	return true
}
