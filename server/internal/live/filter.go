package live

import (
	"encoding/json"
	"strings"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/views"
)

// Filter decides which rows a browser may see (P3-02): the access of the
// signed-in person, as Kubernetes answered it. nil sees everything, which
// is the console itself with sign-in off. Browsers with the same filter
// (compared by identity: a filter is a pointer) share one encoding of
// each change.
type Filter interface {
	// Allow says whether the row with the key in the view may be sent.
	Allow(view, key string) bool
	// AllowNamespace says whether the namespace is the person's: whether
	// they may list pods in it.
	AllowNamespace(namespace string) bool
	// AllowCluster says whether the person may list the cluster-scoped
	// resource.
	AllowCluster(resource string) bool
}

// Scope says which resource, in which namespace, a row of a view stands
// for, so a filter can ask Kubernetes about it: a pod row is pods in its
// namespace, a namespace row is pods in that namespace (what makes a
// namespace someone's), a release row is secrets, where Helm keeps it, and
// a node row is nodes, cluster-wide.
func Scope(view, key string) (resource, namespace string, cluster bool) {
	switch view {
	case ViewNodes:
		return "nodes", "", true
	case ViewNamespaces:
		return "pods", key, false
	case ViewPods:
		ns, _, _ := strings.Cut(key, "/")
		return "pods", ns, false
	case ViewServices:
		ns, _, _ := strings.Cut(key, "/")
		return "services", ns, false
	case ViewHelm:
		ns, _, _ := strings.Cut(key, "/")
		return "secrets", ns, false
	case ViewFleets:
		return "nodes", "", true
	case ViewAlerts:
		kind, ns, _, ok := views.SplitAlertKey(key)
		switch {
		case !ok:
			return "", "", false
		case kind == views.AlertNode:
			return "nodes", "", true
		case kind == views.AlertPVC:
			return "persistentvolumeclaims", ns, false
		}
		return "pods", ns, false
	}
	if ns, ok := strings.CutPrefix(view, NamespacePrefix); ok {
		kind, _, _ := strings.Cut(key, "/")
		for _, k := range kube.Kinds {
			if k.Kind == kind {
				return k.Resource, ns, false
			}
		}
		return "pods", ns, false
	}
	return "", "", false
}

// Resources lists the resources a view's rows stand for: those listed in
// namespaces, and those listed cluster-wide.
func Resources(view string) (namespaced, cluster []string) {
	switch view {
	case ViewNodes, ViewFleets:
		return nil, []string{"nodes"}
	case ViewNamespaces, ViewPods:
		return []string{"pods"}, nil
	case ViewServices:
		return []string{"services"}, nil
	case ViewHelm:
		return []string{"secrets"}, nil
	case ViewAlerts:
		return []string{"pods", "persistentvolumeclaims"}, []string{"nodes"}
	}
	if strings.HasPrefix(view, NamespacePrefix) {
		for _, k := range kube.Kinds {
			if k.Namespaced {
				namespaced = append(namespaced, k.Resource)
			}
		}
		return namespaced, nil
	}
	return nil, nil
}

// NamespaceOf is the namespace of a one-namespace view ("ns:web" → "web").
func NamespaceOf(view string) (string, bool) {
	return strings.CutPrefix(view, NamespacePrefix)
}

// Redact removes from a row what names objects the filter hides: a node
// row's problems name the pods behind them, which someone who may not
// list that namespace must not see. The counts stay, as totals do. Other
// rows name nothing outside their own namespace and pass unchanged.
func Redact(view string, row []byte, f Filter) []byte {
	if f == nil || view != ViewNodes {
		return row
	}
	var r nodes.Row
	if err := json.Unmarshal(row, &r); err != nil {
		return row
	}
	changed := false
	for i := range r.Problems {
		kept := r.Problems[i].Entries[:0:0]
		for _, e := range r.Problems[i].Entries {
			if ns, ok := podNamespace(e.Object); ok && !f.AllowNamespace(ns) {
				changed = true
				continue
			}
			kept = append(kept, e)
		}
		if kept == nil {
			kept = []nodes.Entry{}
		}
		r.Problems[i].Entries = kept
	}
	if !changed {
		return row
	}
	out, err := json.Marshal(r)
	if err != nil {
		return row
	}
	return out
}

// podNamespace reads the namespace of a problem entry's "pod ns/name".
func podNamespace(object string) (string, bool) {
	rest, ok := strings.CutPrefix(object, "pod ")
	if !ok {
		return "", false
	}
	ns, _, ok := strings.Cut(rest, "/")
	return ns, ok
}
