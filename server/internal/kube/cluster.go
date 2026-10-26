package kube

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	apiversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/version"
)

// Connection states reported in Info.State.
const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
)

// Metrics states reported in Info.Metrics.
const (
	MetricsUnknown     = "unknown"
	MetricsAvailable   = "available"
	MetricsUnavailable = "unavailable"
)

// Info describes the cluster and the connection to it.
type Info struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform,omitempty"`
	Mode     Mode   `json:"mode"`
	// State is connecting, connected or disconnected; Since is when it last
	// changed and Message explains a disconnection.
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Message string    `json:"message,omitempty"`
	// Synced is true once every kind has been listed at least once.
	Synced bool `json:"synced"`
	// Warnings are problems reading the cluster that do not stop the
	// console, such as a missing permission.
	Warnings []string `json:"warnings"`
	// Metrics says whether CPU and memory in use are available.
	Metrics        string     `json:"metrics"`
	MetricsMessage string     `json:"metricsMessage,omitempty"`
	MetricsAt      *time.Time `json:"metricsAt,omitempty"`
	// Uptime says whether node boot times can be read from the kubelets.
	Uptime        string `json:"uptime"`
	UptimeMessage string `json:"uptimeMessage,omitempty"`
	// Secrets says whether Secrets are read (for their keys and sizes, and
	// for Helm releases): "read", or "off" when the chart turned it off.
	Secrets string `json:"secrets"`
}

// Change tells the store which rows may have changed: for each view, the
// keys of its rows, or "*" for every row. Info marks the cluster details,
// Totals the counts that belong to no row.
type Change struct {
	Views  map[string][]string
	Info   bool
	Totals bool
}

// AllRows marks every row of a view.
const AllRows = "*"

// UsageOnly marks a view's CPU and memory in use, which the store sends
// apart from the rows, without marking any row.
const UsageOnly = "@usage"

// Mark adds keys of a view to the change.
func (ch *Change) Mark(view string, keys ...string) {
	if ch.Views == nil {
		ch.Views = map[string][]string{}
	}
	ch.Views[view] = append(ch.Views[view], keys...)
}

// Empty reports whether the change carries nothing.
func (ch Change) Empty() bool {
	return !ch.Info && !ch.Totals && len(ch.Views) == 0
}

// Options configure a Cluster.
type Options struct {
	Name           string
	Mode           Mode
	HealthInterval time.Duration
	// KubeletStats reads each node's boot time from its kubelet through the
	// API server, which needs get on nodes/proxy. Off, uptime is not shown.
	KubeletStats bool
	// SecretsRead watches Secrets, for their keys and sizes and for Helm
	// releases (P2-07). Off, Secrets are never read and the Helm tab is
	// hidden (P2-10).
	SecretsRead bool
	// Protobuf asks the API server for protobuf, which is smaller and
	// faster to decode than JSON for large pod lists.
	Protobuf bool
	Logger   *slog.Logger
}

// Cluster keeps watched copies of the objects of one cluster and tracks the
// connection to it.
type Cluster struct {
	log            *slog.Logger
	cfg            *rest.Config   // as given, for the connection test
	clientCfg      *rest.Config   // as the clients use it, for per-person clients (P3-02)
	rest           rest.Interface // the core API, for /version, metrics and the node proxy
	cs             *kubernetes.Clientset
	people         personClients
	mc             metadata.Interface
	kinds          []Kind
	informers      map[string]cache.SharedIndexInformer
	healthInterval time.Duration
	kubeletStats   bool
	secretsRead    bool
	wantPodUsage   atomic.Bool

	mu                 sync.RWMutex
	info               Info
	usage              map[string]nodes.Usage
	podUsage           map[string]nodes.Usage
	boots              map[string]bootEntry
	bootForbiddenUntil time.Time
	// volumes is what the kubelets report about mounted claims (ST-08),
	// by "namespace/claim"; volumesAt when they were last read.
	volumes     map[string]VolumeStats
	volumesAt   time.Time
	watchErrs   map[string]watchErr
	notify      func(Change)
	events      map[string]uint64
	healthFails int
}

type watchErr struct {
	msg string
	at  time.Time
}

