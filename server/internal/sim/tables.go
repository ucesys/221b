package sim

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/ucesys/221b/server/internal/kube"
)

// Server-side printing: kubectl get asks for a Table, which a real API
// server fills with the columns of its built-in printers. These follow
// those printers for the kinds the console shows, so "kubectl get" in
// Watson prints what it prints against a real cluster (Phase 6).

// wantsTable reports whether the client asked for a Table.
func wantsTable(accept string) bool { return strings.Contains(accept, "as=Table") }

type column struct {
	name, typ, format string
	priority          int32
}

func col(name string) column  { return column{name: name, typ: "string"} }
func wide(name string) column { return column{name: name, typ: "string", priority: 1} }
func nameCol() column         { return column{name: "Name", typ: "string", format: "name"} }
func ageCol() column          { return column{name: "Age", typ: "string"} }
func age(t metav1.Time, now time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(now.Sub(t.Time))
}

// table prints objects of a kind as the API server's printers do.
func table(k kube.Kind, objs []runtime.Object, rv string, now time.Time) metav1.Table {
	cols, cells := printer(k)
	t := metav1.Table{TypeMeta: metav1.TypeMeta{Kind: "Table", APIVersion: "meta.k8s.io/v1"}, ListMeta: metav1.ListMeta{ResourceVersion: rv}}
	for _, c := range cols {
		t.ColumnDefinitions = append(t.ColumnDefinitions, metav1.TableColumnDefinition{Name: c.name, Type: c.typ, Format: c.format, Priority: c.priority})
	}
	for _, o := range objs {
		row := metav1.TableRow{Cells: cells(o, now)}
		row.Object.Raw = encodePartial(o)
		t.Rows = append(t.Rows, row)
	}
	if t.Rows == nil {
		t.Rows = []metav1.TableRow{}
	}
	return t
}

func cellsOf(v ...any) []any { return v }

func nameOf(o runtime.Object) string {
	m, err := meta.Accessor(o)
	if err != nil {
		return ""
	}
	return m.GetName()
}

func createdOf(o runtime.Object) metav1.Time {
	m, err := meta.Accessor(o)
	if err != nil {
		return metav1.Time{}
	}
	return m.GetCreationTimestamp()
}

func containersAndImages(spec corev1.PodSpec) (string, string) {
	var names, images []string
	for _, c := range spec.Containers {
		names = append(names, c.Name)
		images = append(images, c.Image)
	}
	return strings.Join(names, ","), strings.Join(images, ",")
}

