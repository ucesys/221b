package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// The findings of the review of the auth package (2026-11-23), each with
// the failure it described, through the handlers.

// Finding 1: a session made while the store was away and exchanged for a
// stored one is what the person is now, not what they were at the
// exchange: a SuperAdmin role taken away is gone from the signed cookie
// too.
func TestAnExchangedSessionIsWhatThePersonIsNow(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a := o.replica(cfg, nil)
	o.create(a, "gus")
	if _, err := a.svc.Accounts().SetSuper(context.Background(), "gus", []roles.ID{roles.SuperAdmin}); err != nil {
		t.Fatal(err)
	}
	o.store.Fail(errStoreAway)
	gus := o.signInAway(a, "gus", goodPassword)
	signed := o.cookie(a, gus)
	o.back()
	if code, _ := a.api(o.holding(a, signed), http.MethodGet, "/api/v1/admin/users", nil); code != http.StatusOK {
		t.Fatalf("gus, a SuperAdmin, at the exchange: %d", code)
	}
	if _, err := a.svc.Accounts().SetSuper(context.Background(), "gus", nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := a.api(o.holding(a, signed), http.MethodGet, "/api/v1/admin/users", nil); code != http.StatusForbidden {
		t.Fatalf("gus's signed cookie after the role was taken away: %d, want 403", code)
	}
	if w, _ := a.whoami(o.holding(a, signed)); w.Admin {
		t.Fatalf("gus's signed cookie is still a SuperAdmin: %+v", w)
	}
	// Disabled, the signed cookie is refused, not exchanged.
	disabled := true
	if _, _, err := a.svc.Accounts().Update(context.Background(), "gus", auth.Change{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, code := a.whoami(o.holding(a, signed)); code != http.StatusUnauthorized {
		t.Fatalf("gus's signed cookie once disabled: %d", code)
	}
}

// Finding 2: a replica whose copy of the accounts lags another's (it
// reads them again every 5 seconds) does not end the session of an
// account it has not read yet: it reads them again, and the first
// administrator stays signed in on every replica.
func TestAReplicaThatHasNotReadANewAccountYetKeepsItsSession(t *testing.T) {
	o := newOutage(t)
	cfg := localConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	c := client(t)
	resp := a.post(c, "/setup", url.Values{"form": {formIn(t, c, a.app.URL+"/setup")}, "token": {"s3cret"}, "username": {"mikhail"}, "name": {"Mikhail"}, "password": {goodPassword}, "again": {goodPassword}})
	readAll(t, resp)
	if resp.Request.URL.Path != "/" {
		t.Fatalf("the first-run page went to %s", resp.Request.URL)
	}
	o.cookie(a, c)
	// The next request reaches the other replica before it reads the
	// accounts again.
	if w, code := b.whoami(c); code != http.StatusOK || !w.Admin {
		t.Fatalf("the first administrator on the other replica: %d %+v", code, w)
	}
	if w, code := a.whoami(c); code != http.StatusOK || !w.Admin {
		t.Fatalf("the first administrator on the replica that made it: %d %+v", code, w)
	}
	resp, _ = c.Get(b.app.URL + "/")
	if body := readAll(t, resp); strings.Contains(body, "Create the first administrator") {
		t.Fatalf("the other replica shows the first-run page: %s", body)
	}
}

// Finding 3: a password reset is not undone, nor got round, on a replica
// that still holds the old hash: the password is checked against the
// account as saved, at sign-in and when it is changed.
func TestAResetPasswordIsCheckedAsSavedOnEveryReplica(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.create(a, "mallory")
	reread(t, b)
	const reset = "the administrator's reset password"
	if _, _, err := a.svc.Accounts().Update(context.Background(), "mallory", auth.Change{Password: ptrTo(reset)}); err != nil {
		t.Fatal(err)
	}
	// b has not read the accounts again: its copy holds the old hash.
	if _, resp := b.signIn("mallory", goodPassword); resp.StatusCode != http.StatusUnauthorized {
		readAll(t, resp)
		t.Fatalf("the old password signed in on the other replica: %s", resp.Status)
	}
	reread(t, b)
	o.create(a, "nina")
	reread(t, b)
	if _, _, err := a.svc.Accounts().Update(context.Background(), "nina", auth.Change{Password: ptrTo(reset)}); err != nil {
		t.Fatal(err)
	}
	r := b.changePassword("nina", goodPassword, "the leaked password's owner's own", "the leaked password's owner's own")
	if body := readAll(t, r); r.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Your password was not changed") {
		t.Fatalf("a change given the old password on the other replica: %s at %s", r.Status, r.Request.URL)
	}
	o.cookie(a, a.mustSignIn("nina", reset))
	o.cookie(b, b.mustSignIn("mallory", reset))
}

func ptrTo[T any](v T) *T { return &v }

// Finding 4: a user created with an email, or a name, in the deployment's
// admin list is a SuperAdmin from its first sign-in, so creating one is
// held to the two-person rule as changing an email to one is.
func TestANewUserInTheAdminListIsHeldToTwoPeople(t *testing.T) {
	cfg := localConfig(t)
	cfg.Admins = []string{"ops@corp.example", "chief"}
	cfg.Local = []auth.LocalUser{{Username: "root", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}}}
	cfg.TwoPerson = twoperson.New(nil, nil)
	e := newEnv(t, cfg)
	root := e.mustSignIn("root", goodPassword)
	for _, in := range []map[string]any{
		{"username": "x", "email": " Ops@Corp.example ", "password": goodPassword},
		{"username": "chief", "password": goodPassword},
	} {
		code, b := e.api(root, http.MethodPost, "/api/v1/admin/users", in)
		if code != http.StatusConflict || !strings.Contains(string(b), "admin list") {
			t.Fatalf("creating %v: %d %s", in["username"], code, b)
		}
		if _, ok := e.svc.Accounts().Find(in["username"].(string)); ok {
			t.Fatalf("%v was created", in["username"])
		}
	}
	if !strings.Contains(e.audit.String(), `"outcome":"denied"`) {
		t.Errorf("the refusal is not audited: %s", e.audit.String())
	}
	if code, b := e.api(root, http.MethodPost, "/api/v1/admin/users", map[string]any{"username": "y", "email": "y@corp.example", "password": goodPassword}); code != http.StatusCreated {
		t.Fatalf("another email: %d %s", code, b)
	}
}

// Finding 5: a reset or End sessions while the session store's list
// cannot be had ends the sessions every replica holds all the same, at
// their next request, and the audit record says what was ended where.
func TestSessionsEndedWhileTheStoreIsAwayEndEverywhere(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.create(a, "ann")
	o.create(a, "bea")
	reread(t, b)
	root := a.mustSignIn("root", goodPassword)
	o.cookie(a, root)
	// Their sessions are rows a has never been shown.
	ann, bea := b.mustSignIn("ann", goodPassword), b.mustSignIn("bea", goodPassword)
	o.cookie(b, ann)
	o.cookie(b, bea)
	*o.now = o.now.Add(time.Minute)
	o.store.Fail(errStoreAway)
	if code, body := a.api(root, http.MethodPatch, "/api/v1/admin/users/ann", map[string]any{"password": "ann's new long password", "currentPassword": goodPassword}); code != http.StatusOK {
		t.Fatalf("the reset: %d %s", code, body)
	}
	if code, body := a.api(root, http.MethodDelete, "/api/v1/admin/users/bea/sessions", nil); code != http.StatusNoContent {
		t.Fatalf("End sessions: %d %s", code, body)
	}
	if !strings.Contains(a.audit.String(), "the session store could not be listed") {
		t.Errorf("the audit record does not say the ending was partial: %s", a.audit.String())
	}
	o.back()
	reread(t, b)
	for name, c := range map[string]*http.Client{"ann": ann, "bea": bea} {
		if _, code := b.whoami(c); code != http.StatusUnauthorized {
			t.Errorf("%s's session after it was ended while the store was away: %d", name, code)
		}
	}
	// Signing in again works.
	o.cookie(b, b.mustSignIn("ann", "ann's new long password"))
	o.cookie(b, b.mustSignIn("bea", goodPassword))
}

// Finding 8: what someone not signed in types, or a provider's error, is
// recorded as plain text: no zero byte, no control character.
func TestATypedUserNameIsRecordedAsPlainText(t *testing.T) {
	e := newEnv(t, outageConfig(t))
	_, resp := e.signIn("x\x00\n", "y")
	readAll(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the sign-in: %s", resp.Status)
	}
	rec := e.audit.String()
	if !strings.Contains(rec, `"action":"signin.failed"`) || strings.Contains(rec, `\u0000`) || strings.Contains(rec, `\n`) {
		t.Fatalf("the record: %s", rec)
	}
}

// countingBody is a request body of n bytes of a form, counting what is
// read of it.
type countingBody struct {
	left, read int
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), b.left)
	for i := range p[:n] {
		p[i] = 'a'
	}
	b.left -= n
	b.read += n
	return n, nil
}

