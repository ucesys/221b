package views

import (
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/ucesys/221b/server/internal/netpol"
)

// The Policies tab (P4-04 to P4-06) reads one row per namespace (network
// isolation and Pod Security), per NetworkPolicy, per disruption budget,
// per node (whether it can be drained), per ResourceQuota and the
// Deployments it constrains, per LimitRange, per admission webhook and per
// PriorityClass, and one about the network plugin.

// Prefixes of the policies view's keys.
const (
	PolNamespace = "ns"
	PolNetPol    = "np"
	PolPDB       = "pdb"
	PolDrain     = "drain"
	PolQuota     = "quota"
	PolRollout   = "rollout"
	PolLimits    = "limits"
	PolHook      = "hook"
	PolPriority  = "pc"
	PolCNI       = "cni"
	PolNoClass   = "pcnone"
)

// SplitPolicyKey reads a key: its prefix and the rest ("web/api", a node's
// name, "Validating/config/webhook").
func SplitPolicyKey(key string) (prefix, rest string, ok bool) {
	prefix, rest, ok = strings.Cut(key, ":")
	return prefix, rest, ok && rest != ""
}

// PolicyKeys lists every row of the policies view.
func PolicyKeys(src Source) []string {
	out := []string{PolCNI + ":cluster", PolNoClass + ":cluster"}
	for _, o := range src.Objects("namespaces", "") {
		if n, ok := o.(*corev1.Namespace); ok {
			out = append(out, PolNamespace+":"+n.Name)
		}
	}
	nsKeys := func(kind, prefix string) {
		for _, o := range src.Objects(kind, "") {
			if m, ok := metaOf(o); ok {
				out = append(out, prefix+":"+m.GetNamespace()+"/"+m.GetName())
			}
		}
	}
	nsKeys("networkpolicies", PolNetPol)
	nsKeys("poddisruptionbudgets", PolPDB)
	nsKeys("resourcequotas", PolQuota)
	nsKeys("limitranges", PolLimits)
	for _, o := range src.Objects("nodes", "") {
		if n, ok := o.(*corev1.Node); ok {
			out = append(out, PolDrain+":"+n.Name)
		}
	}
	quotaNS := map[string]bool{}
	for _, o := range src.Objects("resourcequotas", "") {
		if m, ok := metaOf(o); ok {
			quotaNS[m.GetNamespace()] = true
		}
	}
	for ns := range quotaNS {
		for _, o := range src.Objects("deployments", ns) {
			if d, ok := o.(*appsv1.Deployment); ok {
				out = append(out, PolRollout+":"+d.Namespace+"/"+d.Name)
			}
		}
	}
	out = append(out, hookKeys(src)...)
	for _, o := range src.Objects("priorityclasses", "") {
		if m, ok := metaOf(o); ok {
			out = append(out, PolPriority+":"+m.GetName())
		}
	}
	return out
}

// BuildPolicy builds one row of the policies view.
func BuildPolicy(src Source, key string, now time.Time) (any, bool) {
	prefix, rest, ok := SplitPolicyKey(key)
	if !ok {
		return nil, false
	}
	ns, name, _ := strings.Cut(rest, "/")
	switch prefix {
	case PolCNI:
		return cniRow(src), true
	case PolNoClass:
		return noClassRow(src), true
	case PolNamespace:
		return namespacePolicyRow(src, rest, now)
	case PolNetPol:
		p, _ := src.Object("networkpolicies", ns, name).(*networkingv1.NetworkPolicy)
		if p == nil {
			return nil, false
		}
		return netPolRow(src, p, now), true
	case PolPDB:
		return pdbRow(src, ns, name, now)
	case PolDrain:
		return drainRow(src, rest, now)
	case PolQuota:
		return quotaRow(src, ns, name, now)
	case PolRollout:
		return rolloutRow(src, ns, name)
	case PolLimits:
		return limitsRow(src, ns, name, now)
	case PolHook:
		return hookRow(src, rest, now)
	case PolPriority:
		return priorityRow(src, rest, now)
	}
	return nil, false
}

// live says whether a pod still runs or waits to: not finished, not being
// deleted.
func live(p *corev1.Pod) bool {
	return p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed && p.DeletionTimestamp == nil
}

