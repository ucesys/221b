package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/settings"
)

// source is the fake cache with the two reads that reach the API server.
type source struct {
	*fake.Source
	// logs is what a pod's log stream carries, by "namespace/name"; a
	// missing pod answers NotFound as the API server would.
	logs map[string][]string
	// logErr, when set, is what opening any log stream fails with.
	logErr error
	// hold keeps a followed stream open after its lines, until ctx ends.
	hold bool
}

func (s *source) ObjectYAML(ctx context.Context, k kube.Kind, namespace, name string) (string, error) {
	obj := s.Object(k.Name, namespace, name)
	if obj == nil {
		return "", apierrors.NewNotFound(schema.GroupResource{Group: k.Group, Resource: k.Resource}, name)
	}
	return kube.ToYAML(obj)
}

func (s *source) Logs(ctx context.Context, namespace, name string, o kube.LogOptions) (io.ReadCloser, error) {
	if s.logErr != nil {
		return nil, s.logErr
	}
	lines, ok := s.logs[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, name)
	}
	if o.Tail > 0 && int(o.Tail) < len(lines) {
		lines = lines[len(lines)-int(o.Tail):]
	}
	if o.Previous {
		lines = []string{"previous " + o.Container}
	}
	if !o.Follow {
		return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
	}
	pr, pw := io.Pipe()
	go func() {
		for _, l := range lines {
			if _, err := io.WriteString(pw, l+"\n"); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		if s.hold {
			<-ctx.Done()
		}
		pw.Close()
	}()
	return pr, nil
}

func (s *source) set(name string, f func(*corev1.Node)) {
	s.Update("nodes", "", name, func(o runtime.Object) { f(o.(*corev1.Node)) })
}

func setup(t *testing.T, ready bool) (*httptest.Server, *live.Store, *source) {
	t.Helper()
	src := &source{Source: fake.New("n1", "n2"), logs: map[string][]string{}}
	store := live.New(src, live.Options{})
	store.Notify(everything())
	store.Process()
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app:"+r.URL.Path) })
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, slog.New(slog.NewTextHandler(io.Discard, nil)), func(st settings.State) {
		b, _ := json.Marshal(st)
		store.SetSettings(st.Refresh(), b)
	})
	srv := httptest.NewServer(New(Options{Store: store, Cluster: src, Settings: shared, Ready: func() bool { return ready }, UI: ui, Heartbeat: 50 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)
	return srv, store, src
}

func everything() kube.Change {
	ch := kube.Change{Info: true, Totals: true}
	for _, v := range []string{live.ViewNodes, live.ViewPods, live.ViewNamespaces, live.ViewServices, live.ViewHelm} {
		ch.Mark(v, kube.AllRows)
	}
	return ch
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
		Views map[string]struct {
			Rows []nodes.Row `json:"rows"`
		} `json:"views"`
	}
	if err := json.Unmarshal([]byte(body(t, r)), &snap); err != nil || len(snap.Views["nodes"].Rows) != 2 {
		t.Fatalf("snapshot rows %d err %v", len(snap.Views["nodes"].Rows), err)
	}
	if r := get(t, srv.URL+"/api/v1/snapshot?views=nodes,bogus"); r.StatusCode != 400 {
		t.Fatalf("unknown view %d", r.StatusCode)
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
		src.Put(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	store.Notify(everything())
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
			store.Notify(kube.Change{Views: map[string][]string{live.ViewNodes: {"n1"}}})
			store.Process()
			ev, data = readEvent(t, br)
			if ev != "rows" || !strings.Contains(data, `"cordoned":true`) || !strings.Contains(data, `"view":"nodes","version":2`) {
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
		`b221_rows{view="nodes"} 2`,
		"# TYPE b221_rows gauge",
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
	// Prometheus rejects a family described twice, or whose samples are
	// split by another family's, so every name is described once and all
	// its samples follow in a block.
	seen := map[string]bool{}
	last := ""
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name := strings.Fields(line)[2]
			if seen[name] {
				t.Errorf("metric %s described twice:\n%s", name, text)
			}
			seen[name] = true
			last = name
		case strings.HasPrefix(line, "# TYPE "), line == "":
		default:
			name := line[:strings.IndexAny(line, "{ ")]
			if !strings.HasPrefix(name, last) {
				t.Errorf("sample %q outside its family %s:\n%s", line, last, text)
			}
		}
	}
}

func TestRefusesUnknownHostNames(t *testing.T) {
	store := live.New(fake.New(), live.Options{})
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
	if ev, data := readEvent(t, br); ev != "meta" || !strings.Contains(data, `"settings":{"refreshSeconds":30,"storage":"memory","changedAt":"`) {
		t.Fatalf("meta %s %.200s", ev, data)
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

func samplePod(ns, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name), Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name: "app", Image: "app:1",
			Env: []corev1.EnvVar{
				{Name: "PLAIN", Value: "yes"},
				{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}, Key: "password"}}},
			},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.1.0.5", ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
}

