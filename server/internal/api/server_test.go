package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/settings"
)

type source struct {
	mu    sync.Mutex
	nodes map[string]*corev1.Node
}

func (s *source) Info() kube.Info {
	return kube.Info{Name: "test", State: kube.StateConnected, Synced: true, Warnings: []string{}}
}
func (s *source) NodeNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.nodes {
		out = append(out, n)
	}
	return out
}
func (s *source) Node(name string) *corev1.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodes[name]
}
func (s *source) PodsOn(string) []*corev1.Pod       { return nil }
func (s *source) Unscheduled() int                  { return 0 }
func (s *source) Usage(string) (nodes.Usage, bool)  { return nodes.Usage{}, false }
func (s *source) BootedAt(string) (time.Time, bool) { return time.Time{}, false }
func (s *source) Poll(context.Context)              {}
func (s *source) set(name string, f func(*corev1.Node)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.nodes[name])
}

func setup(t *testing.T, ready bool) (*httptest.Server, *live.Store, *source) {
	t.Helper()
	src := &source{nodes: map[string]*corev1.Node{
		"n1": {ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		"n2": {ObjectMeta: metav1.ObjectMeta{Name: "n2"}},
	}}
	store := live.New(src, live.Options{})
	store.Notify(kube.Change{All: true, Info: true})
	store.Process()
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app:"+r.URL.Path) })
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, slog.New(slog.NewTextHandler(io.Discard, nil)), func(st settings.State) {
		b, _ := json.Marshal(st)
		store.SetSettings(st.Refresh(), b)
	})
	srv := httptest.NewServer(New(Options{Store: store, Settings: shared, Ready: func() bool { return ready }, UI: ui, Heartbeat: 50 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)
	return srv, store, src
}

func get(t *testing.T, url string, hdr ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	// Ask for gzip explicitly so the transport does not decompress for us.
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSecurityHeaders(t *testing.T) {
	srv, _, _ := setup(t, true)
	r := get(t, srv.URL+"/")
	for h, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	csp := r.Header.Get("Content-Security-Policy")
	for _, part := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, part) {
			t.Errorf("CSP %q lacks %q", csp, part)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows unsafe sources: %q", csp)
	}
}

func TestHealthAndReadiness(t *testing.T) {
	srv, _, _ := setup(t, false)
	if r := get(t, srv.URL+"/healthz"); r.StatusCode != 200 {
		t.Fatalf("healthz %d", r.StatusCode)
	}
	if r := get(t, srv.URL+"/readyz"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz before sync %d", r.StatusCode)
	}
	ready, _, _ := setup(t, true)
	if r := get(t, ready.URL+"/readyz"); r.StatusCode != 200 {
		t.Fatalf("readyz after sync %d", r.StatusCode)
	}
}

func TestSnapshotAndNode(t *testing.T) {
	srv, _, _ := setup(t, true)
	r := get(t, srv.URL+"/api/v1/snapshot")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot %d %v", r.StatusCode, r.Header)
	}
	var snap struct {
		Rows []nodes.Row `json:"rows"`
	}
	if err := json.Unmarshal([]byte(body(t, r)), &snap); err != nil || len(snap.Rows) != 2 {
		t.Fatalf("snapshot rows %d err %v", len(snap.Rows), err)
	}

	r = get(t, srv.URL+"/api/v1/nodes/n2")
	var row nodes.Row
	if err := json.Unmarshal([]byte(body(t, r)), &row); err != nil || row.Name != "n2" {
		t.Fatalf("node %+v %v", row, err)
	}
	r = get(t, srv.URL+"/api/v1/nodes/missing")
	if r.StatusCode != 404 || !strings.Contains(body(t, r), `"error"`) {
		t.Fatalf("missing node %d", r.StatusCode)
	}
	r = get(t, srv.URL+"/api/v1/nope")
	if r.StatusCode != 404 || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unknown api path %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
}

func TestGzip(t *testing.T) {
	srv, store, src := setup(t, true)
	// Make the snapshot big enough to be compressed.
	for i := 0; i < 50; i++ {
		name := "node-" + strings.Repeat("x", i)
		src.mu.Lock()
		src.nodes[name] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		src.mu.Unlock()
	}
	store.Notify(kube.Change{All: true})
	store.Process()
	r := get(t, srv.URL+"/api/v1/snapshot", "Accept-Encoding", "gzip")
	if r.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("not compressed: %v", r.Header)
	}
	zr, err := gzip.NewReader(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if !json.Valid(b) {
		t.Fatal("decompressed snapshot is not JSON")
	}
}

func TestReadOnly(t *testing.T) {
	srv, _, _ := setup(t, true)
	for _, path := range []string{"/api/v1/snapshot", "/api/v1/nodes/n1", "/", "/api/v1/whatever"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, resp.StatusCode)
		}
	}
}

