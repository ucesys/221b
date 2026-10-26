package views_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/views"
)

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func ts(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }

func ptr[T any](v T) *T { return &v }

// pod makes a running pod on a node, owned by a ReplicaSet of a Deployment.
func pod(ns, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: types.UID("uid-" + name), CreationTimestamp: ts(3 * time.Hour),
			Labels:          map[string]string{"app": "web", "app.kubernetes.io/instance": "shop", "app.kubernetes.io/managed-by": "Helm"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: ptr(true)}},
		},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{Name: "main", Image: "registry.example/web:1.0", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
			}}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: "10.1.0.5", StartTime: ptr(ts(2 * time.Hour)), QOSClass: corev1.PodQOSBurstable,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: ts(time.Hour)}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ts(2 * time.Hour)}}}},
		},
	}
}

func source() *fake.Source {
	src := fake.New("worker-1")
	src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", CreationTimestamp: ts(48 * time.Hour)}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}})
	src.Put(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-deploy"}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(2)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "registry.example/web:1.0"}}}}}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, Replicas: 2}})
	src.Put(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", Controller: ptr(true)}}}})
	src.Put(pod("shop", "web-abc-1", "worker-1"))
	crashing := pod("shop", "web-abc-2", "worker-1")
	crashing.Status.Conditions[0].Status = corev1.ConditionFalse
	crashing.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "main", Ready: false, RestartCount: 6,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: ts(40 * time.Second)}},
	}}
	src.Put(crashing)
	pending := pod("shop", "web-abc-3", "")
	pending.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes are available: 3 Insufficient cpu."}}}
	src.Put(pending)
	done := pod("shop", "loader-xyz", "worker-1")
	done.OwnerReferences = nil
	done.Labels = map[string]string{"job-name": "loader"}
	done.Status = corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}}
	src.Put(done)
	src.Put(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-svc-web", CreationTimestamp: ts(10 * time.Minute), Labels: map[string]string{"app.kubernetes.io/instance": "shop", "app.kubernetes.io/managed-by": "Helm"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, ClusterIP: "10.96.0.10", Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP, NodePort: 30080}, {Name: "https", Port: 443, Protocol: corev1.ProtocolTCP, NodePort: 30443}}},
	})
	src.Put(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", Labels: map[string]string{discoveryv1.LabelServiceName: "web"}},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.1.0.5"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(true)}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "web-abc-1", Namespace: "shop"}, NodeName: ptr("worker-1")},
			{Addresses: []string{"10.1.0.6"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(false)}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "web-abc-2", Namespace: "shop"}, NodeName: ptr("worker-1")},
		},
	})
	src.Put(&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "web.1", Namespace: "shop"}, InvolvedObject: corev1.ObjectReference{Kind: "Service", Name: "web", UID: "uid-svc-web"}, Type: corev1.EventTypeWarning, Reason: "SyncLoadBalancerFailed", Message: "no load balancer controller", Count: 3, LastTimestamp: ts(time.Minute)})
	src.Put(&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "shop"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web"}}}}}}}}}})
	src.Put(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: corev1.ClusterIPNone, Selector: map[string]string{"app": "cache"}, Ports: []corev1.ServicePort{{Port: 6379, Protocol: corev1.ProtocolTCP}}}})
	src.Put(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "web-config", Namespace: "shop"}, Data: map[string]string{"config.yaml": "listen: 8080", "mode": "prod"}})
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "api-credentials", Namespace: "shop"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"token": []byte("PLANTED-TOKEN-VALUE"), "user": []byte("svc")}})
	// A release whose chart puts no Helm labels on its pods: the pods are
	// found through the StatefulSet its manifest creates.
	src.Put(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "shop", UID: "uid-sts"}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(1)), ServiceName: "cache"}, Status: appsv1.StatefulSetStatus{ReadyReplicas: 1, CurrentReplicas: 1}})
	cache0 := pod("shop", "cache-0", "worker-1")
	cache0.Labels = map[string]string{"app": "cache"}
	cache0.OwnerReferences = []metav1.OwnerReference{{Kind: "StatefulSet", Name: "cache", Controller: ptr(true)}}
	src.Put(cache0)
	cacheRec := kube.HelmRecord{Name: "cache", Namespace: "shop", Version: 1, Config: map[string]any{"replicas": 1}}
	cacheRec.Info.Status, cacheRec.Info.FirstDeployed, cacheRec.Info.LastDeployed = "deployed", "2026-10-01T08:00:00Z", "2026-10-01T08:00:00Z"
	cacheRec.Chart.Metadata = kube.HelmChartMetadata{Name: "redis", Version: "19.6.4", AppVersion: "7.4.0"}
	cacheRec.Manifest = "---\napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: cache\n  namespace: shop\n"
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.cache.v1", Namespace: "shop", Labels: map[string]string{"owner": "helm", "name": "cache", "version": "1", "status": "deployed"}}, Type: kube.HelmSecretType, Data: map[string][]byte{"release": kube.EncodeHelmRelease(cacheRec)}})
	for rev, status := range map[int]string{1: "superseded", 2: "failed", 3: "deployed"} {
		rec := kube.HelmRecord{Name: "shop", Namespace: "shop", Version: rev, Config: map[string]any{"replicas": 2, "db": map[string]any{"password": "PLANTED-PASSWORD", "host": "db.shop"}}}
		rec.Info.Status, rec.Info.Description, rec.Info.Notes = status, fmt.Sprintf("Upgrade complete (revision %d)", rev), "Thank you for installing shop."
		rec.Info.FirstDeployed, rec.Info.LastDeployed = "2026-10-01T08:00:00Z", fmt.Sprintf("2026-10-0%dT08:00:00Z", rev)
		rec.Chart.Metadata = kube.HelmChartMetadata{Name: "shop-frontend", Version: fmt.Sprintf("3.%d.0", rev), AppVersion: "2.1"}
		rec.Manifest = "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: web\n"
		src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sh.helm.release.v1.shop.v%d", rev), Namespace: "shop", Labels: map[string]string{"owner": "helm", "name": "shop", "version": fmt.Sprint(rev), "status": status}}, Type: kube.HelmSecretType, Data: map[string][]byte{"release": kube.EncodeHelmRelease(rec)}})
	}
	return src
}

