package sim

import (
	"sort"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The simulated cluster's policies (P4-04 to P4-06): network policies in
// some namespaces, disruption budgets with their status, quotas with what
// they have used, Pod Security labels and the pods that break them,
// admission webhooks and priority classes. The simulator keeps no
// controllers, so statuses are worked out here once.

func (s *Sim) policySeed(now time.Time) {
	day := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }
	sel := func(kv ...string) *metav1.LabelSelector {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return &metav1.LabelSelector{MatchLabels: m}
	}
	tcp := corev1.ProtocolTCP
	port := func(n int32) networkingv1.NetworkPolicyPort {
		p := intstr.FromInt32(n)
		return networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &p}
	}
	nsSel := func(ns string) *metav1.LabelSelector { return sel("kubernetes.io/metadata.name", ns) }

	// The node agent reads the host: it breaks baseline, and runs at the
	// highest priority.
	for _, p := range s.pods {
		if p.Namespace == "kube-system" && strings.HasPrefix(p.Name, "node-agent-") {
			p.Spec.HostNetwork = true
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "proc", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/proc"}}}, corev1.Volume{Name: "sys", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys"}}})
			p.Spec.Containers[0].Ports = []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9100, HostPort: 9100, Protocol: tcp}}
			p.Spec.PriorityClassName = "system-node-critical"
			prio := int32(2000001000)
			p.Spec.Priority = &prio
		}
		if p.Namespace == "monitoring" && strings.HasPrefix(p.Name, "exporter-") {
			priv := true
			p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &priv}
		}
	}

	// Pod Security labels.
	label := func(ns string, kv ...string) {
		n, _ := s.objects["namespaces"][ns].(*corev1.Namespace)
		if n == nil {
			return
		}
		for i := 0; i+1 < len(kv); i += 2 {
			n.Labels["pod-security.kubernetes.io/"+kv[i]] = kv[i+1]
		}
	}
	label("analytics", "enforce", "baseline", "warn", "restricted")
	label("monitoring", "enforce", "privileged", "warn", "baseline")
	label("ingest", "warn", "baseline", "audit", "restricted")

	// The network plugin.
	ds := &appsv1.DaemonSet{TypeMeta: typeMeta("DaemonSet"), ObjectMeta: objectMeta("kube-system", "calico-node", day(214), map[string]string{"k8s-app": "calico-node"}), Spec: appsv1.DaemonSetSpec{Selector: sel("k8s-app", "calico-node")}}
	n := int32(len(s.nodes))
	ds.Status = appsv1.DaemonSetStatus{DesiredNumberScheduled: n, CurrentNumberScheduled: n, NumberReady: n, NumberAvailable: n, UpdatedNumberScheduled: n, ObservedGeneration: 1}
	s.addLocked(ds)

	// Network policies: web allows its tiers to talk, analytics only to
	// itself, batch may reach DNS and nothing else.
	np := func(ns, name string, age int, spec networkingv1.NetworkPolicySpec) {
		s.addLocked(&networkingv1.NetworkPolicy{TypeMeta: typeMeta("NetworkPolicy"), ObjectMeta: objectMeta(ns, name, day(age), nil), Spec: spec})
	}
	ingress := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	np("web", "allow-frontend-from-gateway", 40, networkingv1.NetworkPolicySpec{PodSelector: *sel("app", "frontend"), PolicyTypes: ingress, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: sel("app", "gateway")}}, Ports: []networkingv1.NetworkPolicyPort{port(8080)}}}})
	np("web", "allow-api-from-frontend", 40, networkingv1.NetworkPolicySpec{PodSelector: *sel("app", "api"), PolicyTypes: ingress, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: sel("app", "frontend")}}, Ports: []networkingv1.NetworkPolicyPort{port(8080)}}}})
	np("web", "allow-gateway-from-anywhere", 40, networkingv1.NetworkPolicySpec{PodSelector: *sel("app", "gateway"), PolicyTypes: ingress, Ingress: []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{port(8080)}}}})
	np("web", "allow-metrics-scrape", 40, networkingv1.NetworkPolicySpec{PolicyTypes: ingress, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSel("monitoring"), PodSelector: sel("app", "exporter")}}, Ports: []networkingv1.NetworkPolicyPort{port(9090)}}}})
	np("analytics", "allow-same-namespace", 60, networkingv1.NetworkPolicySpec{PolicyTypes: ingress, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}}})
	udp := corev1.ProtocolUDP
	dns := intstr.FromInt32(53)
	np("batch", "egress-dns-only", 20, networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{
		To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSel("kube-system"), PodSelector: sel("k8s-app", "kube-dns")}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}},
	}}})

	// Disruption budgets, with the status the disruption controller would
	// write: how many of their pods are ready, and how many may go.
	pdb := func(ns, name string, age int, s0 *metav1.LabelSelector, min, max *intstr.IntOrString) {
		b := &policyv1.PodDisruptionBudget{TypeMeta: typeMeta("PodDisruptionBudget"), ObjectMeta: objectMeta(ns, name, day(age), nil), Spec: policyv1.PodDisruptionBudgetSpec{Selector: s0, MinAvailable: min, MaxUnavailable: max}}
		s.addLocked(b)
	}
	one := intstr.FromInt32(1)
	all := intstr.FromString("100%")
	pdb("analytics", "cache", 90, sel("app", "cache"), nil, &one)
	pdb("web", "frontend", 30, sel("app", "frontend"), &all, nil)
	for _, o := range s.objects["poddisruptionbudgets"] {
		s.pdbStatus(o.(*policyv1.PodDisruptionBudget))
	}

	// Quotas: what each has used is what the pods there request.
	gi := func(n string) resource.Quantity { return resource.MustParse(n) }
	quota := func(ns, name string, age int, hard corev1.ResourceList) {
		q := &corev1.ResourceQuota{TypeMeta: typeMeta("ResourceQuota"), ObjectMeta: objectMeta(ns, name, day(age), nil), Spec: corev1.ResourceQuotaSpec{Hard: hard}}
		s.addLocked(q)
	}
	webUsed := s.used("web")
	mem := webUsed["requests.memory"]
	// Room for a little more than what runs: not for a surge pod of the
	// frontend, whose rolling update must add one before it removes one.
	headroom := mem.DeepCopy()
	headroom.Add(gi("200Mi"))
	pods := webUsed["pods"]
	quota("web", "web-quota", 12, corev1.ResourceList{"requests.memory": headroom, "pods": *resource.NewQuantity(pods.Value()+6, resource.DecimalSI), "services.loadbalancers": gi("1")})
	for _, o := range s.objects["resourcequotas"] {
		q := o.(*corev1.ResourceQuota)
		used := s.used(q.Namespace)
		q.Status.Hard = q.Spec.Hard.DeepCopy()
		q.Status.Used = corev1.ResourceList{}
		for k := range q.Spec.Hard {
			if v, ok := used[string(k)]; ok {
				q.Status.Used[k] = v
			} else {
				q.Status.Used[k] = resource.MustParse("0")
			}
		}
	}
	// The Deployments' templates ask for what their pods do, so a rollout
	// can be checked against the quota.
	for _, o := range s.objects["deployments"] {
		d := o.(*appsv1.Deployment)
		for _, p := range s.pods {
			if p.Namespace == d.Namespace && strings.HasPrefix(p.Name, d.Name+"-") {
				d.Spec.Template.Spec.Containers[0].Resources = p.Spec.Containers[0].Resources
				break
			}
		}
	}

	// Admission webhooks: one whose Service has no pods behind it, failing
	// closed, and one that is ignored when it cannot answer.
	fail, ignore := admissionv1.Fail, admissionv1.Ignore
	none := admissionv1.SideEffectClassNone
	ten, five := int32(10), int32(5)
	p443 := int32(443)
	path := "/validate"
	s.addLocked(&corev1.Service{TypeMeta: typeMeta("Service"), ObjectMeta: objectMeta("kube-system", "image-policy", day(33), map[string]string{"app": "image-policy"}), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.40.12", ClusterIPs: []string{"10.96.40.12"}, Selector: map[string]string{"app": "image-policy"}, Ports: []corev1.ServicePort{{Name: "https", Port: 443, TargetPort: intstr.FromInt32(8443), Protocol: tcp}}}})
	s.addLocked(&admissionv1.ValidatingWebhookConfiguration{TypeMeta: metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}, ObjectMeta: objectMeta("", "image-policy", day(33), nil), Webhooks: []admissionv1.ValidatingWebhook{{
		Name: "images.policy.example", ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: "kube-system", Name: "image-policy", Port: &p443, Path: &path}, CABundle: []byte(fakeCert("image-policy"))},
		Rules:         []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}},
		FailurePolicy: &fail, SideEffects: &none, TimeoutSeconds: &ten, AdmissionReviewVersions: []string{"v1"},
		NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"kube-system"}}}},
	}}})
	mpath := "/mutate"
	s.addLocked(&admissionv1.MutatingWebhookConfiguration{TypeMeta: metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}, ObjectMeta: objectMeta("", "metrics-defaults", day(120), map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "metrics-server"}), Webhooks: []admissionv1.MutatingWebhook{{
		Name: "defaults.metrics.example", ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: "kube-system", Name: "metrics-server", Port: &p443, Path: &mpath}, CABundle: []byte(fakeCert("metrics"))},
		Rules:         []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1"}, Resources: []string{"deployments"}}}},
		FailurePolicy: &ignore, SideEffects: &none, TimeoutSeconds: &five, AdmissionReviewVersions: []string{"v1"},
	}}})

	// Priority classes: Kubernetes' two, and one for batch work.
	never := corev1.PreemptNever
	s.addLocked(&schedulingv1.PriorityClass{TypeMeta: metav1.TypeMeta{Kind: "PriorityClass", APIVersion: "scheduling.k8s.io/v1"}, ObjectMeta: objectMeta("", "system-node-critical", day(214), nil), Value: 2000001000, Description: "Used for system critical pods that must not be moved from their current node."})
	s.addLocked(&schedulingv1.PriorityClass{TypeMeta: metav1.TypeMeta{Kind: "PriorityClass", APIVersion: "scheduling.k8s.io/v1"}, ObjectMeta: objectMeta("", "system-cluster-critical", day(214), nil), Value: 2000000000, Description: "Used for system critical pods that must run in the cluster, but can be moved to another node if necessary."})
	s.addLocked(&schedulingv1.PriorityClass{TypeMeta: metav1.TypeMeta{Kind: "PriorityClass", APIVersion: "scheduling.k8s.io/v1"}, ObjectMeta: objectMeta("", "batch-low", day(52), nil), Value: -10, PreemptionPolicy: &never, Description: "Reports and backfills: they wait for room and never preempt."})
	for _, p := range s.pods {
		if p.Namespace == "batch" {
			p.Spec.PriorityClassName = "batch-low"
			v := int32(-10)
			p.Spec.Priority = &v
		}
	}
}

