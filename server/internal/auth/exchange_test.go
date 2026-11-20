package auth_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
)

// The exchange of a session made while the session store was away never
// gives more than that session had (DB-65), and a signed session that was
// ended is not brought back by it. These are the two scenarios of the
// ninth cluster test's F1, and what follows from the decision taken after
// it, through the handlers: replicas over one store, one key, one clock.

// signOutOn signs the client's session out on a replica.
func signOutOn(t *testing.T, e *env, c *http.Client) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.app.URL+"/signout", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the sign-out answered %s", resp.Status)
	}
}

// latestEnd is the latest time any session the store holds expires; the
// records of ended sessions are not sessions, and are not counted.
func (o *outage) latestEnd() (latest time.Time, sessions int) {
	o.t.Helper()
	for _, b := range o.store.Records() {
		var rec struct {
			Method  string
			Expires time.Time
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			o.t.Fatal(err)
		}
		if rec.Method == "ended" {
			continue
		}
		sessions++
		if rec.Expires.After(latest) {
			latest = rec.Expires
		}
	}
	return latest, sessions
}

// refusedEverywhere fails unless every replica refuses every cookie.
func (o *outage) refusedEverywhere(when string, replicas []*env, cookies map[string]string) {
	o.t.Helper()
	for i, e := range replicas {
		for what, v := range cookies {
			if w, code := e.whoami(o.holding(e, v)); code != http.StatusUnauthorized {
				o.t.Fatalf("%s: replica %d took %s: %d %+v", when, i+1, what, code, w)
			}
		}
	}
}

// The first scenario of the cluster: two replicas, no restart. Signed in
// with the store away on A, signed out on A; the store comes back; the
// signed cookie, kept by someone, is shown to B.
func TestASignedSessionSignedOutDuringTheOutageIsNotExchangedByAnotherReplica(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	signedInAt := *o.now
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	*o.now = o.now.Add(time.Minute)
	signOutOn(t, a, root)
	if _, code := a.whoami(o.holding(a, signed)); code != http.StatusUnauthorized {
		t.Fatalf("where it was signed out: %d", code)
	}
	// While the store is away B cannot be told: the documented bound.
	if _, code := b.whoami(o.holding(b, signed)); code != http.StatusOK {
		t.Fatalf("the replica that cannot be told, during the outage: %d", code)
	}

	// The store is back, and A, which remembers the sign-out, tells it (the
	// database's health calls StoreIsBack; the sweep does the same).
	o.back()
	a.svc.Sessions().StoreIsBack()
	if sessions, endings := o.kept(); sessions != 0 || endings != 1 {
		t.Fatalf("after the store is back it holds %d sessions and %d endings", sessions, endings)
	}
	// B, which never saw the sign-out, refuses the cookie and starts no
	// stored session for it; so does a replica started since.
	c := o.replica(cfg, nil)
	replicas := []*env{b, a, c}
	o.refusedEverywhere("after the store is back", replicas, map[string]string{"the signed-out signed cookie": signed})
	if sessions, endings := o.kept(); sessions != 0 || endings != 1 {
		t.Fatalf("the replay left %d sessions and %d endings", sessions, endings)
	}
	for i, e := range replicas {
		if strings.Contains(e.audit.String(), `"action":"session.exchange"`) {
			t.Fatalf("replica %d recorded an exchange of a signed-out session", i+1)
		}
	}
	// And never after the signed session's own end, ending kept or not.
	*o.now = signedInAt.Add(30*time.Minute + time.Second)
	for _, e := range replicas {
		e.svc.Sweep()
	}
	o.refusedEverywhere("after the half hour", replicas, map[string]string{"the signed cookie": signed})
	if o.store.Len() != 0 {
		t.Fatalf("after the half hour the store still holds %d records", o.store.Len())
	}
}

