// Package sim is a small fake Kubernetes API server for development, tests
// and scale checks. It serves every kind the console watches, with the list
// and watch semantics client-go relies on (resource versions, streaming
// lists, bookmarks, expiry, metadata-only lists), node and pod metrics, pod
// logs, and an admin API to change the cluster during a test. It speaks
// JSON over plain HTTP and needs no credentials.
package sim

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/ucesys/221b/server/internal/kube"
)

// Options shape the simulated cluster.
type Options struct {
	Nodes int
	Pods  int
	Seed  int64
	// Version is the Kubernetes version the simulator reports.
	Version string
	// Metrics turns the metrics.k8s.io API on.
	Metrics bool
	// KubeletStats serves each node's boot time the way the API server
	// proxies a kubelet's Summary API. Off, the proxy is refused, as it is
	// for a ServiceAccount without get on nodes/proxy.
	KubeletStats bool
	// SettingsConfigMap is a "namespace/name" ConfigMap that exists from
	// the start, empty, as the chart's does. Empty means none.
	SettingsConfigMap string
	// Problems adds a not-ready node, a cordoned node, a node under disk
	// pressure and some failing pods, so the problem views have something
	// to show.
	Problems bool
	// Now is the clock; tests can fix it.
	Now func() time.Time
}

// Sim is the simulated cluster and its HTTP handler.
type Sim struct {
	opts Options
	mux  *http.ServeMux

	mu       sync.Mutex
	rv       int64
	nodes    map[string]*corev1.Node
	pods     map[string]*corev1.Pod
	objects  map[string]map[string]runtime.Object // kind -> key -> every other kind
	events   []event
	watchers map[*watcher]struct{}
	down     bool
	refused  int // list and watch requests refused while down
	metrics  bool
	usage    map[string][2]int64 // node -> cpu milli, memory bytes
	podUsage map[string][2]int64 // namespace/name -> cpu milli, memory bytes
	boots    map[string]time.Time
	blips    map[string]time.Time // pods churn made not ready, and when
	rnd      *rand.Rand
}

// event is one change, encoded for watchers of its kind: in full, or as
// metadata only for watchers that asked for that.
type event struct {
	rv      int64
	kind    string
	t       watch.EventType
	obj     runtime.Object
	full    []byte
	partial []byte
}

func (e *event) data(partial bool) []byte {
	if partial {
		if e.partial == nil {
			e.partial = wrap(e.t, encodePartial(e.obj))
		}
		return e.partial
	}
	if e.full == nil {
		b, _ := json.Marshal(e.obj)
		e.full = wrap(e.t, b)
	}
	return e.full
}

type watcher struct {
	kind    string
	partial bool
	ch      chan []byte
	done    chan struct{}
	once    sync.Once
}

func (w *watcher) close() { w.once.Do(func() { close(w.done) }) }

// maxEvents is how many events are kept for watches that resume from an
// older resource version. Older versions get 410 Expired, as on a real
// API server.
const maxEvents = 20000

// New builds a simulated cluster.
func New(o Options) *Sim {
	if o.Nodes <= 0 {
		o.Nodes = 10
	}
	if o.Version == "" {
		o.Version = "v1.37.1"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Sim{
		opts:     o,
		nodes:    map[string]*corev1.Node{},
		pods:     map[string]*corev1.Pod{},
		objects:  map[string]map[string]runtime.Object{},
		watchers: map[*watcher]struct{}{},
		metrics:  o.Metrics,
		usage:    map[string][2]int64{},
		podUsage: map[string][2]int64{},
		boots:    map[string]time.Time{},
		rnd:      rand.New(rand.NewSource(o.Seed + 221)),
	}
	s.generate()
	if ns, name, ok := strings.Cut(o.SettingsConfigMap, "/"); ok && ns != "" && name != "" {
		s.addLocked(&corev1.ConfigMap{TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid(ns + "/" + name)}, Data: map[string]string{}})
	}
	s.routes()
	return s
}

// ServeHTTP serves the fake API and the admin API.
func (s *Sim) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Sim) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /version", s.api(s.version))
	m.HandleFunc("GET /readyz", s.api(s.ok))
	m.HandleFunc("GET /livez", s.api(s.ok))
	m.HandleFunc("GET /healthz", s.api(s.ok))
	for _, k := range kube.Kinds {
		m.HandleFunc("GET "+k.Path(), s.api(s.listOrWatch(k)))
		if k.Namespaced {
			m.HandleFunc("GET "+k.Path()+"/{name}", s.api(s.notFound))
			m.HandleFunc("GET "+namespacedPath(k)+"/{ns}/"+k.Resource, s.api(s.listOrWatch(k)))
			m.HandleFunc("GET "+namespacedPath(k)+"/{ns}/"+k.Resource+"/{name}", s.api(s.get(k)))
		} else {
			m.HandleFunc("GET "+k.Path()+"/{name}", s.api(s.get(k)))
		}
	}
	m.HandleFunc("GET /apis/metrics.k8s.io/v1beta1/nodes", s.api(s.nodeMetrics))
	m.HandleFunc("GET /apis/metrics.k8s.io/v1beta1/pods", s.api(s.podMetrics))
	m.HandleFunc("GET /api/v1/nodes/{name}/proxy/stats/summary", s.api(s.kubeletSummary))
	m.HandleFunc("GET /api/v1/namespaces/{ns}/pods/{name}/log", s.api(s.podLog))
	m.HandleFunc("PATCH /api/v1/namespaces/{ns}/configmaps/{name}", s.api(s.patchConfigMap))
	m.HandleFunc("GET /sim/state", s.state)
	m.HandleFunc("POST /sim/nodes/{name}/cordon", s.admin(s.cordon))
	m.HandleFunc("POST /sim/nodes/{name}/ready", s.admin(s.ready))
	m.HandleFunc("POST /sim/nodes/{name}/reboot", s.admin(s.reboot))
	m.HandleFunc("POST /sim/nodes/{name}/condition", s.admin(s.condition))
	m.HandleFunc("POST /sim/nodes", s.admin(s.addNode))
	m.HandleFunc("DELETE /sim/nodes/{name}", s.admin(s.deleteNode))
	m.HandleFunc("POST /sim/pods/{ns}/{name}/crash", s.admin(s.crashPod))
	m.HandleFunc("POST /sim/pods/{ns}/{name}/heal", s.admin(s.healPod))
	m.HandleFunc("DELETE /sim/pods/{ns}/{name}", s.admin(s.deletePod))
	m.HandleFunc("POST /sim/services/{ns}/{name}/external-ip", s.admin(s.externalIP))
	m.HandleFunc("POST /sim/down", s.admin(s.setDown))
	m.HandleFunc("POST /sim/metrics", s.admin(s.setMetrics))
	m.HandleFunc("POST /sim/churn", s.admin(s.churnOnce))
	m.HandleFunc("/", s.api(s.notFound))
	s.mux = m
}