// New prepares a Cluster. Nothing talks to the API server until Run.
func New(cfg *rest.Config, o Options) (*Cluster, error) {
	c := rest.CopyConfig(cfg)
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	if c.QPS == 0 {
		c.QPS, c.Burst = 50, 100
	}
	if o.Protobuf {
		c.ContentType = "application/vnd.kubernetes.protobuf"
		c.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	} else {
		// Said outright: the typed clients prefer protobuf when the content
		// type was left to them, and the simulator speaks JSON only.
		c.ContentType = "application/json"
		c.AcceptContentTypes = "application/json"
	}
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the Kubernetes client: %w", err)
	}
	mcfg := rest.CopyConfig(c)
	if !o.Protobuf {
		// The metadata client asks for protobuf by default; the simulator
		// speaks JSON only.
		mcfg.ContentType = "application/json"
		mcfg.AcceptContentTypes = "application/json"
	}
	mc, err := metadata.NewForConfig(mcfg)
	if err != nil {
		return nil, fmt.Errorf("creating the metadata client: %w", err)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = 10 * time.Second
	}
	cl := &Cluster{
		log:            o.Logger,
		cfg:            rest.CopyConfig(cfg),
		clientCfg:      c,
		rest:           cs.CoreV1().RESTClient(),
		cs:             cs,
		mc:             mc,
		informers:      map[string]cache.SharedIndexInformer{},
		healthInterval: o.HealthInterval,
		kubeletStats:   o.KubeletStats,
		secretsRead:    o.SecretsRead,
		info: Info{
			Name: o.Name, Mode: o.Mode, State: StateConnecting, Since: time.Now().UTC(),
			Metrics: MetricsUnknown, Uptime: UptimeUnknown, Warnings: []string{}, Secrets: "read",
		},
		usage:     map[string]nodes.Usage{},
		podUsage:  map[string]nodes.Usage{},
		boots:     map[string]bootEntry{},
		volumes:   map[string]VolumeStats{},
		watchErrs: map[string]watchErr{},
		events:    map[string]uint64{},
		notify:    func(Change) {},
	}
	if !o.KubeletStats {
		cl.info.Uptime = UptimeUnavailable
		cl.info.UptimeMessage = "Uptime is not shown: reading the kubelets' stats is off (kubeletStats in the chart)."
	}
	if !o.SecretsRead {
		cl.info.Secrets = "off"
	}
	for _, k := range Kinds {
		if k.Secret && !o.SecretsRead {
			continue
		}
		inf, err := cl.newInformer(k)
		if err != nil {
			return nil, err
		}
		cl.informers[k.Name] = inf
		cl.kinds = append(cl.kinds, k)
	}
	return cl, nil
}

// newInformer builds the informer of one kind: a typed list-and-watch for
// kinds with bodies the screens read, a metadata-only one for the rest.
func (c *Cluster) newInformer(k Kind) (cache.SharedIndexInformer, error) {
	var lw *cache.ListWatch
	if k.Metadata {
		res := c.mc.Resource(k.GVR())
		lw = &cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return res.List(ctx, o)
			},
			WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return res.Watch(ctx, o)
			},
		}
	} else {
		client, err := c.clientFor(k)
		if err != nil {
			return nil, err
		}
		lw = cache.NewListWatchFromClient(client, k.Resource, metav1.NamespaceAll, fields.Everything())
	}
	indexers := cache.Indexers{}
	if k.Namespaced {
		indexers[cache.NamespaceIndex] = cache.MetaNamespaceIndexFunc
	}
	for name, f := range k.Indexers {
		indexers[name] = f
	}
	inf := cache.NewSharedIndexInformerWithOptions(c.patient(lw), k.New(), cache.SharedIndexInformerOptions{ObjectDescription: k.Name, Indexers: indexers})
	if k.Transform != nil {
		if err := inf.SetTransform(k.Transform); err != nil {
			return nil, err
		}
	}
	if err := inf.SetWatchErrorHandlerWithContext(c.watchError(k.Name)); err != nil {
		return nil, err
	}
	kind := k
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.changed(kind, nil, obj) },
		UpdateFunc: func(old, obj any) { c.changed(kind, old, obj) },
		DeleteFunc: func(obj any) { c.changed(kind, obj, nil) },
	}); err != nil {
		return nil, err
	}
	return inf, nil
}

// clientFor returns the REST client of a kind's API group.
func (c *Cluster) clientFor(k Kind) (rest.Interface, error) {
	switch k.Group {
	case "":
		return c.cs.CoreV1().RESTClient(), nil
	case "apps":
		return c.cs.AppsV1().RESTClient(), nil
	case "batch":
		return c.cs.BatchV1().RESTClient(), nil
	case "discovery.k8s.io":
		return c.cs.DiscoveryV1().RESTClient(), nil
	case "networking.k8s.io":
		return c.cs.NetworkingV1().RESTClient(), nil
	case "rbac.authorization.k8s.io":
		return c.cs.RbacV1().RESTClient(), nil
	case "policy":
		return c.cs.PolicyV1().RESTClient(), nil
	case "scheduling.k8s.io":
		return c.cs.SchedulingV1().RESTClient(), nil
	case "admissionregistration.k8s.io":
		return c.cs.AdmissionregistrationV1().RESTClient(), nil
	case "storage.k8s.io":
		return c.cs.StorageV1().RESTClient(), nil
	}
	return nil, fmt.Errorf("no client for API group %q", k.Group)
}