// The same when B is shown the cookie before A has told the store: B may
// exchange it, as the bound of a signed session allows, and the stored
// session ends on every replica as soon as A tells the store, and in any
// case when the signed session would have.
func TestASignOutToldAfterTheExchangeEndsTheStoredSessionToo(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	signedInAt := *o.now
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	signOutOn(t, a, root)
	o.back()

	thief := o.holding(b, signed)
	if _, code := b.whoami(thief); code != http.StatusOK {
		t.Fatalf("the replica that was not told yet: %d", code)
	}
	stored := o.cookie(b, thief)
	if len(stored) != 43 {
		t.Fatalf("the cookie after the exchange has %d characters", len(stored))
	}
	if latest, n := o.latestEnd(); n != 1 || latest.After(signedInAt.Add(30*time.Minute)) {
		t.Fatalf("%d stored sessions, the latest ending %s; the signed session ends %s", n, latest, signedInAt.Add(30*time.Minute))
	}
	if n := strings.Count(b.audit.String(), `"action":"session.exchange"`); n != 1 {
		t.Fatalf("%d audit records of the exchange", n)
	}
	// A refuses both already: it remembers the sign-out.
	o.refusedEverywhere("on the replica that signed it out", []*env{a}, map[string]string{"the signed cookie": signed, "the stored cookie": stored})
	// A tells the store, at its next sweep: both cookies end everywhere.
	a.svc.Sweep()
	if sessions, endings := o.kept(); sessions != 0 || endings != 1 {
		t.Fatalf("after the sweep the store holds %d sessions and %d endings", sessions, endings)
	}
	o.refusedEverywhere("after the store was told", []*env{a, b, o.replica(cfg, nil)}, map[string]string{"the signed cookie": signed, "the stored cookie": stored})
	if sessions, _ := o.kept(); sessions != 0 {
		t.Fatalf("the replay started %d sessions", sessions)
	}
}

// The second scenario of the cluster: one replica, started again. What it
// remembered of the endings is gone with it. An ending the store was told
// of is found there; one it was never told of is lost, and the session
// then lasts what a signed session lasts and no longer.
func TestAReplicaStartedAgainNeverGivesAnEndedSessionMoreThanItsHalfHour(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a := o.replica(cfg, nil)
	o.create(a, "dave", "web-readers")
	o.create(a, "erin", "web-readers")
	o.store.Fail(errStoreAway)
	signedInAt := *o.now
	// dave signs in and out during the outage; erin signs in, and signs
	// out once the store is back, of the stored session she was given.
	dave := o.signInAway(a, "dave", goodPassword)
	daveSigned := o.cookie(a, dave)
	erin := o.signInAway(a, "erin", goodPassword)
	erinSigned := o.cookie(a, erin)
	signOutOn(t, a, dave)

	// The replica is started again before the store is back: nothing
	// remembers dave's sign-out.
	a2 := o.replica(cfg, nil)
	o.back()
	if _, code := a2.whoami(erin); code != http.StatusOK {
		t.Fatalf("erin after the store is back: %d", code)
	}
	erinStored := o.cookie(a2, erin)
	if erinStored == erinSigned || len(erinStored) != 43 {
		t.Fatal("erin's session was not exchanged")
	}
	signOutOn(t, a2, erin)
	// Started again, once more: erin's sign-out is in the store.
	a3 := o.replica(cfg, nil)
	o.refusedEverywhere("after a sign-out of the exchanged session and a restart", []*env{a3, a2}, map[string]string{"erin's signed cookie": erinSigned, "erin's stored cookie": erinStored})
	if latest, n := o.latestEnd(); n != 0 {
		t.Fatalf("erin's signed cookie became %d sessions, ending %s", n, latest)
	}

	// dave's sign-out was never told to anything that lasted. His cookie is
	// taken, as a signed session's is until it expires, and exchanged; the
	// stored session ends when the signed one would have. It is used every
	// few minutes, on both replicas, with the sweep running: nothing moves
	// its end later.
	end := signedInAt.Add(30 * time.Minute)
	thief := o.holding(a3, daveSigned)
	if _, code := a3.whoami(thief); code != http.StatusOK {
		t.Fatalf("a signed cookie whose sign-out no replica remembers, before it expires: %d", code)
	}
	daveStored := o.cookie(a3, thief)
	uses := 0
	for o.now.Before(end.Add(-4 * time.Minute)) {
		*o.now = o.now.Add(4 * time.Minute)
		for _, e := range []*env{a2, a3} {
			if _, code := e.whoami(thief); code != http.StatusOK {
				t.Fatalf("%s before the end: %d", end.Sub(*o.now), code)
			}
			uses++
			e.svc.Sweep()
		}
		if latest, n := o.latestEnd(); n != 1 || latest.After(end) {
			t.Fatalf("%d sessions, the latest ending %s; the signed session ends %s", n, latest, end)
		}
	}
	if uses < 10 {
		t.Fatalf("the session was used %d times", uses)
	}
	*o.now = end.Add(time.Second)
	o.refusedEverywhere("after the half hour, used until the last minutes", []*env{a2, a3, o.replica(cfg, nil)}, map[string]string{"dave's signed cookie": daveSigned, "dave's stored cookie": daveStored})
	// Long after, too.
	*o.now = o.now.Add(3 * 24 * time.Hour)
	for _, e := range []*env{a2, a3} {
		e.svc.Sweep()
	}
	o.refusedEverywhere("days later", []*env{a2, a3}, map[string]string{"dave's signed cookie": daveSigned, "dave's stored cookie": daveStored})
	if o.store.Len() != 0 {
		t.Fatalf("days later the store holds %d records", o.store.Len())
	}
}