func (b *countingBody) Close() error { return nil }

// Finding 9: a request nobody signed in to make is not read past the
// forms' 64 KiB, whatever page answers it.
func TestABodyBeforeSignInIsReadNoFurtherThanTheFormsLimit(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	t.Cleanup(idp.Close)
	local := newEnv(t, localConfig(t))
	hash := hashOf(t, goodPassword)
	signedIn := newEnv(t, auth.Config{Mode: auth.ModeLocal, Local: []auth.LocalUser{{Username: "root", PasswordHash: hash, Roles: []string{"superAdmin"}}}})
	oidc := newEnv(t, auth.Config{Mode: auth.ModeOIDC, OIDC: auth.OIDCConfig{Issuer: idp.URL, ClientID: "c"}})
	for _, tc := range []struct {
		name string
		e    *env
		path string
	}{
		{"the first-run page", local, "/x"},
		{"the sign-in page", signedIn, "/x"},
		{"the OIDC start", oidc, "/signin/start"},
	} {
		body := &countingBody{left: 8 << 20}
		r := httptest.NewRequest(http.MethodPost, tc.path, nil)
		r.Body = body
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		if next := tc.e.svc.Gate(w, r); next != nil {
			for _, rt := range tc.e.svc.Routes() {
				if rt.Pattern == "POST "+tc.path {
					rt.Handler(w, next)
				}
			}
		}
		if body.read > 64<<10+4096 {
			t.Errorf("%s read %d bytes of the body", tc.name, body.read)
		}
	}
}

