// Package netpol reads Kubernetes NetworkPolicies the way a network plugin
// that enforces them does (P4-04): which pods a policy isolates, in which
// direction, and whether a connection from one pod to another on a port
// is allowed, naming the policies that decide. Plugins agree on these
// semantics; where they differ (traffic from a node's own address, which
// most allow, and policies of their own kinds) the answer says so.
package netpol

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Types says which directions a policy restricts: what policyTypes lists,
// or, when it lists nothing, Ingress, and Egress when it has egress rules.
func Types(p *networkingv1.NetworkPolicy) (ingress, egress bool) {
	if len(p.Spec.PolicyTypes) == 0 {
		return true, len(p.Spec.Egress) > 0
	}
	for _, t := range p.Spec.PolicyTypes {
		switch t {
		case networkingv1.PolicyTypeIngress:
			ingress = true
		case networkingv1.PolicyTypeEgress:
			egress = true
		}
	}
	return ingress, egress
}

func selector(s *metav1.LabelSelector) labels.Selector {
	if s == nil {
		return labels.Nothing()
	}
	sel, err := metav1.LabelSelectorAsSelector(s)
	if err != nil {
		return labels.Nothing()
	}
	return sel
}

// Selects says whether a policy applies to a pod: same namespace, and its
// podSelector matches (an empty one matches every pod). Pods on the host's
// network are never isolated.
func Selects(p *networkingv1.NetworkPolicy, pod *corev1.Pod) bool {
	return pod.Namespace == p.Namespace && !pod.Spec.HostNetwork && selector(&p.Spec.PodSelector).Matches(labels.Set(pod.Labels))
}

// Endpoint is one end of a connection.
type Endpoint struct {
	Pod *corev1.Pod
	// NamespaceLabels are the labels of the pod's namespace.
	NamespaceLabels map[string]string
}

// IP is the address the other end sees: the pod's, or its node's for a
// pod on the host's network.
func (e Endpoint) IP() string {
	if e.Pod.Spec.HostNetwork {
		return e.Pod.Status.HostIP
	}
	return e.Pod.Status.PodIP
}

// peerMatches says whether a rule's peer matches an endpoint, seen from a
// policy in namespace ns.
func peerMatches(peer networkingv1.NetworkPolicyPeer, ns string, e Endpoint) bool {
	if peer.IPBlock != nil {
		ip, err := netip.ParseAddr(e.IP())
		if err != nil {
			return false
		}
		cidr, err := netip.ParsePrefix(peer.IPBlock.CIDR)
		if err != nil || !cidr.Contains(ip) {
			return false
		}
		for _, x := range peer.IPBlock.Except {
			if ex, err := netip.ParsePrefix(x); err == nil && ex.Contains(ip) {
				return false
			}
		}
		return true
	}
	// Pod and namespace selectors match pods by their pod address; a pod on
	// the host's network has its node's.
	if e.Pod.Spec.HostNetwork {
		return false
	}
	if peer.NamespaceSelector == nil {
		if e.Pod.Namespace != ns {
			return false
		}
	} else if !selector(peer.NamespaceSelector).Matches(labels.Set(e.NamespaceLabels)) {
		return false
	}
	if peer.PodSelector == nil {
		return true
	}
	return selector(peer.PodSelector).Matches(labels.Set(e.Pod.Labels))
}

// Port is the port a connection is opened to on the destination pod.
type Port struct {
	Number   int32
	Protocol corev1.Protocol
}

// portMatches says whether a rule's port matches: by number (or a range up
// to endPort), or by the name of one of the destination pod's container
// ports. A missing protocol is TCP.
func portMatches(rp networkingv1.NetworkPolicyPort, port Port, dst *corev1.Pod) bool {
	proto := corev1.ProtocolTCP
	if rp.Protocol != nil {
		proto = *rp.Protocol
	}
	if proto != port.Protocol {
		return false
	}
	if rp.Port == nil {
		return true
	}
	if rp.Port.Type == 0 {
		lo, hi := rp.Port.IntVal, rp.Port.IntVal
		if rp.EndPort != nil {
			hi = *rp.EndPort
		}
		return port.Number >= lo && port.Number <= hi
	}
	for _, c := range dst.Spec.Containers {
		for _, cp := range c.Ports {
			p := cp.Protocol
			if p == "" {
				p = corev1.ProtocolTCP
			}
			if cp.Name == rp.Port.StrVal && cp.ContainerPort == port.Number && p == port.Protocol {
				return true
			}
		}
	}
	return false
}