// "Sign out everywhere" (and what ends a person's sessions the same way: a
// password reset, a removal) during the outage: the signed sessions the
// replica holds are ended there, the store is told when it is back, and no
// replica takes or exchanges them after.
func TestSignOutEverywhereOfASignedSessionHoldsWhenTheStoreIsBack(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.create(a, "dave", "web-readers")
	reread(t, b)
	root := a.mustSignIn("root", goodPassword)
	o.cookie(a, root)
	if _, code := b.whoami(root); code != http.StatusOK {
		t.Fatal("the administrator is not signed in on the other replica")
	}
	o.store.Fail(errStoreAway)
	signedInAt := *o.now
	dave := o.signInAway(b, "dave", goodPassword)
	signed := o.cookie(b, dave)
	// A has seen dave's session, so it can end it.
	if _, code := a.whoami(dave); code != http.StatusOK {
		t.Fatal("dave is not signed in on the other replica")
	}
	if code, body := a.api(root, http.MethodDelete, "/api/v1/admin/users/dave/sessions", nil); code != http.StatusNoContent {
		t.Fatalf("ending dave's sessions: %d %s", code, body)
	}
	if _, code := a.whoami(o.holding(a, signed)); code != http.StatusUnauthorized {
		t.Fatalf("where the sessions were ended: %d", code)
	}
	o.back()
	a.svc.Sessions().StoreIsBack()
	replicas := []*env{b, a, o.replica(cfg, nil)}
	o.refusedEverywhere("after the store is back", replicas, map[string]string{"the ended signed cookie": signed})
	if latest, n := o.latestEnd(); n != 1 {
		// The administrator's own session, and no other.
		t.Fatalf("%d sessions in the store, the latest ending %s", n, latest)
	}
	*o.now = signedInAt.Add(30*time.Minute + time.Second)
	o.refusedEverywhere("after the half hour", replicas, map[string]string{"the signed cookie": signed})

	// With the store answering, a stored session that stood for a signed
	// one is ended the same way, and the signed cookie with it.
	o.store.Fail(errStoreAway)
	erinAt := *o.now
	o.create(a, "erin", "web-readers")
	reread(t, b)
	erin := o.signInAway(b, "erin", goodPassword)
	erinSigned := o.cookie(b, erin)
	o.back()
	if _, code := b.whoami(erin); code != http.StatusOK {
		t.Fatal("erin is not signed in")
	}
	erinStored := o.cookie(b, erin)
	if code, body := a.api(o.holding(a, sessionID(t, a, root)), http.MethodDelete, "/api/v1/admin/users/erin/sessions", nil); code != http.StatusNoContent {
		t.Fatalf("ending erin's sessions: %d %s", code, body)
	}
	o.refusedEverywhere("after the exchanged session was ended", []*env{a, b, o.replica(cfg, nil)}, map[string]string{"erin's signed cookie": erinSigned, "erin's stored cookie": erinStored})
	_ = erinAt
}

// One sign-in has one stored session, whichever replicas are shown its
// signed cookie and however often: the cluster saw a second one started
// when the signed cookie was shown again after its exchange.
func TestASignedCookieShownToEveryReplicaComesToOneStoredSession(t *testing.T) {
	o := newOutage(t)
	cfg := outageConfig(t)
	a, b := o.replica(cfg, nil), o.replica(cfg, nil)
	o.store.Fail(errStoreAway)
	root := o.signInAway(a, "root", goodPassword)
	signed := o.cookie(a, root)
	o.back()
	seen := map[string]bool{}
	for _, e := range []*env{a, b, o.replica(cfg, nil), a, b} {
		c := o.holding(e, signed)
		if _, code := e.whoami(c); code != http.StatusOK {
			t.Fatalf("the signed cookie after the store is back: %d", code)
		}
		seen[o.cookie(e, c)] = true
	}
	if _, n := o.latestEnd(); n != 1 || len(seen) != 1 {
		t.Fatalf("one sign-in became %d stored sessions and %d cookies", n, len(seen))
	}
	// The browser is told to keep the new cookie no longer than the
	// session lasts.
	c := o.holding(b, signed)
	req, _ := http.NewRequest(http.MethodGet, b.app.URL+"/api/v1/snapshot", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, k := range resp.Cookies() {
		if k.Name == "221b_session" && (k.MaxAge <= 0 || k.MaxAge > 30*60) {
			t.Fatalf("the exchanged session's cookie is kept for %d seconds", k.MaxAge)
		}
	}
	var _ = auth.SessionHeldFor
}