func objectOf[T any](t *testing.T, src *fake.Source, kind, ns, name string) T {
	t.Helper()
	o, ok := src.Object(kind, ns, name).(T)
	if !ok {
		t.Fatalf("%s %s/%s is a %T", kind, ns, name, src.Object(kind, ns, name))
	}
	return o
}

func TestPodRowsShowStatusAsKubectlDoes(t *testing.T) {
	src := source()
	running := views.BuildPodRow(src, objectOf[*corev1.Pod](t, src, "pods", "shop", "web-abc-1"), now)
	if running.Status != "Running" || running.Tone != "ok" || running.Ready != 1 || running.Total != 1 || running.Node != "worker-1" {
		t.Errorf("running pod: %+v", running)
	}
	if running.CPURequest != 250 || running.CPULimit != 1000 || running.MemoryRequest != 512<<20 || running.MemoryLimit != 1<<30 || running.QoS != "Burstable" {
		t.Errorf("resources: %+v", running)
	}
	// Owned through the ReplicaSet by the Deployment, and part of the release.
	if running.OwnerKind != "Deployment" || running.OwnerName != "web" || running.Release != "shop" {
		t.Errorf("owner and release: %+v", running)
	}
	crashing := views.BuildPodRow(src, objectOf[*corev1.Pod](t, src, "pods", "shop", "web-abc-2"), now)
	if crashing.Status != "CrashLoopBackOff" || crashing.Tone != "err" || crashing.Restarts != 6 || crashing.LastTermination != "Error, exit 1" || crashing.LastRestart == nil {
		t.Errorf("crash-looping pod: %+v", crashing)
	}
	if !strings.Contains(crashing.StatusTip, "back-off") {
		t.Errorf("tip: %q", crashing.StatusTip)
	}
	pending := views.BuildPodRow(src, objectOf[*corev1.Pod](t, src, "pods", "shop", "web-abc-3"), now)
	if pending.Status != "Pending" || pending.Tone != "warn" || pending.Node != "" || !strings.Contains(pending.StatusTip, "Insufficient cpu") {
		t.Errorf("pending pod: %+v", pending)
	}
	done := views.BuildPodRow(src, objectOf[*corev1.Pod](t, src, "pods", "shop", "loader-xyz"), now)
	if done.Status != "Completed" || done.Tone != "muted" || !done.Done || done.Release != "" {
		t.Errorf("finished pod: %+v", done)
	}
}

