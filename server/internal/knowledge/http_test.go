package knowledge

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/backup"
)

func serve(t *testing.T, s *Service, c *Caller, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	h := s.Handler(func(*http.Request) (Caller, bool) {
		if c == nil {
			return Caller{}, false
		}
		return *c, true
	})
	r := httptest.NewRequest(method, Prefix+path, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Who may do what: a SuperAdmin sets up the sources, anyone signed in
// reads the approved pages, and credentials never reach a page.
func TestTheAPIKeepsItsRoles(t *testing.T) {
	f := newFixture(t)
	if w := serve(t, f.s, nil, "GET", "pages", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", w.Code)
	}
	if w := serve(t, f.s, &viewer, "GET", "settings", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer's settings: %d", w.Code)
	}
	body := `{"source":{"id":"ops","name":"Ops space","kind":"confluence","baseURL":"https://ops.atlassian.net","flavor":"cloud","email":"ann@example.com","space":"OPS"},"token":"secret-token-abcdef"}`
	if w := serve(t, f.s, &bob, "POST", "sources", body, nil); w.Code != http.StatusForbidden {
		t.Fatalf("an Admin adds a source: %d", w.Code)
	}
	if w := serve(t, f.s, &admin, "POST", "sources", body, nil); w.Code != http.StatusCreated {
		t.Fatalf("a SuperAdmin adds a source: %d %s", w.Code, w.Body)
	}
	w := serve(t, f.s, &admin, "GET", "settings", "", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "secret-token-abcdef") {
		t.Fatalf("the settings: %d %s", w.Code, w.Body)
	}
	var v View
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if !v.Creds["ops"].Token || len(v.Settings.Sources) != 4 {
		t.Fatalf("the token shows as set, the console, documents and what is added in Mycroft are listed: %+v", v)
	}
	if w := serve(t, f.s, &viewer, "GET", "pages", "", nil); w.Code != http.StatusOK {
		t.Fatalf("a viewer reads the pages: %d", w.Code)
	}
	if w := serve(t, f.s, &viewer, "GET", "drafts", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer's drafts: %d", w.Code)
	}
	if w := serve(t, f.s, &viewer, "GET", "drafts/count", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":0`) {
		t.Fatalf("a viewer's count: %d %s", w.Code, w.Body)
	}
	if w := serve(t, f.s, &admin, "POST", "sources", `{"source":{"id":"bad","name":"x","kind":"git","url":"http://insecure.example.com/x.git"}}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("a repository over plain HTTP: %d", w.Code)
	}
}

// The webhook needs its token: GitHub's signature, GitLab's header, or a
// bearer token.
func TestTheWebhookNeedsItsToken(t *testing.T) {
	f := newFixture(t)
	if w := serve(t, f.s, nil, "POST", "hook/wiki", "{}", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token set: %d", w.Code)
	}
	if err := f.s.SaveHookToken(ctx, admin, "hook-token-0123456789"); err != nil {
		t.Fatal(err)
	}
	if w := serve(t, f.s, nil, "POST", "hook/wiki", "{}", map[string]string{"Authorization": "Bearer wrong-token-0123456789"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %d", w.Code)
	}
	if w := serve(t, f.s, nil, "POST", "hook/wiki", "{}", map[string]string{"X-Gitlab-Token": "hook-token-0123456789"}); w.Code != http.StatusAccepted {
		t.Fatalf("GitLab's header: %d %s", w.Code, w.Body)
	}
	mac := hmac.New(sha256.New, []byte("hook-token-0123456789"))
	mac.Write([]byte(`{"ref":"refs/heads/main"}`))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if w := serve(t, f.s, nil, "POST", "hook/wiki", `{"ref":"refs/heads/main"}`, map[string]string{"X-Hub-Signature-256": sig}); w.Code != http.StatusAccepted {
		t.Fatalf("GitHub's signature: %d %s", w.Code, w.Body)
	}
	if !HookPath(Prefix+"hook/wiki") || HookPath(Prefix+"pages") {
		t.Fatal("HookPath")
	}
}

// A backup holds the settings and which credentials are set, as
// placeholders; a file with a credential in it is refused.
func TestABackupHoldsPlaceholders(t *testing.T) {
	f := newFixture(t)
	f.space(t, map[string]string{"Shop": "<p>Shop.</p>"})
	part, err := f.s.Export(ctx, (&backup.Env{}).Seal)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(part)
	if bytes.Contains(b, []byte("read-token-123456")) || part.Credentials[sourceKey("infra")] != Placeholder {
		t.Fatalf("the backup: %s", b)
	}
	if _, _, err := f.s.Import(ctx, b, (&backup.Env{}).Open); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(Placeholder)
	bad := bytes.Replace(b, quoted, []byte(`"read-token-123456"`), 1)
	if _, _, err := f.s.Import(ctx, bad, (&backup.Env{}).Open); !errors.Is(err, ErrSecretInFile) {
		t.Fatalf("a credential in the file: %v", err)
	}
	set, _ := f.s.Settings(ctx)
	if _, ok := set.source("infra"); !ok {
		t.Fatal("the source is restored")
	}
}