// patient makes every list and watch wait until the API server answers.
// During an outage client-go's informers otherwise fail fast and back off,
// up to 30 to 60 seconds between attempts, so they could resume up to a
// minute after the API server returns. Waiting here instead keeps their
// backoff short: they resume within a second or two of its return.
func (c *Cluster) patient(lw *cache.ListWatch) *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			c.whenReachable(ctx)
			return lw.ListWithContext(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			c.whenReachable(ctx)
			return lw.WatchWithContext(ctx, o)
		},
	}
}

// whenReachable returns once the API server answers /version (any answer,
// even an error status, means it is up), checking every second.
func (c *Cluster) whenReachable(ctx context.Context) {
	for {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := c.rest.Get().AbsPath("/version").MaxRetries(0).Do(probe).Error()
		cancel()
		var status apierrors.APIStatus
		if err == nil || errors.As(err, &status) || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// OnChange registers the function told about changes. Call it before Run.
func (c *Cluster) OnChange(f func(Change)) {
	c.mu.Lock()
	c.notify = f
	c.mu.Unlock()
}

// coreKinds must be listed before the console is ready; the other kinds
// fill in as they arrive, and a kind the ServiceAccount may not read shows
// as a warning rather than stopping everything.
var coreKinds = []string{"nodes", "pods", "namespaces"}

// RestConfig returns a copy of the configuration the cluster was built with.
func (c *Cluster) RestConfig() *rest.Config { return rest.CopyConfig(c.cfg) }

// Run watches the cluster until ctx is done.
func (c *Cluster) Run(ctx context.Context) {
	var core, all []cache.InformerSynced
	for name, inf := range c.informers {
		go inf.RunWithContext(ctx)
		all = append(all, inf.HasSynced)
		for _, k := range coreKinds {
			if k == name {
				core = append(core, inf.HasSynced)
			}
		}
	}
	go func() {
		if !cache.WaitForCacheSync(ctx.Done(), core...) {
			return
		}
		c.mu.Lock()
		c.info.Synced = true
		c.mu.Unlock()
		c.log.Info("nodes, pods and namespaces listed", "nodes", c.Count("nodes", ""), "pods", c.Count("pods", ""), "namespaces", c.Count("namespaces", ""))
		c.emit(c.everything())
		if cache.WaitForCacheSync(ctx.Done(), all...) {
			c.log.Info("every kind listed", "kinds", len(c.informers), "objects", c.totalObjects())
			c.emit(c.everything())
		}
	}()
	go c.loop(ctx, c.healthInterval, c.checkHealth)
	if c.kubeletStats {
		go c.bootLoop(ctx)
	}
	<-ctx.Done()
}

// everything marks every static view, the details and the totals.
func (c *Cluster) everything() Change {
	ch := Change{Info: true, Totals: true}
	for _, v := range []string{"nodes", "pods", "namespaces", "services", "helm", "alerts", "fleets", "access", "policies", "networks", "storage"} {
		ch.Mark(v, AllRows)
	}
	return ch
}

func (c *Cluster) totalObjects() int {
	n := 0
	for name := range c.informers {
		n += c.Count(name, "")
	}
	return n
}

// Poll reads what is not watched: CPU and memory in use from
// metrics-server, for nodes always and for pods while a browser shows
// them. The store calls it once per refresh, so one read serves every
// browser. ctx bounds how long it may take.
func (c *Cluster) Poll(ctx context.Context) {
	c.readMetrics(ctx)
	if c.kubeletStats {
		c.pollVolumes(ctx)
	}
	if c.wantPodUsage.Load() {
		c.readPodMetrics(ctx)
	} else {
		c.mu.Lock()
		had := len(c.podUsage) > 0
		c.podUsage = map[string]nodes.Usage{}
		c.mu.Unlock()
		if had {
			c.emit(Change{Views: map[string][]string{"pods": {UsageOnly}}})
		}
	}
}

// WantPodUsage says whether pod usage should be read on the next polls.
func (c *Cluster) WantPodUsage(want bool) { c.wantPodUsage.Store(want) }

func (c *Cluster) loop(ctx context.Context, every time.Duration, f func(context.Context)) {
	f(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f(ctx)
		}
	}
}

// Info returns the cluster and connection details.
func (c *Cluster) Info() Info {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info := c.info
	info.Warnings = []string{}
	now := time.Now()
	keys := make([]string, 0, len(c.watchErrs))
	for k := range c.watchErrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if e := c.watchErrs[k]; now.Sub(e.at) < 2*time.Minute {
			info.Warnings = append(info.Warnings, e.msg)
		}
	}
	return info
}

