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
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	apiversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
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
	// Synced is true once nodes and pods have been listed at least once.
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
}

// Change tells the store what may have changed: the rows of some nodes, every
// row, the cluster details (connection, versions, warnings) or the totals
// that belong to no row (pods waiting for a node).
type Change struct {
	Nodes  []string
	All    bool
	Info   bool
	Totals bool
}

// Options configure a Cluster.
type Options struct {
	Name           string
	Mode           Mode
	HealthInterval time.Duration
	// KubeletStats reads each node's boot time from its kubelet through the
	// API server, which needs get on nodes/proxy. Off, uptime is not shown.
	KubeletStats bool
	// Protobuf asks the API server for protobuf, which is smaller and
	// faster to decode than JSON for large pod lists.
	Protobuf bool
	Logger   *slog.Logger
}

// Cluster keeps watched copies of the nodes and pods of one cluster and
// tracks the connection to it.
type Cluster struct {
	log             *slog.Logger
	rest            rest.Interface
	nodeInf, podInf cache.SharedIndexInformer
	healthInterval  time.Duration
	kubeletStats    bool

	mu                 sync.RWMutex
	info               Info
	usage              map[string]nodes.Usage
	boots              map[string]bootEntry
	bootForbiddenUntil time.Time
	watchErrs          map[string]watchErr
	notify             func(Change)
	nodeEvents         uint64
	podEvents          uint64
	healthFails        int
}

type watchErr struct {
	msg string
	at  time.Time
}

const podsByNode = "byNode"

// New prepares a Cluster. Nothing talks to the API server until Run.
func New(cfg *rest.Config, o Options) (*Cluster, error) {
	c := rest.CopyConfig(cfg)
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	if c.QPS == 0 {
		c.QPS, c.Burst = 20, 40
	}
	if o.Protobuf {
		c.ContentType = "application/vnd.kubernetes.protobuf"
		c.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	}
	core, err := corev1client.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the Kubernetes client: %w", err)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = 10 * time.Second
	}
	cl := &Cluster{
		log:            o.Logger,
		rest:           core.RESTClient(),
		healthInterval: o.HealthInterval,
		kubeletStats:   o.KubeletStats,
		info: Info{
			Name: o.Name, Mode: o.Mode, State: StateConnecting, Since: time.Now().UTC(),
			Metrics: MetricsUnknown, Uptime: UptimeUnknown, Warnings: []string{},
		},
		usage:     map[string]nodes.Usage{},
		boots:     map[string]bootEntry{},
		watchErrs: map[string]watchErr{},
		notify:    func(Change) {},
	}
	if !o.KubeletStats {
		cl.info.Uptime = UptimeUnavailable
		cl.info.UptimeMessage = "Uptime is not shown: reading the kubelets' stats is off (kubeletStats in the chart)."
	}

	cl.nodeInf = cache.NewSharedIndexInformerWithOptions(
		cl.patient(cache.NewListWatchFromClient(core.RESTClient(), "nodes", metav1.NamespaceAll, fields.Everything())),
		&corev1.Node{},
		cache.SharedIndexInformerOptions{ObjectDescription: "nodes"},
	)
	cl.podInf = cache.NewSharedIndexInformerWithOptions(
		cl.patient(cache.NewListWatchFromClient(core.RESTClient(), "pods", metav1.NamespaceAll, fields.Everything())),
		&corev1.Pod{},
		cache.SharedIndexInformerOptions{ObjectDescription: "pods", Indexers: cache.Indexers{podsByNode: podNodeName}},
	)
	if err := cl.nodeInf.SetTransform(StripNode); err != nil {
		return nil, err
	}
	if err := cl.podInf.SetTransform(StripPod); err != nil {
		return nil, err
	}
	if err := cl.nodeInf.SetWatchErrorHandlerWithContext(cl.watchError("nodes")); err != nil {
		return nil, err
	}
	if err := cl.podInf.SetWatchErrorHandlerWithContext(cl.watchError("pods")); err != nil {
		return nil, err
	}
	if _, err := cl.nodeInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { cl.nodeChanged(obj) },
		UpdateFunc: func(_, obj any) { cl.nodeChanged(obj) },
		DeleteFunc: func(obj any) { cl.nodeChanged(obj) },
	}); err != nil {
		return nil, err
	}
	if _, err := cl.podInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { cl.podChanged(nil, obj) },
		UpdateFunc: func(old, obj any) { cl.podChanged(old, obj) },
		DeleteFunc: func(obj any) { cl.podChanged(nil, obj) },
	}); err != nil {
		return nil, err
	}
	return cl, nil
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

