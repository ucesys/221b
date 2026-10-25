// Package fake is a stand-in for the cluster cache in tests: objects are
// put in by hand and read the way the views and the store read them.
package fake

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
)

// Source holds objects by kind and the figures the cluster reads apart
// from them. Every method is safe to call from any goroutine.
type Source struct {
	mu       sync.Mutex
	info     kube.Info
	objects  map[string]map[string]runtime.Object // kind -> cache key -> object
	usage    map[string]nodes.Usage
	podUsage map[string]nodes.Usage
	boots    map[string]time.Time
	volumes  map[string]kube.VolumeStats
	secrets  bool
	polls    int
	wantPods bool
	// OnPoll runs inside Poll, as metrics-server reads do.
	OnPoll func()
}

// New returns a synced, connected source with the named nodes.
func New(nodeNames ...string) *Source {
	s := &Source{
		info:     kube.Info{Name: "test", State: kube.StateConnected, Synced: true, Warnings: []string{}, Metrics: kube.MetricsUnknown, Uptime: kube.UptimeUnknown, Secrets: "read"},
		objects:  map[string]map[string]runtime.Object{},
		usage:    map[string]nodes.Usage{},
		podUsage: map[string]nodes.Usage{},
		boots:    map[string]time.Time{},
		volumes:  map[string]kube.VolumeStats{},
		secrets:  true,
	}
	for _, n := range nodeNames {
		s.Put(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n}})
	}
	return s
}

// SetSecrets turns Secret reading on or off.
func (s *Source) SetSecrets(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = on
	if on {
		s.info.Secrets = "read"
	} else {
		s.info.Secrets = "off"
		delete(s.objects, "secrets")
	}
}

// Put stores an object, trimmed as the cache trims it. The kind comes
// from the object's type.
func (s *Source) Put(obj runtime.Object) {
	k, ok := kindOf(obj)
	if !ok {
		panic("fake: no kind for " + obj.GetObjectKind().GroupVersionKind().String())
	}
	if k.Transform != nil {
		t, err := k.Transform(obj)
		if err != nil {
			panic(err)
		}
		obj = t.(runtime.Object)
	}
	key, _ := cache.MetaNamespaceKeyFunc(obj)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objects[k.Name] == nil {
		s.objects[k.Name] = map[string]runtime.Object{}
	}
	s.objects[k.Name][key] = obj
}

// Delete removes an object; namespace is empty for cluster-scoped kinds.
func (s *Source) Delete(kind, namespace, name string) {
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects[kind], key)
}

// Update changes a stored object in place, under the lock.
func (s *Source) Update(kind, namespace, name string, f func(runtime.Object)) {
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.objects[kind][key]; o != nil {
		f(o)
	}
}

// SetInfo changes the cluster details.
func (s *Source) SetInfo(f func(*kube.Info)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.info)
}

// SetUsage sets a node's CPU and memory in use.
func (s *Source) SetUsage(node string, u nodes.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[node] = u
}

// SetPodUsage replaces every pod's CPU and memory in use.
func (s *Source) SetPodUsage(u map[string]nodes.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.podUsage = map[string]nodes.Usage{}
	for k, v := range u {
		s.podUsage[k] = v
	}
}

// SetBoot sets when a node booted.
func (s *Source) SetBoot(node string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boots[node] = at
}

// Polls counts the calls to Poll.
func (s *Source) Polls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.polls
}

// WantsPodUsage reports what the store last asked for.
func (s *Source) WantsPodUsage() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wantPods
}

func kindOf(obj runtime.Object) (kube.Kind, bool) {
	gvk := obj.GetObjectKind().GroupVersionKind()
	if gvk.Kind == "" {
		// Typed objects carry no TypeMeta; match on the Go type instead.
		for _, k := range kube.Kinds {
			if k.New != nil {
				if n := k.New(); typeName(n) == typeName(obj) {
					return k, true
				}
			}
		}
		if _, ok := obj.(*kube.SecretSummary); ok {
			return kube.KindByName("secrets")
		}
		if _, ok := obj.(*kube.ConfigMapSummary); ok {
			return kube.KindByName("configmaps")
		}
		return kube.Kind{}, false
	}
	return kube.KindOf(gvk.Kind)
}

