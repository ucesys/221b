package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sim"
)

// saToken is an unsigned token shaped like a ServiceAccount's; the
// simulator only compares it with the one it was told to require.
func saToken(exp time.Time) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "x"})
	b, _ := json.Marshal(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console", "exp": exp.Unix()})
	enc := base64.RawURLEncoding.EncodeToString
	return enc(h) + "." + enc(b) + "." + enc([]byte("signature"))
}

// Counts and RestConfig make the fake a clusters.Client.
func (s *source) Counts() (int, int, uint64, uint64) {
	return len(s.Objects("nodes", "")), len(s.Objects("pods", "")), 0, 0
}

func (s *source) RestConfig() *rest.Config { return nil }

// multiSetup runs the console with sign-in by local accounts (an
// administrator and a viewer), the fake cache as the local cluster and a
// manager over a memory store, and returns a client signed in as who.
func multiSetup(t *testing.T, who string) (*httptest.Server, *http.Client, *clusters.Manager, *bytes.Buffer) {
	t.Helper()
	local := &source{Source: fake.New("n1", "n2"), logs: map[string][]string{}}
	store := live.New(local, live.Options{})
	store.Notify(everything())
	store.Process()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := clusters.New(clusters.NewMemoryStore(), clusters.Options{Refresh: time.Second, HealthInterval: time.Second, SecretsRead: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Server: "https://10.0.0.1:6443", Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var auditOut bytes.Buffer
	hash, _ := auth.HashPassword("correct horse battery staple")
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeLocal,
		Local: []auth.LocalUser{
			{Email: "ops@corp.example", Name: "Ops", PasswordHash: hash},
			{Email: "viewer@corp.example", Name: "Viewer", PasswordHash: hash},
		},
		Admins:      []string{"ops@corp.example"},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, nil, audit.New(audit.Options{Out: &auditOut}), nil)
	if err != nil {
		t.Fatal(err)
	}
	shared := settings.NewService(ctx, &settings.Memory{}, settings.Default, slog.New(slog.NewTextHandler(io.Discard, nil)), func(st settings.State) {
		b, _ := json.Marshal(st)
		m.SetSettings(st.Refresh(), b)
	})
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app:"+r.URL.Path) })
	srv := httptest.NewServer(New(Options{Store: store, Cluster: local, Settings: shared, Clusters: m, Auth: svc, Audit: audit.New(audit.Options{Out: &auditOut}), Ready: func() bool { return true }, UI: ui, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)
	return srv, signIn(t, srv, who), m, &auditOut
}

// signIn returns a client signed in to the console as who.
func signIn(t *testing.T, srv *httptest.Server, who string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	page, err := c.Get(srv.URL + "/signin")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	m := regexp.MustCompile(`name="form" value="([^"]+)"`).FindSubmatch(html)
	if m == nil {
		t.Fatalf("no form token on the sign-in page")
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/signin", strings.NewReader(url.Values{"email": {who}, "password": {"correct horse battery staple"}, "next": {"/"}, "form": {string(m[1])}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign-in as %s: %s", who, resp.Status)
	}
	return c
}

func send(t *testing.T, c *http.Client, method, url, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestClusterConnections(t *testing.T) {
	other := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 20, Seed: 7}))
	t.Cleanup(other.Close)
	tok := saToken(time.Now().Add(48 * time.Hour))
	// The other cluster requires the token, as a real one would.
	resp, err := http.Post(other.URL+"/sim/token", "application/json", strings.NewReader(`{"token":"`+tok+`"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sim token: %v %v", err, resp)
	}
	resp.Body.Close()

	srv, admin, m, auditOut := multiSetup(t, "ops@corp.example")

	// Everyone sees the list; it starts with the local cluster.
	resp, b := send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"id":"local"`) || !strings.Contains(b, `"name":"Here"`) || strings.Contains(b, "10.0.0.1") == false {
		t.Fatalf("list: %s %s", resp.Status, b)
	}
	// The local cluster answers under its id too.
	resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/local/snapshot", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"nodes"`) {
		t.Fatalf("local snapshot: %s %.100s", resp.Status, b)
	}

	// A draft with a bad field is refused with the field named.
	draft := map[string]any{"name": "analytics-dev", "env": "development", "description": "sandbox", "server": other.URL, "ca": "system", "auth": "token", "mode": "read", "token": tok}
	bad := map[string]any{}
	for k, v := range draft {
		bad[k] = v
	}
	bad["env"] = "prod"
	bb, _ := json.Marshal(bad)
	resp, b = send(t, admin, http.MethodPost, srv.URL+"/api/v1/clusters", string(bb))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(b, `"field":"env"`) {
		t.Fatalf("bad env: %s %s", resp.Status, b)
	}
	// Unknown fields are refused rather than ignored: a typo would lose a credential.
	resp, b = send(t, admin, http.MethodPost, srv.URL+"/api/v1/clusters", `{"name":"x","tokn":"y"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(b, "tokn") {
		t.Fatalf("unknown field: %s %s", resp.Status, b)
	}

	// A test of the draft before saving: every step passes but the
	// metrics one (the simulator has no metrics-server for this client).
	db, _ := json.Marshal(draft)
	resp, b = send(t, admin, http.MethodPost, srv.URL+"/api/v1/clusters/test", string(db))
	var tr clusters.TestResult
	if err := json.Unmarshal([]byte(b), &tr); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("test draft: %s %s", resp.Status, b)
	}
	if tr.Nodes != 3 || tr.Tone == "err" || len(tr.Steps) < 4 {
		t.Fatalf("test result: %+v", tr)
	}

	// Adding it answers 201 with the summary, never the token.
	resp, b = send(t, admin, http.MethodPost, srv.URL+"/api/v1/clusters", string(db))
	var sum clusters.Summary
	if err := json.Unmarshal([]byte(b), &sum); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %s %s", resp.Status, b)
	}
	if sum.ID != "analytics-dev" || sum.Cred.Who != "system:serviceaccount:kube-system:cluster-console" || sum.Cred.Expires == nil || strings.Contains(b, tok) {
		t.Fatalf("added: %s", b)
	}
	// Its data routes come up under its id.
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev/snapshot", "")
		if resp.StatusCode == http.StatusOK && strings.Contains(b, `"synced":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("other cluster snapshot: %s %.200s", resp.Status, b)
		}
		time.Sleep(100 * time.Millisecond)
	}
	resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev/nodes/cp-1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other cluster node: %s %.200s", resp.Status, b)
	}
	// An unknown cluster is 404; a failed connection 503 with a pointer to its page.
	resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/nowhere/snapshot", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown cluster: %s %s", resp.Status, b)
	}

	// The saved connection can be tested, and the result is kept for its page.
	resp, b = send(t, admin, http.MethodPost, srv.URL+"/api/v1/clusters/analytics-dev/test", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"nodes":3`) {
		t.Fatalf("test saved: %s %s", resp.Status, b)
	}
	resp, b = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"lastTest"`) {
		t.Fatalf("get: %s %s", resp.Status, b)
	}

	// A change without credentials keeps them.
	upd := map[string]any{"name": "analytics-dev", "env": "staging", "description": "shared sandbox", "server": other.URL, "ca": "system", "auth": "token", "mode": "write"}
	ub, _ := json.Marshal(upd)
	resp, b = send(t, admin, http.MethodPut, srv.URL+"/api/v1/clusters/analytics-dev", string(ub))
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"env":"staging"`) || !strings.Contains(b, `"mode":"write"`) || !strings.Contains(b, `"who":"system:serviceaccount:kube-system:cluster-console"`) {
		t.Fatalf("update: %s %s", resp.Status, b)
	}
	// The local cluster's name can change; its connection cannot.
	resp, b = send(t, admin, http.MethodPut, srv.URL+"/api/v1/clusters/local", `{"name":"Lakehouse","env":"production","description":"","server":"https://elsewhere:6443","ca":"system","auth":"token","mode":"read","token":"x"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"name":"Lakehouse"`) || !strings.Contains(b, `"server":"https://10.0.0.1:6443"`) || !strings.Contains(b, `"auth":"incluster"`) {
		t.Fatalf("update local: %s %s", resp.Status, b)
	}
	resp, b = send(t, admin, http.MethodDelete, srv.URL+"/api/v1/clusters/local", "")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(b, "cannot be removed") {
		t.Fatalf("remove local: %s %s", resp.Status, b)
	}

	// A viewer sees the list and the data, and can change nothing.
	viewer := signIn(t, srv, "viewer@corp.example")
	resp, b = send(t, viewer, http.MethodGet, srv.URL+"/api/v1/clusters", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"id":"analytics-dev"`) {
		t.Fatalf("viewer list: %s %s", resp.Status, b)
	}
	resp, b = send(t, viewer, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev/snapshot", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer snapshot: %s %.100s", resp.Status, b)
	}
	for _, try := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/clusters", string(db)},
		{http.MethodPost, "/api/v1/clusters/test", string(db)},
		{http.MethodPut, "/api/v1/clusters/analytics-dev", string(ub)},
		{http.MethodPost, "/api/v1/clusters/analytics-dev/test", ""},
		{http.MethodDelete, "/api/v1/clusters/analytics-dev", ""},
	} {
		resp, b = send(t, viewer, try.method, srv.URL+try.path, try.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("viewer %s %s: %s %s", try.method, try.path, resp.Status, b)
		}
	}
	// Without the same-site proof, nothing changes either (X-06).
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/clusters/analytics-dev", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site delete: %s", resp.Status)
	}
	if _, ok := m.Get("analytics-dev"); !ok {
		t.Fatal("the connection was removed by a viewer or a cross-site request")
	}

	// Removal deletes the credential and stops the data routes.
	resp, b = send(t, admin, http.MethodDelete, srv.URL+"/api/v1/clusters/analytics-dev", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"removed":true`) {
		t.Fatalf("remove: %s %s", resp.Status, b)
	}
	resp, _ = send(t, admin, http.MethodGet, srv.URL+"/api/v1/clusters/analytics-dev/snapshot", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after removal: %s", resp.Status)
	}
	if _, _, err := m.Store().Get(context.Background(), "analytics-dev"); err != clusters.ErrNotFound {
		t.Errorf("credential after removal: %v", err)
	}

	// The audit log tells the story, without the token.
	log := auditOut.String()
	for _, want := range []string{`"action":"cluster.test"`, `"action":"cluster.add"`, `"action":"cluster.update"`, `"action":"cluster.remove"`, `"cluster":"analytics-dev"`, `"email":"ops@corp.example"`, `"outcome":"failed"`} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log lacks %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, tok) {
		t.Error("the token reached the audit log")
	}
}
