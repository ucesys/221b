// Package auth signs people in (P3-01): with an OIDC provider, or with
// local accounts for evaluations; keeps their sessions in cookies; tells
// admins from everyone else (P3-03); and writes the audit records of
// sign-ins and sign-outs (P3-10).
package auth

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"golang.org/x/oauth2"
)

// Mode is how people sign in.
type Mode string

const (
	// ModeNone turns sign-in off: everyone is an anonymous admin. For
	// development and the simulator only.
	ModeNone Mode = "none"
	// ModeOIDC sends people to an OpenID Connect provider.
	ModeOIDC Mode = "oidc"
	// ModeLocal signs people in with the console's own accounts.
	ModeLocal Mode = "local"
)

// Config is everything sign-in needs.
type Config struct {
	Mode  Mode
	OIDC  OIDCConfig
	Local []LocalUser
	// Admins are usernames (or subjects) that manage clusters and settings;
	// AdminGroups are groups that do.
	Admins      []string
	AdminGroups []string
	// SessionIdle ends a session after that long without a request;
	// SessionMax ends it that long after sign-in whatever happens.
	SessionIdle time.Duration
	SessionMax  time.Duration
	// Title is the console's name on the sign-in page.
	Title string
}

// Cookie names.
const (
	cookieSession = "221b_session"
	cookieSignIn  = "221b_signin"
	// cookieForm pairs the local sign-in form with the browser it was
	// served to: the form carries the same token in a hidden field, and a
	// POST without the pair did not come from this page (X-06).
	cookieForm = "221b_form"
)

// Service signs people in and out and checks sessions.
type Service struct {
	cfg      Config
	sessions *Sessions
	provider *Provider
	local    map[string]LocalUser
	// perAccount slows guessing at one account; perAddress slows spraying
	// many accounts from one address, which is also many people behind one
	// NAT, so it is the looser of the two.
	perAccount *limiter
	perAddress *limiter
	audit      *audit.Log
	log        *slog.Logger
	now        func() time.Time
	page       *template.Template
}

//go:embed signin.html signin.css signin.js
var pageFS embed.FS

// New builds the service. OIDC discovery is lazy, so a provider that is
// down at start does not stop the console.
func New(cfg Config, log *slog.Logger, auditLog *audit.Log, now func() time.Time) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	if cfg.SessionIdle <= 0 {
		cfg.SessionIdle = 8 * time.Hour
	}
	if cfg.SessionMax <= 0 {
		cfg.SessionMax = 7 * 24 * time.Hour
	}
	if cfg.Title == "" {
		cfg.Title = "221B"
	}
	s := &Service{cfg: cfg, sessions: NewSessions(cfg.SessionIdle, cfg.SessionMax, now), audit: auditLog, log: log, now: now, perAccount: newLimiter(5, time.Minute, now), perAddress: newLimiter(30, time.Minute, now)}
	switch cfg.Mode {
	case ModeNone:
	case ModeOIDC:
		if cfg.OIDC.Issuer == "" || cfg.OIDC.ClientID == "" {
			return nil, errors.New("OIDC sign-in needs the provider's issuer URL and a client ID")
		}
		if !strings.HasPrefix(cfg.OIDC.Issuer, "https://") && !strings.HasPrefix(cfg.OIDC.Issuer, "http://") {
			return nil, fmt.Errorf("the OIDC issuer %q is not a URL", cfg.OIDC.Issuer)
		}
		s.provider = NewProvider(cfg.OIDC, nil, now)
	case ModeLocal:
		if err := ValidateLocalUsers(cfg.Local); err != nil {
			return nil, err
		}
		s.local = map[string]LocalUser{}
		for _, u := range cfg.Local {
			s.local[strings.ToLower(strings.TrimSpace(u.Email))] = u
		}
	default:
		return nil, fmt.Errorf("unknown sign-in mode %q: use none, oidc or local", cfg.Mode)
	}
	s.page = template.Must(template.ParseFS(pageFS, "signin.html"))
	return s, nil
}

// Mode is how people sign in.
func (s *Service) Mode() Mode { return s.cfg.Mode }

// Sessions exposes the session store, for metrics and tests.
func (s *Service) Sessions() *Sessions { return s.sessions }

// Sweep drops expired sessions; call it now and then.
func (s *Service) Sweep() { s.sessions.Sweep() }

