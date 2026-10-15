package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/prefs"
)

// A signed-in person's own preferences, such as the columns each table
// shows (asked at the Phase 4 review): GET returns them, PUT replaces them.
// Served only with sign-in on and per-person preferences enabled; without
// them the browser keeps its own.
func (s *Server) getPrefs(w http.ResponseWriter, r *http.Request) {
	subject, ok := prefsSubject(w, r)
	if !ok {
		return
	}
	p, err := s.opts.Prefs.Get(r.Context(), subject)
	if err != nil {
		s.opts.Logger.Warn("preferences not read", "err", err)
		jsonError(w, http.StatusBadGateway, "Your preferences could not be read: "+err.Error())
		return
	}
	b, _ := json.Marshal(map[string]any{"prefs": p, "store": "account"})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, b)
}

func (s *Server) putPrefs(w http.ResponseWriter, r *http.Request) {
	subject, ok := prefsSubject(w, r)
	if !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the preferences as application/json")
		return
	}
	var in struct {
		Prefs prefs.Prefs `json:"prefs"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*prefs.MaxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the preferences are not valid: "+err.Error())
		return
	}
	if err := prefs.Validate(in.Prefs); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Prefs.Put(r.Context(), subject, in.Prefs); err != nil {
		s.opts.Logger.Warn("preferences not saved", "err", err)
		jsonError(w, http.StatusBadGateway, "Your preferences were not saved: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// prefsSubject is whose preferences a request is about: the signed-in
// person's, never anyone else's.
func prefsSubject(w http.ResponseWriter, r *http.Request) (string, bool) {
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		jsonError(w, http.StatusUnauthorized, "sign in to keep preferences with your account")
		return "", false
	}
	subject := sess.User.Subject
	if subject == "" {
		subject = sess.User.Email
	}
	if subject == "" {
		jsonError(w, http.StatusBadRequest, "your sign-in names nobody to keep preferences for")
		return "", false
	}
	return sess.Method + ":" + subject, true
}
