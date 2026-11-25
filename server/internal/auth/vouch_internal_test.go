package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
)

// What a replica vouches for when it forwards a request (vouch.go), and
// what the leader takes of it: the session of that cookie only, and only
// while nothing else can be said of it.
func TestTheLeaderTakesAVouchOnlyForASessionItCannotAskAbout(t *testing.T) {
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := NewOutageStore()
	leader := NewSessions(30*time.Minute, 2*time.Hour, clock)
	leader.Share(store, nil)
	sess := Session{ID: randomID(), User: User{Subject: "s-1", Username: "ann@corp.example", Email: "ann@corp.example", Groups: []string{"web"}, KubeName: "ann@corp.example", KubeGroups: []string{"web", "system:authenticated"}}, Method: string(ModeLocal), Created: now, LastSeen: now, Expires: now.Add(2 * time.Hour)}

	v := VouchFor(sess)
	if v == "" || strings.Contains(v, sess.ID) {
		t.Fatalf("the vouch is empty or carries the cookie's value: %q", v)
	}
	req := func(v string) *http.Request {
		return WithVouch(httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil), v)
	}
	got, ok := vouchedFor(req(v), sess.ID)
	if !ok || got.ID != sess.ID || got.User.KubeName != sess.User.KubeName || len(got.User.KubeGroups) != 2 || got.Method != sess.Method || !got.Expires.Equal(sess.Expires) {
		t.Fatalf("the vouched session: %+v %v", got, ok)
	}
	if _, ok := vouchedFor(req(v), randomID()); ok {
		t.Fatal("a vouch was taken for another cookie")
	}
	for _, bad := range []string{"", "x", keyOf(sess.ID), keyOf(sess.ID) + ".!!", keyOf(sess.ID) + ".e30x", "ab." + strings.SplitN(v, ".", 2)[1]} {
		if _, ok := vouchedFor(req(bad), sess.ID); ok {
			t.Fatalf("a vouch that cannot be read was taken: %q", bad)
		}
	}
	signed := sess
	signed.Signed = true
	if VouchFor(signed) != "" || VouchFor(anonymousAs("viewer")) != "" {
		t.Fatal("a signed session or no session was vouched for")
	}

	// The store answers: it decides, and it holds no such session.
	if _, ok := leader.takeVouched(sess.ID, got); ok {
		t.Fatal("a vouch was taken for a session the store says is gone")
	}
	// The store away, the session not held here: taken.
	store.Fail(fmt.Errorf("reading: %w", db.ErrUnavailable))
	if _, ok := leader.takeVouched(sess.ID, got); !ok {
		t.Fatal("the vouch for a session nothing else can be said of was refused")
	}
	if _, ok := leader.takeVouched(sess.ID, signed); ok {
		t.Fatal("a vouch for a signed session was taken")
	}
	idle := got
	idle.LastSeen = now.Add(-31 * time.Minute)
	if _, ok := leader.takeVouched(sess.ID, idle); ok {
		t.Fatal("a vouch for an idle session was taken")
	}
	// Signed out here during the outage: this replica knows it ended.
	leader.Delete(sess.ID)
	if _, ok := leader.takeVouched(sess.ID, got); ok {
		t.Fatal("a vouch for a session ended here was taken")
	}
}
