package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/views"
)

// The detail endpoints read the watch cache, so they answer at once and
// never reach the API server, apart from logs and YAML, which read the
// object as it is now.

// podDetails is the pod details with the pod's CPU and memory in use.
type podDetails struct {
	views.PodDetails
	Usage *nodes.Usage `json:"usage"`
}

func (s *Server) pod(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	p, _ := s.opts.Cluster.Object("pods", ns, name).(*corev1.Pod)
	if p == nil {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no pod %s/%s: it may have been deleted", ns, name))
		return
	}
	d := podDetails{PodDetails: views.BuildPodDetails(s.opts.Cluster, p, s.opts.Now().UTC())}
	if u, ok := s.opts.Cluster.PodUsageOf(ns, name); ok {
		d.Usage = &u
	}
	b, err := json.Marshal(d)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "encoding the pod details: "+err.Error())
		return
	}
	writeJSON(w, r, b)
}

func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	svc, _ := s.opts.Cluster.Object("services", ns, name).(*corev1.Service)
	if svc == nil {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no service %s/%s: it may have been deleted", ns, name))
		return
	}
	b, err := json.Marshal(views.BuildServiceDetails(s.opts.Cluster, svc, s.opts.Now().UTC()))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "encoding the service details: "+err.Error())
		return
	}
	writeJSON(w, r, b)
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Cluster.SecretsRead() {
		jsonError(w, http.StatusNotFound, "Helm releases are not read: reading Secrets is off (secrets.read in the chart)")
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	d, ok := views.BuildReleaseDetails(s.opts.Cluster, ns, name, s.opts.Now().UTC())
	if !ok {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no Helm release %s in %s: it may have been uninstalled", name, ns))
		return
	}
	for _, p := range d.PodRows {
		if u, ok := s.opts.Cluster.PodUsageOf(p.Namespace, p.Name); ok {
			if d.Usage == nil {
				d.Usage = map[string]nodes.Usage{}
			}
			d.Usage[p.Key] = u
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "encoding the release details: "+err.Error())
		return
	}
	writeJSON(w, r, b)
}