// isAdmin says whether a person manages the console.
func (s *Service) isAdmin(u User) bool {
	for _, a := range s.cfg.Admins {
		if a != "" && (strings.EqualFold(a, u.Username) || a == u.Subject || (u.Email != "" && strings.EqualFold(a, u.Email))) {
			return true
		}
	}
	for _, g := range s.cfg.AdminGroups {
		if g != "" && contains(u.Groups, g) {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// SessionFrom reads the session the middleware attached to a request.
func SessionFrom(ctx context.Context) (Session, bool) {
	sess, ok := ctx.Value(ctxKey{}).(Session)
	return sess, ok
}

// WithSession attaches a session to a context: for the console's own
// requests made on a person's behalf, such as Sherlock's reads.
func WithSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, sess)
}

// anonymous is the session every request gets when sign-in is off.
var anonymous = Session{ID: "", User: User{Subject: "anonymous", Username: "anonymous", Name: "Anonymous"}, Admin: true, Method: "none"}

// Open paths need no session: the health probes and metrics, the sign-in
// pages with their assets, and the browser app's static files, which are
// the same for everyone.
func openPath(p string) bool {
	switch {
	case p == "/healthz", p == "/readyz", p == "/metrics":
		return true
	case p == "/signin", strings.HasPrefix(p, "/signin/"), p == "/signin.css", p == "/signin.js", p == "/signout":
		return true
	case strings.HasPrefix(p, "/assets/"), p == "/favicon.svg", p == "/favicon.ico", p == "/manifest.webmanifest":
		return true
	}
	return false
}

// Gate checks the session. It returns the request with the session in its
// context, or nil after answering: a 401 for the API, the sign-in page for
// a page.
func (s *Service) Gate(w http.ResponseWriter, r *http.Request) *http.Request {
	if s.cfg.Mode == ModeNone {
		return r.WithContext(context.WithValue(r.Context(), ctxKey{}, anonymous))
	}
	if sess, ok := s.sessionOf(r); ok {
		return r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess))
	}
	if openPath(r.URL.Path) {
		return r
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "sign in to use the console", "signin": "/signin"})
		return nil
	}
	s.renderSignIn(w, r, "", http.StatusOK)
	return nil
}

func (s *Service) sessionOf(r *http.Request) (Session, bool) {
	c, err := r.Cookie(cookieSession)
	if err != nil || c.Value == "" {
		return Session{}, false
	}
	return s.sessions.Get(c.Value)
}

// secure says whether the request came over TLS, directly or through a
// proxy that says so; the cookies are then marked Secure.
func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Service) setSession(w http.ResponseWriter, r *http.Request, sess *Session) {
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: sess.ID, Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode, MaxAge: int(s.cfg.SessionMax / time.Second)})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// safeNext keeps a redirect on this site: a path, with its query and hash.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

// Route is one path the service serves.
type Route struct {
	Pattern string
	Name    string
	Handler http.HandlerFunc
}

// Routes lists the sign-in endpoints for the API mux.
func (s *Service) Routes() []Route {
	return []Route{
		{"GET /signin", "signin", s.getSignIn},
		{"POST /signin", "signin-local", s.postSignIn},
		{"GET /signin/start", "signin-start", s.startOIDC},
		{"POST /signin/start", "signin-start", s.startOIDC},
		{"GET /signin/callback", "signin-callback", s.callback},
		{"POST /signout", "signout", s.signOut},
		{"GET /signin.css", "signin-asset", s.asset("signin.css", "text/css; charset=utf-8")},
		{"GET /signin.js", "signin-asset", s.asset("signin.js", "text/javascript; charset=utf-8")},
		{"GET /api/v1/me", "me", s.me},
	}
}

func (s *Service) asset(name, ctype string) http.HandlerFunc {
	b, err := pageFS.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(b)
	}
}

// pageData fills the sign-in page.
type pageData struct {
	Title    string
	Mode     Mode
	Provider string
	Error    string
	Next     string
	Email    string
	Local    bool
	OIDC     bool
	Off      bool
	Form     string
}

