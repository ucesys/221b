// Package access decides what a signed-in person may see in a cluster
// (P3-02). The console watches everything with its own account and keeps
// the rows for everyone; before a row reaches a person, Kubernetes is
// asked, through SubjectAccessReviews made by the console, whether that
// person may list the resource in that namespace. The answers are kept
// for a while per person and cluster, so a stream costs one review per
// namespace and resource it shows, not one per row.
package access

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// Checker answers whether a person may do something; *kube.Cluster is one.
type Checker interface {
	CanI(ctx context.Context, p kube.Person, verb, group, resource, namespace string) (bool, error)
}

// Pair is one question: may the person list the resource in the
// namespace ("" for a cluster-scoped resource)?
type Pair struct {
	Group     string
	Resource  string
	Namespace string
}

type verdict struct {
	allowed bool
	at      time.Time
}

// Access is what one person may see in one cluster: the verdicts so far.
// It satisfies live.Filter; unknown pairs are denied until Prepare has
// asked about them, so nothing is shown before Kubernetes has said so.
type Access struct {
	person  kube.Person
	checker Checker
	ttl     time.Duration
	now     func() time.Time

	mu       sync.RWMutex
	verdicts map[Pair]verdict
	// version counts changes to the verdicts, so a stream knows when to
	// send a fresh snapshot.
	version uint64
	lastUse time.Time
}

// Options tune the cache.
type Options struct {
	// TTL is how long a verdict stands before it is asked again: RBAC
	// changes reach people within it.
	TTL time.Duration
	// Parallel bounds the reviews in flight at once.
	Parallel int
	Now      func() time.Time
}

// Cache keeps an Access per person and cluster.
type Cache struct {
	opts    Options
	mu      sync.Mutex
	entries map[string]*Access
}

// NewCache makes a cache.
func NewCache(o Options) *Cache {
	if o.TTL <= 0 {
		o.TTL = 2 * time.Minute
	}
	if o.Parallel <= 0 {
		o.Parallel = 16
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Cache{opts: o, entries: map[string]*Access{}}
}

// For returns the person's access in the cluster, made on first use. The
// console itself (a zero Person) has no filter: nil.
func (c *Cache) For(cluster string, checker Checker, p kube.Person) *Access {
	if p.Nobody() {
		return nil
	}
	key := cluster + "\x00" + p.Key()
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.entries[key]
	if !ok {
		a = &Access{person: p, ttl: c.opts.TTL, now: c.opts.Now, verdicts: map[Pair]verdict{}}
		c.entries[key] = a
	}
	a.mu.Lock()
	// The connection may have been replaced since (new credentials): the
	// questions go to the one running now.
	a.checker = checker
	a.lastUse = c.opts.Now()
	a.mu.Unlock()
	return a
}

// Sweep drops the access of people who have not asked for a while.
func (c *Cache) Sweep(idle time.Duration) {
	now := c.opts.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, a := range c.entries {
		a.mu.RLock()
		last := a.lastUse
		a.mu.RUnlock()
		if now.Sub(last) > idle {
			delete(c.entries, key)
		}
	}
}

// Count is how many people's access is kept.
func (c *Cache) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Person is who the access is for.
func (a *Access) Person() kube.Person { return a.person }

// Version counts the changes to the verdicts so far.
func (a *Access) Version() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.version
}

// Namespaced says whether the person may list the resource in the
// namespace; unknown pairs are denied until Prepare has asked.
func (a *Access) Namespaced(resource, namespace string) bool {
	return a.allowed(Pair{Group: groupOf(resource), Resource: resource, Namespace: namespace})
}

// Cluster says whether the person may list the cluster-scoped resource.
func (a *Access) Cluster(resource string) bool {
	return a.allowed(Pair{Group: groupOf(resource), Resource: resource})
}

func (a *Access) allowed(p Pair) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	v, ok := a.verdicts[p]
	return ok && v.allowed
}

