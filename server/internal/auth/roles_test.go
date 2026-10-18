package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

const goodPassword = "correct horse battery staple"

type env struct {
	t     *testing.T
	svc   *auth.Service
	app   *httptest.Server
	audit *syncBuffer
	now   *time.Time
}

func newEnv(t *testing.T, cfg auth.Config) *env {
	t.Helper()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	e := &env{t: t, audit: &syncBuffer{}, now: &now}
	clock := func() time.Time { return *e.now }
	svc, err := auth.New(cfg, nil, audit.New(audit.Options{Out: e.audit, Now: clock}), clock)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.app = console(t, svc)
	t.Cleanup(e.app.Close)
	return e
}

// syncBuffer is the audit log's output in tests: the directory's syncs
// write to it from their own goroutine while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// formIn reads the token of a page the client was served.
func formIn(t *testing.T, c *http.Client, page string) string {
	t.Helper()
	resp, err := c.Get(page)
	if err != nil {
		t.Fatal(err)
	}
	b := readAll(t, resp)
	m := regexp.MustCompile(`name="form" value="([^"]+)"`).FindStringSubmatch(b)
	if m == nil {
		t.Fatalf("no form token on %s:\n%s", page, b)
	}
	return m[1]
}

func (e *env) post(c *http.Client, path string, form url.Values) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.app.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// signIn signs a local user in with a fresh client, or returns the response
// of the refusal.
func (e *env) signIn(login, password string) (*http.Client, *http.Response) {
	e.t.Helper()
	c := client(e.t)
	resp := e.post(c, "/signin", url.Values{"email": {login}, "password": {password}, "form": {formIn(e.t, c, e.app.URL+"/signin")}})
	return c, resp
}

func (e *env) mustSignIn(login, password string) *http.Client {
	e.t.Helper()
	c, resp := e.signIn(login, password)
	body := readAll(e.t, resp)
	if resp.Request.URL.Path != "/" || resp.StatusCode != http.StatusOK {
		e.t.Fatalf("sign-in as %s failed: %s %s", login, resp.Status, body)
	}
	return c
}

func (e *env) api(c *http.Client, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.app.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, buf.Bytes()
}

type who struct {
	User       string   `json:"user"`
	Admin      bool     `json:"admin"`
	Roles      []string `json:"roles"`
	KubeName   string   `json:"kubeName"`
	KubeGroups []string `json:"kubeGroups"`
}

func (e *env) whoami(c *http.Client) (who, int) {
	e.t.Helper()
	code, b := e.api(c, http.MethodGet, "/api/v1/snapshot", nil)
	var w who
	_ = json.Unmarshal(b, &w)
	return w, code
}

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func localConfig(t *testing.T) auth.Config {
	return auth.Config{
		Mode: auth.ModeLocal,
		LocalGroups: []auth.LocalGroup{
			{Name: "web-readers", Role: roles.Viewer, Namespaces: []string{"web"}},
			{Name: "checkout-ops", Role: roles.Admin, Namespaces: []string{"checkout", "web"}},
		},
		SetupToken: "s3cret",
	}
}

func TestFirstRunCreatesTheFirstSuperAdminOnce(t *testing.T) {
	e := newEnv(t, localConfig(t))
	c := client(t)

	// Every page is the first-run page, the API is a 401, and sign-in sends
	// people there.
	resp, _ := c.Get(e.app.URL + "/")
	if b := readAll(t, resp); !strings.Contains(b, "Create the first administrator") || !strings.Contains(b, `data-testid="setup-token"`) {
		t.Fatalf("first-run page: %s", b)
	}
	if code, _ := e.api(client(t), http.MethodGet, "/api/v1/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("API during first run: %d", code)
	}
	// Each attempt needs its own page: the form token pairs with the cookie of
	// the page last served.
	attempt := func(token, name, pw, again string) int {
		r := e.post(c, "/setup", url.Values{"form": {formIn(t, c, e.app.URL+"/setup")}, "token": {token}, "username": {name}, "name": {"Mikhail"}, "password": {pw}, "again": {again}})
		readAll(t, r)
		return r.StatusCode
	}
	for _, tc := range []struct {
		why                    string
		token, name, pw, again string
		code                   int
	}{
		{"wrong token", "nope", "mikhail", goodPassword, goodPassword, http.StatusUnauthorized},
		{"no token", "", "mikhail", goodPassword, goodPassword, http.StatusUnauthorized},
		{"short password", "s3cret", "mikhail", "short", "short", http.StatusBadRequest},
		{"password is the name", "s3cret", "mikhail.stolpner", "mikhail.stolpner", "mikhail.stolpner", http.StatusBadRequest},
		{"passwords differ", "s3cret", "mikhail", goodPassword, goodPassword + "x", http.StatusBadRequest},
		{"reserved name", "s3cret", "system:masters", goodPassword, goodPassword, http.StatusBadRequest},
		{"capital letters", "s3cret", "Mikhail", goodPassword, goodPassword, http.StatusBadRequest},
	} {
		*e.now = e.now.Add(time.Minute) // the page slows guessing, which this is not about
		if got := attempt(tc.token, tc.name, tc.pw, tc.again); got != tc.code {
			t.Errorf("%s: %d, want %d", tc.why, got, tc.code)
		}
	}
	if !strings.Contains(e.audit.String(), `"action":"setup.failed"`) {
		t.Error("failed attempts were not audited")
	}

	r := e.post(c, "/setup", url.Values{"form": {formIn(t, c, e.app.URL+"/setup")}, "token": {"s3cret"}, "username": {"mikhail"}, "name": {"Mikhail"}, "password": {goodPassword}, "again": {goodPassword}})
	readAll(t, r)
	if r.Request.URL.Path != "/" || r.StatusCode != http.StatusOK {
		t.Fatalf("setup: %s %s", r.Status, r.Request.URL)
	}
	w, _ := e.whoami(c)
	if w.User != "mikhail" || !w.Admin || !slices.Equal(w.Roles, []string{"superAdmin"}) {
		t.Fatalf("who: %+v", w)
	}
	if w.KubeName != "221b-local:mikhail" || !slices.Contains(w.KubeGroups, roles.GroupSuperAdmins) || !slices.Contains(w.KubeGroups, roles.GroupConsoleUsers) {
		t.Fatalf("what the clusters see: %+v", w)
	}
	if !strings.Contains(e.audit.String(), `"action":"setup"`) || strings.Contains(e.audit.String(), goodPassword) || strings.Contains(e.audit.String(), "s3cret") {
		t.Fatalf("audit: %s", e.audit.String())
	}

	// It closes for good: a second visit is the normal sign-in, and a post
	// creates nothing.
	resp, _ = client(t).Get(e.app.URL + "/")
	if b := readAll(t, resp); strings.Contains(b, "first administrator") || !strings.Contains(b, "signin-form") {
		t.Fatalf("after setup: %s", b)
	}
	c2 := client(t)
	r = e.post(c2, "/setup", url.Values{"username": {"intruder"}, "password": {goodPassword}, "again": {goodPassword}})
	readAll(t, r)
	if _, resp := e.signIn("intruder", goodPassword); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("intruder: %s", resp.Status)
	}
}

