package sim

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/ucesys/221b/server/internal/kube"
)

// The Phase 2 objects: everything around the pods that the Namespaces,
// Pods, Services and Helm tabs show. Pods are grouped by namespace and app
// into workloads, each workload gets a Service with endpoints, and a few
// namespaces get the long tail of kinds.

// PlantedSecretValue is a Secret value that must never appear in any
// response, log or export (P2-07). The e2e tests search for it.
const PlantedSecretValue = "PLANTED-SECRET-VALUE-9f8e7d6c5b4a"

// PlantedHelmPassword is a credential-looking Helm value, masked in the
// Helm tab (P2-07).
const PlantedHelmPassword = "PLANTED-HELM-PASSWORD-1a2b3c4d"

// systemNamespaces exist in every cluster.
var systemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease", "default"}

func typeMeta(kind string) metav1.TypeMeta {
	k, _ := kube.KindOf(kind)
	return metav1.TypeMeta{Kind: kind, APIVersion: k.APIVersion()}
}

func objectMeta(ns, name string, created time.Time, labels map[string]string) metav1.ObjectMeta {
	key := name
	if ns != "" {
		key = ns + "/" + name
	}
	return metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid(key), CreationTimestamp: metav1.NewTime(created.Truncate(time.Second)), Labels: labels}
}

// group is the pods of one app in one namespace.
type group struct {
	ns, app string
	pods    []*corev1.Pod
}

func (s *Sim) generateObjects(now time.Time) {
	add := func(obj runtime.Object) { s.addLocked(obj) }

	// Pods by namespace and app, in a stable order.
	var keys []string
	for k := range s.pods {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	groups := map[string]*group{}
	var order []string
	podIPs := map[string]int{}
	for _, key := range keys {
		p := s.pods[key]
		app := strings.SplitN(p.Name, "-", 2)[0]
		if p.Namespace == "kube-system" && strings.HasPrefix(p.Name, "node-agent-") {
			app = "node-agent"
		}
		if p.Labels == nil {
			p.Labels = map[string]string{}
		}
		p.Labels["app"] = app
		gk := p.Namespace + "/" + app
		g := groups[gk]
		if g == nil {
			g = &group{ns: p.Namespace, app: app}
			groups[gk] = g
			order = append(order, gk)
		}
		g.pods = append(g.pods, p)
		// A pod IP from its node's CIDR; the host network for node agents.
		if p.Spec.NodeName != "" && p.Status.Phase != corev1.PodPending {
			n := s.nodes[p.Spec.NodeName]
			podIPs[p.Spec.NodeName]++
			if app == "node-agent" {
				p.Spec.HostNetwork = true
				p.Status.PodIP = address(n, corev1.NodeInternalIP)
			} else {
				cidr := strings.TrimSuffix(n.Spec.PodCIDR, ".0/24")
				p.Status.PodIP = fmt.Sprintf("%s.%d", cidr, 2+podIPs[p.Spec.NodeName]%250)
			}
			p.Status.PodIPs = []corev1.PodIP{{IP: p.Status.PodIP}}
			p.Status.HostIP = address(n, corev1.NodeInternalIP)
			p.Status.QOSClass = corev1.PodQOSBurstable
		}
	}

	// Namespaces: the system ones and every one with pods.
	nsSet := map[string]bool{}
	for _, ns := range systemNamespaces {
		nsSet[ns] = true
	}
	for _, g := range groups {
		nsSet[g.ns] = true
	}
	var nsNames []string
	for ns := range nsSet {
		nsNames = append(nsNames, ns)
	}
	sort.Strings(nsNames)
	for i, ns := range nsNames {
		created := now.Add(-time.Duration(60+i) * 24 * time.Hour)
		n := &corev1.Namespace{TypeMeta: typeMeta("Namespace"), ObjectMeta: objectMeta("", ns, created, map[string]string{"kubernetes.io/metadata.name": ns})}
		n.Spec.Finalizers = []corev1.FinalizerName{corev1.FinalizerKubernetes}
		n.Status.Phase = corev1.NamespaceActive
		if ns == "web" {
			n.Labels["pod-security.kubernetes.io/enforce"] = "baseline"
		}
		add(n)
		add(&corev1.ServiceAccount{TypeMeta: typeMeta("ServiceAccount"), ObjectMeta: objectMeta(ns, "default", created, nil)})
		add(&corev1.ConfigMap{TypeMeta: typeMeta("ConfigMap"), ObjectMeta: objectMeta(ns, "kube-root-ca.crt", created, nil), Data: map[string]string{"ca.crt": fakeCert("cluster-ca")}})
	}

	// Workloads, one per group, and their Services.
	for _, gk := range order {
		g := groups[gk]
		s.workload(g, now)
	}

	// The long tail, in the namespaces that have those apps.
	s.extras(groups, now)

	// Who may do what, and what limits it.
	s.rbacSeed(now)
	s.policySeed(now)
	s.networkSeed(now)

	// Helm releases, as Secrets, and the labels Helm puts on their pods.
	s.helmReleases(groups, now)

	// Leases: one per node, as the kubelets keep them.
	for _, name := range s.sortedNodeNames() {
		holder := name
		renew := metav1.NewMicroTime(now.Add(-time.Duration(s.rnd.Intn(9)) * time.Second))
		secs := int32(40)
		add(&coordinationv1.Lease{TypeMeta: typeMeta("Lease"), ObjectMeta: objectMeta("kube-node-lease", name, s.nodes[name].CreationTimestamp.Time, nil), Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &secs, RenewTime: &renew}})
	}

	// Events for what is wrong.
	s.problemEvents(now)
}

