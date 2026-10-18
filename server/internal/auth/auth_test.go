package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/oidcsim"
)

// console wires the service into a mux the way the API server does, with
// a protected page and API behind the gate.
func console(t *testing.T, svc *auth.Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range svc.Routes() {
		mux.HandleFunc(r.Pattern, r.Handler)
	}
	mux.HandleFunc("GET /api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		sess, _ := auth.SessionFrom(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{"user": sess.User.Username, "admin": sess.Admin, "roles": sess.Roles, "kubeName": sess.User.KubeName, "kubeGroups": sess.User.KubeGroups})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>the app</html>")) })
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r = svc.Gate(w, r); r != nil {
			mux.ServeHTTP(w, r)
		}
	}))
}

// formToken opens the sign-in page with the client and returns the form's
// token, which the page's cookie now matches.
func formToken(t *testing.T, c *http.Client, app string) string {
	t.Helper()
	resp, err := c.Get(app + "/signin")
	if err != nil {
		t.Fatal(err)
	}
	b := readAll(t, resp)
	m := regexp.MustCompile(`name="form" value="([^"]+)"`).FindStringSubmatch(b)
	if m == nil {
		t.Fatalf("no form token in the sign-in page:\n%s", b)
	}
	return m[1]
}

func client(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 10 {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

func TestOIDCSignInEndToEnd(t *testing.T) {
	// The provider is mounted on its own server, as a real one would be.
	var idp *oidcsim.Provider
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { idp.ServeHTTP(w, r) }))
	defer idpSrv.Close()
	var err error
	idp, err = oidcsim.New(oidcsim.Options{Issuer: idpSrv.URL + "/oidc"})
	if err != nil {
		t.Fatal(err)
	}
	var auditOut bytes.Buffer
	log := audit.New(audit.Options{Out: &auditOut})
	svc, err := auth.New(auth.Config{
		Mode:        auth.ModeOIDC,
		OIDC:        auth.OIDCConfig{Issuer: idpSrv.URL + "/oidc", ClientID: "221b", ClientSecret: "221b-secret", ProviderName: "Simulated"},
		AdminGroups: []string{"platform-admins"},
	}, nil, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := console(t, svc)
	defer app.Close()
	c := client(t)

	// Without a session: the API says 401, a page shows the sign-in page.
	resp, _ := c.Get(app.URL + "/api/v1/snapshot")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API without a session: %s", resp.Status)
	}
	resp.Body.Close()
	resp, _ = c.Get(app.URL + "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health probe needs no session: %s", resp.Status)
	}
	resp.Body.Close()
	resp, _ = c.Get(app.URL + "/")
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Sign in with Simulated") || strings.Contains(body, "the app") {
		t.Fatalf("page without a session: %s %q", resp.Status, body)
	}

	// Start the sign-in: the console sends the browser to the provider with PKCE.
	resp, err = c.Get(app.URL + "/signin/start?next=" + url.QueryEscape("/#/pods?ns=web"))
	if err != nil {
		t.Fatal(err)
	}
	form := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(form, "Simulated identity provider") {
		t.Fatalf("expected the provider's form, got %s: %s", resp.Status, form)
	}
	authURL := resp.Request.URL // the provider's /auth with the request in the query
	if authURL.Query().Get("code_challenge_method") != "S256" || authURL.Query().Get("nonce") == "" {
		t.Fatalf("the authorization request lacks PKCE or a nonce: %s", authURL)
	}

	// A wrong password stays at the provider.
	q := authURL.Query()
	q.Set("email", "admin@corp.example")
	q.Set("password", "wrong")
	resp, _ = c.PostForm(idpSrv.URL+"/oidc/auth", q)
	if b := readAll(t, resp); !strings.Contains(b, "Wrong email or password") {
		t.Fatalf("wrong password: %s", b)
	}

	// The right one comes back to the console signed in, at the page asked for.
	q.Set("password", "password")
	resp, err = c.PostForm(idpSrv.URL+"/oidc/auth", q)
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "the app") || resp.Request.URL.Path != "/" || resp.Request.URL.Fragment != "/pods?ns=web" {
		t.Fatalf("after sign-in: %s at %s: %q", resp.Status, resp.Request.URL, body)
	}
	var me auth.Me
	getJSON(t, c, app.URL+"/api/v1/me", &me)
	if me.User.Username != "admin@corp.example" || me.User.Name != "Ada Admin" || !me.Admin || me.Method != "oidc" || me.Mode != auth.ModeOIDC {
		t.Fatalf("me: %+v", me)
	}
	if !strings.Contains(strings.Join(me.User.Groups, ","), "platform-admins") {
		t.Fatalf("groups: %v", me.User.Groups)
	}
	var snap map[string]any
	getJSON(t, c, app.URL+"/api/v1/snapshot", &snap)
	if snap["user"] != "admin@corp.example" || snap["admin"] != true {
		t.Fatalf("the session reaches the API: %v", snap)
	}
	// The cookie is HttpOnly and SameSite=Lax; not Secure over plain HTTP.
	var sessionCookie *http.Cookie
	for _, ck := range c.Jar.Cookies(mustURL(app.URL)) {
		if ck.Name == "221b_session" {
			sessionCookie = ck
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}
	// The sign-in's own cookie is gone once it has done its work.
	for _, ck := range c.Jar.Cookies(mustURL(app.URL + "/signin/callback")) {
		if ck.Name == "221b_signin" {
			t.Error("the sign-in state cookie was left behind")
		}
	}

	// The audit log has the sign-in, naming the person.
	if !strings.Contains(auditOut.String(), `"action":"signin"`) || !strings.Contains(auditOut.String(), `"email":"admin@corp.example"`) {
		t.Fatalf("audit: %s", auditOut.String())
	}

	// Sign out: the API is closed again.
	req, _ := http.NewRequest(http.MethodPost, app.URL+"/signout", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, _ = c.Do(req)
	resp.Body.Close()
	resp, _ = c.Get(app.URL + "/api/v1/me")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after sign-out: %s", resp.Status)
	}
	resp.Body.Close()
	if !strings.Contains(auditOut.String(), `"action":"signout"`) {
		t.Fatalf("audit: %s", auditOut.String())
	}
	if idp.SignIns != 1 {
		t.Fatalf("provider sign-ins: %d", idp.SignIns)
	}
}

