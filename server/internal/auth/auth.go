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
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/roles"
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
	// ModeLDAP signs people in with a directory (RB-19 to RB-25).
	ModeLDAP Mode = "ldap"
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

	// Lists name the groups of each role, for LDAP and OIDC (RB-20).
	Lists RoleLists
	// LocalGroups are the groups of local users, defined in values (RB-50).
	LocalGroups []LocalGroup
	// Prefixes go before the names Kubernetes sees (RB-15).
	Prefixes roles.Prefixes
	// SetupToken, when set, must be typed on the first-run page (RB-27).
	SetupToken string
	// Accounts keeps the users created in the console; nil keeps them in
	// memory.
	Accounts Backend
	// SessionStore keeps the sessions where every replica sees them
	// (P7-02); nil keeps them in this process's memory.
	SessionStore SharedStore
	// Directory is the LDAP server, for ModeLDAP.
	Directory Directory
	// SyncInterval is how often the directory's groups are read again.
	SyncInterval time.Duration
	// AccessTTL is how long a person's groups stand before the directory is
	// asked again (RB-23).
	AccessTTL time.Duration
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
	accounts *Accounts
	dir      Directory
	people   *people
	// rereads serializes reading one person's groups again. Each person
	// has their own lock, so a directory slow to answer holds up only the
	// person it is answering about, not everyone signed in from it (RB-22).
	rereadMu sync.Mutex
	rereads  map[string]*rereadLock
	// setup slows guessing at the first-run page.
	setup *limiter
	// perAccount slows guessing at one account; perAddress slows spraying
	// many accounts from one address, which is also many people behind one
	// NAT, so it is the looser of the two.
	perAccount *limiter
	perAddress *limiter
	audit      *audit.Log
	log        *slog.Logger
	now        func() time.Time
	page       *template.Template
	setupPage  *template.Template
	// passwordPage changes one's own password (6.4).
	passwordPage *template.Template
}

//go:embed signin.html setup.html password.html signin.css signin.js
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
	if cfg.Prefixes.Local == "" {
		cfg.Prefixes.Local = roles.DefaultPrefixes().Local
	}
	if cfg.Prefixes.LDAP == "" {
		cfg.Prefixes.LDAP = roles.DefaultPrefixes().LDAP
	}
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = 2 * time.Minute
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = time.Hour
	}
	s := &Service{cfg: cfg, setup: newLimiter(5, time.Minute, now), sessions: NewSessions(cfg.SessionIdle, cfg.SessionMax, now), audit: auditLog, log: log, now: now, perAccount: newLimiter(5, time.Minute, now), perAddress: newLimiter(30, time.Minute, now)}
	if cfg.SessionStore != nil {
		s.sessions.Share(cfg.SessionStore, log.With("store", "sessions"))
	}
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
	case ModeLocal, ModeLDAP:
		if err := ValidateLocalUsers(cfg.Local); err != nil {
			return nil, err
		}
		if cfg.Mode == ModeLDAP {
			if cfg.Directory == nil {
				return nil, errors.New("LDAP sign-in needs a directory; set auth.ldap")
			}
			if cfg.Lists.Empty() {
				return nil, errors.New("LDAP sign-in needs at least one group under auth.ldap.roles: only members of a listed group may sign in")
			}
			s.dir = cfg.Directory
			s.people = newPeople(cfg.Directory.URL(), now)
		}
		for _, g := range cfg.LocalGroups {
			if !roles.ValidGroupName(g.Name) || (g.Role != roles.Viewer && g.Role != roles.Admin) {
				return nil, fmt.Errorf("local group %q: a name of lowercase letters, digits, dot, dash and underscore, and the role viewer or admin", g.Name)
			}
		}
		var managed []Account
		for _, u := range cfg.Local {
			managed = append(managed, u.Account())
		}
		backend := cfg.Accounts
		if backend == nil {
			backend = &MemoryBackend{}
		}
		external := len(cfg.Admins) > 0 || len(cfg.AdminGroups) > 0 || len(cfg.Lists[roles.SuperAdmin]) > 0
		s.accounts = NewAccounts(backend, managed, cfg.LocalGroups, external, now)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := s.accounts.Load(ctx); err != nil {
			s.log.Error("reading the saved accounts failed; the first-run page stays closed and accounts cannot change until they can be read", "err", err)
		}
		cancel()
	default:
		return nil, fmt.Errorf("unknown sign-in mode %q: use none, oidc or local", cfg.Mode)
	}
	s.page = template.Must(template.ParseFS(pageFS, "signin.html"))
	s.setupPage = template.Must(template.ParseFS(pageFS, "setup.html"))
	s.passwordPage = template.Must(template.ParseFS(pageFS, "password.html"))
	return s, nil
}

