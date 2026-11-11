package api

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/sherlocktools"
)

// Sherlock's tools are package sherlocktools (A-2); here they are given
// the console's API, called in-process as the person asking, and the two
// reads that do not go through it: the home page's summary and the cached
// events.

func (s *Server) toolboxFor(r *http.Request, cluster string, cl Cluster) *sherlocktools.Toolbox {
	sess, ok := auth.SessionFrom(r.Context())
	return s.newToolbox(cluster, cl, sess, ok)
}

// newToolbox is the toolbox for a cluster, read as sess when hasSess is
// set.
func (s *Server) newToolbox(cluster string, cl Cluster, sess auth.Session, hasSess bool) *sherlocktools.Toolbox {
	return sherlocktools.New(sherlocktools.Console{
		API:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }),
		Overview: func(ctx context.Context) any { return s.toolOverview(ctx, cluster, cl) },
		Events: func(ctx context.Context, namespace string) []*corev1.Event {
			return s.toolEvents(ctx, cluster, cl, s.toolPerson(sess, hasSess), namespace)
		},
	}, cluster, sess, hasSess)
}

// toolOverview is cluster_overview's summary: the home page's, as the
// person whose session ctx carries sees it.
func (s *Server) toolOverview(ctx context.Context, cluster string, cl Cluster) any {
	h := homeCluster{ID: cluster}
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(cluster); ok {
			h = homeOf(sum)
		}
	}
	st, _, ok := s.toolStore(cluster, cl)
	if ok {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
		s.fillHome(req, &h, st, cl)
	}
	return h
}

func (s *Server) toolStore(cluster string, cl Cluster) (*live.Store, Cluster, bool) {
	if s.opts.Clusters == nil {
		st, _ := s.opts.Store.(*live.Store)
		return st, cl, st != nil
	}
	st, client, ok := s.opts.Clusters.Lookup(cluster)
	c, _ := client.(Cluster)
	return st, c, ok && st != nil && c != nil
}

// toolEvents reads events from the cache, keeping those in namespaces the
// person may list events in (and node events only for one who may list
// nodes).
func (s *Server) toolEvents(ctx context.Context, cluster string, cl Cluster, p kube.Person, ns string) []*corev1.Event {
	var a *access.Access
	if !p.Nobody() && s.opts.Access != nil {
		a = s.opts.Access.For(cluster, cl, p)
		var pairs []access.Pair
		for _, n := range append(namespaceNames(cl), "default") {
			if ns == "" || n == ns {
				pairs = append(pairs, access.Pair{Resource: "events", Namespace: n})
			}
		}
		pairs = append(pairs, access.Pair{Resource: "nodes"})
		if a.Stale(pairs) {
			c, cancel := context.WithTimeout(ctx, prepareTimeout)
			_, _ = a.Prepare(c, pairs, 0)
			cancel()
		}
	}
	var out []*corev1.Event
	for _, o := range cl.Objects("events", ns) {
		e, ok := o.(*corev1.Event)
		if !ok {
			continue
		}
		if a != nil {
			if !a.Namespaced("events", e.Namespace) || e.InvolvedObject.Kind == "Node" && !a.Cluster("nodes") {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// toolPerson is the person the tools read as.
func (s *Server) toolPerson(sess auth.Session, hasSess bool) kube.Person {
	if s.opts.Auth == nil || s.opts.Access == nil || !hasSess {
		return kube.Person{}
	}
	return s.opts.Impersonation.personOf(sess.User)
}
