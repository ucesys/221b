package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/oidcsim"
	"github.com/ucesys/221b/server/internal/roles"
)

// Signing in while the session store is away (DB-65), through the
// handlers: the session is kept in its cookie, signed, and every replica
// with the key takes it.

var errStoreAway = fmt.Errorf("writing the session: %w", db.ErrUnavailable)

// outage is an installation whose replicas share a session store, the
// key's Secret, the accounts and the clock. Everything the replicas log
// and audit is kept, and checked at the end for the key and for every
// session cookie the test saw.
type outage struct {
	t        *testing.T
	store    *auth.OutageStore
	keys     *auth.MemoryKeys
	accounts *auth.MemoryBackend
	now      *time.Time
	logs     *syncBuffer
	audits   []*syncBuffer
	// cookies are the session cookies' values seen, never to be logged.
	cookies []string
}

func newOutage(t *testing.T) *outage {
	t.Helper()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	o := &outage{t: t, store: auth.NewOutageStore(), keys: &auth.MemoryKeys{}, accounts: &auth.MemoryBackend{}, now: &now, logs: &syncBuffer{}}
	t.Cleanup(o.nothingSecretWasWritten)
	return o
}

// replica starts a console replica of the installation; change may alter
// its configuration (another key, say).
func (o *outage) replica(cfg auth.Config, change func(*auth.Config)) *env {
	o.t.Helper()
	clock := func() time.Time { return *o.now }
	key := auth.NewSessionKey(o.keys, clock)
	if err := key.Ensure(context.Background()); err != nil {
		o.t.Fatal(err)
	}
	cfg.SessionStore, cfg.SessionKey = o.store, key
	if cfg.Mode == auth.ModeLocal || cfg.Mode == auth.ModeLDAP {
		cfg.Accounts = o.accounts
	}
	if change != nil {
		change(&cfg)
	}
	e := &env{t: o.t, audit: &syncBuffer{}, now: o.now}
	o.audits = append(o.audits, e.audit)
	log := slog.New(slog.NewJSONHandler(o.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := auth.New(cfg, log, audit.New(audit.Options{Out: e.audit, Now: clock}), clock)
	if err != nil {
		o.t.Fatal(err)
	}
	e.svc = svc
	e.app = console(o.t, svc)
	o.t.Cleanup(e.app.Close)
	return e
}

// back makes the store answer again, and lets the replicas notice.
func (o *outage) back() {
	o.store.Fail(nil)
	*o.now = o.now.Add(11 * time.Second)
}

// nothingSecretWasWritten fails the test when the key, in any spelling,
// or a session cookie's value is in a log line or an audit record.
func (o *outage) nothingSecretWasWritten() {
	o.t.Helper()
	written := map[string]string{"the log": o.logs.String()}
	for i, a := range o.audits {
		written[fmt.Sprintf("the audit log of replica %d", i+1)] = a.String()
	}
	secrets := map[string]string{}
	if text := o.keys.Text(); text != "" {
		raw, err := hex.DecodeString(strings.TrimSpace(text))
		if err != nil {
			o.t.Fatalf("the key's Secret does not hold hexadecimal")
		}
		secrets["the key as its Secret holds it"] = text
		secrets["the key in capitals"] = strings.ToUpper(text)
		secrets["the key in base64"] = base64.StdEncoding.EncodeToString(raw)
		secrets["the key in URL-safe base64"] = base64.RawURLEncoding.EncodeToString(raw)
		secrets["half the key"] = text[:32]
	}
	for i, c := range o.cookies {
		secrets[fmt.Sprintf("session cookie %d", i+1)] = c
		if payload, mac, ok := auth.SplitSignedForTest(c); ok {
			secrets[fmt.Sprintf("the MAC of session cookie %d", i+1)] = mac
			secrets[fmt.Sprintf("the payload of session cookie %d", i+1)] = payload
		}
	}
	for where, text := range written {
		for what, secret := range secrets {
			if strings.Contains(text, secret) {
				o.t.Errorf("%s holds %s", where, what)
			}
		}
	}
	if len(o.cookies) == 0 || o.logs.String() == "" {
		o.t.Errorf("nothing to check: %d cookies seen, %d bytes logged", len(o.cookies), len(o.logs.String()))
	}
}

// cookie is the client's session cookie; the test remembers it.
func (o *outage) cookie(e *env, c *http.Client) string {
	o.t.Helper()
	v := sessionID(o.t, e, c)
	o.cookies = append(o.cookies, v)
	return v
}

// holding is a client that carries one cookie's value and nothing else.
func (o *outage) holding(e *env, value string) *http.Client {
	o.t.Helper()
	c := client(o.t)
	u, _ := url.Parse(e.app.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: "221b_session", Value: value, Path: "/"}})
	o.cookies = append(o.cookies, value)
	return c
}

// signInAway signs a person in while the store is away, and reads what
// they are told: the sign-in page, saying how long the session lasts, with
// the way on.
func (o *outage) signInAway(e *env, login, password string) *http.Client {
	o.t.Helper()
	c, resp := e.signIn(login, password)
	body := readAll(o.t, resp)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/signin" || resp.Request.URL.Query().Get("next") != "/" {
		o.t.Fatalf("sign-in of %s while the store is away: %s at %s\n%s", login, resp.Status, resp.Request.URL, body)
	}
	for _, want := range []string{`data-testid="signin-temporary"`, "You are signed in.", "The session store was unavailable when you signed in", "lasts 30 minutes", "at " + o.now.Add(30*time.Minute).UTC().Format("15:04 UTC") + " you will be asked to sign in again", `href="/" data-testid="signin-continue"`} {
		if !strings.Contains(body, want) {
			o.t.Fatalf("the page after the sign-in lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="password"`) {
		o.t.Fatalf("the page after the sign-in asks for a password:\n%s", body)
	}
	if v := o.cookie(e, c); !strings.HasPrefix(v, "s1.") {
		o.t.Fatalf("the session cookie is not a signed session: %d characters", len(v))
	}
	return c
}

func outageConfig(t *testing.T) auth.Config {
	cfg := localConfig(t)
	cfg.Local = []auth.LocalUser{{Username: "root", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}}}
	return cfg
}

