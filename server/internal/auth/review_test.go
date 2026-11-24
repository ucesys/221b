package auth_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
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