func TestPodDetails(t *testing.T) {
	srv, store, src := setup(t, true)
	src.Put(samplePod("shop", "web", "n1"))
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "shop"}, Data: map[string][]byte{"password": []byte("hunter2-planted")}})
	src.SetPodUsage(map[string]nodes.Usage{"shop/web": {CPUMilli: 12, MemoryBytes: 1 << 20}})
	store.Notify(everything())
	store.Process()

	r := get(t, srv.URL+"/api/v1/pods/shop/web")
	text := body(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("pod %d %s", r.StatusCode, text)
	}
	var d struct {
		Name       string `json:"name"`
		PodIP      string `json:"podIP"`
		Containers []struct {
			Name string `json:"name"`
			Env  []struct {
				Name, Value, Source, From, Key string
			} `json:"env"`
		} `json:"containers"`
		Usage *nodes.Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(text), &d); err != nil || d.Name != "web" || d.PodIP != "10.1.0.5" || d.Usage == nil || d.Usage.CPUMilli != 12 {
		t.Fatalf("details %+v %v", d, err)
	}
	if len(d.Containers) != 1 || len(d.Containers[0].Env) != 2 {
		t.Fatalf("containers %+v", d.Containers)
	}
	env := d.Containers[0].Env[1]
	if env.Name != "PASSWORD" || env.Value != "" || env.Source != "Secret" || env.From != "creds" || env.Key != "password" {
		t.Fatalf("secret env %+v", env)
	}
	if strings.Contains(text, "hunter2") {
		t.Fatal("a Secret value reached the pod details")
	}
	if r := get(t, srv.URL+"/api/v1/pods/shop/gone"); r.StatusCode != 404 || !strings.Contains(body(t, r), "deleted") {
		t.Fatalf("missing pod %d", r.StatusCode)
	}
}

