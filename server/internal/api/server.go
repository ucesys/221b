// Package api serves the browser app, the JSON API and the live event
// stream, plus health and metrics endpoints.
package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	corev1 "k8s.io/api/core/v1"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/views"
	"github.com/ucesys/221b/server/internal/watson"
)

// Store is what the API reads rows from; *live.Store implements it.
type Store interface {
	Snapshot(views []string, f live.Filter) []byte
	Row(view, key string) ([]byte, bool)
	Subscribe(views []string, f live.Filter) (*live.Sub, []byte, error)
	Unsubscribe(*live.Sub)
	Stats() (views []live.ViewStats, subscribers int, recomputes uint64)
	Delays() live.Histogram
}

// Cluster is what the detail endpoints read; *kube.Cluster implements it.
// nil leaves them unserved.
type Cluster interface {
	views.Source
	PodUsageOf(namespace, name string) (nodes.Usage, bool)
	ObjectYAML(ctx context.Context, p kube.Person, k kube.Kind, namespace, name string) (string, error)
	Logs(ctx context.Context, p kube.Person, namespace, name string, o kube.LogOptions) (io.ReadCloser, error)
	CanI(ctx context.Context, p kube.Person, verb, group, resource, namespace string) (bool, error)
	// Secret editing (Phase 10): live, as the person, never cached.
	GetSecret(ctx context.Context, p kube.Person, namespace, name string) (*corev1.Secret, error)
	UpdateSecret(ctx context.Context, p kube.Person, sec *corev1.Secret) (*corev1.Secret, error)
	CreateSecret(ctx context.Context, p kube.Person, sec *corev1.Secret) (*corev1.Secret, error)
	// CreateNamespace makes a namespace, as the person (Phase 10).
	CreateNamespace(ctx context.Context, p kube.Person, name string, labels map[string]string) error
}

// Settings reads and changes the shared settings; *settings.Service
// implements it.
type Settings interface {
	Current() settings.State
	Update(ctx context.Context, s settings.Settings) (settings.State, error)
	// UpdateFleets replaces a cluster's fleets (P4-01).
	UpdateFleets(ctx context.Context, cluster string, list []fleets.Fleet) (settings.State, error)
	// UpdateAcks changes a cluster's acknowledged alerts.
	UpdateAcks(ctx context.Context, cluster string, change func([]acks.Ack) []acks.Ack) (settings.State, error)
	// UpdateSherlock replaces Sherlock's settings (P5-05).
	UpdateSherlock(ctx context.Context, sh settings.Sherlock) (settings.State, error)
	// UpdateWatson replaces Watson's settings (P6-07).
	UpdateWatson(ctx context.Context, w settings.Watson) (settings.State, error)
}

// Auth gates requests by session and serves the sign-in endpoints;
// *auth.Service implements it. nil leaves everything open, as before
// Phase 3.
type Auth interface {
	// Gate returns the request with its session attached, or nil after
	// answering it (a 401, or the sign-in page).
	Gate(w http.ResponseWriter, r *http.Request) *http.Request
	Routes() []auth.Route
}

// Clusters resolves connections (P3-09) and manages them (P3-05 to
// P3-08); *clusters.Manager implements it.
type Clusters interface {
	Lookup(id string) (*live.Store, clusters.Client, bool)
	List() []clusters.Summary
	Summary(id string) (clusters.Summary, bool)
	Add(ctx context.Context, d clusters.Draft) (clusters.Summary, error)
	Update(ctx context.Context, id string, d clusters.Draft) (clusters.Summary, error)
	Remove(ctx context.Context, id string) error
	TestDraft(ctx context.Context, id string, d clusters.Draft) (clusters.TestResult, error)
	TestSaved(ctx context.Context, id string) (clusters.TestResult, error)
}