// api wraps fake API handlers: while the simulator is "down" they fail like
// an unreachable API server would.
func (s *Sim) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.down
		if down && (r.URL.Path == "/api/v1/nodes" || r.URL.Path == "/api/v1/pods") {
			s.refused++
		}
		s.mu.Unlock()
		if down {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
			status(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the simulated API server is down")
			return
		}
		h(w, r)
	}
}

func (s *Sim) admin(h func(r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Sim) ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func (s *Sim) notFound(w http.ResponseWriter, r *http.Request) {
	status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
}

func (s *Sim) version(w http.ResponseWriter, _ *http.Request) {
	minor := strings.Split(strings.TrimPrefix(s.opts.Version, "v"), ".")
	writeJSON(w, map[string]string{
		"major": "1", "minor": minor[min(1, len(minor)-1)], "gitVersion": s.opts.Version,
		"gitCommit": "0000000000000000000000000000000000000000", "gitTreeState": "clean",
		"buildDate": "2026-09-30T00:00:00Z", "goVersion": "go1.26.9", "compiler": "gc", "platform": "linux/amd64",
	})
}

// ---------- list, get and watch ----------

// namespacedPath is the prefix of a namespaced kind's paths, before
// /{namespace}/{resource}.
func namespacedPath(k kube.Kind) string {
	if k.Group == "" {
		return "/api/" + k.Version + "/namespaces"
	}
	return "/apis/" + k.Group + "/" + k.Version + "/namespaces"
}

// wantsPartial reports whether the client asked for metadata only, as the
// metadata client does.
func wantsPartial(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadata")
}

func (s *Sim) listOrWatch(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("watch") == "true" || q.Get("watch") == "1" {
			s.watch(k, w, r)
			return
		}
		s.list(k, w, r)
	}
}