// Ready reports whether every kind has been listed.
func (c *Cluster) Ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info.Synced
}

// SecretsRead reports whether Secrets are watched.
func (c *Cluster) SecretsRead() bool { return c.secretsRead }

// Kinds returns the kinds this cluster watches.
func (c *Cluster) Kinds() []Kind { return c.kinds }

// Objects returns the cached objects of a kind, in one namespace or, with
// an empty namespace, all of them.
func (c *Cluster) Objects(kind, namespace string) []runtime.Object {
	inf, ok := c.informers[kind]
	if !ok {
		return nil
	}
	var items []any
	if namespace == "" {
		items = inf.GetStore().List()
	} else {
		var err error
		if items, err = inf.GetIndexer().ByIndex(cache.NamespaceIndex, namespace); err != nil {
			return nil
		}
	}
	out := make([]runtime.Object, 0, len(items))
	for _, it := range items {
		if o, ok := it.(runtime.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// Object returns one cached object, or nil. namespace is empty for
// cluster-scoped kinds.
func (c *Cluster) Object(kind, namespace, name string) runtime.Object {
	inf, ok := c.informers[kind]
	if !ok {
		return nil
	}
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	obj, found, err := inf.GetStore().GetByKey(key)
	if err != nil || !found {
		return nil
	}
	o, _ := obj.(runtime.Object)
	return o
}

// ByIndex returns the cached objects of a kind an index maps a value to.
func (c *Cluster) ByIndex(kind, index, value string) []runtime.Object {
	inf, ok := c.informers[kind]
	if !ok {
		return nil
	}
	items, err := inf.GetIndexer().ByIndex(index, value)
	if err != nil {
		return nil
	}
	out := make([]runtime.Object, 0, len(items))
	for _, it := range items {
		if o, ok := it.(runtime.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// Count counts the cached objects of a kind, in one namespace or all.
func (c *Cluster) Count(kind, namespace string) int {
	inf, ok := c.informers[kind]
	if !ok {
		return 0
	}
	if namespace == "" {
		return len(inf.GetStore().ListKeys())
	}
	keys, err := inf.GetIndexer().IndexKeys(cache.NamespaceIndex, namespace)
	if err != nil {
		return 0
	}
	return len(keys)
}

// Keys returns the cache keys ("namespace/name" or "name") of a kind.
func (c *Cluster) Keys(kind string) []string {
	inf, ok := c.informers[kind]
	if !ok {
		return nil
	}
	return inf.GetStore().ListKeys()
}

// NodeNames returns the names of all cached nodes.
func (c *Cluster) NodeNames() []string { return c.Keys("nodes") }

// Node returns a cached node, or nil.
func (c *Cluster) Node(name string) *corev1.Node {
	n, _ := c.Object("nodes", "", name).(*corev1.Node)
	return n
}

// PodsOn returns the cached pods bound to a node.
func (c *Cluster) PodsOn(node string) []*corev1.Pod {
	objs := c.ByIndex("pods", IndexPodNode, node)
	out := make([]*corev1.Pod, 0, len(objs))
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			out = append(out, p)
		}
	}
	return out
}

// Pod returns a cached pod, or nil.
func (c *Cluster) Pod(namespace, name string) *corev1.Pod {
	p, _ := c.Object("pods", namespace, name).(*corev1.Pod)
	return p
}

// Unscheduled counts pods waiting for a node.
func (c *Cluster) Unscheduled() int {
	n := 0
	for _, p := range c.PodsOn("") {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			n++
		}
	}
	return n
}

// Usage returns what metrics-server last reported for a node.
func (c *Cluster) Usage(node string) (nodes.Usage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	u, ok := c.usage[node]
	return u, ok
}

// PodUsage returns what metrics-server last reported for every pod, by
// "namespace/name".
func (c *Cluster) PodUsage() map[string]nodes.Usage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]nodes.Usage, len(c.podUsage))
	for k, v := range c.podUsage {
		out[k] = v
	}
	return out
}

// Counts reports cache sizes and event totals for the metrics endpoint.
func (c *Cluster) Counts() (nodeCount, podCount int, nodeEvents, podEvents uint64) {
	c.mu.RLock()
	nodeEvents, podEvents = c.events["nodes"], c.events["pods"]
	c.mu.RUnlock()
	return c.Count("nodes", ""), c.Count("pods", ""), nodeEvents, podEvents
}

// CachedObjects reports the number of cached objects of every kind.
func (c *Cluster) CachedObjects() map[string]int {
	out := make(map[string]int, len(c.informers))
	for name := range c.informers {
		out[name] = c.Count(name, "")
	}
	return out
}