// Mode is how people sign in.
func (s *Service) Mode() Mode { return s.cfg.Mode }

// Sessions exposes the session store, for metrics and tests.
func (s *Service) Sessions() *Sessions { return s.sessions }

// Sweep drops expired sessions and tries again to read the saved accounts if
// they could not be read at start; call it now and then.
func (s *Service) Sweep() {
	s.sessions.Sweep()
	if s.accounts != nil && !s.accounts.Loaded() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.accounts.Load(ctx); err != nil {
			s.log.Warn("the saved accounts still cannot be read", "err", err)
		}
	}
}

// RunAccounts reads the saved accounts again every interval until ctx is
// done, so a change made on another replica counts here within that time
// (P7-02). It does nothing without local accounts.
func (s *Service) RunAccounts(ctx context.Context, every time.Duration) {
	if s.accounts == nil || !s.accounts.Persistent() {
		return
	}
	s.accounts.Run(ctx, every, func(err error) {
		s.log.Warn("reading the saved accounts again failed; the last ones read stand", "err", err)
	})
}

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
var anonymous = Session{ID: "", User: User{Subject: "anonymous", Username: "anonymous", Name: "Anonymous"}, Admin: true, Roles: []roles.ID{roles.SuperAdmin}, Super: []roles.ID{roles.SuperAdmin}, Method: "none"}

// Open paths need no session: the health probes and metrics, the sign-in
// pages with their assets, and the browser app's static files, which are
// the same for everyone.
func openPath(p string) bool {
	switch {
	case p == "/healthz", p == "/readyz", p == "/metrics":
		return true
	case p == "/signin", strings.HasPrefix(p, "/signin/"), p == "/signin.css", p == "/signin.js", p == "/signout", p == "/setup":
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
	if s.firstRun() {
		s.renderSetup(w, r, "", http.StatusOK)
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
	sess, ok := s.sessions.Get(c.Value)
	if !ok {
		return Session{}, false
	}
	return s.live(sess)
}

// secure says whether the request came over TLS, directly or through a
// proxy that says so; the cookies are then marked Secure.
func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Service) setSession(w http.ResponseWriter, r *http.Request, sess *Session) {
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: sess.ID, Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode, MaxAge: int(s.cfg.SessionMax / time.Second)})
}

// clearCookie removes a cookie; the path must be the one it was set with.
func clearCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// safeNext keeps a redirect on this site: a path, with its query and hash.
// A backslash or control character anywhere is refused: http.Redirect
// cleans the path, so /./\evil.example would become /\evil.example, which
// browsers read as //evil.example.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsFunc(next, func(c rune) bool { return c == '\\' || c < 0x20 || c == 0x7f }) {
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
	routes := []Route{
		{"GET /signin", "signin", s.getSignIn},
		{"POST /signin", "signin-local", s.postSignIn},
		{"GET /signin/password", "signin-password", s.getPassword},
		{"POST /signin/password", "signin-password", s.postPassword},
		{"GET /signin/start", "signin-start", s.startOIDC},
		{"POST /signin/start", "signin-start", s.startOIDC},
		{"GET /signin/callback", "signin-callback", s.callback},
		{"POST /signout", "signout", s.signOut},
		{"GET /signin.css", "signin-asset", s.asset("signin.css", "text/css; charset=utf-8")},
		{"GET /signin.js", "signin-asset", s.asset("signin.js", "text/javascript; charset=utf-8")},
		{"GET /api/v1/me", "me", s.me},
		{"GET /setup", "setup", s.getSetup},
		{"POST /setup", "setup-create", s.postSetup},
	}
	return append(routes, s.adminRoutes()...)
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
	// Notice says what went right: a password changed (6.4).
	Notice string
	Next   string
	Email  string
	Local  bool
	OIDC   bool
	Off    bool
	Form   string
}

