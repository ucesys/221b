package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/settings"
)

// testLeader is a follower's view of the election.
type testLeader struct {
	mu   sync.Mutex
	lead bool
	addr string
}

func (l *testLeader) IsLeader() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.lead }
func (l *testLeader) LeaderAddr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.addr
}
func (l *testLeader) set(addr string) { l.mu.Lock(); l.addr = addr; l.mu.Unlock() }

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLeaderOnlyPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/sherlock":                           true,
		"/api/v1/sherlock/events":                    true,
		"/api/v1/sherlock/config":                    true,
		"/api/v1/sherlock/key":                       true,
		"/api/v1/watson/commands/abc/approve":        true,
		"/api/v1/watson/config":                      true,
		"/api/v1/assistants/events":                  true,
		"/api/v1/clusters/staging/watson/events":     true,
		"/api/v1/clusters/staging/sherlock/ui/7":     true,
		"/api/v1/clusters/staging/assistants/events": true,
		// The data is the leader's too (ADR-70): it alone reads the clusters.
		"/api/v1/clusters":                            true,
		"/api/v1/clusters/test":                       true,
		"/api/v1/clusters/staging/test":               true,
		"/api/v1/clusters/staging/events":             true,
		"/api/v1/events":                              true,
		"/api/v1/settings":                            true,
		"/api/v1/home":                                true,
		"/api/v1/helm/shop/web":                       true,
		"/api/v1/clusters/sherlock":                   true,
		"/api/v1/clusters/watson/nodes/watson-worker": true,
		// What a replica does itself: sign people in, and keep the accounts.
		"/api/v1/me":                       false,
		"/api/v1/admin/users":              false,
		"/api/v1/admin/users/pat/sessions": false,
		"/api/v1/admin/ldap/refresh":       false,
		"/signin":                          false,
		"/setup":                           false,
		"/healthz":                         false,
		"/metrics":                         false,
	} {
		if got := leaderOnly(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

// A follower sends the leader's requests to it: a stream event by event,
// a POST with its body, the browser's Host and cookie, and who sent it;
// the data too, since the leader alone reads the clusters (ADR-70). The
// session and the probes it answers itself.
func TestFollowerForwardsToTheLeader(t *testing.T) {
	type seen struct{ path, host, cookie, client, token, body string }
	got := make(chan seen, 10)
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c, _ := r.Cookie("221b_session")
		cookie := ""
		if c != nil {
			cookie = c.Value
		}
		got <- seen{r.URL.Path, r.Host, cookie, r.Header.Get(clientHeader), r.Header.Get(proxyHeader), string(b)}
		w.Header().Set("Content-Security-Policy", "from the leader")
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: first\ndata: 1\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer leaderSrv.Close()

	lead := &testLeader{addr: strings.TrimPrefix(leaderSrv.URL, "http://")}
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	follower := httptest.NewServer(New(Options{Settings: shared, Leader: lead, ProxyToken: "s3cret", AllowedHosts: []string{"console.example"}, Logger: quietLog()}))
	defer follower.Close()

	// A stream: its first event arrives while the leader still holds it open.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, follower.URL+"/api/v1/clusters/staging/watson/events", nil)
	req.Host = "console.example:8443"
	req.AddCookie(&http.Cookie{Name: "221b_session", Value: "abc"})
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(r.Body).ReadString('\n')
	if line != "event: first\n" {
		t.Fatalf("stream gave %q", line)
	}
	if csp := r.Header.Values("Content-Security-Policy"); len(csp) != 1 || csp[0] != "from the leader" {
		t.Fatalf("security headers %q", csp)
	}
	s := <-got
	if s.path != "/api/v1/clusters/staging/watson/events" || s.host != "console.example:8443" || s.cookie != "abc" || s.client != "127.0.0.1" || s.token != "s3cret" {
		t.Fatalf("the leader saw %+v", s)
	}
	r.Body.Close()

	// A POST, body and status as the leader sent them.
	req, _ = http.NewRequest(http.MethodPost, follower.URL+"/api/v1/watson/commands", strings.NewReader(`{"command":"kubectl get nodes"}`))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusAccepted || string(b) != `{"ok":true}` {
		t.Fatalf("POST %d %s", r.StatusCode, b)
	}
	if s := <-got; s.body != `{"command":"kubectl get nodes"}` {
		t.Fatalf("the leader got body %q", s.body)
	}

	// The data goes to the leader too (ADR-70): a release's details, a
	// connection's pod, the connections themselves.
	for _, p := range []string{"/api/v1/helm/shop/web", "/api/v1/clusters/staging/pods/shop/web", "/api/v1/clusters"} {
		r = get(t, follower.URL+p)
		if r.StatusCode != http.StatusAccepted {
			t.Fatalf("%s through the follower: %d, not the leader's answer", p, r.StatusCode)
		}
		if s := <-got; s.path != p {
			t.Fatalf("the leader saw %s for %s", s.path, p)
		}
	}
	// What is the replica's own stays with it: the session (no sign-in
	// here, so its own 404 rather than the leader's 202) and the probes.
	r = get(t, follower.URL+"/api/v1/me")
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("me %d", r.StatusCode)
	}
	if r = get(t, follower.URL+"/healthz"); r.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", r.StatusCode)
	}
	select {
	case s := <-got:
		t.Fatalf("forwarded %s", s.path)
	default:
	}

	// No leader yet, or one that does not answer: 503, which the app
	// retries.
	lead.set("")
	r = get(t, follower.URL+"/api/v1/sherlock/config")
	if r.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body(t, r), "moving to another replica") {
		t.Fatalf("no leader: %d", r.StatusCode)
	}
	lead.set("127.0.0.1:1")
	r = get(t, follower.URL+"/api/v1/sherlock/config")
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("leader gone: %d", r.StatusCode)
	}
}

