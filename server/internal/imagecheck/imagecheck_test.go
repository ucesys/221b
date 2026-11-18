package imagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRegistry serves a tag list behind an anonymous bearer token, as
// ghcr.io and Docker Hub do, in two pages, and a manifest's digest.
type fakeRegistry struct {
	srv      *httptest.Server
	tagAsks  atomic.Int32
	tokens   atomic.Int32
	private  bool
	sawCreds atomic.Bool
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokens.Add(1)
		if r.Header.Get("Authorization") != "" {
			f.sawCreds.Store(true)
		}
		if r.URL.Query().Get("scope") != "repository:team/app:pull" || r.URL.Query().Get("service") != "fake" {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon-token"})
	})
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if f.private || r.Header.Get("Authorization") != "Bearer anon-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+f.srv.URL+`/token",service="fake",scope="repository:team/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/v2/team/app/tags/list", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		f.tagAsks.Add(1)
		if r.URL.Query().Get("last") == "" {
			w.Header().Set("Link", `</v2/team/app/tags/list?last=1.2.4&n=1000>; rel="next"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "team/app", "tags": []string{"1.2.3", "1.2.4"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "team/app", "tags": []string{"1.3.0", "2.0.0-rc.1", "latest"}})
	})
	mux.HandleFunc("/v2/team/app/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("b", 64))
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) host() string { return f.srv.Listener.Addr().String() }

func (f *fakeRegistry) checker(now *time.Time) *Checker {
	return New(Options{Client: &Client{HTTP: f.srv.Client()}, Now: func() time.Time { return *now }})
}

// settle asks until the check is no longer pending.
func settle(t *testing.T, check func() Result) Result {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := check()
		if r.State != StatePending {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("the check stayed pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTagsThroughAnAnonymousToken(t *testing.T) {
	f := newFakeRegistry(t)
	c := &Client{HTTP: f.srv.Client()}
	tags, err := c.Tags(context.Background(), f.host(), "team/app")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "1.2.3,1.2.4,1.3.0,2.0.0-rc.1,latest" {
		t.Fatalf("tags %v", tags)
	}
	if f.tokens.Load() != 1 {
		t.Fatalf("asked for %d tokens; the second page reuses the first", f.tokens.Load())
	}
	if f.sawCreds.Load() {
		t.Fatal("the token was asked for with credentials")
	}
}

func TestACheckDoesNotBlockAndIsKept(t *testing.T) {
	f := newFakeRegistry(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	c := f.checker(&now)
	if r := c.Check(f.host(), "team/app", "1.2.3", ""); r.State != StatePending {
		t.Fatalf("first look: %+v", r)
	}
	r := settle(t, func() Result { return c.Check(f.host(), "team/app", "1.2.3", "") })
	if r.State != StateNewer || r.Newer != "1.3.0" || r.Checked == nil {
		t.Fatalf("got %+v", r)
	}
	// Another tag of the same repository is answered from what is kept.
	if r := c.Check(f.host(), "team/app", "1.3.0", ""); r.State != StateCurrent {
		t.Fatalf("1.3.0: %+v", r)
	}
	if n := f.tagAsks.Load(); n != 2 {
		t.Fatalf("the tag list was read %d times, want 2 pages once", n)
	}
	// Six hours on, the registry is asked again in the background, and
	// what was kept is said meanwhile.
	now = now.Add(7 * time.Hour)
	if r := c.Check(f.host(), "team/app", "1.2.3", ""); r.State != StateNewer {
		t.Fatalf("while refreshing: %+v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.tagAsks.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.tagAsks.Load() != 4 {
		t.Fatal("the old answer was not refreshed")
	}
}

func TestATagThatMovedIsNewer(t *testing.T) {
	f := newFakeRegistry(t)
	now := time.Now()
	c := f.checker(&now)
	old := "sha256:" + strings.Repeat("a", 64)
	r := settle(t, func() Result { return c.Check(f.host(), "team/app", "latest", old) })
	if r.State != StateNewer || r.Newer != "latest" || r.Digest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("got %+v", r)
	}
	r = settle(t, func() Result { return c.Check(f.host(), "team/app", "latest", "sha256:"+strings.Repeat("b", 64)) })
	if r.State != StateCurrent {
		t.Fatalf("same digest: %+v", r)
	}
	for _, x := range []struct{ tag, digest string }{{"latest", ""}, {"", old}, {"main", ""}} {
		if r := c.Check(f.host(), "team/app", x.tag, x.digest); r.State != StateSkipped || r.Reason == "" {
			t.Fatalf("%q@%q: %+v", x.tag, x.digest, r)
		}
	}
}

func TestAPrivateRepositoryIsUnknown(t *testing.T) {
	f := newFakeRegistry(t)
	f.private = true
	now := time.Now()
	c := f.checker(&now)
	r := settle(t, func() Result { return c.Check(f.host(), "team/app", "1.2.3", "") })
	if r.State != StateUnknown || r.Reason != ErrCredentials.Error() {
		t.Fatalf("got %+v", r)
	}
}

func TestAnUnreachableRegistryIsUnknownAndAskedAgainLater(t *testing.T) {
	// A port nothing listens on: the air-gapped cluster's case.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := l.Addr().String()
	l.Close()
	var asks atomic.Int32
	now := time.Now()
	c := New(Options{
		Client: &Client{HTTP: &http.Client{Timeout: 2 * time.Second, Transport: countingTransport{&asks, http.DefaultTransport}}},
		Now:    func() time.Time { return now },
	})
	r := settle(t, func() Result { return c.Check(host, "team/app", "1.2.3", "") })
	if r.State != StateUnknown || !strings.Contains(r.Reason, "could not be reached") {
		t.Fatalf("got %+v", r)
	}
	n := asks.Load()
	c.Check(host, "team/app", "1.2.3", "")
	time.Sleep(20 * time.Millisecond)
	if asks.Load() != n {
		t.Fatal("a failed registry was asked again at once")
	}
	now = now.Add(2 * time.Hour)
	c.Check(host, "team/app", "1.2.3", "")
	deadline := time.Now().Add(5 * time.Second)
	for asks.Load() == n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if asks.Load() == n {
		t.Fatal("not asked again after an hour")
	}
}

func TestAStandbyAsksNothing(t *testing.T) {
	f := newFakeRegistry(t)
	c := New(Options{Client: &Client{HTTP: f.srv.Client()}, Active: func() bool { return false }})
	c.Check(f.host(), "team/app", "1.2.3", "")
	time.Sleep(20 * time.Millisecond)
	if r := c.Check(f.host(), "team/app", "1.2.3", ""); r.State != StatePending || f.tagAsks.Load() != 0 {
		t.Fatalf("a standby asked: %+v, %d", r, f.tagAsks.Load())
	}
}

func TestATokenRealmMustBeHTTPS(t *testing.T) {
	c := &Client{}
	_, err := c.token(context.Background(), `Bearer realm="http://auth.example.com/token"`, "a/b")
	if err == nil || errors.Is(err, ErrCredentials) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.token(context.Background(), `Basic realm="registry"`, "a/b"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("basic: %v", err)
	}
}

type countingTransport struct {
	n    *atomic.Int32
	next http.RoundTripper
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}