// Run watches the cluster until ctx is done.
func (c *Cluster) Run(ctx context.Context) {
	go c.nodeInf.RunWithContext(ctx)
	go c.podInf.RunWithContext(ctx)
	go func() {
		if cache.WaitForCacheSync(ctx.Done(), c.nodeInf.HasSynced, c.podInf.HasSynced) {
			c.mu.Lock()
			c.info.Synced = true
			n, p := len(c.nodeInf.GetStore().ListKeys()), len(c.podInf.GetStore().ListKeys())
			c.mu.Unlock()
			c.log.Info("nodes and pods listed", "nodes", n, "pods", p)
			c.emit(Change{All: true, Info: true})
		}
	}()
	go c.loop(ctx, c.healthInterval, c.checkHealth)
	if c.kubeletStats {
		go c.bootLoop(ctx)
	}
	<-ctx.Done()
}

// Poll reads what is not watched: CPU and memory in use from
// metrics-server. The store calls it once per refresh, so one read serves
// every browser. ctx bounds how long it may take.
func (c *Cluster) Poll(ctx context.Context) {
	c.readMetrics(ctx)
}

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

// Ready reports whether nodes and pods have been listed.
func (c *Cluster) Ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info.Synced
}

// NodeNames returns the names of all cached nodes.
func (c *Cluster) NodeNames() []string {
	return c.nodeInf.GetStore().ListKeys()
}

// Node returns a cached node, or nil.
func (c *Cluster) Node(name string) *corev1.Node {
	obj, ok, err := c.nodeInf.GetStore().GetByKey(name)
	if err != nil || !ok {
		return nil
	}
	n, _ := obj.(*corev1.Node)
	return n
}

// PodsOn returns the cached pods bound to a node.
func (c *Cluster) PodsOn(node string) []*corev1.Pod {
	objs, err := c.podInf.GetIndexer().ByIndex(podsByNode, node)
	if err != nil {
		return nil
	}
	out := make([]*corev1.Pod, 0, len(objs))
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			out = append(out, p)
		}
	}
	return out
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

// Counts reports cache sizes and event totals for the metrics endpoint.
func (c *Cluster) Counts() (nodeCount, podCount int, nodeEvents, podEvents uint64) {
	c.mu.RLock()
	nodeEvents, podEvents = c.nodeEvents, c.podEvents
	c.mu.RUnlock()
	return len(c.nodeInf.GetStore().ListKeys()), len(c.podInf.GetStore().ListKeys()), nodeEvents, podEvents
}

func (c *Cluster) emit(ch Change) {
	c.mu.RLock()
	f := c.notify
	c.mu.RUnlock()
	f(ch)
}

func (c *Cluster) nodeChanged(obj any) {
	c.mu.Lock()
	c.nodeEvents++
	delete(c.watchErrs, "nodes")
	c.mu.Unlock()
	if n := asNode(obj); n != nil {
		c.emit(Change{Nodes: []string{n.Name}})
	}
}

func (c *Cluster) podChanged(old, obj any) {
	c.mu.Lock()
	c.podEvents++
	delete(c.watchErrs, "pods")
	c.mu.Unlock()
	names := map[string]bool{}
	if p := asPod(old); p != nil {
		names[p.Spec.NodeName] = true
	}
	if p := asPod(obj); p != nil {
		names[p.Spec.NodeName] = true
	}
	ch := Change{}
	for n := range names {
		// Unscheduled pods change the cluster totals but no node's row.
		if n != "" {
			ch.Nodes = append(ch.Nodes, n)
		} else {
			ch.Totals = true
		}
	}
	c.emit(ch)
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
			Name string `json:"name"`
		} `json:"metadata"`
		Timestamp time.Time         `json:"timestamp"`
		Usage     map[string]string `json:"usage"`
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
				cpu, cerr := resource.ParseQuantity(it.Usage["cpu"])
				mem, merr := resource.ParseQuantity(it.Usage["memory"])
				if cerr != nil || merr != nil {
					continue
				}
				usage[it.Metadata.Name] = nodes.Usage{CPUMilli: cpu.MilliValue(), MemoryBytes: mem.Value()}
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
		c.emit(Change{All: true, Info: true})
	}
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

func podNodeName(obj any) ([]string, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, nil
	}
	return []string{p.Spec.NodeName}, nil
}

func asNode(obj any) *corev1.Node {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	n, _ := obj.(*corev1.Node)
	return n
}

func asPod(obj any) *corev1.Pod {
	if obj == nil {
		return nil
	}
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	p, _ := obj.(*corev1.Pod)
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
