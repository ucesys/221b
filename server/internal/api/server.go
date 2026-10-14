// Package api serves the browser app, the JSON API and the live event
// stream, plus health and metrics endpoints.
package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/views"
)

// Store is what the API reads rows from; *live.Store implements it.
type Store interface {
	Snapshot(views []string) []byte
	Row(view, key string) ([]byte, bool)
	Subscribe(views []string) (*live.Sub, []byte, error)
	Unsubscribe(*live.Sub)
	Stats() (views []live.ViewStats, subscribers int, recomputes uint64)
	Delays() live.Histogram
}

// Cluster is what the detail endpoints read; *kube.Cluster implements it.
// nil leaves them unserved.
type Cluster interface {
	views.Source
	PodUsageOf(namespace, name string) (nodes.Usage, bool)
	ObjectYAML(ctx context.Context, k kube.Kind, namespace, name string) (string, error)
	Logs(ctx context.Context, namespace, name string, o kube.LogOptions) (io.ReadCloser, error)
}

// Settings reads and changes the shared settings; *settings.Service
// implements it.
type Settings interface {
	Current() settings.State
	Update(ctx context.Context, s settings.Settings) (settings.State, error)
}

// Options configure the HTTP handler.
type Options struct {
	Store Store
	// Cluster serves the details of pods, services, releases, logs and YAML.
	Cluster Cluster
	// Settings serves /api/v1/settings; nil leaves the path unserved.
	Settings Settings
	// Ready reports whether the cluster has been listed; it drives /readyz.
	Ready func() bool
	// UI serves the browser app.
	UI http.Handler
	// Gauges adds cluster figures to /metrics.
	Gauges func() []Gauge
	Logger *slog.Logger
	// MaxStreams caps concurrent event streams.
	MaxStreams int
	// Heartbeat is how often an idle event stream sends a comment, so
	// proxies do not close it.
	Heartbeat time.Duration
	// AllowedHosts are the host names the server answers to, besides
	// localhost and IP addresses. Any other Host is refused, so a web page
	// cannot reach the console through DNS rebinding while a port-forward
	// is open.
	AllowedHosts []string
	// MaxLogs caps concurrent log streams.
	MaxLogs int
	Now     func() time.Time
}

// Gauge is one value on the metrics endpoint.
type Gauge struct {
	Name, Help string
	// Labels, when set, go between the braces: `view="nodes"`.
	Labels  string
	Value   float64
	Counter bool
}

// Server is the HTTP handler.
type Server struct {
	opts         Options
	mux          *http.ServeMux
	streams      atomic.Int64
	logs         atomic.Int64
	allowedHosts map[string]bool

	reqMu sync.Mutex
	reqs  map[reqKey]uint64
}

type reqKey struct {
	route string
	code  int
}

// New builds the handler.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxStreams <= 0 {
		o.MaxStreams = 200
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 15 * time.Second
	}
	if o.Ready == nil {
		o.Ready = func() bool { return true }
	}
	if o.UI == nil {
		o.UI = http.NotFoundHandler()
	}
	if o.MaxLogs <= 0 {
		o.MaxLogs = 50
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Server{opts: o, mux: http.NewServeMux(), reqs: map[reqKey]uint64{}, allowedHosts: map[string]bool{}}
	for _, h := range o.AllowedHosts {
		if h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), "."); h != "" {
			s.allowedHosts[h] = true
		}
	}
	s.handle("GET /api/v1/snapshot", "snapshot", s.snapshot)
	s.handle("GET /api/v1/nodes/{name}", "node", s.node)
	s.handle("GET /api/v1/events", "events", s.events)
	if o.Cluster != nil {
		s.handle("GET /api/v1/pods/{namespace}/{name}", "pod", s.pod)
		s.handle("GET /api/v1/pods/{namespace}/{name}/log", "pod-log", s.podLog)
		s.handle("GET /api/v1/services/{namespace}/{name}", "service", s.service)
		s.handle("GET /api/v1/helm/{namespace}/{name}", "release", s.release)
		s.handle("GET /api/v1/objects/{kind}/{namespace}/{name}", "object", s.object)
		s.handle("GET /api/v1/yaml/{kind}/{name}", "yaml", s.yaml)
		s.handle("GET /api/v1/yaml/{kind}/{namespace}/{name}", "yaml", s.yaml)
	}
	if o.Settings != nil {
		s.handle("GET /api/v1/settings", "settings", s.getSettings)
		s.handle("PUT /api/v1/settings", "settings-update", s.putSettings)
	}
	s.handle("GET /api/", "api-unknown", s.apiNotFound)
	s.handle("GET /healthz", "healthz", s.healthz)
	s.handle("GET /readyz", "readyz", s.readyz)
	s.handle("GET /metrics", "metrics", s.metrics)
	s.handle("GET /", "ui", o.UI.ServeHTTP)
	return s
}