func (s *Service) renderSignIn(w http.ResponseWriter, r *http.Request, msg string, code int) {
	d := pageData{Title: s.cfg.Title, Mode: s.cfg.Mode, Error: msg, Next: safeNext(r.URL.Query().Get("next")), Local: s.cfg.Mode == ModeLocal, OIDC: s.cfg.Mode == ModeOIDC, Off: s.cfg.Mode == ModeNone}
	if s.provider != nil {
		d.Provider = s.provider.Name()
	}
	if r.Method == http.MethodPost {
		d.Email = r.PostFormValue("email")
		d.Next = safeNext(r.PostFormValue("next"))
	}
	if d.Local {
		// A fresh token for the form, in the page and in a cookie only this
		// site's pages send (SameSite=Strict): a form posted from elsewhere
		// cannot carry both.
		d.Form = randomID()
		http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: d.Form, Path: "/signin", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := s.page.Execute(w, d); err != nil {
		s.log.Warn("rendering the sign-in page", "err", err)
	}
}

// getSignIn shows the sign-in page, or goes on when already signed in.
func (s *Service) getSignIn(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Mode == ModeNone {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	if _, ok := s.sessionOf(r); ok {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	s.renderSignIn(w, r, "", http.StatusOK)
}

// from is the address a request came from, for the audit log.
func from(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func (s *Service) record(r *http.Request, action string, who User, outcome, detail string) {
	if s.audit == nil {
		return
	}
	s.audit.Record(audit.Event{Action: action, Who: audit.Who{Subject: who.Subject, Email: who.Email, Name: who.Name}, Outcome: outcome, Detail: detail, From: from(r)})
}

// postSignIn signs a local account in.
func (s *Service) postSignIn(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Mode != ModeLocal {
		http.Error(w, "local sign-in is off", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderSignIn(w, r, "The form could not be read.", http.StatusBadRequest)
		return
	}
	// The headers, where the browser sends them, and the form's token
	// (a form post carries Origin: null under the console's referrer
	// policy, and plain HTTP to a non-local address no Sec-Fetch-Site at
	// all, so the token is what every browser can be held to).
	if !sameSiteHeaders(r, true) || !s.formToken(r) {
		s.renderSignIn(w, r, "That sign-in did not come from this console. Try again from this page.", http.StatusForbidden)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	password := r.PostFormValue("password")
	if !s.perAddress.allow(from(r)) || !s.perAccount.allow(email) {
		s.record(r, audit.ActionSignInFailed, User{Username: email, Email: email}, audit.OutcomeDenied, "too many attempts")
		s.renderSignIn(w, r, "Too many attempts. Wait a minute and try again.", http.StatusTooManyRequests)
		return
	}
	u, ok := s.local[email]
	// The check runs on a dummy hash when the account is unknown, so the
	// answer takes as long either way.
	hash := u.PasswordHash
	if !ok {
		hash = dummyHash
	}
	if !CheckPassword(hash, password) || !ok {
		s.record(r, audit.ActionSignInFailed, User{Username: email, Email: email}, audit.OutcomeFailed, "wrong email or password")
		s.renderSignIn(w, r, "That email and password do not match an account.", http.StatusUnauthorized)
		return
	}
	user := User{Subject: "local:" + email, Username: email, Email: email, Name: u.Name, Groups: append([]string(nil), u.Groups...)}
	sess := s.sessions.Create(user, s.isAdmin(user), string(ModeLocal))
	s.setSession(w, r, sess)
	http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: "", Path: "/signin", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	s.record(r, audit.ActionSignIn, user, audit.OutcomeOK, "local account")
	s.log.Info("signed in", "user", user.Username, "method", "local", "admin", sess.Admin)
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

// dummyHash is checked against when the account does not exist.
var dummyHash = func() string {
	h, err := HashPassword("not-a-real-password")
	if err != nil {
		panic(err)
	}
	return h
}()

// signInState is what the browser carries through the OIDC round trip.
type signInState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	At       int64  `json:"t"`
}

// redirectURL is where the provider sends people back.
func (s *Service) redirectURL(r *http.Request) string {
	if s.cfg.OIDC.RedirectURL != "" {
		return s.cfg.OIDC.RedirectURL
	}
	scheme := "http"
	if secure(r) {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host + "/signin/callback"
}

// startOIDC sends the person to the provider.
func (s *Service) startOIDC(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		http.Error(w, "OIDC sign-in is off", http.StatusNotFound)
		return
	}
	next := r.URL.Query().Get("next")
	if r.Method == http.MethodPost {
		next = r.PostFormValue("next")
	}
	st := signInState{State: randomID(), Nonce: randomID(), Verifier: oauth2.GenerateVerifier(), Next: safeNext(next), At: s.now().Unix()}
	authURL, err := s.provider.AuthURL(r.Context(), s.redirectURL(r), st.State, st.Nonce, st.Verifier)
	if err != nil {
		s.log.Warn("sign-in unavailable", "err", err)
		s.renderSignIn(w, r, "The sign-in provider cannot be reached: "+err.Error(), http.StatusBadGateway)
		return
	}
	b, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{Name: cookieSignIn, Value: base64.RawURLEncoding.EncodeToString(b), Path: "/signin", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, authURL, http.StatusFound)
}

// callback finishes the OIDC sign-in.
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		http.Error(w, "OIDC sign-in is off", http.StatusNotFound)
		return
	}
	c, err := r.Cookie(cookieSignIn)
	var st signInState
	if err == nil {
		if raw, derr := base64.RawURLEncoding.DecodeString(c.Value); derr == nil {
			err = json.Unmarshal(raw, &st)
		} else {
			err = derr
		}
	}
	if err != nil || st.State == "" {
		s.renderSignIn(w, r, "This sign-in has expired or did not start here. Try again.", http.StatusBadRequest)
		return
	}
	clearCookie(w, r, cookieSignIn)
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		s.renderSignIn(w, r, "This sign-in does not match the one that started. Try again.", http.StatusBadRequest)
		return
	}
	if s.now().Unix()-st.At > 600 {
		s.renderSignIn(w, r, "This sign-in took too long. Try again.", http.StatusBadRequest)
		return
	}
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg += ": " + d
		}
		s.record(r, audit.ActionSignInFailed, User{}, audit.OutcomeFailed, "the provider refused: "+msg)
		s.renderSignIn(w, r, "The provider did not sign you in ("+msg+").", http.StatusUnauthorized)
		return
	}
	id, err := s.provider.Exchange(r.Context(), s.redirectURL(r), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		s.log.Warn("sign-in failed", "err", err)
		s.record(r, audit.ActionSignInFailed, User{}, audit.OutcomeFailed, err.Error())
		s.renderSignIn(w, r, "Sign-in failed: "+err.Error(), http.StatusUnauthorized)
		return
	}
	user := User{Subject: id.Subject, Username: id.Username, Email: id.Email, Name: id.Name, Groups: id.Groups}
	sess := s.sessions.Create(user, s.isAdmin(user), string(ModeOIDC))
	s.setSession(w, r, sess)
	s.record(r, audit.ActionSignIn, user, audit.OutcomeOK, "OIDC, "+s.provider.Name())
	s.log.Info("signed in", "user", user.Username, "method", "oidc", "admin", sess.Admin, "groups", len(user.Groups))
	http.Redirect(w, r, safeNext(st.Next), http.StatusSeeOther)
}

// signOut ends the session.
func (s *Service) signOut(w http.ResponseWriter, r *http.Request) {
	if !SameSite(r) {
		http.Error(w, "sign out from the console itself", http.StatusForbidden)
		return
	}
	if sess, ok := s.sessionOf(r); ok {
		s.sessions.Delete(sess.ID)
		s.record(r, audit.ActionSignOut, sess.User, audit.OutcomeOK, "")
	}
	clearCookie(w, r, cookieSession)
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"signedOut":true}` + "\n"))
		return
	}
	http.Redirect(w, r, "/signin", http.StatusSeeOther)
}

// Me is what /api/v1/me tells the browser.
type Me struct {
	Mode     Mode   `json:"mode"`
	User     User   `json:"user"`
	Admin    bool   `json:"admin"`
	Method   string `json:"method"`
	Provider string `json:"provider,omitempty"`
	// SessionIdle is how long the session lives without a request, in
	// seconds.
	SessionIdle int `json:"sessionIdleSeconds"`
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"sign in to use the console","signin":"/signin"}` + "\n"))
		return
	}
	me := Me{Mode: s.cfg.Mode, User: sess.User, Admin: sess.Admin, Method: sess.Method, SessionIdle: int(s.cfg.SessionIdle / time.Second)}
	if s.provider != nil {
		me.Provider = s.provider.Name()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(me)
}

// SameSite says whether a request that changes something came from the
// console's own pages (X-06): browsers mark cross-site requests with
// Sec-Fetch-Site, and older ones send Origin, which must match the host.
// A request with neither header is not from a browser page; the cookie
// alone gets it in, as it did before Phase 3.
func SameSite(r *http.Request) bool {
	return sameSiteHeaders(r, false)
}

// sameSiteHeaders reads Sec-Fetch-Site and Origin. nullOrigin says whether
// "Origin: null" passes: a form posted by the console's own pages carries
// it, because the Referrer-Policy is no-referrer, so the sign-in form
// relies on its token instead; a fetch from the app carries the real
// origin, so the API does not take null.
func sameSiteHeaders(r *http.Request, nullOrigin bool) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if origin == "null" {
		return nullOrigin
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// formToken says whether a posted sign-in form carries the token of the
// page it was served with, in the field and in the cookie alike.
func (s *Service) formToken(r *http.Request) bool {
	c, err := r.Cookie(cookieForm)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostFormValue("form"))) == 1
}