// Finding 10: requests nobody signed in to make write a bounded number of
// audit records: the OIDC callback counts against the per-address limit
// before it records anything, and a refusal for too many attempts is
// recorded once per minute for what refused it.
func TestUnauthenticatedRequestsWriteABoundedNumberOfRecords(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	t.Cleanup(idp.Close)
	oidc := newEnv(t, auth.Config{Mode: auth.ModeOIDC, OIDC: auth.OIDCConfig{Issuer: idp.URL, ClientID: "c"}})
	state := base64.RawURLEncoding.EncodeToString([]byte(`{"s":"x","t":9999999999}`))
	for range 100 {
		req, _ := http.NewRequest(http.MethodGet, oidc.app.URL+"/signin/callback?state=x&error="+strings.Repeat("e", 1000), nil)
		req.AddCookie(&http.Cookie{Name: "221b_signin", Value: state})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
	}
	if n := strings.Count(oidc.audit.String(), "\n"); n > 31 {
		t.Errorf("100 callbacks wrote %d audit records", n)
	}
	local := newEnv(t, outageConfig(t))
	c := client(t)
	for i := range 60 {
		local.post(c, "/signin", url.Values{"email": {fmt.Sprintf("guess%d", i)}, "password": {"y"}, "form": {formIn(t, c, local.app.URL+"/signin")}}).Body.Close()
	}
	if n := strings.Count(local.audit.String(), "too many attempts"); n != 1 {
		t.Errorf("60 sign-ins from one address recorded %d refusals for too many attempts", n)
	}
}

