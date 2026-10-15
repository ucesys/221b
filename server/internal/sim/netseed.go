package sim

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The simulated cluster's ingress (P4-02): an IngressClass with an
// ingress-nginx controller on three workers, a real certificate for the
// shop, a policy that lets the controller reach the frontend, and an
// Ingress whose Service does not exist.
func (s *Sim) networkSeed(now time.Time) {
	day := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }
	s.addLocked(&networkingv1.IngressClass{TypeMeta: metav1.TypeMeta{Kind: "IngressClass", APIVersion: "networking.k8s.io/v1"}, ObjectMeta: metav1.ObjectMeta{Name: "nginx", UID: uid("ingressclass/nginx"), CreationTimestamp: metav1.NewTime(day(200)), Annotations: map[string]string{networkingv1.AnnotationIsDefaultIngressClass: "true"}, Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "ingress-nginx"}}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}})

	labels := map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller", "app.kubernetes.io/instance": "ingress-nginx"}
	ds := &appsv1.DaemonSet{TypeMeta: typeMeta("DaemonSet"), ObjectMeta: objectMeta("kube-system", "ingress-nginx-controller", day(200), labels), Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}}}
	ctrl := true
	n := int32(0)
	for _, name := range s.sortedNodeNames() {
		node := s.nodes[name]
		if _, cp := node.Labels["node-role.kubernetes.io/control-plane"]; cp || n == 3 || !isReady(node) {
			continue
		}
		p := s.newPod("kube-system", "ingress-nginx-controller-"+suffix(s.rnd, 5), name, 100, 90, now)
		if p.Labels == nil {
			p.Labels = map[string]string{}
		}
		for k, v := range labels {
			p.Labels[k] = v
		}
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: ds.Name, UID: ds.UID, Controller: &ctrl}}
		p.Spec.Containers[0].Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: 80, HostPort: 80, Protocol: corev1.ProtocolTCP}, {Name: "https", ContainerPort: 443, HostPort: 443, Protocol: corev1.ProtocolTCP}}
		probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(10254)}}}
		p.Spec.Containers[0].LivenessProbe, p.Spec.Containers[0].ReadinessProbe = probe, probe
		p.Status.PodIP = podIPOn(node, 250)
		p.Status.PodIPs = []corev1.PodIP{{IP: p.Status.PodIP}}
		p.Status.HostIP = address(node, corev1.NodeInternalIP)
		s.pods[p.Namespace+"/"+p.Name] = p
		n++
	}
	s.dnsSeed(day(214), now)
	ds.Status = appsv1.DaemonSetStatus{DesiredNumberScheduled: n, CurrentNumberScheduled: n, NumberReady: n, NumberAvailable: n, UpdatedNumberScheduled: n, ObservedGeneration: 1}
	s.addLocked(ds)

	// A real certificate for the shop's hosts, so its expiry and names can
	// be read; the key stays a placeholder.
	if sec, ok := s.objects["secrets"]["web/shop-tls"].(*corev1.Secret); ok {
		sec.Data["tls.crt"] = selfSigned([]string{"shop.example.test", "api.shop.example.test"}, now.Add(-30*24*time.Hour), now.Add(60*24*time.Hour))
		s.objects["secrets"]["web/shop-tls"] = sec
	}
	// The controller may reach the frontend; the api allows only the
	// frontend, so api.shop.example.test is blocked.
	tcp := corev1.ProtocolTCP
	p8080 := intstr.FromInt32(appPort("frontend"))
	s.addLocked(&networkingv1.NetworkPolicy{TypeMeta: typeMeta("NetworkPolicy"), ObjectMeta: objectMeta("web", "allow-frontend-from-ingress", day(40), nil), Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "frontend"}},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "ingress-nginx"}},
		}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &p8080}}}},
	}})
	// Dashboards whose Service was never created.
	path := networkingv1.PathTypePrefix
	cls := "nginx"
	ing := &networkingv1.Ingress{TypeMeta: typeMeta("Ingress"), ObjectMeta: objectMeta("analytics", "dashboards", day(9), nil), Spec: networkingv1.IngressSpec{
		IngressClassName: &cls,
		Rules:            []networkingv1.IngressRule{{Host: "dashboards.example.test", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &path, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "dashboards", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}}},
	}}
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.20"}}
	s.addLocked(ing)
}