func int32Of(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

// printer returns the columns of a kind and how to fill them.
func printer(k kube.Kind) ([]column, func(runtime.Object, time.Time) []any) {
	switch k.Name {
	case "nodes":
		return []column{nameCol(), col("Status"), col("Roles"), ageCol(), col("Version"), wide("Internal-IP"), wide("External-IP"), wide("OS-Image"), wide("Kernel-Version"), wide("Container-Runtime")},
			func(o runtime.Object, now time.Time) []any {
				n := o.(*corev1.Node)
				st := "Unknown"
				for _, c := range n.Status.Conditions {
					if c.Type == corev1.NodeReady {
						switch c.Status {
						case corev1.ConditionTrue:
							st = "Ready"
						case corev1.ConditionFalse:
							st = "NotReady"
						}
					}
				}
				if n.Spec.Unschedulable {
					st += ",SchedulingDisabled"
				}
				var roles []string
				for l := range n.Labels {
					if r, ok := strings.CutPrefix(l, "node-role.kubernetes.io/"); ok && r != "" {
						roles = append(roles, r)
					}
				}
				sort.Strings(roles)
				role := strings.Join(roles, ",")
				if role == "" {
					role = "<none>"
				}
				ext := "<none>"
				if a := address(n, corev1.NodeExternalIP); a != "" {
					ext = a
				}
				in := address(n, corev1.NodeInternalIP)
				if in == "" {
					in = "<none>"
				}
				i := n.Status.NodeInfo
				return cellsOf(n.Name, st, role, age(n.CreationTimestamp, now), i.KubeletVersion, in, ext, i.OSImage, i.KernelVersion, i.ContainerRuntimeVersion)
			}
	case "pods":
		return []column{nameCol(), col("Ready"), col("Status"), col("Restarts"), ageCol(), wide("IP"), wide("Node"), wide("Nominated Node"), wide("Readiness Gates")},
			func(o runtime.Object, now time.Time) []any {
				p := o.(*corev1.Pod)
				ready, total := 0, len(p.Spec.Containers)
				for _, c := range p.Status.ContainerStatuses {
					if c.Ready {
						ready++
					}
				}
				ip, node := p.Status.PodIP, p.Spec.NodeName
				if ip == "" {
					ip = "<none>"
				}
				if node == "" {
					node = "<none>"
				}
				return cellsOf(p.Name, fmt.Sprintf("%d/%d", ready, total), podStatus(p), podRestarts(p, now), age(p.CreationTimestamp, now), ip, node, "<none>", "<none>")
			}
	case "deployments":
		return []column{nameCol(), col("Ready"), col("Up-to-date"), col("Available"), ageCol(), wide("Containers"), wide("Images"), wide("Selector")},
			func(o runtime.Object, now time.Time) []any {
				d := o.(*appsv1.Deployment)
				c, i := containersAndImages(d.Spec.Template.Spec)
				return cellsOf(d.Name, fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, int32Of(d.Spec.Replicas, 1)), int64(d.Status.UpdatedReplicas), int64(d.Status.AvailableReplicas), age(d.CreationTimestamp, now), c, i, metav1.FormatLabelSelector(d.Spec.Selector))
			}
	case "replicasets":
		return []column{nameCol(), col("Desired"), col("Current"), col("Ready"), ageCol(), wide("Containers"), wide("Images"), wide("Selector")},
			func(o runtime.Object, now time.Time) []any {
				r := o.(*appsv1.ReplicaSet)
				c, i := containersAndImages(r.Spec.Template.Spec)
				return cellsOf(r.Name, int64(int32Of(r.Spec.Replicas, 1)), int64(r.Status.Replicas), int64(r.Status.ReadyReplicas), age(r.CreationTimestamp, now), c, i, metav1.FormatLabelSelector(r.Spec.Selector))
			}
	case "statefulsets":
		return []column{nameCol(), col("Ready"), ageCol(), wide("Containers"), wide("Images")},
			func(o runtime.Object, now time.Time) []any {
				st := o.(*appsv1.StatefulSet)
				c, i := containersAndImages(st.Spec.Template.Spec)
				return cellsOf(st.Name, fmt.Sprintf("%d/%d", st.Status.ReadyReplicas, int32Of(st.Spec.Replicas, 1)), age(st.CreationTimestamp, now), c, i)
			}
	case "daemonsets":
		return []column{nameCol(), col("Desired"), col("Current"), col("Ready"), col("Up-to-date"), col("Available"), col("Node Selector"), ageCol(), wide("Containers"), wide("Images"), wide("Selector")},
			func(o runtime.Object, now time.Time) []any {
				d := o.(*appsv1.DaemonSet)
				c, i := containersAndImages(d.Spec.Template.Spec)
				sel := labels.FormatLabels(d.Spec.Template.Spec.NodeSelector)
				s := d.Status
				return cellsOf(d.Name, int64(s.DesiredNumberScheduled), int64(s.CurrentNumberScheduled), int64(s.NumberReady), int64(s.UpdatedNumberScheduled), int64(s.NumberAvailable), sel, age(d.CreationTimestamp, now), c, i, metav1.FormatLabelSelector(d.Spec.Selector))
			}
	case "jobs":
		return []column{nameCol(), col("Status"), col("Completions"), col("Duration"), ageCol(), wide("Containers"), wide("Images"), wide("Selector")},
			func(o runtime.Object, now time.Time) []any {
				j := o.(*batchv1.Job)
				c, i := containersAndImages(j.Spec.Template.Spec)
				st := "Running"
				for _, cd := range j.Status.Conditions {
					if cd.Status == corev1.ConditionTrue && (cd.Type == batchv1.JobComplete || cd.Type == batchv1.JobFailed) {
						st = map[batchv1.JobConditionType]string{batchv1.JobComplete: "Complete", batchv1.JobFailed: "Failed"}[cd.Type]
					}
				}
				dur := ""
				if j.Status.StartTime != nil {
					end := now
					if j.Status.CompletionTime != nil {
						end = j.Status.CompletionTime.Time
					}
					dur = duration.HumanDuration(end.Sub(j.Status.StartTime.Time))
				}
				return cellsOf(j.Name, st, fmt.Sprintf("%d/%d", j.Status.Succeeded, int32Of(j.Spec.Completions, 1)), dur, age(j.CreationTimestamp, now), c, i, metav1.FormatLabelSelector(j.Spec.Selector))
			}
	case "cronjobs":
		return []column{nameCol(), col("Schedule"), col("Timezone"), col("Suspend"), col("Active"), col("Last Schedule"), ageCol(), wide("Containers"), wide("Images"), wide("Selector")},
			func(o runtime.Object, now time.Time) []any {
				cj := o.(*batchv1.CronJob)
				c, i := containersAndImages(cj.Spec.JobTemplate.Spec.Template.Spec)
				last := "<none>"
				if cj.Status.LastScheduleTime != nil {
					last = age(*cj.Status.LastScheduleTime, now)
				}
				tz := "<none>"
				if cj.Spec.TimeZone != nil {
					tz = *cj.Spec.TimeZone
				}
				suspend := cj.Spec.Suspend != nil && *cj.Spec.Suspend
				return cellsOf(cj.Name, cj.Spec.Schedule, tz, strconv.FormatBool(suspend), int64(len(cj.Status.Active)), last, age(cj.CreationTimestamp, now), c, i, "<none>")
			}
	case "services":
		return []column{nameCol(), col("Type"), col("Cluster-IP"), col("External-IP"), col("Port(s)"), ageCol(), wide("Selector")},
			func(o runtime.Object, now time.Time) []any {
				sv := o.(*corev1.Service)
				ext := "<none>"
				switch {
				case len(sv.Spec.ExternalIPs) > 0:
					ext = strings.Join(sv.Spec.ExternalIPs, ",")
				case sv.Spec.Type == corev1.ServiceTypeLoadBalancer:
					var ips []string
					for _, in := range sv.Status.LoadBalancer.Ingress {
						if in.IP != "" {
							ips = append(ips, in.IP)
						} else if in.Hostname != "" {
							ips = append(ips, in.Hostname)
						}
					}
					ext = "<pending>"
					if len(ips) > 0 {
						ext = strings.Join(ips, ",")
					}
				case sv.Spec.Type == corev1.ServiceTypeExternalName:
					ext = sv.Spec.ExternalName
				}
				var ports []string
				for _, p := range sv.Spec.Ports {
					s := strconv.Itoa(int(p.Port))
					if p.NodePort > 0 {
						s += ":" + strconv.Itoa(int(p.NodePort))
					}
					ports = append(ports, s+"/"+string(p.Protocol))
				}
				pt := strings.Join(ports, ",")
				if pt == "" {
					pt = "<none>"
				}
				cip := sv.Spec.ClusterIP
				if cip == "" {
					cip = "<none>"
				}
				sel := labels.FormatLabels(sv.Spec.Selector)
				return cellsOf(sv.Name, string(sv.Spec.Type), cip, ext, pt, age(sv.CreationTimestamp, now), sel)
			}
	case "namespaces":
		return []column{nameCol(), col("Status"), ageCol()},
			func(o runtime.Object, now time.Time) []any {
				n := o.(*corev1.Namespace)
				ph := string(n.Status.Phase)
				if ph == "" {
					ph = "Active"
				}
				return cellsOf(n.Name, ph, age(n.CreationTimestamp, now))
			}
	case "events":
		return []column{col("Last Seen"), col("Type"), col("Reason"), col("Object"), wide("Subobject"), wide("Source"), col("Message"), wide("First Seen"), wide("Count"), {name: "Name", typ: "string", format: "name", priority: 1}},
			func(o runtime.Object, now time.Time) []any {
				e := o.(*corev1.Event)
				last := e.LastTimestamp
				if last.IsZero() {
					last = metav1.Time{Time: e.EventTime.Time}
				}
				first := e.FirstTimestamp
				if first.IsZero() {
					first = last
				}
				src := e.Source.Component
				if e.Source.Host != "" {
					src += ", " + e.Source.Host
				}
				obj := strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name
				return cellsOf(age(last, now), e.Type, e.Reason, obj, e.InvolvedObject.FieldPath, src, strings.TrimSpace(e.Message), age(first, now), int64(e.Count), e.Name)
			}
	case "configmaps":
		return []column{nameCol(), col("Data"), ageCol()},
			func(o runtime.Object, now time.Time) []any {
				c := o.(*corev1.ConfigMap)
				return cellsOf(c.Name, int64(len(c.Data)+len(c.BinaryData)), age(c.CreationTimestamp, now))
			}
	case "secrets":
		return []column{nameCol(), col("Type"), col("Data"), ageCol()},
			func(o runtime.Object, now time.Time) []any {
				c := o.(*corev1.Secret)
				return cellsOf(c.Name, string(c.Type), int64(len(c.Data)), age(c.CreationTimestamp, now))
			}
	case "persistentvolumeclaims":
		return []column{nameCol(), col("Status"), col("Volume"), col("Capacity"), col("Access Modes"), col("StorageClass"), col("VolumeAttributesClass"), ageCol(), wide("VolumeMode")},
			func(o runtime.Object, now time.Time) []any {
				c := o.(*corev1.PersistentVolumeClaim)
				capacity := ""
				if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
					capacity = q.String()
				}
				var modes []string
				for _, m := range c.Spec.AccessModes {
					modes = append(modes, map[corev1.PersistentVolumeAccessMode]string{corev1.ReadWriteOnce: "RWO", corev1.ReadOnlyMany: "ROX", corev1.ReadWriteMany: "RWX", corev1.ReadWriteOncePod: "RWOP"}[m])
				}
				sc := ""
				if c.Spec.StorageClassName != nil {
					sc = *c.Spec.StorageClassName
				}
				vm := "Filesystem"
				if c.Spec.VolumeMode != nil {
					vm = string(*c.Spec.VolumeMode)
				}
				return cellsOf(c.Name, string(c.Status.Phase), c.Spec.VolumeName, capacity, strings.Join(modes, ","), sc, "<unset>", age(c.CreationTimestamp, now), vm)
			}
	case "poddisruptionbudgets":
		return []column{nameCol(), col("Min Available"), col("Max Unavailable"), col("Allowed Disruptions"), ageCol()},
			func(o runtime.Object, now time.Time) []any {
				b := o.(*policyv1.PodDisruptionBudget)
				minA, maxU := "N/A", "N/A"
				if b.Spec.MinAvailable != nil {
					minA = b.Spec.MinAvailable.String()
				}
				if b.Spec.MaxUnavailable != nil {
					maxU = b.Spec.MaxUnavailable.String()
				}
				return cellsOf(b.Name, minA, maxU, int64(b.Status.DisruptionsAllowed), age(b.CreationTimestamp, now))
			}
	case "ingresses":
		return []column{nameCol(), col("Class"), col("Hosts"), col("Address"), col("Ports"), ageCol()},
			func(o runtime.Object, now time.Time) []any {
				in := o.(*networkingv1.Ingress)
				cls := "<none>"
				if in.Spec.IngressClassName != nil {
					cls = *in.Spec.IngressClassName
				}
				var hosts []string
				for _, r := range in.Spec.Rules {
					if r.Host != "" {
						hosts = append(hosts, r.Host)
					}
				}
				h := strings.Join(hosts, ",")
				if h == "" {
					h = "*"
				}
				var addrs []string
				for _, a := range in.Status.LoadBalancer.Ingress {
					if a.IP != "" {
						addrs = append(addrs, a.IP)
					} else if a.Hostname != "" {
						addrs = append(addrs, a.Hostname)
					}
				}
				ports := "80"
				if len(in.Spec.TLS) > 0 {
					ports = "80, 443"
				}
				return cellsOf(in.Name, cls, h, strings.Join(addrs, ","), ports, age(in.CreationTimestamp, now))
			}
	}
	return []column{nameCol(), ageCol()}, func(o runtime.Object, now time.Time) []any {
		return cellsOf(nameOf(o), age(createdOf(o), now))
	}
}