func TestServiceAndReleaseDetails(t *testing.T) {
	srv, store, src := setup(t, true)
	src.Put(samplePod("shop", "web", "n1"))
	src.Put(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.9", Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80}}}})
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.web.v1", Namespace: "shop", Labels: map[string]string{"owner": "helm", "name": "web", "version": "1", "status": "deployed"}}, Type: kube.HelmSecretType, Data: map[string][]byte{"release": kube.EncodeHelmRelease(helmRecord("web", "shop", 1, "deployed", map[string]any{"replicas": 2, "dbPassword": "planted-pw"}))}})
	store.Notify(everything())
	store.Process()

	r := get(t, srv.URL+"/api/v1/services/shop/web")
	text := body(t, r)
	if r.StatusCode != 200 || !strings.Contains(text, `"dnsName":"web.shop.svc.cluster.local"`) {
		t.Fatalf("service %d %s", r.StatusCode, text)
	}
	if r := get(t, srv.URL+"/api/v1/services/shop/gone"); r.StatusCode != 404 {
		t.Fatalf("missing service %d", r.StatusCode)
	}

	r = get(t, srv.URL+"/api/v1/helm/shop/web")
	text = body(t, r)
	if r.StatusCode != 200 || !strings.Contains(text, `"chart":"web"`) || !strings.Contains(text, "replicas: 2") {
		t.Fatalf("release %d %s", r.StatusCode, text)
	}
	if strings.Contains(text, "planted-pw") || !strings.Contains(text, `"maskedValues":["dbPassword"]`) {
		t.Fatalf("the credential-looking value was not masked: %s", text)
	}
	if r := get(t, srv.URL+"/api/v1/helm/shop/gone"); r.StatusCode != 404 {
		t.Fatalf("missing release %d", r.StatusCode)
	}

	src.SetSecrets(false)
	if r := get(t, srv.URL+"/api/v1/helm/shop/web"); r.StatusCode != 404 || !strings.Contains(body(t, r), "off") {
		t.Fatalf("release with secrets off %d", r.StatusCode)
	}
	if r := get(t, srv.URL+"/api/v1/yaml/secrets/shop/creds"); r.StatusCode != 404 {
		t.Fatalf("secret yaml with secrets off %d", r.StatusCode)
	}
}

func TestYAML(t *testing.T) {
	srv, _, src := setup(t, true)
	src.Put(samplePod("shop", "web", "n1"))
	r := get(t, srv.URL+"/api/v1/yaml/pods/shop/web")
	text := body(t, r)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/yaml") || !strings.Contains(text, "name: web") || !strings.Contains(text, "nodeName: n1") {
		t.Fatalf("yaml %d %q %s", r.StatusCode, r.Header.Get("Content-Type"), text)
	}
	r = get(t, srv.URL+"/api/v1/yaml/nodes/n1?download=1")
	if r.StatusCode != 200 || r.Header.Get("Content-Disposition") != `attachment; filename="node-n1.yaml"` {
		t.Fatalf("node yaml %d %q", r.StatusCode, r.Header.Get("Content-Disposition"))
	}
	for path, want := range map[string]int{
		"/api/v1/yaml/pods/web":         404, // namespaced kind without a namespace
		"/api/v1/yaml/nodes/shop/n1":    404, // cluster-scoped kind with one
		"/api/v1/yaml/things/shop/web":  404,
		"/api/v1/yaml/pods/shop/absent": 404,
	} {
		if r := get(t, srv.URL+path); r.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, r.StatusCode, want)
		}
	}
}

func TestPodLogs(t *testing.T) {
	srv, _, src := setup(t, true)
	src.logs["shop/web"] = []string{"one", "two", "three"}

	r := get(t, srv.URL+"/api/v1/pods/shop/web/log?tail=2&download=1")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") || r.Header.Get("Content-Disposition") != `attachment; filename="web.log"` {
		t.Fatalf("log %d %v", r.StatusCode, r.Header)
	}
	if got := body(t, r); got != "two\nthree\n" {
		t.Fatalf("log body %q", got)
	}
	if got := body(t, get(t, srv.URL+"/api/v1/pods/shop/web/log?previous=1&container=app")); got != "previous app\n" {
		t.Fatalf("previous log %q", got)
	}
	if r := get(t, srv.URL+"/api/v1/pods/shop/web/log?tail=x"); r.StatusCode != 400 {
		t.Fatalf("bad tail %d", r.StatusCode)
	}
	if r := get(t, srv.URL+"/api/v1/pods/shop/gone/log"); r.StatusCode != 404 || !strings.Contains(body(t, r), "deleted") {
		t.Fatalf("missing pod %d", r.StatusCode)
	}
	src.logErr = apierrors.NewBadRequest("container \"app\" in pod \"web\" is waiting to start: ContainerCreating")
	if r := get(t, srv.URL+"/api/v1/pods/shop/web/log"); r.StatusCode != 400 || !strings.Contains(body(t, r), "waiting to start") {
		t.Fatalf("waiting container %d %s", r.StatusCode, body(t, r))
	}
	src.logErr = apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "web", errors.New("no"))
	if r := get(t, srv.URL+"/api/v1/pods/shop/web/log"); r.StatusCode != 403 {
		t.Fatalf("forbidden %d", r.StatusCode)
	}
	src.logErr = nil

	// Following streams the lines as events and says when they end.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/pods/shop/web/log?follow=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(resp.Body)
	var lines []string
	for {
		ev, data := readEvent(t, br)
		if ev == "end" {
			if !strings.Contains(data, "stopped writing") {
				t.Fatalf("end %s", data)
			}
			break
		}
		if ev != "log" {
			t.Fatalf("event %s", ev)
		}
		var batch []string
		if err := json.Unmarshal([]byte(data), &batch); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, batch...)
	}
	if strings.Join(lines, ",") != "one,two,three" {
		t.Fatalf("lines %v", lines)
	}
}

