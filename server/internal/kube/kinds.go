package kube

import (
	"fmt"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// Kind is one resource the console watches. Every kind is cached with a
// transform that drops what no screen shows, so the cache stays small
// (X-09); the long tail is watched as metadata only, which the API server
// serves without the objects' bodies.
type Kind struct {
	// Name is the key the rest of the server uses: "pods", "deployments".
	Name string
	// Kind, Group, Version and Resource name the API: Pod, apps, v1, pods.
	Kind     string
	Group    string
	Version  string
	Resource string
	// Plural is how screens label the kind: "Pods", "Deployments".
	Plural string
	// Namespaced is false for cluster-scoped kinds (nodes, namespaces).
	Namespaced bool
	// Metadata watches metadata only: no body ever reaches the server.
	Metadata bool
	// Secret marks kinds that need the chart's Secret grant.
	Secret bool
	// New returns an empty object of the kind, for the informer.
	New func() runtime.Object
	// Transform trims an object before it is cached; nil keeps it whole.
	Transform cache.TransformFunc
	// Indexers index the cache beyond the namespace index every kind has.
	Indexers cache.Indexers
	// Order is the kind's place in lists, as the prototype orders them.
	Order int
}

// GVR is the kind's group, version and resource.
func (k Kind) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
}

// APIVersion is "v1" or "apps/v1".
func (k Kind) APIVersion() string {
	if k.Group == "" {
		return k.Version
	}
	return k.Group + "/" + k.Version
}

// Path is the API path of the kind's collection.
func (k Kind) Path() string {
	if k.Group == "" {
		return "/api/" + k.Version + "/" + k.Resource
	}
	return "/apis/" + k.Group + "/" + k.Version + "/" + k.Resource
}

// Index names.
const (
	// IndexPodNode indexes pods by the node they are bound to.
	IndexPodNode = "byNode"
	// IndexEventObject indexes events by the UID of the object they are about.
	IndexEventObject = "byObject"
	// IndexSliceService indexes endpoint slices by their Service.
	IndexSliceService = "byService"
	// IndexRelease indexes Helm release records by namespace/name.
	IndexRelease = "byRelease"
	// IndexBindingRole indexes role bindings by the role they grant:
	// "ClusterRole/name", or "Role/namespace/name".
	IndexBindingRole = "byRole"
)

func typed[T any, PT interface {
	*T
	runtime.Object
}]() func() runtime.Object {
	return func() runtime.Object { return PT(new(T)) }
}

func metadataOnly(name, kind, group, version, resource, plural string, order int) Kind {
	return Kind{
		Name: name, Kind: kind, Group: group, Version: version, Resource: resource, Plural: plural,
		Namespaced: true, Metadata: true, Order: order,
		New:       func() runtime.Object { return &metav1.PartialObjectMetadata{} },
		Transform: stripMetadata,
	}
}