func (s *Server) handle(pattern, route string, h http.HandlerFunc) {
	s.mux.Handle(pattern, s.observe(route, h))
}

// ServeHTTP adds security headers and recovers from panics.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r.Host) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMisdirectedRequest)
		fmt.Fprintf(w, "221B does not answer to the host name %q. Open it as localhost or by IP address, or add the name to its allowed hosts.\n", r.Host)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			s.opts.Logger.Error("handler panicked", "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	s.mux.ServeHTTP(w, r)
}

// hostAllowed accepts localhost, *.localhost, IP addresses and the
// configured names. A DNS-rebinding page arrives with its own host name.
func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	switch {
	case host == "", host == "localhost", strings.HasSuffix(host, ".localhost"):
		return true
	case net.ParseIP(host) != nil:
		return true
	}
	return s.allowedHosts[host]
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and deadlines.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) observe(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		h(sw, r)
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		s.reqMu.Lock()
		s.reqs[reqKey{route, code}]++
		s.reqMu.Unlock()
		if code >= 500 {
			s.opts.Logger.Warn("request failed", "method", r.Method, "path", r.URL.Path, "code", code, "duration", time.Since(start))
		} else {
			s.opts.Logger.Debug("request", "method", r.Method, "path", r.URL.Path, "code", code, "duration", time.Since(start))
		}
	})
}

// viewsOf reads the views a request asks for: ?views=nodes,pods. Without
// any, it is the nodes.
func viewsOf(r *http.Request) ([]string, error) {
	q := r.URL.Query().Get("views")
	if q == "" {
		return []string{live.ViewNodes}, nil
	}
	var out []string
	for _, v := range strings.Split(q, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("at most 8 views per stream")
	}
	return out, live.ValidViews(out)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	names, err := viewsOf(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, r, s.opts.Store.Snapshot(names))
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	row, ok := s.opts.Store.Row(live.ViewNodes, r.PathValue("name"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no node named "+strconv.Quote(r.PathValue("name")))
		return
	}
	writeJSON(w, r, row)
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	jsonError(w, http.StatusNotFound, "no such API path")
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	b, _ := json.Marshal(s.opts.Settings.Current())
	writeJSON(w, r, b)
}

// maxSettingsBody bounds a settings update; the settings are a few fields.
const maxSettingsBody = 4096

// putSettings changes the shared settings. It is the console's only
// request that changes anything, so it takes JSON only and refuses
// requests another site made the browser send (X-06).
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	if !sameSite(r) {
		jsonError(w, http.StatusForbidden, "settings can only be changed from the console itself")
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the settings as application/json")
		return
	}
	var in settings.Settings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the settings are not valid: "+err.Error())
		return
	}
	if err := in.Validate(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := s.opts.Settings.Update(r.Context(), in)
	if err != nil {
		s.opts.Logger.Warn("settings not saved", "err", err)
		jsonError(w, http.StatusInternalServerError, "The settings were not saved: "+err.Error())
		return
	}
	s.opts.Logger.Info("settings changed from a browser", "refresh", st.Refresh(), "from", r.RemoteAddr)
	b, _ := json.Marshal(st)
	writeJSON(w, r, b)
}

// sameSite reports whether the browser says the request came from the
// console's own pages. Browsers send Sec-Fetch-Site on every request and
// Origin on every cross-site one; a request with neither is not from a
// current browser, so it is let through for curl and scripts.
func sameSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return origin == ""
	}
	host := origin
	if i := strings.Index(origin, "://"); i >= 0 {
		host = origin[i+3:]
	}
	return strings.EqualFold(strings.TrimSuffix(host, "/"), r.Host)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.opts.Ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "waiting for the first list of nodes and pods\n")
		return
	}
	io.WriteString(w, "ok\n")
}