func TestLogStreamsAreCapped(t *testing.T) {
	src := &source{Source: fake.New(), logs: map[string][]string{"a/b": {"x"}}, hold: true}
	store := live.New(src, live.Options{})
	h := New(Options{Store: store, Cluster: src, MaxLogs: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/pods/a/b/log?follow=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = readEvent(t, bufio.NewReader(resp.Body))
	// The first stream is still open (its reader is slow), so a second is refused.
	if r := get(t, srv.URL+"/api/v1/pods/a/b/log"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second log stream %d", r.StatusCode)
	}
}

func helmRecord(name, ns string, revision int, status string, values map[string]any) kube.HelmRecord {
	rec := kube.HelmRecord{Name: name, Namespace: ns, Version: revision, Config: values}
	rec.Info.Status, rec.Info.Description = status, fmt.Sprintf("Install complete (revision %d)", revision)
	rec.Info.FirstDeployed, rec.Info.LastDeployed = "2026-10-01T08:00:00Z", "2026-10-01T08:00:00Z"
	rec.Chart.Metadata = kube.HelmChartMetadata{Name: name, Version: "1.2.3", AppVersion: "4.5.6"}
	return rec
}

func TestObjectDetails(t *testing.T) {
	srv, store, src := setup(t, true)
	web := samplePod("shop", "web", "n1")
	web.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: ptrBool(true)}}
	src.Put(web)
	src.Put(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", Controller: ptrBool(true)}}}})
	src.Put(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-deploy"}, Spec: appsv1.DeploymentSpec{Replicas: ptrInt32(1)}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}})
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "shop"}, Data: map[string][]byte{"password": []byte("hunter2-planted")}})
	src.SetPodUsage(map[string]nodes.Usage{"shop/web": {CPUMilli: 12, MemoryBytes: 1 << 20}})
	store.Notify(everything())
	store.Process()

	for _, kind := range []string{"Deployment", "deployments"} {
		r := get(t, srv.URL+"/api/v1/objects/"+kind+"/shop/web")
		text := body(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("%s %d %s", kind, r.StatusCode, text)
		}
		var d struct {
			Kind     string `json:"kind"`
			Status   string `json:"status"`
			Workload bool   `json:"workload"`
			Pods     []struct {
				Key string `json:"key"`
			} `json:"pods"`
			Facts []struct{ Label, Value string } `json:"facts"`
			Usage map[string]nodes.Usage          `json:"usage"`
		}
		if err := json.Unmarshal([]byte(text), &d); err != nil || d.Kind != "Deployment" || d.Status != "1/1 ready" || !d.Workload {
			t.Fatalf("%s: %+v %v", kind, d, err)
		}
		if len(d.Pods) != 1 || d.Pods[0].Key != "shop/web" || d.Usage["shop/web"].CPUMilli != 12 {
			t.Fatalf("%s pods and usage: %+v", kind, d)
		}
		if len(d.Facts) == 0 || d.Facts[0].Label != "Replicas" {
			t.Fatalf("%s facts: %+v", kind, d.Facts)
		}
	}
	r := get(t, srv.URL+"/api/v1/objects/secrets/shop/creds")
	text := body(t, r)
	if r.StatusCode != 200 || !strings.Contains(text, `"keys":[{"key":"password","bytes":15}]`) || strings.Contains(text, "hunter2") {
		t.Fatalf("secret details %d %s", r.StatusCode, text)
	}
	for path, code := range map[string]int{"/api/v1/objects/deployments/shop/gone": 404, "/api/v1/objects/gizmos/shop/web": 404} {
		if r := get(t, srv.URL+path); r.StatusCode != code {
			t.Fatalf("%s: %d, want %d", path, r.StatusCode, code)
		}
	}
	// With Secrets off, a Secret's details are not served either.
	src.SetSecrets(false)
	if r := get(t, srv.URL+"/api/v1/objects/secrets/shop/creds"); r.StatusCode != 404 {
		t.Fatalf("secret with Secrets off: %d", r.StatusCode)
	}
}

func ptrBool(b bool) *bool    { return &b }
func ptrInt32(n int32) *int32 { return &n }