// used is what a namespace's live pods request and limit, and how many
// there are, by quota resource name.
func (s *Sim) used(ns string) map[string]resource.Quantity {
	out := map[string]resource.Quantity{}
	add := func(k string, q resource.Quantity) {
		v := out[k]
		v.Add(q)
		out[k] = v
	}
	keys := make([]string, 0, len(s.pods))
	for k := range s.pods {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := s.pods[k]
		if p.Namespace != ns || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		add("pods", *resource.NewQuantity(1, resource.DecimalSI))
		for _, c := range p.Spec.Containers {
			for r, q := range c.Resources.Requests {
				add("requests."+string(r), q)
			}
			for r, q := range c.Resources.Limits {
				add("limits."+string(r), q)
			}
		}
	}
	if _, ok := out["pods"]; !ok {
		out["pods"] = resource.MustParse("0")
	}
	lbs := int64(0)
	for _, o := range s.objects["services"] {
		if svc, ok := o.(*corev1.Service); ok && svc.Namespace == ns && svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			lbs++
		}
	}
	out["services.loadbalancers"] = *resource.NewQuantity(lbs, resource.DecimalSI)
	return out
}

// pdbStatus works out a budget's status as the disruption controller
// would: its pods, how many are ready, and how many may be disrupted.
func (s *Sim) pdbStatus(b *policyv1.PodDisruptionBudget) {
	sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
	if err != nil {
		return
	}
	var expected, healthy int32
	for _, p := range s.pods {
		if p.Namespace != b.Namespace || !sel.Matches(labels.Set(p.Labels)) || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		expected++
		if podReady(p) {
			healthy++
		}
	}
	var desired int32
	switch {
	case b.Spec.MinAvailable != nil:
		v, _ := intstr.GetScaledValueFromIntOrPercent(b.Spec.MinAvailable, int(expected), true)
		desired = int32(v)
	case b.Spec.MaxUnavailable != nil:
		v, _ := intstr.GetScaledValueFromIntOrPercent(b.Spec.MaxUnavailable, int(expected), true)
		desired = max(0, expected-int32(v))
	}
	b.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: b.Generation, ExpectedPods: expected, CurrentHealthy: healthy, DesiredHealthy: desired, DisruptionsAllowed: max(0, healthy-desired)}
}