// Options configure the HTTP handler.
type Options struct {
	// Store and Cluster are the local cluster's; Clusters, when set,
	// resolves every connection, the local one included.
	Store    Store
	Clusters Clusters
	Auth     Auth
	// Audit records the settings changes; nil records nothing.
	Audit *audit.Log
	// KV is the console's small key-value storage next to the settings
	// (Sherlock's spend, the Toolbox's session records, the audit log),
	// for the backup; nil backs none of it up.
	KV kv.Backend
	// Version is the console's version, written into a backup.
	Version string
	// Cluster serves the details of pods, services, releases, logs and YAML.
	Cluster Cluster
	// Settings serves /api/v1/settings; nil leaves the path unserved.
	Settings Settings
	// Sherlock answers questions (Phase 5); nil leaves it out.
	Sherlock *sherlock.Service
	// SherlockKeys holds the Claude API key.
	SherlockKeys *sherlock.Keys
	// Watson runs kubectl and helm commands (Phase 6); nil leaves it out.
	Watson *watson.Service
	// WatsonLocalWrite allows changes in the one cluster of a console
	// without connections (tests).
	WatsonLocalWrite bool
	// Ready reports whether the cluster has been listed; it drives /readyz.
	Ready func() bool
	// UI serves the browser app.
	UI http.Handler
	// Gauges adds cluster figures to /metrics.
	Gauges func() []Gauge
	Logger *slog.Logger
	// MaxStreams caps concurrent event streams.
	MaxStreams int
	// Leader, with several replicas, says whether this one reads the
	// clusters and runs Sherlock and Watson; the others forward every
	// data request to it (P7-02, ADR-70). nil is a single replica.
	Leader Leader
	// Active says whether this replica reads the clusters now (ADR-70):
	// the role checks run only while it does. nil: always.
	Active func() bool
	// Standby says whether this replica is a cold standby, holding no
	// watches (ADR-70): it is ready as soon as it knows the leader, since
	// it forwards everything to it. nil: never.
	Standby func() bool
	// ProxyToken is shared by the replicas: a forwarded request carries it,
	// and only then is its client address believed.
	ProxyToken string
	// SherlockDefaultURL is the Claude API's address when the settings name
	// no provider; SherlockProviderLocked keeps the settings from changing
	// the provider (the chart's sherlock.lockProvider).
	SherlockDefaultURL     string
	SherlockProviderLocked bool
	// SherlockDefaultProvider is the provider when the settings name none
	// (the chart's sherlock.provider), and the only one when locked.
	SherlockDefaultProvider sherlock.Provider
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
	// HTTPSRedirect sends plain-HTTP requests (by X-Forwarded-Proto) to
	// HTTPS, for a TLS-terminating Ingress (P3-11).
	HTTPSRedirect bool
	// Access keeps what each person may see in each cluster (P3-02); nil
	// shows everyone everything, as with sign-in off.
	Access *access.Cache
	// AccessRecheck is how often an open stream asks again whether its
	// person's access has changed (what the cache has forgotten).
	AccessRecheck time.Duration
	// Impersonation maps the signed-in person to the user and groups the
	// clusters know them as.
	Impersonation Impersonation
	// Roles configure the Roles and Permissions pages and the check of the
	// deployed roles (RB-36, RB-41 to RB-44).
	Roles RolesOptions
	// SecretsReveal lets an Admin or SuperAdmin see a Secret's values, one
	// key at a time and audited (Phase 10, secrets.reveal); off, values
	// are only ever written.
	SecretsReveal bool
	// Apps keeps the Helm applications (Phase 10); nil, or no Watson,
	// leaves the Helm1 page's routes out.
	Apps apps.Store
	// Sessions runs the toolbox's sessions (Phase 9); nil, or no Watson,
	// leaves the toolbox out.
	Sessions *sessions.Manager
}

// Impersonation says how a person's name and groups are given to the API
// servers: with the prefixes their OIDC configuration adds
// (--oidc-username-prefix, --oidc-groups-prefix), so RBAC written for the
// people matches the console's reads for them.
type Impersonation struct {
	UserPrefix  string
	GroupPrefix string
}

