// Package oidcsim is a small OpenID Connect provider for tests and the
// simulator: discovery, keys, a sign-in form with fixed accounts, the
// authorization code flow with PKCE, and ID tokens signed with RS256. It
// is not for production.
package oidcsim

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// User is one account the provider knows.
type User struct {
	Email    string
	Name     string
	Password string
	Groups   []string
	// Subject is the stable identifier; the email when empty.
	Subject string
}

// Client is one relying party.
type Client struct {
	ID     string
	Secret string
	// RedirectURIs the client may use; empty allows any, for tests.
	RedirectURIs []string
}

// Options configure the provider.
type Options struct {
	// Issuer is the URL the provider is reached at, discovery included:
	// http://127.0.0.1:7443/oidc.
	Issuer  string
	Users   []User
	Clients []Client
	Now     func() time.Time
	// TokenLife is how long ID tokens are valid.
	TokenLife time.Duration
}

// Provider is the handler. Mount it at the issuer's path.
type Provider struct {
	opts Options
	key  *rsa.PrivateKey
	kid  string
	page *template.Template

	mu     sync.Mutex
	codes  map[string]*grant
	tokens map[string]*grant
	// SignIns counts successful sign-ins, for tests.
	SignIns int
}

type grant struct {
	user      User
	client    string
	redirect  string
	nonce     string
	challenge string
	scope     string
	expires   time.Time
}

// DefaultUsers are the accounts the simulator ships with.
var DefaultUsers = []User{
	{Email: "admin@corp.example", Name: "Ada Admin", Password: "password", Groups: []string{"platform-admins", "oidc:platform-admins"}},
	{Email: "dev@corp.example", Name: "Dev Eloper", Password: "password", Groups: []string{"developers"}},
	{Email: "viewer@corp.example", Name: "Vera Viewer", Password: "password"},
}

// New makes a provider with a fresh signing key.
func New(o Options) (*Provider, error) {
	if o.Issuer == "" {
		return nil, errors.New("an issuer URL is needed")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.TokenLife <= 0 {
		o.TokenLife = time.Hour
	}
	if len(o.Users) == 0 {
		o.Users = DefaultUsers
	}
	if len(o.Clients) == 0 {
		o.Clients = []Client{{ID: "221b", Secret: "221b-secret"}}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	p := &Provider{opts: o, key: key, kid: "sim-" + fmt.Sprintf("%x", sha256.Sum256(key.PublicKey.N.Bytes()))[:12], codes: map[string]*grant{}, tokens: map[string]*grant{}}
	p.page = template.Must(template.New("login").Parse(loginPage))
	return p, nil
}

func (p *Provider) path(rest string) string {
	u, _ := url.Parse(p.opts.Issuer)
	return strings.TrimSuffix(u.Path, "/") + rest
}

// ServeHTTP serves the provider's endpoints, whatever prefix it is mounted at.
func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	base, _ := url.Parse(p.opts.Issuer)
	path = strings.TrimPrefix(path, strings.TrimSuffix(base.Path, "/"))
	switch {
	case path == "/.well-known/openid-configuration" && r.Method == http.MethodGet:
		p.discovery(w)
	case path == "/keys" && r.Method == http.MethodGet:
		p.keys(w)
	case path == "/auth" && r.Method == http.MethodGet:
		p.authForm(w, r, "")
	case path == "/auth" && r.Method == http.MethodPost:
		p.authSubmit(w, r)
	case path == "/token" && r.Method == http.MethodPost:
		p.token(w, r)
	case path == "/userinfo" && r.Method == http.MethodGet:
		p.userinfo(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Provider) discovery(w http.ResponseWriter) {
	iss := strings.TrimSuffix(p.opts.Issuer, "/")
	writeJSON(w, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/auth",
		"token_endpoint":                        iss + "/token",
		"jwks_uri":                              iss + "/keys",
		"userinfo_endpoint":                     iss + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"claims_supported":                      []string{"sub", "email", "email_verified", "name", "groups"},
	})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (p *Provider) keys(w http.ResponseWriter) {
	writeJSON(w, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.kid,
		"n": b64(p.key.PublicKey.N.Bytes()), "e": b64(big.NewInt(int64(p.key.PublicKey.E)).Bytes()),
	}}})
}

