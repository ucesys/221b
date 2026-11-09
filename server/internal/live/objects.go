package live

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/kube"
)

// The objects the New exception form suggests (Mycroft › Exceptions):
// every object the console keeps of the cluster, by kind, namespace and
// name, searched by what someone types. The index is built from the
// watches' caches, not from the views' rows (most views exist only while a
// tab shows them), and Kubernetes is not asked: it is built again at most
// once per objectIndexAge, and only when changes came since. A search
// reads it without copying it and stops once it has what was asked for.

// objectIndexAge is how long an index is kept while changes come in.
const objectIndexAge = 2 * time.Second

// leftOutKinds are the kinds nobody names in an exception: what the
// controllers make and change by the second.
var leftOutKinds = map[string]bool{"events": true, "leases": true, "endpoints": true, "endpointslices": true, "controllerrevisions": true, "volumeattachments": true}

// ObjectMatch is one object a search found.
type ObjectMatch struct {
	Kind      kube.Kind
	Namespace string
	Name      string
}

// String is the object as an exception names it: namespace/Kind/name, or
// Kind/name for a cluster-scoped one.
func (m ObjectMatch) String() string {
	if m.Namespace == "" {
		return m.Kind.Kind + "/" + m.Name
	}
	return m.Namespace + "/" + m.Kind.Kind + "/" + m.Name
}

// indexedObject is one object of the index.
type indexedObject struct {
	// key is the cache's: "namespace/name", or "name".
	key string
	// lower is "namespace/kind/name" or "kind/name" in lower case; the
	// kind starts at kindAt, the name at nameAt.
	lower          string
	kindAt, nameAt int32
	kind           uint16
}

type objectIndex struct {
	at    time.Time
	gen   uint64
	kinds []kube.Kind
	items []indexedObject
}

// buildObjectIndex reads the keys of every kind the source watches,
// sorted by name, then namespace, then kind.
func buildObjectIndex(src Source, now time.Time, gen uint64) *objectIndex {
	ix := &objectIndex{at: now, gen: gen}
	for _, k := range src.Kinds() {
		if leftOutKinds[k.Name] {
			continue
		}
		keys := src.Keys(k.Name)
		if len(keys) == 0 {
			continue
		}
		kind := uint16(len(ix.kinds))
		ix.kinds = append(ix.kinds, k)
		kindLower := strings.ToLower(k.Kind)
		for _, key := range keys {
			ns, name, namespaced := strings.Cut(key, "/")
			if !namespaced {
				ns, name = "", key
			}
			var b strings.Builder
			b.Grow(len(key) + len(kindLower) + 2)
			if namespaced {
				b.WriteString(strings.ToLower(ns))
				b.WriteByte('/')
			}
			kindAt := int32(b.Len())
			b.WriteString(kindLower)
			b.WriteByte('/')
			nameAt := int32(b.Len())
			b.WriteString(strings.ToLower(name))
			ix.items = append(ix.items, indexedObject{key: key, lower: b.String(), kindAt: kindAt, nameAt: nameAt, kind: kind})
		}
	}
	slices.SortFunc(ix.items, func(a, b indexedObject) int {
		return cmp.Or(strings.Compare(a.lower[a.nameAt:], b.lower[b.nameAt:]), strings.Compare(a.lower[:a.kindAt], b.lower[:b.kindAt]), cmp.Compare(ix.kinds[a.kind].Order, ix.kinds[b.kind].Order))
	})
	return ix
}

// Object ranks, best first: the name, kind/name or the whole object is
// what was typed; the name or kind/name starts with it; the whole object
// does (its namespace); it is anywhere in it.
const (
	rankExact = iota
	rankPrefix
	rankNamespace
	rankAnywhere
	ranks
)

// rankObject ranks an object's text ("namespace/kind/name" or
// "kind/name", lower case) for a lower-case query, -1 when it does not
// match. kindAt and nameAt are where its kind and name start.
func rankObject(lower string, kindAt, nameAt int, q string) int {
	name, kindName := lower[nameAt:], lower[kindAt:]
	switch {
	case name == q || kindName == q || lower == q:
		return rankExact
	case strings.HasPrefix(name, q) || strings.HasPrefix(kindName, q):
		return rankPrefix
	case strings.HasPrefix(lower, q):
		return rankNamespace
	case strings.Contains(lower, q):
		return rankAnywhere
	}
	return -1
}

// RankObject ranks an object as an exception names it (namespace/Kind/name
// or Kind/name) for what someone typed, as FindObjects ranks the cluster's:
// 0 is best, -1 does not match. The caller ranks other objects with it, as
// the objects the cases name.
func RankObject(object, q string) int {
	lower, q := strings.ToLower(object), strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return -1
	}
	kindAt, nameAt := 0, strings.LastIndexByte(lower, '/')+1
	if strings.Count(lower, "/") == 2 {
		kindAt = strings.IndexByte(lower, '/') + 1
	}
	return rankObject(lower, kindAt, nameAt, q)
}

func (s *Store) objectIndex() *objectIndex {
	fresh := func(ix *objectIndex, now time.Time, gen uint64) bool {
		return ix != nil && (ix.gen == gen || now.Sub(ix.at) < objectIndexAge && !now.Before(ix.at))
	}
	now, gen := s.opts.Now(), s.objGen.Load()
	if ix := s.objects.Load(); fresh(ix, now, gen) {
		return ix
	}
	s.objMu.Lock()
	defer s.objMu.Unlock()
	if ix := s.objects.Load(); fresh(ix, now, gen) {
		return ix
	}
	ix := buildObjectIndex(s.src, now, gen)
	s.objects.Store(ix)
	return ix
}

// FindObjects finds up to limit objects of the cluster whose namespace,
// kind or name holds q (any case), best first (RankObject); within a rank
// by name, then namespace, then kind. The caller filters them for the
// person asking.
func (s *Store) FindObjects(q string, limit int) []ObjectMatch {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" || limit <= 0 {
		return nil
	}
	ix := s.objectIndex()
	// One pass, keeping each rank's first finds: once the ranks before
	// need hold limit, nothing ranked later is needed, and once the exact
	// matches alone do, the pass stops.
	var found [ranks][]int32
	need, full := ranks, false
	for i := range ix.items {
		o := &ix.items[i]
		r := rankObject(o.lower, int(o.kindAt), int(o.nameAt), q)
		if r < 0 || r >= need || full && r == need-1 {
			continue
		}
		found[r] = append(found[r], int32(i))
		held := 0
		for j := 0; j < need; j++ {
			held += len(found[j])
			if held >= limit {
				need, full = j+1, true
				break
			}
		}
		if full && need == 1 {
			break
		}
	}
	out := make([]ObjectMatch, 0, limit)
	for r := 0; r < ranks && len(out) < limit; r++ {
		for _, i := range found[r] {
			if len(out) == limit {
				break
			}
			o := &ix.items[i]
			m := ObjectMatch{Kind: ix.kinds[o.kind], Name: o.key}
			if ns, name, ok := strings.Cut(o.key, "/"); ok {
				m.Namespace, m.Name = ns, name
			}
			out = append(out, m)
		}
	}
	return out
}
