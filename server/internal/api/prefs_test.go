package api

import (
	"net/http"
	"strings"
	"testing"
)

// Each signed-in person keeps their own preferences, and nobody else's
// (asked at the Phase 4 review).
func TestPrefsArePerPerson(t *testing.T) {
	srv, viewer, _, _, _, _ := multiSetupWith(t, "viewer@corp.example", nil)
	ops := signIn(t, srv, "ops@corp.example")
	if r, b := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/prefs", ""); r.StatusCode != 200 || !strings.Contains(b, `"prefs":{}`) || !strings.Contains(b, `"store":"account"`) {
		t.Fatalf("empty prefs %d %s", r.StatusCode, b)
	}
	if r, b := send(t, viewer, http.MethodPut, srv.URL+"/api/v1/prefs", `{"prefs":{"221b.pods.hidden":["ip","node"]}}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("put %d %s", r.StatusCode, b)
	}
	if _, b := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/prefs", ""); !strings.Contains(b, `"221b.pods.hidden":["ip","node"]`) {
		t.Fatalf("own prefs %s", b)
	}
	if _, b := send(t, ops, http.MethodGet, srv.URL+"/api/v1/prefs", ""); strings.Contains(b, "221b.pods.hidden") {
		t.Fatalf("someone else's prefs: %s", b)
	}
	for name, body := range map[string]string{
		"bad name": `{"prefs":{"no spaces please":1}}`,
		"too big":  `{"prefs":{"big":"` + strings.Repeat("x", 20<<10) + `"}}`,
		"not json": `prefs=1`,
		"unknown":  `{"prefs":{},"user":"ops@corp.example"}`,
	} {
		if r, b := send(t, viewer, http.MethodPut, srv.URL+"/api/v1/prefs", body); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, r.StatusCode, b)
		}
	}
	anon := &http.Client{}
	if r, err := anon.Get(srv.URL + "/api/v1/prefs"); err != nil || r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %v %v", r.StatusCode, err)
	}
}