func (c *Cluster) emit(ch Change) {
	if ch.Empty() {
		return
	}
	c.mu.RLock()
	f := c.notify
	c.mu.RUnlock()
	f(ch)
}

// changed turns an object event into the rows it touches.
func (c *Cluster) changed(k Kind, old, obj any) {
	c.mu.Lock()
	c.events[k.Name]++
	delete(c.watchErrs, k.Name)
	c.mu.Unlock()
	ch := Change{}
	oldMeta, newMeta := asMeta(old), asMeta(obj)
	ns := ""
	if newMeta != nil {
		ns = newMeta.GetNamespace()
	} else if oldMeta != nil {
		ns = oldMeta.GetNamespace()
	}
	added, removed := oldMeta == nil, newMeta == nil
	switch k.Name {
	case "nodes":
		for _, m := range []metav1.Object{oldMeta, newMeta} {
			if m != nil {
				ch.Mark("nodes", m.GetName())
				ch.Mark("alerts", "node:"+m.GetName())
				ch.Mark("policies", "drain:"+m.GetName())
			}
		}
		ch.Mark("fleets", AllRows)
	case "persistentvolumeclaims":
		for _, m := range []metav1.Object{oldMeta, newMeta} {
			if m != nil {
				ch.Mark("alerts", "pvc:"+m.GetNamespace()+"/"+m.GetName())
			}
		}
	case "events":
		for _, o := range []any{old, obj} {
			e, ok := unwrap(o).(*corev1.Event)
			if !ok {
				continue
			}
			io := e.InvolvedObject
			switch io.Kind {
			case "Pod":
				ch.Mark("alerts", "pod:"+io.Namespace+"/"+io.Name)
				// What the kubelet reports about a pod's volumes shows under
				// the claims as well.
				if strings.Contains(e.Reason, "Attach") || strings.Contains(e.Reason, "Mount") {
					if p, _ := c.Object("pods", io.Namespace, io.Name).(*corev1.Pod); p != nil {
						for _, claim := range podClaims(p) {
							ch.Mark("alerts", "pvc:"+io.Namespace+"/"+claim)
						}
					}
				}
			case "Node":
				ch.Mark("alerts", "node:"+io.Name)
			case "PersistentVolumeClaim":
				ch.Mark("alerts", "pvc:"+io.Namespace+"/"+io.Name)
			}
		}
	case "pods":
		for _, o := range []any{old, obj} {
			if p := asPod(o); p != nil {
				if p.Spec.NodeName != "" {
					ch.Mark("nodes", p.Spec.NodeName)
					ch.Mark("fleets", AllRows)
				} else {
					ch.Totals = true
				}
				ch.Mark("pods", p.Namespace+"/"+p.Name)
				ch.Mark("alerts", "pod:"+p.Namespace+"/"+p.Name)
				sa := p.Spec.ServiceAccountName
				if sa == "" {
					sa = "default"
				}
				ch.Mark("access", "sa:"+p.Namespace+"/"+sa)
				c.markPodPolicies(&ch, p)
				for _, claim := range podClaims(p) {
					ch.Mark("alerts", "pvc:"+p.Namespace+"/"+claim)
				}
				if release := podRelease(p); release != "" {
					ch.Mark("helm", p.Namespace+"/"+release)
				} else {
					// A pod without Helm's labels may still belong to a
					// release through its workload: the releases of its
					// namespace are rebuilt, and there are few of those.
					for _, r := range c.releasesIn(p.Namespace) {
						ch.Mark("helm", p.Namespace+"/"+r)
					}
				}
			}
		}
	case "namespaces":
		for _, m := range []metav1.Object{oldMeta, newMeta} {
			if m != nil {
				ch.Mark("namespaces", m.GetName())
				ch.Mark("policies", "ns:"+m.GetName())
			}
		}
	case "services":
		for _, m := range []metav1.Object{oldMeta, newMeta} {
			if m != nil {
				ch.Mark("services", m.GetNamespace()+"/"+m.GetName())
				c.markHooks(&ch, m.GetNamespace(), m.GetName())
			}
		}
	case "endpointslices":
		for _, o := range []any{old, obj} {
			if m := asMeta(o); m != nil {
				if svc := m.GetLabels()["kubernetes.io/service-name"]; svc != "" {
					ch.Mark("services", m.GetNamespace()+"/"+svc)
					c.markHooks(&ch, m.GetNamespace(), svc)
				}
			}
		}
	case "ingresses":
		ch.Mark("services", AllRows)
	case "networkpolicies", "limitranges":
		prefix := map[string]string{"networkpolicies": "np", "limitranges": "limits"}[k.Name]
		for _, m := range []metav1.Object{oldMeta, newMeta} {
			if m != nil {
				ch.Mark("policies", prefix+":"+m.GetNamespace()+"/"+m.GetName())
				ch.Mark("policies", "ns:"+m.GetNamespace())
			}
		}
	case "poddisruptionbudgets":
		// A budget decides the drains of the nodes its pods run on.
		ch.Mark("policies", AllRows)
	case "resourcequotas", "priorityclasses", "validatingwebhookconfigurations", "mutatingwebhookconfigurations":
		ch.Mark("policies", AllRows)
	case "deployments":
		for _, m := range []metav1.Object{oldMeta, newMeta} {
			if m != nil {
				ch.Mark("policies", "rollout:"+m.GetNamespace()+"/"+m.GetName())
			}
		}
	case "daemonsets":
		// A DaemonSet that goes leaves its pods orphaned, which stops a drain.
		ch.Mark("policies", AllRows)
	case "roles", "rolebindings", "clusterroles", "clusterrolebindings", "serviceaccounts":
		// A role's change can change the findings of every binding that
		// grants it, and a ServiceAccount's of every binding that names
		// it; RBAC changes rarely, so the view is rebuilt whole.
		ch.Mark("access", AllRows)
	case "secrets":
		for _, o := range []any{old, obj} {
			if s, ok := unwrap(o).(*SecretSummary); ok && s.Release != nil {
				ch.Mark("helm", s.Namespace+"/"+s.Release.Name)
			}
			if s, ok := unwrap(o).(*SecretSummary); ok && s.Type == corev1.SecretTypeServiceAccountToken {
				ch.Mark("access", "sa:"+s.Namespace+"/"+s.Annotations[corev1.ServiceAccountNameKey])
			}
		}
	}
	if k.Namespaced && ns != "" {
		ch.Mark("ns:"+ns, AllRows)
		// Counts change on add and delete; pods also carry a status.
		if added || removed || k.Name == "pods" {
			ch.Mark("namespaces", ns)
		}
		if k.Name != "events" && (added || removed) {
			ch.Mark("helm", AllRows)
		}
	}
	switch k.Name {
	case "persistentvolumeclaims", "persistentvolumes", "storageclasses", "volumeattachments", "resourcequotas", "statefulsets":
		// The storage view is built whole (ST-25).
		ch.Mark("storage", AllRows)
	case "pods":
		for _, o := range []any{old, obj} {
			if p := asPod(o); p != nil && len(podClaims(p)) > 0 {
				ch.Mark("storage", AllRows)
				break
			}
		}
	case "events":
		for _, o := range []any{old, obj} {
			if e, ok := unwrap(o).(*corev1.Event); ok && (e.InvolvedObject.Kind == "PersistentVolumeClaim" || e.InvolvedObject.Kind == "PersistentVolume") {
				ch.Mark("storage", AllRows)
				break
			}
		}
	}
	switch k.Name {
	case "ingresses", "ingressclasses", "services", "endpointslices", "networkpolicies", "pods", "nodes", "daemonsets":
		// The traces read all of these; there are few of them.
		ch.Mark("networks", AllRows)
	case "secrets":
		for _, o := range []any{old, obj} {
			if s, ok := unwrap(o).(*SecretSummary); ok && s.Type == corev1.SecretTypeTLS {
				ch.Mark("networks", AllRows)
			}
		}
	}
	c.emit(ch)
}

