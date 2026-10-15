package views_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/views"
)

func cert(t *testing.T, names []string, from, to time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: from, NotAfter: to}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// netSource is a shop behind ingress-nginx: one controller pod, a TLS
// Secret for shop.example.test, the web Service with two ready pods.
func netSource(t *testing.T) *fake.Source {
	src := fake.New("worker-1")
	src.Put(&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx", Annotations: map[string]string{networkingv1.AnnotationIsDefaultIngressClass: "true"}}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}})
	ctl := pod("ingress", "controller-1", "worker-1")
	ctl.Labels = map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}
	ctl.Status.PodIP = "10.1.0.9"
	src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ingress", Labels: map[string]string{"kubernetes.io/metadata.name": "ingress"}}})
	src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"kubernetes.io/metadata.name": "shop"}}})
	src.Put(ctl)
	sec, err := kube.SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "shop-tls"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": cert(t, []string{"shop.example.test"}, now.Add(-24*time.Hour), now.Add(90*24*time.Hour)), "tls.key": []byte("unused")}})
	if err != nil {
		t.Fatal(err)
	}
	src.Put(sec.(*kube.SecretSummary))
	src.Put(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.20", Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}}}})
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-abc", Labels: map[string]string{discoveryv1.LabelServiceName: "web"}}, Ports: []discoveryv1.EndpointPort{{Name: ptr("http"), Port: ptr(int32(8080))}}}
	for i, n := range []string{"web-1", "web-2"} {
		p := pod("shop", n, "worker-1")
		p.Status.PodIP = "10.1.0." + string(rune('1'+i))
		src.Put(p)
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{Addresses: []string{p.Status.PodIP}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(true)}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: n}})
	}
	src.Put(slice)
	prefix := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "shop"}, Spec: networkingv1.IngressSpec{
		TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.test"}, SecretName: "shop-tls"}},
		Rules: []networkingv1.IngressRule{{Host: "shop.example.test", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
			{Path: "/", PathType: &prefix, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Name: "http"}}}},
		}}}}},
	}}
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.10"}}
	src.Put(ing)
	return src
}

func trace(t *testing.T, src *fake.Source) views.TraceRow {
	t.Helper()
	keys := views.NetworkKeys(src)
	if len(keys) == 0 || keys[0] != "facts:cluster" {
		t.Fatalf("keys %v", keys)
	}
	row, ok := views.BuildNetwork(src, "route:shop/shop/0/0", now)
	if !ok {
		t.Fatalf("no route row in %v", keys)
	}
	return row.(views.TraceRow)
}

func hopOf(t *testing.T, r views.TraceRow, k string) views.Hop {
	t.Helper()
	for _, h := range r.Hops {
		if h.K == k {
			return h
		}
	}
	t.Fatalf("no %s hop in %+v", k, r.Hops)
	return views.Hop{}
}

func TestTraceWorks(t *testing.T) {
	r := trace(t, netSource(t))
	if r.Verdict != "Works" || r.URL != "https://shop.example.test/" || r.Backend != "web:http" {
		t.Fatalf("%s %s %s: %s", r.Verdict, r.URL, r.Backend, r.Text)
	}
	var ks []string
	for _, h := range r.Hops {
		ks = append(ks, h.K+"="+h.Tone)
	}
	want := "Address=ok Ingress controller=ok TLS certificate=ok Ingress rule=ok Service=ok Endpoints=ok Network policy=muted Pods=ok"
	if strings.Join(ks, " ") != want {
		t.Fatalf("hops %s", strings.Join(ks, " "))
	}
	if c := hopOf(t, r, "TLS certificate"); c.S != "90 days left" || c.Link == nil || c.Link.Kind != "secret" {
		t.Fatalf("cert hop %+v", c)
	}
	if r.Policy.Text != "not restricted" {
		t.Fatalf("policy %+v", r.Policy)
	}
}

