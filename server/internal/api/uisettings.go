package api

import (
	"context"
	"net/http"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/settings"
)

// --- Show costs (Phase 12, wave 6c, CF-40) ---
//
// A SuperAdmin switch, Settings › AI › Show costs, on by default: off, no
// page shows a dollar amount to anyone (runs, jobs, spend, Sherlock's
// usage, agents, setup, cards, case analysis), but the budget fields where
// budgets are set. The budgets are kept all the same. Every signed-in
// person reads it: here, and as showCosts in the settings every page
// loads (GET /api/v1/settings).

// uiSettings are the console-wide page settings.
type uiSettings struct {
	ShowCosts bool `json:"showCosts"`
}

// costsSetter changes Show costs; *settings.Service is one.
type costsSetter interface {
	UpdateCosts(ctx context.Context, show bool) (settings.State, error)
}

func (s *Server) getUISettings(w http.ResponseWriter, r *http.Request) {
	writeValue(w, r, uiSettings{ShowCosts: s.opts.Settings.Current().ShowsCosts()})
}

func (s *Server) putUISettings(w http.ResponseWriter, r *http.Request) {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin changes whether the console shows costs.")
		return
	}
	set, ok := s.opts.Settings.(costsSetter)
	if !ok {
		jsonError(w, http.StatusNotImplemented, "This console cannot change whether it shows costs.")
		return
	}
	var in struct {
		ShowCosts *bool `json:"showCosts"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	if in.ShowCosts == nil {
		jsonError(w, http.StatusBadRequest, "say whether to show costs: {\"showCosts\": true}")
		return
	}
	before := s.opts.Settings.Current().ShowsCosts()
	st, err := set.UpdateCosts(r.Context(), *in.ShowCosts)
	if err != nil {
		s.opts.Logger.Warn("show costs not saved", "err", err)
		jsonError(w, settingsStatus(err, http.StatusInternalServerError), "The setting was not saved: "+err.Error())
		return
	}
	words := map[bool]string{true: "shown", false: "hidden"}
	if before != st.ShowsCosts() {
		s.writeAudit(r, "", audit.OutcomeOK, audit.ActionSettingsUI, "settings", "costs "+words[before]+" → "+words[st.ShowsCosts()])
	}
	writeValue(w, r, uiSettings{ShowCosts: st.ShowsCosts()})
}

// --- end of show costs.