// markPodPolicies marks the policy rows a pod's change touches: its
// namespace's, its node's drain, the policies and budgets that may select
// it, the LimitRanges that may have filled it in, its PriorityClass.
func (c *Cluster) markPodPolicies(ch *Change, p *corev1.Pod) {
	ch.Mark("policies", "ns:"+p.Namespace)
	ch.Mark("policies", "pcnone:cluster")
	if p.Spec.NodeName != "" {
		ch.Mark("policies", "drain:"+p.Spec.NodeName)
	}
	if p.Spec.PriorityClassName != "" {
		ch.Mark("policies", "pc:"+p.Spec.PriorityClassName)
	}
	for kind, prefix := range map[string]string{"networkpolicies": "np", "poddisruptionbudgets": "pdb", "limitranges": "limits"} {
		for _, o := range c.Objects(kind, p.Namespace) {
			if m := asMeta(o); m != nil {
				ch.Mark("policies", prefix+":"+m.GetNamespace()+"/"+m.GetName())
			}
		}
	}
	if p.Namespace == "kube-system" {
		ch.Mark("policies", "cni:cluster")
	}
}

// markHooks marks the webhooks whose backend is a Service.
func (c *Cluster) markHooks(ch *Change, ns, name string) {
	for _, o := range c.Objects("validatingwebhookconfigurations", "") {
		if w, ok := o.(*admissionv1.ValidatingWebhookConfiguration); ok {
			for _, h := range w.Webhooks {
				if s := h.ClientConfig.Service; s != nil && s.Namespace == ns && s.Name == name {
					ch.Mark("policies", "hook:Validating/"+w.Name+"/"+h.Name)
				}
			}
		}
	}
	for _, o := range c.Objects("mutatingwebhookconfigurations", "") {
		if w, ok := o.(*admissionv1.MutatingWebhookConfiguration); ok {
			for _, h := range w.Webhooks {
				if s := h.ClientConfig.Service; s != nil && s.Namespace == ns && s.Name == name {
					ch.Mark("policies", "hook:Mutating/"+w.Name+"/"+h.Name)
				}
			}
		}
	}
}