// workload creates the controller that owns a group's pods, with the
// labels and owner references the real controllers set, and its Service.
func (s *Sim) workload(g *group, now time.Time) {
	add := func(obj runtime.Object) { s.addLocked(obj) }
	first := g.pods[0]
	created := first.CreationTimestamp.Time.Add(-time.Duration(2+s.rnd.Intn(30)) * 24 * time.Hour)
	image := first.Spec.Containers[0].Image
	labels := map[string]string{"app": g.app}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": g.app}}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: image}}}}
	ready := int32(0)
	for _, p := range g.pods {
		if podReady(p) {
			ready++
		}
	}
	n := int32(len(g.pods))
	var owner metav1.OwnerReference
	ctrl := true
	switch {
	case g.app == "node-agent":
		ds := &appsv1.DaemonSet{TypeMeta: typeMeta("DaemonSet"), ObjectMeta: objectMeta(g.ns, g.app, created, labels), Spec: appsv1.DaemonSetSpec{Selector: selector, Template: template}}
		ds.Status = appsv1.DaemonSetStatus{DesiredNumberScheduled: n, CurrentNumberScheduled: n, NumberReady: ready, NumberAvailable: ready, UpdatedNumberScheduled: n, ObservedGeneration: 1}
		ds.Generation = 1
		add(ds)
		owner = metav1.OwnerReference{APIVersion: "apps/v1", Kind: "DaemonSet", Name: ds.Name, UID: ds.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}
		add(&appsv1.ControllerRevision{TypeMeta: typeMeta("ControllerRevision"), ObjectMeta: objectMeta(g.ns, g.app+"-7d9f6c8b5", created, labels), Revision: 1})
		for _, p := range g.pods {
			p.Labels["controller-revision-hash"] = "7d9f6c8b5"
			p.Labels["pod-template-generation"] = "1"
			p.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
			p.Spec.ServiceAccountName = "node-agent"
		}
		add(&corev1.ServiceAccount{TypeMeta: typeMeta("ServiceAccount"), ObjectMeta: objectMeta(g.ns, "node-agent", created, nil)})
	case g.app == "cache" || g.app == "queue":
		svcName := g.app + "-headless"
		sts := &appsv1.StatefulSet{TypeMeta: typeMeta("StatefulSet"), ObjectMeta: objectMeta(g.ns, g.app, created, labels), Spec: appsv1.StatefulSetSpec{Replicas: &n, Selector: selector, Template: template, ServiceName: svcName}}
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}, Spec: pvcSpec("20Gi")}}
		sts.Status = appsv1.StatefulSetStatus{Replicas: n, ReadyReplicas: ready, AvailableReplicas: ready, CurrentReplicas: n, UpdatedReplicas: n, ObservedGeneration: 1, CurrentRevision: g.app + "-5f8d7c6b4", UpdateRevision: g.app + "-5f8d7c6b4"}
		sts.Generation = 1
		add(sts)
		owner = metav1.OwnerReference{APIVersion: "apps/v1", Kind: "StatefulSet", Name: sts.Name, UID: sts.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}
		add(&appsv1.ControllerRevision{TypeMeta: typeMeta("ControllerRevision"), ObjectMeta: objectMeta(g.ns, g.app+"-5f8d7c6b4", created, labels), Revision: 1})
		for i, p := range g.pods {
			// StatefulSet pods are numbered; rename the generated ones.
			delete(s.pods, p.Namespace+"/"+p.Name)
			p.Name = fmt.Sprintf("%s-%d", g.app, i)
			p.UID = uid(p.Namespace + "/" + p.Name)
			s.pods[p.Namespace+"/"+p.Name] = p
			p.Labels["controller-revision-hash"] = g.app + "-5f8d7c6b4"
			p.Labels["statefulset.kubernetes.io/pod-name"] = p.Name
			p.Spec.Hostname, p.Spec.Subdomain = p.Name, svcName
			claim := "data-" + p.Name
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}})
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: "/var/lib/" + g.app})
			pvc := &corev1.PersistentVolumeClaim{TypeMeta: typeMeta("PersistentVolumeClaim"), ObjectMeta: objectMeta(g.ns, claim, p.CreationTimestamp.Time, map[string]string{"app": g.app}), Spec: pvcSpec("20Gi")}
			pvc.Spec.VolumeName = "pvc-" + string(uid(claim))[:8]
			pvc.Status = corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: pvc.Spec.AccessModes, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}}
			if g.ns == "storage" && i == 0 && p.Status.Phase == corev1.PodPending {
				pvc.Status = corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}
			}
			add(pvc)
		}
		// Headless Service for the stable names.
		add(s.service(g, svcName, corev1.ServiceTypeClusterIP, true, created))
	case g.app == "loader":
		cj := &batchv1.CronJob{TypeMeta: typeMeta("CronJob"), ObjectMeta: objectMeta(g.ns, g.app, created, labels), Spec: batchv1.CronJobSpec{Schedule: "*/30 * * * *", JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: template}}}}
		last := metav1.NewTime(now.Add(-11 * time.Minute))
		cj.Status.LastScheduleTime = &last
		add(cj)
		jobName := fmt.Sprintf("%s-%d", g.app, now.Add(-11*time.Minute).Unix()/60)
		job := &batchv1.Job{TypeMeta: typeMeta("Job"), ObjectMeta: objectMeta(g.ns, jobName, now.Add(-11*time.Minute), map[string]string{"app": g.app, "job-name": jobName}), Spec: batchv1.JobSpec{Template: template, Completions: &n, Parallelism: &n}}
		job.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: &ctrl}}
		start := metav1.NewTime(now.Add(-11 * time.Minute))
		job.Status = batchv1.JobStatus{StartTime: &start, Active: n}
		add(job)
		owner = metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}
		for _, p := range g.pods {
			p.Labels["job-name"] = jobName
			p.Labels["batch.kubernetes.io/job-name"] = jobName
			p.Spec.RestartPolicy = corev1.RestartPolicyNever
		}
	default:
		hash := strings.ReplaceAll(string(uid(g.ns+"/"+g.app+"/template")), "-", "")[:9]
		dep := &appsv1.Deployment{TypeMeta: typeMeta("Deployment"), ObjectMeta: objectMeta(g.ns, g.app, created, labels), Spec: appsv1.DeploymentSpec{Replicas: &n, Selector: selector, Template: template}}
		dep.Annotations = map[string]string{"deployment.kubernetes.io/revision": "3"}
		dep.Generation = 3
		dep.Status = appsv1.DeploymentStatus{Replicas: n, ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: n, ObservedGeneration: 3}
		dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"}}
		if ready < n {
			dep.Status.Conditions[0].Status, dep.Status.Conditions[0].Reason = corev1.ConditionFalse, "MinimumReplicasUnavailable"
		}
		add(dep)
		rsLabels := map[string]string{"app": g.app, "pod-template-hash": hash}
		rs := &appsv1.ReplicaSet{TypeMeta: typeMeta("ReplicaSet"), ObjectMeta: objectMeta(g.ns, g.app+"-"+hash, created.Add(2*24*time.Hour), rsLabels), Spec: appsv1.ReplicaSetSpec{Replicas: &n, Selector: &metav1.LabelSelector{MatchLabels: rsLabels}, Template: template}}
		rs.Annotations = map[string]string{"deployment.kubernetes.io/revision": "3"}
		rs.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
		rs.Status = appsv1.ReplicaSetStatus{Replicas: n, ReadyReplicas: ready, AvailableReplicas: ready, FullyLabeledReplicas: n, ObservedGeneration: 1}
		add(rs)
		// An older, scaled-down ReplicaSet, as a rollout leaves behind.
		oldLabels := map[string]string{"app": g.app, "pod-template-hash": "6b7c8d9e0"}
		zero := int32(0)
		old := &appsv1.ReplicaSet{TypeMeta: typeMeta("ReplicaSet"), ObjectMeta: objectMeta(g.ns, g.app+"-6b7c8d9e0", created, oldLabels), Spec: appsv1.ReplicaSetSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: oldLabels}, Template: template}}
		old.Annotations = map[string]string{"deployment.kubernetes.io/revision": "2"}
		old.OwnerReferences = rs.OwnerReferences
		add(old)
		owner = metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}
		for _, p := range g.pods {
			p.Labels["pod-template-hash"] = hash
		}
	}
	for _, p := range g.pods {
		p.OwnerReferences = []metav1.OwnerReference{owner}
		s.dressPod(p, g)
	}

	// Every app but the agents and the batch jobs has a Service.
	switch g.app {
	case "node-agent", "loader":
		return
	case "cache", "queue":
		return // the headless one above
	case "frontend":
		// A LoadBalancer with its address from the cloud; web's is the one
		// the Ingress fronts.
		svc := s.service(g, g.app, corev1.ServiceTypeLoadBalancer, false, created)
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: fmt.Sprintf("203.0.113.%d", 10+int(uid(g.ns)[0])%40)}}
		if g.ns == "web" {
			svc.Status.LoadBalancer.Ingress[0].IP = "203.0.113.10"
		}
		add(svc)
	case "gateway":
		add(s.service(g, g.app, corev1.ServiceTypeNodePort, false, created))
	default:
		add(s.service(g, g.app, corev1.ServiceTypeClusterIP, false, created))
	}
}