func portsMatch(ports []networkingv1.NetworkPolicyPort, port Port, dst *corev1.Pod) bool {
	if len(ports) == 0 {
		return true
	}
	for _, p := range ports {
		if portMatches(p, port, dst) {
			return true
		}
	}
	return false
}

func peersMatch(peers []networkingv1.NetworkPolicyPeer, ns string, e Endpoint) bool {
	if len(peers) == 0 {
		return true
	}
	for _, p := range peers {
		if peerMatches(p, ns, e) {
			return true
		}
	}
	return false
}

// Side is what one direction decides: whether any policy isolates the pod
// in that direction, which do, and which of them have a rule that admits
// the connection.
type Side struct {
	Isolated  bool     `json:"isolated"`
	Allowed   bool     `json:"allowed"`
	Selecting []string `json:"selecting"`
	AllowedBy []string `json:"allowedBy"`
	// Rules are the admitting rules, in words, by policy.
	Rules map[string]string `json:"rules,omitempty"`
}

// Ingress decides the destination's side: policies in its namespace that
// select it for ingress, and whether one of their rules admits src on the
// port.
func Ingress(policies []*networkingv1.NetworkPolicy, src, dst Endpoint, port Port) Side {
	s := Side{Allowed: true, Selecting: []string{}, AllowedBy: []string{}}
	if dst.Pod.Spec.HostNetwork {
		return s
	}
	for _, p := range policies {
		if in, _ := Types(p); !in || !Selects(p, dst.Pod) {
			continue
		}
		s.Isolated = true
		s.Selecting = append(s.Selecting, p.Name)
		for _, r := range p.Spec.Ingress {
			if peersMatch(r.From, p.Namespace, src) && portsMatch(r.Ports, port, dst.Pod) {
				s.AllowedBy = append(s.AllowedBy, p.Name)
				if s.Rules == nil {
					s.Rules = map[string]string{}
				}
				s.Rules[p.Name] = ruleText("from", r.From, r.Ports, p.Namespace)
				break
			}
		}
	}
	if s.Isolated {
		s.Allowed = len(s.AllowedBy) > 0
	}
	sort.Strings(s.Selecting)
	sort.Strings(s.AllowedBy)
	return s
}

// Egress decides the source's side: policies in its namespace that select
// it for egress, and whether one of their rules admits dst on the port.
func Egress(policies []*networkingv1.NetworkPolicy, src, dst Endpoint, port Port) Side {
	s := Side{Allowed: true, Selecting: []string{}, AllowedBy: []string{}}
	if src.Pod.Spec.HostNetwork {
		return s
	}
	for _, p := range policies {
		if _, eg := Types(p); !eg || !Selects(p, src.Pod) {
			continue
		}
		s.Isolated = true
		s.Selecting = append(s.Selecting, p.Name)
		for _, r := range p.Spec.Egress {
			if peersMatch(r.To, p.Namespace, dst) && portsMatch(r.Ports, port, dst.Pod) {
				s.AllowedBy = append(s.AllowedBy, p.Name)
				if s.Rules == nil {
					s.Rules = map[string]string{}
				}
				s.Rules[p.Name] = ruleText("to", r.To, r.Ports, p.Namespace)
				break
			}
		}
	}
	if s.Isolated {
		s.Allowed = len(s.AllowedBy) > 0
	}
	sort.Strings(s.Selecting)
	sort.Strings(s.AllowedBy)
	return s
}

// ---------- words ----------

// SelectorText is "app=web, tier in (a, b)", or "every pod" for an empty
// selector.
func SelectorText(s *metav1.LabelSelector) string {
	if s == nil || (len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0) {
		return "every pod"
	}
	var parts []string
	keys := make([]string, 0, len(s.MatchLabels))
	for k := range s.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+s.MatchLabels[k])
	}
	for _, e := range s.MatchExpressions {
		switch e.Operator {
		case metav1.LabelSelectorOpIn:
			parts = append(parts, fmt.Sprintf("%s in (%s)", e.Key, strings.Join(e.Values, ", ")))
		case metav1.LabelSelectorOpNotIn:
			parts = append(parts, fmt.Sprintf("%s notin (%s)", e.Key, strings.Join(e.Values, ", ")))
		case metav1.LabelSelectorOpExists:
			parts = append(parts, e.Key)
		case metav1.LabelSelectorOpDoesNotExist:
			parts = append(parts, "!"+e.Key)
		}
	}
	return strings.Join(parts, ", ")
}