// personOf is the person a session is, as the clusters know them.
func (i Impersonation) personOf(u auth.User) kube.Person {
	if u.Username == "" {
		return kube.Person{}
	}
	// The sign-in works out the name and groups the clusters get, with the
	// prefixes of where the person comes from, and only the groups the
	// chart lists (RB-15, RB-37); nothing is added or prefixed again here.
	if u.KubeName != "" {
		p := kube.Person{User: u.KubeName, Groups: append([]string(nil), u.KubeGroups...)}
		if !slices.Contains(p.Groups, "system:authenticated") {
			p.Groups = append(p.Groups, "system:authenticated")
		}
		return p
	}
	p := kube.Person{User: i.UserPrefix + u.Username}
	authenticated := false
	for _, g := range u.Groups {
		if strings.HasPrefix(g, "system:") {
			p.Groups = append(p.Groups, g)
			authenticated = authenticated || g == "system:authenticated"
		} else {
			p.Groups = append(p.Groups, i.GroupPrefix+g)
		}
	}
	// Every signed-in user carries system:authenticated in Kubernetes, and
	// RBAC bound to it (the discovery role, say) must apply to them.
	if !authenticated {
		p.Groups = append(p.Groups, "system:authenticated")
	}
	return p
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

	// leaderChecks is how often a forwarded stream checks the leader.
	leaderChecks time.Duration
	// patterns are the mux's, for the test that every route is either
	// forwarded or listed as local (ADR-70).
	patterns []string
	// roleCheck asks RunRoleChecks for a check now.
	roleCheck chan struct{}

	// drifts keeps the applications' drift checks (HA-09), by cluster,
	// release and person, for two minutes.
	drifts sync.Map
	// toolCache keeps what people may use where (TB-05); owners the
	// sign-in checks of people who asked for sessions.
	toolCache toolCache
	owners    sync.Map
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
	if o.AccessRecheck <= 0 {
		o.AccessRecheck = 30 * time.Second
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
	if o.Roles.Monitor == nil {
		o.Roles.Monitor = &roles.Monitor{}
	}
	s := &Server{opts: o, mux: http.NewServeMux(), reqs: map[reqKey]uint64{}, allowedHosts: map[string]bool{}, leaderChecks: leaderChecks, roleCheck: make(chan struct{}, 1)}
	for _, h := range o.AllowedHosts {
		if h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), "."); h != "" {
			s.allowedHosts[h] = true
		}
	}
	// Every data path exists for the local cluster as /api/v1/<path> and
	// for any connection as /api/v1/clusters/{cluster}/<path> (P3-09).
	s.data("snapshot", "snapshot", s.snapshot)
	s.data("nodes/{name}", "node", s.node)
	s.data("events", "events", s.events)
	if o.Cluster != nil || o.Clusters != nil {
		s.data("pods/{namespace}/{name}", "pod", s.pod)
		s.data("pods/{namespace}/{name}/log", "pod-log", s.podLog)
		s.data("services/{namespace}/{name}", "service", s.service)
		s.data("helm/{namespace}/{name}", "release", s.release)
		s.data("objects/{kind}/{namespace}/{name}", "object", s.object)
		s.data("yaml/{kind}/{name}", "yaml", s.yaml)
		s.data("yaml/{kind}/{namespace}/{name}", "yaml", s.yaml)
		s.data("netcheck", "netcheck", s.netCheck)
		// The Storage tab's scope and drawers (ST-13, ST-20).
		s.data("storage", "storage", s.storageScope)
		s.data("storage/claims/{namespace}/{name}", "storage-claim", s.storageClaim)
		s.data("storage/volumes/{name}", "storage-volume", s.storageVolume)
	}
	if o.Clusters != nil {
		s.handle("GET /api/v1/clusters", "clusters", s.listClusters)
		s.handle("GET /api/v1/home", "home", s.home)
		s.handle("POST /api/v1/clusters", "cluster-add", s.addCluster)
		s.handle("POST /api/v1/clusters/test", "cluster-test", s.testClusterDraft)
		s.handle("GET /api/v1/clusters/{cluster}", "cluster", s.getCluster)
		s.handle("PUT /api/v1/clusters/{cluster}", "cluster-update", s.updateCluster)
		s.handle("DELETE /api/v1/clusters/{cluster}", "cluster-remove", s.removeCluster)
		s.handle("POST /api/v1/clusters/{cluster}/test", "cluster-test", s.testCluster)
	}
	if o.Settings != nil {
		s.handle("GET /api/v1/settings", "settings", s.getSettings)
		s.handle("PUT /api/v1/settings", "settings-update", s.putSettings)
		s.handle("PUT /api/v1/fleets", "fleets-update", s.putFleets)
		s.handle("POST /api/v1/alerts/ack", "alert-ack", s.postAck)
		s.handle("POST /api/v1/clusters/{cluster}/alerts/ack", "alert-ack", s.postAck)
		s.handle("PUT /api/v1/clusters/{cluster}/fleets", "fleets-update", s.putFleets)
	}
	if o.Sherlock != nil && o.Settings != nil {
		for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
			s.handle("GET "+base+"sherlock", "sherlock", s.getSherlock)
			s.handle("GET "+base+"sherlock/events", "sherlock-events", s.sherlockEvents)
			s.handle("GET "+base+"sherlock/conversation", "sherlock-conversation", s.getSherlockConversation)
			s.handle("POST "+base+"sherlock/ask", "sherlock-ask", s.postSherlockAsk)
			s.handle("POST "+base+"sherlock/stop", "sherlock-stop", s.postSherlockStop)
			s.handle("POST "+base+"sherlock/new", "sherlock-new", s.postSherlockNew)
			s.handle("POST "+base+"sherlock/ui/{id}", "sherlock-ui", s.postSherlockUI)
		}
		s.handle("GET /api/v1/sherlock/config", "sherlock-config", s.getSherlockConfig)
		s.handle("GET /api/v1/sherlock/prompt", "sherlock-prompt", s.getSherlockPrompt)
		s.handle("POST /api/v1/sherlock/test", "sherlock-test", s.postSherlockTest)
		s.handle("PUT /api/v1/sherlock/config", "sherlock-config-update", s.putSherlockConfig)
		s.handle("PUT /api/v1/sherlock/key", "sherlock-key", s.putSherlockKey)
		s.handle("DELETE /api/v1/sherlock/key", "sherlock-key", s.deleteSherlockKey)
	}
	if o.Watson != nil {
		s.watsonRoutes()
	}
	if (o.Sherlock != nil && o.Settings != nil) || o.Watson != nil {
		for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
			s.handle("GET "+base+"assistants/events", "assistants-events", s.assistantsEvents)
		}
	}
	if o.Watson != nil && o.Apps != nil {
		s.appsRoutes()
	}
	s.secretsRoutes()
	if o.Watson != nil && o.Sessions != nil {
		s.toolboxRoutes()
		o.Watson.SetSessionHooks(s.planSession, s.startSession)
	}
	s.rolesRoutes()
	s.auditRoutes()
	s.backupRoutes()
	if o.Auth != nil {
		for _, r := range o.Auth.Routes() {
			s.handle(r.Pattern, r.Name, r.Handler)
		}
	}
	s.handle("GET /api/", "api-unknown", s.apiNotFound)
	s.handle("GET /healthz", "healthz", s.healthz)
	s.handle("GET /readyz", "readyz", s.readyz)
	s.handle("GET /metrics", "metrics", s.metrics)
	s.handle("GET /", "ui", o.UI.ServeHTTP)
	return s
}