// dressPod gives a generated pod what the pod details show: ports, env
// from ConfigMaps and Secrets, probes, mounts and volumes, a service
// account, and the pod's own scheduling details.
func (s *Sim) dressPod(p *corev1.Pod, g *group) {
	c := &p.Spec.Containers[0]
	port := appPort(g.app)
	c.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: port, Protocol: corev1.ProtocolTCP}}
	if g.app == "exporter" {
		c.Ports[0].Name = "metrics"
	}
	c.Env = []corev1.EnvVar{
		{Name: "APP_ENV", Value: "production"},
		{Name: "LOG_LEVEL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: g.app + "-config"}, Key: "logLevel"}}},
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "CPU_LIMIT", ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{Resource: "limits.cpu"}}},
	}
	if g.app == "api" || g.app == "worker" {
		c.Env = append(c.Env, corev1.EnvVar{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: g.app + "-credentials"}, Key: "password"}}})
		c.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: g.app + "-credentials"}}}}
	}
	c.LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 10, PeriodSeconds: 10}
	c.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(port)}}, PeriodSeconds: 5}
	c.VolumeMounts = append(c.VolumeMounts,
		corev1.VolumeMount{Name: "config", MountPath: "/etc/" + g.app, ReadOnly: true},
		corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"},
		corev1.VolumeMount{Name: "kube-api-access", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true},
	)
	p.Spec.Volumes = append(p.Spec.Volumes,
		corev1.Volume{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: g.app + "-config"}}}},
		corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: "kube-api-access", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}}}}},
	)
	if g.app == "api" {
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: g.app + "-credentials"}}})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "tls", MountPath: "/etc/tls", ReadOnly: true})
	}
	if p.Spec.ServiceAccountName == "" {
		p.Spec.ServiceAccountName = "default"
		if g.app == "api" && g.ns == "default" {
			p.Spec.ServiceAccountName = "api"
		}
	}
	if p.Spec.RestartPolicy == "" {
		p.Spec.RestartPolicy = corev1.RestartPolicyAlways
	}
	p.Spec.DNSPolicy = corev1.DNSClusterFirst
	grace := int64(30)
	p.Spec.TerminationGracePeriodSeconds = &grace
	p.Spec.SchedulerName = "default-scheduler"
	prio := int32(0)
	p.Spec.Priority = &prio
	if g.app == "api" || g.app == "frontend" {
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{Weight: 100, PodAffinityTerm: corev1.PodAffinityTerm{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": g.app}}, TopologyKey: "kubernetes.io/hostname"}}}}}
	}
	if g.app == "indexer" {
		p.Spec.NodeSelector = map[string]string{"workload": "analytics"}
		p.Spec.Tolerations = append(p.Spec.Tolerations, corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "analytics", Effect: corev1.TaintEffectNoSchedule})
	}
	p.Spec.Tolerations = append(p.Spec.Tolerations,
		corev1.Toleration{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &grace},
		corev1.Toleration{Key: corev1.TaintNodeUnreachable, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &grace},
	)
	if len(p.Status.ContainerStatuses) > 0 {
		cs := &p.Status.ContainerStatuses[0]
		cs.Image = c.Image
		cs.ImageID = "registry.example/" + g.app + "@sha256:" + strings.Repeat(string(uid(c.Image))[:8], 8)
		if cs.State.Running != nil || cs.LastTerminationState.Terminated != nil {
			cs.ContainerID = "containerd://" + strings.Repeat(string(uid(p.Name))[:8], 8)
		}
	}
	if g.app == "gateway" {
		s.addSidecar(p)
	}
}