// The first-run page (6.3 review) asks for the setup token only when the
// deployment set one, shows an error beside its field, and keeps the
// passwords when the error is not about them.
func TestFirstRunPageAsksOnlyWhatItNeeds(t *testing.T) {
	cfg := localConfig(t)
	cfg.SetupToken = ""
	e := newEnv(t, cfg)
	resp, _ := client(t).Get(e.app.URL + "/setup")
	if b := readAll(t, resp); strings.Contains(b, `name="token"`) || !strings.Contains(b, `pattern="[a-z0-9._\-]+"`) {
		t.Fatalf("no token set: %s", b)
	}

	e = newEnv(t, localConfig(t))
	c := client(t)
	post := func(token, name, pw, again string) string {
		*e.now = e.now.Add(time.Minute)
		r := e.post(c, "/setup", url.Values{"form": {formIn(t, c, e.app.URL+"/setup")}, "token": {token}, "username": {name}, "password": {pw}, "again": {again}})
		return readAll(t, r)
	}
	b := post("nope", "mikhail", goodPassword, goodPassword)
	if !strings.Contains(b, `id="token-error"`) || strings.Contains(b, `data-testid="setup-error"`) || strings.Count(b, `value="`+goodPassword+`"`) != 2 {
		t.Fatalf("wrong token: %s", b)
	}
	b = post("s3cret", "Mikhail", goodPassword, goodPassword)
	if !strings.Contains(b, `id="username-error"`) || strings.Count(b, `value="`+goodPassword+`"`) != 2 {
		t.Fatalf("bad name: %s", b)
	}
	b = post("s3cret", "mikhail", goodPassword, goodPassword+"x")
	if !strings.Contains(b, `id="again-error"`) || strings.Count(b, `value="`+goodPassword+`"`) != 1 || strings.Contains(b, goodPassword+"x") {
		t.Fatalf("passwords differ: %s", b)
	}
	b = post("s3cret", "mikhail", "short", "short")
	if !strings.Contains(b, `id="password-error"`) || strings.Contains(b, `value="short"`) {
		t.Fatalf("short password: %s", b)
	}
}

func TestFirstRunIsRateLimited(t *testing.T) {
	e := newEnv(t, localConfig(t))
	c := client(t)
	var last int
	for i := 0; i < 7; i++ {
		r := e.post(c, "/setup", url.Values{"form": {formIn(t, c, e.app.URL+"/setup")}, "token": {"wrong"}, "username": {"a"}, "password": {goodPassword}, "again": {goodPassword}})
		last = r.StatusCode
		readAll(t, r)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after many attempts: %d", last)
	}
}

func TestFirstRunStaysClosedWhenTheSavedAccountsCannotBeRead(t *testing.T) {
	cfg := localConfig(t)
	cfg.Accounts = brokenBackend{}
	e := newEnv(t, cfg)
	resp, _ := client(t).Get(e.app.URL + "/")
	if b := readAll(t, resp); strings.Contains(b, "first administrator") {
		t.Fatal("the first-run page opened on an unreadable backend")
	}
}

type brokenBackend struct{}

func (brokenBackend) Load(context.Context) ([]byte, error) { return nil, errors.New("forbidden") }
func (brokenBackend) Save(context.Context, []byte) error   { return errors.New("forbidden") }

// admin signs the first administrator in.
func adminEnv(t *testing.T) (*env, *http.Client) {
	cfg := localConfig(t)
	cfg.Local = []auth.LocalUser{{Username: "root", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}}}
	e := newEnv(t, cfg)
	return e, e.mustSignIn("root", goodPassword)
}

