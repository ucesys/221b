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

// NamespaceRow is one namespace as the Namespaces tab lists it.
type NamespaceRow struct {
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
	// Group is "system" for Kubernetes' own namespaces, "apps" otherwise.
	Group string `json:"group"`
	// Counts are the objects of each kind, by the kind's name.
	Counts  map[string]int `json:"counts"`
	Objects int            `json:"objects"`
	Kinds   int            `json:"kinds"`
	Pods    int            `json:"pods"`
	// PodsNotRunning counts pods that are not running and not done.
	PodsNotRunning int               `json:"podsNotRunning"`
	PodsFailing    int               `json:"podsFailing"`
	Releases       []string          `json:"releases"`
	Labels         map[string]string `json:"labels"`
}

// systemNamespaces are Kubernetes' own.
func systemNamespace(name string) bool {
	return name == "default" || strings.HasPrefix(name, "kube-")
}

// BuildNamespaceRow computes a namespace's row.
func BuildNamespaceRow(src Source, ns *corev1.Namespace, now time.Time) NamespaceRow {
	r := NamespaceRow{
		Name:     ns.Name,
		Status:   string(ns.Status.Phase),
		Created:  ns.CreationTimestamp.UTC(),
		Group:    "apps",
		Counts:   map[string]int{},
		Releases: []string{},
		Labels:   ns.Labels,
	}
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	if systemNamespace(ns.Name) {
		r.Group = "system"
	}
	for _, k := range src.Kinds() {
		if !k.Namespaced {
			continue
		}
		n := src.Count(k.Name, ns.Name)
		if n > 0 {
			r.Counts[k.Name] = n
			r.Objects += n
			r.Kinds++
		}
	}
	for _, o := range src.Objects("pods", ns.Name) {
		p, ok := o.(*corev1.Pod)
		if !ok {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		r.Pods++
		status, tone, _ := podStatus(p, now)
		if status != "Running" {
			r.PodsNotRunning++
		}
		if tone == "err" {
			r.PodsFailing++
		}
	}
	seen := map[string]bool{}
	for _, o := range src.Objects("secrets", ns.Name) {
		if s, ok := o.(*kube.SecretSummary); ok && s.Release != nil && !seen[s.Release.Name] {
			seen[s.Release.Name] = true
			r.Releases = append(r.Releases, s.Release.Name)
		}
	}
	sort.Strings(r.Releases)
	return r
}

// ObjectRow is one object of a namespace, as the Namespaces tab lists
// them under their kind: a name, a status, a line of details and an age.
type ObjectRow struct {
	Key        string    `json:"key"`
	Kind       string    `json:"kind"`
	KindName   string    `json:"kindName"`
	APIVersion string    `json:"apiVersion"`
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Status     string    `json:"status"`
	Tone       string    `json:"tone"`
	Details    string    `json:"details"`
	Created    time.Time `json:"created"`
	// Order is the kind's place in the list.
	Order int `json:"order"`
}

// BuildObjectRows lists every object of a namespace, by kind in the order
// the prototype shows kinds, then by name.
func BuildObjectRows(src Source, namespace string, now time.Time) []ObjectRow {
	var out []ObjectRow
	for _, k := range src.Kinds() {
		if !k.Namespaced {
			continue
		}
		objs := src.Objects(k.Name, namespace)
		for _, o := range objs {
			if row, ok := objectRow(src, k, o, now); ok {
				out = append(out, row)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ObjectKey is the key of an object's row: "Kind/name".
func ObjectKey(kind, name string) string { return kind + "/" + name }

func objectRow(src Source, k kube.Kind, o runtime.Object, now time.Time) (ObjectRow, bool) {
	m, ok := o.(metav1.Object)
	if !ok {
		return ObjectRow{}, false
	}
	r := ObjectRow{
		Key: ObjectKey(k.Kind, m.GetName()), Kind: k.Kind, KindName: k.Name, APIVersion: k.APIVersion(),
		Name: m.GetName(), Namespace: m.GetNamespace(), Created: m.GetCreationTimestamp().UTC(), Order: k.Order,
		Status: "—", Tone: "muted",
	}
	switch x := o.(type) {
	case *corev1.Pod:
		r.Status, r.Tone, _ = podStatus(x, now)
		ready, total := 0, len(x.Spec.Containers)
		for _, cs := range x.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
		}
		var parts []string
		parts = append(parts, fmt.Sprintf("%d/%d ready", ready, total))
		if x.Spec.NodeName != "" {
			parts = append(parts, x.Spec.NodeName)
		} else {
			parts = append(parts, "no node")
		}
		var restarts int32
		for _, cs := range x.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
		if restarts > 0 {
			parts = append(parts, fmt.Sprintf("%d restarts", restarts))
		}
		r.Details = strings.Join(parts, " · ")
	case *appsv1.Deployment:
		desired := int32(1)
		if x.Spec.Replicas != nil {
			desired = *x.Spec.Replicas
		}
		r.Status, r.Tone = readyStatus(x.Status.ReadyReplicas, desired)
		r.Details = images(x.Spec.Template.Spec.Containers)
	case *appsv1.StatefulSet:
		desired := int32(1)
		if x.Spec.Replicas != nil {
			desired = *x.Spec.Replicas
		}
		r.Status, r.Tone = readyStatus(x.Status.ReadyReplicas, desired)
		r.Details = images(x.Spec.Template.Spec.Containers)
	case *appsv1.DaemonSet:
		r.Status, r.Tone = readyStatus(x.Status.NumberReady, x.Status.DesiredNumberScheduled)
		r.Details = images(x.Spec.Template.Spec.Containers)
	case *appsv1.ReplicaSet:
		desired := int32(1)
		if x.Spec.Replicas != nil {
			desired = *x.Spec.Replicas
		}
		if desired == 0 {
			r.Status, r.Tone = "scaled to 0", "muted"
		} else {
			r.Status, r.Tone = readyStatus(x.Status.ReadyReplicas, desired)
		}
		r.Details = images(x.Spec.Template.Spec.Containers)
		if owner := controllerOf(src, x.Namespace, x.OwnerReferences); owner != nil {
			r.Details = "of " + owner.Kind + " " + owner.Name + " · " + r.Details
		}
	case *batchv1.Job:
		switch {
		case x.Status.Succeeded > 0 && x.Status.Active == 0:
			r.Status, r.Tone = "succeeded", "ok"
		case x.Status.Failed > 0 && x.Status.Active == 0:
			r.Status, r.Tone = fmt.Sprintf("failed (%d)", x.Status.Failed), "err"
		case x.Status.Active > 0:
			r.Status, r.Tone = fmt.Sprintf("%d running", x.Status.Active), "info"
		default:
			r.Status, r.Tone = "pending", "warn"
		}
		if x.Status.CompletionTime != nil {
			r.Details = "finished " + ago(x.Status.CompletionTime.Time, now)
		} else if x.Status.StartTime != nil {
			r.Details = "started " + ago(x.Status.StartTime.Time, now)
		}
		if owner := controllerOf(src, x.Namespace, x.OwnerReferences); owner != nil {
			r.Details = "of " + owner.Kind + " " + owner.Name + sep(r.Details)
		}
	case *batchv1.CronJob:
		if x.Spec.Suspend != nil && *x.Spec.Suspend {
			r.Status, r.Tone = "suspended", "muted"
		} else {
			r.Status, r.Tone = "scheduled", "ok"
		}
		r.Details = x.Spec.Schedule
		if x.Status.LastScheduleTime != nil {
			r.Details += " · last run " + ago(x.Status.LastScheduleTime.Time, now)
		}
	case *corev1.Service:
		ready, total := endpointCounts(src, x)
		if len(x.Spec.Selector) == 0 {
			r.Status, r.Tone = "no selector", "muted"
		} else {
			r.Status, r.Tone = fmt.Sprintf("%d/%d endpoints", ready, total), endpointTone(ready, total)
		}
		var ports []string
		for _, p := range x.Spec.Ports {
			ports = append(ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
		}
		kind := string(x.Spec.Type)
		if x.Spec.ClusterIP == corev1.ClusterIPNone {
			kind = "headless"
		}
		r.Details = kind + " · " + strings.Join(ports, ", ")
	case *discoveryv1.EndpointSlice:
		ready, total := 0, len(x.Endpoints)
		for _, e := range x.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				ready++
			}
		}
		r.Status, r.Tone = fmt.Sprintf("%d/%d ready", ready, total), endpointTone(ready, total)
		r.Details = "Service " + x.Labels[discoveryv1.LabelServiceName]
	case *networkingv1.Ingress:
		var hosts []string
		for _, rule := range x.Spec.Rules {
			if rule.Host != "" {
				hosts = append(hosts, rule.Host)
			}
		}
		if len(x.Status.LoadBalancer.Ingress) > 0 {
			r.Status, r.Tone = "published", "ok"
		} else {
			r.Status, r.Tone = "no address yet", "warn"
		}
		r.Details = strings.Join(hosts, ", ")
	case *corev1.PersistentVolumeClaim:
		r.Status = string(x.Status.Phase)
		switch x.Status.Phase {
		case corev1.ClaimBound:
			r.Tone = "ok"
		case corev1.ClaimPending:
			r.Tone = "warn"
		default:
			r.Tone = "err"
		}
		var parts []string
		if q, ok := x.Status.Capacity[corev1.ResourceStorage]; ok {
			parts = append(parts, q.String())
		} else if q, ok := x.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			parts = append(parts, q.String()+" requested")
		}
		if x.Spec.StorageClassName != nil && *x.Spec.StorageClassName != "" {
			parts = append(parts, *x.Spec.StorageClassName)
		}
		r.Details = strings.Join(parts, " · ")
	case *kube.ConfigMapSummary:
		r.Status, r.Tone = fmt.Sprintf("%d keys", len(x.Keys)), "muted"
		r.Details = keyList(x.Keys)
	case *kube.SecretSummary:
		r.Status, r.Tone = kube.SecretTypeName(x.Type), "muted"
		if x.Release != nil {
			r.Details = fmt.Sprintf("%s revision %d · %s", x.Release.Name, x.Release.Revision, x.Release.Status)
		} else {
			r.Details = keyList(x.Keys)
		}
	case *metav1.PartialObjectMetadata:
		if owner := controllerOf(src, x.Namespace, x.OwnerReferences); owner != nil {
			r.Details = "of " + owner.Kind + " " + owner.Name
		}
	case *corev1.Event:
		r.Status, r.Tone = x.Reason, "muted"
		if x.Type == corev1.EventTypeWarning {
			r.Tone = "warn"
		}
		r.Details = x.InvolvedObject.Kind + " " + x.InvolvedObject.Name + ": " + x.Message
	}
	return r, true
}

func sep(s string) string {
	if s == "" {
		return ""
	}
	return " · " + s
}

func readyStatus(ready, desired int32) (string, string) {
	s := fmt.Sprintf("%d/%d ready", ready, desired)
	switch {
	case desired == 0:
		return "scaled to 0", "muted"
	case ready >= desired:
		return s, "ok"
	case ready == 0:
		return s, "err"
	}
	return s, "warn"
}

func endpointTone(ready, total int) string {
	switch {
	case total == 0:
		return "warn"
	case ready == total:
		return "ok"
	case ready == 0:
		return "err"
	}
	return "warn"
}

func images(cs []corev1.Container) string {
	var out []string
	for _, c := range cs {
		img := c.Image
		if i := strings.LastIndex(img, "/"); i >= 0 && strings.Contains(img[i:], ":") {
			img = img[i+1:]
		}
		out = append(out, img)
	}
	return strings.Join(out, ", ")
}

// keyList lists keys with their sizes: "ca.crt (1.1 KB), token (900 B)".
func keyList(keys []kube.KeySize) string {
	var parts []string
	for i, k := range keys {
		if i == 6 {
			parts = append(parts, fmt.Sprintf("and %d more", len(keys)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", k.Key, sizeText(k.Bytes)))
	}
	return strings.Join(parts, ", ")
}

func sizeText(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