// Kinds lists every kind the console watches, in the order screens show
// them. Nodes and pods come first because Phase 1 did.
var Kinds = []Kind{
	{Name: "nodes", Kind: "Node", Version: "v1", Resource: "nodes", Plural: "Nodes", New: typed[corev1.Node](), Transform: StripNode, Order: -2},
	{Name: "pods", Kind: "Pod", Version: "v1", Resource: "pods", Plural: "Pods", Namespaced: true, New: typed[corev1.Pod](), Transform: StripPod, Indexers: cache.Indexers{IndexPodNode: podNodeName}, Order: 0},
	{Name: "deployments", Kind: "Deployment", Group: "apps", Version: "v1", Resource: "deployments", Plural: "Deployments", Namespaced: true, New: typed[appsv1.Deployment](), Transform: stripDeployment, Order: 1},
	{Name: "statefulsets", Kind: "StatefulSet", Group: "apps", Version: "v1", Resource: "statefulsets", Plural: "StatefulSets", Namespaced: true, New: typed[appsv1.StatefulSet](), Transform: stripStatefulSet, Order: 2},
	{Name: "daemonsets", Kind: "DaemonSet", Group: "apps", Version: "v1", Resource: "daemonsets", Plural: "DaemonSets", Namespaced: true, New: typed[appsv1.DaemonSet](), Transform: stripDaemonSet, Order: 3},
	{Name: "jobs", Kind: "Job", Group: "batch", Version: "v1", Resource: "jobs", Plural: "Jobs", Namespaced: true, New: typed[batchv1.Job](), Transform: stripJob, Order: 4},
	{Name: "cronjobs", Kind: "CronJob", Group: "batch", Version: "v1", Resource: "cronjobs", Plural: "CronJobs", Namespaced: true, New: typed[batchv1.CronJob](), Transform: stripCronJob, Order: 5},
	{Name: "replicasets", Kind: "ReplicaSet", Group: "apps", Version: "v1", Resource: "replicasets", Plural: "ReplicaSets", Namespaced: true, New: typed[appsv1.ReplicaSet](), Transform: stripReplicaSet, Order: 6},
	metadataOnly("controllerrevisions", "ControllerRevision", "apps", "v1", "controllerrevisions", "ControllerRevisions", 7),
	{Name: "services", Kind: "Service", Version: "v1", Resource: "services", Plural: "Services", Namespaced: true, New: typed[corev1.Service](), Transform: stripService, Order: 8},
	{Name: "endpointslices", Kind: "EndpointSlice", Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices", Plural: "EndpointSlices", Namespaced: true, New: typed[discoveryv1.EndpointSlice](), Transform: stripEndpointSlice, Indexers: cache.Indexers{IndexSliceService: sliceServiceName}, Order: 9},
	metadataOnly("endpoints", "Endpoints", "", "v1", "endpoints", "Endpoints", 10),
	{Name: "ingresses", Kind: "Ingress", Group: "networking.k8s.io", Version: "v1", Resource: "ingresses", Plural: "Ingresses", Namespaced: true, New: typed[networkingv1.Ingress](), Transform: stripIngress, Order: 11},
	{Name: "networkpolicies", Kind: "NetworkPolicy", Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies", Plural: "NetworkPolicies", Namespaced: true, New: typed[networkingv1.NetworkPolicy](), Transform: stripObject, Order: 12},
	{Name: "persistentvolumeclaims", Kind: "PersistentVolumeClaim", Version: "v1", Resource: "persistentvolumeclaims", Plural: "PersistentVolumeClaims", Namespaced: true, New: typed[corev1.PersistentVolumeClaim](), Transform: stripPVC, Order: 13},
	{Name: "configmaps", Kind: "ConfigMap", Version: "v1", Resource: "configmaps", Plural: "ConfigMaps", Namespaced: true, New: typed[corev1.ConfigMap](), Transform: SummarizeConfigMap, Order: 14},
	{Name: "secrets", Kind: "Secret", Version: "v1", Resource: "secrets", Plural: "Secrets", Namespaced: true, Secret: true, New: typed[corev1.Secret](), Transform: SummarizeSecret, Indexers: cache.Indexers{IndexRelease: releaseKey}, Order: 15},
	{Name: "serviceaccounts", Kind: "ServiceAccount", Version: "v1", Resource: "serviceaccounts", Plural: "ServiceAccounts", Namespaced: true, New: typed[corev1.ServiceAccount](), Transform: stripObject, Order: 16},
	{Name: "roles", Kind: "Role", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles", Plural: "Roles", Namespaced: true, New: typed[rbacv1.Role](), Transform: stripObject, Order: 17},
	{Name: "rolebindings", Kind: "RoleBinding", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings", Plural: "RoleBindings", Namespaced: true, New: typed[rbacv1.RoleBinding](), Transform: stripObject, Indexers: cache.Indexers{IndexBindingRole: bindingRole}, Order: 18},
	{Name: "poddisruptionbudgets", Kind: "PodDisruptionBudget", Group: "policy", Version: "v1", Resource: "poddisruptionbudgets", Plural: "PodDisruptionBudgets", Namespaced: true, New: typed[policyv1.PodDisruptionBudget](), Transform: stripObject, Order: 19},
	metadataOnly("horizontalpodautoscalers", "HorizontalPodAutoscaler", "autoscaling", "v2", "horizontalpodautoscalers", "HorizontalPodAutoscalers", 20),
	{Name: "resourcequotas", Kind: "ResourceQuota", Version: "v1", Resource: "resourcequotas", Plural: "ResourceQuotas", Namespaced: true, New: typed[corev1.ResourceQuota](), Transform: stripObject, Order: 21},
	{Name: "limitranges", Kind: "LimitRange", Version: "v1", Resource: "limitranges", Plural: "LimitRanges", Namespaced: true, New: typed[corev1.LimitRange](), Transform: stripObject, Order: 22},
	metadataOnly("leases", "Lease", "coordination.k8s.io", "v1", "leases", "Leases", 23),
	{Name: "events", Kind: "Event", Version: "v1", Resource: "events", Plural: "Events", Namespaced: true, New: typed[corev1.Event](), Transform: stripEvent, Indexers: cache.Indexers{IndexEventObject: eventObjectUID}, Order: 24},
	{Name: "namespaces", Kind: "Namespace", Version: "v1", Resource: "namespaces", Plural: "Namespaces", New: typed[corev1.Namespace](), Transform: stripNamespace, Order: -1},
	// Cluster-scoped kinds the analysis tabs read (Phase 4): who may do
	// what, which policies apply, and how requests reach the cluster.
	{Name: "clusterroles", Kind: "ClusterRole", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles", Plural: "ClusterRoles", New: typed[rbacv1.ClusterRole](), Transform: stripObject, Order: 30},
	{Name: "clusterrolebindings", Kind: "ClusterRoleBinding", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings", Plural: "ClusterRoleBindings", New: typed[rbacv1.ClusterRoleBinding](), Transform: stripObject, Indexers: cache.Indexers{IndexBindingRole: bindingRole}, Order: 31},
	{Name: "priorityclasses", Kind: "PriorityClass", Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses", Plural: "PriorityClasses", New: typed[schedulingv1.PriorityClass](), Transform: stripObject, Order: 32},
	{Name: "validatingwebhookconfigurations", Kind: "ValidatingWebhookConfiguration", Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations", Plural: "ValidatingWebhookConfigurations", New: typed[admissionv1.ValidatingWebhookConfiguration](), Transform: stripWebhooks, Order: 33},
	{Name: "mutatingwebhookconfigurations", Kind: "MutatingWebhookConfiguration", Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations", Plural: "MutatingWebhookConfigurations", New: typed[admissionv1.MutatingWebhookConfiguration](), Transform: stripWebhooks, Order: 34},
	{Name: "ingressclasses", Kind: "IngressClass", Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses", Plural: "IngressClasses", New: typed[networkingv1.IngressClass](), Transform: stripObject, Order: 35},
}

// KindByName finds a kind by its name; ok is false for an unknown name.
func KindByName(name string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Name == name {
			return k, true
		}
	}
	return Kind{}, false
}

// KindOf finds the kind of an object the cache holds.
func KindOf(kind string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return Kind{}, false
}

// NamespacedKinds are the kinds the Namespaces tab counts, in order.
func NamespacedKinds() []Kind {
	var out []Kind
	for _, k := range Kinds {
		if k.Namespaced {
			out = append(out, k)
		}
	}
	return out
}

func podNodeName(obj any) ([]string, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, nil
	}
	return []string{p.Spec.NodeName}, nil
}

func eventObjectUID(obj any) ([]string, error) {
	e, ok := obj.(*corev1.Event)
	if !ok || e.InvolvedObject.UID == "" {
		return nil, nil
	}
	return []string{string(e.InvolvedObject.UID)}, nil
}

func sliceServiceName(obj any) ([]string, error) {
	s, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil, nil
	}
	if svc := s.Labels[discoveryv1.LabelServiceName]; svc != "" {
		return []string{s.Namespace + "/" + svc}, nil
	}
	return nil, nil
}

func releaseKey(obj any) ([]string, error) {
	s, ok := obj.(*SecretSummary)
	if !ok || s.Release == nil {
		return nil, nil
	}
	return []string{s.Namespace + "/" + s.Release.Name}, nil
}

// stripObject keeps a whole object but its managed fields and the
// annotation that copies it, for the kinds the analysis tabs read.
func stripObject(obj any) (any, error) {
	if m, err := meta.Accessor(obj); err == nil {
		m.SetManagedFields(nil)
		m.SetAnnotations(trimAnnotations(m.GetAnnotations()))
	}
	return obj, nil
}

// CABundleSet is what a webhook's CA bundle is cached as: whether one is
// set matters, its certificates do not.
var CABundleSet = []byte{1}

// stripWebhooks is stripObject for webhook configurations, which also
// drops each webhook's CA bundle.
func stripWebhooks(obj any) (any, error) {
	switch w := obj.(type) {
	case *admissionv1.ValidatingWebhookConfiguration:
		for i := range w.Webhooks {
			if len(w.Webhooks[i].ClientConfig.CABundle) > 0 {
				w.Webhooks[i].ClientConfig.CABundle = CABundleSet
			}
		}
	case *admissionv1.MutatingWebhookConfiguration:
		for i := range w.Webhooks {
			if len(w.Webhooks[i].ClientConfig.CABundle) > 0 {
				w.Webhooks[i].ClientConfig.CABundle = CABundleSet
			}
		}
	}
	return stripObject(obj)
}

// bindingRole indexes a binding by its role: "ClusterRole/name", or
// "Role/namespace/name".
func bindingRole(obj any) ([]string, error) {
	switch b := obj.(type) {
	case *rbacv1.ClusterRoleBinding:
		return []string{b.RoleRef.Kind + "/" + b.RoleRef.Name}, nil
	case *rbacv1.RoleBinding:
		if b.RoleRef.Kind == "Role" {
			return []string{"Role/" + b.Namespace + "/" + b.RoleRef.Name}, nil
		}
		return []string{b.RoleRef.Kind + "/" + b.RoleRef.Name}, nil
	}
	return nil, nil
}

// stripMetadata keeps what the Namespaces tab shows of a metadata-only
// object: its name, labels, owners and age.
func stripMetadata(obj any) (any, error) {
	m, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return obj, nil
	}
	m.ManagedFields = nil
	m.Annotations = nil
	return m, nil
}

func stripNamespace(obj any) (any, error) {
	n, ok := obj.(*corev1.Namespace)
	if !ok {
		return obj, nil
	}
	n.ManagedFields = nil
	n.Annotations = trimAnnotations(n.Annotations)
	return n, nil
}

// trimAnnotations drops the one annotation that can be as large as the
// object itself.
func trimAnnotations(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	if _, ok := in["kubectl.kubernetes.io/last-applied-configuration"]; !ok {
		return in
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k != "kubectl.kubernetes.io/last-applied-configuration" {
			out[k] = v
		}
	}
	return out
}

// templateImages keeps a pod template's container names and images, which
// is what the Namespaces tab shows of a workload.
func templateImages(t *corev1.PodTemplateSpec) {
	t.ObjectMeta = metav1.ObjectMeta{Labels: t.Labels}
	keep := func(cs []corev1.Container) []corev1.Container {
		out := make([]corev1.Container, len(cs))
		for i, c := range cs {
			out[i] = corev1.Container{Name: c.Name, Image: c.Image}
		}
		return out
	}
	t.Spec = corev1.PodSpec{Containers: keep(t.Spec.Containers), InitContainers: keep(t.Spec.InitContainers), NodeSelector: t.Spec.NodeSelector}
}

func stripDeployment(obj any) (any, error) {
	d, ok := obj.(*appsv1.Deployment)
	if !ok {
		return obj, nil
	}
	d.ManagedFields = nil
	d.Annotations = trimAnnotations(d.Annotations)
	templateImages(&d.Spec.Template)
	return d, nil
}

func stripStatefulSet(obj any) (any, error) {
	s, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return obj, nil
	}
	s.ManagedFields = nil
	s.Annotations = trimAnnotations(s.Annotations)
	templateImages(&s.Spec.Template)
	s.Spec.VolumeClaimTemplates = nil
	return s, nil
}

func stripDaemonSet(obj any) (any, error) {
	d, ok := obj.(*appsv1.DaemonSet)
	if !ok {
		return obj, nil
	}
	d.ManagedFields = nil
	d.Annotations = trimAnnotations(d.Annotations)
	templateImages(&d.Spec.Template)
	return d, nil
}

func stripReplicaSet(obj any) (any, error) {
	r, ok := obj.(*appsv1.ReplicaSet)
	if !ok {
		return obj, nil
	}
	r.ManagedFields = nil
	r.Annotations = trimAnnotations(r.Annotations)
	templateImages(&r.Spec.Template)
	return r, nil
}

func stripJob(obj any) (any, error) {
	j, ok := obj.(*batchv1.Job)
	if !ok {
		return obj, nil
	}
	j.ManagedFields = nil
	j.Annotations = trimAnnotations(j.Annotations)
	templateImages(&j.Spec.Template)
	return j, nil
}

func stripCronJob(obj any) (any, error) {
	c, ok := obj.(*batchv1.CronJob)
	if !ok {
		return obj, nil
	}
	c.ManagedFields = nil
	c.Annotations = trimAnnotations(c.Annotations)
	templateImages(&c.Spec.JobTemplate.Spec.Template)
	c.Spec.JobTemplate.ObjectMeta = metav1.ObjectMeta{}
	return c, nil
}

func stripService(obj any) (any, error) {
	s, ok := obj.(*corev1.Service)
	if !ok {
		return obj, nil
	}
	s.ManagedFields = nil
	s.Annotations = trimAnnotations(s.Annotations)
	return s, nil
}

func stripEndpointSlice(obj any) (any, error) {
	s, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return obj, nil
	}
	s.ManagedFields = nil
	s.Annotations = nil
	for i := range s.Endpoints {
		s.Endpoints[i].DeprecatedTopology = nil
		s.Endpoints[i].Hints = nil
	}
	return s, nil
}

func stripIngress(obj any) (any, error) {
	i, ok := obj.(*networkingv1.Ingress)
	if !ok {
		return obj, nil
	}
	i.ManagedFields = nil
	i.Annotations = trimAnnotations(i.Annotations)
	return i, nil
}

func stripPVC(obj any) (any, error) {
	p, ok := obj.(*corev1.PersistentVolumeClaim)
	if !ok {
		return obj, nil
	}
	p.ManagedFields = nil
	p.Annotations = trimAnnotations(p.Annotations)
	return p, nil
}

// maxEventMessage bounds a stored event message; the API server allows
// much longer ones.
const maxEventMessage = 2048

func stripEvent(obj any) (any, error) {
	e, ok := obj.(*corev1.Event)
	if !ok {
		return obj, nil
	}
	e.ManagedFields = nil
	e.Annotations = nil
	e.Labels = nil
	if len(e.Message) > maxEventMessage {
		e.Message = e.Message[:maxEventMessage] + "…"
	}
	return e, nil
}

// DescribeObject names an object for messages: "Pod dremio/zk-0".
func DescribeObject(kind string, obj metav1.Object) string {
	if obj.GetNamespace() == "" {
		return fmt.Sprintf("%s %s", kind, obj.GetName())
	}
	return fmt.Sprintf("%s %s/%s", kind, obj.GetNamespace(), obj.GetName())
}