// Prepare asks about the pairs it has no fresh verdict for, a few at a
// time, and reports whether any verdict changed. A review that fails
// leaves the pair unknown, which denies it, and returns the error after
// the others ran.
func (a *Access) Prepare(ctx context.Context, pairs []Pair, parallel int) (changed bool, err error) {
	now := a.now()
	a.mu.RLock()
	var ask []Pair
	seen := map[Pair]bool{}
	for _, p := range pairs {
		if seen[p] {
			continue
		}
		seen[p] = true
		if v, ok := a.verdicts[p]; !ok || now.Sub(v.at) >= a.ttl {
			ask = append(ask, p)
		}
	}
	a.mu.RUnlock()
	if len(ask) == 0 {
		return false, nil
	}
	sort.Slice(ask, func(i, j int) bool {
		if ask[i].Namespace != ask[j].Namespace {
			return ask[i].Namespace < ask[j].Namespace
		}
		return ask[i].Resource < ask[j].Resource
	})
	if parallel <= 0 {
		parallel = 16
	}
	type answer struct {
		pair    Pair
		allowed bool
		err     error
	}
	a.mu.RLock()
	checker := a.checker
	a.mu.RUnlock()
	answers := make([]answer, len(ask))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, p := range ask {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p Pair) {
			defer wg.Done()
			defer func() { <-sem }()
			ok, e := checker.CanI(ctx, a.person, "list", p.Group, p.Resource, p.Namespace)
			answers[i] = answer{pair: p, allowed: ok, err: e}
		}(i, p)
	}
	wg.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ans := range answers {
		if ans.err != nil {
			if err == nil {
				err = ans.err
			}
			continue
		}
		old, had := a.verdicts[ans.pair]
		if !had || old.allowed != ans.allowed {
			changed = true
		}
		a.verdicts[ans.pair] = verdict{allowed: ans.allowed, at: now}
	}
	if changed {
		a.version++
	}
	a.lastUse = now
	return changed, err
}

// Stale reports whether any of the pairs needs asking again.
func (a *Access) Stale(pairs []Pair) bool {
	now := a.now()
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, p := range pairs {
		if v, ok := a.verdicts[p]; !ok || now.Sub(v.at) >= a.ttl {
			return true
		}
	}
	return false
}

// Allow is live.Filter: whether a row of a view may be sent to the person.
func (a *Access) Allow(view, key string) bool {
	resource, namespace, cluster := live.Scope(view, key)
	if cluster {
		return a.Cluster(resource)
	}
	return a.Namespaced(resource, namespace)
}

// AllowNamespace is live.Filter: whether the person may list pods in the
// namespace, which is what makes a namespace theirs.
func (a *Access) AllowNamespace(namespace string) bool {
	return a.Namespaced("pods", namespace)
}

// AllowCluster is live.Filter: whether the person may list the
// cluster-scoped resource.
func (a *Access) AllowCluster(resource string) bool {
	return a.Cluster(resource)
}

// Pairs lists what a stream of the views needs verdicts for, given the
// namespaces that exist: the cluster-scoped resources, and for every
// namespace the resources the views show there.
func Pairs(views []string, namespaces []string) []Pair {
	var out []Pair
	seen := map[Pair]bool{}
	add := func(p Pair) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, view := range views {
		resources, cluster := live.Resources(view)
		for _, r := range resources {
			p := Pair{Group: groupOf(r), Resource: r}
			if cluster {
				add(p)
				continue
			}
			if ns, ok := live.NamespaceOf(view); ok {
				p.Namespace = ns
				add(p)
				continue
			}
			for _, ns := range namespaces {
				p.Namespace = ns
				add(p)
			}
		}
	}
	// The namespace rows and the counts need pods in every namespace.
	for _, ns := range namespaces {
		add(Pair{Resource: "pods", Namespace: ns})
	}
	return out
}

// groupOf is the API group of a resource the console shows.
func groupOf(resource string) string {
	for _, k := range kube.Kinds {
		if k.Resource == resource {
			return k.Group
		}
	}
	return ""
}
