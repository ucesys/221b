package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// --- The objects New exception suggests (Mycroft › Exceptions) ---
//
// GET /api/v1/aiops/objects?cluster=<id>&q=<text>&limit=20 suggests the
// objects a new exception may be on as someone types: first those the
// cluster's cases and exceptions name (the agents' keys), the likeliest
// targets and kinds the console may not keep, then the objects the
// console keeps of the cluster (live.Store.FindObjects, read from its
// caches: Kubernetes is not asked per keystroke), by name and kind, best
// match first, each once, written as the exceptions write them. It is for
// those who may create an exception (cases.approverRoles), as POST
// /api/v1/aiops/exceptions is. A person is offered only the objects they
// may list (P3-02), as the tabs show them. Names only: never what an
// object holds.

const (
	// maxObjectQuery bounds what was typed, as an exception's object is
	// bounded.
	maxObjectQuery = 300
	// The suggestions asked for, by default and at most.
	defaultObjectLimit = 20
	maxObjectLimit     = 50
	// objectCandidates bounds the cluster's objects looked at for one
	// request, before the person's access is applied.
	objectCandidates = 400
	// objectChunk is how many candidates are checked at once: Kubernetes
	// is asked about their kinds and namespaces together, only as far as
	// the suggestions need.
	objectChunk = 40
	// objectsTimeout bounds asking Kubernetes what the person may see.
	objectsTimeout = 5 * time.Second
)