func (s *Server) handle(pattern, route string, h http.HandlerFunc) {
	s.patterns = append(s.patterns, pattern)
	s.mux.Handle(pattern, s.observe(route, h))
}

// data registers a GET path for the local cluster and for any connection.
func (s *Server) data(path, route string, h http.HandlerFunc) {
	s.handle("GET /api/v1/"+path, route, h)
	s.handle("GET /api/v1/clusters/{cluster}/"+path, route, h)
}

// lookup finds the store and cluster a request is for: the {cluster} path
// value, or the local cluster without one. It answers 404 for an unknown
// connection and 503 for one whose watches are not running.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (Store, Cluster, bool) {
	id := r.PathValue("cluster")
	if id == "" {
		id = clusters.LocalID
	}
	if s.opts.Clusters == nil {
		if id != clusters.LocalID || s.opts.Store == nil {
			jsonError(w, http.StatusNotFound, "no cluster "+strconv.Quote(id))
			return nil, nil, false
		}
		return s.opts.Store, s.opts.Cluster, true
	}
	st, client, ok := s.opts.Clusters.Lookup(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "no cluster "+strconv.Quote(id))
		return nil, nil, false
	}
	cl, _ := client.(Cluster)
	if st == nil || cl == nil {
		jsonError(w, http.StatusServiceUnavailable, "the connection to "+strconv.Quote(id)+" is not running; see its cluster page")
		return nil, nil, false
	}
	return st, cl, true
}

