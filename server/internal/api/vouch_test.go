package api

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/settings"
)

// awayStore is a shared session store that can be taken away: every call
// then fails as a database that cannot be reached does (DB-65).
type awayStore struct {
	mu   sync.Mutex
	recs map[string][]byte
	away bool
}

func (s *awayStore) setAway(away bool) { s.mu.Lock(); s.away = away; s.mu.Unlock() }

func (s *awayStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.away {
		return nil, false, auth.ErrStoreAway
	}
	b, ok := s.recs[key]
	return b, ok, nil
}

func (s *awayStore) Put(_ context.Context, key string, b []byte, replace func([]byte) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.away {
		return false, auth.ErrStoreAway
	}
	if !replace(s.recs[key]) {
		return false, nil
	}
	s.recs[key] = append([]byte(nil), b...)
	return true, nil
}

func (s *awayStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.away {
		return auth.ErrStoreAway
	}
	delete(s.recs, key)
	return nil
}

func (s *awayStore) List(context.Context) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.away {
		return nil, auth.ErrStoreAway
	}
	return maps.Clone(s.recs), nil
}

// The review of November 23, 2026 (session.go:663-666): while the session
// store is away, a person whose session only the follower holds was sent
// to sign in by the leader, which had never seen the session and could
// not ask about it; the follower, which knows it, then sent them back to
// the console, and round it went. The follower now vouches for the
// session it checked when it forwards the request, with the replicas'
// token, and the leader takes that only for a session it cannot ask
// about. A session no replica holds is still asked to sign in again.
func TestAFollowersSessionIsTakenByTheLeaderWhileTheStoreIsAway(t *testing.T) {
	store := &awayStore{recs: map[string][]byte{}}
	hash, _ := auth.HashPassword("correct horse battery staple")
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	replica := func() *auth.Service {
		svc, err := auth.New(auth.Config{
			Mode: auth.ModeLocal,
			Local: []auth.LocalUser{
				{Email: "ops@corp.example", Name: "Ops", PasswordHash: hash},
				{Email: "viewer@corp.example", Name: "Viewer", PasswordHash: hash},
			},
			Admins:       []string{"ops@corp.example"},
			SessionStore: store,
		}, quietLog(), nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shared := settings.NewService(ctx, &settings.Memory{}, settings.Default, quietLog(), nil)
	const token = "s3cret"
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app:"+r.URL.Path) })

	// The leader; what the follower sends it is kept, to be replayed.
	var vouch atomic.Value
	vouch.Store("")
	leaderAPI := New(Options{Settings: shared, Auth: replica(), Leader: &testLeader{lead: true}, ProxyToken: token, UI: ui, Logger: quietLog()})
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("X-221B-Session"); v != "" {
			vouch.Store(v)
		}
		leaderAPI.ServeHTTP(w, r)
	}))
	defer leaderSrv.Close()
	follower := httptest.NewServer(New(Options{Settings: shared, Auth: replica(), Leader: &testLeader{addr: strings.TrimPrefix(leaderSrv.URL, "http://")}, ProxyToken: token, UI: ui, Logger: quietLog()}))
	defer follower.Close()
	// A third replica, outside the chain, signs in someone neither of the
	// two ever sees.
	other := httptest.NewServer(New(Options{Settings: shared, Auth: replica(), Leader: &testLeader{lead: true}, ProxyToken: token, UI: ui, Logger: quietLog()}))
	defer other.Close()

	const leaderPath = "/api/v1/database"
	ops := signIn(t, follower, "ops@corp.example")
	viewer := signIn(t, other, "viewer@corp.example")
	// Before the outage ops uses only what the follower answers itself:
	// the leader never sees the session.
	if r := getWith(t, ops, follower.URL+"/api/v1/me", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("before the outage, on the follower: %d", r.StatusCode)
	}
	cookie := sessionCookie(t, ops, follower.URL)

	store.setAway(true)
	// The follower holds ops's session; the leader has never seen it and
	// cannot ask. Request after request, the leader's answer comes back,
	// not a 401 that sends the browser to sign in.
	for range 3 {
		r := getWith(t, ops, follower.URL+leaderPath, nil)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("a session the follower holds, forwarded to the leader while the store is away: %d %s", r.StatusCode, body(t, r))
		}
		r.Body.Close()
	}
	if r := getWith(t, ops, follower.URL+"/api/v1/me", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("me on the follower: %d", r.StatusCode)
	}
	// The other half of the loop: the follower's sign-in page sends a
	// person it knows back to the console, so what the leader answers
	// decides whether the browser goes round.
	noFollow := &http.Client{Jar: ops.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if r := getWith(t, noFollow, follower.URL+"/signin?next=/", nil); r.StatusCode != http.StatusFound {
		t.Fatalf("the sign-in page on the follower: %d", r.StatusCode)
	}
	// Nobody holds viewer's session but the store: asked to sign in again
	// (TestSessionsThroughAnOutageOfTheDatabase).
	if r := getWith(t, viewer, follower.URL+leaderPath, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a session no replica of the two holds: %d", r.StatusCode)
	}

	// Nothing a browser sends makes the leader take a session: not the
	// follower's vouch without the token, nor with another token, nor for
	// another cookie with the token.
	v, _ := vouch.Load().(string)
	if v == "" {
		t.Fatal("the follower vouched for nothing")
	}
	if strings.Contains(v, cookie) {
		t.Fatal("the vouch carries the cookie's value")
	}
	direct := func(cookie string, hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodGet, leaderSrv.URL+leaderPath, nil)
		req.AddCookie(&http.Cookie{Name: "221b_session", Value: cookie})
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		return r.StatusCode
	}
	if code := direct(cookie, map[string]string{"X-221B-Session": v}); code != http.StatusUnauthorized {
		t.Fatalf("a vouch without the token: %d", code)
	}
	if code := direct(cookie, map[string]string{"X-221B-Session": v, "X-221B-Client": "10.0.0.9", "X-221B-Proxy": "guess"}); code != http.StatusUnauthorized {
		t.Fatalf("a vouch with another token: %d", code)
	}
	if code := direct(strings.Repeat("A", 43), map[string]string{"X-221B-Session": v, "X-221B-Client": "10.0.0.9", "X-221B-Proxy": token}); code != http.StatusUnauthorized {
		t.Fatalf("a vouch for another session: %d", code)
	}
	if code := direct(cookie, map[string]string{"X-221B-Session": v, "X-221B-Client": "10.0.0.9", "X-221B-Proxy": token}); code != http.StatusOK {
		t.Fatalf("the follower's own request: %d", code)
	}

	// With the store back the store decides: a session it no longer holds
	// is refused, vouched or not.
	// Every call asks the store again (DB-65); the clock moves on all the
	// same.
	store.setAway(false)
	clockMu.Lock()
	now = now.Add(11 * time.Second)
	clockMu.Unlock()
	if code := direct(cookie, map[string]string{"X-221B-Session": v, "X-221B-Client": "10.0.0.9", "X-221B-Proxy": token}); code != http.StatusOK {
		t.Fatalf("the store back, the session in it: %d", code)
	}
	store.mu.Lock()
	clear(store.recs)
	store.mu.Unlock()
	if code := direct(cookie, map[string]string{"X-221B-Session": v, "X-221B-Client": "10.0.0.9", "X-221B-Proxy": token}); code != http.StatusUnauthorized {
		t.Fatalf("a vouch for a session the store no longer holds: %d", code)
	}
}

// getWith sends a GET with a client and extra headers.
func getWith(t *testing.T, c *http.Client, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sessionCookie is the session cookie a client holds for a server.
func sessionCookie(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base, nil)
	for _, k := range c.Jar.Cookies(req.URL) {
		if k.Name == "221b_session" {
			return k.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}