func typeName(obj runtime.Object) string { return reflect.TypeOf(obj).String() }

// Info returns the cluster details.
func (s *Source) Info() kube.Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Objects returns the objects of a kind, in one namespace or all.
func (s *Source) Objects(kind, namespace string) []runtime.Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []runtime.Object
	for _, o := range s.objects[kind] {
		if namespace == "" || metaOf(o).GetNamespace() == namespace {
			out = append(out, o)
		}
	}
	return out
}

// Object returns one object, or nil.
func (s *Source) Object(kind, namespace, name string) runtime.Object {
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[kind][key]
}

// ByIndex evaluates the kind's indexer the way the cache would.
func (s *Source) ByIndex(kind, index, value string) []runtime.Object {
	k, ok := kube.KindByName(kind)
	if !ok {
		return nil
	}
	f := k.Indexers[index]
	if f == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []runtime.Object
	for _, o := range s.objects[kind] {
		vals, err := f(o)
		if err != nil {
			continue
		}
		for _, v := range vals {
			if v == value {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

// Count counts the objects of a kind.
func (s *Source) Count(kind, namespace string) int {
	return len(s.Objects(kind, namespace))
}

// Kinds lists the kinds watched.
func (s *Source) Kinds() []kube.Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []kube.Kind
	for _, k := range kube.Kinds {
		if k.Secret && !s.secrets {
			continue
		}
		out = append(out, k)
	}
	return out
}

// SecretsRead reports whether Secrets are read.
func (s *Source) SecretsRead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets
}

// Keys lists the cache keys of a kind, sorted.
func (s *Source) Keys(kind string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects[kind]))
	for k := range s.objects[kind] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Node returns a node, or nil.
func (s *Source) Node(name string) *corev1.Node {
	n, _ := s.Object("nodes", "", name).(*corev1.Node)
	return n
}

// PodsOn returns the pods bound to a node ("" for unscheduled ones).
func (s *Source) PodsOn(node string) []*corev1.Pod {
	var out []*corev1.Pod
	for _, o := range s.ByIndex("pods", kube.IndexPodNode, node) {
		out = append(out, o.(*corev1.Pod))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Unscheduled counts pods without a node that have not finished.
func (s *Source) Unscheduled() int {
	n := 0
	for _, p := range s.PodsOn("") {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			n++
		}
	}
	return n
}

// Usage returns a node's CPU and memory in use.
func (s *Source) Usage(node string) (nodes.Usage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[node]
	return u, ok
}

// BootedAt returns when a node booted.
func (s *Source) BootedAt(node string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boots[node]
	return b, ok
}

// PodUsage returns every pod's CPU and memory in use.
func (s *Source) PodUsage() map[string]nodes.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]nodes.Usage, len(s.podUsage))
	for k, v := range s.podUsage {
		out[k] = v
	}
	return out
}

// PodUsageOf returns one pod's CPU and memory in use.
func (s *Source) PodUsageOf(namespace, name string) (nodes.Usage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.podUsage[namespace+"/"+name]
	return u, ok
}

// WantPodUsage records what the store asks for.
func (s *Source) WantPodUsage(want bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wantPods = want
}

// Poll counts itself and runs OnPoll.
func (s *Source) Poll(context.Context) {
	s.mu.Lock()
	s.polls++
	f := s.OnPoll
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

func metaOf(o runtime.Object) metav1.Object {
	m, _ := o.(metav1.Object)
	if m == nil {
		return &metav1.ObjectMeta{}
	}
	return m
}

// SetVolumeStats sets what the kubelets report about a mounted claim.
func (s *Source) SetVolumeStats(namespace, claim string, v kube.VolumeStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volumes[namespace+"/"+claim] = v
}

// VolumeStats returns what the kubelets report about a mounted claim.
func (s *Source) VolumeStats(namespace, claim string) (kube.VolumeStats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[namespace+"/"+claim]
	return v, ok
}
