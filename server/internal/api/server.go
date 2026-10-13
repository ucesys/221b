// Package api serves the browser app, the JSON API and the live event
// stream, plus health and metrics endpoints.
package api

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ucesys/221b/server/internal/live"
)

// Store is what the API reads rows from; *live.Store implements it.
type Store interface {
	Snapshot() []byte
	Row(name string) ([]byte, bool)
	Subscribe() (*live.Sub, []byte)
	Unsubscribe(*live.Sub)
	Stats() (version uint64, rows, subscribers int, recomputes uint64)
	Delays() live.Histogram
}

// Options configure the HTTP handler.
type Options struct {
	Store Store
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
}

// Gauge is one value on the metrics endpoint.
type Gauge struct {
	Name, Help string
	Value      float64
	Counter    bool
}

// Server is the HTTP handler.
type Server struct {
	opts    Options
	mux     *http.ServeMux
	streams atomic.Int64

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
	s := &Server{opts: o, mux: http.NewServeMux(), reqs: map[reqKey]uint64{}}
	s.handle("GET /api/v1/snapshot", "snapshot", s.snapshot)
	s.handle("GET /api/v1/nodes/{name}", "node", s.node)
	s.handle("GET /api/v1/events", "events", s.events)
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

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.opts.Store.Snapshot())
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	row, ok := s.opts.Store.Row(r.PathValue("name"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no node named "+strconv.Quote(r.PathValue("name")))
		return
	}
	writeJSON(w, r, row)
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	jsonError(w, http.StatusNotFound, "no such API path")
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

// events streams the rows as Server-Sent Events: one "snapshot" event, then
// a "delta" event per change.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
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

	sub, snap := s.opts.Store.Subscribe()
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
			deltas, resync := sub.Take()
			if resync {
				if err := write("snapshot", s.opts.Store.Snapshot()); err != nil {
					return
				}
				continue
			}
			for _, d := range deltas {
				if err := write("delta", d); err != nil {
					return
				}
			}
		}
	}
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder
	version, rows, subs, recomputes := s.opts.Store.Stats()
	gauges := []Gauge{
		{Name: "b221_rows_version", Help: "Version of the rows; it increases with every published change.", Value: float64(version), Counter: true},
		{Name: "b221_rows", Help: "Node rows held.", Value: float64(rows)},
		{Name: "b221_event_streams", Help: "Open browser event streams.", Value: float64(subs)},
		{Name: "b221_recomputes_total", Help: "Times the rows were recomputed.", Value: float64(recomputes), Counter: true},
	}
	if s.opts.Gauges != nil {
		gauges = append(gauges, s.opts.Gauges()...)
	}
	for _, g := range gauges {
		kind := "gauge"
		if g.Counter {
			kind = "counter"
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", g.Name, g.Help, g.Name, kind, g.Name, strconv.FormatFloat(g.Value, 'f', -1, 64))
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

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && !strings.Contains(strings.ReplaceAll(q, " ", ""), "q=0") {
			return true
		}
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
