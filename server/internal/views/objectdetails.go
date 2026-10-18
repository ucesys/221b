package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/ucesys/221b/server/internal/kube"
)

// ObjectDetails is what the drawer of any namespaced object shows: the
// row, facts that depend on the kind, labels, annotations, conditions,
// the owner chain, events and, for a workload, the pods it runs.
type ObjectDetails struct {
	ObjectRow
	Facts       []Fact            `json:"facts"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Conditions  []Condition       `json:"conditions"`
	Owners      []Owner           `json:"owners"`
	Events      []Event           `json:"events"`
	// Pods is set for kinds that run pods (Deployments, StatefulSets,
	// DaemonSets, ReplicaSets, Jobs, CronJobs): the pods they own, through
	// their ReplicaSets and Jobs.
	Pods []PodRow `json:"pods"`
	// Workload is true for those kinds, so the drawer shows the pods even
	// when there are none.
	Workload bool `json:"workload"`
	// Keys lists a ConfigMap's or Secret's keys with their sizes; values
	// never travel (X-01).
	Keys []kube.KeySize `json:"keys,omitempty"`
	// Release is the Helm release the object belongs to, by Helm's labels
	// and annotation or by the release manifest, through its owners.
	Release string `json:"release,omitempty"`
}

// Fact is one line of a details list.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Mono asks for the monospace face: names, images, selectors.
	Mono bool `json:"mono,omitempty"`
}

// BuildObjectDetails computes the drawer's data for an object of any kind
// the console watches; ok is false when there is no such object.
func BuildObjectDetails(src Source, k kube.Kind, namespace, name string, now time.Time) (ObjectDetails, bool) {
	ns := namespace
	if !k.Namespaced {
		ns = ""
	}
	obj := src.Object(k.Name, ns, name)
	if obj == nil {
		return ObjectDetails{}, false
	}
	row, ok := objectRow(src, k, obj, now)
	if !ok {
		return ObjectDetails{}, false
	}
	m, _ := obj.(metav1.Object)
	d := ObjectDetails{
		ObjectRow:   row,
		Facts:       []Fact{},
		Labels:      m.GetLabels(),
		Annotations: m.GetAnnotations(),
		Conditions:  []Condition{},
		Owners:      ownerChain(src, m.GetNamespace(), m.GetOwnerReferences(), 4),
		Events:      eventsFor(src, string(m.GetUID())),
		Pods:        []PodRow{},
	}
	if d.Labels == nil {
		d.Labels = map[string]string{}
	}
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	if d.Owners == nil {
		d.Owners = []Owner{}
	}
	fact := func(label, value string) {
		if value != "" {
			d.Facts = append(d.Facts, Fact{Label: label, Value: value})
		}
	}
	mono := func(label, value string) {
		if value != "" {
			d.Facts = append(d.Facts, Fact{Label: label, Value: value, Mono: true})
		}
	}
	switch x := obj.(type) {
	case *appsv1.Deployment:
		d.Workload = true
		fact("Replicas", replicasText(ptrInt32(x.Spec.Replicas, 1), x.Status.ReadyReplicas, x.Status.UpdatedReplicas, x.Status.AvailableReplicas, x.Status.UnavailableReplicas))
		fact("Strategy", strategyText(string(x.Spec.Strategy.Type), x.Spec.Strategy.RollingUpdate))
		mono("Selector", selectorText(x.Spec.Selector))
		mono("Images", fullImages(x.Spec.Template.Spec.Containers))
		fact("Revision", x.Annotations["deployment.kubernetes.io/revision"])
		fact("Paused", boolText(x.Spec.Paused, "yes: no new rollouts until resumed", ""))
		d.Conditions = conditionsOf(len(x.Status.Conditions), func(i int) Condition {
			c := x.Status.Conditions[i]
			return Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)}
		})
	case *appsv1.StatefulSet:
		d.Workload = true
		fact("Replicas", fmt.Sprintf("%d desired · %d ready · %d current · %d updated", ptrInt32(x.Spec.Replicas, 1), x.Status.ReadyReplicas, x.Status.CurrentReplicas, x.Status.UpdatedReplicas))
		mono("Service", x.Spec.ServiceName)
		fact("Update strategy", string(x.Spec.UpdateStrategy.Type))
		fact("Pod management", string(x.Spec.PodManagementPolicy))
		mono("Selector", selectorText(x.Spec.Selector))
		mono("Images", fullImages(x.Spec.Template.Spec.Containers))
		if x.Status.CurrentRevision != "" && x.Status.CurrentRevision != x.Status.UpdateRevision {
			mono("Revisions", x.Status.CurrentRevision+" → "+x.Status.UpdateRevision)
		}
		d.Conditions = conditionsOf(len(x.Status.Conditions), func(i int) Condition {
			c := x.Status.Conditions[i]
			return Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)}
		})
	case *appsv1.DaemonSet:
		d.Workload = true
		fact("Nodes", fmt.Sprintf("%d desired · %d current · %d ready · %d up to date · %d available", x.Status.DesiredNumberScheduled, x.Status.CurrentNumberScheduled, x.Status.NumberReady, x.Status.UpdatedNumberScheduled, x.Status.NumberAvailable))
		if x.Status.NumberMisscheduled > 0 {
			fact("Misscheduled", fmt.Sprintf("%d pods run on nodes they should not", x.Status.NumberMisscheduled))
		}
		fact("Update strategy", string(x.Spec.UpdateStrategy.Type))
		mono("Node selector", labelsText(x.Spec.Template.Spec.NodeSelector))
		mono("Selector", selectorText(x.Spec.Selector))
		mono("Images", fullImages(x.Spec.Template.Spec.Containers))
		d.Conditions = conditionsOf(len(x.Status.Conditions), func(i int) Condition {
			c := x.Status.Conditions[i]
			return Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)}
		})
	case *appsv1.ReplicaSet:
		d.Workload = true
		fact("Replicas", replicasText(ptrInt32(x.Spec.Replicas, 1), x.Status.ReadyReplicas, 0, x.Status.AvailableReplicas, 0))
		mono("Selector", selectorText(x.Spec.Selector))
		mono("Images", fullImages(x.Spec.Template.Spec.Containers))
		fact("Revision", x.Annotations["deployment.kubernetes.io/revision"])
		d.Conditions = conditionsOf(len(x.Status.Conditions), func(i int) Condition {
			c := x.Status.Conditions[i]
			return Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)}
		})
	case *batchv1.Job:
		d.Workload = true
		fact("Pods", fmt.Sprintf("%d active · %d succeeded · %d failed", x.Status.Active, x.Status.Succeeded, x.Status.Failed))
		if x.Spec.Completions != nil {
			fact("Completions", fmt.Sprintf("%d of %d", x.Status.Succeeded, *x.Spec.Completions))
		}
		if x.Spec.Parallelism != nil {
			fact("Parallelism", fmt.Sprint(*x.Spec.Parallelism))
		}
		if x.Spec.BackoffLimit != nil {
			fact("Back-off limit", fmt.Sprintf("%d retries", *x.Spec.BackoffLimit))
		}
		if x.Status.StartTime != nil {
			fact("Started", fmtTime(x.Status.StartTime.Time, now))
		}
		if x.Status.CompletionTime != nil {
			fact("Finished", fmtTime(x.Status.CompletionTime.Time, now))
			if x.Status.StartTime != nil {
				fact("Took", dur(x.Status.CompletionTime.Sub(x.Status.StartTime.Time)))
			}
		}
		mono("Images", fullImages(x.Spec.Template.Spec.Containers))
		d.Conditions = conditionsOf(len(x.Status.Conditions), func(i int) Condition {
			c := x.Status.Conditions[i]
			return Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)}
		})
	case *batchv1.CronJob:
		d.Workload = true
		mono("Schedule", x.Spec.Schedule)
		if x.Spec.TimeZone != nil {
			fact("Time zone", *x.Spec.TimeZone)
		}
		fact("Suspended", boolText(x.Spec.Suspend != nil && *x.Spec.Suspend, "yes: no new jobs are started", ""))
		fact("Concurrency", string(x.Spec.ConcurrencyPolicy))
		if x.Status.LastScheduleTime != nil {
			fact("Last run", fmtTime(x.Status.LastScheduleTime.Time, now))
		}
		if x.Status.LastSuccessfulTime != nil {
			fact("Last success", fmtTime(x.Status.LastSuccessfulTime.Time, now))
		}
		fact("Active jobs", fmt.Sprint(len(x.Status.Active)))
		mono("Images", fullImages(x.Spec.JobTemplate.Spec.Template.Spec.Containers))
	case *corev1.Service:
		fact("Type", string(x.Spec.Type))
		mono("Cluster IP", x.Spec.ClusterIP)
		mono("Selector", labelsText(x.Spec.Selector))
	case *discoveryv1.EndpointSlice:
		fact("Address type", string(x.AddressType))
		mono("Service", x.Labels[discoveryv1.LabelServiceName])
		var addrs []string
		for _, e := range x.Endpoints {
			ready := "ready"
			if e.Conditions.Ready != nil && !*e.Conditions.Ready {
				ready = "not ready"
			}
			target := ""
			if e.TargetRef != nil {
				target = " " + e.TargetRef.Kind + " " + e.TargetRef.Name
			}
			addrs = append(addrs, strings.Join(e.Addresses, ", ")+" ("+ready+")"+target)
		}
		mono("Endpoints", strings.Join(addrs, "\n"))
		var ports []string
		for _, p := range x.Ports {
			if p.Port != nil {
				name := ""
				if p.Name != nil && *p.Name != "" {
					name = *p.Name + " "
				}
				proto := "TCP"
				if p.Protocol != nil {
					proto = string(*p.Protocol)
				}
				ports = append(ports, fmt.Sprintf("%s%d/%s", name, *p.Port, proto))
			}
		}
		mono("Ports", strings.Join(ports, ", "))
	case *networkingv1.Ingress:
		if x.Spec.IngressClassName != nil {
			fact("Class", *x.Spec.IngressClassName)
		}
		var addrs []string
		for _, a := range x.Status.LoadBalancer.Ingress {
			if a.IP != "" {
				addrs = append(addrs, a.IP)
			} else if a.Hostname != "" {
				addrs = append(addrs, a.Hostname)
			}
		}
		mono("Address", strings.Join(addrs, ", "))
		var rules []string
		for _, rule := range x.Spec.Rules {
			host := rule.Host
			if host == "" {
				host = "*"
			}
			if rule.HTTP == nil {
				rules = append(rules, host)
				continue
			}
			for _, p := range rule.HTTP.Paths {
				path := p.Path
				if path == "" {
					path = "/"
				}
				to := ""
				if p.Backend.Service != nil {
					to = p.Backend.Service.Name
					if p.Backend.Service.Port.Number != 0 {
						to += fmt.Sprintf(":%d", p.Backend.Service.Port.Number)
					} else if p.Backend.Service.Port.Name != "" {
						to += ":" + p.Backend.Service.Port.Name
					}
				}
				rules = append(rules, host+path+" → "+to)
			}
		}
		mono("Rules", strings.Join(rules, "\n"))
		var tls []string
		for _, t := range x.Spec.TLS {
			line := strings.Join(t.Hosts, ", ")
			if t.SecretName != "" {
				line += " (Secret " + t.SecretName + ")"
			}
			tls = append(tls, line)
		}
		mono("TLS", strings.Join(tls, "\n"))
		if x.Spec.DefaultBackend != nil && x.Spec.DefaultBackend.Service != nil {
			mono("Default backend", x.Spec.DefaultBackend.Service.Name)
		}
	case *corev1.PersistentVolumeClaim:
		fact("Phase", string(x.Status.Phase))
		if q, ok := x.Status.Capacity[corev1.ResourceStorage]; ok {
			fact("Capacity", q.String())
		}
		if q, ok := x.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			fact("Requested", q.String())
		}
		if x.Spec.StorageClassName != nil {
			fact("Storage class", *x.Spec.StorageClassName)
		}
		var modes []string
		for _, m := range x.Spec.AccessModes {
			modes = append(modes, string(m))
		}
		fact("Access modes", strings.Join(modes, ", "))
		if x.Spec.VolumeMode != nil {
			fact("Volume mode", string(*x.Spec.VolumeMode))
		}
		mono("Volume", x.Spec.VolumeName)
		d.Conditions = conditionsOf(len(x.Status.Conditions), func(i int) Condition {
			c := x.Status.Conditions[i]
			return Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)}
		})
	case *kube.ConfigMapSummary:
		d.Keys = x.Keys
		fact("Keys", fmt.Sprint(len(x.Keys)))
		fact("Immutable", boolText(x.Immutable, "yes", ""))
	case *kube.SecretSummary:
		d.Keys = x.Keys
		fact("Type", string(x.Type))
		fact("Keys", fmt.Sprintf("%d, shown by name and size only", len(x.Keys)))
		fact("Immutable", boolText(x.Immutable, "yes", ""))
		if x.Release != nil {
			fact("Helm release", fmt.Sprintf("%s, revision %d, %s", x.Release.Name, x.Release.Revision, x.Release.Status))
		}
	case *corev1.Event:
		fact("Type", x.Type)
		fact("Reason", x.Reason)
		fact("Message", x.Message)
		mono("Object", x.InvolvedObject.Kind+" "+x.InvolvedObject.Name)
		if x.Count > 1 {
			fact("Count", fmt.Sprint(x.Count))
		}
		if !x.LastTimestamp.IsZero() {
			fact("Last seen", fmtTime(x.LastTimestamp.Time, now))
		}
		fact("From", x.Source.Component)
	}
	if d.Workload {
		d.Pods = podsOwnedBy(src, m.GetNamespace(), k.Kind, name, now)
	}
	d.Release = objectRelease(m)
	if d.Release == "" {
		d.Release = releaseOwning(src, m.GetNamespace(), append([]Owner{{Kind: k.Kind, Name: name}}, d.Owners...))
	}
	return d, true
}

// objectRelease reads the Helm release an object was installed by from the
// labels and annotation Helm sets.
func objectRelease(m metav1.Object) string {
	// Helm writes the annotation itself; charts set the instance label, and
	// some set it to something else.
	if r := m.GetAnnotations()["meta.helm.sh/release-name"]; r != "" {
		return r
	}
	if m.GetLabels()["app.kubernetes.io/managed-by"] == "Helm" {
		return m.GetLabels()["app.kubernetes.io/instance"]
	}
	return ""
}

// podsOwnedBy lists a workload's pods: those whose controlling owners, up
// through ReplicaSets and Jobs, reach the workload.
func podsOwnedBy(src Source, namespace, kind, name string, now time.Time) []PodRow {
	out := []PodRow{}
	for _, o := range src.Objects("pods", namespace) {
		p, ok := o.(*corev1.Pod)
		if !ok {
			continue
		}
		for _, owner := range ownerChain(src, namespace, p.OwnerReferences, 4) {
			if owner.Kind == kind && owner.Name == name {
				out = append(out, BuildPodRow(src, p, now))
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// fullImages lists containers with their images, one per line, as the
// details show them; rows abbreviate them instead.
func fullImages(cs []corev1.Container) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name+": "+c.Image)
	}
	return strings.Join(out, "\n")
}

func conditionsOf(n int, at func(i int) Condition) []Condition {
	out := make([]Condition, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, at(i))
	}
	return out
}

func ptrInt32(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

func replicasText(desired, ready, updated, available, unavailable int32) string {
	parts := []string{fmt.Sprintf("%d desired", desired), fmt.Sprintf("%d ready", ready)}
	if updated > 0 {
		parts = append(parts, fmt.Sprintf("%d up to date", updated))
	}
	parts = append(parts, fmt.Sprintf("%d available", available))
	if unavailable > 0 {
		parts = append(parts, fmt.Sprintf("%d unavailable", unavailable))
	}
	return strings.Join(parts, " · ")
}

func strategyText(kind string, ru *appsv1.RollingUpdateDeployment) string {
	if ru == nil || kind != string(appsv1.RollingUpdateDeploymentStrategyType) {
		return kind
	}
	var parts []string
	if ru.MaxSurge != nil {
		parts = append(parts, "max surge "+ru.MaxSurge.String())
	}
	if ru.MaxUnavailable != nil {
		parts = append(parts, "max unavailable "+ru.MaxUnavailable.String())
	}
	if len(parts) == 0 {
		return kind
	}
	return kind + " (" + strings.Join(parts, ", ") + ")"
}

func selectorText(sel *metav1.LabelSelector) string {
	if sel == nil {
		return ""
	}
	s, err := metaSelector(sel)
	if err != nil {
		return ""
	}
	return s
}

func labelsText(m map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

func boolText(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func fmtTime(t time.Time, now time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05 UTC") + " · " + ago(t, now)
}

// ObjectKindOf finds the registry kind for a kind name as the rows carry
// it ("Deployment") or as the API names it ("deployments").
func ObjectKindOf(name string) (kube.Kind, bool) {
	if k, ok := kube.KindByName(name); ok {
		return k, true
	}
	return kube.KindOf(name)
}

var _ runtime.Object = (*corev1.Pod)(nil)