// Finding 11: after the sign-in mode is changed, a stored session of the
// old mode is refused, as a signed one is.
func TestASessionOfTheOldSignInModeIsRefused(t *testing.T) {
	o := newOutage(t)
	a := o.replica(outageConfig(t), nil)
	root := a.mustSignIn("root", goodPassword)
	o.cookie(a, root)
	if w, code := a.whoami(root); code != http.StatusOK || !w.Admin {
		t.Fatalf("root before the upgrade: %d %+v", code, w)
	}
	// The release upgraded to OIDC, over the same session store.
	b := o.replica(auth.Config{Mode: auth.ModeOIDC, OIDC: auth.OIDCConfig{Issuer: "https://idp.example", ClientID: "c"}}, nil)
	if w, code := b.whoami(root); code != http.StatusUnauthorized {
		t.Fatalf("root's local session on the OIDC console: %d %+v", code, w)
	}
}

// Finding 12: a local-users file of the wrong shape is an error, not "no
// accounts", which in local mode would open the first-run page to
// whoever comes first.
func TestAMalformedLocalUsersFileIsRefused(t *testing.T) {
	hash := hashOf(t, goodPassword)
	for _, tc := range []struct {
		name, text string
		users      int
		ok         bool
	}{
		{"one account without the leading dash", "email: ops@corp.example\npasswordHash: " + hash + "\n", 0, false},
		{"a list under another key", "accounts:\n- username: ops\n  passwordHash: " + hash + "\n", 0, false},
		{"an empty file", "", 0, false},
		{"a file of comments", "# nobody yet\n", 0, false},
		{"an empty list", "[]\n", 0, true},
		{"an empty users list", "users: []\n", 0, true},
		{"no users, as the chart writes null", "null\n", 0, true},
		{"a list", "- username: ops\n  passwordHash: " + hash + "\n", 1, true},
		{"an object with users", "users:\n- username: ops\n  passwordHash: " + hash + "\n", 1, true},
	} {
		path := filepath.Join(t.TempDir(), "users.yaml")
		if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
			t.Fatal(err)
		}
		users, err := auth.ReadLocalUsers(path)
		if (err == nil) != tc.ok || len(users) != tc.users {
			t.Errorf("%s: %d users, %v", tc.name, len(users), err)
		}
	}
}

// Finding 13: an email changed away from one in the admin list takes the
// SuperAdmin role from the person's open sessions too.
func TestAnEmailChangedAwayFromTheAdminListEndsTheRoleInOpenSessions(t *testing.T) {
	cfg := outageConfig(t)
	cfg.Admins = []string{"boss@corp.example"}
	e := newEnv(t, cfg)
	if _, err := e.svc.Accounts().Create(context.Background(), auth.NewAccount{Username: "alice", Email: "boss@corp.example", Password: goodPassword}); err != nil {
		t.Fatal(err)
	}
	alice := e.mustSignIn("alice", goodPassword)
	if w, _ := e.whoami(alice); !w.Admin {
		t.Fatalf("alice with the listed email: %+v", w)
	}
	if _, _, err := e.svc.Accounts().Update(context.Background(), "alice", auth.Change{Email: ptrTo("alice@other.example")}); err != nil {
		t.Fatal(err)
	}
	if w, code := e.whoami(alice); code != http.StatusOK || w.Admin || strings.Contains(strings.Join(w.KubeGroups, ","), "superadmins") {
		t.Fatalf("alice's open session after the email changed: %d %+v", code, w)
	}
	var me auth.Me
	getJSON(t, alice, e.app.URL+"/api/v1/me", &me)
	if me.User.Email != "alice@other.example" {
		t.Fatalf("/api/v1/me says %q", me.User.Email)
	}
}