func TestPodsOnANodeThatStoppedReportingAreUnknown(t *testing.T) {
	src := source()
	lost := pod("shop", "agent-1", "worker-2")
	lost.Status.Conditions[0].Status = corev1.ConditionFalse
	src.Put(lost)
	src.Put(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-2"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, Reason: "NodeStatusUnknown", LastTransitionTime: ts(time.Hour)}}}})
	r := views.BuildPodRow(src, lost, now)
	if r.Status != "Unknown" || r.Tone != "err" || !r.ReadyUnknown || !strings.Contains(r.StatusTip, "worker-2 stopped reporting") {
		t.Errorf("pod on a lost node: %+v", r)
	}
	// A node that reports itself not ready is still reporting.
	src.Put(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-2"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}})
	if r := views.BuildPodRow(src, lost, now); r.Status != "Running" || r.ReadyUnknown {
		t.Errorf("pod on a not-ready node: %+v", r)
	}
}

func TestPodDetailsNameSecretsWithoutTheirValues(t *testing.T) {
	src := source()
	p := objectOf[*corev1.Pod](t, src, "pods", "shop", "web-abc-1")
	p.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "MODE", Value: "prod"},
		{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-credentials"}, Key: "token"}}},
	}
	p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-credentials"}}}}
	d := views.BuildPodDetails(src, p, now)
	if len(d.Containers) != 1 || len(d.Containers[0].Env) != 2 {
		t.Fatalf("containers: %+v", d.Containers)
	}
	token := d.Containers[0].Env[1]
	if token.Source != "Secret" || token.From != "api-credentials" || token.Key != "token" || token.Value != "" {
		t.Errorf("a Secret-backed variable shows the reference only: %+v", token)
	}
	if len(d.Containers[0].EnvFrom) != 1 || d.Containers[0].EnvFrom[0].Source != "Secret" {
		t.Errorf("envFrom: %+v", d.Containers[0].EnvFrom)
	}
	if len(d.Owners) != 2 || d.Owners[0].Kind != "ReplicaSet" || d.Owners[1].Kind != "Deployment" {
		t.Errorf("owner chain: %+v", d.Owners)
	}
	if len(d.Services) != 1 || d.Services[0] != "web" {
		t.Errorf("services selecting the pod: %v", d.Services)
	}
	if text := fmt.Sprintf("%+v", d); strings.Contains(text, "PLANTED") {
		t.Errorf("a Secret value reached the pod details: %s", text)
	}
}

func TestServiceRowsCarryEndpointsAndTheExternalColumns(t *testing.T) {
	src := source()
	web := views.BuildServiceRow(src, objectOf[*corev1.Service](t, src, "services", "shop", "web"), now)
	if web.Type != "LoadBalancer" || web.ClusterIP != "10.96.0.10" || web.Headless {
		t.Errorf("type and IP: %+v", web)
	}
	if got := strings.Join(web.Ports, ", "); got != "http 80/TCP, https 443/TCP" {
		t.Errorf("ports: %q", got)
	}
	if web.Ready != 1 || web.Total != 2 || web.Status != "1 of 2 ready" || web.Tone != "warn" {
		t.Errorf("endpoints: %+v", web)
	}
	// No address yet: pending, with the reason from the Service's events.
	if !web.Pending || len(web.ExternalIPs) != 0 || !strings.HasPrefix(web.PendingWhy, "SyncLoadBalancerFailed: no load balancer controller") {
		t.Errorf("pending: %+v", web)
	}
	if len(web.NodePorts) != 2 || web.NodePorts[0] != 30080 || len(web.Hostnames) != 1 || web.Hostnames[0] != "shop.example.com" || web.Release != "shop" {
		t.Errorf("external columns: %+v", web)
	}
	// With an address, it is no longer pending.
	src.Update("services", "shop", "web", func(o runtime.Object) {
		o.(*corev1.Service).Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}
	})
	web = views.BuildServiceRow(src, objectOf[*corev1.Service](t, src, "services", "shop", "web"), now)
	if web.Pending || len(web.ExternalIPs) != 1 || web.ExternalIPs[0] != "203.0.113.10" {
		t.Errorf("with an address: %+v", web)
	}
	cache := views.BuildServiceRow(src, objectOf[*corev1.Service](t, src, "services", "shop", "cache"), now)
	if !cache.Headless || cache.ClusterIP != "" || cache.Status != "no endpoints" || cache.Tone != "warn" {
		t.Errorf("headless: %+v", cache)
	}
	d := views.BuildServiceDetails(src, objectOf[*corev1.Service](t, src, "services", "shop", "web"), now)
	if d.DNSName != "web.shop.svc.cluster.local" || len(d.Endpoints) != 2 || d.Endpoints[0].Pod != "web-abc-1" || d.Endpoints[0].Node != "worker-1" || !d.Endpoints[0].Ready || d.Endpoints[1].Ready {
		t.Errorf("details: %+v", d)
	}
	if pods := views.PodsBehind(src, objectOf[*corev1.Service](t, src, "services", "shop", "web")); len(pods) != 2 {
		t.Errorf("pods behind the Service: %v", pods)
	}
}