func TestTraceNamesTheFailingHop(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(t *testing.T, src *fake.Source)
		hop    string
		s      string
		in     string
	}{
		{"no address", func(t *testing.T, src *fake.Source) {
			ing := src.Object("ingresses", "shop", "shop").(*networkingv1.Ingress).DeepCopy()
			ing.Status.LoadBalancer.Ingress = nil
			src.Put(ing)
		}, "Address", "no address", "no controller has admitted it"},
		{"no class", func(t *testing.T, src *fake.Source) {
			src.Delete("ingressclasses", "", "nginx")
		}, "Ingress controller", "no class", "names no class and none is the default"},
		{"controller not ready", func(t *testing.T, src *fake.Source) {
			p := src.Object("pods", "ingress", "controller-1").(*corev1.Pod).DeepCopy()
			p.Status.Conditions[0].Status = corev1.ConditionFalse
			p.Status.ContainerStatuses[0].Ready = false
			src.Put(p)
		}, "Ingress controller", "0 of 1 ready", "No controller pod is ready"},
		{"secret missing", func(t *testing.T, src *fake.Source) {
			src.Delete("secrets", "shop", "shop-tls")
		}, "TLS certificate", "secret missing", "Secret shop/shop-tls doesn’t exist"},
		{"expired", func(t *testing.T, src *fake.Source) {
			sec, _ := kube.SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "shop-tls"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": cert(t, []string{"shop.example.test"}, now.Add(-90*24*time.Hour), now.Add(-time.Hour))}})
			src.Put(sec.(*kube.SecretSummary))
		}, "TLS certificate", "expired", "expired on 2026-10-12"},
		{"wrong host", func(t *testing.T, src *fake.Source) {
			sec, _ := kube.SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "shop-tls"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": cert(t, []string{"*.other.test"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))}})
			src.Put(sec.(*kube.SecretSummary))
		}, "TLS certificate", "doesn’t cover host", "covers *.other.test, not shop.example.test"},
		{"missing service", func(t *testing.T, src *fake.Source) {
			src.Delete("services", "shop", "web")
		}, "Service", "missing", "controller answers 503"},
		{"no such port", func(t *testing.T, src *fake.Source) {
			svc := src.Object("services", "shop", "web").(*corev1.Service).DeepCopy()
			svc.Spec.Ports[0].Name = "web"
			src.Put(svc)
		}, "Service", "no such port", "has no port http; it serves 80 (web)"},
		{"no endpoints", func(t *testing.T, src *fake.Source) {
			src.Delete("endpointslices", "shop", "web-abc")
		}, "Endpoints", "no endpoints", "matches no ready pod in shop"},
		{"denied", func(t *testing.T, src *fake.Source) {
			src.Put(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deny-all"}, Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}})
		}, "Network policy", "blocked", "deny-all selects these pods and none admits ingress/web on TCP 8080"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := netSource(t)
			c.break_(t, src)
			r := trace(t, src)
			h := hopOf(t, r, c.hop)
			if r.Verdict != "Fails" || h.Tone != "err" || h.S != c.s || !strings.Contains(h.D, c.in) {
				t.Fatalf("%s: hop %+v", r.Verdict, h)
			}
			if !strings.HasPrefix(r.Text, "Fails at "+strings.ToLower(c.hop)+": ") {
				t.Fatalf("text %q", r.Text)
			}
		})
	}
}

func TestTraceAllowedByPolicy(t *testing.T) {
	src := netSource(t)
	tcp := corev1.ProtocolTCP
	port := intstr.FromInt32(8080)
	src.Put(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "from-ingress"}, Spec: networkingv1.NetworkPolicySpec{
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "ingress"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}}},
	}})
	r := trace(t, src)
	if r.Verdict != "Works" || r.Policy.Text != "from-ingress" || r.Policy.Tone != "ok" {
		t.Fatalf("%s policy %+v", r.Verdict, r.Policy)
	}
	if h := hopOf(t, r, "Network policy"); h.Link == nil || h.Link.Kind != "netpol" || h.Link.Name != "from-ingress" {
		t.Fatalf("policy hop %+v", h)
	}
}

func TestTraceDegradedWhenSomePodsAreNotReady(t *testing.T) {
	src := netSource(t)
	s := src.Object("endpointslices", "shop", "web-abc").(*discoveryv1.EndpointSlice).DeepCopy()
	s.Endpoints[1].Conditions.Ready = ptr(false)
	src.Put(s)
	p := src.Object("pods", "shop", "web-2").(*corev1.Pod).DeepCopy()
	p.Status.Conditions[0].Status = corev1.ConditionFalse
	p.Status.ContainerStatuses[0].Ready = false
	src.Put(p)
	r := trace(t, src)
	if r.Verdict != "Works, degraded" || r.Endpoints.Text != "1 of 2 ready" || !strings.HasPrefix(r.Text, "Endpoints: 1 of 2 ready") {
		t.Fatalf("%s %+v: %s", r.Verdict, r.Endpoints, r.Text)
	}
}

func TestNetworkFactsAndNodes(t *testing.T) {
	src := netSource(t)
	row, ok := views.BuildNetwork(src, "facts:cluster", now)
	if !ok {
		t.Fatal("no facts")
	}
	f := row.(views.NetFactsRow)
	if len(f.Classes) != 1 || f.Classes[0].Name != "nginx" || !f.Classes[0].Default || f.Classes[0].Ready != 1 || f.Classes[0].Hosts != 1 {
		t.Fatalf("classes %+v", f.Classes)
	}
	if _, ok := views.BuildNetwork(src, "node:worker-1", now); !ok {
		t.Fatal("no node row")
	}
	if _, ok := views.BuildNetwork(src, "route:shop/shop/3/0", now); ok {
		t.Fatal("a rule that doesn't exist has a row")
	}
}