// Finding 14: with OIDC and role lists, a SuperAdmin by auth.adminGroups
// is asserted to the clusters as the listed groups and 221b:superadmins
// only: the chart lets the console impersonate those, and Kubernetes
// refuses a request that impersonates any other group.
func TestAnOIDCAdminGroupIsNotAssertedToTheClusters(t *testing.T) {
	cfg := auth.Config{Mode: auth.ModeOIDC, Lists: auth.RoleLists{roles.Viewer: {"devs"}}, AdminGroups: []string{"platform-admins"},
		Prefixes: roles.Prefixes{OIDCUser: "oidc-user:", OIDCGroup: "oidc:"}}
	g := auth.GrantFor(cfg, roles.OIDC, auth.User{Username: "pat@corp.example", Groups: []string{"platform-admins", "devs"}}, nil)
	if !g.Admin() {
		t.Fatalf("not a SuperAdmin: %+v", g)
	}
	if slices.Contains(g.KubeGroups, "oidc:platform-admins") || !slices.Contains(g.KubeGroups, "oidc:devs") || !slices.Contains(g.KubeGroups, "221b:superadmins") {
		t.Fatalf("asserted to the clusters: %v", g.KubeGroups)
	}
}

// Cut by the review's cap (setup.go:177): a session is its account's by
// the account's user name only. The session of an account named by its
// email, taken out of values, does not go on as another account that has
// that email.
func TestASessionIsNotTakenUpByAnotherAccountWithItsNameAsEmail(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	withOps := cfg
	withOps.Local = append(append([]auth.LocalUser(nil), cfg.Local...), auth.LocalUser{Email: "ops@corp.example", PasswordHash: hashOf(t, goodPassword), Roles: []string{"superAdmin"}})
	a := o.replica(withOps, nil)
	ops := a.mustSignIn("ops@corp.example", goodPassword)
	o.cookie(a, ops)
	// The values no longer name ops; a console user has the email.
	b := o.replica(cfg, nil)
	if _, err := b.svc.Accounts().Create(context.Background(), auth.NewAccount{Username: "bob", Email: "ops@corp.example", Password: goodPassword}); err != nil {
		t.Fatal(err)
	}
	if w, code := b.whoami(ops); code != http.StatusUnauthorized {
		t.Fatalf("the session of the account taken out of values: %d %+v", code, w)
	}
}

// Cut by the review's cap (auth.go:726): the per-account limit counts an
// account's attempts by its user name and by its email alike.
func TestTheAccountLimitCountsTheNameAndTheEmailTogether(t *testing.T) {
	e := newEnv(t, outageConfig(t))
	if _, err := e.svc.Accounts().Create(context.Background(), auth.NewAccount{Username: "gina", Email: "gina@corp.example", Password: goodPassword}); err != nil {
		t.Fatal(err)
	}
	codes := []int{}
	for i := range 6 {
		login := "gina"
		if i%2 == 1 {
			login = "GINA@corp.example"
		}
		_, resp := e.signIn(login, "wrong wrong wrong")
		readAll(t, resp)
		codes = append(codes, resp.StatusCode)
	}
	if codes[5] != http.StatusTooManyRequests {
		t.Fatalf("six attempts at one account by name and email: %v", codes)
	}
}

// Cut by the review's cap (auth.go:982-986): a sign-out on a replica that
// cannot look the session up (the store away, and it never held it)
// still ends it: the store is told when it is back.
func TestASignOutWhereTheSessionCannotBeLookedUpStillEndsIt(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	root := a.mustSignIn("root", goodPassword)
	o.cookie(a, root)
	o.store.Fail(errStoreAway)
	signOutOn(t, b, root)
	o.back()
	b.svc.Sessions().StoreIsBack()
	c := o.holding(a, o.cookies[0])
	if _, code := a.whoami(c); code != http.StatusUnauthorized {
		t.Fatalf("the session signed out on the other replica: %d", code)
	}
}

