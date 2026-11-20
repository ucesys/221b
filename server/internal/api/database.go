package api

import (
	"net/http"
	"time"

	"github.com/ucesys/221b/server/internal/db"
)

// The database's state (Phase 13, DB-15): whether the console keeps its
// records in PostgreSQL, and whether the database answers. The pages that
// need it show a banner from this while it does not; the rest go on.

// DatabaseState is what Options.Database returns.
type DatabaseState struct {
	// Health is how the database last answered.
	Health db.Health
	// SchemaVersion is the newest step of the schema this build applied.
	SchemaVersion int
	// Domains are the domains whose records are in the database; the rest
	// are still in ConfigMaps.
	Domains []string
	// StartedWithout says this console was started without its database
	// (DB-67): it keeps no records, and stops and is started again when
	// the database answers.
	StartedWithout bool
}

type databaseView struct {
	// Configured is false for a console with no database at all.
	Configured bool       `json:"configured"`
	Available  bool       `json:"available"`
	Since      *time.Time `json:"since,omitempty"`
	// StartedWithout: the console was started while the database was away
	// and serves only what needs none until it is started again.
	StartedWithout bool `json:"startedWithout,omitempty"`
	// Error, SchemaVersion and Domains are for SuperAdmins: the error can
	// name the database's host.
	Error         string   `json:"error,omitempty"`
	SchemaVersion int      `json:"schemaVersion,omitempty"`
	Domains       []string `json:"domains,omitempty"`
}

// getDatabase serves GET /api/v1/database to anyone signed in.
func (s *Server) getDatabase(w http.ResponseWriter, r *http.Request) {
	if s.opts.Database == nil {
		writeValue(w, r, databaseView{})
		return
	}
	st := s.opts.Database()
	v := databaseView{Configured: true, Available: st.Health.Available, StartedWithout: st.StartedWithout}
	if !st.Health.Since.IsZero() {
		since := st.Health.Since.UTC()
		v.Since = &since
	}
	if s.isSuperAdmin(r) {
		v.Error, v.SchemaVersion, v.Domains = st.Health.Error, st.SchemaVersion, st.Domains
	}
	writeValue(w, r, v)
}