func podsIn(src Source, ns string) []*corev1.Pod {
	var out []*corev1.Pod
	for _, o := range src.Objects("pods", ns) {
		if p, ok := o.(*corev1.Pod); ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func policiesIn(src Source, ns string) []*networkingv1.NetworkPolicy {
	var out []*networkingv1.NetworkPolicy
	for _, o := range src.Objects("networkpolicies", ns) {
		if p, ok := o.(*networkingv1.NetworkPolicy); ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// workloadName names what runs a pod: its Deployment, StatefulSet,
// DaemonSet or Job, or the pod itself.
func workloadName(src Source, p *corev1.Pod) string {
	if o := controllerOf(src, p.Namespace, p.OwnerReferences); o != nil {
		return o.Name
	}
	return p.Name
}

// ---------- the network plugin ----------

// CNIRow says which network plugin runs and whether it enforces
// NetworkPolicies, as far as its DaemonSet tells.
type CNIRow struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Enforces is "yes", "no" or "unknown".
	Enforces string `json:"enforces"`
	Note     string `json:"note,omitempty"`
}

var cniKnown = []struct{ match, name, enforces, note string }{
	{"cilium", "Cilium", "yes", ""},
	{"calico", "Calico", "yes", ""},
	{"canal", "Canal", "yes", "Calico enforces the policies; flannel carries the traffic."},
	{"antrea", "Antrea", "yes", ""},
	{"kube-router", "kube-router", "yes", ""},
	{"weave", "Weave Net", "yes", ""},
	{"kindnet", "kindnet", "yes", "kindnet enforces NetworkPolicies since kind 0.24."},
	{"kube-ovn", "Kube-OVN", "yes", ""},
	{"aws-node", "Amazon VPC CNI", "unknown", "It enforces NetworkPolicies only when its network policy agent is on."},
	{"azure-cns", "Azure CNI", "unknown", "Azure enforces NetworkPolicies only with Azure or Cilium network policy turned on."},
	{"flannel", "flannel", "no", "flannel alone does not enforce NetworkPolicies: they are stored and ignored."},
}

func cniRow(src Source) CNIRow {
	for _, o := range src.Objects("daemonsets", "kube-system") {
		d, ok := o.(*appsv1.DaemonSet)
		if !ok {
			continue
		}
		for _, k := range cniKnown {
			if strings.Contains(d.Name, k.match) {
				return CNIRow{Key: PolCNI + ":cluster", Name: k.name, Enforces: k.enforces, Note: k.note}
			}
		}
	}
	return CNIRow{Key: PolCNI + ":cluster", Enforces: "unknown", Note: "221B could not tell which network plugin runs, so it cannot say whether these policies are enforced."}
}

// ---------- namespaces ----------

// PolicyNSRow is a namespace as the Policies tab sees it: who may connect
// to its pods, its Pod Security levels and the pods each level rejects,
// and its quotas, limit ranges and budgets.
type PolicyNSRow struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// System marks Kubernetes' own namespaces.
	System bool `json:"system,omitempty"`
	Pods   int  `json:"pods"`
	// HostNetwork pods use their node's network: no policy isolates them.
	HostNetwork int `json:"hostNetwork"`
	Policies    int `json:"policies"`
	// IsolatedIn and IsolatedOut count the other pods some policy
	// isolates, for ingress and egress.
	IsolatedIn  int  `json:"isolatedIn"`
	IsolatedOut int  `json:"isolatedOut"`
	DenyIn      bool `json:"denyIn,omitempty"`
	DenyOut     bool `json:"denyOut,omitempty"`
	// Open are the workloads whose pods no ingress policy selects.
	Open []string `json:"open"`
	// Pod Security: each mode's level and version ("" when unlabelled).
	Enforce        string `json:"enforce,omitempty"`
	EnforceVersion string `json:"enforceVersion,omitempty"`
	Warn           string `json:"warn,omitempty"`
	WarnVersion    string `json:"warnVersion,omitempty"`
	Audit          string `json:"audit,omitempty"`
	AuditVersion   string `json:"auditVersion,omitempty"`
	// Baseline and Restricted are what each level would say of the pods
	// running now.
	Baseline    PSAResult `json:"baseline"`
	Restricted  PSAResult `json:"restricted"`
	Quotas      []string  `json:"quotas"`
	LimitRanges []string  `json:"limitRanges"`
	Budgets     int       `json:"budgets"`
}

var k8sNamespaces = map[string]bool{"default": true, "kube-node-lease": true, "kube-public": true, "kube-system": true}

func namespacePolicyRow(src Source, name string, now time.Time) (any, bool) {
	n, _ := src.Object("namespaces", "", name).(*corev1.Namespace)
	if n == nil {
		return nil, false
	}
	r := PolicyNSRow{Key: PolNamespace + ":" + name, Name: name, System: k8sNamespaces[name], Open: []string{}, Quotas: []string{}, LimitRanges: []string{}}
	pols := policiesIn(src, name)
	r.Policies = len(pols)
	for _, p := range pols {
		in, out := netpol.DefaultDeny(p)
		r.DenyIn = r.DenyIn || in
		r.DenyOut = r.DenyOut || out
	}
	open := map[string]bool{}
	for _, p := range podsIn(src, name) {
		if !live(p) {
			continue
		}
		r.Pods++
		if p.Spec.HostNetwork {
			r.HostNetwork++
			continue
		}
		in, out := false, false
		for _, np := range pols {
			if !netpol.Selects(np, p) {
				continue
			}
			i, e := netpol.Types(np)
			in, out = in || i, out || e
		}
		if in {
			r.IsolatedIn++
		} else if w := workloadName(src, p); !open[w] {
			open[w] = true
			r.Open = append(r.Open, w)
		}
		if out {
			r.IsolatedOut++
		}
	}
	l := n.Labels
	r.Enforce, r.EnforceVersion = l[psaLabel+"enforce"], l[psaLabel+"enforce-version"]
	r.Warn, r.WarnVersion = l[psaLabel+"warn"], l[psaLabel+"warn-version"]
	r.Audit, r.AuditVersion = l[psaLabel+"audit"], l[psaLabel+"audit-version"]
	// The API server checks every pod it lists, finished ones too.
	all := podsIn(src, name)
	r.Baseline = evaluatePSA(all, name, "baseline", r.EnforceVersion)
	r.Restricted = evaluatePSA(all, name, "restricted", r.EnforceVersion)
	for _, o := range src.Objects("resourcequotas", name) {
		if m, ok := metaOf(o); ok {
			r.Quotas = append(r.Quotas, m.GetName())
		}
	}
	for _, o := range src.Objects("limitranges", name) {
		if m, ok := metaOf(o); ok {
			r.LimitRanges = append(r.LimitRanges, m.GetName())
		}
	}
	sort.Strings(r.Quotas)
	sort.Strings(r.LimitRanges)
	r.Budgets = src.Count("poddisruptionbudgets", name)
	_ = now
	return r, true
}

// ---------- network policies ----------

// NetPolRow is one NetworkPolicy.
type NetPolRow struct {
	Key       string     `json:"key"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Selector  string     `json:"selector"`
	Ingress   bool       `json:"ingress"`
	Egress    bool       `json:"egress"`
	Allows    string     `json:"allows,omitempty"`
	AllowsOut string     `json:"allowsOut,omitempty"`
	Pods      []string   `json:"pods"`
	Created   *time.Time `json:"created,omitempty"`
}

func netPolRow(src Source, p *networkingv1.NetworkPolicy, now time.Time) NetPolRow {
	in, eg := netpol.Types(p)
	r := NetPolRow{Key: PolNetPol + ":" + p.Namespace + "/" + p.Name, Namespace: p.Namespace, Name: p.Name, Selector: netpol.SelectorText(&p.Spec.PodSelector), Ingress: in, Egress: eg, Allows: netpol.AllowsText(p), AllowsOut: netpol.AllowsOutText(p), Pods: []string{}, Created: created(p)}
	for _, pod := range podsIn(src, p.Namespace) {
		if live(pod) && netpol.Selects(p, pod) {
			r.Pods = append(r.Pods, pod.Name)
		}
	}
	_ = now
	return r
}

// NetCheck is the answer to "can pod A connect to pod B on a port?"
// (P4-04): what each side's policies decide, and the verdict.
type NetCheck struct {
	From    string      `json:"from"`
	To      string      `json:"to"`
	Port    int32       `json:"port"`
	Proto   string      `json:"protocol"`
	Allowed bool        `json:"allowed"`
	Egress  netpol.Side `json:"egress"`
	Ingress netpol.Side `json:"ingress"`
	// FromHost and ToHost mark pods on their node's network.
	FromHost bool `json:"fromHost,omitempty"`
	ToHost   bool `json:"toHost,omitempty"`
	// ToIP is the address the connection goes to.
	ToIP string `json:"toIP,omitempty"`
}

// CheckConnection decides whether src may open port on dst.
func CheckConnection(src Source, from, to *corev1.Pod, port netpol.Port) NetCheck {
	nsLabels := func(ns string) map[string]string {
		if n, _ := src.Object("namespaces", "", ns).(*corev1.Namespace); n != nil {
			return n.Labels
		}
		return map[string]string{"kubernetes.io/metadata.name": ns}
	}
	a := netpol.Endpoint{Pod: from, NamespaceLabels: nsLabels(from.Namespace)}
	b := netpol.Endpoint{Pod: to, NamespaceLabels: nsLabels(to.Namespace)}
	c := NetCheck{From: from.Namespace + "/" + from.Name, To: to.Namespace + "/" + to.Name, Port: port.Number, Proto: string(port.Protocol), FromHost: from.Spec.HostNetwork, ToHost: to.Spec.HostNetwork, ToIP: b.IP()}
	c.Egress = netpol.Egress(policiesIn(src, from.Namespace), a, b, port)
	c.Ingress = netpol.Ingress(policiesIn(src, to.Namespace), a, b, port)
	c.Allowed = c.Egress.Allowed && c.Ingress.Allowed
	return c
}