// podStatus is the STATUS kubectl prints for a pod.
func podStatus(p *corev1.Pod) string {
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}
	initializing := false
	for i, c := range p.Status.InitContainerStatuses {
		switch {
		case c.State.Terminated != nil && c.State.Terminated.ExitCode == 0:
			continue
		case c.State.Terminated != nil:
			if c.State.Terminated.Reason != "" {
				reason = "Init:" + c.State.Terminated.Reason
			} else {
				reason = fmt.Sprintf("Init:ExitCode:%d", c.State.Terminated.ExitCode)
			}
			initializing = true
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + c.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
			initializing = true
		}
		break
	}
	if !initializing {
		hasRunning := false
		for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
			c := p.Status.ContainerStatuses[i]
			switch {
			case c.State.Waiting != nil && c.State.Waiting.Reason != "":
				reason = c.State.Waiting.Reason
			case c.State.Terminated != nil && c.State.Terminated.Reason != "":
				reason = c.State.Terminated.Reason
			case c.State.Terminated != nil:
				reason = fmt.Sprintf("ExitCode:%d", c.State.Terminated.ExitCode)
			case c.Ready && c.State.Running != nil:
				hasRunning = true
			}
		}
		if reason == "Completed" && hasRunning {
			reason = "Running"
		}
		if reason == "Running" {
			ready := false
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					ready = true
				}
			}
			if !ready && len(p.Status.Conditions) > 0 {
				reason = "NotReady"
			}
		}
	}
	if p.DeletionTimestamp != nil {
		reason = "Terminating"
	}
	return reason
}

// podRestarts is the RESTARTS column: the count and, after a restart,
// how long ago the last one was.
func podRestarts(p *corev1.Pod, now time.Time) string {
	var n int32
	var last time.Time
	for _, c := range p.Status.ContainerStatuses {
		n += c.RestartCount
		if c.LastTerminationState.Terminated != nil && c.LastTerminationState.Terminated.FinishedAt.After(last) {
			last = c.LastTerminationState.Terminated.FinishedAt.Time
		}
	}
	s := strconv.Itoa(int(n))
	if n > 0 && !last.IsZero() {
		s += " (" + duration.HumanDuration(now.Sub(last)) + " ago)"
	}
	return s
}

// encodeTable writes a Table as JSON.
func encodeTable(t metav1.Table) []byte {
	b, _ := json.Marshal(t)
	return b
}
