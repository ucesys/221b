package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
)

// The Audit log page (Administration › Audit log, asked October 30,
// 2026): the newest events the console kept, for a SuperAdmin. The
// stream on standard output and the webhook stay the full record.

func (s *Server) auditRoutes() {
	s.handle("GET /api/v1/admin/audit", "admin-audit", s.getAudit)
}

// auditPage is what the page reads.
type auditPage struct {
	Events []audit.Event `json:"events"`
	// Total counts the events kept, before the filter and the limit.
	Total int `json:"total"`
	// Stored says whether events are kept at all: without the settings
	// ConfigMap (a laptop) they are not.
	Stored bool `json:"stored"`
	// Dropped counts events the webhook never received.
	Dropped int `json:"dropped"`
	// Actions lists every action among the kept events, for the filter.
	Actions []string `json:"actions"`
}

// matchesAudit says whether an event has the words, each somewhere in it.
func matchesAudit(e audit.Event, words []string) bool {
	if len(words) == 0 {
		return true
	}
	text := strings.ToLower(strings.Join([]string{e.Action, e.Who.Name, e.Who.Email, e.Who.Subject, e.Cluster, e.Target, e.Detail, e.Outcome, e.From}, " "))
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

func (s *Server) auditEvents(ctx context.Context) ([]audit.Event, error) {
	if s.opts.Audit == nil {
		return nil, nil
	}
	return s.opts.Audit.Events(ctx)
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	all, err := s.auditEvents(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, "reading the audit log: "+err.Error())
		return
	}
	q := r.URL.Query()
	words := strings.Fields(strings.ToLower(q.Get("q")))
	action, outcome := q.Get("action"), q.Get("outcome")
	limit := 500
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = min(n, 5000)
	}
	seen := map[string]bool{}
	page := auditPage{Events: []audit.Event{}, Total: len(all), Actions: []string{}}
	if s.opts.Audit != nil {
		page.Stored, page.Dropped = s.opts.Audit.Stored(), s.opts.Audit.Dropped()
	}
	for _, e := range all {
		if !seen[e.Action] {
			seen[e.Action] = true
			page.Actions = append(page.Actions, e.Action)
		}
		if action != "" && e.Action != action && !strings.HasPrefix(e.Action, action+".") {
			continue
		}
		if outcome != "" && e.Outcome != outcome {
			continue
		}
		if !matchesAudit(e, words) {
			continue
		}
		if len(page.Events) < limit {
			page.Events = append(page.Events, e)
		}
	}
	b, _ := json.Marshal(page)
	writeJSON(w, r, b)
}