func TestSuperAdminCreatesUsersAndGroupsTakeEffectInOpenSessions(t *testing.T) {
	e, root := adminEnv(t)
	code, b := e.api(root, http.MethodPost, "/api/v1/admin/users", map[string]any{"username": "alice", "name": "Alice", "password": goodPassword, "groups": []string{"web-readers"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	if strings.Contains(string(b), "passwordHash") || strings.Contains(string(b), "pbkdf2") {
		t.Fatalf("a hash left the server: %s", b)
	}
	alice := e.mustSignIn("alice", goodPassword)
	w, _ := e.whoami(alice)
	if w.Admin || !slices.Equal(w.Roles, []string{"viewer"}) || !slices.Contains(w.KubeGroups, "221b-local:web-readers") || slices.Contains(w.KubeGroups, roles.GroupSuperAdmins) {
		t.Fatalf("alice: %+v", w)
	}

	// Not a SuperAdmin: refused everything.
	for _, call := range [][]string{{"GET", "/api/v1/admin/users"}, {"POST", "/api/v1/admin/users"}, {"DELETE", "/api/v1/admin/users/alice"}, {"POST", "/api/v1/admin/ldap/refresh"}} {
		if code, _ := e.api(alice, call[0], call[1], map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%v as a Viewer: %d", call, code)
		}
	}

	// Added to an Admin group: her open session has it at once.
	if code, b := e.api(root, http.MethodPost, "/api/v1/admin/users/alice/groups", map[string]any{"add": []string{"checkout-ops"}}); code != http.StatusOK {
		t.Fatalf("add: %d %s", code, b)
	}
	w, _ = e.whoami(alice)
	if !slices.Equal(w.Roles, []string{"admin", "viewer"}) || !slices.Contains(w.KubeGroups, "221b-local:checkout-ops") {
		t.Fatalf("alice after joining: %+v", w)
	}
	// And a group that is not defined cannot be joined.
	if code, _ := e.api(root, http.MethodPost, "/api/v1/admin/users/alice/groups", map[string]any{"add": []string{"made-up"}}); code != http.StatusBadRequest {
		t.Fatalf("an undefined group: %d", code)
	}
	if code, _ := e.api(root, http.MethodPost, "/api/v1/admin/users/alice/groups", map[string]any{"remove": []string{"checkout-ops", "web-readers"}}); code != http.StatusOK {
		t.Fatal("remove")
	}
	if w, _ = e.whoami(alice); len(w.Roles) != 0 || slices.Contains(w.KubeGroups, "221b-local:web-readers") {
		t.Fatalf("alice after leaving: %+v", w)
	}

	// A super role given and taken, in the open session.
	if code, b := e.api(root, http.MethodPut, "/api/v1/admin/users/alice/super", map[string]any{"roles": []string{"superViewer"}}); code != http.StatusOK {
		t.Fatalf("super: %d %s", code, b)
	}
	w, _ = e.whoami(alice)
	if w.Admin || !slices.Equal(w.Roles, []string{"superViewer"}) || !slices.Contains(w.KubeGroups, roles.GroupSuperViewers) {
		t.Fatalf("a SuperViewer: %+v", w)
	}
	e.api(root, http.MethodPut, "/api/v1/admin/users/alice/super", map[string]any{"roles": []string{"superAdmin"}})
	if w, _ = e.whoami(alice); !w.Admin {
		t.Fatalf("a SuperAdmin: %+v", w)
	}
	e.api(root, http.MethodPut, "/api/v1/admin/users/alice/super", map[string]any{"roles": []string{}})
	if w, _ = e.whoami(alice); w.Admin || slices.Contains(w.KubeGroups, roles.GroupSuperAdmins) {
		t.Fatalf("after taking the role: %+v", w)
	}
	if code, _ := e.api(root, http.MethodPut, "/api/v1/admin/users/alice/super", map[string]any{"roles": []string{"admin"}}); code != http.StatusBadRequest {
		t.Fatalf("admin as a super role: %d", code)
	}
	for _, a := range []string{`"action":"user.create"`, `"action":"user.groups"`, `"action":"user.super"`} {
		if !strings.Contains(e.audit.String(), a) {
			t.Errorf("no audit record %s", a)
		}
	}
}

func TestUserRulesAndProtections(t *testing.T) {
	e, root := adminEnv(t)
	create := func(m map[string]any) int {
		code, _ := e.api(root, http.MethodPost, "/api/v1/admin/users", m)
		return code
	}
	for _, tc := range []struct {
		why string
		m   map[string]any
		ok  bool
	}{
		{"capital letters", map[string]any{"username": "Alice", "password": goodPassword}, false},
		{"reserved prefix", map[string]any{"username": "system:masters", "password": goodPassword}, false},
		{"another reserved name", map[string]any{"username": "221b:superadmins", "password": goodPassword}, false},
		{"short password", map[string]any{"username": "bob", "password": "short"}, false},
		{"password is the name", map[string]any{"username": "bobbybobbybob", "password": "bobbybobbybob"}, false},
		{"unknown group", map[string]any{"username": "bob", "password": goodPassword, "groups": []string{"nope"}}, false},
		{"fine", map[string]any{"username": "bob", "password": goodPassword}, true},
		{"twice", map[string]any{"username": "bob", "password": goodPassword}, false},
		{"as an existing managed name", map[string]any{"username": "root", "password": goodPassword}, false},
	} {
		if got := create(tc.m) == http.StatusCreated; got != tc.ok {
			t.Errorf("%s: created=%v", tc.why, got)
		}
	}

	// Managed users are shown and never changed.
	for _, call := range []struct{ m, p string }{{"PATCH", "/api/v1/admin/users/root"}, {"DELETE", "/api/v1/admin/users/root"}} {
		if code, _ := e.api(root, call.m, call.p, map[string]any{"disabled": true}); code != http.StatusForbidden {
			t.Errorf("%s %s: %d", call.m, call.p, code)
		}
	}
	// The last SuperAdmin cannot lose the role, be disabled or be deleted.
	if code, b := e.api(root, http.MethodPost, "/api/v1/admin/users", map[string]any{"username": "carol", "password": goodPassword}); code != http.StatusCreated {
		t.Fatalf("carol: %d %s", code, b)
	}
	e.api(root, http.MethodPut, "/api/v1/admin/users/carol/super", map[string]any{"roles": []string{"superAdmin"}})
	carol := e.mustSignIn("carol", goodPassword)
	// carol is the only stored SuperAdmin besides managed root; root counts, so carol can go.
	if code, _ := e.api(root, http.MethodPut, "/api/v1/admin/users/carol/super", map[string]any{"roles": []string{}}); code != http.StatusOK {
		t.Fatalf("taking the role while another SuperAdmin exists: %d", code)
	}
	if w, _ := e.whoami(carol); w.Admin {
		t.Fatal("carol is still an admin")
	}
}

func TestLastSuperAdminCannotBeRemoved(t *testing.T) {
	e := newEnv(t, localConfig(t))
	c := client(t)
	r := e.post(c, "/setup", url.Values{"form": {formIn(t, c, e.app.URL+"/setup")}, "token": {"s3cret"}, "username": {"mikhail"}, "password": {goodPassword}, "again": {goodPassword}})
	readAll(t, r)
	for _, tc := range []struct {
		m, p string
		body any
	}{
		{"PUT", "/api/v1/admin/users/mikhail/super", map[string]any{"roles": []string{}}},
		{"PATCH", "/api/v1/admin/users/mikhail", map[string]any{"disabled": true}},
		{"DELETE", "/api/v1/admin/users/mikhail", nil},
	} {
		if code, b := e.api(c, tc.m, tc.p, tc.body); code != http.StatusConflict || !strings.Contains(string(b), "last SuperAdmin") {
			t.Errorf("%s %s: %d %s", tc.m, tc.p, code, b)
		}
	}
	// With a second SuperAdmin the first can go.
	e.api(c, "POST", "/api/v1/admin/users", map[string]any{"username": "second", "password": goodPassword})
	e.api(c, "PUT", "/api/v1/admin/users/second/super", map[string]any{"roles": []string{"superAdmin"}})
	if code, _ := e.api(c, "PUT", "/api/v1/admin/users/mikhail/super", map[string]any{"roles": []string{}}); code != http.StatusOK {
		t.Fatalf("with a second SuperAdmin: %d", code)
	}
}

func TestDisableResetAndDeleteEndSessions(t *testing.T) {
	e, root := adminEnv(t)
	e.api(root, "POST", "/api/v1/admin/users", map[string]any{"username": "dave", "password": goodPassword, "groups": []string{"web-readers"}})
	check := func(what string, act func()) {
		t.Helper()
		*e.now = e.now.Add(time.Minute)
		dave := e.mustSignIn("dave", goodPassword)
		if _, code := e.whoami(dave); code != http.StatusOK {
			t.Fatal("not signed in")
		}
		act()
		if _, code := e.whoami(dave); code != http.StatusUnauthorized {
			t.Errorf("%s left the session open: %d", what, code)
		}
	}
	// A change needs the SuperAdmin's own password (6.4).
	for _, own := range []map[string]any{{"password": "another long password"}, {"password": "another long password", "currentPassword": "not my password"}} {
		if code, b := e.api(root, "PATCH", "/api/v1/admin/users/dave", own); code != http.StatusForbidden || !strings.Contains(string(b), "Your password is not right") {
			t.Fatalf("without the SuperAdmin's own password: %d %s", code, b)
		}
	}
	check("a reset", func() {
		if code, b := e.api(root, "PATCH", "/api/v1/admin/users/dave", map[string]any{"password": "another long password", "currentPassword": goodPassword}); code != http.StatusOK {
			t.Fatalf("%d %s", code, b)
		}
	})
	if _, resp := e.signIn("dave", goodPassword); resp.StatusCode != http.StatusUnauthorized {
		t.Error("the old password still works")
	}
	goodPW := "another long password"
	e.mustSignIn("dave", goodPW)
	if code, _ := e.api(root, "PATCH", "/api/v1/admin/users/dave", map[string]any{"password": "short", "currentPassword": goodPassword}); code != http.StatusBadRequest {
		t.Error("a short reset was accepted")
	}
	pw := goodPW
	check2 := func(what string, act func()) {
		t.Helper()
		*e.now = e.now.Add(time.Minute)
		dave := e.mustSignIn("dave", pw)
		act()
		if _, code := e.whoami(dave); code != http.StatusUnauthorized {
			t.Errorf("%s left the session open: %d", what, code)
		}
	}
	check2("a disable", func() { e.api(root, "PATCH", "/api/v1/admin/users/dave", map[string]any{"disabled": true}) })
	if _, resp := e.signIn("dave", pw); resp.StatusCode != http.StatusUnauthorized {
		t.Error("a disabled user signed in")
	}
	e.api(root, "PATCH", "/api/v1/admin/users/dave", map[string]any{"disabled": false})
	*e.now = e.now.Add(time.Minute)
	check2("signing out everywhere", func() {
		if code, _ := e.api(root, "DELETE", "/api/v1/admin/users/dave/sessions", nil); code != http.StatusNoContent {
			t.Fatal("sessions")
		}
	})
	check2("a delete", func() {
		if code, _ := e.api(root, "DELETE", "/api/v1/admin/users/dave", nil); code != http.StatusNoContent {
			t.Fatal("delete")
		}
	})
	if _, resp := e.signIn("dave", pw); resp.StatusCode != http.StatusUnauthorized {
		t.Error("a deleted user signed in")
	}
}

// changePassword posts the sign-in page's change-password form with a fresh
// client.
func (e *env) changePassword(user, current, password, again string) *http.Response {
	e.t.Helper()
	c := client(e.t)
	return e.post(c, "/signin/password", url.Values{"form": {formIn(e.t, c, e.app.URL+"/signin/password")}, "username": {user}, "current": {current}, "password": {password}, "again": {again}})
}

func TestPeopleChangeTheirOwnPasswordFromTheSignInPage(t *testing.T) {
	e, root := adminEnv(t)
	e.api(root, "POST", "/api/v1/admin/users", map[string]any{"username": "gina", "password": goodPassword})
	page, err := client(t).Get(e.app.URL + "/signin")
	if err != nil {
		t.Fatal(err)
	}
	if b := readAll(t, page); !strings.Contains(b, `href="/signin/password`) {
		t.Fatalf("no Change password link on the sign-in page: %s", b)
	}
	const next = "a new long password"
	for _, c := range []struct {
		name, user, current, password, again string
		code                                 int
		says                                 string
	}{
		{"wrong current password", "gina", "wrong wrong wrong", next, next, http.StatusUnauthorized, "Your password was not changed"},
		{"unknown account", "nobody", goodPassword, next, next, http.StatusUnauthorized, "Your password was not changed"},
		{"too short", "gina", goodPassword, "short", "short", http.StatusBadRequest, `id="password-error"`},
		{"the same as before", "gina", goodPassword, goodPassword, goodPassword, http.StatusBadRequest, "not the current one"},
		{"repeat differs", "gina", goodPassword, next, next + "x", http.StatusBadRequest, `id="again-error"`},
		{"defined in values", "root", goodPassword, next, next, http.StatusBadRequest, "helm upgrade"},
	} {
		*e.now = e.now.Add(time.Minute)
		r := e.changePassword(c.user, c.current, c.password, c.again)
		if b := readAll(t, r); r.StatusCode != c.code || !strings.Contains(b, c.says) {
			t.Errorf("%s: %d, want %d saying %q: %s", c.name, r.StatusCode, c.code, c.says, b)
		}
	}
	*e.now = e.now.Add(time.Minute)
	gina := e.mustSignIn("gina", goodPassword)
	r := e.changePassword("gina", goodPassword, next, next)
	if b := readAll(t, r); r.Request.URL.Path != "/signin" || !strings.Contains(b, "Your password is changed") {
		t.Fatalf("after a change: %s %s", r.Request.URL, b)
	}
	if _, code := e.whoami(gina); code != http.StatusUnauthorized {
		t.Error("the change left a session open")
	}
	if _, resp := e.signIn("gina", goodPassword); resp.StatusCode != http.StatusUnauthorized {
		t.Error("the old password still works")
	}
	e.mustSignIn("gina", next)
	if !strings.Contains(e.audit.String(), `"action":"password.change"`) {
		t.Error("no audit record")
	}
}

func TestRefusalIsTheSameWhateverTheReason(t *testing.T) {
	e, root := adminEnv(t)
	e.api(root, "POST", "/api/v1/admin/users", map[string]any{"username": "erin", "password": goodPassword})
	e.api(root, "PATCH", "/api/v1/admin/users/erin", map[string]any{"disabled": true})
	var bodies []string
	for _, c := range [][2]string{{"erin", goodPassword}, {"erin", "wrong wrong wrong"}, {"nobody", goodPassword}} {
		_, resp := e.signIn(c[0], c[1])
		bodies = append(bodies, strings.Join(strings.Fields(regexp.MustCompile(`(?s)<p class="si-error".*?</p>`).FindString(readAll(t, resp))), " "))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%v: %d", c, resp.StatusCode)
		}
	}
	if bodies[0] == "" || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("the refusals differ: %q", bodies)
	}
	out := e.audit.String()
	for _, reason := range []string{"the account is disabled", "wrong user name or password"} {
		if !strings.Contains(out, reason) {
			t.Errorf("the audit record lacks %q", reason)
		}
	}
}

func TestUsersListHoldsNoSecrets(t *testing.T) {
	e, root := adminEnv(t)
	e.api(root, "POST", "/api/v1/admin/users", map[string]any{"username": "frank", "name": "Frank", "password": goodPassword, "groups": []string{"checkout-ops"}})
	code, b := e.api(root, "GET", "/api/v1/admin/users", nil)
	if code != http.StatusOK || strings.Contains(string(b), "pbkdf2") || strings.Contains(string(b), "passwordHash") {
		t.Fatalf("%d %s", code, b)
	}
	var out struct {
		Users []struct {
			ID, Username, Source, Status, KubeName string
			Managed, Self                          bool
			Holdings                               []struct {
				Role       string
				Via        string
				Namespaces []string
			}
		}
		Groups []struct {
			Name    string
			Members int
		}
		CanCreate  bool
		Persistent bool
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.CanCreate || out.Persistent {
		t.Errorf("canCreate=%v persistent=%v (memory is not persistent)", out.CanCreate, out.Persistent)
	}
	var frank, rootRow bool
	for _, u := range out.Users {
		switch u.Username {
		case "frank":
			frank = u.KubeName == "221b-local:frank" && len(u.Holdings) == 1 && u.Holdings[0].Role == "admin" && u.Holdings[0].Via == "checkout-ops" && slices.Equal(u.Holdings[0].Namespaces, []string{"checkout", "web"})
		case "root":
			rootRow = u.Managed && u.Self && u.Holdings[0].Role == "superAdmin"
		}
	}
	if !frank || !rootRow {
		t.Errorf("rows: %+v", out.Users)
	}
	for _, g := range out.Groups {
		if g.Name == "checkout-ops" && g.Members != 1 {
			t.Errorf("members = %d", g.Members)
		}
	}
}

func TestReservedGroupNamesAreDropped(t *testing.T) {
	cfg := localConfig(t)
	cfg.Local = []auth.LocalUser{{Username: "gina", PasswordHash: hashOf(t, goodPassword), Groups: []string{"web-readers", "221b:superadmins", "system:masters"}}}
	e := newEnv(t, cfg)
	c := e.mustSignIn("gina", goodPassword)
	w, _ := e.whoami(c)
	if w.Admin || slices.Contains(w.KubeGroups, roles.GroupSuperAdmins) || slices.Contains(w.KubeGroups, "system:masters") {
		t.Fatalf("a reserved group got through: %+v", w)
	}
	if !slices.Contains(w.KubeGroups, "221b-local:web-readers") || !slices.Contains(w.KubeGroups, roles.GroupConsoleUsers) {
		t.Fatalf("groups: %+v", w)
	}
	if strings.Count(e.audit.String(), `"action":"name.refused"`) != 1 {
		t.Fatalf("audit: %s", e.audit.String())
	}
}

func TestOIDCKeepsAdminsAndTheListsGiveSuperRoles(t *testing.T) {
	// Today's auth.admins and adminGroups mean SuperAdmin (RB-04, RB-57); the
	// lists give the rest. The groups sent on are only the listed ones.
	for _, tc := range []struct {
		name   string
		cfg    auth.Config
		groups []string
		admin  bool
		roles  []string
		sent   []string
	}{
		{"admins", auth.Config{Admins: []string{"sam@corp.example"}}, []string{"x"}, true, []string{"superAdmin"}, nil},
		{"adminGroups", auth.Config{AdminGroups: []string{"platform"}}, []string{"platform"}, true, []string{"superAdmin"}, nil},
		{"lists", auth.Config{Lists: auth.RoleLists{roles.Admin: {"team-a"}, roles.SuperViewer: {"auditors"}}}, []string{"team-a", "auditors", "random"}, false, []string{"superViewer", "admin"}, []string{"oidc:team-a", "oidc:auditors"}},
		{"none listed", auth.Config{}, []string{"anything"}, false, nil, []string{"oidc:anything"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Mode = auth.ModeOIDC
			cfg.Prefixes = roles.Prefixes{OIDCUser: "oidc-user:", OIDCGroup: "oidc:"}
			sess := auth.GrantFor(cfg, roles.OIDC, auth.User{Username: "sam@corp.example", Email: "sam@corp.example", Groups: tc.groups}, nil)
			if sess.Admin() != tc.admin || !slices.Equal(toStrings(sess.Roles), tc.roles) {
				t.Fatalf("roles = %v admin=%v", sess.Roles, sess.Admin())
			}
			if sess.KubeName != "oidc-user:sam@corp.example" {
				t.Fatalf("name = %s", sess.KubeName)
			}
			for _, g := range tc.sent {
				if !slices.Contains(sess.KubeGroups, g) {
					t.Errorf("missing %s in %v", g, sess.KubeGroups)
				}
			}
			if tc.name == "lists" && slices.Contains(sess.KubeGroups, "oidc:random") {
				t.Errorf("an unlisted group was asserted: %v", sess.KubeGroups)
			}
		})
	}
}

func toStrings(ids []roles.ID) []string {
	var out []string
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// ---- LDAP, with a directory made for the test ----

type fakeDir struct {
	mu      sync.Mutex
	people  map[string]*auth.Person
	pw      map[string]string
	down    bool
	members []auth.Person
	syncs   int
	block   chan struct{}
}

func (d *fakeDir) URL() string { return "ldaps://ldap.example:636" }

func (d *fakeDir) Authenticate(_ context.Context, login, password string) (auth.Person, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return auth.Person{}, auth.ErrDirectoryDown
	}
	p, ok := d.people[login]
	if !ok || d.pw[login] != password {
		return auth.Person{}, auth.ErrBadCredentials
	}
	return *p, nil
}

func (d *fakeDir) Groups(_ context.Context, ref string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return nil, auth.ErrDirectoryDown
	}
	for _, p := range d.people {
		if p.Ref == ref {
			return append([]string(nil), p.Groups...), nil
		}
	}
	return nil, auth.ErrBadCredentials
}

func (d *fakeDir) Members(ctx context.Context, groups []string) ([]auth.Person, error) {
	if d.block != nil {
		select {
		case <-d.block:
		case <-ctx.Done():
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.syncs++
	if d.down {
		return nil, auth.ErrDirectoryDown
	}
	return append([]auth.Person(nil), d.members...), nil
}

func ldapEnv(t *testing.T) (*env, *fakeDir) {
	d := &fakeDir{
		people: map[string]*auth.Person{
			"priya": {Ref: "uid=priya", Username: "priya", Name: "Priya", Email: "priya@corp.example", Groups: []string{"platform-admins"}},
			"omar":  {Ref: "uid=omar", Username: "omar", Name: "Omar", Groups: []string{"team-a-ops", "221b:superadmins"}},
			"zed":   {Ref: "uid=zed", Username: "zed", Name: "Zed", Groups: []string{"everyone"}},
		},
		pw: map[string]string{"priya": goodPassword, "omar": goodPassword, "zed": goodPassword},
	}
	cfg := auth.Config{
		Mode:      auth.ModeLDAP,
		Directory: d,
		Lists:     auth.RoleLists{roles.SuperAdmin: {"platform-admins"}, roles.Admin: {"team-a-ops"}, roles.Viewer: {"readers"}},
		Local:     []auth.LocalUser{{Username: "break-glass", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}}},
		AccessTTL: 2 * time.Minute,
	}
	return newEnv(t, cfg), d
}

func TestLDAPSignInNeedsAListedGroupAndSaysNothingElse(t *testing.T) {
	e, d := ldapEnv(t)
	priya := e.mustSignIn("priya", goodPassword)
	w, _ := e.whoami(priya)
	if !w.Admin || w.KubeName != "221b-ldap:priya" || !slices.Contains(w.KubeGroups, "221b-ldap:platform-admins") || !slices.Contains(w.KubeGroups, roles.GroupSuperAdmins) {
		t.Fatalf("priya: %+v", w)
	}
	omar := e.mustSignIn("omar", goodPassword)
	w, _ = e.whoami(omar)
	if w.Admin || !slices.Equal(w.Roles, []string{"admin"}) || slices.Contains(w.KubeGroups, roles.GroupSuperAdmins) || !slices.Contains(w.KubeGroups, "221b-ldap:team-a-ops") {
		t.Fatalf("a directory group named 221b:superadmins gave something: %+v", w)
	}
	if strings.Count(e.audit.String(), `"action":"name.refused"`) != 1 {
		t.Errorf("audit: %s", e.audit.String())
	}

	var bodies []string
	for _, c := range [][2]string{{"zed", goodPassword}, {"zed", "wrong"}, {"nobody", "x"}} {
		_, resp := e.signIn(c[0], c[1])
		bodies = append(bodies, strings.Join(strings.Fields(regexp.MustCompile(`(?s)<p class="si-error".*?</p>`).FindString(readAll(t, resp))), " "))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%v: %d", c, resp.StatusCode)
		}
	}
	if bodies[0] == "" || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("the refusals differ: %q", bodies)
	}
	if !strings.Contains(e.audit.String(), "in none of the groups listed for a role") {
		t.Error("the audit record does not say why")
	}

	// A local break-glass account still signs in with the directory in place.
	if w, _ := e.whoami(e.mustSignIn("break-glass", goodPassword)); !w.Admin || w.KubeName != "221b-local:break-glass" {
		t.Fatalf("break-glass: %+v", w)
	}

	// Directory down: a new sign-in gets a clear message; an open session works.
	d.mu.Lock()
	d.down = true
	d.mu.Unlock()
	_, resp := e.signIn("priya", goodPassword)
	if b := readAll(t, resp); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(b, "cannot be reached") {
		t.Fatalf("directory down: %s %s", resp.Status, b)
	}
	if _, code := e.whoami(priya); code != http.StatusOK {
		t.Fatalf("an open session during the outage: %d", code)
	}
}