// releasesIn names the Helm releases recorded in a namespace.
func (c *Cluster) releasesIn(namespace string) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range c.Objects("secrets", namespace) {
		if s, ok := o.(*SecretSummary); ok && s.Release != nil && !seen[s.Release.Name] {
			seen[s.Release.Name] = true
			out = append(out, s.Release.Name)
		}
	}
	return out
}

// podRelease names the Helm release a pod belongs to, from the labels
// Helm charts set, or "".
func podRelease(p *corev1.Pod) string {
	if p.Labels["app.kubernetes.io/managed-by"] == "Helm" {
		if r := p.Labels["app.kubernetes.io/instance"]; r != "" {
			return r
		}
	}
	return p.Annotations["meta.helm.sh/release-name"]
}

func (c *Cluster) watchError(resource string) cache.WatchErrorHandlerWithContext {
	return func(ctx context.Context, r *cache.Reflector, err error) {
		// A closed or expired watch is routine; the reflector lists again.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
			c.log.Debug("watch restarted", "resource", resource, "err", err)
			return
		}
		msg := describeWatchError(resource, err)
		c.mu.Lock()
		c.watchErrs[resource] = watchErr{msg: msg, at: time.Now()}
		c.mu.Unlock()
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			c.log.Error("cannot watch the cluster", "resource", resource, "err", err)
		} else {
			c.log.Warn("watch interrupted; it will resume", "resource", resource, "err", err)
		}
		c.emit(Change{Info: true})
	}
}

func describeWatchError(resource string, err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "Not allowed to list or watch " + resource + ". Check the ClusterRole the chart installs."
	case apierrors.IsUnauthorized(err):
		return "The API server rejected the credentials while watching " + resource + "."
	case apierrors.IsNotFound(err):
		return "This cluster has no " + resource + " API, so they are not shown."
	default:
		return "Watching " + resource + " was interrupted; it resumes automatically. " + shortError(err)
	}
}

// checkHealth reads /version, which every authenticated client may read. It
// both checks the connection and keeps the Kubernetes version current.
func (c *Cluster) checkHealth(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("/version").SetHeader("Accept", "application/json").Do(ctx).Raw()
	var v apiversion.Info
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	c.mu.Lock()
	prev := c.info
	if err != nil {
		c.healthFails++
		if c.info.State != StateDisconnected {
			c.info.State, c.info.Since = StateDisconnected, time.Now().UTC()
		}
		c.info.Message = unreachable(err)
	} else {
		c.healthFails = 0
		if c.info.State != StateConnected {
			c.info.State, c.info.Since = StateConnected, time.Now().UTC()
		}
		c.info.Message = ""
		c.info.Version, c.info.Platform = v.GitVersion, v.Platform
	}
	changed := prev.State != c.info.State || prev.Message != c.info.Message || prev.Version != c.info.Version
	c.mu.Unlock()
	if changed {
		if err != nil {
			c.log.Warn("API server unreachable", "err", err)
		} else if prev.State != StateConnected {
			c.log.Info("API server reachable", "version", v.GitVersion)
		}
		c.emit(Change{Info: true})
	}
}