// dnsSeed runs CoreDNS behind kube-system/kube-dns: two replicas on the
// first two ready control-plane nodes, with the Service's endpoints.
func (s *Sim) dnsSeed(created, now time.Time) {
	ctrl := true
	n := int32(2)
	labels := map[string]string{"k8s-app": "kube-dns"}
	dep := &appsv1.Deployment{TypeMeta: typeMeta("Deployment"), ObjectMeta: objectMeta("kube-system", "coredns", created, labels), Spec: appsv1.DeploymentSpec{Replicas: &n, Selector: &metav1.LabelSelector{MatchLabels: labels}}}
	dep.Status = appsv1.DeploymentStatus{Replicas: n, ReadyReplicas: n, AvailableReplicas: n, UpdatedReplicas: n, ObservedGeneration: 1}
	dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"}}
	s.addLocked(dep)
	rsLabels := map[string]string{"k8s-app": "kube-dns", "pod-template-hash": "7c65d6cfc9"}
	rs := &appsv1.ReplicaSet{TypeMeta: typeMeta("ReplicaSet"), ObjectMeta: objectMeta("kube-system", "coredns-7c65d6cfc9", created, rsLabels), Spec: appsv1.ReplicaSetSpec{Replicas: &n, Selector: &metav1.LabelSelector{MatchLabels: rsLabels}}}
	rs.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
	rs.Status = appsv1.ReplicaSetStatus{Replicas: n, ReadyReplicas: n, AvailableReplicas: n, FullyLabeledReplicas: n, ObservedGeneration: 1}
	s.addLocked(rs)

	svc, _ := s.objects["services"]["kube-system/kube-dns"].(*corev1.Service)
	if svc == nil {
		return
	}
	udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
	dns, dnsTCP, p53 := "dns", "dns-tcp", int32(53)
	slice := &discoveryv1.EndpointSlice{TypeMeta: typeMeta("EndpointSlice"), ObjectMeta: objectMeta("kube-system", "kube-dns-"+suffix(s.rnd, 5), created, map[string]string{"kubernetes.io/service-name": "kube-dns", "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"}), AddressType: discoveryv1.AddressTypeIPv4}
	slice.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: svc.Name, UID: svc.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
	slice.Ports = []discoveryv1.EndpointPort{{Name: &dns, Port: &p53, Protocol: &udp}, {Name: &dnsTCP, Port: &p53, Protocol: &tcp}}
	placed := int32(0)
	for _, name := range s.sortedNodeNames() {
		node := s.nodes[name]
		if _, cp := node.Labels["node-role.kubernetes.io/control-plane"]; !cp || placed == n || !isReady(node) {
			continue
		}
		p := s.newPod("kube-system", "coredns-7c65d6cfc9-"+suffix(s.rnd, 5), name, 100, 70, now)
		if p.Labels == nil {
			p.Labels = map[string]string{}
		}
		for k, v := range rsLabels {
			p.Labels[k] = v
		}
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
		p.Spec.Containers[0].Name = "coredns"
		p.Spec.Containers[0].Ports = []corev1.ContainerPort{{Name: dns, ContainerPort: 53, Protocol: udp}, {Name: dnsTCP, ContainerPort: 53, Protocol: tcp}}
		probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(8181)}}}
		p.Spec.Containers[0].LivenessProbe, p.Spec.Containers[0].ReadinessProbe = probe, probe
		p.Spec.PriorityClassName = "system-cluster-critical"
		p.Status.PodIP = podIPOn(node, 240)
		p.Status.PodIPs = []corev1.PodIP{{IP: p.Status.PodIP}}
		p.Status.HostIP = address(node, corev1.NodeInternalIP)
		s.pods[p.Namespace+"/"+p.Name] = p
		ready, nodeName := podReady(p), name
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{p.Status.PodIP},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready, Serving: &ready, Terminating: ptrBool(false)},
			NodeName:   &nodeName,
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, UID: p.UID},
		})
		placed++
	}
	s.addLocked(slice)
}

// podIPOn is an address in the node's pod range.
func podIPOn(n *corev1.Node, host int) string {
	return fmt.Sprintf("%s.%d", strings.TrimSuffix(n.Spec.PodCIDR, ".0/24"), host)
}

// selfSigned makes a PEM certificate for the names, valid between the times.
func selfSigned(names []string, from, to time.Time) []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, Issuer: pkix.Name{CommonName: "simulated-ca"}, DNSNames: names, NotBefore: from, NotAfter: to, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