func TestUIFallsThrough(t *testing.T) {
	srv, _, _ := setup(t, true)
	if got := body(t, get(t, srv.URL+"/nodes/n1")); got != "app:/nodes/n1" {
		t.Fatalf("ui got %q", got)
	}
}

// readEvent reads one SSE event, skipping comments and retry lines.
func readEvent(t *testing.T, br *bufio.Reader) (event, data string) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && event != "":
			return event, data
		}
	}
}

func TestEventStream(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "plain"
		if compressed {
			name = "gzip"
		}
		t.Run(name, func(t *testing.T) {
			srv, store, src := setup(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
			if compressed {
				req.Header.Set("Accept-Encoding", "gzip")
			} else {
				req.Header.Set("Accept-Encoding", "identity")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
			}
			var rd io.Reader = resp.Body
			if compressed {
				if resp.Header.Get("Content-Encoding") != "gzip" {
					t.Fatal("stream not compressed")
				}
				zr, err := gzip.NewReader(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				rd = zr
			}
			br := bufio.NewReader(rd)
			ev, data := readEvent(t, br)
			if ev != "snapshot" || !strings.Contains(data, `"name":"n1"`) {
				t.Fatalf("first event %s %.80s", ev, data)
			}
			src.set("n1", func(n *corev1.Node) { n.Spec.Unschedulable = true })
			store.Notify(kube.Change{Nodes: []string{"n1"}})
			store.Process()
			ev, data = readEvent(t, br)
			if ev != "delta" || !strings.Contains(data, `"cordoned":true`) || !strings.Contains(data, `"version":3`) {
				t.Fatalf("second event %s %.200s", ev, data)
			}
		})
	}
}

func TestMetrics(t *testing.T) {
	srv, _, _ := setup(t, true)
	get(t, srv.URL+"/api/v1/snapshot")
	text := body(t, get(t, srv.URL+"/metrics"))
	for _, want := range []string{
		"b221_rows 2",
		"# TYPE b221_http_requests_total counter",
		`b221_http_requests_total{route="snapshot",code="200"} 1`,
		"# TYPE b221_change_delay_seconds histogram",
		`b221_change_delay_seconds_bucket{le="+Inf"} 1`,
		"b221_change_delay_seconds_count 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics lack %q:\n%s", want, text)
		}
	}
}

func TestRefusesUnknownHostNames(t *testing.T) {
	src := &source{nodes: map[string]*corev1.Node{}}
	store := live.New(src, live.Options{})
	h := New(Options{Store: store, AllowedHosts: []string{"Console.Example.", " "}})
	for host, want := range map[string]int{
		"localhost:8080":      http.StatusOK,
		"127.0.0.1:8080":      http.StatusOK,
		"[::1]:8080":          http.StatusOK,
		"10.244.1.7:8080":     http.StatusOK,
		"app.localhost:8080":  http.StatusOK,
		"console.example":     http.StatusOK,
		"console.example:443": http.StatusOK,
		"evil.example:8080":   http.StatusMisdirectedRequest,
		"localhost.evil.test": http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: %d, want %d", host, rec.Code, want)
		}
	}
}

