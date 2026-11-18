package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Administration › Credentials (0.12.1, CR-01 to CR-14) ---

// A SuperAdmin adds, changes, tests and removes credentials; anyone else
// reads their names, kinds, endpoints and uses only. No answer, and no
// audit record, holds a secret.
func TestCredentialsAPIForSuperAdminsAndOthers(t *testing.T) {
	creds := credentials.New(credentials.Options{})
	var usedID string
	creds.Register(func(context.Context) ([]credentials.Ref, error) {
		return []credentials.Ref{{Credential: usedID, What: "application team-b/pay", Where: "Helm"}}, nil
	})
	srv, c, _, auditOut, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { o.Credentials = creds })
	base := srv.URL + "/api/v1/credentials"
	body := `{"name":"GitHub ucesys","kind":"token","endpoint":"https://github.com/ucesys","description":"read only","expires":"2026-11-20T00:00:00Z","sendAs":{"method":"basic","username":"x-access-token"},"secret":{"token":"ghp_api-secret-value"}}`
	resp, got := send(t, c, http.MethodPost, base, body)
	if resp.StatusCode != http.StatusCreated || strings.Contains(got, "ghp_api-secret-value") || !strings.Contains(got, `"set":["token"]`) {
		t.Fatalf("create: %s %s", resp.Status, got)
	}
	id := between(got, `"id":"`, `"`)
	usedID = id
	resp, got = send(t, c, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || strings.Contains(got, "ghp_api") || !strings.Contains(got, `"mayEdit":true`) || !strings.Contains(got, `"description":"read only"`) || !strings.Contains(got, `"expiring":true`) || !strings.Contains(got, `"usedBy":[{"what":"application team-b/pay"`) {
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

	// Someone who is not a SuperAdmin sees the names, kinds and
	// endpoints, not what uses them (CR-06), and changes nothing.
	v := signIn(t, srv, "viewer@corp.example")
	resp, got = send(t, v, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(got, `"name":"GitHub ucesys"`) || !strings.Contains(got, `"endpoint":"https://github.com/ucesys"`) || strings.Contains(got, "description") || strings.Contains(got, "sendAs") || strings.Contains(got, `"mayEdit":true`) || strings.Contains(got, "team-b") || !strings.Contains(got, `"usedBy":[]`) {
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

	usedID = ""
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

// CR-02: an SSH key's host key is written and never read back: the page
// gets its type, fingerprint and the key cut short; a change without it
// keeps it, and a new SSH key needs it. CR-04: an OAuth2 client's token
// host is said beside its endpoint, to everyone who sees it.
func TestTheHostKeyIsShownByItsFingerprint(t *testing.T) {
	creds := credentials.New(credentials.Options{})
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { o.Credentials = creds })
	base := srv.URL + "/api/v1/credentials"
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	key, _ := json.Marshal(string(pem.EncodeToMemory(block)))
	sp, _ := ssh.NewPublicKey(pub)
	line := "github.com " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	body64 := strings.Fields(line)[2]
	hostKey, _ := json.Marshal(line)
	if resp, got := send(t, c, http.MethodPost, base, `{"name":"deploy","kind":"ssh","endpoint":"git@github.com:ucesys","secret":{"privateKey":`+string(key)+`}}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(got, "host key") {
		t.Fatalf("an SSH key without its host key: %s %s", resp.Status, got)
	}
	resp, got := send(t, c, http.MethodPost, base, `{"name":"deploy","kind":"ssh","endpoint":"git@github.com:ucesys","hostKey":`+string(hostKey)+`,"secret":{"privateKey":`+string(key)+`}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %s %s", resp.Status, got)
	}
	id := between(got, `"id":"`, `"`)
	fp := ssh.FingerprintSHA256(sp)
	short := "ssh-ed25519 " + body64[:12] + "…" + body64[len(body64)-8:]
	for _, step := range []string{"created", "listed", "changed"} {
		switch step {
		case "listed":
			_, got = send(t, c, http.MethodGet, base, "")
		case "changed":
			resp, got = send(t, c, http.MethodPut, base+"/"+id, `{"name":"deploy key","kind":"ssh","endpoint":"git@github.com:ucesys"}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("change without the host key: %s %s", resp.Status, got)
			}
		}
		if strings.Contains(got, body64) || strings.Contains(got, `"hostKey":`) || !strings.Contains(got, `"fingerprint":"`+fp+`"`) || !strings.Contains(got, `"short":"`+short+`"`) || !strings.Contains(got, `"type":"ssh-ed25519"`) {
			t.Fatalf("%s: %s", step, got)
		}
	}
	if kept, _ := creds.Get(context.Background(), id); kept.HostKey != line {
		t.Fatalf("the host key was not kept: %q", kept.HostKey)
	}
	// Another kind becoming an SSH key needs it.
	resp, got = send(t, c, http.MethodPost, base, `{"name":"tok","kind":"token","endpoint":"https://github.com/ucesys","secret":{"token":"tok-123456"}}`)
	tok := between(got, `"id":"`, `"`)
	if resp, got := send(t, c, http.MethodPut, base+"/"+tok, `{"name":"tok","kind":"ssh","endpoint":"git@github.com:ucesys","secret":{"privateKey":`+string(key)+`}}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(got, "host key") {
		t.Fatalf("a kind changed to SSH without a host key: %s %s", resp.Status, got)
	}
	// An OAuth2 client's token host.
	if resp, got := send(t, c, http.MethodPost, base, `{"name":"api","kind":"oauth2","endpoint":"https://api.example.com","tokenURL":"https://auth.example.org/oauth/token","clientId":"me","secret":{"clientSecret":"cs-123456"}}`); resp.StatusCode != http.StatusCreated || !strings.Contains(got, `"tokenHost":"auth.example.org"`) {
		t.Fatalf("oauth2: %s %s", resp.Status, got)
	}
	if resp, got := send(t, c, http.MethodPost, base, `{"name":"api2","kind":"oauth2","endpoint":"https://api.example.com","tokenURL":"https://auth.example.org/a/../token","clientId":"me","secret":{"clientSecret":"cs-123456"}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a token URL with ..: %s %s", resp.Status, got)
	}
	v := signIn(t, srv, "viewer@corp.example")
	if _, got := send(t, v, http.MethodGet, base, ""); !strings.Contains(got, `"tokenHost":"auth.example.org"`) || strings.Contains(got, "fingerprint") {
		t.Fatalf("a viewer's list: %s", got)
	}
}