// events streams the rows of the views asked for (?views=nodes,pods) as
// Server-Sent Events: one "snapshot" event, then a "rows" event per change
// of a view, a "meta" event per change of the cluster details or settings
// and a "refresh" event per refresh tick.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	names, err := viewsOf(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if n := s.streams.Add(1); n > int64(s.opts.MaxStreams) {
		s.streams.Add(-1)
		w.Header().Set("Retry-After", "10")
		jsonError(w, http.StatusServiceUnavailable, "too many open event streams")
		return
	}
	defer s.streams.Add(-1)

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Vary", "Accept-Encoding")
	var out io.Writer = w
	var gz *gzip.Writer
	if acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		gz, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
		out = gz
		defer gz.Close()
	}
	flush := func() error {
		if gz != nil {
			if err := gz.Flush(); err != nil {
				return err
			}
		}
		return rc.Flush()
	}
	write := func(event string, data []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := fmt.Fprintf(out, "event: %s\ndata: ", event); err != nil {
			return err
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
		if _, err := io.WriteString(out, "\n\n"); err != nil {
			return err
		}
		return flush()
	}

	sub, snap, err := s.opts.Store.Subscribe(names)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer s.opts.Store.Unsubscribe(sub)
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(out, "retry: 3000\n\n"); err != nil {
		return
	}
	if err := write("snapshot", snap); err != nil {
		return
	}
	beat := time.NewTicker(s.opts.Heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := io.WriteString(out, ": ping\n\n"); err != nil {
				return
			}
			if err := flush(); err != nil {
				return
			}
		case <-sub.Wake():
			events, resync := sub.Take()
			if resync {
				if err := write("snapshot", s.opts.Store.Snapshot(names)); err != nil {
					return
				}
				continue
			}
			for _, e := range events {
				if err := write(e.Name, e.Data); err != nil {
					return
				}
			}
		}
	}
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder
	viewStats, subs, recomputes := s.opts.Store.Stats()
	gauges := []Gauge{
		{Name: "b221_event_streams", Help: "Open browser event streams.", Value: float64(subs)},
		{Name: "b221_log_streams", Help: "Open pod log streams.", Value: float64(s.logs.Load())},
		{Name: "b221_recomputes_total", Help: "Times the rows were recomputed.", Value: float64(recomputes), Counter: true},
	}
	for _, v := range viewStats {
		labels := fmt.Sprintf("view=%q", v.Name)
		gauges = append(gauges,
			Gauge{Name: "b221_rows_version", Help: "Version of a view's rows; it increases with every published change.", Labels: labels, Value: float64(v.Version), Counter: true},
			Gauge{Name: "b221_rows", Help: "Rows held per view.", Labels: labels, Value: float64(v.Rows)},
			Gauge{Name: "b221_view_subscribers", Help: "Browsers showing each view.", Labels: labels, Value: float64(v.Subscribers)},
		)
	}
	if s.opts.Gauges != nil {
		gauges = append(gauges, s.opts.Gauges()...)
	}
	// The exposition format keeps every sample of a metric together, after
	// its one HELP and TYPE line, so labelled gauges are grouped by name in
	// the order their names first appear.
	var names []string
	families := map[string][]Gauge{}
	for _, g := range gauges {
		if _, ok := families[g.Name]; !ok {
			names = append(names, g.Name)
		}
		families[g.Name] = append(families[g.Name], g)
	}
	for _, name := range names {
		family := families[name]
		kind := "gauge"
		if family[0].Counter {
			kind = "counter"
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, family[0].Help, name, kind)
		for _, g := range family {
			if g.Labels != "" {
				fmt.Fprintf(&b, "%s{%s} %s\n", name, g.Labels, strconv.FormatFloat(g.Value, 'f', -1, 64))
			} else {
				fmt.Fprintf(&b, "%s %s\n", name, strconv.FormatFloat(g.Value, 'f', -1, 64))
			}
		}
	}
	d := s.opts.Store.Delays()
	b.WriteString("# HELP b221_change_delay_seconds Time from a watch event reaching 221B to the change going out to browsers.\n# TYPE b221_change_delay_seconds histogram\n")
	for i, le := range live.DelayBuckets {
		fmt.Fprintf(&b, "b221_change_delay_seconds_bucket{le=\"%s\"} %d\n", strconv.FormatFloat(le, 'f', -1, 64), d.Counts[i])
	}
	n := d.Counts[len(live.DelayBuckets)]
	fmt.Fprintf(&b, "b221_change_delay_seconds_bucket{le=\"+Inf\"} %d\nb221_change_delay_seconds_sum %s\nb221_change_delay_seconds_count %d\n", n, strconv.FormatFloat(d.Sum, 'f', -1, 64), n)
	b.WriteString("# HELP b221_http_requests_total HTTP requests by route and status code.\n# TYPE b221_http_requests_total counter\n")
	s.reqMu.Lock()
	keys := make([]reqKey, 0, len(s.reqs))
	for k := range s.reqs {
		keys = append(keys, k)
	}
	s.reqMu.Unlock()
	sortKeys(keys)
	for _, k := range keys {
		s.reqMu.Lock()
		n := s.reqs[k]
		s.reqMu.Unlock()
		fmt.Fprintf(&b, "b221_http_requests_total{route=%q,code=\"%d\"} %d\n", k.route, k.code, n)
	}
	io.WriteString(w, b.String())
}

func sortKeys(keys []reqKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].code < keys[j].code
	})
}

// acceptsGzip reports whether the request accepts gzip with a quality
// above zero ("gzip", "gzip;q=0.8"; not "gzip;q=0").
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			if k, v, ok := strings.Cut(strings.TrimSpace(f), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = parsed
				}
			}
		}
		return q > 0
	}
	return false
}

func writeJSON(w http.ResponseWriter, r *http.Request, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Accept-Encoding")
	if len(body) > 1024 && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		gz.Write(body)
		gz.Close()
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(append(b, '\n'))
}