// person is who the request is for, as the clusters know them: nobody
// with sign-in off, when the console reads as itself.
func (s *Server) person(r *http.Request) kube.Person {
	if s.opts.Auth == nil || s.opts.Access == nil {
		return kube.Person{}
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return kube.Person{}
	}
	return s.opts.Impersonation.personOf(sess.User)
}

// clusterID is the connection a data request is for.
func clusterID(r *http.Request) string {
	if id := r.PathValue("cluster"); id != "" {
		return id
	}
	return clusters.LocalID
}

// access is what the request's person may see in the cluster; nil for
// nobody, who sees everything.
func (s *Server) access(r *http.Request, cl Cluster) *access.Access {
	p := s.person(r)
	if p.Nobody() {
		return nil
	}
	return s.opts.Access.For(clusterID(r), cl, p)
}

// prepareTimeout bounds the reviews before a snapshot or a stream.
const prepareTimeout = 15 * time.Second

// prepare asks Kubernetes what the person may see of the views, if it
// has not been asked lately, and reports whether the answers changed.
func (s *Server) prepare(ctx context.Context, a *access.Access, cl Cluster, names []string) (bool, error) {
	if a == nil {
		return false, nil
	}
	pairs := access.Pairs(names, namespaceNames(cl))
	if !a.Stale(pairs) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	return a.Prepare(ctx, pairs, 0)
}

// namespaceNames lists the namespaces in the cache.
func namespaceNames(cl Cluster) []string {
	objs := cl.Objects("namespaces", "")
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		if m, ok := o.(metav1.Object); ok {
			out = append(out, m.GetName())
		}
	}
	return out
}

// mayList answers whether the person may list the resource in the
// namespace, as the detail endpoints need to know; 403 otherwise. The
// answer is taken from the stream's reviews when there is one, and asked
// for when there is not.
func (s *Server) mayList(w http.ResponseWriter, r *http.Request, cl Cluster, resource, namespace string) bool {
	a := s.access(r, cl)
	if a == nil {
		return true
	}
	pairs := []access.Pair{{Group: groupOf(resource), Resource: resource, Namespace: namespace}}
	if a.Stale(pairs) {
		ctx, cancel := context.WithTimeout(r.Context(), prepareTimeout)
		defer cancel()
		if _, err := a.Prepare(ctx, pairs, 0); err != nil {
			jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
			return false
		}
	}
	if !a.Namespaced(resource, namespace) {
		jsonError(w, http.StatusForbidden, fmt.Sprintf("Kubernetes does not let you list %s in %s", resource, namespace))
		return false
	}
	return true
}

func groupOf(resource string) string {
	for _, k := range kube.Kinds {
		if k.Resource == resource {
			return k.Group
		}
	}
	return ""
}

// accessState is the "access" stream event: why nothing is shown, or
// that Kubernetes answers again ("").
func accessState(err error) map[string]string {
	if err == nil {
		return map[string]string{"error": ""}
	}
	msg := "Kubernetes could not be asked what you may see: " + err.Error() + "."
	if apierrors.IsForbidden(err) {
		msg += " The console's account needs create on subjectaccessreviews there."
	}
	return map[string]string{"error": msg}
}