// authRequest is what the authorization endpoint receives.
type authRequest struct {
	ClientID, Redirect, State, Nonce, Scope, Challenge, Method, ResponseType string
}

func requestOf(q url.Values) authRequest {
	return authRequest{ClientID: q.Get("client_id"), Redirect: q.Get("redirect_uri"), State: q.Get("state"), Nonce: q.Get("nonce"), Scope: q.Get("scope"), Challenge: q.Get("code_challenge"), Method: q.Get("code_challenge_method"), ResponseType: q.Get("response_type")}
}

func (p *Provider) client(id string) (Client, bool) {
	for _, c := range p.opts.Clients {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

func (p *Provider) checkRequest(a authRequest) string {
	c, ok := p.client(a.ClientID)
	if !ok {
		return "unknown client_id"
	}
	if a.Redirect == "" {
		return "redirect_uri is required"
	}
	if len(c.RedirectURIs) > 0 {
		allowed := false
		for _, u := range c.RedirectURIs {
			if u == a.Redirect {
				allowed = true
			}
		}
		if !allowed {
			return "redirect_uri is not registered for this client"
		}
	}
	if a.ResponseType != "code" {
		return "only response_type=code is supported"
	}
	if !strings.Contains(" "+a.Scope+" ", " openid ") {
		return "the openid scope is required"
	}
	if a.Challenge != "" && a.Method != "S256" {
		return "only the S256 code challenge method is supported"
	}
	return ""
}

func (p *Provider) authForm(w http.ResponseWriter, r *http.Request, msg string) {
	a := requestOf(r.URL.Query())
	if r.Method == http.MethodPost {
		a = requestOf(r.PostForm)
	}
	if e := p.checkRequest(a); e != "" {
		http.Error(w, e, http.StatusBadRequest)
		return
	}
	var users []string
	for _, u := range p.opts.Users {
		users = append(users, u.Email)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = p.page.Execute(w, map[string]any{"Req": a, "Error": msg, "Users": users, "Action": p.path("/auth")})
}

func (p *Provider) authSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	a := requestOf(r.PostForm)
	if e := p.checkRequest(a); e != "" {
		http.Error(w, e, http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	var user *User
	for i := range p.opts.Users {
		if strings.EqualFold(p.opts.Users[i].Email, email) && p.opts.Users[i].Password == r.PostFormValue("password") {
			user = &p.opts.Users[i]
		}
	}
	if user == nil {
		p.authForm(w, r, "Wrong email or password.")
		return
	}
	code := b64(randomBytes(24))
	p.mu.Lock()
	p.codes[code] = &grant{user: *user, client: a.ClientID, redirect: a.Redirect, nonce: a.Nonce, challenge: a.Challenge, scope: a.Scope, expires: p.opts.Now().Add(5 * time.Minute)}
	p.SignIns++
	p.mu.Unlock()
	u, err := url.Parse(a.Redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("code", code)
	if a.State != "" {
		q.Set("state", a.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func tokenError(w http.ResponseWriter, code int, kind, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": kind, "error_description": desc})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	clientID, secret, ok := r.BasicAuth()
	if !ok {
		clientID, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	c, found := p.client(clientID)
	if !found || (c.Secret != "" && c.Secret != secret) {
		tokenError(w, http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
		return
	}
	if r.PostFormValue("grant_type") != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	p.mu.Lock()
	g, ok := p.codes[r.PostFormValue("code")]
	delete(p.codes, r.PostFormValue("code"))
	p.mu.Unlock()
	if !ok || g.client != clientID || p.opts.Now().After(g.expires) {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the code is unknown, used or expired")
		return
	}
	if g.redirect != r.PostFormValue("redirect_uri") {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
		return
	}
	if g.challenge != "" {
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if b64(sum[:]) != g.challenge {
			tokenError(w, http.StatusBadRequest, "invalid_grant", "the code verifier does not match")
			return
		}
	}
	now := p.opts.Now()
	access := b64(randomBytes(24))
	p.mu.Lock()
	p.tokens[access] = g
	p.mu.Unlock()
	idToken, err := p.sign(p.claims(g, now))
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(p.opts.TokenLife / time.Second), "id_token": idToken, "scope": g.scope})
}

func (p *Provider) claims(g *grant, now time.Time) map[string]any {
	sub := g.user.Subject
	if sub == "" {
		sub = g.user.Email
	}
	c := map[string]any{"iss": strings.TrimSuffix(p.opts.Issuer, "/"), "sub": sub, "aud": g.client, "exp": now.Add(p.opts.TokenLife).Unix(), "iat": now.Unix(), "email": g.user.Email, "email_verified": true, "name": g.user.Name}
	if g.nonce != "" {
		c["nonce"] = g.nonce
	}
	if len(g.user.Groups) > 0 {
		c["groups"] = g.user.Groups
	}
	return c
}

// sign makes an RS256 JWS of the claims.
func (p *Provider) sign(claims map[string]any) (string, error) {
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.kid})
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signed + "." + b64(sig), nil
}

// Token signs arbitrary claims, for tests that need a token with a flaw.
func (p *Provider) Token(claims map[string]any) (string, error) { return p.sign(claims) }

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	access := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	g, ok := p.tokens[access]
	p.mu.Unlock()
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unknown token", http.StatusUnauthorized)
		return
	}
	c := p.claims(g, p.opts.Now())
	delete(c, "aud")
	delete(c, "exp")
	delete(c, "iat")
	delete(c, "nonce")
	writeJSON(w, c)
}

const loginPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Sign in · simulated provider</title>
<style>body{font:14px/1.5 system-ui,sans-serif;background:#f4f5f7;color:#13161b;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
form{background:#fff;border:1px solid #e1e4e8;border-radius:12px;padding:28px;width:min(360px,92vw);display:flex;flex-direction:column;gap:12px}
h1{font-size:18px;margin:0}label{display:flex;flex-direction:column;gap:4px;font-size:12.5px;color:#3f4651}input{font:inherit;padding:8px 10px;border:1px solid #c9cfd6;border-radius:7px}
button{font:inherit;font-weight:600;background:#0a6e69;color:#fff;border:0;border-radius:8px;height:38px;cursor:pointer}p{margin:0;font-size:12px;color:#5b636e}.err{color:#b52c22}</style></head>
<body><form method="post" action="{{.Action}}">
<h1>Simulated identity provider</h1>
<p>Not a real provider: any account below signs in with the password <code>password</code>.</p>
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
<input type="hidden" name="client_id" value="{{.Req.ClientID}}"><input type="hidden" name="redirect_uri" value="{{.Req.Redirect}}"><input type="hidden" name="state" value="{{.Req.State}}"><input type="hidden" name="nonce" value="{{.Req.Nonce}}"><input type="hidden" name="scope" value="{{.Req.Scope}}"><input type="hidden" name="code_challenge" value="{{.Req.Challenge}}"><input type="hidden" name="code_challenge_method" value="{{.Req.Method}}"><input type="hidden" name="response_type" value="code">
<label>Email<input name="email" type="email" list="users" autocomplete="username" required autofocus></label>
<datalist id="users">{{range .Users}}<option value="{{.}}">{{end}}</datalist>
<label>Password<input name="password" type="password" autocomplete="current-password" required></label>
<button type="submit">Sign in</button>
</form></body></html>`