func TestLDAPGroupsAreReadAgainWhenTheyHaveStood(t *testing.T) {
	e, d := ldapEnv(t)
	omar := e.mustSignIn("omar", goodPassword)
	if w, _ := e.whoami(omar); !slices.Equal(w.Roles, []string{"admin"}) {
		t.Fatalf("omar: %+v", w)
	}
	// Moved to the readers group: nothing changes before the access lifetime,
	// and within it, with no new sign-in, he is a Viewer.
	d.mu.Lock()
	d.people["omar"].Groups = []string{"readers"}
	d.mu.Unlock()
	*e.now = e.now.Add(90 * time.Second)
	if w, _ := e.whoami(omar); !slices.Equal(w.Roles, []string{"admin"}) {
		t.Fatalf("too soon: %+v", w)
	}
	*e.now = e.now.Add(40 * time.Second)
	w, _ := e.whoami(omar)
	if !slices.Equal(w.Roles, []string{"viewer"}) || !slices.Contains(w.KubeGroups, "221b-ldap:readers") || slices.Contains(w.KubeGroups, "221b-ldap:team-a-ops") {
		t.Fatalf("after the move: %+v", w)
	}
	// The directory cannot answer: the old groups stand.
	d.mu.Lock()
	d.down = true
	d.mu.Unlock()
	*e.now = e.now.Add(3 * time.Minute)
	if w, code := e.whoami(omar); code != http.StatusOK || !slices.Equal(w.Roles, []string{"viewer"}) {
		t.Fatalf("directory down: %d %+v", code, w)
	}
	// Removed from every listed group: the session ends.
	d.mu.Lock()
	d.down = false
	d.people["omar"].Groups = []string{"everyone"}
	d.mu.Unlock()
	*e.now = e.now.Add(3 * time.Minute)
	if _, code := e.whoami(omar); code != http.StatusUnauthorized {
		t.Fatalf("a person in no listed group kept the session: %d", code)
	}
}

