package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sim"
)

// An edit of a connection carries the version its editor read, and one
// saved since is refused with 409 and a sentence, changing nothing
// (DB-92); an edit without the version is saved as before.
func TestAnEditOfAConnectionSavedSinceIsRefused(t *testing.T) {
	other := httptest.NewServer(sim.New(sim.Options{Nodes: 1, Pods: 2, Seed: 3}))
	t.Cleanup(other.Close)
	tok := saToken(time.Now().Add(48 * time.Hour))
	if resp, err := http.Post(other.URL+"/sim/token", "application/json", strings.NewReader(`{"token":"`+tok+`"}`)); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sim token: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	srv, admin, _, auditOut := multiSetup(t, "ops@corp.example")
	draft, _ := json.Marshal(map[string]any{"name": "analytics-dev", "env": "development", "server": other.URL, "ca": "system", "auth": "token", "mode": "read", "token": tok})
	resp, b := send(t, admin, http.MethodPost, srv.URL+"/api/v1/clusters", string(draft))
	var sum clusters.Summary
	if err := json.Unmarshal([]byte(b), &sum); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %s %s", resp.Status, b)
	}
	resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev", "")
	if err := json.Unmarshal([]byte(b), &sum); err != nil || sum.EditVersion <= 0 || !strings.Contains(b, `"rowVersion":`) {
		t.Fatalf("the connection carries no version: %s %s", resp.Status, b)
	}
	edit := func(description string, version int) (*http.Response, string) {
		body := map[string]any{"name": "analytics-dev", "env": "development", "description": description, "server": other.URL, "ca": "system", "auth": "token", "mode": "read"}
		if version > 0 {
			body["rowVersion"] = version
		}
		j, _ := json.Marshal(body)
		return send(t, admin, http.MethodPut, srv.URL+"/api/v1/clusters/analytics-dev", string(j))
	}
	read := sum.EditVersion
	resp, b = edit("the first edit", read)
	var first clusters.Summary
	if err := json.Unmarshal([]byte(b), &first); err != nil || resp.StatusCode != http.StatusOK || first.EditVersion != read+1 {
		t.Fatalf("the first edit: %s %s", resp.Status, b)
	}
	resp, b = edit("the second edit, of what was read before the first", read)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(b, "changed by someone else") {
		t.Fatalf("the second edit: %s %s", resp.Status, b)
	}
	if !strings.Contains(auditOut.String(), "changed by someone else") {
		t.Fatalf("the refusal is not in the audit log:\n%s", auditOut.String())
	}
	resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev", "")
	if !strings.Contains(b, `"description":"the first edit"`) {
		t.Fatalf("after the refused edit: %s %s", resp.Status, b)
	}
	if resp, b = edit("an edit with no version", 0); resp.StatusCode != http.StatusOK {
		t.Fatalf("an edit with no version: %s %s", resp.Status, b)
	}
}