func (o *outage) create(e *env, name string, groups ...string) {
	o.t.Helper()
	if _, err := e.svc.Accounts().Create(context.Background(), auth.NewAccount{Username: name, Password: goodPassword, Groups: groups}); err != nil {
		o.t.Fatal(err)
	}
}

// reread is a replica reading the accounts' Secret again, as each does
// every five seconds.
func reread(t *testing.T, e *env) {
	t.Helper()
	if err := e.svc.Accounts().Load(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestASignInWhileTheSessionStoreIsAwayWorksOnEveryReplica(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	// A replica of another installation: the same accounts, another key.
	stranger := o.replica(cfg, func(c *auth.Config) {
		k := auth.NewSessionKey(&auth.MemoryKeys{}, nil)
		if err := k.Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
		c.SessionKey = k
	})
	o.create(a, "dave", "web-readers")
	reread(t, b)
	o.store.Fail(errStoreAway)

	// The cookie as the sign-in sets it: the signed session, kept by the
	// browser for as long as the session lasts and no longer.
	c := client(t)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp := a.post(c, "/signin", url.Values{"email": {"root"}, "password": {goodPassword}, "form": {formIn(t, c, a.app.URL+"/signin")}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin?next=%2F" {
		t.Fatalf("sign-in: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
	var set *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == "221b_session" {
			set = k
		}
	}
	if set == nil || !strings.HasPrefix(set.Value, "s1.") || set.MaxAge != 1800 || !set.HttpOnly || set.SameSite != http.SameSiteLaxMode || set.Path != "/" {
		t.Fatalf("the cookie set: %+v", set)
	}
	o.cookies = append(o.cookies, set.Value)
	if len(set.Value) > auth.MaxSignedForTest {
		t.Fatalf("a cookie of %d characters", len(set.Value))
	}

	root := o.signInAway(a, "root", goodPassword)
	dave := o.signInAway(a, "dave", goodPassword)
	if o.store.Len() != 0 {
		t.Fatalf("the store holds %d records", o.store.Len())
	}
	// An API route that needs a session, on the replica that signed and on
	// one that did not: the same person, with what they may do worked out
	// there.
	for name, e := range map[string]*env{"the replica that signed": a, "another replica": b} {
		w, code := e.whoami(root)
		if code != http.StatusOK || w.User != "root" || !w.Admin || len(w.Roles) != 1 || w.Roles[0] != "superAdmin" || w.KubeName != "221b-local:root" || !strings.Contains(strings.Join(w.KubeGroups, ","), "221b:superadmins") {
			t.Fatalf("%s: root is %+v (%d)", name, w, code)
		}
		w, code = e.whoami(dave)
		if code != http.StatusOK || w.User != "dave" || w.Admin || strings.Join(w.Roles, ",") != "viewer" || w.KubeName != "221b-local:dave" {
			t.Fatalf("%s: dave is %+v (%d)", name, w, code)
		}
		var me auth.Me
		getJSON(t, root, e.app.URL+"/api/v1/me", &me)
		if me.User.Username != "root" || !me.Admin || me.Method != "local" {
			t.Fatalf("%s: me %+v", name, me)
		}
		// A page: the app, not the sign-in page.
		resp, _ := root.Get(e.app.URL + "/")
		if body := readAll(t, resp); !strings.Contains(body, "the app") {
			t.Fatalf("%s: a page shows %q", name, body)
		}
		// A SuperAdmin's page of the console's own: the users.
		if code, body := e.api(root, http.MethodGet, "/api/v1/admin/users", nil); code != http.StatusOK || !strings.Contains(string(body), "dave") {
			t.Fatalf("%s: the users: %d %s", name, code, body)
		}
		if code, _ := e.api(dave, http.MethodGet, "/api/v1/admin/users", nil); code != http.StatusForbidden {
			t.Fatalf("%s: a viewer reads the users: %d", name, code)
		}
	}
	// With another key it is no session at all.
	if _, code := stranger.whoami(root); code != http.StatusUnauthorized {
		t.Fatalf("a replica with another key answered %d", code)
	}
	resp, _ = root.Get(stranger.app.URL + "/")
	if body := readAll(t, resp); !strings.Contains(body, `name="password"`) {
		t.Fatalf("a replica with another key shows %q", body)
	}
	// The audit record says what kind of session it is.
	if got := a.audit.String(); !strings.Contains(got, `"action":"signin"`) || !strings.Contains(got, "the session store was unavailable, so the session is kept in its cookie and lasts 30 minutes") {
		t.Fatalf("audit: %s", got)
	}
	// Signed in already, the sign-in page says again how long it lasts.
	resp, _ = root.Get(b.app.URL + "/signin?next=/%23/pods")
	if body := readAll(t, resp); !strings.Contains(body, "lasts 30 minutes") || !strings.Contains(body, `href="/#/pods"`) {
		t.Fatalf("the sign-in page for someone signed in: %q", body)
	}

	// It is not renewed by use, and ends half an hour after the sign-in on
	// every replica.
	*o.now = o.now.Add(29*time.Minute + 59*time.Second)
	for _, e := range []*env{a, b} {
		if _, code := e.whoami(root); code != http.StatusOK {
			t.Fatalf("a second before its end: %d", code)
		}
	}
	*o.now = o.now.Add(time.Second)
	for _, e := range []*env{a, b} {
		if _, code := e.whoami(root); code != http.StatusUnauthorized {
			t.Fatalf("at its end: %d", code)
		}
		if _, code := e.whoami(dave); code != http.StatusUnauthorized {
			t.Fatalf("at its end: %d", code)
		}
	}
	// The person signs in again, and that is a new half hour.
	again := o.holding(b, set.Value)
	if _, code := b.whoami(again); code != http.StatusUnauthorized {
		t.Fatalf("the first cookie after its end: %d", code)
	}
}

func TestATamperedSignedCookieIsNoSession(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.create(a, "dave", "web-readers")
	reread(t, b)
	o.store.Fail(errStoreAway)
	root := o.cookie(a, o.signInAway(a, "root", goodPassword))
	dave := o.cookie(a, o.signInAway(a, "dave", goodPassword))
	rootPayload, rootMAC, _ := auth.SplitSignedForTest(root)
	davePayload, daveMAC, _ := auth.SplitSignedForTest(dave)
	change := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'x' {
			b[i] = 'y'
		} else {
			b[i] = 'x'
		}
		return string(b)
	}
	calls := o.store.Calls()
	for name, v := range map[string]string{
		"dave's MAC under root's payload":     "s1." + rootPayload + "." + daveMAC,
		"root's MAC under dave's payload":     "s1." + davePayload + "." + rootMAC,
		"a payload changed":                   "s1." + change(davePayload, 10) + "." + daveMAC,
		"a MAC changed":                       "s1." + davePayload + "." + change(daveMAC, 10),
		"the MAC cut off":                     "s1." + davePayload,
		"the MAC, which is shaped like an ID": daveMAC,
		"the payload":                         davePayload,
		"an ID in a signed cookie's place":    "s1." + strings.Repeat("a", 43),
		"another format":                      "s2." + davePayload + "." + daveMAC,
	} {
		for _, e := range []*env{a, b} {
			c := o.holding(e, v)
			// A value shaped like a stored session's ID cannot be told,
			// with the store away, from one another replica holds: it is
			// answered 503, not served and not sent to sign in (the review
			// of the auth package, cut by its cap).
			want := http.StatusUnauthorized
			if name == "the MAC, which is shaped like an ID" {
				want = http.StatusServiceUnavailable
			}
			if w, code := e.whoami(c); code != want {
				t.Errorf("%s: answered %d as %+v", name, code, w)
			}
			resp, _ := c.Get(e.app.URL + "/")
			if body := readAll(t, resp); strings.Contains(body, "the app") {
				t.Errorf("%s: a page was served", name)
			}
		}
	}
	// One read at most: the value shaped like an ID, on the replica that
	// had not yet found the store away.
	if n := o.store.Calls() - calls; n > 1 {
		t.Fatalf("made-up cookies cost the store %d calls", n)
	}
	// The real ones still work.
	if w, code := b.whoami(o.holding(b, dave)); code != http.StatusOK || w.User != "dave" || w.Admin {
		t.Fatalf("dave: %+v %d", w, code)
	}
}

func TestALocalAccountDisabledOrDeletedLosesItsSignedSessionAtOnce(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	for _, name := range []string{"dave", "erin", "fay", "gus"} {
		o.create(a, name, "web-readers")
	}
	if _, err := a.svc.Accounts().SetSuper(context.Background(), "gus", []roles.ID{roles.SuperAdmin}); err != nil {
		t.Fatal(err)
	}
	reread(t, b)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	dave, erin, fay, gus := o.signInAway(a, "dave", goodPassword), o.signInAway(a, "erin", goodPassword), o.signInAway(b, "fay", goodPassword), o.signInAway(b, "gus", goodPassword)
	for _, c := range []*http.Client{dave, erin, fay, gus} {
		for _, e := range []*env{a, b} {
			if _, code := e.whoami(c); code != http.StatusOK {
				t.Fatalf("not signed in: %d", code)
			}
		}
	}
	// Disabled through the console, by a SuperAdmin whose own session is a
	// signed one: refused at once where it was done, and on the other
	// replica as soon as it has read the accounts again.
	if code, body := a.api(root, http.MethodPatch, "/api/v1/admin/users/dave", map[string]any{"disabled": true}); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, body)
	}
	if _, code := a.whoami(dave); code != http.StatusUnauthorized {
		t.Fatalf("a disabled account's signed session: %d", code)
	}
	reread(t, b)
	if _, code := b.whoami(dave); code != http.StatusUnauthorized {
		t.Fatalf("a disabled account's signed session on the other replica: %d", code)
	}
	// Enabled again, the cookie that was refused stays refused there.
	if code, _ := a.api(root, http.MethodPatch, "/api/v1/admin/users/dave", map[string]any{"disabled": false}); code != http.StatusOK {
		t.Fatal("enable")
	}
	reread(t, b)
	for _, e := range []*env{a, b} {
		if _, code := e.whoami(dave); code != http.StatusUnauthorized {
			t.Fatalf("a session ended by a disable came back: %d", code)
		}
	}
	// Deleted.
	if code, body := a.api(root, http.MethodDelete, "/api/v1/admin/users/erin", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}
	if _, code := a.whoami(erin); code != http.StatusUnauthorized {
		t.Fatalf("a deleted account's signed session: %d", code)
	}
	reread(t, b)
	if _, code := b.whoami(erin); code != http.StatusUnauthorized {
		t.Fatalf("a deleted account's signed session on the other replica: %d", code)
	}
	// What the account may do is the account's as it is now, not the
	// cookie's: a group taken away, a super role taken away.
	if _, err := a.svc.Accounts().ChangeGroups(context.Background(), "fay", nil, []string{"web-readers"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Accounts().SetSuper(context.Background(), "gus", nil); err != nil {
		t.Fatal(err)
	}
	reread(t, b)
	for _, e := range []*env{a, b} {
		if w, code := e.whoami(fay); code != http.StatusOK || len(w.Roles) != 0 || len(w.KubeGroups) != 1 {
			t.Fatalf("fay without her group: %+v %d", w, code)
		}
		if w, code := e.whoami(gus); code != http.StatusOK || w.Admin || strings.Join(w.Roles, ",") != "viewer" {
			t.Fatalf("gus without the super role: %+v %d", w, code)
		}
		if code, _ := e.api(gus, http.MethodGet, "/api/v1/admin/users", nil); code != http.StatusForbidden {
			t.Fatalf("gus reads the users: %d", code)
		}
	}
	// A password reset does not end a signed session on a replica that does
	// not hold it: the half hour is what bounds it (DB-65). Where it is
	// held, it ends.
	if code, body := a.api(root, http.MethodPatch, "/api/v1/admin/users/fay", map[string]any{"password": "another long password", "currentPassword": goodPassword}); code != http.StatusOK {
		t.Fatalf("reset: %d %s", code, body)
	}
	if _, code := a.whoami(fay); code != http.StatusUnauthorized {
		t.Fatalf("after a reset, where it was done: %d", code)
	}
}

func TestSigningOutOfASignedSessionEndsItOnThatReplica(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	kept := o.cookie(a, root)
	if _, code := b.whoami(root); code != http.StatusOK {
		t.Fatal("not signed in on the other replica")
	}
	req, _ := http.NewRequest(http.MethodPost, a.app.URL+"/signout", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := root.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cleared bool
	for _, k := range resp.Cookies() {
		if k.Name == "221b_session" && k.Value == "" && k.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("the sign-out did not clear the cookie")
	}
	if _, code := a.whoami(root); code != http.StatusUnauthorized {
		t.Fatalf("the browser after the sign-out: %d", code)
	}
	// Someone who kept the cookie: refused where the sign-out was, until
	// the session would have ended anyway.
	thief := o.holding(a, kept)
	for range 3 {
		*o.now = o.now.Add(9 * time.Minute)
		a.svc.Sweep()
		if _, code := a.whoami(thief); code != http.StatusUnauthorized {
			t.Fatalf("a signed-out signed session was taken on the replica that signed it out: %d", code)
		}
	}
	// The other replica could not be told, and takes it until it expires:
	// that is the bound the document gives, half an hour from sign-in.
	if _, code := b.whoami(thief); code != http.StatusOK {
		t.Fatalf("the replica that was not told answered %d: the document says it takes the cookie until it expires", code)
	}
	*o.now = o.now.Add(4 * time.Minute)
	if _, code := b.whoami(thief); code != http.StatusUnauthorized {
		t.Fatalf("after half an hour: %d", code)
	}
	if !strings.Contains(a.audit.String(), `"action":"signout"`) {
		t.Fatalf("audit: %s", a.audit.String())
	}
}

func TestASignedSessionBecomesAStoredOneWhenTheStoreIsBack(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	signedInAt := *o.now
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	*o.now = o.now.Add(5 * time.Minute)
	if _, code := b.whoami(root); code != http.StatusOK {
		t.Fatal("not signed in")
	}
	// A store that answers, and refuses the write: the signed session
	// stands, and the exchange is tried again later.
	o.store.Fail(errors.New("secrets is forbidden"))
	*o.now = o.now.Add(11 * time.Second)
	for range 3 {
		if _, code := b.whoami(root); code != http.StatusOK {
			t.Fatalf("with an exchange that fails: %d", code)
		}
	}
	if o.cookie(b, root) != signed {
		t.Fatal("the cookie changed although no stored session was started")
	}

	// The store is back: the next request, on a replica that did not sign
	// it, starts a stored session for the same person and sets the usual
	// cookie. Nobody is asked to sign in.
	o.store.Fail(nil)
	*o.now = o.now.Add(31 * time.Second)
	w, code := b.whoami(root)
	if code != http.StatusOK || w.User != "root" || !w.Admin {
		t.Fatalf("the request that exchanges: %+v %d", w, code)
	}
	stored := o.cookie(b, root)
	if strings.Contains(stored, ".") || len(stored) != 43 {
		t.Fatalf("after the exchange the cookie is not an ID: %d characters", len(stored))
	}
	recs := o.store.Records()
	if len(recs) != 1 {
		t.Fatalf("the store holds %d records", len(recs))
	}
	for _, b := range recs {
		var rec struct {
			User struct {
				Subject, Username, KubeName string
			}
			Admin                      bool
			Roles                      []string
			Method                     string
			Created, LastSeen, Expires time.Time
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.User.Subject != "local:root" || rec.User.Username != "root" || rec.User.KubeName != "221b-local:root" || !rec.Admin || rec.Method != "local" || strings.Join(rec.Roles, ",") != "superAdmin" {
			t.Fatalf("the stored session: %s", b)
		}
		// Signed in when the person signed in, not at the exchange, and
		// ending when the signed session would have: the exchange gives no
		// more than the signed session had (DB-65).
		if !rec.Created.Equal(signedInAt) || !rec.Expires.Equal(signedInAt.Add(30*time.Minute)) {
			t.Fatalf("created %s, expires %s; signed in at %s", rec.Created, rec.Expires, signedInAt)
		}
		if strings.Contains(string(b), signed) || strings.Contains(string(b), stored) {
			t.Fatal("the stored session holds a cookie's value")
		}
	}
	// The exchange is in the audit log of the replica that made it, once,
	// with who it was for. (That no cookie is in it is checked at the end.)
	if n := strings.Count(b.audit.String(), `"action":"session.exchange"`); n != 1 || !strings.Contains(b.audit.String(), "local:root") {
		t.Fatalf("%d audit records of the exchange: %s", n, b.audit.String())
	}
	if strings.Contains(a.audit.String(), `"action":"session.exchange"`) {
		t.Fatal("the replica that did not exchange recorded an exchange")
	}
	// Both replicas take the new cookie, for as long as the signed session
	// would have lasted: 25 minutes after the sign-in.
	*o.now = signedInAt.Add(25 * time.Minute)
	for _, e := range []*env{a, b} {
		if w, code := e.whoami(root); code != http.StatusOK || !w.Admin {
			t.Fatalf("25 minutes after the sign-in: %+v %d", w, code)
		}
		resp, _ := root.Get(e.app.URL + "/")
		if body := readAll(t, resp); !strings.Contains(body, "the app") {
			t.Fatalf("25 minutes after the sign-in, a page shows %q", body)
		}
	}
	if len(o.store.Records()) != 1 {
		t.Fatalf("the store holds %d records", len(o.store.Records()))
	}
	// The sign-in page no longer says the session is a short one.
	resp, _ := root.Get(a.app.URL + "/signin")
	if body := readAll(t, resp); !strings.Contains(body, "the app") {
		t.Fatalf("the sign-in page for a stored session: %q", body)
	}
	// And no longer: used a minute ago or not, the stored session ends
	// when the signed one would have, on every replica, and so does the
	// signed cookie it came from. (This test asserted, until the ninth
	// cluster test's F1, that the stored session lived on for days.)
	*o.now = signedInAt.Add(30*time.Minute + time.Second)
	for _, e := range []*env{a, b} {
		for what, c := range map[string]*http.Client{"the stored cookie": o.holding(e, stored), "the signed cookie": o.holding(e, signed)} {
			if _, code := e.whoami(c); code != http.StatusUnauthorized {
				t.Fatalf("%s after the signed session's half hour: %d", what, code)
			}
		}
	}
}

func TestTheExchangeOfASignedSessionHappensOnce(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a := o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	o.back()
	// Several requests of the browser arrive with the signed cookie: a
	// page's first calls go out together.
	results := make(chan int, 8)
	for range 8 {
		go func() {
			_, code := a.whoami(o.holdingQuietly(a, signed))
			results <- code
		}()
	}
	for range 8 {
		if code := <-results; code != http.StatusOK {
			t.Fatalf("a request during the exchange: %d", code)
		}
	}
	if n := o.store.Len(); n != 1 {
		t.Fatalf("%d stored sessions were started for one signed session", n)
	}
	// A browser that missed the answer with the new cookie is given it
	// again.
	late := o.holding(a, signed)
	if _, code := a.whoami(late); code != http.StatusOK {
		t.Fatal("not signed in")
	}
	stored := o.cookie(a, late)
	if len(stored) != 43 || o.store.Len() != 1 {
		t.Fatalf("the late browser has a cookie of %d characters; %d stored", len(stored), o.store.Len())
	}
	// Signing out of the stored session ends the signed one here too:
	// the cookie it replaced is not a way back in.
	req, _ := http.NewRequest(http.MethodPost, a.app.URL+"/signout", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, _ := late.Do(req)
	resp.Body.Close()
	// What the store holds now is no session: the record that the session
	// was ended, kept until the signed one would have expired, so no
	// replica exchanges the signed cookie again.
	if sessions, endings := o.kept(); sessions != 0 || endings != 1 {
		t.Fatalf("after the sign-out the store holds %d sessions and %d endings", sessions, endings)
	}
	if _, code := a.whoami(o.holding(a, signed)); code != http.StatusUnauthorized {
		t.Fatalf("the signed cookie after the sign-out of the session that replaced it: %d", code)
	}
	if _, code := a.whoami(o.holding(a, stored)); code != http.StatusUnauthorized {
		t.Fatalf("the stored cookie after its sign-out: %d", code)
	}
	if sessions, endings := o.kept(); sessions != 0 || endings != 1 {
		t.Fatalf("the signed cookie started a stored session again: %d sessions and %d endings", sessions, endings)
	}
	// The ending is removed once the signed session would have expired.
	*o.now = o.now.Add(31 * time.Minute)
	a.svc.Sweep()
	if o.store.Len() != 0 {
		t.Fatalf("after the half hour the store still holds %d records", o.store.Len())
	}
}

// kept counts what the store holds: the sessions, and the records of
// sessions that were ended (kept until the signed session they stood for
// would have expired). An ending names nobody's groups or roles.
func (o *outage) kept() (sessions, endings int) {
	o.t.Helper()
	for _, b := range o.store.Records() {
		var rec struct {
			Method string
			Admin  bool
			Roles  []string
			Super  []string
			User   struct{ Groups, KubeGroups []string }
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			o.t.Fatal(err)
		}
		if rec.Method != "ended" {
			sessions++
			continue
		}
		endings++
		if rec.Admin || len(rec.Roles)+len(rec.Super)+len(rec.User.Groups)+len(rec.User.KubeGroups) != 0 {
			o.t.Fatalf("the record of an ended session carries what a session may do: %s", b)
		}
	}
	return sessions, endings
}

// holdingQuietly is holding for use from several goroutines: the value is
// one the test remembers already.
func (o *outage) holdingQuietly(e *env, value string) *http.Client {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(e.app.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "221b_session", Value: value, Path: "/"}})
	return &http.Client{Jar: jar}
}

func TestSigningOutWithTheRequestThatExchangesEndsBoth(t *testing.T) {
	o := newOutage(t)
	a := o.replica(outageConfig(t), nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	o.back()
	// The first request after the store is back is the sign-out.
	req, _ := http.NewRequest(http.MethodPost, a.app.URL+"/signout", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, _ := root.Do(req)
	resp.Body.Close()
	if sessions, endings := o.kept(); sessions != 0 || endings != 1 {
		t.Fatalf("the sign-out left %d stored sessions and %d endings", sessions, endings)
	}
	if _, code := a.whoami(root); code != http.StatusUnauthorized {
		t.Fatalf("the browser after the sign-out: %d", code)
	}
	if _, code := a.whoami(o.holding(a, signed)); code != http.StatusUnauthorized {
		t.Fatalf("the signed cookie after the sign-out: %d", code)
	}
}

func TestAStreamDoesNotExchangeASignedSession(t *testing.T) {
	o := newOutage(t)
	a := o.replica(outageConfig(t), nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	o.back()
	for _, headers := range []map[string]string{{"Accept": "text/event-stream"}, {"Connection": "Upgrade", "Upgrade": "websocket"}} {
		if code := a.ask(root, "/api/v1/snapshot", headers); code != http.StatusOK {
			t.Fatalf("a stream with a signed session: %d", code)
		}
	}
	if o.store.Len() != 0 || o.cookie(a, root) != signed {
		t.Fatal("a stream exchanged the session")
	}
	// What the page asks by itself does.
	if code := a.ask(root, "/api/v1/snapshot", map[string]string{"X-221B-Background": "1"}); code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	if o.store.Len() != 1 || len(o.cookie(a, root)) != 43 {
		t.Fatal("the page's own request did not exchange the session")
	}
	// The stream opened with the signed session goes on until that ends.
	if _, ok := a.svc.Current(auth.Session{ID: signed}); !ok {
		t.Fatal("what was opened with the signed session was closed by the exchange")
	}
	*o.now = o.now.Add(29 * time.Minute)
	if _, code := a.whoami(root); code != http.StatusOK {
		t.Fatalf("the stored session before the half hour ends: %d", code)
	}
	// The half hour ends for both: the stored session gives no more than
	// the signed one had, and the person signs in again. (This test
	// asserted that the stored session went on, until the ninth cluster
	// test's F1.)
	*o.now = o.now.Add(2 * time.Minute)
	if _, ok := a.svc.Current(auth.Session{ID: signed}); ok {
		t.Fatal("the signed session outlived its half hour")
	}
	if _, code := a.whoami(root); code != http.StatusUnauthorized {
		t.Fatalf("the stored session after the half hour: %d", code)
	}
}

func TestAStoreThatRefusesMakesNoSignedSession(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errors.New(`secrets "k8s-console-221b-session-x" is forbidden: exceeded quota`))
	c, resp := a.signIn("root", goodPassword)
	body := readAll(t, resp)
	if resp.Request.URL.Path != "/" || !strings.Contains(body, "the app") {
		t.Fatalf("sign-in: %s at %s", resp.Status, resp.Request.URL)
	}
	v := o.cookie(a, c)
	if strings.HasPrefix(v, "s1.") || len(v) != 43 {
		t.Fatalf("a store that refused made a cookie of %d characters", len(v))
	}
	// As it always was: the session is the replica's that made it, and no
	// other's.
	if _, code := a.whoami(c); code != http.StatusOK {
		t.Fatalf("on the replica that made it: %d", code)
	}
	if _, code := b.whoami(c); code != http.StatusUnauthorized {
		t.Fatalf("on another replica: %d", code)
	}
	if strings.Contains(a.audit.String(), "kept in its cookie") {
		t.Fatalf("audit: %s", a.audit.String())
	}
}

func TestAPersonInTooManyGroupsIsToldALocalAccountCanSignIn(t *testing.T) {
	o := newOutage(t)
	d := &fakeDir{people: map[string]*auth.Person{}, pw: map[string]string{"priya": goodPassword, "omar": goodPassword}}
	groups := []string{"platform-admins"}
	for i := range 700 {
		groups = append(groups, fmt.Sprintf("cn=%x-%d,ou=groups,dc=corp,dc=example", sha256.Sum256([]byte(fmt.Sprint(i))), i))
	}
	d.people["priya"] = &auth.Person{Ref: "uid=priya", Username: "priya", Name: "Priya", Groups: groups}
	d.people["omar"] = &auth.Person{Ref: "uid=omar", Username: "omar", Name: "Omar", Groups: []string{"team-a-ops"}}
	cfg := auth.Config{
		Mode: auth.ModeLDAP, Directory: d, AccessTTL: 2 * time.Minute,
		Lists: auth.RoleLists{roles.SuperAdmin: {"platform-admins"}, roles.Admin: {"team-a-ops"}},
		Local: []auth.LocalUser{{Username: "break-glass", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}}},
	}
	a := o.replica(cfg, nil)
	// With the store answering she signs in, groups and all.
	if _, code := a.whoami(a.mustSignIn("priya", goodPassword)); code != http.StatusOK {
		t.Fatal("priya cannot sign in with the store answering")
	}
	before := o.store.Len()
	o.store.Fail(errStoreAway)
	c, resp := a.signIn("priya", goodPassword)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "The session store is unavailable, and you are in too many groups for a session kept in the browser instead. A local account can sign in.") || !strings.Contains(body, `data-testid="signin-error"`) {
		t.Fatalf("sign-in of a person in 700 groups: %s\n%s", resp.Status, body)
	}
	u, _ := url.Parse(a.app.URL)
	for _, k := range c.Jar.Cookies(u) {
		if k.Name == "221b_session" {
			t.Fatalf("a session cookie of %d characters was set", len(k.Value))
		}
	}
	if _, code := a.whoami(c); code != http.StatusUnauthorized {
		t.Fatalf("after the refusal: %d", code)
	}
	if !strings.Contains(a.audit.String(), "do not fit a session kept in its cookie") {
		t.Fatalf("audit: %s", a.audit.String())
	}
	// The local account, and a directory person in few groups, sign in.
	glass := o.signInAway(a, "break-glass", goodPassword)
	if w, code := a.whoami(glass); code != http.StatusOK || !w.Admin {
		t.Fatalf("break-glass: %+v %d", w, code)
	}
	omar := o.signInAway(a, "omar", goodPassword)
	if w, code := a.whoami(omar); code != http.StatusOK || strings.Join(w.Roles, ",") != "admin" {
		t.Fatalf("omar: %+v %d", w, code)
	}
	if o.store.Len() != before {
		t.Fatal("something was stored")
	}
}

func TestADirectoryPersonGetsASignedSessionAndStillLosesItWithTheirGroups(t *testing.T) {
	o := newOutage(t)
	d := &fakeDir{
		people: map[string]*auth.Person{
			"priya": {Ref: "uid=priya", Username: "priya", Name: "Priya", Email: "priya@corp.example", Groups: []string{"platform-admins"}},
			"omar":  {Ref: "uid=omar", Username: "omar", Name: "Omar", Groups: []string{"team-a-ops", "221b:superadmins"}},
		},
		pw: map[string]string{"priya": goodPassword, "omar": goodPassword},
	}
	cfg := auth.Config{
		Mode: auth.ModeLDAP, Directory: d, AccessTTL: 2 * time.Minute,
		Lists: auth.RoleLists{roles.SuperAdmin: {"platform-admins"}, roles.Admin: {"team-a-ops"}, roles.Viewer: {"readers"}},
		Local: []auth.LocalUser{{Username: "break-glass", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}}},
	}
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	priya, omar := o.signInAway(a, "priya", goodPassword), o.signInAway(a, "omar", goodPassword)
	for name, e := range map[string]*env{"the replica that signed": a, "another replica": b} {
		w, code := e.whoami(priya)
		if code != http.StatusOK || w.User != "priya" || !w.Admin || w.KubeName != "221b-ldap:priya" {
			t.Fatalf("%s: priya is %+v (%d)", name, w, code)
		}
		// A reserved group name the directory gave is not asserted, on any
		// replica (RB-17).
		w, code = e.whoami(omar)
		if code != http.StatusOK || w.Admin || strings.Join(w.Roles, ",") != "admin" || strings.Contains(strings.Join(w.KubeGroups, ","), "superadmins") {
			t.Fatalf("%s: omar is %+v (%d)", name, w, code)
		}
		var me auth.Me
		getJSON(t, priya, e.app.URL+"/api/v1/me", &me)
		if me.Method != "ldap" || me.User.Email != "priya@corp.example" || me.User.Name != "Priya" {
			t.Fatalf("%s: me %+v", name, me)
		}
	}
	// Moved to another group: the roles follow at the next reading of the
	// groups, on a replica that never read them itself.
	d.mu.Lock()
	d.people["priya"].Groups = []string{"readers"}
	d.people["omar"].Groups = []string{"everyone"}
	d.mu.Unlock()
	*o.now = o.now.Add(3 * time.Minute)
	for _, e := range []*env{b, a} {
		if w, code := e.whoami(priya); code != http.StatusOK || w.Admin || strings.Join(w.Roles, ",") != "viewer" {
			t.Fatalf("priya after the move: %+v %d", w, code)
		}
		// In no listed group any more: the session ends (RB-23).
		if _, code := e.whoami(omar); code != http.StatusUnauthorized {
			t.Fatalf("omar, in no listed group: %d", code)
		}
	}
	// With the directory away too, the groups last read stand.
	d.mu.Lock()
	d.down = true
	d.mu.Unlock()
	*o.now = o.now.Add(3 * time.Minute)
	if w, code := b.whoami(priya); code != http.StatusOK || strings.Join(w.Roles, ",") != "viewer" {
		t.Fatalf("priya with the directory away: %+v %d", w, code)
	}
	// And the local account still signs in (RB-28a).
	if w, code := b.whoami(o.signInAway(b, "break-glass", goodPassword)); code != http.StatusOK || !w.Admin {
		t.Fatalf("break-glass: %+v %d", w, code)
	}
	// The exchange keeps the way of signing in and the directory's name
	// for the person.
	o.back()
	if _, code := b.whoami(priya); code != http.StatusOK {
		t.Fatal("the exchange")
	}
	if v := o.cookie(b, priya); len(v) != 43 {
		t.Fatalf("priya's cookie after the store is back: %d characters", len(v))
	}
	var found bool
	for _, rec := range o.store.Records() {
		if strings.Contains(string(rec), `"method":"ldap"`) && strings.Contains(string(rec), `"ref":"uid=priya"`) && strings.Contains(string(rec), `"subject":"ldap:priya"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the stored session: %v", o.store.Records())
	}
}

func TestAnOIDCSignInGetsASignedSessionToo(t *testing.T) {
	var idp *oidcsim.Provider
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { idp.ServeHTTP(w, r) }))
	defer idpSrv.Close()
	var err error
	idp, err = oidcsim.New(oidcsim.Options{Issuer: idpSrv.URL + "/oidc"})
	if err != nil {
		t.Fatal(err)
	}
	o := newOutage(t)
	// The provider's tokens are checked against the real clock.
	*o.now = time.Now()
	cfg := auth.Config{
		Mode:        auth.ModeOIDC,
		OIDC:        auth.OIDCConfig{Issuer: idpSrv.URL + "/oidc", ClientID: "221b", ClientSecret: "221b-secret", ProviderName: "Simulated"},
		AdminGroups: []string{"platform-admins"},
	}
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	c := client(t)
	resp, err := c.Get(a.app.URL + "/signin/start?next=" + url.QueryEscape("/#/pods?ns=web"))
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	q := resp.Request.URL.Query()
	q.Set("email", "admin@corp.example")
	q.Set("password", "password")
	resp, err = c.PostForm(idpSrv.URL+"/oidc/auth", q)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/signin" || resp.Request.URL.Query().Get("next") != "/#/pods?ns=web" {
		t.Fatalf("after the provider: %s at %s\n%s", resp.Status, resp.Request.URL, body)
	}
	if !strings.Contains(body, "lasts 30 minutes") || !strings.Contains(body, `href="/#/pods?ns=web" data-testid="signin-continue"`) || strings.Contains(body, "Sign in with Simulated") {
		t.Fatalf("the page after the sign-in:\n%s", body)
	}
	if v := o.cookie(a, c); !strings.HasPrefix(v, "s1.") {
		t.Fatalf("the cookie is not a signed session: %d characters", len(v))
	}
	for name, e := range map[string]*env{"the replica that signed": a, "another replica": b} {
		var me auth.Me
		getJSON(t, c, e.app.URL+"/api/v1/me", &me)
		if me.User.Username != "admin@corp.example" || me.User.Name != "Ada Admin" || !me.Admin || me.Method != "oidc" || !strings.Contains(strings.Join(me.User.Groups, ","), "platform-admins") {
			t.Fatalf("%s: me %+v", name, me)
		}
		if w, code := e.whoami(c); code != http.StatusOK || !w.Admin {
			t.Fatalf("%s: %+v %d", name, w, code)
		}
	}
	if !strings.Contains(a.audit.String(), "OIDC, Simulated; the session store was unavailable") {
		t.Fatalf("audit: %s", a.audit.String())
	}
	if o.store.Len() != 0 {
		t.Fatal("something was stored")
	}
	o.back()
	if _, code := b.whoami(c); code != http.StatusOK || len(o.cookie(b, c)) != 43 || o.store.Len() != 1 {
		t.Fatalf("the exchange: %d, %d stored", code, o.store.Len())
	}
}

// A signed session says how its person signed in. A console that does not
// sign people in that way does not take it: nothing there would check the
// account, or bound the groups it names.
func TestASignedSessionOfAnotherWayOfSigningInIsRefused(t *testing.T) {
	o := newOutage(t)
	local := o.replica(outageConfig(t), nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(local, "root", goodPassword)
	var idp *oidcsim.Provider
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { idp.ServeHTTP(w, r) }))
	defer idpSrv.Close()
	idp, _ = oidcsim.New(oidcsim.Options{Issuer: idpSrv.URL + "/oidc"})
	oidc := o.replica(auth.Config{Mode: auth.ModeOIDC, OIDC: auth.OIDCConfig{Issuer: idpSrv.URL + "/oidc", ClientID: "221b"}, Admins: []string{"root"}}, nil)
	if w, code := oidc.whoami(root); code != http.StatusUnauthorized {
		t.Fatalf("a console with OIDC sign-in took a local account's signed session: %+v", w)
	}
	if _, code := local.whoami(root); code != http.StatusOK {
		t.Fatal("the console that signed it refuses it")
	}
}

func TestTheSignedSessionsLifetimeIsTheConfigurations(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	cfg.OutageSession = 10 * time.Minute
	a := o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	c, resp := a.signIn("root", goodPassword)
	body := readAll(t, resp)
	if !strings.Contains(body, "lasts 10 minutes") || !strings.Contains(body, "at 09:10 UTC") {
		t.Fatalf("the page: %s", body)
	}
	o.cookie(a, c)
	*o.now = o.now.Add(9*time.Minute + 59*time.Second)
	if _, code := a.whoami(c); code != http.StatusOK {
		t.Fatal("ended early")
	}
	*o.now = o.now.Add(time.Second)
	if _, code := a.whoami(c); code != http.StatusUnauthorized {
		t.Fatal("outlived its ten minutes")
	}
	if !bytes.Contains([]byte(a.audit.String()), []byte("lasts 10 minutes")) {
		t.Fatalf("audit: %s", a.audit.String())
	}
}

// A request that is forwarded to the leader goes through the gate twice:
// the replica that got it, then the leader. The session is exchanged once.
func TestARequestForwardedToTheLeaderExchangesOnce(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, leader := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	signed := o.cookie(a, o.signInAway(a, "root", goodPassword))
	o.back()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/releases", nil)
	req.AddCookie(&http.Cookie{Name: "other", Value: "kept"})
	req.AddCookie(&http.Cookie{Name: "221b_session", Value: signed})
	rec := httptest.NewRecorder()
	gated := a.svc.Gate(rec, req)
	if gated == nil {
		t.Fatalf("the gate refused: %d", rec.Code)
	}
	var set string
	for _, k := range rec.Result().Cookies() {
		if k.Name == "221b_session" {
			set = k.Value
		}
	}
	o.cookies = append(o.cookies, set)
	carried, err := gated.Cookie("221b_session")
	if err != nil || len(set) != 43 || carried.Value != set || len(gated.Cookies()) != 2 {
		t.Fatalf("the request carries on with %d cookies; the session cookie set has %d characters", len(gated.Cookies()), len(set))
	}
	if k, err := gated.Cookie("other"); err != nil || k.Value != "kept" {
		t.Fatal("another cookie of the request was lost")
	}
	sess, _ := auth.SessionFrom(gated.Context())
	if sess.ID != set || sess.Signed || !sess.Admin {
		t.Fatalf("the request's session: signed %v, admin %v", sess.Signed, sess.Admin)
	}
	// The leader gets the request as it now is.
	rec2 := httptest.NewRecorder()
	again := leader.svc.Gate(rec2, gated)
	if again == nil {
		t.Fatalf("the leader's gate refused: %d", rec2.Code)
	}
	if len(rec2.Result().Cookies()) != 0 || o.store.Len() != 1 {
		t.Fatalf("the leader set %d cookies; %d sessions stored", len(rec2.Result().Cookies()), o.store.Len())
	}
	if sess, _ := auth.SessionFrom(again.Context()); sess.ID != set || !sess.Admin {
		t.Fatal("the leader sees another session")
	}
}