// metricsList is the part of metrics.k8s.io/v1beta1 NodeMetricsList we read.
type metricsList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Timestamp time.Time         `json:"timestamp"`
		Usage     map[string]string `json:"usage"`
		// Pod metrics carry one usage per container instead.
		Containers []struct {
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

// readMetrics reads node usage from metrics-server, when it is installed.
// It takes at most 10 seconds, or less when ctx ends sooner.
func (c *Cluster) readMetrics(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").SetHeader("Accept", "application/json").Do(ctx).Raw()
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	usage := map[string]nodes.Usage{}
	state, msg := MetricsAvailable, ""
	if err == nil {
		var list metricsList
		if err = json.Unmarshal(raw, &list); err == nil {
			for _, it := range list.Items {
				if u, ok := parseUsage(it.Usage); ok {
					usage[it.Metadata.Name] = u
				}
			}
		}
	}
	if err != nil {
		state = MetricsUnavailable
		switch {
		case apierrors.IsNotFound(err):
			msg = "metrics-server is not installed, so CPU and memory in use are not shown."
		case apierrors.IsServiceUnavailable(err):
			msg = "metrics-server is not responding, so CPU and memory in use are not shown."
		case apierrors.IsForbidden(err):
			msg = "Not allowed to read node metrics, so CPU and memory in use are not shown."
		case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err):
			msg = "metrics-server did not answer in time, so CPU and memory in use are not shown."
		default:
			msg = "CPU and memory in use are not available: " + shortError(err)
		}
		usage = map[string]nodes.Usage{}
	}
	c.mu.Lock()
	prevState, prevMsg := c.info.Metrics, c.info.MetricsMessage
	changed := prevState != state || prevMsg != msg || !sameUsage(c.usage, usage)
	c.usage = usage
	c.info.Metrics, c.info.MetricsMessage = state, msg
	if state == MetricsAvailable {
		now := time.Now().UTC()
		c.info.MetricsAt = &now
	}
	c.mu.Unlock()
	if prevState != state {
		if state == MetricsAvailable {
			c.log.Info("reading CPU and memory in use from metrics-server")
		} else {
			c.log.Info("CPU and memory in use unavailable", "reason", msg)
		}
	}
	if changed {
		c.emit(Change{Views: map[string][]string{"nodes": {AllRows}}, Info: true})
	}
}

// readPodMetrics reads every pod's usage from metrics-server. Pod usage is
// not part of a pod's row: the store sends it separately, so a usage
// change does not resend the row.
func (c *Cluster) readPodMetrics(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/pods").SetHeader("Accept", "application/json").Do(ctx).Raw()
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	usage := map[string]nodes.Usage{}
	if err == nil {
		var list metricsList
		if err = json.Unmarshal(raw, &list); err == nil {
			for _, it := range list.Items {
				var sum nodes.Usage
				for _, ct := range it.Containers {
					if u, ok := parseUsage(ct.Usage); ok {
						sum.CPUMilli += u.CPUMilli
						sum.MemoryBytes += u.MemoryBytes
					}
				}
				usage[it.Metadata.Namespace+"/"+it.Metadata.Name] = sum
			}
		}
	}
	if err != nil {
		c.log.Debug("pod metrics unavailable", "err", err)
	}
	c.mu.Lock()
	changed := !sameUsage(c.podUsage, usage)
	c.podUsage = usage
	c.mu.Unlock()
	if changed {
		c.emit(Change{Views: map[string][]string{"pods": {UsageOnly}}})
	}
}

func parseUsage(u map[string]string) (nodes.Usage, bool) {
	cpu, cerr := resource.ParseQuantity(u["cpu"])
	mem, merr := resource.ParseQuantity(u["memory"])
	if cerr != nil || merr != nil {
		return nodes.Usage{}, false
	}
	return nodes.Usage{CPUMilli: cpu.MilliValue(), MemoryBytes: mem.Value()}, true
}

func sameUsage(a, b map[string]nodes.Usage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func unwrap(obj any) any {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return d.Obj
	}
	return obj
}

func asMeta(obj any) metav1.Object {
	if obj == nil {
		return nil
	}
	m, _ := unwrap(obj).(metav1.Object)
	return m
}

func asPod(obj any) *corev1.Pod {
	if obj == nil {
		return nil
	}
	p, _ := unwrap(obj).(*corev1.Pod)
	return p
}

// unreachable says why the API server did not answer, in words an operator
// can act on. The raw error goes to the log.
func unreachable(err error) string {
	s := err.Error()
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	switch {
	case apierrors.IsUnauthorized(err):
		return "It refused the credentials (401 Unauthorized)."
	case apierrors.IsForbidden(err):
		return "It refused to give its version (403 Forbidden)."
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("The name %s does not resolve.", dnsErr.Name)
	case errors.As(err, &certErr) || strings.Contains(s, "x509: "):
		return "Its certificate is not trusted: " + shortError(err)
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(s, "connection refused"):
		return "The connection was refused."
	// client-go reports a retried request by its last error, so the cause
	// of a timeout after a dropped connection is in the text.
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		strings.HasSuffix(s, ": EOF") || strings.Contains(s, "connection reset"):
		return "The connection closed before it answered."
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "It did not answer within 5 seconds."
	}
	msg := shortError(err)
	return strings.ToUpper(msg[:1]) + msg[1:] + "."
}

// shortError trims long client errors to their last, most useful part.
func shortError(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && len(s) > 160 {
		s = s[i+2:]
	}
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// podClaims names the volume claims a pod mounts.
func podClaims(p *corev1.Pod) []string {
	var out []string
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			out = append(out, v.PersistentVolumeClaim.ClaimName)
		}
	}
	return out
}