// addSidecar gives a pod an init container and a sidecar, as a service
// mesh does, so the Containers and Environment tabs show several
// containers. The sidecar's state follows the main container's.
func (s *Sim) addSidecar(p *corev1.Pod) {
	p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{
		Name: "wait-for-api", Image: "registry.example/busybox:1.36",
		Command: []string{"sh", "-c", "until nc -z api 8080; do sleep 1; done"},
		Env:     []corev1.EnvVar{{Name: "TARGET", Value: "api:8080"}},
	})
	p.Spec.Containers = append(p.Spec.Containers, corev1.Container{
		Name: "proxy", Image: "registry.example/proxy:2.1",
		Ports: []corev1.ContainerPort{{Name: "proxy", ContainerPort: 15001, Protocol: corev1.ProtocolTCP}},
		Env: []corev1.EnvVar{
			{Name: "PROXY_MODE", Value: "sidecar"},
			{Name: "UPSTREAM", Value: "127.0.0.1:8080"},
			{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
	})
	if len(p.Status.ContainerStatuses) == 0 {
		return
	}
	main := p.Status.ContainerStatuses[0]
	started := p.Status.StartTime
	if started == nil {
		started = &metav1.Time{Time: p.CreationTimestamp.Time}
	}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "wait-for-api", Image: "registry.example/busybox:1.36", Ready: true,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", StartedAt: *started, FinishedAt: metav1.Time{Time: started.Add(3 * time.Second)}}},
	}}
	p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
		Name: "proxy", Image: "registry.example/proxy:2.1", Ready: main.State.Running != nil,
		State: corev1.ContainerState{Running: main.State.Running, Waiting: main.State.Waiting},
	})
}

func appPort(app string) int32 {
	switch app {
	case "api", "gateway":
		return 8080
	case "frontend":
		return 3000
	case "cache":
		return 6379
	case "queue":
		return 5672
	case "exporter":
		return 9100
	}
	return 8000
}

