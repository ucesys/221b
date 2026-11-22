package credentials

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/s3/s3test"
)

var ctx = context.Background()

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newService(t *testing.T, hc *http.Client) (*Service, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	if hc == nil {
		hc = http.DefaultClient
	}
	return open(t, Options{HTTP: hc, Now: c.now}), c
}

func token(t *testing.T, s *Service, name, endpoint string, sa SendAs, tok string) Credential {
	t.Helper()
	c, err := s.Create(ctx, Credential{Name: name, Kind: KindToken, Endpoint: endpoint, SendAs: sa}, Secret{Token: tok}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// CR-01, CR-05: a secret is written once and never in what the service
// gives back; the list says which parts are set.
func TestASecretNeverLeavesTheService(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{Method: SendBasic, Username: "x-access-token"}, "ghp_very-secret-value")
	list, _ := s.List(ctx)
	got, _ := s.Get(ctx, c.ID)
	b, _ := json.Marshal([]any{list, got, c})
	if strings.Contains(string(b), "ghp_very-secret-value") || len(list) != 1 || strings.Join(list[0].Set, ",") != "token" {
		t.Fatalf("%s", b)
	}
	// Another kind needs its secret; a name is unique, any case.
	if _, err := s.Update(ctx, c.ID, Credential{Name: "GitHub", Kind: KindUserPass, Endpoint: "https://github.com", Username: "u"}, nil, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another kind kept the secret: %v", err)
	}
	if _, err := s.Create(ctx, Credential{Name: "github", Kind: KindToken, Endpoint: "github.com"}, Secret{Token: "x"}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second name: %v", err)
	}
	// Changed without a secret, it keeps it.
	if _, err := s.Update(ctx, c.ID, Credential{Name: "GitHub org", Kind: KindToken, Endpoint: "https://github.com/ucesys", Description: "read only", SendAs: SendAs{Method: SendBearer}}, nil, "bob"); err != nil {
		t.Fatal(err)
	}
	u, err := s.Resolve(ctx, c.ID, UseGit, "https://github.com/ucesys/221b")
	if err != nil || u.Token != "ghp_very-secret-value" || u.Method != SendBearer {
		t.Fatalf("kept: %+v %v", u, err)
	}
	if got, _ := s.Get(ctx, c.ID); got.ChangedBy != "bob" || got.CreatedBy != "ann" {
		t.Fatalf("changed by: %+v", got)
	}
}

// CR-04: a credential goes only to addresses under its endpoint.
func TestTheEndpointScopesEveryUse(t *testing.T) {
	for _, tc := range []struct {
		ep, target string
		ok         bool
	}{
		{"https://github.com/ucesys", "https://github.com/ucesys/221b.git", true},
		{"https://github.com/ucesys", "https://github.com/ucesys", true},
		{"https://github.com/ucesys", "https://github.com/ucesys-evil/x", false},
		{"https://github.com/ucesys", "https://github.com.evil.example/ucesys/x", false},
		{"https://github.com/ucesys", "http://github.com/ucesys/x", false},
		{"github.com/ucesys", "https://github.com/ucesys/x", true},
		{"github.com/ucesys", "http://github.com/ucesys/x", false},
		{"github.com/ucesys/221b", "https://github.com/ucesys/221b.git/info/refs?service=git-upload-pack", true},
		{"https://charts.example.com:8443", "https://charts.example.com/x", false},
		{"https://charts.example.com:443", "https://charts.example.com/x", true},
		{"oci://ghcr.io/org", "https://ghcr.io/org/charts/app", true},
		{"oci://ghcr.io/org", "oci://ghcr.io/other", false},
		{"git@github.com:ucesys", "ssh://git@github.com/ucesys/x.git", true},
		{"https://s3.amazonaws.com/docs", S3Address("", "docs"), true},
		{"https://s3.amazonaws.com/docs", S3Address("", "docs-other"), false},
	} {
		ep, err := ParseEndpoint(tc.ep)
		if err != nil {
			t.Fatalf("%s: %v", tc.ep, err)
		}
		if got := ep.Covers(tc.target); got != tc.ok {
			t.Errorf("%s covers %s: %v", tc.ep, tc.target, got)
		}
	}
	s, _ := newService(t, nil)
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{}, "tok")
	if _, err := s.Resolve(ctx, c.ID, UseHTTP, "https://example.com/x"); !errors.Is(err, ErrOutside) {
		t.Fatalf("another host: %v", err)
	}
	u, err := s.Resolve(ctx, c.ID, UseHTTP, "https://github.com/ucesys/x")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://evil.example/ucesys/x", nil)
	if err := u.Apply(req); !errors.Is(err, ErrOutside) || req.Header.Get("Authorization") != "" {
		t.Fatalf("applied elsewhere: %v %v", err, req.Header)
	}
	req, _ = http.NewRequest(http.MethodGet, "https://evil.example/x", nil)
	u.Signer()(req)
	if req.Header.Get("Authorization") != "" {
		t.Fatal("the signer signed another host")
	}
	// A use refuses a credential of another kind.
	if _, err := s.Resolve(ctx, c.ID, UseS3, "https://github.com/ucesys/x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a token for a bucket: %v", err)
	}
}