// When another replica becomes the leader, a forwarded stream ends, so the
// browser reconnects to the new one.
func TestForwardedStreamEndsWhenTheLeaderMoves(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: first\ndata: 1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer leaderSrv.Close()
	lead := &testLeader{addr: strings.TrimPrefix(leaderSrv.URL, "http://")}
	api := New(Options{Leader: lead, ProxyToken: "t", Logger: quietLog()})
	api.leaderChecks = 20 * time.Millisecond
	follower := httptest.NewServer(api)
	defer follower.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, follower.URL+"/api/v1/assistants/events", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	br := bufio.NewReader(r.Body)
	if line, _ := br.ReadString('\n'); line != "event: first\n" {
		t.Fatalf("stream gave %q", line)
	}
	lead.set("10.0.0.9:8080")
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, br); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the move")
	}
}

// While the leader changes, two replicas can each take the other for it: a
// request one forwarded is answered 503, never sent back, and a leader that
// moves before it answers gives 503 too, never an empty 200.
func TestAHandoverAnswersRetry(t *testing.T) {
	hits := make(chan string, 10)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.URL.Path
		<-r.Context().Done()
	}))
	defer slow.Close()
	lead := &testLeader{addr: strings.TrimPrefix(slow.URL, "http://")}
	api := New(Options{Leader: lead, ProxyToken: "t", Logger: quietLog()})
	api.leaderChecks = 20 * time.Millisecond
	follower := httptest.NewServer(api)
	defer follower.Close()

	req, _ := http.NewRequest(http.MethodGet, follower.URL+"/api/v1/sherlock/config", nil)
	req.Header.Set(clientHeader, "203.0.113.9")
	req.Header.Set(proxyHeader, "t")
	// Before the fix, the request went on to the leader and hung there.
	client := &http.Client{Timeout: 5 * time.Second}
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusServiceUnavailable || r.Header.Get("Retry-After") == "" {
		t.Fatalf("a forwarded request: %d", r.StatusCode)
	}
	r.Body.Close()
	select {
	case p := <-hits:
		t.Fatalf("forwarded again: %s", p)
	default:
	}

	go func() {
		<-hits
		lead.set("10.0.0.9:8080")
	}()
	r = get(t, follower.URL+"/api/v1/sherlock/config")
	if r.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body(t, r), "moving to another replica") {
		t.Fatalf("the leader moved: %d", r.StatusCode)
	}
}

// The leader believes a forwarded client address only with the replicas'
// token; a browser that sends the header is ignored.
func TestClientAddressNeedsTheToken(t *testing.T) {
	out := &lockedBuffer{}
	log := audit.New(audit.Options{Out: out, Logger: quietLog()})
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	srv := httptest.NewServer(New(Options{Settings: shared, Audit: log, ProxyToken: "s3cret", Logger: quietLog()}))
	defer srv.Close()

	put := func(token string) string {
		before := len(out.String())
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/settings", strings.NewReader(`{"refreshSeconds":5}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set(clientHeader, "203.0.113.9")
		if token != "" {
			req.Header.Set(proxyHeader, token)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("PUT %d", r.StatusCode)
		}
		var ev struct {
			From string `json:"from"`
		}
		line := strings.TrimSpace(out.String()[before:])
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("audit %q: %v", line, err)
		}
		return ev.From
	}
	if from := put("s3cret"); from != "203.0.113.9" {
		t.Fatalf("with the token: from %q", from)
	}
	if from := put("guess"); from != "127.0.0.1" {
		t.Fatalf("with a wrong token: from %q", from)
	}
	if from := put(""); from != "127.0.0.1" {
		t.Fatalf("without a token: from %q", from)
	}
}
