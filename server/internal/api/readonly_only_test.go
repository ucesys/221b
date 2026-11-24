package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/roles"
)

// RB-03: the fully protected read-only deployment (both templates
// readOnly) is said wherever Watson's cards are: Watson's status says
// changes are impossible by configuration, for the web to show. It was
// not built (the review of November 24, 2026, cut by its cap:
// ReadOnlyOnly had no caller).
func TestWatsonSaysChangesAreImpossibleInTheReadOnlyDeployment(t *testing.T) {
	status := func(tpl roles.Templates) map[string]any {
		t.Helper()
		srv, c, _, _, _ := watsonSetup(t, "ops@corp.example", clusters.EnvOther, clusters.ModeRead, func(o *Options) { o.Roles.Templates = tpl })
		r, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/watson", "")
		if r.StatusCode != http.StatusOK {
			t.Fatalf("watson status: %d %s", r.StatusCode, body)
		}
		var st map[string]any
		if err := json.Unmarshal([]byte(body), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := status(roles.Templates{Admin: roles.ReadOnly, SuperAdmin: roles.ReadOnly}); st["impossible"] != true {
		t.Fatalf("both templates read-only: %v", st)
	}
	for _, tpl := range []roles.Templates{{}, {Admin: roles.ReadOnly}, {SuperAdmin: roles.ReadOnly}} {
		if st := status(tpl); st["impossible"] != nil {
			t.Fatalf("templates %+v: %v", tpl, st)
		}
	}
}