// CaseObject is one object New exception suggests.
type CaseObject struct {
	// Object is as the exception names it: namespace/Kind/name or
	// Kind/name.
	Object    string `json:"object"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// From is "cases" for an object a case or an exception names, and
	// "cluster" for one the console keeps of the cluster.
	From string `json:"from"`
}

// objectFinder is what searches a cluster's objects; *live.Store is one.
type objectFinder interface {
	FindObjects(q string, limit int) []live.ObjectMatch
}

func (s *Server) caseObjects(w http.ResponseWriter, r *http.Request) {
	c, ok := s.lessonsCaller(r)
	switch {
	case !ok:
		jsonError(w, http.StatusUnauthorized, "sign in first")
		return
	case !c.Change:
		jsonError(w, http.StatusForbidden, "Only the people who may approve proposed changes (cases.approverRoles) create, remove or make active again an exception.")
		return
	}
	query := r.URL.Query()
	id, q := strings.TrimSpace(query.Get("cluster")), strings.TrimSpace(query.Get("q"))
	if id == "" {
		jsonError(w, http.StatusBadRequest, "pick the cluster")
		return
	}
	if len(q) > maxObjectQuery {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("type at most %d characters", maxObjectQuery))
		return
	}
	limit := defaultObjectLimit
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			jsonError(w, http.StatusBadRequest, "the limit is a number from 1")
			return
		}
		limit = min(n, maxObjectLimit)
	}
	finder, cl, known := s.objectsOn(id)
	if !known {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a connected cluster", clipText(id, 60)))
		return
	}
	out := []CaseObject{}
	if utf8.RuneCountInString(q) < 2 {
		writeValue(w, r, map[string]any{"objects": out})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), objectsTimeout)
	defer cancel()

	// Who asks: nobody (sign-in off) sees everything; a person whose
	// access cannot be asked about, the cluster not running, sees nothing.
	var acc *access.Access
	if p := s.person(r); !p.Nobody() {
		if cl == nil {
			writeValue(w, r, map[string]any{"objects": out})
			return
		}
		acc = s.opts.Access.For(id, cl, p)
	}

	// The candidates, best first: the cases' and exceptions' objects, then
	// the cluster's, each once.
	type candidate struct {
		o    jobs.Object
		from string
	}
	var cands []candidate
	seen := map[string]bool{}
	for _, o := range s.casesObjects(ctx, id, q) {
		seen[o.String()] = true
		cands = append(cands, candidate{o, "cases"})
	}
	if finder != nil {
		for _, m := range finder.FindObjects(q, objectCandidates) {
			o, err := jobs.NormalizeObject(m.String())
			if err != nil || o.Kind == "" || seen[o.String()] {
				continue
			}
			seen[o.String()] = true
			cands = append(cands, candidate{o, "cluster"})
		}
	}

	asking := acc != nil
	for i := 0; i < len(cands) && len(out) < limit; i += objectChunk {
		chunk := cands[i:min(i+objectChunk, len(cands))]
		if asking {
			var pairs []access.Pair
			for _, c := range chunk {
				if p, ok := objectPair(c.o); ok && !slices.Contains(pairs, p) {
					pairs = append(pairs, p)
				}
			}
			if acc.Stale(pairs) {
				if _, err := acc.Prepare(ctx, pairs, 0); err != nil {
					// What was not answered stays hidden; what is known
					// still shows, and nothing more is asked.
					s.opts.Logger.Warn("asking what a person may see failed", "cluster", id, "user", acc.Person().User, "err", err)
					asking = false
				}
			}
		}
		for _, c := range chunk {
			if len(out) == limit {
				break
			}
			if acc != nil && !objectAllowed(acc, c.o) {
				continue
			}
			out = append(out, CaseObject{Object: c.o.String(), Kind: c.o.Kind, Namespace: c.o.Namespace, Name: c.o.Name, From: c.from})
		}
	}
	writeValue(w, r, map[string]any{"objects": out})
}

// objectsOn finds what searches a cluster's objects and what answers what
// a person may see in it; known is false for no cluster of that id. A
// connection that is not running has neither.
func (s *Server) objectsOn(id string) (finder objectFinder, cl Cluster, known bool) {
	if s.opts.Clusters == nil {
		if id != clusters.LocalID {
			return nil, nil, false
		}
		finder, _ = s.opts.Store.(objectFinder)
		return finder, s.opts.Cluster, true
	}
	st, client, ok := s.opts.Clusters.Lookup(id)
	if !ok {
		return nil, nil, false
	}
	if st != nil {
		finder = st
	}
	cl, _ = client.(Cluster)
	return finder, cl, true
}

// casesObjects are the objects the cluster's exceptions and cases name
// that match what was typed, best first (live.RankObject), each once:
// an agent-found case's key, or the object a case was opened about when
// it reads as one.
func (s *Server) casesObjects(ctx context.Context, cluster, q string) []jobs.Object {
	var named []string
	if s.opts.Lessons != nil {
		list, err := s.opts.Lessons.Objects(ctx, cluster)
		if err != nil {
			s.opts.Logger.Warn("the exceptions cannot be read for New exception's suggestions", "err", err)
		}
		named = append(named, list...)
	}
	if s.opts.Issues != nil {
		lines, err := s.opts.Issues.Index(ctx)
		if err != nil {
			s.opts.Logger.Warn("the cases cannot be read for New exception's suggestions", "err", err)
		}
		for _, l := range lines {
			switch {
			case l.Key != nil && l.Key.Cluster == cluster:
				named = append(named, l.Key.Object)
			case l.Key == nil && l.Cluster == cluster:
				named = append(named, l.Object)
			}
		}
	}
	type ranked struct {
		o    jobs.Object
		rank int
	}
	var found []ranked
	seen := map[string]bool{}
	for _, text := range named {
		o, err := jobs.NormalizeObject(text)
		if err != nil || o.Kind == "" || seen[o.String()] {
			continue
		}
		seen[o.String()] = true
		if rank := live.RankObject(o.String(), q); rank >= 0 {
			found = append(found, ranked{o, rank})
		}
	}
	slices.SortFunc(found, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), strings.Compare(a.o.Name, b.o.Name), strings.Compare(a.o.String(), b.o.String()))
	})
	out := make([]jobs.Object, len(found))
	for i, f := range found {
		out[i] = f.o
	}
	return out
}

// objectPair is what Kubernetes is asked about an object (P3-02): may the
// person list its kind in its namespace, or cluster-wide; a namespace is
// the person's when they may list its pods, as the Namespaces tab has it.
// A kind the console does not watch is the person's when its namespace
// is; ok is false for one without a namespace, which nothing hides.
func objectPair(o jobs.Object) (access.Pair, bool) {
	if o.Kind == "Namespace" {
		return access.Pair{Resource: "pods", Namespace: o.Name}, true
	}
	if k, ok := kube.KindOf(o.Kind); ok {
		return access.Pair{Group: groupOf(k.Resource), Resource: k.Resource, Namespace: o.Namespace}, true
	}
	if o.Namespace != "" {
		return access.Pair{Resource: "pods", Namespace: o.Namespace}, true
	}
	return access.Pair{}, false
}

// objectAllowed says whether the person may see the object, from the
// verdicts at hand.
func objectAllowed(a *access.Access, o jobs.Object) bool {
	p, ok := objectPair(o)
	switch {
	case !ok:
		return true
	case p.Namespace == "":
		return a.Cluster(p.Resource)
	}
	return a.Namespaced(p.Resource, p.Namespace)
}

// clipText cuts a text to at most n characters.
func clipText(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n-1]) + "…"
}
