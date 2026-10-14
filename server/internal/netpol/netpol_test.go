package netpol

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func pod(ns, name, ip string, lbl map[string]string) Endpoint {
	return Endpoint{
		Pod:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbl}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}}, Status: corev1.PodStatus{PodIP: ip, HostIP: "192.168.0.10"}},
		NamespaceLabels: map[string]string{"kubernetes.io/metadata.name": ns, "team": ns},
	}
}

func tcp(n int32) Port { return Port{Number: n, Protocol: corev1.ProtocolTCP} }

func pp(port intstr.IntOrString) networkingv1.NetworkPolicyPort {
	return networkingv1.NetworkPolicyPort{Port: &port}
}

func TestIngressAndEgress(t *testing.T) {
	web := pod("shop", "web", "10.0.0.2", map[string]string{"app": "web"})
	db := pod("shop", "db", "10.0.0.3", map[string]string{"app": "db"})
	other := pod("ops", "probe", "10.0.1.4", map[string]string{"app": "probe"})
	host := pod("kube-system", "proxy", "", map[string]string{"app": "proxy"})
	host.Pod.Spec.HostNetwork = true

	// Nothing selects db: open to all.
	if s := Ingress(nil, web, db, tcp(5432)); s.Isolated || !s.Allowed {
		t.Fatalf("no policy: %+v", s)
	}
	deny := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deny"}, Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}}
	allowWeb := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-from-web"}, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}, {NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "ops"}}}},
			Ports: []networkingv1.NetworkPolicyPort{pp(intstr.FromInt32(5432))},
		}},
	}}
	pols := []*networkingv1.NetworkPolicy{deny, allowWeb}
	if s := Ingress(pols, web, db, tcp(5432)); !s.Isolated || !s.Allowed || strings.Join(s.AllowedBy, ",") != "db-from-web" || strings.Join(s.Selecting, ",") != "db-from-web,deny" {
		t.Fatalf("web→db: %+v", s)
	}
	if s := Ingress(pols, web, db, tcp(5433)); s.Allowed {
		t.Fatalf("other port allowed: %+v", s)
	}
	if s := Ingress(pols, other, db, tcp(5432)); !s.Allowed {
		t.Fatalf("namespace selector: %+v", s)
	}
	if s := Ingress(pols, host, db, tcp(5432)); s.Allowed {
		t.Fatalf("a host-network pod matched a pod selector: %+v", s)
	}
	if s := Ingress(pols, db, web, tcp(8080)); !s.Isolated || s.Allowed {
		t.Fatalf("deny selects web: %+v", s)
	}
	if got := s(AllowsText(allowWeb)); got != "from pods with app=web in shop or namespaces with team=ops on TCP 5432" {
		t.Fatalf("allows text %q", got)
	}

	// Egress with a named port and an ipBlock.
	eg := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "out"}, Spec: networkingv1.NetworkPolicySpec{
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}}}, Ports: []networkingv1.NetworkPolicyPort{pp(intstr.FromString("http"))}},
			{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/24", Except: []string{"10.0.0.3/32"}}}}},
		},
	}}
	if s := Egress([]*networkingv1.NetworkPolicy{eg}, other, web, tcp(8080)); !s.Allowed {
		t.Fatalf("named port: %+v", s)
	}
	if s := Egress([]*networkingv1.NetworkPolicy{eg}, other, db, tcp(5432)); s.Allowed {
		t.Fatalf("except: %+v", s)
	}
	if s := Egress([]*networkingv1.NetworkPolicy{eg}, other, web, tcp(9999)); !s.Allowed {
		t.Fatalf("ipBlock any port: %+v", s)
	}
	if in, out := Types(eg); in || !out {
		t.Fatal("types")
	}
	if in, out := DefaultDeny(deny); !in || out {
		t.Fatal("default deny")
	}
}

func s(x string) string { return x }