// Cut by the review's cap (accounts.go:163-168): an account defined in
// values hides a console account of the same name wholly: the hidden one
// does not sign in by its email, is not listed twice, and does not count
// as a SuperAdmin.
func TestAnAccountInValuesHidesAConsoleAccountOfItsName(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	cfg.TwoPerson = twoperson.New(nil, nil)
	// Before: kim is a console SuperAdmin with an email.
	a := o.replica(cfg, nil)
	if _, err := a.svc.Accounts().Create(context.Background(), auth.NewAccount{Username: "kim", Email: "kim@corp.example", Password: "kim's old long password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Accounts().SetSuper(context.Background(), "kim", []roles.ID{roles.SuperAdmin}); err != nil {
		t.Fatal(err)
	}
	// Then the values define kim, with another password and no role.
	withKim := cfg
	withKim.Local = append(append([]auth.LocalUser(nil), cfg.Local...), auth.LocalUser{Username: "kim", PasswordHash: hashOf(t, goodPassword)})
	b := o.replica(withKim, nil)
	if _, resp := b.signIn("kim@corp.example", "kim's old long password"); resp.StatusCode != http.StatusUnauthorized {
		readAll(t, resp)
		t.Fatalf("the hidden account's old password signed in by its email: %s", resp.Status)
	}
	n := 0
	for _, u := range b.svc.Accounts().List() {
		if u.Username == "kim" {
			n++
			if !u.Managed {
				t.Errorf("kim is listed as the console's account")
			}
		}
	}
	if n != 1 {
		t.Fatalf("kim is listed %d times", n)
	}
	root := b.mustSignIn("root", goodPassword)
	o.cookie(b, root)
	if b.svc.OtherSuperAdmin(auth.Session{User: auth.User{Subject: "local:root", Username: "root"}, Method: "local"}) {
		t.Fatal("the hidden account counts as another SuperAdmin")
	}
}

// Cut by the review's cap (auth.go:835, setup.go:100): a failure's audit
// record names whom it was about, as typed: a sign-in the directory could
// not answer, and the first-run page's refusals.
func TestFailureRecordsNameWhomTheyAreAbout(t *testing.T) {
	e, dir := ldapEnv(t)
	dir.mu.Lock()
	dir.down = true
	dir.mu.Unlock()
	_, resp := e.signIn("omar", "his password")
	readAll(t, resp)
	if rec := e.audit.String(); !strings.Contains(rec, `"action":"signin.failed"`) || !strings.Contains(rec, `"email":"omar"`) {
		t.Errorf("the directory's outage: %s", rec)
	}
	local := newEnv(t, localConfig(t))
	c := client(t)
	readAll(t, local.post(c, "/setup", url.Values{"form": {formIn(t, c, local.app.URL+"/setup")}, "token": {"wrong"}, "username": {"mikhail"}, "password": {goodPassword}, "again": {goodPassword}}))
	if rec := local.audit.String(); !strings.Contains(rec, `"action":"setup.failed"`) || !strings.Contains(rec, `"email":"mikhail"`) {
		t.Errorf("the first-run page: %s", rec)
	}
}

// Cut by the review's cap (auth.go:354-356): the exchange of a signed
// session does not set a cookie on a static file's answer, which a shared
// cache may keep and hand to others; the page's own requests exchange it.
func TestNoSessionCookieOnAStaticFile(t *testing.T) {
	o := newOutage(t)
	a := o.replica(outageConfig(t), nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	o.back()
	c := o.holding(a, signed)
	resp, err := c.Get(a.app.URL + "/signin.css")
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if len(resp.Header.Values("Set-Cookie")) != 0 || !strings.Contains(resp.Header.Get("Cache-Control"), "public") {
		t.Fatalf("a static file's answer sets %d cookies, Cache-Control %q", len(resp.Header.Values("Set-Cookie")), resp.Header.Get("Cache-Control"))
	}
	if _, code := a.whoami(c); code != http.StatusOK {
		t.Fatalf("the API: %d", code)
	}
}

// From the review's "not confirmed" list (auth.go:279): the admin list
// matches names and emails without regard to ASCII case only; a name a
// provider issues with a character that Unicode folds onto a letter (the
// Kelvin sign onto k) is not the listed one.
func TestTheAdminListMatchesASCIICaseOnly(t *testing.T) {
	cfg := auth.Config{Mode: auth.ModeOIDC, Admins: []string{"kate@corp.example"}}
	if g := auth.GrantFor(cfg, roles.OIDC, auth.User{Username: "Kate@corp.example", Email: "Kate@corp.example"}, nil); g.Admin() {
		t.Fatal("a name with the Kelvin sign is the listed admin")
	}
	if g := auth.GrantFor(cfg, roles.OIDC, auth.User{Username: "Kate@Corp.Example"}, nil); !g.Admin() {
		t.Fatal("the listed admin in other capitals is not")
	}
}

// groupsDir is fakeDir whose sync lists the members of the groups asked
// for, as a directory does.
type groupsDir struct{ *fakeDir }

func (d groupsDir) Members(ctx context.Context, groups []string) ([]auth.Person, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []auth.Person
	for _, p := range d.people {
		for _, g := range p.Groups {
			if slices.Contains(groups, g) {
				out = append(out, *p)
				break
			}
		}
	}
	return out, nil
}

// Cut by the review's cap (admin.go:589, directory.go:114): the directory
// sync reads the members of every group that lets a person in, the admin
// and reporter groups too, so the Users page does not show those people
// as disabled.
func TestTheDirectorySyncKeepsPeopleOfTheAdminAndReporterGroups(t *testing.T) {
	var dir *fakeDir
	e, d := ldapEnvWith(t, func(c *auth.Config) {
		c.AdminGroups = []string{"ops-leads"}
		c.ReporterGroups = []string{"support"}
		dir = c.Directory.(*fakeDir)
		c.Directory = groupsDir{dir}
	})
	d.mu.Lock()
	d.people["sue"] = &auth.Person{Ref: "uid=sue", Username: "sue", Groups: []string{"support"}}
	d.people["lee"] = &auth.Person{Ref: "uid=lee", Username: "lee", Groups: []string{"ops-leads"}}
	d.pw["sue"], d.pw["lee"] = goodPassword, goodPassword
	d.mu.Unlock()
	e.mustSignIn("sue", goodPassword)
	e.mustSignIn("lee", goodPassword)
	root := e.mustSignIn("break-glass", goodPassword)
	if code, b := e.api(root, http.MethodPost, "/api/v1/admin/ldap/refresh", nil); code != http.StatusAccepted {
		t.Fatalf("refresh: %d %s", code, b)
	}
	var view struct {
		Users []auth.UserRow `json:"users"`
		LDAP  struct {
			Running bool `json:"running"`
		} `json:"ldap"`
	}
	waitFor(t, func() bool {
		_, b := e.api(root, http.MethodGet, "/api/v1/admin/users", nil)
		_ = json.Unmarshal(b, &view)
		return !view.LDAP.Running
	})
	for _, u := range view.Users {
		if (u.Username == "sue" || u.Username == "lee") && u.Status != "active" {
			t.Errorf("%s after the sync: %s (%s)", u.Username, u.Status, u.StatusNote)
		}
	}
}

// Cut by the review's cap (auth.go:565-566) and the final sweep: the form
// token stays the same while its cookie lives, so a second sign-in tab
// does not make the first one's form refused, and the cookie outlives a
// page left open over lunch.
func TestTheSignInFormsTokenServesEveryTab(t *testing.T) {
	e := newEnv(t, outageConfig(t))
	c := client(t)
	first := formIn(t, c, e.app.URL+"/signin")
	second := formIn(t, c, e.app.URL+"/signin")
	if first != second {
		t.Fatal("a second sign-in page changed the form's token")
	}
	resp := e.post(c, "/signin", url.Values{"email": {"root"}, "password": {goodPassword}, "form": {first}})
	readAll(t, resp)
	if resp.Request.URL.Path != "/" {
		t.Fatalf("the first tab's form: %s at %s", resp.Status, resp.Request.URL)
	}
	resp, err := client(t).Get(e.app.URL + "/signin")
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	for _, k := range resp.Cookies() {
		if k.Name == "221b_form" && k.MaxAge < 12*3600 {
			t.Fatalf("the form's cookie lives %d seconds", k.MaxAge)
		}
	}
}
