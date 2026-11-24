package auth_test

import (
	"context"
	"net/http"
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
