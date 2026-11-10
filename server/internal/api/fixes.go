package api

import (
	"net/http"

	"github.com/ucesys/221b/server/internal/fixes"
)

// --- Fixes (Phase 12, wave 6g, CF-51) ---
//
// The fixes, served by package fixes under /api/v1/cases/fixes for AIOps ›
// Mycroft › Fixes. Who works the cases changes them, as the cases; who
// reads the cases reads them.

func (s *Server) fixesRoutes() {
	for _, rt := range s.opts.Fixes.Routes(s.fixesCaller) {
		s.handle(rt.Pattern, rt.Name, rt.Handler)
	}
}

func (s *Server) fixesCaller(r *http.Request) (fixes.Caller, bool) {
	c, ok := s.caseworkCaller(r)
	if !ok {
		return fixes.Caller{}, false
	}
	return fixes.Caller{Person: fixes.Person{Subject: c.Subject, Email: c.Email, Name: c.Name}, Read: c.Read || c.Admin, Change: c.Approver}, true
}