// A provider that names someone with a name Kubernetes or 221B reserves
// does not sign them in (RB-17): the console would act as that name.
func TestOIDCRefusesAReservedUserName(t *testing.T) {
	var idp *oidcsim.Provider
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { idp.ServeHTTP(w, r) }))
	defer idpSrv.Close()
	var err error
	idp, err = oidcsim.New(oidcsim.Options{Issuer: idpSrv.URL + "/oidc", Users: []oidcsim.User{{Email: "system:kube-controller-manager", Name: "Mallory", Password: "password"}}})
	if err != nil {
		t.Fatal(err)
	}
	var auditOut syncBuffer
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{Issuer: idpSrv.URL + "/oidc", ClientID: "221b", ClientSecret: "221b-secret"},
	}, nil, audit.New(audit.Options{Out: &auditOut}), nil)
	if err != nil {
		t.Fatal(err)
	}
	app := console(t, svc)
	defer app.Close()
	c := client(t)
	resp, err := c.Get(app.URL + "/signin/start")
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	q := resp.Request.URL.Query()
	q.Set("email", "system:kube-controller-manager")
	q.Set("password", "password")
	resp, err = c.PostForm(idpSrv.URL+"/oidc/auth", q)
	if err != nil {
		t.Fatal(err)
	}
	if b := readAll(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(b, "reserved") {
		t.Fatalf("a reserved name: %s %s", resp.Status, b)
	}
	resp, _ = c.Get(app.URL + "/api/v1/me")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a session was made: %s", resp.Status)
	}
	if !strings.Contains(auditOut.String(), `"action":"name.refused"`) {
		t.Errorf("audit: %s", auditOut.String())
	}
}