// service builds a group's Service and its EndpointSlice.
func (s *Sim) service(g *group, name string, t corev1.ServiceType, headless bool, created time.Time) *corev1.Service {
	port := appPort(g.app)
	svc := &corev1.Service{TypeMeta: typeMeta("Service"), ObjectMeta: objectMeta(g.ns, name, created, map[string]string{"app": g.app}), Spec: corev1.ServiceSpec{
		Type: t, Selector: map[string]string{"app": g.app},
		Ports:           []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}},
		SessionAffinity: corev1.ServiceAffinityNone,
		IPFamilies:      []corev1.IPFamily{corev1.IPv4Protocol},
	}}
	if g.app == "cache" || g.app == "queue" || g.app == "exporter" {
		svc.Spec.Ports[0].Port = port
		svc.Spec.Ports[0].Name = g.app
	}
	if headless {
		svc.Spec.ClusterIP, svc.Spec.ClusterIPs = corev1.ClusterIPNone, []string{corev1.ClusterIPNone}
	} else {
		h := uid(g.ns + "/" + name)
		svc.Spec.ClusterIP = fmt.Sprintf("10.96.%d.%d", 1+h[0]%200, 1+h[1]%250)
		svc.Spec.ClusterIPs = []string{svc.Spec.ClusterIP}
	}
	if t == corev1.ServiceTypeNodePort || t == corev1.ServiceTypeLoadBalancer {
		svc.Spec.Ports[0].NodePort = 30000 + int32(uid(name)[0]%26)*100 + int32(port%100)
		policy := corev1.ServiceExternalTrafficPolicyCluster
		svc.Spec.ExternalTrafficPolicy = policy
	}
	// Endpoints: the pods' addresses, ready or not.
	slice := &discoveryv1.EndpointSlice{TypeMeta: typeMeta("EndpointSlice"), ObjectMeta: objectMeta(g.ns, name+"-"+suffix(s.rnd, 5), created, map[string]string{"kubernetes.io/service-name": name, "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"}), AddressType: discoveryv1.AddressTypeIPv4}
	ctrl := true
	slice.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: name, UID: svc.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
	slice.Ports = []discoveryv1.EndpointPort{{Name: &svc.Spec.Ports[0].Name, Port: &port, Protocol: &svc.Spec.Ports[0].Protocol}}
	for _, p := range g.pods {
		if p.Status.PodIP == "" {
			continue
		}
		ready := podReady(p)
		serving := ready
		node := p.Spec.NodeName
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{p.Status.PodIP},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready, Serving: &serving, Terminating: ptrBool(false)},
			NodeName:   &node,
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, UID: p.UID},
		})
	}
	s.addLocked(slice)
	return svc
}

func pvcSpec(size string) corev1.PersistentVolumeClaimSpec {
	class := "standard"
	return corev1.PersistentVolumeClaimSpec{
		AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
		StorageClassName: &class,
	}
}