func (s *Service) renderSignIn(w http.ResponseWriter, r *http.Request, msg string, code int) {
	d := pageData{Title: s.cfg.Title, Mode: s.cfg.Mode, Error: msg, Next: safeNext(r.URL.Query().Get("next")), Local: s.cfg.Mode == ModeLocal || s.cfg.Mode == ModeLDAP, OIDC: s.cfg.Mode == ModeOIDC, Off: s.cfg.Mode == ModeNone}
	if s.provider != nil {
		d.Provider = s.provider.Name()
	}
	if r.Method == http.MethodPost {
		d.Email = r.PostFormValue("email")
		d.Next = safeNext(r.PostFormValue("next"))
	} else if r.URL.Query().Get("changed") == "1" && msg == "" {
		d.Notice = "Your password is changed. Sign in with the new one."
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
	if s.firstRun() {
		http.Redirect(w, r, "/setup", http.StatusFound)
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

// refusedText is what a person is told when they cannot sign in, whatever
// the reason: a wrong password, a disabled account, a directory person in no
// listed group (RB-21). The audit record has the reason.
const refusedText = "You cannot sign in. Check your username and password. If they are right, you may not have access to this console; ask an administrator."

// postSignIn signs a local account or a directory person in.
func (s *Service) postSignIn(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Mode != ModeLocal && s.cfg.Mode != ModeLDAP {
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
	login := Login(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	if !s.perAddress.allow(from(r)) || !s.perAccount.allow(login) {
		s.record(r, audit.ActionSignInFailed, User{Username: login, Email: login}, audit.OutcomeDenied, "too many attempts")
		s.renderSignIn(w, r, "Too many attempts. Wait a minute and try again.", http.StatusTooManyRequests)
		return
	}
	refuse := func(reason string) {
		s.record(r, audit.ActionSignInFailed, User{Username: login, Email: login}, audit.OutcomeFailed, reason)
		s.renderSignIn(w, r, refusedText, http.StatusUnauthorized)
	}
	acct, local := s.accounts.Find(login)
	if !local && s.dir != nil {
		s.signInDirectory(w, r, login, password, refuse)
		return
	}
	// The check runs on a dummy hash when the account is unknown, so the
	// answer takes as long either way.
	hash := acct.PasswordHash
	if !local {
		hash = dummyHash
	}
	if !CheckPassword(hash, password) || !local {
		refuse("wrong user name or password")
		return
	}
	if acct.Disabled {
		refuse("the account is disabled")
		return
	}
	user := User{Subject: "local:" + acct.Username, Username: acct.Username, Email: acct.Email, Name: acct.Name, Groups: append([]string(nil), acct.Groups...)}
	s.start(w, r, Session{User: user, Method: string(ModeLocal)}, roles.Local, acct.Super, "local account")
	s.accounts.SignedIn(acct.Username, s.now())
}

// start gives a person their session: roles worked out, cookie set, audit
// record written, and on to the page they asked for.
func (s *Service) start(w http.ResponseWriter, r *http.Request, sess Session, src roles.Source, supers []roles.ID, how string) {
	g := s.grant(src, sess.User, supers)
	g.apply(&sess)
	started := s.sessions.Start(sess)
	s.setSession(w, r, started)
	http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: "", Path: "/signin", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	if len(g.Dropped) > 0 {
		s.record(r, audit.ActionNameRefused, sess.User, audit.OutcomeDenied, "reserved group names dropped: "+strings.Join(g.Dropped, ", "))
	}
	s.record(r, audit.ActionSignIn, sess.User, audit.OutcomeOK, how)
	s.log.Info("signed in", "user", sess.User.Username, "method", sess.Method, "admin", started.Admin, "roles", len(started.Roles))
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

// signInDirectory signs a person in with the directory (RB-21, RB-22).
func (s *Service) signInDirectory(w http.ResponseWriter, r *http.Request, login, password string, refuse func(string)) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := s.dir.Authenticate(ctx, login, password)
	switch {
	case errors.Is(err, ErrDirectoryDown):
		s.record(r, audit.ActionSignInFailed, User{Username: login}, audit.OutcomeFailed, "the directory cannot be reached: "+cleanErr(err))
		s.renderSignIn(w, r, "The directory cannot be reached, so nobody can sign in just now. People already signed in can keep working. Try again in a minute.", http.StatusServiceUnavailable)
		return
	case err != nil:
		refuse("the directory refused the user name or password")
		return
	}
	user := User{Subject: "ldap:" + p.Username, Username: p.Username, Email: p.Email, Name: p.Name, Groups: p.Groups}
	if g := s.grant(roles.LDAP, user, nil); len(g.Roles) == 0 {
		refuse("signed in to the directory, but in none of the groups listed for a role")
		return
	}
	s.people.seen(p)
	s.start(w, r, Session{User: user, Method: string(ModeLDAP), Ref: p.Ref}, roles.LDAP, nil, "directory, "+s.dir.URL())
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
	clearCookie(w, r, cookieSignIn, "/signin")
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
	// A user name Kubernetes or 221B reserves is refused (RB-17): the
	// console would act as system:… or as a local or directory person.
	if s.cfg.Prefixes.Reserved(user.Username) {
		s.record(r, audit.ActionNameRefused, user, audit.OutcomeDenied, "reserved user name refused: "+user.Username)
		s.renderSignIn(w, r, "You cannot sign in: the provider names you "+user.Username+", a name reserved for Kubernetes or 221B. Ask an administrator.", http.StatusForbidden)
		return
	}
	g := s.grant(roles.OIDC, user, nil)
	sess := Session{User: user, Method: string(ModeOIDC)}
	g.apply(&sess)
	started := s.sessions.Start(sess)
	s.setSession(w, r, started)
	if len(g.Dropped) > 0 {
		s.record(r, audit.ActionNameRefused, user, audit.OutcomeDenied, "reserved group names dropped: "+strings.Join(g.Dropped, ", "))
	}
	s.record(r, audit.ActionSignIn, user, audit.OutcomeOK, "OIDC, "+s.provider.Name())
	s.log.Info("signed in", "user", user.Username, "method", "oidc", "admin", started.Admin, "groups", len(user.Groups))
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
	clearCookie(w, r, cookieSession, "/")
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"signedOut":true}` + "\n"))
		return
	}
	http.Redirect(w, r, "/signin", http.StatusSeeOther)
}

// Me is what /api/v1/me tells the browser.
type Me struct {
	Mode  Mode `json:"mode"`
	User  User `json:"user"`
	Admin bool `json:"admin"`
	// Roles are the roles the person holds, from the most to the least
	// privileged (RB-41).
	Roles    []roles.ID `json:"roles,omitempty"`
	Method   string     `json:"method"`
	Provider string     `json:"provider,omitempty"`
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
	me := Me{Mode: s.cfg.Mode, User: sess.User, Admin: sess.Admin, Roles: sess.Roles, Method: sess.Method, SessionIdle: int(s.cfg.SessionIdle / time.Second)}
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