func TestNamespaceRowsCountEveryKindAndListObjectsWithoutSecretValues(t *testing.T) {
	src := source()
	row := views.BuildNamespaceRow(src, objectOf[*corev1.Namespace](t, src, "namespaces", "", "shop"), now)
	want := map[string]int{"pods": 5, "deployments": 1, "statefulsets": 1, "replicasets": 1, "services": 2, "endpointslices": 1, "events": 1, "ingresses": 1, "configmaps": 1, "secrets": 5}
	for kind, n := range want {
		if row.Counts[kind] != n {
			t.Errorf("%s: %d, want %d", kind, row.Counts[kind], n)
		}
	}
	if row.Objects != 19 || row.Kinds != len(want) || row.Pods != 4 || row.PodsNotRunning != 2 || row.PodsFailing != 1 {
		t.Errorf("row: %+v", row)
	}
	if len(row.Releases) != 2 || row.Releases[0] != "cache" || row.Releases[1] != "shop" || row.Group != "apps" {
		t.Errorf("releases and group: %+v", row)
	}
	rows := views.BuildObjectRows(src, "shop", now)
	byKey := map[string]views.ObjectRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if len(rows) != 19 {
		t.Errorf("%d object rows, want 19", len(rows))
	}
	secret := byKey["Secret/api-credentials"]
	if secret.Kind != "Secret" || !strings.Contains(secret.Details, "token (19 B)") || !strings.Contains(secret.Details, "user (3 B)") {
		t.Errorf("Secret row shows keys and sizes: %+v", secret)
	}
	if cm := byKey["ConfigMap/web-config"]; !strings.Contains(cm.Details, "config.yaml") || strings.Contains(cm.Details, "listen") {
		t.Errorf("ConfigMap row shows keys, not values: %+v", cm)
	}
	if dep := byKey["Deployment/web"]; dep.Status != "1/2 ready" || dep.Tone != "warn" {
		t.Errorf("Deployment row: %+v", dep)
	}
	if crash := byKey["Pod/web-abc-2"]; crash.Status != "CrashLoopBackOff" || !strings.Contains(crash.Details, "6 restarts") {
		t.Errorf("crashing pod row: %+v", crash)
	}
	if text := fmt.Sprintf("%+v", rows); strings.Contains(text, "PLANTED") {
		t.Errorf("a Secret value reached the namespace rows: %s", text)
	}
	// Kinds come in the prototype's order: pods before their workloads' owners.
	if rows[0].Kind != "Pod" || rows[len(rows)-1].Order < rows[0].Order {
		t.Errorf("order: first %s, last %s", rows[0].Kind, rows[len(rows)-1].Kind)
	}
}