func TestAcceptsGzipWithQualities(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip":               true,
		"gzip, deflate, br":  true,
		"br;q=1, gzip;q=0.8": true,
		"gzip;q=0":           false,
		"gzip; q=0.000":      false,
		"identity":           false,
		"":                   false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(req); got != want {
			t.Errorf("Accept-Encoding %q: %v, want %v", header, got, want)
		}
	}
}

func put(t *testing.T, url, body string, hdr ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestSettings(t *testing.T) {
	srv, _, _ := setup(t, true)
	r := get(t, srv.URL+"/api/v1/settings")
	var st settings.State
	if err := json.Unmarshal([]byte(body(t, r)), &st); err != nil || st.RefreshSeconds != 3 || st.Storage != "memory" || st.ChangedAt != nil {
		t.Fatalf("settings %+v %v", st, err)
	}

	// The event stream starts with the settings and carries every change.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if ev, data := readEvent(t, br); ev != "snapshot" || !strings.Contains(data, `"settings":{"refreshSeconds":3,"storage":"memory"}`) {
		t.Fatalf("first event %s %.120s", ev, data)
	}

	host := strings.TrimPrefix(srv.URL, "http://")
	r = put(t, srv.URL+"/api/v1/settings", `{"refreshSeconds":30}`, "Origin", "http://"+host, "Sec-Fetch-Site", "same-origin")
	if r.StatusCode != 200 {
		t.Fatalf("PUT settings %d: %s", r.StatusCode, body(t, r))
	}
	if err := json.Unmarshal([]byte(body(t, r)), &st); err != nil || st.RefreshSeconds != 30 || st.ChangedAt == nil {
		t.Fatalf("settings after PUT %+v %v", st, err)
	}
	if ev, data := readEvent(t, br); ev != "delta" || !strings.Contains(data, `"settings":{"refreshSeconds":30,"storage":"memory","changedAt":"`) {
		t.Fatalf("delta %s %.200s", ev, data)
	}
	if r = get(t, srv.URL+"/api/v1/settings"); !strings.Contains(body(t, r), `"refreshSeconds":30`) {
		t.Fatal("the change was not kept")
	}

	for name, c := range map[string]struct {
		body string
		hdr  []string
		want int
	}{
		"out of range":       {`{"refreshSeconds":0}`, nil, 400},
		"too long":           {`{"refreshSeconds":601}`, nil, 400},
		"unknown field":      {`{"refreshSeconds":5,"theme":"dark"}`, nil, 400},
		"not json":           {`refresh=5`, nil, 400},
		"from another site":  {`{"refreshSeconds":5}`, []string{"Origin", "http://evil.example", "Sec-Fetch-Site", "cross-site"}, 403},
		"origin elsewhere":   {`{"refreshSeconds":5}`, []string{"Origin", "http://evil.example"}, 403},
		"opaque origin":      {`{"refreshSeconds":5}`, []string{"Origin", "null"}, 403},
		"wrong content type": {`{"refreshSeconds":5}`, []string{"Content-Type", "text/plain"}, 415},
	} {
		if r := put(t, srv.URL+"/api/v1/settings", c.body, c.hdr...); r.StatusCode != c.want {
			t.Errorf("%s: %d, want %d (%s)", name, r.StatusCode, c.want, body(t, r))
		}
	}
	if r := get(t, srv.URL+"/api/v1/settings"); !strings.Contains(body(t, r), `"refreshSeconds":30`) {
		t.Fatal("a refused change was applied")
	}
	// Only PUT changes settings; POST is refused like every other write.
	resp, err = http.Post(srv.URL+"/api/v1/settings", "application/json", strings.NewReader(`{"refreshSeconds":5}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST settings %d", resp.StatusCode)
	}
}