// yaml serves an object as YAML (P2-08): /api/v1/yaml/{kind}/{name} for
// cluster-scoped kinds, /api/v1/yaml/{kind}/{namespace}/{name} otherwise.
func (s *Server) yaml(w http.ResponseWriter, r *http.Request) {
	k, ok := kube.KindByName(r.PathValue("kind"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no kind named "+strconv.Quote(r.PathValue("kind")))
		return
	}
	if k.Secret && !s.opts.Cluster.SecretsRead() {
		jsonError(w, http.StatusNotFound, "Secrets are not read: reading Secrets is off (secrets.read in the chart)")
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if k.Namespaced != (ns != "") {
		if k.Namespaced {
			jsonError(w, http.StatusNotFound, k.Kind+" is namespaced: give its namespace")
		} else {
			jsonError(w, http.StatusNotFound, k.Kind+" is cluster-scoped: it has no namespace")
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	text, err := s.opts.Cluster.ObjectYAML(ctx, k, ns, name)
	if err != nil {
		code, msg := http.StatusBadGateway, "reading the object from the API server: "+err.Error()
		switch {
		case apierrors.IsNotFound(err):
			code, msg = http.StatusNotFound, fmt.Sprintf("no %s named %s: it may have been deleted", k.Kind, name)
		case apierrors.IsForbidden(err):
			code, msg = http.StatusForbidden, fmt.Sprintf("not allowed to read %s %s: %s", k.Kind, name, err.Error())
		case errors.Is(err, context.DeadlineExceeded):
			code, msg = http.StatusGatewayTimeout, "the API server did not answer in time"
		}
		jsonError(w, code, msg)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/yaml; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") != "" {
		file := k.Kind + "-" + name + ".yaml"
		if ns != "" {
			file = k.Kind + "-" + ns + "-" + name + ".yaml"
		}
		h.Set("Content-Disposition", "attachment; filename="+strconv.Quote(strings.ToLower(file)))
	}
	io.WriteString(w, text)
}

// maxLogLine bounds one log line; longer lines are split.
const maxLogLine = 1 << 20

// podLog serves a pod's logs (P2-04). Without follow, the body is the log
// as text, for reading or downloading (?download=1). With ?follow=1 it is
// an event stream: a "log" event per chunk of lines, and an "end" event
// when the container stops writing. ?container=, ?previous=1, ?tail=N and
// ?timestamps=1 select what is read.
func (s *Server) podLog(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	q := r.URL.Query()
	o := kube.LogOptions{
		Container:  q.Get("container"),
		Follow:     q.Get("follow") != "" && q.Get("follow") != "0",
		Previous:   q.Get("previous") != "" && q.Get("previous") != "0",
		Timestamps: q.Get("timestamps") != "" && q.Get("timestamps") != "0",
	}
	if t := q.Get("tail"); t != "" {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil || n < 0 {
			jsonError(w, http.StatusBadRequest, "tail must be a number of lines")
			return
		}
		o.Tail = n
	}
	if n := s.logs.Add(1); n > int64(s.opts.MaxLogs) {
		s.logs.Add(-1)
		w.Header().Set("Retry-After", "10")
		jsonError(w, http.StatusServiceUnavailable, "too many open log streams")
		return
	}
	defer s.logs.Add(-1)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := s.opts.Cluster.Logs(ctx, ns, name, o)
	if err != nil {
		code, msg := http.StatusBadGateway, "reading the logs from the API server: "+err.Error()
		var status *apierrors.StatusError
		switch {
		case apierrors.IsNotFound(err):
			code, msg = http.StatusNotFound, fmt.Sprintf("no pod %s/%s: it may have been deleted", ns, name)
		case apierrors.IsForbidden(err):
			code, msg = http.StatusForbidden, "not allowed to read pod logs: "+err.Error()
		case apierrors.IsBadRequest(err):
			// "previous terminated container not found", "container x is
			// waiting to start": the API server's own words are the clearest.
			code, msg = http.StatusBadRequest, err.Error()
			if errors.As(err, &status) && status.ErrStatus.Message != "" {
				msg = status.ErrStatus.Message
			}
		}
		jsonError(w, code, msg)
		return
	}
	defer stream.Close()

	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	if !o.Follow {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		if q.Get("download") != "" {
			file := name + ".log"
			if o.Container != "" {
				file = name + "-" + o.Container + ".log"
			}
			if o.Previous {
				file = strings.TrimSuffix(file, ".log") + "-previous.log"
			}
			h.Set("Content-Disposition", "attachment; filename="+strconv.Quote(file))
		}
		var out io.Writer = w
		if acceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
			defer gz.Close()
			out = gz
		}
		w.WriteHeader(http.StatusOK)
		io.Copy(out, stream)
		return
	}

	rc := http.NewResponseController(w)
	h.Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	write := func(event string, data []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: ", event); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\n\n"); err != nil {
			return err
		}
		return rc.Flush()
	}
	// Lines are read by one goroutine and sent in batches by this one, so a
	// chatty container becomes a few events per second rather than one per
	// line, and a silent one still gets heartbeats.
	lines := make(chan string, 256)
	readErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stream)
		sc.Buffer(make([]byte, 64*1024), maxLogLine)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		readErr <- sc.Err()
	}()
	beat := time.NewTicker(s.opts.Heartbeat)
	defer beat.Stop()
	flushEvery := time.NewTicker(200 * time.Millisecond)
	defer flushEvery.Stop()
	var batch []string
	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		b, _ := json.Marshal(batch)
		batch = batch[:0]
		return write("log", b)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case line := <-lines:
			batch = append(batch, line)
			if len(batch) >= 500 {
				if err := send(); err != nil {
					return
				}
			}
		case <-flushEvery.C:
			if err := send(); err != nil {
				return
			}
		case <-beat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case err := <-readErr:
			// Drain what the reader queued before it stopped.
			for {
				select {
				case line := <-lines:
					batch = append(batch, line)
					continue
				default:
				}
				break
			}
			if err := send(); err != nil {
				return
			}
			reason := "the container stopped writing"
			if err != nil {
				reason = "the log stream ended: " + err.Error()
			}
			b, _ := json.Marshal(map[string]string{"reason": reason})
			_ = write("end", b)
			return
		}
	}
}