// extras adds the long tail of kinds to the namespaces that have the apps
// they belong to.
func (s *Sim) extras(groups map[string]*group, now time.Time) {
	add := func(obj runtime.Object) { s.addLocked(obj) }
	created := now.Add(-30 * 24 * time.Hour)
	seen := map[string]bool{}
	for key, g := range groups {
		if seen[key] {
			continue
		}
		seen[key] = true
		// Every app reads its ConfigMap.
		add(&corev1.ConfigMap{TypeMeta: typeMeta("ConfigMap"), ObjectMeta: objectMeta(g.ns, g.app+"-config", created, map[string]string{"app": g.app}), Data: map[string]string{
			"logLevel": "info", "config.yaml": "server:\n  port: " + strconv.Itoa(int(appPort(g.app))) + "\n  workers: 4\n",
		}})
		if g.app == "api" || g.app == "worker" {
			add(&corev1.Secret{TypeMeta: typeMeta("Secret"), ObjectMeta: objectMeta(g.ns, g.app+"-credentials", created, map[string]string{"app": g.app}), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
				"username": []byte(g.app), "password": []byte(PlantedSecretValue), "database-url": []byte("postgres://" + g.app + ":" + PlantedSecretValue + "@db.internal:5432/" + g.app),
			}})
		}
	}
	// The planted Secret, which the P2-07 test looks for everywhere.
	add(&corev1.Secret{TypeMeta: typeMeta("Secret"), ObjectMeta: objectMeta("default", "planted-secret", created, nil), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"token": []byte(PlantedSecretValue)}})
	add(&corev1.Secret{TypeMeta: typeMeta("Secret"), ObjectMeta: objectMeta("default", "registry-pull", created, nil), Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"registry.example":{"auth":"` + PlantedSecretValue + `"}}}`)}})

	if _, ok := groups["web/frontend"]; ok {
		add(&corev1.Secret{TypeMeta: typeMeta("Secret"), ObjectMeta: objectMeta("web", "shop-tls", created, nil), Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": []byte(fakeCert("shop")), "tls.key": []byte("-----BEGIN PRIVATE KEY-----\n" + PlantedSecretValue + "\n-----END PRIVATE KEY-----\n")}})
		path := networkingv1.PathTypePrefix
		ing := &networkingv1.Ingress{TypeMeta: typeMeta("Ingress"), ObjectMeta: objectMeta("web", "shop", created, nil), Spec: networkingv1.IngressSpec{
			IngressClassName: ptrString("nginx"),
			TLS:              []networkingv1.IngressTLS{{Hosts: []string{"shop.example.test"}, SecretName: "shop-tls"}},
			Rules: []networkingv1.IngressRule{
				{Host: "shop.example.test", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &path, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "frontend", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}},
				{Host: "api.shop.example.test", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &path, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}},
			},
		}}
		ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.20"}}
		add(ing)
		add(&networkingv1.NetworkPolicy{TypeMeta: typeMeta("NetworkPolicy"), ObjectMeta: objectMeta("web", "default-deny-ingress", created, nil), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}})
		// The legacy Endpoints object the endpoints controller keeps beside
		// every Service: its name is a plural already (asked at the Phase 2
		// review, when the Namespaces tab said "Endpointss").
		add(&corev1.Endpoints{TypeMeta: typeMeta("Endpoints"), ObjectMeta: objectMeta("web", "frontend", created, nil), Subsets: []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.244.1.10"}}, Ports: []corev1.EndpointPort{{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP}}}}})
	}
	if g, ok := groups["web/gateway"]; ok {
		// A LoadBalancer the cloud never gave an address: pending (P2-05).
		svc := s.service(g, "gateway-lb", corev1.ServiceTypeLoadBalancer, false, now.Add(-25*time.Minute))
		add(svc)
	}
	if _, ok := groups["default/api"]; ok {
		add(&corev1.ServiceAccount{TypeMeta: typeMeta("ServiceAccount"), ObjectMeta: objectMeta("default", "api", created, nil)})
		add(&rbacv1.Role{TypeMeta: typeMeta("Role"), ObjectMeta: objectMeta("default", "api-reader", created, nil), Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "list"}}}})
		add(&rbacv1.RoleBinding{TypeMeta: typeMeta("RoleBinding"), ObjectMeta: objectMeta("default", "api-reader", created, nil), RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "api-reader"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "api", Namespace: "default"}}})
		one := intstr.FromInt32(1)
		add(&policyv1.PodDisruptionBudget{TypeMeta: typeMeta("PodDisruptionBudget"), ObjectMeta: objectMeta("default", "api", created, nil), Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}}})
		minR, target := int32(2), int32(70)
		add(&autoscalingv2.HorizontalPodAutoscaler{TypeMeta: typeMeta("HorizontalPodAutoscaler"), ObjectMeta: objectMeta("default", "api", created, nil), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MinReplicas: &minR, MaxReplicas: 10,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &target}}}},
		}})
	}
	if _, ok := groups["analytics/api"]; ok {
		add(&corev1.ResourceQuota{TypeMeta: typeMeta("ResourceQuota"), ObjectMeta: objectMeta("analytics", "compute", created, nil), Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("40"), corev1.ResourceRequestsMemory: resource.MustParse("128Gi")}}})
		add(&corev1.LimitRange{TypeMeta: typeMeta("LimitRange"), ObjectMeta: objectMeta("analytics", "defaults", created, nil), Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}}}}})
	}
	// The cluster's own Services.
	add(&corev1.Service{TypeMeta: typeMeta("Service"), ObjectMeta: objectMeta("default", "kubernetes", created.Add(-30*24*time.Hour), map[string]string{"component": "apiserver", "provider": "kubernetes"}), Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.1", ClusterIPs: []string{"10.96.0.1"}, Ports: []corev1.ServicePort{{Name: "https", Port: 443, TargetPort: intstr.FromInt32(6443), Protocol: corev1.ProtocolTCP}},
	}})
	add(&corev1.Service{TypeMeta: typeMeta("Service"), ObjectMeta: objectMeta("kube-system", "kube-dns", created.Add(-30*24*time.Hour), map[string]string{"k8s-app": "kube-dns"}), Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.10", ClusterIPs: []string{"10.96.0.10"}, Selector: map[string]string{"k8s-app": "kube-dns"},
		Ports: []corev1.ServicePort{{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP, TargetPort: intstr.FromInt32(53)}, {Name: "dns-tcp", Port: 53, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(53)}},
	}})
}

// helmReleases stores release records the way Helm does, as Secrets of
// type helm.sh/release.v1 named sh.helm.release.v1.<name>.v<revision>, and
// labels the pods the charts manage.
func (s *Sim) helmReleases(groups map[string]*group, now time.Time) {
	type release struct {
		name, ns, chart, version, app string
		values                        map[string]any
		manifest                      string
		notes                         string
		group                         string
		// unlabelled charts do not put Helm's labels on their pods, as
		// some do not; the console then finds the pods through the
		// workloads the manifest names.
		unlabelled bool
		revisions  []string // statuses, oldest first
	}
	releases := []release{
		{name: "shop", ns: "web", chart: "shop-frontend", version: "3.2.1", app: "3.2.1", group: "web/frontend",
			values:    map[string]any{"replicas": 3, "image": map[string]any{"tag": "3.2.1"}, "ingress": map[string]any{"host": "shop.example.test"}, "db": map[string]any{"password": PlantedHelmPassword, "host": "db.internal"}},
			manifest:  "---\n# Source: shop-frontend/templates/deployment.yaml\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: frontend\n  namespace: web\n---\n# Source: shop-frontend/templates/service.yaml\napiVersion: v1\nkind: Service\nmetadata:\n  name: frontend\n---\n# Source: shop-frontend/templates/ingress.yaml\napiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n  name: shop\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: frontend-config\n",
			notes:     "Thank you for installing shop-frontend.\n\nGet the application URL by running:\n  kubectl get ingress -n web shop\n",
			revisions: []string{"superseded", "failed", "deployed"}},
		{name: "analytics-cache", ns: "analytics", chart: "redis", version: "19.6.4", app: "7.4.0", group: "analytics/cache", unlabelled: true,
			values:    map[string]any{"architecture": "standalone", "auth": map[string]any{"enabled": true, "password": PlantedHelmPassword}, "master": map[string]any{"persistence": map[string]any{"size": "20Gi"}}},
			manifest:  "---\napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: cache\n  namespace: analytics\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: cache-headless\n---\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data-cache-0\n---\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data-cache-1\n---\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data-cache-2\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: analytics-cache\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cache-config\n",
			notes:     "Redis can be accessed on the following DNS name from within your cluster:\n  cache-headless.analytics.svc.cluster.local\n",
			revisions: []string{"deployed"}},
		{name: "node-agent", ns: "kube-system", chart: "node-agent", version: "0.9.2", app: "0.9.2", group: "kube-system/node-agent",
			values:    map[string]any{"tolerations": []any{map[string]any{"operator": "Exists"}}, "resources": map[string]any{"requests": map[string]any{"cpu": "50m"}}},
			manifest:  "---\napiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: node-agent\n  namespace: kube-system\n---\napiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: node-agent\n",
			revisions: []string{"superseded", "deployed"}},
		{name: "metrics-server", ns: "kube-system", chart: "metrics-server", version: "3.12.2", app: "0.7.2",
			values:    map[string]any{"args": []any{"--kubelet-insecure-tls"}, "apiService": map[string]any{"insecureSkipTLSVerify": true}},
			manifest:  "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: metrics-server\n  namespace: kube-system\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: metrics-server\n",
			revisions: []string{"deployed"}},
	}
	for _, r := range releases {
		if r.group != "" && !r.unlabelled {
			if g := groups[r.group]; g != nil {
				for _, p := range g.pods {
					p.Labels["app.kubernetes.io/managed-by"] = "Helm"
					p.Labels["app.kubernetes.io/instance"] = r.name
					p.Labels["app.kubernetes.io/name"] = r.chart
					p.Labels["helm.sh/chart"] = r.chart + "-" + r.version
				}
			}
		}
		deployedAt := func(rev int) time.Time {
			return now.Add(-time.Duration(len(r.revisions)-rev+1) * 9 * 24 * time.Hour).Add(time.Duration(rev*17) * time.Minute).Truncate(time.Second)
		}
		for i, status := range r.revisions {
			rev := i + 1
			at := deployedAt(rev)
			rec := kube.HelmRecord{Name: r.name, Namespace: r.ns, Version: rev, Config: r.values, Manifest: r.manifest}
			rec.Info = kube.HelmRecordInfo{Status: status, Notes: r.notes, FirstDeployed: deployedAt(1).Format(time.RFC3339), LastDeployed: at.Format(time.RFC3339)}
			switch {
			case rev == 1:
				rec.Info.Description = "Install complete"
			case status == "failed":
				rec.Info.Description = "Upgrade \"" + r.name + "\" failed: timed out waiting for the condition"
			default:
				rec.Info.Description = "Upgrade complete"
			}
			version := r.version
			if rev < len(r.revisions) {
				version = previousVersion(r.version, len(r.revisions)-rev)
			}
			rec.Chart.Metadata = kube.HelmChartMetadata{Name: r.chart, Version: version, AppVersion: r.app}
			name := fmt.Sprintf("sh.helm.release.v1.%s.v%d", r.name, rev)
			sec := &corev1.Secret{TypeMeta: typeMeta("Secret"), ObjectMeta: objectMeta(r.ns, name, at, map[string]string{
				"name": r.name, "owner": "helm", "status": status, "version": strconv.Itoa(rev), "modifiedAt": strconv.FormatInt(at.Unix(), 10),
			}), Type: kube.HelmSecretType, Data: map[string][]byte{"release": kube.EncodeHelmRelease(rec)}}
			s.addLocked(sec)
		}
	}
}

// previousVersion lowers a semantic version by some patch releases,
// borrowing from the minor version when the patch runs out.
func previousVersion(v string, by int) string {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return v
	}
	minor, err1 := strconv.Atoi(parts[1])
	patch, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return v
	}
	patch -= by
	for patch < 0 && minor > 0 {
		minor--
		patch += 10
	}
	return parts[0] + "." + strconv.Itoa(minor) + "." + strconv.Itoa(max(0, patch))
}

// problemEvents records the events the real controllers would have for
// the pods and Services that are not well.
func (s *Sim) problemEvents(now time.Time) {
	for _, key := range s.keysLocked("pods") {
		p := s.pods[key]
		if len(p.Status.ContainerStatuses) > 0 {
			if w := p.Status.ContainerStatuses[0].State.Waiting; w != nil {
				switch w.Reason {
				case "CrashLoopBackOff":
					s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeWarning, "BackOff", "Back-off restarting failed container main in pod "+p.Name+"_"+p.Namespace+"("+string(p.UID)+")", 12, now.Add(-30*time.Minute), now.Add(-40*time.Second), "kubelet", p.Spec.NodeName)
					s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeNormal, "Pulled", "Container image \""+p.Spec.Containers[0].Image+"\" already present on machine", 13, now.Add(-30*time.Minute), now.Add(-2*time.Minute), "kubelet", p.Spec.NodeName)
				case "ImagePullBackOff":
					s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeWarning, "Failed", "Failed to pull image \""+p.Spec.Containers[0].Image+"\": rpc error: code = NotFound desc = failed to pull and unpack image: not found", 7, now.Add(-22*time.Minute), now.Add(-90*time.Second), "kubelet", p.Spec.NodeName)
					s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeWarning, "BackOff", "Back-off pulling image \""+p.Spec.Containers[0].Image+"\"", 31, now.Add(-22*time.Minute), now.Add(-20*time.Second), "kubelet", p.Spec.NodeName)
				}
			}
		}
		if p.Spec.NodeName == "" && p.Status.Phase == corev1.PodPending {
			msg := "no nodes available"
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Message != "" {
					msg = c.Message
				}
			}
			s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeWarning, "FailedScheduling", msg, 45, now.Add(-15*time.Minute), now.Add(-10*time.Second), "default-scheduler", "")
		}
		if p.Status.Phase == corev1.PodRunning && !podReady(p) && len(p.Status.ContainerStatuses) > 0 && p.Status.ContainerStatuses[0].State.Running != nil {
			s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeWarning, "Unhealthy", "Readiness probe failed: HTTP probe failed with statuscode: 503", 108, now.Add(-9*time.Minute), now.Add(-5*time.Second), "kubelet", p.Spec.NodeName)
		}
	}
	// The kubelet's warnings about a node short of disk name the node by
	// its name in the event's UID.
	for _, name := range s.sortedNodeNames() {
		n := s.nodes[name]
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeDiskPressure && c.Status == corev1.ConditionTrue {
				s.event("", "Node", name, types.UID(name), corev1.EventTypeWarning, "ImageGCFailed", "failed to garbage collect required amount of images. Attempted to free 4523145216 bytes, but only found 0 bytes eligible to free.", 11, now.Add(-50*time.Minute), now.Add(-4*time.Minute), "kubelet", name)
			}
		}
	}
	for _, key := range s.keysLocked("services") {
		svc := s.objects["services"][key].(*corev1.Service)
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer && len(svc.Status.LoadBalancer.Ingress) == 0 {
			s.event(svc.Namespace, "Service", svc.Name, svc.UID, corev1.EventTypeWarning, "SyncLoadBalancerFailed", "Error syncing load balancer: failed to ensure load balancer: no IP addresses available in the pool", 9, now.Add(-25*time.Minute), now.Add(-70*time.Second), "service-controller", "")
			s.event(svc.Namespace, "Service", svc.Name, svc.UID, corev1.EventTypeNormal, "EnsuringLoadBalancer", "Ensuring load balancer", 9, now.Add(-25*time.Minute), now.Add(-75*time.Second), "service-controller", "")
		}
	}
}

// event stores an Event the way the API server does, with the object it is
// about, its count and its first and last times.
func (s *Sim) event(ns, kind, name string, objUID types.UID, t, reason, msg string, count int32, first, last time.Time, component, host string) {
	evName := fmt.Sprintf("%s.%x", name, uint64(last.UnixNano()))
	e := &corev1.Event{
		TypeMeta:       typeMeta("Event"),
		ObjectMeta:     objectMeta(ns, evName, first, nil),
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: name, UID: objUID, APIVersion: "v1"},
		Type:           t, Reason: reason, Message: msg, Count: count,
		FirstTimestamp: metav1.NewTime(first.Truncate(time.Second)), LastTimestamp: metav1.NewTime(last.Truncate(time.Second)),
		Source:              corev1.EventSource{Component: component, Host: host},
		ReportingController: component,
	}
	s.addLocked(e)
}

func address(n *corev1.Node, t corev1.NodeAddressType) string {
	for _, a := range n.Status.Addresses {
		if a.Type == t {
			return a.Address
		}
	}
	return ""
}

// fakeCert is PEM-looking text; nothing reads it.
func fakeCert(name string) string {
	body := strings.Repeat(string(uid(name))[:8]+strings.ToUpper(string(uid(name + "x"))[:8]), 12)
	var lines []string
	for i := 0; i < len(body); i += 64 {
		lines = append(lines, body[i:min(i+64, len(body))])
	}
	return "-----BEGIN CERTIFICATE-----\n" + strings.Join(lines, "\n") + "\n-----END CERTIFICATE-----\n"
}

func ptrString(s string) *string { return &s }