func TestOIDCRefusesBadTokens(t *testing.T) {
	var idp *oidcsim.Provider
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { idp.ServeHTTP(w, r) }))
	defer idpSrv.Close()
	idp, _ = oidcsim.New(oidcsim.Options{Issuer: idpSrv.URL})
	other, _ := oidcsim.New(oidcsim.Options{Issuer: idpSrv.URL})
	now := time.Now()
	p := auth.NewProvider(auth.OIDCConfig{Issuer: idpSrv.URL, ClientID: "221b"}, nil, func() time.Time { return now })
	good := map[string]any{"iss": idpSrv.URL, "sub": "u1", "aud": "221b", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "n1", "email": "u1@corp.example", "groups": []string{"a"}}
	tok, _ := idp.Token(good)
	id, err := p.Verify(context.Background(), tok, "n1")
	if err != nil || id.Username != "u1@corp.example" || len(id.Groups) != 1 {
		t.Fatalf("good token: %v %+v", err, id)
	}
	cases := map[string]func(c map[string]any) (string, error){
		"another key":    func(c map[string]any) (string, error) { return other.Token(c) },
		"wrong issuer":   func(c map[string]any) (string, error) { c["iss"] = "https://evil.example"; return idp.Token(c) },
		"wrong audience": func(c map[string]any) (string, error) { c["aud"] = "someone-else"; return idp.Token(c) },
		"expired":        func(c map[string]any) (string, error) { c["exp"] = now.Add(-time.Hour).Unix(); return idp.Token(c) },
		"wrong nonce":    func(c map[string]any) (string, error) { c["nonce"] = "n2"; return idp.Token(c) },
		"no subject":     func(c map[string]any) (string, error) { delete(c, "sub"); return idp.Token(c) },
		// Named by an email the provider has not verified.
		"unverified email": func(c map[string]any) (string, error) { c["email_verified"] = false; return idp.Token(c) },
		"alg none": func(c map[string]any) (string, error) {
			h, _ := json.Marshal(map[string]string{"alg": "none"})
			b, _ := json.Marshal(c)
			return b64(h) + "." + b64(b) + ".", nil
		},
		"tampered": func(c map[string]any) (string, error) {
			s, _ := idp.Token(c)
			parts := strings.Split(s, ".")
			c["groups"] = []string{"platform-admins"}
			b, _ := json.Marshal(c)
			return parts[0] + "." + b64(b) + "." + parts[2], nil
		},
	}
	for name, make := range cases {
		c := map[string]any{}
		for k, v := range good {
			c[k] = v
		}
		s, _ := make(c)
		if _, err := p.Verify(context.Background(), s, "n1"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Named by another claim, an unverified email is left out, so it cannot
	// match an administrator's; a verified one, as a string, is kept.
	byName := auth.NewProvider(auth.OIDCConfig{Issuer: idpSrv.URL, ClientID: "221b", UsernameClaim: "preferred_username"}, nil, func() time.Time { return now })
	for verified, want := range map[any]string{false: "", "false": "", true: "u1@corp.example", "true": "u1@corp.example"} {
		c := map[string]any{"preferred_username": "u1", "email_verified": verified}
		for k, v := range good {
			c[k] = v
		}
		s, _ := idp.Token(c)
		id, err := byName.Verify(context.Background(), s, "n1")
		if err != nil || id.Username != "u1" || id.Email != want {
			t.Errorf("email_verified %v: %v %+v", verified, err, id)
		}
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestLocalAccounts(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(hash, "correct horse battery staple") || auth.CheckPassword(hash, "wrong") || auth.CheckPassword("garbage", "x") {
		t.Fatal("password check")
	}
	if _, err := auth.HashPassword("short"); err == nil {
		t.Fatal("a short password was accepted")
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	var auditOut bytes.Buffer
	svc, err := auth.New(auth.Config{
		Mode:        auth.ModeLocal,
		Local:       []auth.LocalUser{{Email: "Ops@corp.example", Name: "Ops", Groups: []string{"ops"}, PasswordHash: hash}, {Email: "dev@corp.example", PasswordHash: hash}},
		Admins:      []string{"ops@corp.example"},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, nil, audit.New(audit.Options{Out: &auditOut, Now: clock}), clock)
	if err != nil {
		t.Fatal(err)
	}
	app := console(t, svc)
	defer app.Close()
	c := client(t)

	post := func(email, password string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, app.URL+"/signin", strings.NewReader(url.Values{"email": {email}, "password": {password}, "next": {"/#/nodes"}, "form": {formToken(t, c, app.URL)}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// What a browser sends for a form posted by the page: no Sec-Fetch-Site
		// over plain HTTP to a non-local address, and Origin: null under the
		// console's referrer policy.
		req.Header.Set("Origin", "null")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post("ops@corp.example", "wrong")
	if b := readAll(t, resp); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(b, "cannot sign in") {
		t.Fatalf("wrong password: %s %s", resp.Status, b)
	}
	resp = post("nobody@corp.example", "whatever")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown account: %s", resp.Status)
	}
	resp.Body.Close()
	resp = post("OPS@corp.example", "correct horse battery staple")
	readAll(t, resp)
	if resp.Request.URL.Path != "/" || resp.StatusCode != http.StatusOK {
		t.Fatalf("after sign-in: %s %s", resp.Status, resp.Request.URL)
	}
	var me auth.Me
	getJSON(t, c, app.URL+"/api/v1/me", &me)
	if me.User.Username != "ops@corp.example" || !me.Admin || me.User.Name != "Ops" || me.SessionIdle != 1800 {
		t.Fatalf("me: %+v", me)
	}
	if !strings.Contains(auditOut.String(), `"action":"signin.failed"`) || !strings.Contains(auditOut.String(), `"outcome":"failed"`) {
		t.Fatalf("audit: %s", auditOut.String())
	}

	// A cross-site form post is refused: by its headers when the browser
	// sends them, and without them by the missing token (a form from
	// another site has no cookie to pair with).
	req, _ := http.NewRequest(http.MethodPost, app.URL+"/signin", strings.NewReader(url.Values{"email": {"dev@corp.example"}, "password": {"correct horse battery staple"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, _ = client(t).Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site sign-in: %s", resp.Status)
	}
	resp.Body.Close()
	stranger := client(t)
	tok := formToken(t, stranger, app.URL)
	for _, form := range []url.Values{
		{"email": {"dev@corp.example"}, "password": {"correct horse battery staple"}},
		{"email": {"dev@corp.example"}, "password": {"correct horse battery staple"}, "form": {"not-the-token"}},
	} {
		req, _ := http.NewRequest(http.MethodPost, app.URL+"/signin", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "null")
		resp, _ = stranger.Do(req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("sign-in without the form token: %s", resp.Status)
		}
		resp.Body.Close()
	}
	// The token without its cookie is no better.
	req, _ = http.NewRequest(http.MethodPost, app.URL+"/signin", strings.NewReader(url.Values{"email": {"dev@corp.example"}, "password": {"correct horse battery staple"}, "form": {tok}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = client(t).Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sign-in with the token but no cookie: %s", resp.Status)
	}
	resp.Body.Close()

	// Idle expiry: after 31 minutes without a request the session is gone.
	now = now.Add(31 * time.Minute)
	resp, _ = c.Get(app.URL + "/api/v1/me")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("idle session: %s", resp.Status)
	}
	resp.Body.Close()

	// Too many attempts are slowed down.
	c2 := client(t)
	var last int
	for i := 0; i < 7; i++ {
		req, _ := http.NewRequest(http.MethodPost, app.URL+"/signin", strings.NewReader(url.Values{"email": {"dev@corp.example"}, "password": {"wrong"}, "form": {formToken(t, c2, app.URL)}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, _ := c2.Do(req)
		last = resp.StatusCode
		resp.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after many attempts: %d", last)
	}
}

func TestModeNoneIsAnAnonymousAdmin(t *testing.T) {
	svc, err := auth.New(auth.Config{Mode: auth.ModeNone}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := console(t, svc)
	defer app.Close()
	var me auth.Me
	getJSON(t, client(t), app.URL+"/api/v1/me", &me)
	if me.Mode != auth.ModeNone || !me.Admin || me.User.Username != "anonymous" {
		t.Fatalf("me: %+v", me)
	}
}

// The page to go on to after sign-in stays on this site, including the
// forms http.Redirect would clean into one a browser reads as another host.
func TestSignInNextStaysOnSite(t *testing.T) {
	svc, err := auth.New(auth.Config{Mode: auth.ModeNone}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := console(t, svc)
	defer app.Close()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for next, want := range map[string]string{
		"/#/nodes":               "/#/nodes",
		"/c/prod/#/pods?ns=a":    "/c/prod/#/pods?ns=a",
		"//evil.example":         "/",
		"/\\evil.example":        "/",
		"/./\\evil.example":      "/",
		"/a/../\\evil.example":   "/",
		"/\tevil.example":        "/",
		"https://evil.example/x": "/",
	} {
		resp, err := c.Get(app.URL + "/signin?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Location"); got != want {
			t.Errorf("next %q: went to %q, want %q", next, got, want)
		}
	}
}

func TestConfigIsChecked(t *testing.T) {
	if _, err := auth.New(auth.Config{Mode: auth.ModeOIDC}, nil, nil, nil); err == nil {
		t.Error("OIDC without an issuer")
	}
	if _, err := auth.New(auth.Config{Mode: auth.ModeLocal, Local: []auth.LocalUser{{Email: "x@y", PasswordHash: "plain"}}}, nil, nil, nil); err == nil {
		t.Error("a local user with a plain password")
	}
	if _, err := auth.New(auth.Config{Mode: "magic"}, nil, nil, nil); err == nil {
		t.Error("an unknown mode")
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.String()
}

func getJSON(t *testing.T, c *http.Client, url string, out any) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
