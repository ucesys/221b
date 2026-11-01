package live

import (
	"bytes"
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
	case ViewAccess:
		prefix, kind, ns, _, ok := views.SplitAccessKey(key)
		switch {
		case !ok:
			return "", "", false
		case prefix == views.AccessSA:
			return "serviceaccounts", ns, false
		case kind == "ClusterRole":
			return "clusterroles", "", true
		case kind == "ClusterRoleBinding":
			return "clusterrolebindings", "", true
		case kind == "Role":
			return "roles", ns, false
		}
		return "rolebindings", ns, false
	case ViewNetworks:
		prefix, rest, ok := views.SplitNetworkKey(key)
		if !ok {
			return "", "", false
		}
		if prefix == views.NetRoute {
			ns, _, _ := strings.Cut(rest, "/")
			return "ingresses", ns, false
		}
		return "nodes", "", true
	case ViewPolicies:
		prefix, rest, ok := views.SplitPolicyKey(key)
		if !ok {
			return "", "", false
		}
		ns, _, _ := strings.Cut(rest, "/")
		switch prefix {
		case views.PolCNI:
			return "pods", "kube-system", false
		case views.PolNoClass, views.PolDrain:
			return "pods", "", true
		case views.PolNamespace:
			return "pods", rest, false
		case views.PolNetPol:
			return "networkpolicies", ns, false
		case views.PolPDB:
			return "poddisruptionbudgets", ns, false
		case views.PolQuota:
			return "resourcequotas", ns, false
		case views.PolRollout:
			return "deployments", ns, false
		case views.PolLimits:
			return "limitranges", ns, false
		case views.PolHook:
			if strings.HasPrefix(rest, "Mutating/") {
				return "mutatingwebhookconfigurations", "", true
			}
			return "validatingwebhookconfigurations", "", true
		case views.PolPriority:
			return "priorityclasses", "", true
		}
		return "", "", false
	case ViewStorage:
		kind, ns, _, ok := views.SplitStorageKey(key)
		switch {
		case !ok:
			return "", "", false
		case kind == views.StClass:
			return "storageclasses", "", true
		case kind == views.StVolume:
			return "persistentvolumes", "", true
		case kind == views.StQuota:
			return "resourcequotas", ns, false
		}
		return "persistentvolumeclaims", ns, false
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
	case ViewAccess:
		return []string{"roles", "rolebindings", "serviceaccounts"}, []string{"clusterroles", "clusterrolebindings"}
	case ViewNetworks:
		return []string{"ingresses"}, []string{"nodes"}
	case ViewStorage:
		return []string{"persistentvolumeclaims", "resourcequotas"}, []string{"storageclasses", "persistentvolumes"}
	case ViewPolicies:
		return []string{"pods", "networkpolicies", "poddisruptionbudgets", "resourcequotas", "deployments", "limitranges"}, []string{"pods", "validatingwebhookconfigurations", "mutatingwebhookconfigurations", "priorityclasses"}
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

// Redact removes from a row what names objects the filter hides: a row
// that stands for a cluster-scoped object can name the pods, workloads and
// Services behind it, which someone who may not list their namespace must
// not see. The counts stay, as totals do. Other rows name nothing outside
// their own namespace and pass unchanged.
func Redact(view string, row []byte, f Filter) []byte {
	if f == nil {
		return row
	}
	switch view {
	case ViewNodes:
		return redactNode(row, f)
	case ViewStorage:
		return redactStorage(row, f)
	case ViewAlerts:
		if rowOf(row, views.AlertNode) {
			return redactNodeAlert(row, f)
		}
	case ViewPolicies:
		if rowOf(row, views.PolHook) {
			return redactHook(row, f)
		}
		if rowOf(row, views.PolPriority) {
			return redactPriority(row, f)
		}
	case ViewNetworks:
		if rowOf(row, views.NetFacts) {
			return redactNetFacts(row, f)
		}
	}
	return row
}

// rowOf says whether a row's key has the prefix, without decoding it:
// every row of these views writes its key first.
func rowOf(row []byte, prefix string) bool {
	return bytes.HasPrefix(row, []byte(`{"key":"`+prefix+`:`))
}

func redactNode(row []byte, f Filter) []byte {
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

// redactNodeAlert drops a node's events that name a pod the filter hides.
func redactNodeAlert(row []byte, f Filter) []byte {
	var r views.AlertRow
	if err := json.Unmarshal(row, &r); err != nil {
		return row
	}
	kept := make([]views.AlertEntry, 0, len(r.Events))
	for _, e := range r.Events {
		ns, _, named := strings.Cut(e.Pod, "/")
		if !named {
			ns, named = views.PodNamespaceIn(e.Message)
		}
		if !named || f.AllowNamespace(ns) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(r.Events) {
		return row
	}
	r.Events = kept
	return encoded(r, row)
}

// redactHook drops a webhook's backend pods when the filter hides their
// namespace. The webhook's own object names its Service, so that stays.
func redactHook(row []byte, f Filter) []byte {
	var r views.HookRow
	if err := json.Unmarshal(row, &r); err != nil || len(r.Reach.Pods) == 0 || f.AllowNamespace(r.ServiceNamespace) {
		return row
	}
	r.Reach.Pods = []views.HookPod{}
	return encoded(r, row)
}

// redactPriority keeps a class's workloads to the namespaces the filter
// allows.
func redactPriority(row []byte, f Filter) []byte {
	var r views.PriorityRow
	if err := json.Unmarshal(row, &r); err != nil {
		return row
	}
	kept := make([]string, 0, len(r.Workloads))
	for _, w := range r.Workloads {
		if ns, _, _ := strings.Cut(w, "/"); f.AllowNamespace(ns) {
			kept = append(kept, w)
		}
	}
	if len(kept) == len(r.Workloads) {
		return row
	}
	r.Workloads = kept
	return encoded(r, row)
}

// redactNetFacts keeps the Services and namespaces the row lists to those
// the filter allows.
func redactNetFacts(row []byte, f Filter) []byte {
	var r views.NetFactsRow
	if err := json.Unmarshal(row, &r); err != nil {
		return row
	}
	services := make([]string, 0, len(r.PendingLBs))
	for _, s := range r.PendingLBs {
		if f.Allow(ViewServices, s) {
			services = append(services, s)
		}
	}
	namespaces := make([]string, 0, len(r.PolicyNS))
	for _, ns := range r.PolicyNS {
		if f.AllowNamespace(ns) {
			namespaces = append(namespaces, ns)
		}
	}
	if len(services) == len(r.PendingLBs) && len(namespaces) == len(r.PolicyNS) {
		return row
	}
	r.PendingLBs, r.PolicyNS = services, namespaces
	return encoded(r, row)
}

// encoded is a redacted row as JSON. Nothing the views build fails to
// encode; if a row did, nothing is better than the row unredacted.
func encoded(r any, row []byte) []byte {
	out, err := json.Marshal(r)
	if err != nil {
		return []byte(`{}`)
	}
	return out
}

// redactStorage keeps a claim's row to what the claim itself says for
// someone who may not read volumes (ST-11, ST-20): the volume's name and
// the class's name come from the claim's spec; its reclaim policy, source,
// labels and attachments come from the volume and go.
func redactStorage(row []byte, f Filter) []byte {
	if f.AllowCluster("persistentvolumes") {
		return row
	}
	var r views.StorageRow
	if err := json.Unmarshal(row, &r); err != nil || r.Pair == nil {
		return row
	}
	p := r.Pair
	p.Reclaim, p.Source, p.VolumeLabels, p.Attached, p.WasClaim = "", nil, nil, nil, ""
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