// ServeHTTP adds security headers and recovers from panics.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r.Host) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMisdirectedRequest)
		fmt.Fprintf(w, "221B does not answer to the host name %q. Open it as localhost or by IP address, or add the name to its allowed hosts.\n", r.Host)
		return
	}
	// A request another replica forwarded says whose it is (P7-02).
	forwarded := s.trustForwarded(r)
	h := w.Header()
	for _, kv := range securityHeaders {
		h.Set(kv[0], kv[1])
	}
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			s.opts.Logger.Error("handler panicked", "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	if s.opts.HTTPSRedirect && !secureRequest(r) && r.Method == http.MethodGet && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
		// Behind a proxy that terminates TLS, plain HTTP is sent to HTTPS (P3-11).
		u := *r.URL
		u.Scheme, u.Host = "https", r.Host
		http.Redirect(w, r, u.String(), http.StatusPermanentRedirect)
		return
	}
	// A port forward's CLI carries the forward's token, not a cookie
	// (TB-20); its handler checks it.
	if s.opts.Auth != nil && !IsTunnelPath(r.URL.Path) {
		if r = s.opts.Auth.Gate(w, r); r == nil {
			s.observeOnly("gate", http.StatusUnauthorized)
			return
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && strings.HasPrefix(r.URL.Path, "/api/") && !auth.SameSite(r) {
		// Every request that changes anything must come from the console's
		// own pages (X-06).
		jsonError(w, http.StatusForbidden, "this can only be done from the console itself")
		return
	}
	if s.forwardToLeader(w, r, forwarded) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// securityHeaders go on every answer.
var securityHeaders = [][2]string{
	{"Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"},
	{"X-Content-Type-Options", "nosniff"},
	{"X-Frame-Options", "DENY"},
	{"Referrer-Policy", "no-referrer"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
	{"Cross-Origin-Resource-Policy", "same-origin"},
	{"Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()"},
}

// secureRequest says whether a request came over TLS, directly or through
// a proxy that says so.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// observeOnly counts a request the gate answered.
func (s *Server) observeOnly(route string, code int) {
	s.reqMu.Lock()
	s.reqs[reqKey{route, code}]++
	s.reqMu.Unlock()
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
	st, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	names, err := viewsOf(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := s.access(r, cl)
	if _, err := s.prepare(r.Context(), a, cl, names); err != nil {
		// Nothing is shown rather than everything; the stream says why.
		s.opts.Logger.Warn("asking what a person may see failed", "cluster", clusterID(r), "user", a.Person().User, "err", err)
	}
	writeJSON(w, r, st.Snapshot(names, filterOf(a)))
}

// filterOf is the access as the store takes it: a nil *Access must be a
// nil Filter, not an interface holding nil.
func filterOf(a *access.Access) live.Filter {
	if a == nil {
		return nil
	}
	return a
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	st, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if a := s.access(r, cl); a != nil {
		if _, err := s.prepare(r.Context(), a, cl, []string{live.ViewNodes}); err != nil {
			jsonError(w, http.StatusBadGateway, accessState(err)["error"])
			return
		}
		if !a.Cluster("nodes") {
			jsonError(w, http.StatusForbidden, "Kubernetes does not let you list nodes")
			return
		}
	}
	row, ok := st.Row(live.ViewNodes, r.PathValue("name"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no node named "+strconv.Quote(r.PathValue("name")))
		return
	}
	writeJSON(w, r, live.Redact(live.ViewNodes, row, filterOf(s.access(r, cl))))
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

// putSettings changes the shared settings, for admins (P3-03). It takes
// JSON only; ServeHTTP already refused requests another site made the
// browser send (X-06).
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.SessionFrom(r.Context())
	if s.opts.Auth != nil && (!ok || !sess.Admin) {
		jsonError(w, http.StatusForbidden, "only an administrator can change the settings")
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
		jsonError(w, settingsStatus(err, http.StatusInternalServerError), "The settings were not saved: "+err.Error())
		return
	}
	s.opts.Logger.Info("settings changed from a browser", "refresh", st.Refresh(), "from", r.RemoteAddr, "user", sess.User.Username)
	detail := fmt.Sprintf("refresh %s", st.Refresh())
	if f := st.TimeFormat; f != nil {
		detail += fmt.Sprintf(", time format %s %s", f.Date, f.Clock)
	}
	if b := st.AlertBanners; b != nil && !*b {
		detail += ", alert banners hidden"
	}
	s.record(r, audit.ActionSettings, "settings", detail)
	b, _ := json.Marshal(st)
	writeJSON(w, r, b)
}

// settingsStatus is the status for a settings save that failed: 409 when
// a newer 221B saved them, which this one does not overwrite (P7-03).
func settingsStatus(err error, otherwise int) int {
	if errors.Is(err, settings.ErrNewer) {
		return http.StatusConflict
	}
	return otherwise
}

// record writes an audit event for the request's session.
func (s *Server) record(r *http.Request, action, target, detail string) {
	if s.opts.Audit == nil {
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	s.opts.Audit.Record(audit.Event{Action: action, Who: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}, Target: target, Detail: detail, From: fromAddr(r)})
}

// fromAddr is the address a request came from, without the port.
func fromAddr(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// A cold standby serves nothing of its own: it is ready once it knows
	// where to forward (ADR-70).
	if s.opts.Standby != nil && s.opts.Standby() {
		if s.opts.Leader == nil || s.opts.Leader.LeaderAddr() == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "waiting for a leader to forward to\n")
			return
		}
		io.WriteString(w, "ok\n")
		return
	}
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
	st, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	names, err := viewsOf(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// What the person may see is asked before the stream opens, and again
	// as the answers age, so a change in their RBAC reaches them. When
	// Kubernetes cannot be asked (the API server is away, or the console's
	// account may not ask), nothing is shown and the stream says why.
	a := s.access(r, cl)
	_, prepareErr := s.prepare(r.Context(), a, cl, names)
	if prepareErr != nil {
		s.opts.Logger.Warn("asking what a person may see failed", "cluster", clusterID(r), "user", a.Person().User, "err", prepareErr)
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

	sub, snap, err := st.Subscribe(names, filterOf(a))
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer st.Unsubscribe(sub)
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(out, "retry: 3000\n\n"); err != nil {
		return
	}
	if err := write("snapshot", snap); err != nil {
		return
	}
	accessEvent := func(err error) error {
		b, _ := json.Marshal(accessState(err))
		return write("access", b)
	}
	if prepareErr != nil {
		if err := accessEvent(prepareErr); err != nil {
			return
		}
	}
	beat := time.NewTicker(s.opts.Heartbeat)
	defer beat.Stop()
	// The person's access is asked again as it ages, or when namespaces
	// appear; a change sends a fresh snapshot. While Kubernetes cannot be
	// asked, it is tried again sooner.
	var recheck <-chan time.Time
	var recheckTicker *time.Ticker
	if a != nil {
		every := s.opts.AccessRecheck
		if prepareErr != nil {
			every = min(every, 5*time.Second)
		}
		recheckTicker = time.NewTicker(every)
		defer recheckTicker.Stop()
		recheck = recheckTicker.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if !s.sessionStands(r) {
				return
			}
			// A connection replaced since (new credentials) has a new store:
			// ending the stream makes the browser reopen it there.
			if s.opts.Clusters != nil {
				if now, _, ok := s.opts.Clusters.Lookup(clusterID(r)); !ok || now != st {
					return
				}
			}
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := io.WriteString(out, ": ping\n\n"); err != nil {
				return
			}
			if err := flush(); err != nil {
				return
			}
		case <-recheck:
			changed, err := s.prepare(r.Context(), a, cl, names)
			if (err == nil) != (prepareErr == nil) {
				// Kubernetes can be asked again, or no longer: the browser hears.
				if werr := accessEvent(err); werr != nil {
					return
				}
				if err == nil {
					recheckTicker.Reset(s.opts.AccessRecheck)
				} else {
					recheckTicker.Reset(min(s.opts.AccessRecheck, 5*time.Second))
				}
			}
			if err != nil && prepareErr == nil {
				s.opts.Logger.Warn("asking what a person may see failed", "cluster", clusterID(r), "user", a.Person().User, "err", err)
			}
			prepareErr = err
			if changed {
				sub.Resync()
			}
		case <-sub.Wake():
			events, resync := sub.Take()
			if resync {
				if err := write("snapshot", st.Snapshot(names, filterOf(a))); err != nil {
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
	leads := 1.0
	if s.opts.Leader != nil && !s.opts.Leader.IsLeader() {
		leads = 0
	}
	gauges := []Gauge{
		// The leader reads the clusters (ADR-70): its figures are the
		// console's; a follower's view gauges are its own, mostly empty.
		{Name: "b221_leader", Help: "1 on the replica that reads the clusters and serves the data; 0 on one that forwards to it.", Value: leads},
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
	writeJSONStatus(w, r, http.StatusOK, body)
}

// writeJSONStatus is writeJSON with another status. The status is sent
// after the headers are set: once it is, later headers are dropped, and a
// gzipped body without its Content-Encoding cannot be read.
func writeJSONStatus(w http.ResponseWriter, r *http.Request, code int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Accept-Encoding")
	if len(body) > 1024 && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		w.WriteHeader(code)
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		gz.Write(body)
		gz.Close()
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(code)
	w.Write(body)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(append(b, '\n'))
}
