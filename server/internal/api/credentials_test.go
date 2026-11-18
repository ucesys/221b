package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Settings › Credentials (0.12.1, CR-01 to CR-14) ---

// A SuperAdmin adds, changes, tests and removes credentials; anyone else
// reads their names, kinds, endpoints and uses only. No answer, and no
// audit record, holds a secret.
func TestCredentialsAPIForSuperAdminsAndOthers(t *testing.T) {
	creds := credentials.New(credentials.Options{})
	srv, c, _, auditOut, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { o.Credentials = creds })
	base := srv.URL + "/api/v1/credentials"
	body := `{"name":"GitHub ucesys","kind":"token","endpoint":"https://github.com/ucesys","description":"read only","expires":"2026-11-20T00:00:00Z","sendAs":{"method":"basic","username":"x-access-token"},"secret":{"token":"ghp_api-secret-value"}}`
	resp, got := send(t, c, http.MethodPost, base, body)
	if resp.StatusCode != http.StatusCreated || strings.Contains(got, "ghp_api-secret-value") || !strings.Contains(got, `"set":["token"]`) {
		t.Fatalf("create: %s %s", resp.Status, got)
	}
	id := between(got, `"id":"`, `"`)
	resp, got = send(t, c, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || strings.Contains(got, "ghp_api") || !strings.Contains(got, `"mayEdit":true`) || !strings.Contains(got, `"description":"read only"`) || !strings.Contains(got, `"expiring":true`) {
		t.Fatalf("list: %s %s", resp.Status, got)
	}
	// Changed: the secret replaced, and said so in the audit log, never
	// its value.
	resp, got = send(t, c, http.MethodPut, base+"/"+id, `{"name":"GitHub ucesys","kind":"token","endpoint":"https://github.com/ucesys","sendAs":{"method":"bearer"},"secret":{"token":"ghp_new-api-secret"}}`)
	if resp.StatusCode != http.StatusOK || strings.Contains(got, "ghp_new") {
		t.Fatalf("change: %s %s", resp.Status, got)
	}
	if resp, got := send(t, c, http.MethodPost, base, `{"name":"bad","kind":"token","endpoint":"","secret":{"token":"x"}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no endpoint: %s %s", resp.Status, got)
	}
	// The picker's choices: the ones that serve the use at the address.
	if resp, got := send(t, c, http.MethodGet, base+"/choices?use=git&address=https://github.com/ucesys/221b", ""); resp.StatusCode != http.StatusOK || !strings.Contains(got, `"id":"`+id+`"`) {
		t.Fatalf("choices: %s %s", resp.Status, got)
	}
	if _, got := send(t, c, http.MethodGet, base+"/choices?use=git&address=https://gitlab.com/x/y", ""); strings.Contains(got, id) {
		t.Fatalf("choices elsewhere: %s", got)
	}
	if resp, _ := send(t, c, http.MethodGet, base+"/choices?use=ftp", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown use: %s", resp.Status)
	}

	// Someone who is not a SuperAdmin sees the names, kinds, endpoints and
	// uses, and changes nothing.
	v := signIn(t, srv, "viewer@corp.example")
	resp, got = send(t, v, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(got, `"name":"GitHub ucesys"`) || !strings.Contains(got, `"endpoint":"https://github.com/ucesys"`) || strings.Contains(got, "description") || strings.Contains(got, "sendAs") || strings.Contains(got, `"mayEdit":true`) {
		t.Fatalf("a viewer's list: %s %s", resp.Status, got)
	}
	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "", body},
		{http.MethodPut, "/" + id, body},
		{http.MethodDelete, "/" + id, ""},
		{http.MethodPost, "/" + id + "/test", `{}`},
		{http.MethodPost, "/migration/dismiss", ""},
	} {
		if resp, got := send(t, v, req.method, base+req.path, req.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a viewer's %s %s: %s %s", req.method, req.path, resp.Status, got)
		}
	}

	// A test outside the endpoint sends nothing; the result is kept.
	resp, got = send(t, c, http.MethodPost, base+"/"+id+"/test", `{"target":"https://example.com/x"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(got, `"ok":false`) || !strings.Contains(got, "never sent there") {
		t.Fatalf("test: %s %s", resp.Status, got)
	}
	if resp, _ := send(t, c, http.MethodDelete, base+"/"+id, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %s", resp.Status)
	}
	if resp, _ := send(t, c, http.MethodDelete, base+"/"+id, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("remove again: %s", resp.Status)
	}
	log := auditOut.String()
	for _, want := range []string{`"action":"credential.save"`, "its secret replaced", `"action":"credential.test"`, `"action":"credential.remove"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("the audit log lacks %s: %s", want, log)
		}
	}
	if strings.Contains(log, "ghp_") {
		t.Fatal("the audit log holds a secret")
	}
}