func TestReleaseRowsMatchHelmListAndHistory(t *testing.T) {
	src := source()
	if keys := views.ReleaseKeys(src); len(keys) != 2 || keys[0] != "shop/cache" || keys[1] != "shop/shop" {
		t.Fatalf("release keys: %v", keys)
	}
	row, ok := views.BuildReleaseRow(src, "shop", "shop", now)
	if !ok || row.Revision != 3 || row.Status != "deployed" || row.Chart != "shop-frontend" || row.ChartVersion != "3.3.0" || row.AppVersion != "2.1" || row.Revisions != 3 {
		t.Errorf("row: %+v", row)
	}
	if row.Pods != 3 || row.PodsReady != 1 || row.PodsFailed != 1 || row.Tone != "ok" {
		t.Errorf("pods of the release: %+v", row)
	}
	d, ok := views.BuildReleaseDetails(src, "shop", "shop", now)
	if !ok {
		t.Fatal("no details")
	}
	if len(d.History) != 3 || d.History[0].Revision != 3 || d.History[1].Status != "failed" || d.History[1].Tone != "err" || d.History[2].Revision != 1 {
		t.Errorf("history, latest first: %+v", d.History)
	}
	if d.Notes != "Thank you for installing shop." {
		t.Errorf("notes: %q", d.Notes)
	}
	// Values as helm get values shows them, with the password masked.
	if !strings.Contains(d.Values, "replicas: 2") || !strings.Contains(d.Values, "host: db.shop") || !strings.Contains(d.Values, "password: "+kube.MaskedValue) {
		t.Errorf("values:\n%s", d.Values)
	}
	if len(d.MaskedValues) != 1 || d.MaskedValues[0] != "db.password" {
		t.Errorf("masked keys: %v", d.MaskedValues)
	}
	if len(d.Resources) != 2 || d.Resources[0].Kind != "Deployment" || d.Resources[0].Status != "1/2 ready" || !d.Resources[0].Watched || d.Resources[1].Kind != "Service" {
		t.Errorf("resources: %+v", d.Resources)
	}
	if len(d.PodRows) != 3 {
		t.Errorf("%d pod rows, want 3", len(d.PodRows))
	}
	if text := fmt.Sprintf("%+v", d); strings.Contains(text, "PLANTED") {
		t.Errorf("the planted password reached the release details: %s", text)
	}
	// Pods without Helm's labels are found through the release's workloads,
	// and name their release.
	cache, ok := views.BuildReleaseRow(src, "shop", "cache", now)
	if !ok || cache.Pods != 1 || cache.PodsReady != 1 {
		t.Errorf("release whose chart labels no pods: %+v", cache)
	}
	if r := views.BuildPodRow(src, objectOf[*corev1.Pod](t, src, "pods", "shop", "cache-0"), now); r.Release != "cache" || r.OwnerKind != "StatefulSet" {
		t.Errorf("pod of that release: %+v", r)
	}
	// Without Secrets there are no releases at all.
	src.SetSecrets(false)
	if keys := views.ReleaseKeys(src); len(keys) != 0 {
		t.Errorf("releases without Secrets: %v", keys)
	}
}

func TestObjectDetailsForEveryKind(t *testing.T) {
	src := source()
	kind := func(name string) kube.Kind {
		k, ok := views.ObjectKindOf(name)
		if !ok {
			t.Fatalf("no kind %s", name)
		}
		return k
	}
	d, ok := views.BuildObjectDetails(src, kind("Deployment"), "shop", "web", now)
	if !ok || !d.Workload || d.Status != "1/2 ready" {
		t.Fatalf("deployment: %+v", d)
	}
	if len(d.Pods) != 3 || d.Pods[0].Name != "web-abc-1" || d.Pods[2].Name != "web-abc-3" {
		t.Errorf("pods of the deployment, through its ReplicaSet: %+v", d.Pods)
	}
	facts := map[string]string{}
	for _, f := range d.Facts {
		facts[f.Label] = f.Value
	}
	if facts["Replicas"] != "2 desired · 1 ready · 0 available" || !strings.Contains(facts["Images"], "registry.example/web:1.0") {
		t.Errorf("facts: %v", facts)
	}
	sts, ok := views.BuildObjectDetails(src, kind("statefulsets"), "shop", "cache", now)
	if !ok || len(sts.Pods) != 1 || sts.Pods[0].Name != "cache-0" {
		t.Errorf("statefulset: %+v", sts)
	}
	// The release comes from the manifest when Helm's labels are missing,
	// and from the labels when they are there.
	if sts.Release != "cache" {
		t.Errorf("statefulset release: %q", sts.Release)
	}
	if d.Release != "shop" {
		t.Errorf("deployment in the shop release's manifest: %q", d.Release)
	}
	if cm, ok := views.BuildObjectDetails(src, kind("ConfigMap"), "shop", "web-config", now); !ok || cm.Release != "" {
		t.Errorf("configmap outside any release: %+v", cm)
	}
	secret, ok := views.BuildObjectDetails(src, kind("Secret"), "shop", "api-credentials", now)
	if !ok || len(secret.Keys) != 2 || secret.Keys[0].Key != "token" || secret.Keys[0].Bytes != 19 || secret.Workload {
		t.Errorf("secret: %+v", secret)
	}
	if text := fmt.Sprintf("%+v", secret); strings.Contains(text, "PLANTED") {
		t.Errorf("a Secret value reached the details: %s", text)
	}
	svc, ok := views.BuildObjectDetails(src, kind("Service"), "shop", "web", now)
	if !ok || len(svc.Events) != 1 || svc.Events[0].Reason != "SyncLoadBalancerFailed" || svc.Release != "shop" {
		t.Errorf("service events and release: %+v %q", svc.Events, svc.Release)
	}
	if _, ok := views.BuildObjectDetails(src, kind("Deployment"), "shop", "missing", now); ok {
		t.Error("a missing object has details")
	}
	if _, ok := views.ObjectKindOf("Gizmo"); ok {
		t.Error("an unknown kind is found")
	}
}