func (s *Sim) list(k kube.Kind, w http.ResponseWriter, r *http.Request) {
	partial := wantsPartial(r)
	s.mu.Lock()
	items := s.itemsLocked(k.Name, r.PathValue("ns"), partial)
	rv := s.rv
	s.mu.Unlock()
	listKind, apiVersion := k.Kind+"List", k.APIVersion()
	if partial {
		listKind, apiVersion = "PartialObjectMetadataList", "meta.k8s.io/v1"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"kind":%q,"apiVersion":%q,"metadata":{"resourceVersion":"%d"},"items":[`, listKind, apiVersion, rv)
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteString("]}")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(b.String()))
}

// get serves one object.
func (s *Sim) get(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("name")
		if k.Namespaced {
			key = r.PathValue("ns") + "/" + key
		}
		s.mu.Lock()
		obj := s.objectLocked(k.Name, key)
		s.mu.Unlock()
		if obj == nil {
			status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("%s %q not found", k.Resource, r.PathValue("name")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if wantsPartial(r) {
			w.Write(encodePartial(obj))
			return
		}
		b, _ := json.Marshal(obj)
		w.Write(b)
	}
}

// objectLocked finds one object of a kind by its cache key.
func (s *Sim) objectLocked(kind, key string) runtime.Object {
	switch kind {
	case "nodes":
		if n, ok := s.nodes[key]; ok {
			return n
		}
	case "pods":
		if p, ok := s.pods[key]; ok {
			return p
		}
	default:
		if o, ok := s.objects[kind][key]; ok {
			return o
		}
	}
	return nil
}

// keysLocked lists the cache keys of a kind, sorted.
func (s *Sim) keysLocked(kind string) []string {
	var keys []string
	switch kind {
	case "nodes":
		for k := range s.nodes {
			keys = append(keys, k)
		}
	case "pods":
		for k := range s.pods {
			keys = append(keys, k)
		}
	default:
		for k := range s.objects[kind] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// itemsLocked encodes the current objects of a kind, sorted by key, in one
// namespace or all.
func (s *Sim) itemsLocked(kind, namespace string, partial bool) [][]byte {
	keys := s.keysLocked(kind)
	out := make([][]byte, 0, len(keys))
	for _, k := range keys {
		if namespace != "" && !strings.HasPrefix(k, namespace+"/") {
			continue
		}
		obj := s.objectLocked(kind, k)
		if partial {
			out = append(out, encodePartial(obj))
			continue
		}
		b, _ := json.Marshal(obj)
		out = append(out, b)
	}
	return out
}

// encodePartial encodes an object as the metadata-only form the API server
// serves to clients that ask for it.
func encodePartial(obj runtime.Object) []byte {
	m, err := meta.Accessor(obj)
	if err != nil {
		return []byte("{}")
	}
	p := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadata", APIVersion: "meta.k8s.io/v1"}}
	p.ObjectMeta = metav1.ObjectMeta{
		Name: m.GetName(), Namespace: m.GetNamespace(), UID: m.GetUID(), ResourceVersion: m.GetResourceVersion(),
		CreationTimestamp: m.GetCreationTimestamp(), DeletionTimestamp: m.GetDeletionTimestamp(),
		Labels: m.GetLabels(), Annotations: m.GetAnnotations(), OwnerReferences: m.GetOwnerReferences(), Generation: m.GetGeneration(),
	}
	b, _ := json.Marshal(p)
	return b
}

func (s *Sim) watch(k kube.Kind, w http.ResponseWriter, r *http.Request) {
	kind := k.Name
	q := r.URL.Query()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	timeout := 10 * time.Minute
	if t, err := strconv.Atoi(q.Get("timeoutSeconds")); err == nil && t > 0 {
		timeout = time.Duration(t) * time.Second
	}
	initial := q.Get("sendInitialEvents") == "true"
	bookmarks := q.Get("allowWatchBookmarks") == "true"
	rvParam := q.Get("resourceVersion")
	partial := wantsPartial(r)

	wt := &watcher{kind: kind, partial: partial, ch: make(chan []byte, 4096), done: make(chan struct{})}
	var backlog [][]byte
	s.mu.Lock()
	current := s.rv
	switch {
	case initial || rvParam == "" || rvParam == "0":
		// Send the current state as ADDED events, as the API server does for
		// a watch without a resource version and for streaming lists.
		for _, it := range s.itemsLocked(kind, r.PathValue("ns"), partial) {
			backlog = append(backlog, wrap(watch.Added, it))
		}
		if initial {
			backlog = append(backlog, bookmark(k, current, true, partial))
		}
	default:
		from, err := strconv.ParseInt(rvParam, 10, 64)
		if err != nil {
			s.mu.Unlock()
			status(w, http.StatusBadRequest, "BadRequest", "invalid resourceVersion")
			return
		}
		if len(s.events) > 0 && from < s.events[0].rv-1 {
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(expired(from, s.events[0].rv))
			w.Write([]byte("\n"))
			return
		}
		for i := range s.events {
			if e := &s.events[i]; e.rv > from && e.kind == kind {
				backlog = append(backlog, e.data(partial))
			}
		}
	}
	s.watchers[wt] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.watchers, wt)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	for _, b := range backlog {
		if _, err := w.Write(append(b, '\n')); err != nil {
			return
		}
	}
	flusher.Flush()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-wt.done:
			return
		case <-deadline.C:
			return
		case b := <-wt.ch:
			if _, err := w.Write(append(b, '\n')); err != nil {
				return
			}
			// Drain what is already queued before flushing.
			for more := true; more; {
				select {
				case b := <-wt.ch:
					if _, err := w.Write(append(b, '\n')); err != nil {
						return
					}
				default:
					more = false
				}
			}
			flusher.Flush()
		case <-tick.C:
			if bookmarks {
				s.mu.Lock()
				rv := s.rv
				s.mu.Unlock()
				if _, err := w.Write(append(bookmark(k, rv, false, partial), '\n')); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// publishLocked records a change and sends it to the watchers of its kind.
// obj must not change afterwards.
func (s *Sim) publishLocked(kind string, t watch.EventType, obj runtime.Object) {
	ev := event{rv: s.rv, kind: kind, t: t, obj: obj}
	ev.data(false)
	s.events = append(s.events, ev)
	if len(s.events) > maxEvents {
		s.events = append([]event(nil), s.events[len(s.events)-maxEvents/2:]...)
	}
	for wt := range s.watchers {
		if wt.kind != kind {
			continue
		}
		select {
		case wt.ch <- ev.data(wt.partial):
		default:
			// A watcher that cannot keep up is closed; client-go resumes it.
			wt.close()
		}
	}
}

// addLocked stores a new object of any kind but nodes and pods, with a
// fresh resource version, and tells the watchers.
func (s *Sim) addLocked(obj runtime.Object) {
	s.storeLocked(obj, watch.Added)
}

// storeLocked stores an object of any kind but nodes and pods and tells the
// watchers.
func (s *Sim) storeLocked(obj runtime.Object, t watch.EventType) {
	k, ok := kube.KindOf(obj.GetObjectKind().GroupVersionKind().Kind)
	if !ok {
		panic("sim: no kind for " + obj.GetObjectKind().GroupVersionKind().String())
	}
	m, _ := meta.Accessor(obj)
	s.rv++
	m.SetResourceVersion(strconv.FormatInt(s.rv, 10))
	key := m.GetName()
	if k.Namespaced {
		key = m.GetNamespace() + "/" + key
	}
	if s.objects[k.Name] == nil {
		s.objects[k.Name] = map[string]runtime.Object{}
	}
	if t == watch.Deleted {
		delete(s.objects[k.Name], key)
	} else {
		s.objects[k.Name][key] = obj
	}
	s.publishLocked(k.Name, t, obj)
}

func wrap(t watch.EventType, obj []byte) []byte {
	return []byte(`{"type":"` + string(t) + `","object":` + string(obj) + `}`)
}

func bookmark(k kube.Kind, rv int64, initialEnd, partial bool) []byte {
	kind, apiVersion := k.Kind, k.APIVersion()
	if partial {
		kind, apiVersion = "PartialObjectMetadata", "meta.k8s.io/v1"
	}
	meta := fmt.Sprintf(`{"resourceVersion":"%d"}`, rv)
	if initialEnd {
		meta = fmt.Sprintf(`{"resourceVersion":"%d","annotations":{"k8s.io/initial-events-end":"true"}}`, rv)
	}
	return []byte(fmt.Sprintf(`{"type":"BOOKMARK","object":{"kind":%q,"apiVersion":%q,"metadata":%s}}`, kind, apiVersion, meta))
}

func expired(from, oldest int64) []byte {
	st := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Message:  fmt.Sprintf("too old resource version: %d (%d)", from, oldest),
		Reason:   metav1.StatusReasonExpired,
		Code:     http.StatusGone,
	}
	b, _ := json.Marshal(st)
	return wrap(watch.Error, b)
}

func status(w http.ResponseWriter, code int, reason metav1.StatusReason, msg string) {
	st := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Message: msg, Reason: reason, Code: int32(code),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(st)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------- metrics ----------

func (s *Sim) nodeMetrics(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.metrics {
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	now := s.opts.Now().UTC().Format(time.RFC3339)
	type item struct {
		Metadata  map[string]string `json:"metadata"`
		Timestamp string            `json:"timestamp"`
		Window    string            `json:"window"`
		Usage     map[string]string `json:"usage"`
	}
	names := make([]string, 0, len(s.nodes))
	for n := range s.nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	items := []item{}
	for _, n := range names {
		if !isReady(s.nodes[n]) {
			continue // metrics-server has no reading for a node that stopped reporting
		}
		u := s.usage[n]
		items = append(items, item{
			Metadata:  map[string]string{"name": n},
			Timestamp: now, Window: "20.035s",
			Usage: map[string]string{
				"cpu":    resource.NewMilliQuantity(u[0], resource.DecimalSI).String(),
				"memory": resource.NewQuantity(u[1], resource.BinarySI).String(),
			},
		})
	}
	writeJSON(w, map[string]any{"kind": "NodeMetricsList", "apiVersion": "metrics.k8s.io/v1beta1", "metadata": map[string]string{}, "items": items})
}

// ---------- kubelet stats, through the API server's node proxy ----------

// kubeletSummary answers as the API server does when it proxies a
// kubelet's Summary API: the boot time in node.startTime, 403 without the
// permission, 503 when the kubelet cannot be reached.
func (s *Sim) kubeletSummary(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.opts.KubeletStats {
		status(w, http.StatusForbidden, "Forbidden", fmt.Sprintf(`nodes %q is forbidden: User "system:serviceaccount:221b:console" cannot get resource "nodes/proxy" in API group "" at the cluster scope`, name))
		return
	}
	s.mu.Lock()
	n, ok := s.nodes[name]
	boot := s.boots[name]
	usage := s.usage[name]
	s.mu.Unlock()
	if !ok {
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("nodes %q not found", name))
		return
	}
	if !isReady(n) {
		status(w, http.StatusServiceUnavailable, "ServiceUnavailable", "error trying to reach service: dial tcp 10.0.0.10:10250: connect: connection refused")
		return
	}
	now := s.opts.Now().UTC().Format(time.RFC3339)
	writeJSON(w, map[string]any{
		"node": map[string]any{
			"nodeName":  name,
			"startTime": boot.UTC().Format(time.RFC3339),
			"cpu":       map[string]any{"time": now, "usageNanoCores": usage[0] * 1e6},
			"memory":    map[string]any{"time": now, "workingSetBytes": usage[1]},
		},
		"pods": []any{},
	})
}

// reboot gives a node a new boot ID and boot time, as a restart does.
func (s *Sim) reboot(r *http.Request) error {
	name := r.PathValue("name")
	now := s.opts.Now()
	return s.updateNode(name, func(n *corev1.Node) {
		n.Status.NodeInfo.BootID = bootID(name, now)
		s.boots[name] = now
	})
}

// ---------- ConfigMaps, for the console's settings ----------

// patchConfigMap applies a JSON merge patch to the data, as the console
// sends one.
func (s *Sim) patchConfigMap(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("ns") + "/" + r.PathValue("name")
	if ct := r.Header.Get("Content-Type"); ct != string(types.MergePatchType) {
		status(w, http.StatusUnsupportedMediaType, "UnsupportedMediaType", "the simulator takes application/merge-patch+json only, got "+ct)
		return
	}
	var patch struct {
		Data map[string]*string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		status(w, http.StatusBadRequest, "BadRequest", "invalid patch: "+err.Error())
		return
	}
	s.mu.Lock()
	cm, ok := s.objects["configmaps"][key].(*corev1.ConfigMap)
	var out []byte
	if ok {
		cp := cm.DeepCopy()
		if cp.Data == nil {
			cp.Data = map[string]string{}
		}
		for k, v := range patch.Data {
			if v == nil {
				delete(cp.Data, k)
			} else {
				cp.Data[k] = *v
			}
		}
		s.storeLocked(cp, watch.Modified)
		out, _ = json.Marshal(cp)
	}
	s.mu.Unlock()
	if !ok {
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("configmaps %q not found", r.PathValue("name")))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ---------- admin API ----------

func (s *Sim) state(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	objects := map[string]int{}
	for kind, m := range s.objects {
		objects[kind] = len(m)
	}
	writeJSON(w, map[string]any{"nodes": len(s.nodes), "pods": len(s.pods), "objects": objects, "resourceVersion": s.rv, "down": s.down, "refused": s.refused, "metrics": s.metrics, "watchers": len(s.watchers)})
}

func (s *Sim) cordon(r *http.Request) error {
	on := r.URL.Query().Get("on") != "false"
	return s.updateNode(r.PathValue("name"), func(n *corev1.Node) {
		n.Spec.Unschedulable = on
		if on {
			n.Spec.Taints = appendTaint(n.Spec.Taints, corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule})
		} else {
			n.Spec.Taints = removeTaint(n.Spec.Taints, corev1.TaintNodeUnschedulable)
		}
	})
}

func (s *Sim) ready(r *http.Request) error {
	st := corev1.ConditionStatus(r.URL.Query().Get("status"))
	if st != corev1.ConditionTrue && st != corev1.ConditionFalse && st != corev1.ConditionUnknown {
		return fmt.Errorf("status must be True, False or Unknown")
	}
	return s.updateNode(r.PathValue("name"), func(n *corev1.Node) { setReady(n, st, s.opts.Now()) })
}

func (s *Sim) condition(r *http.Request) error {
	t := corev1.NodeConditionType(r.URL.Query().Get("type"))
	st := corev1.ConditionStatus(r.URL.Query().Get("status"))
	if t == "" || (st != corev1.ConditionTrue && st != corev1.ConditionFalse) {
		return fmt.Errorf("give type and status=True|False")
	}
	return s.updateNode(r.PathValue("name"), func(n *corev1.Node) { setCondition(n, t, st, s.opts.Now()) })
}

func (s *Sim) addNode(r *http.Request) error {
	var body struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		return fmt.Errorf("give a JSON body with a name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[body.Name]; ok {
		return fmt.Errorf("node %s exists", body.Name)
	}
	n := s.newNode(body.Name, body.Role == "control-plane", len(s.nodes), s.opts.Now())
	s.rv++
	n.ResourceVersion = strconv.FormatInt(s.rv, 10)
	s.nodes[n.Name] = n
	s.usage[n.Name] = [2]int64{n.Status.Allocatable.Cpu().MilliValue() / 10, n.Status.Allocatable.Memory().Value() / 10}
	s.publishLocked("nodes", watch.Added, n)
	return nil
}

func (s *Sim) deleteNode(r *http.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[r.PathValue("name")]
	if !ok {
		return fmt.Errorf("no node %s", r.PathValue("name"))
	}
	delete(s.nodes, n.Name)
	delete(s.boots, n.Name)
	delete(s.usage, n.Name)
	s.rv++
	n.ResourceVersion = strconv.FormatInt(s.rv, 10)
	s.publishLocked("nodes", watch.Deleted, n)
	return nil
}

func (s *Sim) crashPod(r *http.Request) error {
	return s.updatePod(r.PathValue("ns"), r.PathValue("name"), func(p *corev1.Pod) { crashLoop(p, s.opts.Now()) })
}

func (s *Sim) healPod(r *http.Request) error {
	return s.updatePod(r.PathValue("ns"), r.PathValue("name"), func(p *corev1.Pod) { runReady(p, s.opts.Now()) })
}

func (s *Sim) deletePod(r *http.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.PathValue("ns") + "/" + r.PathValue("name")
	p, ok := s.pods[key]
	if !ok {
		return fmt.Errorf("no pod %s", key)
	}
	delete(s.pods, key)
	delete(s.podUsage, key)
	s.rv++
	p.ResourceVersion = strconv.FormatInt(s.rv, 10)
	s.publishLocked("pods", watch.Deleted, p)
	return nil
}

// externalIP gives a LoadBalancer Service an external IP (?ip=), or takes
// it away (?ip= empty), as a cloud controller would.
func (s *Sim) externalIP(r *http.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.PathValue("ns") + "/" + r.PathValue("name")
	svc, ok := s.objects["services"][key].(*corev1.Service)
	if !ok {
		return fmt.Errorf("no service %s", key)
	}
	cp := svc.DeepCopy()
	cp.Status.LoadBalancer.Ingress = nil
	if ip := r.URL.Query().Get("ip"); ip != "" {
		cp.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip}}
	}
	s.storeLocked(cp, watch.Modified)
	return nil
}

func (s *Sim) setDown(r *http.Request) error {
	on := r.URL.Query().Get("on") != "false"
	s.mu.Lock()
	s.down = on
	if on {
		for wt := range s.watchers {
			wt.close()
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Sim) setMetrics(r *http.Request) error {
	s.mu.Lock()
	s.metrics = r.URL.Query().Get("on") != "false"
	s.mu.Unlock()
	return nil
}

func (s *Sim) churnOnce(r *http.Request) error {
	n, _ := strconv.Atoi(r.URL.Query().Get("pods"))
	if n <= 0 {
		n = 10
	}
	s.Churn(n)
	return nil
}

// blipFor is how long a pod churn made not ready stays that way.
const blipFor = 20 * time.Second

// Churn changes the usage of every node and makes n random pods not ready
// for a short while, the way a busy cluster moves: readiness blips come and
// go, so the share of pods not ready stays small however long it runs.
func (s *Sim) Churn(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	if s.blips == nil {
		s.blips = map[string]time.Time{}
	}
	for key, at := range s.blips {
		p := s.pods[key]
		if p == nil {
			delete(s.blips, key)
			continue
		}
		if now.Sub(at) < blipFor {
			continue
		}
		delete(s.blips, key)
		if p.Status.Phase == corev1.PodRunning && !podReady(p) {
			cp := p.DeepCopy()
			setPodReady(cp, true, now) // the container kept running; only readiness returns
			s.putPodLocked(cp, watch.Modified)
		}
	}
	for name, u := range s.usage {
		node := s.nodes[name]
		if node == nil {
			continue
		}
		u[0] = clampInt(u[0]+int64(s.rnd.Intn(401)-200), 0, node.Status.Allocatable.Cpu().MilliValue())
		u[1] = clampInt(u[1]+int64(s.rnd.Intn(257)-128)*1024*1024, 0, node.Status.Allocatable.Memory().Value())
		s.usage[name] = u
	}
	for key, u := range s.podUsage {
		p := s.pods[key]
		if p == nil {
			delete(s.podUsage, key)
			continue
		}
		req := p.Spec.Containers[0].Resources.Requests
		u[0] = clampInt(u[0]+int64(s.rnd.Intn(21)-10), 1, max(2, req.Cpu().MilliValue()*2))
		u[1] = clampInt(u[1]+int64(s.rnd.Intn(9)-4)*1024*1024, 4*1024*1024, max(8*1024*1024, req.Memory().Value()*2))
		s.podUsage[key] = u
	}
	if len(s.pods) == 0 {
		return
	}
	keys := make([]string, 0, len(s.pods))
	for k := range s.pods {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i := 0; i < n; i++ {
		key := keys[s.rnd.Intn(len(keys))]
		p := s.pods[key]
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning || !podReady(p) {
			continue
		}
		cp := p.DeepCopy()
		setPodReady(cp, false, now)
		s.putPodLocked(cp, watch.Modified)
		s.blips[key] = now
	}
}

func (s *Sim) updateNode(name string, f func(*corev1.Node)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[name]
	if !ok {
		return fmt.Errorf("no node %s", name)
	}
	cp := n.DeepCopy()
	f(cp)
	s.rv++
	cp.ResourceVersion = strconv.FormatInt(s.rv, 10)
	s.nodes[name] = cp
	s.publishLocked("nodes", watch.Modified, cp)
	return nil
}

func (s *Sim) updatePod(ns, name string, f func(*corev1.Pod)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pods[ns+"/"+name]
	if !ok {
		return fmt.Errorf("no pod %s/%s", ns, name)
	}
	cp := p.DeepCopy()
	f(cp)
	s.putPodLocked(cp, watch.Modified)
	return nil
}

func (s *Sim) putPodLocked(p *corev1.Pod, t watch.EventType) {
	s.rv++
	p.ResourceVersion = strconv.FormatInt(s.rv, 10)
	s.pods[p.Namespace+"/"+p.Name] = p
	s.publishLocked("pods", t, p)
}

// ---------- the generated cluster ----------

var (
	zones      = []string{"zone-a", "zone-b", "zone-c"}
	namespaces = []string{"default", "analytics", "ingest", "web", "monitoring", "storage", "batch"}
	apps       = []string{"api", "worker", "frontend", "cache", "queue", "indexer", "scheduler", "gateway", "exporter", "loader"}
)

func (s *Sim) generate() {
	now := s.opts.Now()
	cps := 3
	if s.opts.Nodes < 6 {
		cps = 1
	}
	for i := 0; i < s.opts.Nodes; i++ {
		var n *corev1.Node
		if i < cps {
			n = s.newNode(fmt.Sprintf("cp-%d", i+1), true, i, now)
		} else {
			n = s.newNode(fmt.Sprintf("worker-%03d", i-cps+1), false, i, now)
		}
		s.nodes[n.Name] = n
	}
	workers := []string{}
	for name, n := range s.nodes {
		if _, cp := n.Labels["node-role.kubernetes.io/control-plane"]; !cp {
			workers = append(workers, name)
		}
	}
	sort.Strings(workers)
	all := s.sortedNodeNames()

	// One system pod per node, like a DaemonSet, then the workload pods.
	count := 0
	for _, name := range all {
		if count >= s.opts.Pods {
			break
		}
		p := s.newPod("kube-system", "node-agent-"+suffix(s.rnd, 5), name, 50, 64, now)
		s.pods[p.Namespace+"/"+p.Name] = p
		count++
	}
	// Size requests so the workers end up about two-thirds full, then place
	// each pod where it fits, as the scheduler would.
	var totalCPU, totalMem int64
	for _, w := range workers {
		totalCPU += s.nodes[w].Status.Allocatable.Cpu().MilliValue()
		totalMem += s.nodes[w].Status.Allocatable.Memory().Value() / (1024 * 1024)
	}
	workload := max(1, s.opts.Pods-count)
	cpuScale := min(1.0, float64(totalCPU)*0.68/(float64(workload)*325))
	memScale := min(1.0, float64(totalMem)*0.62/(float64(workload)*800))
	used := map[string][3]int64{} // cpu milli, memory Mi, pods
	fits := func(w string, cpu, mem int64) bool {
		n, u := s.nodes[w], used[w]
		return u[0]+cpu <= n.Status.Allocatable.Cpu().MilliValue() &&
			u[1]+mem <= n.Status.Allocatable.Memory().Value()/(1024*1024) &&
			u[2]+1 <= n.Status.Allocatable.Pods().Value()-2
	}
	for i := 0; count < s.opts.Pods; i++ {
		ns := namespaces[i%len(namespaces)]
		app := apps[(i/len(namespaces))%len(apps)]
		cpu := max(10, int64(float64(50+s.rnd.Intn(12)*50)*cpuScale))
		mem := max(16, int64(float64(64*(1+s.rnd.Intn(24)))*memScale))
		node := ""
		for try := 0; try < 12 && len(workers) > 0; try++ {
			w := workers[s.rnd.Intn(len(workers))]
			if fits(w, cpu, mem) {
				node = w
				break
			}
		}
		if node != "" {
			u := used[node]
			used[node] = [3]int64{u[0] + cpu, u[1] + mem, u[2] + 1}
		}
		p := s.newPod(ns, fmt.Sprintf("%s-%s-%s", app, suffix(s.rnd, 9), suffix(s.rnd, 5)), node, cpu, mem, now)
		s.pods[p.Namespace+"/"+p.Name] = p
		count++
	}

	if s.opts.Problems && len(workers) >= 4 {
		setReady(s.nodes[workers[len(workers)-1]], corev1.ConditionUnknown, now.Add(-47*time.Minute))
		cordon := s.nodes[workers[len(workers)-2]]
		cordon.Spec.Unschedulable = true
		cordon.Spec.Taints = appendTaint(cordon.Spec.Taints, corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule})
		setCondition(s.nodes[workers[len(workers)-3]], corev1.NodeDiskPressure, corev1.ConditionTrue, now.Add(-12*time.Minute))
		keys := make([]string, 0, len(s.pods))
		for k := range s.pods {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		bad := max(2, s.opts.Pods/200)
		for i := 0; i < bad && i < len(keys); i++ {
			p := s.pods[keys[(i*7919)%len(keys)]]
			if p.Spec.NodeName == "" || p.Namespace == "kube-system" {
				continue
			}
			switch i % 3 {
			case 0:
				crashLoop(p, now)
			case 1:
				imagePullBackOff(p, now)
			default:
				setPodReady(p, false, now.Add(-9*time.Minute))
			}
		}
		for i := 0; i < 2; i++ {
			p := s.newPod("batch", "report-"+suffix(s.rnd, 5), "", 4000, 32768, now.Add(-15*time.Minute))
			p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
				Message: "0/" + strconv.Itoa(s.opts.Nodes) + " nodes are available: insufficient cpu.", LastTransitionTime: metav1.NewTime(now.Add(-15 * time.Minute)),
			}}}
			s.pods[p.Namespace+"/"+p.Name] = p
		}
	}

	// Usage: a share of what pods request, varied per node.
	requested := map[string][2]int64{}
	for _, p := range s.pods {
		if p.Spec.NodeName == "" {
			continue
		}
		r := requested[p.Spec.NodeName]
		for _, c := range p.Spec.Containers {
			r[0] += c.Resources.Requests.Cpu().MilliValue()
			r[1] += c.Resources.Requests.Memory().Value()
		}
		requested[p.Spec.NodeName] = r
	}
	for name, n := range s.nodes {
		f := 0.35 + s.rnd.Float64()*0.6
		r := requested[name]
		s.usage[name] = [2]int64{
			clampInt(int64(float64(r[0])*f)+100, 0, n.Status.Allocatable.Cpu().MilliValue()),
			clampInt(int64(float64(r[1])*f)+512*1024*1024, 0, n.Status.Allocatable.Memory().Value()),
		}
	}

	// Every object gets its own resource version, nodes first.
	for _, name := range all {
		s.rv++
		s.nodes[name].ResourceVersion = strconv.FormatInt(s.rv, 10)
	}
	s.generateObjects(now)

	// Pod usage: a share of the request, varied per pod. After the objects,
	// which rename the StatefulSet pods.
	for key, p := range s.pods {
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		req := p.Spec.Containers[0].Resources.Requests
		f := 0.2 + s.rnd.Float64()*0.9
		s.podUsage[key] = [2]int64{max(1, int64(float64(req.Cpu().MilliValue())*f)), max(4*1024*1024, int64(float64(req.Memory().Value())*f))}
	}
	keys := make([]string, 0, len(s.pods))
	for k := range s.pods {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.rv++
		s.pods[k].ResourceVersion = strconv.FormatInt(s.rv, 10)
	}
}

func (s *Sim) sortedNodeNames() []string {
	out := make([]string, 0, len(s.nodes))
	for n := range s.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *Sim) newNode(name string, controlPlane bool, i int, now time.Time) *corev1.Node {
	z := zones[i%len(zones)]
	cpu, mem, pods := "16", "64Gi", "110"
	instance := "general-16x64"
	if !controlPlane && i%5 == 4 {
		cpu, mem, instance = "32", "256Gi", "memory-32x256"
	}
	if controlPlane {
		cpu, mem, instance = "8", "32Gi", "general-8x32"
	}
	kubelet := s.opts.Version
	if !controlPlane && i%17 == 9 {
		kubelet = "v1.36.5"
	}
	labels := map[string]string{
		"kubernetes.io/hostname":                                  name,
		"kubernetes.io/os":                                        "linux",
		"kubernetes.io/arch":                                      "amd64",
		"beta.kubernetes.io/os":                                   "linux",
		"beta.kubernetes.io/arch":                                 "amd64",
		corev1.LabelTopologyZone:                                  z,
		corev1.LabelTopologyRegion:                                "region-1",
		corev1.LabelInstanceTypeStable:                            instance,
		"node.kubernetes.io/exclude-from-external-load-balancers": "",
	}
	var taints []corev1.Taint
	if controlPlane {
		labels["node-role.kubernetes.io/control-plane"] = ""
		taints = append(taints, corev1.Taint{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule})
	} else {
		delete(labels, "node.kubernetes.io/exclude-from-external-load-balancers")
		labels["node-role.kubernetes.io/worker"] = ""
		if i%5 == 4 {
			labels["workload"] = "analytics"
			taints = append(taints, corev1.Taint{Key: "dedicated", Value: "analytics", Effect: corev1.TaintEffectNoSchedule})
		}
	}
	alloc := func(c, m, p string) corev1.ResourceList {
		return corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse(c),
			corev1.ResourceMemory:           resource.MustParse(m),
			corev1.ResourcePods:             resource.MustParse(p),
			corev1.ResourceEphemeralStorage: resource.MustParse("190Gi"),
		}
	}
	allocCPU := map[string]string{"8": "7800m", "16": "15800m", "32": "31750m"}[cpu]
	allocMem := map[string]string{"32Gi": "30Gi", "64Gi": "61Gi", "256Gi": "250Gi"}[mem]
	created := now.Add(-time.Duration(40+i%30) * 24 * time.Hour).Add(-time.Duration(i*37) * time.Minute)
	n := &corev1.Node{
		TypeMeta:   metav1.TypeMeta{Kind: "Node", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid(name), Labels: labels, CreationTimestamp: metav1.NewTime(created.Truncate(time.Second))},
		Spec:       corev1.NodeSpec{Taints: taints, PodCIDR: fmt.Sprintf("10.244.%d.0/24", i%250)},
		Status: corev1.NodeStatus{
			Capacity:    alloc(cpu, mem, pods),
			Allocatable: alloc(allocCPU, allocMem, pods),
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: fmt.Sprintf("10.0.%d.%d", i/250, 10+i%250)},
				{Type: corev1.NodeHostName, Address: name},
			},
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion: kubelet, OSImage: "Ubuntu 24.04.3 LTS", KernelVersion: "6.8.0-79-generic",
				ContainerRuntimeVersion: "containerd://2.1.4", OperatingSystem: "linux", Architecture: "amd64",
				BootID: bootID(name, created), MachineID: string(uid("machine-" + name)), SystemUUID: strings.ToUpper(string(uid("system-" + name))),
			},
		},
	}
	// Booted a minute and a half before the kubelet joined the cluster.
	s.boots[name] = created.Add(-90 * time.Second).Truncate(time.Second)
	since := created.Add(3 * time.Minute)
	for _, t := range []corev1.NodeConditionType{corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure} {
		setCondition(n, t, corev1.ConditionFalse, since)
	}
	setReady(n, corev1.ConditionTrue, since)
	return n
}

func (s *Sim) newPod(ns, name, node string, cpuMilli, memMi int64, now time.Time) *corev1.Pod {
	started := now.Add(-time.Duration(30+s.rnd.Intn(60*24*20)) * time.Minute).Truncate(time.Second)
	limits := corev1.ResourceList{corev1.ResourceMemory: *resource.NewQuantity(memMi*2*1024*1024, resource.BinarySI)}
	if s.rnd.Intn(3) == 0 {
		limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpuMilli*2, resource.DecimalSI)
	}
	p := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid(ns + "/" + name), CreationTimestamp: metav1.NewTime(started)},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "registry.example/" + strings.SplitN(name, "-", 2)[0] + ":1.0",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
						corev1.ResourceMemory: *resource.NewQuantity(memMi*1024*1024, resource.BinarySI),
					},
					Limits: limits,
				},
			}},
		},
	}
	if node == "" {
		p.Status = corev1.PodStatus{Phase: corev1.PodPending}
		return p
	}
	st := metav1.NewTime(started)
	p.Status.StartTime = &st
	runReady(p, started)
	if s.rnd.Intn(150) == 0 && ns == "batch" {
		p.Status.Phase = corev1.PodSucceeded
		p.Status.ContainerStatuses[0].Ready = false
		p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}
		setPodReady(p, false, started.Add(4*time.Minute))
	}
	return p
}

func runReady(p *corev1.Pod, at time.Time) {
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "main", Ready: true, Started: ptrBool(true), RestartCount: restarts(p),
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(at)}},
	}}
	setPodReady(p, true, at)
}

func crashLoop(p *corev1.Pod, now time.Time) {
	p.Status.Phase = corev1.PodRunning
	n := restarts(p) + 6
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "main", Ready: false, RestartCount: n,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container=main pod=" + p.Name + "_" + p.Namespace + "(" + string(p.UID) + ")",
		}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(now.Add(-40 * time.Second))}},
	}}
	setPodReady(p, false, now.Add(-30*time.Minute))
}

func imagePullBackOff(p *corev1.Pod, now time.Time) {
	p.Status.Phase = corev1.PodPending
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "main", Ready: false,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "ImagePullBackOff", Message: `Back-off pulling image "` + p.Spec.Containers[0].Image + `"`,
		}},
	}}
	setPodReady(p, false, now.Add(-22*time.Minute))
}

func restarts(p *corev1.Pod) int32 {
	if len(p.Status.ContainerStatuses) > 0 {
		return p.Status.ContainerStatuses[0].RestartCount
	}
	return 0
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func setPodReady(p *corev1.Pod, ready bool, at time.Time) {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	conds := []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: p.CreationTimestamp},
		{Type: corev1.PodInitialized, Status: corev1.ConditionTrue, LastTransitionTime: p.CreationTimestamp},
		{Type: corev1.ContainersReady, Status: st, LastTransitionTime: metav1.NewTime(at)},
		{Type: corev1.PodReady, Status: st, LastTransitionTime: metav1.NewTime(at)},
	}
	if !ready {
		conds[2].Reason, conds[3].Reason = "ContainersNotReady", "ContainersNotReady"
		conds[3].Message = "containers with unready status: [main]"
	}
	p.Status.Conditions = conds
	for i := range p.Status.ContainerStatuses {
		if p.Status.ContainerStatuses[i].State.Running != nil {
			p.Status.ContainerStatuses[i].Ready = ready
		}
	}
}

func setReady(n *corev1.Node, st corev1.ConditionStatus, at time.Time) {
	c := corev1.NodeCondition{Type: corev1.NodeReady, Status: st, LastHeartbeatTime: metav1.NewTime(at), LastTransitionTime: metav1.NewTime(at)}
	switch st {
	case corev1.ConditionTrue:
		c.Reason, c.Message = "KubeletReady", "kubelet is posting ready status"
	case corev1.ConditionFalse:
		c.Reason, c.Message = "KubeletNotReady", "container runtime network not ready: NetworkReady=false reason:NetworkPluginNotReady message:Network plugin returns error: cni plugin not initialized"
	default:
		c.Reason, c.Message = "NodeStatusUnknown", "Kubelet stopped posting node status."
	}
	replaceCondition(n, c)
}

func setCondition(n *corev1.Node, t corev1.NodeConditionType, st corev1.ConditionStatus, at time.Time) {
	reasons := map[corev1.NodeConditionType][2]string{
		corev1.NodeMemoryPressure: {"KubeletHasSufficientMemory", "KubeletHasInsufficientMemory"},
		corev1.NodeDiskPressure:   {"KubeletHasNoDiskPressure", "KubeletHasDiskPressure"},
		corev1.NodePIDPressure:    {"KubeletHasSufficientPID", "KubeletHasInsufficientPID"},
	}
	c := corev1.NodeCondition{Type: t, Status: st, LastHeartbeatTime: metav1.NewTime(at), LastTransitionTime: metav1.NewTime(at)}
	if r, ok := reasons[t]; ok {
		if st == corev1.ConditionTrue {
			c.Reason = r[1]
			c.Message = "kubelet has " + strings.ToLower(strings.TrimPrefix(r[1], "KubeletHas"))
		} else {
			c.Reason = r[0]
			c.Message = "kubelet has " + strings.ToLower(strings.TrimPrefix(r[0], "KubeletHas"))
		}
	}
	replaceCondition(n, c)
}

func replaceCondition(n *corev1.Node, c corev1.NodeCondition) {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == c.Type {
			n.Status.Conditions[i] = c
			return
		}
	}
	n.Status.Conditions = append(n.Status.Conditions, c)
}

func isReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func appendTaint(ts []corev1.Taint, t corev1.Taint) []corev1.Taint {
	for _, x := range ts {
		if x.Key == t.Key && x.Effect == t.Effect {
			return ts
		}
	}
	return append(ts, t)
}

func removeTaint(ts []corev1.Taint, key string) []corev1.Taint {
	out := ts[:0:0]
	for _, x := range ts {
		if x.Key != key {
			out = append(out, x)
		}
	}
	return out
}

// bootID is what a kernel reports in /proc/sys/kernel/random/boot_id: a
// UUID that changes with every boot.
func bootID(name string, at time.Time) string {
	return string(uid(name + "@" + at.UTC().Format(time.RFC3339)))
}

func uid(seed string) types.UID {
	h := uint64(1469598103934665603)
	for i := 0; i < len(seed); i++ {
		h ^= uint64(seed[i])
		h *= 1099511628211
	}
	return types.UID(fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x", uint32(h), uint16(h>>32), uint16(h>>44)&0xfff, uint16(h>>20)&0xfff, h&0xffffffffffff))
}

const suffixChars = "bcdfghjklmnpqrstvwxz2456789"

func suffix(r *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = suffixChars[r.Intn(len(suffixChars))]
	}
	return string(b)
}

func clampInt(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ptrBool(b bool) *bool { return &b }
