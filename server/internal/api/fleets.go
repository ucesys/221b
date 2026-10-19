package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/fleets"
)

// maxFleetsBody bounds a fleets update: a hundred fleets of fifty
// requirements fit with room to spare.
const maxFleetsBody = 512 << 10

// putFleets replaces a cluster's fleets, for admins (P4-01). Fleets are
// kept with the console's settings, not in the cluster; a node joins a
// fleet only through its own labels and taints.
func (s *Server) putFleets(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.SessionFrom(r.Context())
	if s.opts.Auth != nil && (!ok || !sess.Admin) {
		jsonError(w, http.StatusForbidden, "only an administrator can change the fleets")
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the fleets as application/json")
		return
	}
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	id := clusterID(r)
	var in struct {
		Fleets []fleets.Fleet `json:"fleets"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFleetsBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the fleets are not valid: "+err.Error())
		return
	}
	list := fleets.Clean(in.Fleets)
	if err := fleets.Validate(list); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := s.opts.Settings.UpdateFleets(r.Context(), id, list)
	if err != nil {
		s.opts.Logger.Warn("fleets not saved", "cluster", id, "err", err)
		jsonError(w, settingsStatus(err, http.StatusInternalServerError), "The fleets were not saved: "+err.Error())
		return
	}
	names := make([]string, len(list))
	for i, f := range list {
		names[i] = f.Name
	}
	detail := fmt.Sprintf("%d fleets: %s", len(list), strings.Join(names, ", "))
	if len(list) == 0 {
		detail = "no fleets"
	}
	s.opts.Logger.Info("fleets changed from a browser", "cluster", id, "fleets", len(list), "user", sess.User.Username)
	if s.opts.Audit != nil {
		s.opts.Audit.Record(audit.Event{Action: audit.ActionFleets, Who: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}, Cluster: id, Target: "fleets", Outcome: "ok", Detail: detail, From: fromAddr(r)})
	}
	saved := st.Fleets[id]
	if saved == nil {
		saved = []fleets.Fleet{}
	}
	b, _ := json.Marshal(map[string]any{"fleets": saved})
	writeJSON(w, r, b)
}