func peerText(p networkingv1.NetworkPolicyPeer, ns string) string {
	if p.IPBlock != nil {
		if len(p.IPBlock.Except) > 0 {
			return p.IPBlock.CIDR + " except " + strings.Join(p.IPBlock.Except, ", ")
		}
		return p.IPBlock.CIDR
	}
	where := "in " + ns
	if p.NamespaceSelector != nil {
		ml := p.NamespaceSelector.MatchLabels
		switch {
		case len(ml) == 1 && ml["kubernetes.io/metadata.name"] != "" && len(p.NamespaceSelector.MatchExpressions) == 0:
			where = "in namespace " + ml["kubernetes.io/metadata.name"]
		case len(ml) == 0 && len(p.NamespaceSelector.MatchExpressions) == 0:
			where = "in every namespace"
		default:
			where = "in namespaces with " + SelectorText(p.NamespaceSelector)
		}
	}
	if p.PodSelector == nil || (len(p.PodSelector.MatchLabels) == 0 && len(p.PodSelector.MatchExpressions) == 0) {
		if p.NamespaceSelector != nil && p.PodSelector == nil {
			return strings.TrimPrefix(where, "in ")
		}
		return "every pod " + where
	}
	return "pods with " + SelectorText(p.PodSelector) + " " + where
}

func portText(ps []networkingv1.NetworkPolicyPort) string {
	if len(ps) == 0 {
		return "any port"
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		proto := "TCP"
		if p.Protocol != nil {
			proto = string(*p.Protocol)
		}
		switch {
		case p.Port == nil:
			out[i] = "any " + proto + " port"
		case p.Port.Type == 1:
			out[i] = proto + " port named " + p.Port.StrVal
		case p.EndPort != nil:
			out[i] = fmt.Sprintf("%s %d–%d", proto, p.Port.IntVal, *p.EndPort)
		default:
			out[i] = fmt.Sprintf("%s %d", proto, p.Port.IntVal)
		}
	}
	return strings.Join(out, ", ")
}

// OrText is "a, b or c".
func OrText(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

func ruleText(dir string, peers []networkingv1.NetworkPolicyPeer, ports []networkingv1.NetworkPolicyPort, ns string) string {
	who := "anywhere"
	if len(peers) > 0 {
		ps := make([]string, len(peers))
		for i, p := range peers {
			ps[i] = peerText(p, ns)
		}
		who = OrText(ps)
	}
	return dir + " " + who + " on " + portText(ports)
}

// AllowsText says what a policy's ingress rules let in.
func AllowsText(p *networkingv1.NetworkPolicy) string {
	if in, _ := Types(p); !in {
		return ""
	}
	if len(p.Spec.Ingress) == 0 {
		return "nothing: it denies every incoming connection that no other policy here allows"
	}
	out := make([]string, len(p.Spec.Ingress))
	for i, r := range p.Spec.Ingress {
		out[i] = ruleText("from", r.From, r.Ports, p.Namespace)
	}
	return strings.Join(out, "; ")
}

// AllowsOutText says where a policy's egress rules let its pods connect.
func AllowsOutText(p *networkingv1.NetworkPolicy) string {
	if _, eg := Types(p); !eg {
		return ""
	}
	if len(p.Spec.Egress) == 0 {
		return "nothing: it denies every outgoing connection that no other policy here allows"
	}
	out := make([]string, len(p.Spec.Egress))
	for i, r := range p.Spec.Egress {
		out[i] = ruleText("to", r.To, r.Ports, p.Namespace)
	}
	return strings.Join(out, "; ")
}

// DefaultDeny says whether a policy selects every pod and admits nothing,
// per direction.
func DefaultDeny(p *networkingv1.NetworkPolicy) (ingress, egress bool) {
	sel := p.Spec.PodSelector
	if len(sel.MatchLabels) != 0 || len(sel.MatchExpressions) != 0 {
		return false, false
	}
	in, eg := Types(p)
	return in && len(p.Spec.Ingress) == 0, eg && len(p.Spec.Egress) == 0
}