// CR-06: a credential in use is not removed, and the refusal says by whom.
func TestACredentialInUseIsNotRemoved(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{}, "tok")
	used := true
	s.Register(func(context.Context) ([]Ref, error) {
		if !used {
			return nil, nil
		}
		return []Ref{{Credential: c.ID, What: "app 221b/k8s-console"}}, nil
	})
	if _, err := s.Delete(ctx, c.ID); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "app 221b/k8s-console") {
		t.Fatalf("removed in use: %v", err)
	}
	used = false
	if _, err := s.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if v := s.SecretValues(ctx, c.ID); len(v) != 0 {
		t.Fatalf("the secret stayed: %d", len(v))
	}
}

// CR-03: each way of sending a token puts it where it says, and a test
// reports how it was sent, never the value, with a suggestion after 401.
func TestEachWayOfSending(t *testing.T) {
	var mu sync.Mutex
	var last *http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.Clone(context.Background())
		mu.Unlock()
		if r.URL.Path == "/basic-only" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s, _ := newService(t, srv.Client())
	for _, tc := range []struct {
		sa    SendAs
		check func(*http.Request) bool
		says  string
	}{
		{SendAs{Method: SendBearer}, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok-1" }, "with Bearer"},
		{SendAs{Method: SendBasic, Username: "x-access-token"}, func(r *http.Request) bool {
			u, p, ok := r.BasicAuth()
			return ok && u == "x-access-token" && p == "tok-1"
		}, "with Basic for x-access-token"},
		{SendAs{Method: SendHeader, Header: "PRIVATE-TOKEN"}, func(r *http.Request) bool { return r.Header.Get("PRIVATE-TOKEN") == "tok-1" }, "the header PRIVATE-TOKEN"},
		{SendAs{Method: SendHeader, Header: "Authorization", Prefix: "token "}, func(r *http.Request) bool { return r.Header.Get("Authorization") == "token tok-1" }, "the header Authorization"},
		{SendAs{Method: SendQuery, Param: "access_token"}, func(r *http.Request) bool { return r.URL.Query().Get("access_token") == "tok-1" }, "the query parameter access_token"},
		{SendAs{Method: SendCookie, Cookie: "session"}, func(r *http.Request) bool { c, err := r.Cookie("session"); return err == nil && c.Value == "tok-1" }, "the cookie session"},
	} {
		c := token(t, s, "t "+tc.sa.Method+tc.sa.Header+tc.sa.Prefix, srv.URL, tc.sa, "tok-1")
		res, err := s.RunTest(ctx, c.ID, srv.URL+"/x", "ann")
		mu.Lock()
		got := last
		mu.Unlock()
		if err != nil || !res.OK || !tc.check(got) || !strings.Contains(res.Text, tc.says) || strings.Contains(res.Text, "tok-1") {
			t.Errorf("%+v: %+v %v", tc.sa, res, err)
		}
		if saved, _ := s.Get(ctx, c.ID); saved.LastTest == nil || !saved.LastTest.OK || saved.LastTest.By != "ann" {
			t.Errorf("the last test: %+v", saved.LastTest)
		}
	}
	c := token(t, s, "bearer", srv.URL, SendAs{}, "tok-1")
	res, _ := s.RunTest(ctx, c.ID, "/basic-only", "ann")
	if res.OK || res.Status != http.StatusUnauthorized || res.Suggest != SendBasic || !strings.Contains(res.Text, "asks for Basic") {
		t.Fatalf("a 401: %+v", res)
	}
	res, _ = s.RunTest(ctx, c.ID, "https://elsewhere.example/x", "ann")
	if res.OK || !strings.Contains(res.Text, "never sent there") {
		t.Fatalf("outside: %+v", res)
	}
}

// CR-02: an OAuth2 client's bearer is fetched with the client
// credentials, kept until shortly before it expires, and fetched again.
func TestOAuth2ClientCredentials(t *testing.T) {
	var mu sync.Mutex
	fetched := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		if id == "" {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if r.PostForm.Get("grant_type") != "client_credentials" || id != "console" || secret != "client-secret-1" || r.PostForm.Get("scope") != "read:docs" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		fetched++
		n := fetched
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"bearer-%d","token_type":"Bearer","expires_in":3600}`, n)
	}))
	defer srv.Close()
	s, clk := newService(t, srv.Client())
	c, err := s.Create(ctx, Credential{Name: "docs", Kind: KindOAuth2, Endpoint: "https://docs.example", TokenURL: srv.URL + "/token", ClientID: "console", Scope: "read:docs"}, Secret{ClientSecret: "client-secret-1"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		u, err := s.Resolve(ctx, c.ID, UseHTTP, "https://docs.example/a")
		if err != nil || u.Token != "bearer-1" || u.Method != SendBearer {
			t.Fatalf("%+v %v", u, err)
		}
	}
	if fetched != 1 {
		t.Fatalf("fetched %d times", fetched)
	}
	_ = clk
	// The library compares with the wall clock; the service with its own:
	// past the expiry less the margin, a new one is fetched.
	s.mu.Lock()
	m := s.minted[c.ID]
	m.expires = clk.now().Add(time.Minute)
	s.minted[c.ID] = m
	s.mu.Unlock()
	u, err := s.Resolve(ctx, c.ID, UseHTTP, "https://docs.example/a")
	if err != nil || u.Token != "bearer-2" {
		t.Fatalf("refreshed: %+v %v", u, err)
	}
	// A wrong secret says so, without it.
	if _, err := s.Update(ctx, c.ID, Credential{Name: "docs", Kind: KindOAuth2, Endpoint: "https://docs.example", TokenURL: srv.URL + "/token", ClientID: "console", Scope: "read:docs"}, &Secret{ClientSecret: "wrong-secret-xyz"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, c.ID, UseHTTP, "https://docs.example/a"); err == nil || strings.Contains(err.Error(), "wrong-secret-xyz") {
		t.Fatalf("a wrong secret: %v", err)
	}
}

// CR-02: a GitHub App's installation token is minted with a JWT the App's
// key signs; the key never leaves the console.
func TestGitHubAppMintsAnInstallationToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	var mu sync.Mutex
	minted := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 || strings.Contains(r.Header.Get("Authorization"), "PRIVATE KEY") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var cl struct {
			Iss string `json:"iss"`
		}
		_ = json.Unmarshal(claims, &cl)
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil || cl.Iss != "1234" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		minted++
		mu.Unlock()
		fmt.Fprintf(w, `{"token":"ghs_installation","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer srv.Close()
	APIOverride = srv.URL
	t.Cleanup(func() { APIOverride = "" })
	s, _ := newService(t, srv.Client())
	if _, err := s.Create(ctx, Credential{Name: "bad", Kind: KindGitHubApp, Endpoint: "https://github.com/ucesys", AppID: "x", InstallationID: "42"}, Secret{PrivateKey: keyPEM}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an app id that is not a number: %v", err)
	}
	c, err := s.Create(ctx, Credential{Name: "app", Kind: KindGitHubApp, Endpoint: "https://github.com/ucesys", AppID: "1234", InstallationID: "42", SendAs: SendAs{Method: SendBasic, Username: "x-access-token"}}, Secret{PrivateKey: keyPEM}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		u, err := s.Resolve(ctx, c.ID, UseGit, "https://github.com/ucesys/221b")
		if err != nil {
			t.Fatal(err)
		}
		if user, pass, ok := u.Basic(); !ok || user != "x-access-token" || pass != "ghs_installation" {
			t.Fatalf("%+v", u)
		}
	}
	if minted != 1 {
		t.Fatalf("minted %d times", minted)
	}
	if GitHubAPI("https://github.com/x") != srv.URL {
		t.Fatal("override")
	}
	APIOverride = ""
	if GitHubAPI("https://github.com/x") != "https://api.github.com" || GitHubAPI("https://git.corp.example/x") != "https://git.corp.example/api/v3" {
		t.Fatal("the API's address")
	}
}

// CR-02, CR-07: an access key signs a bucket's listing (SigV4); a wrong
// one is refused.
func TestAnAccessKeySignsTheBucket(t *testing.T) {
	key := awsv4.Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "bucket-secret-key"}
	b := s3test.New("docs", "eu-west-1", key)
	defer b.Close()
	s, _ := newService(t, nil)
	c, err := s.Create(ctx, Credential{Name: "docs", Kind: KindAccessKey, Endpoint: b.URL, Region: "eu-west-1"}, Secret{AccessKeyID: key.AccessKeyID, SecretAccessKey: key.SecretAccessKey}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.RunTest(ctx, c.ID, "docs", "ann")
	if err != nil || !res.OK || !strings.Contains(res.Text, "SigV4") || b.Requests["LIST"] != 1 || strings.Contains(res.Text, "bucket-secret-key") {
		t.Fatalf("%+v %v %v", res, err, b.Requests)
	}
	if _, err := s.Update(ctx, c.ID, Credential{Name: "docs", Kind: KindAccessKey, Endpoint: b.URL, Region: "eu-west-1"}, &Secret{AccessKeyID: key.AccessKeyID, SecretAccessKey: "wrong-secret-key"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.RunTest(ctx, c.ID, "docs", "ann"); res.OK || strings.Contains(res.Text, "wrong-secret-key") {
		t.Fatalf("a wrong key: %+v", res)
	}
	u, err := s.Resolve(ctx, c.ID, UseS3, S3Address(b.URL, "docs"))
	if err != nil || u.Method != MethodSigV4 || u.AccessKeyID != key.AccessKeyID || u.Region != "eu-west-1" {
		t.Fatalf("%+v %v", u, err)
	}
}

// CR-02: an SSH key needs the host key; the console never trusts a host
// on first use.
func TestAnSSHKeyNeedsItsHostKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	keyPEM := string(pem.EncodeToMemory(block))
	hostPub, _, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewPublicKey(hostPub)
	host := string(ssh.MarshalAuthorizedKey(hk))
	s, _ := newService(t, nil)
	if _, err := s.Create(ctx, Credential{Name: "deploy", Kind: KindSSH, Endpoint: "ssh://git@git.example.com/org"}, Secret{PrivateKey: keyPEM}, "ann"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("no host key: %v", err)
	}
	if _, err := s.Create(ctx, Credential{Name: "deploy", Kind: KindSSH, Endpoint: "ssh://git@git.example.com/org", HostKey: "not a key"}, Secret{PrivateKey: keyPEM}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad host key: %v", err)
	}
	c, err := s.Create(ctx, Credential{Name: "deploy", Kind: KindSSH, Endpoint: "ssh://git@git.example.com/org", HostKey: "git.example.com " + host}, Secret{PrivateKey: keyPEM}, "ann")
	if err != nil || c.HostKey == "" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := s.Create(ctx, Credential{Name: "broken", Kind: KindSSH, Endpoint: "ssh://git.example.com", HostKey: host}, Secret{PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n-----END OPENSSH PRIVATE KEY-----"}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a key that does not read: %v", err)
	}
}

// fakeConsumer is a part of the console with secrets of its own.
type fakeConsumer struct {
	found    []Found
	switched map[string]string
}

func (f *fakeConsumer) Find(context.Context) ([]Found, []string, error) {
	var out []Found
	for _, x := range f.found {
		if _, done := f.switched[x.Key]; !done {
			out = append(out, x)
		}
	}
	return out, nil, nil
}

func (f *fakeConsumer) Switch(_ context.Context, key, id string) error {
	f.switched[key] = id
	return nil
}

// CR-12: one credential per distinct secret and endpoint, identical ones
// merged, consumers switched; a second run makes nothing.
func TestTheUpgradeMergesAndRunsOnce(t *testing.T) {
	s, _ := newService(t, nil)
	basic := SendAs{Method: SendBasic, Username: "x-access-token"}
	apps := &fakeConsumer{switched: map[string]string{}, found: []Found{
		{Key: "a", Label: "app 221b/k8s-console", Endpoint: "https://github.com/ucesys", Kind: KindToken, SendAs: basic, Secret: Secret{Token: "ghp_one"}},
		{Key: "b", Label: "app 221b-github/221b-github", Endpoint: "https://github.com/ucesys", Kind: KindToken, SendAs: basic, Secret: Secret{Token: "ghp_one"}},
		{Key: "c", Label: "app other/x", Endpoint: "https://github.com/ucesys", Kind: KindToken, SendAs: basic, Secret: Secret{Token: "ghp_two"}},
	}}
	repos := &fakeConsumer{switched: map[string]string{}, found: []Found{
		{Key: "charts", Label: "Helm repository charts", Endpoint: "https://charts.example.com", Kind: KindUserPass, Username: "reader", Secret: Secret{Password: "pw"}},
		{Key: "broken", Label: "Helm repository broken", Endpoint: "", Kind: KindToken, Secret: Secret{Token: "x"}},
	}}
	created, err := s.Migrate(ctx, []Consumer{apps, repos}, "the upgrade")
	if err != nil || len(created) != 3 {
		t.Fatalf("%+v %v", created, err)
	}
	if apps.switched["a"] == "" || apps.switched["a"] != apps.switched["b"] || apps.switched["c"] == apps.switched["a"] || repos.switched["charts"] == "" {
		t.Fatalf("switched: %+v %+v", apps.switched, repos.switched)
	}
	if _, ok := repos.switched["broken"]; ok {
		t.Fatal("a secret that cannot be a credential was switched")
	}
	merged, _ := s.Get(ctx, apps.switched["a"])
	if merged.Name != "github.com/ucesys (from app 221b-github/221b-github, app 221b/k8s-console)" || len(merged.From) != 2 {
		t.Fatalf("named: %+v", merged)
	}
	if u, err := s.Resolve(ctx, merged.ID, UseGit, "https://github.com/ucesys/221b"); err != nil || u.Token != "ghp_one" || u.Username != "x-access-token" {
		t.Fatalf("resolved: %+v %v", u, err)
	}
	m, _ := s.Migration(ctx)
	if len(m.Created) != 3 || len(m.Notes) != 1 || !strings.Contains(m.Notes[0], "Helm repository broken keeps its own secret") || m.Dismissed {
		t.Fatalf("migration: %+v", m)
	}
	again, err := s.Migrate(ctx, []Consumer{apps, repos}, "the upgrade")
	if err != nil || len(again) != 0 {
		t.Fatalf("again: %+v %v", again, err)
	}
	// A consumer switched while its credential was made last time (a run
	// cut short) takes the credential made then, not a second one.
	late := &fakeConsumer{switched: map[string]string{}, found: []Found{{Key: "d", Label: "app late/x", Endpoint: "https://github.com/ucesys", Kind: KindToken, SendAs: basic, Secret: Secret{Token: "ghp_one"}}}}
	if again, _ := s.Migrate(ctx, []Consumer{late}, "the upgrade"); len(again) != 0 || late.switched["d"] != merged.ID {
		t.Fatalf("late: %+v %+v", again, late.switched)
	}
	if list, _ := s.List(ctx); len(list) != 3 {
		t.Fatalf("list: %d", len(list))
	}
	if err := s.DismissMigration(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Migration(ctx); !m.Dismissed {
		t.Fatal("not dismissed")
	}
}

// CR-13: a backup holds each secret sealed, never in clear; a restore
// adds the missing credentials, and one whose secret does not open comes
// back without it, to be given again.
func TestTheCredentialsBackup(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{}, "ghp_secret")
	seal := func(v string) string { return "<sealed:k:" + base64.StdEncoding.EncodeToString([]byte(v)) + ">" }
	list, err := s.Export(ctx, seal)
	if err != nil || len(list) != 1 || strings.Contains(list[0].Secret, "ghp_secret") {
		t.Fatalf("%+v %v", list, err)
	}
	into, _ := newService(t, nil)
	open := func(in, _, _ string) (string, error) {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(in, "<sealed:k:"), ">"))
		return string(b), nil
	}
	check := func(v string) error {
		if v != "" && !strings.HasPrefix(v, "<sealed:") {
			return errors.New("in clear")
		}
		return nil
	}
	if n, err := into.Import(ctx, list, check, open, t.Logf); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if u, err := into.Resolve(ctx, c.ID, UseHTTP, "https://github.com/ucesys/x"); err != nil || u.Token != "ghp_secret" {
		t.Fatalf("restored: %+v %v", u, err)
	}
	clear := []BackupEntry{{Credential: Credential{ID: "cr-x", Name: "x", Kind: KindToken, Endpoint: "x.example"}, Secret: `{"token":"in-clear"}`}}
	if _, err := into.Import(ctx, clear, check, open, t.Logf); err == nil {
		t.Fatal("a secret in clear was taken")
	}
	// Without its secret, a credential is restored and refuses to be used.
	none, _ := newService(t, nil)
	if n, _ := none.Import(ctx, list, check, func(string, string, string) (string, error) { return "", nil }, t.Logf); n != 1 {
		t.Fatal("not restored")
	}
	if _, err := none.Resolve(ctx, c.ID, UseHTTP, "https://github.com/ucesys/x"); err == nil || !strings.Contains(err.Error(), "give its secret again") {
		t.Fatalf("used without its secret: %v", err)
	}
}