func TestLDAPRefreshRunsOneSyncAtATimeAndKeepsTheListOnFailure(t *testing.T) {
	e, d := ldapEnv(t)
	root := e.mustSignIn("break-glass", goodPassword)
	d.mu.Lock()
	d.members = []auth.Person{{Username: "priya", Name: "Priya", Groups: []string{"platform-admins"}}, {Username: "omar", Name: "Omar", Groups: []string{"team-a-ops"}}}
	d.block = make(chan struct{})
	d.mu.Unlock()

	for i := 0; i < 2; i++ {
		if code, b := e.api(root, "POST", "/api/v1/admin/ldap/refresh", nil); code != http.StatusAccepted || !strings.Contains(string(b), `"running":true`) {
			t.Fatalf("refresh %d: %d %s", i, code, b)
		}
	}
	close(d.block)
	waitFor(t, func() bool {
		var out struct{ LDAP struct{ Running bool } }
		_, b := e.api(root, "GET", "/api/v1/admin/users", nil)
		_ = json.Unmarshal(b, &out)
		return !out.LDAP.Running
	})
	d.mu.Lock()
	syncs := d.syncs
	d.mu.Unlock()
	if syncs != 1 {
		t.Fatalf("two clicks ran %d syncs", syncs)
	}
	list := func() (people int, status map[string]string, state string) {
		var out struct {
			LDAP struct {
				People int
				State  string
			}
			Users []struct{ Username, Source, Status, StatusNote string }
		}
		_, b := e.api(root, "GET", "/api/v1/admin/users", nil)
		_ = json.Unmarshal(b, &out)
		status = map[string]string{}
		for _, u := range out.Users {
			if u.Source == "ldap" {
				status[u.Username] = u.Status
			}
		}
		return out.LDAP.People, status, out.LDAP.State
	}
	if n, st, state := list(); n != 2 || st["priya"] != "active" || st["omar"] != "active" || state != "connected" {
		t.Fatalf("after the first sync: %d %v %s", n, st, state)
	}

	// Omar leaves the group: disabled, not deleted.
	d.mu.Lock()
	d.block = nil
	d.members = d.members[:1]
	d.mu.Unlock()
	*e.now = e.now.Add(5 * time.Minute)
	e.svc.SyncDirectoryAsync()
	waitFor(t, func() bool { _, st, _ := list(); return st["omar"] == "disabled" })
	_, st, _ := list()
	if st["priya"] != "active" {
		t.Fatalf("statuses: %v", st)
	}

	// The directory stops: the list stays, and the page says so. The sync
	// that disabled Omar may still be finishing; a second one is not
	// started while one runs, so wait for it.
	waitFor(t, func() bool {
		var out struct{ LDAP struct{ Running bool } }
		_, b := e.api(root, "GET", "/api/v1/admin/users", nil)
		_ = json.Unmarshal(b, &out)
		return !out.LDAP.Running
	})
	d.mu.Lock()
	d.down = true
	d.mu.Unlock()
	e.svc.SyncDirectoryAsync()
	waitFor(t, func() bool { _, _, state := list(); return state == "error" })
	if _, st, _ := list(); st["priya"] != "active" || st["omar"] != "disabled" {
		t.Fatalf("the list changed on a failed sync: %v", st)
	}
	if !strings.Contains(e.audit.String(), `"action":"ldap.sync"`) {
		t.Error("no audit record of the sync")
	}
	// No one can create local users with the directory in place (RB-28).
	if code, _ := e.api(root, "POST", "/api/v1/admin/users", map[string]any{"username": "x", "password": goodPassword}); code != http.StatusConflict {
		t.Fatalf("creating a local user with LDAP: %d", code)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestLDAPNeedsADirectoryAndAListedGroup(t *testing.T) {
	if _, err := auth.New(auth.Config{Mode: auth.ModeLDAP, Lists: auth.RoleLists{roles.Viewer: {"x"}}}, nil, nil, nil); err == nil {
		t.Error("LDAP without a directory")
	}
	if _, err := auth.New(auth.Config{Mode: auth.ModeLDAP, Directory: &fakeDir{}}, nil, nil, nil); err == nil {
		t.Error("LDAP with no group listed")
	}
	if _, err := auth.New(auth.Config{Mode: auth.ModeLocal, LocalGroups: []auth.LocalGroup{{Name: "x", Role: roles.SuperAdmin}}}, nil, nil, nil); err == nil {
		t.Error("a local group with a super role")
	}
}

func TestRolesViewHelpers(t *testing.T) {
	cfg := auth.Config{
		Mode:        auth.ModeLocal,
		LocalGroups: []auth.LocalGroup{{Name: "web-readers", Role: roles.Viewer, Namespaces: []string{"web"}}},
		Local: []auth.LocalUser{
			{Username: "root", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}},
			{Username: "vic", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superViewer"}, Groups: []string{"web-readers"}},
			{Username: "wes", PasswordHash: hashOf(t, goodPassword), Groups: []string{"web-readers"}},
		},
		Lists: auth.RoleLists{roles.SuperViewer: {"auditors"}},
	}
	e := newEnv(t, cfg)
	h := e.svc.Holders()
	if h[roles.SuperAdmin] != (roles.Holders{Users: 1}) {
		t.Errorf("SuperAdmin holders = %+v", h[roles.SuperAdmin])
	}
	if h[roles.SuperViewer] != (roles.Holders{Users: 1, Groups: 1}) {
		t.Errorf("SuperViewer holders = %+v", h[roles.SuperViewer])
	}
	if n := e.svc.Members("web-readers"); n != 2 {
		t.Errorf("web-readers has %d members, want 2", n)
	}
	if !e.svc.UserExists("wes") || e.svc.UserExists("nobody") {
		t.Error("UserExists is wrong")
	}
}
