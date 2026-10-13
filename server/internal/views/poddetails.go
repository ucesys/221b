package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/ucesys/221b/server/internal/kube"
)

// PodDetails is everything the pod drawer shows.
type PodDetails struct {
	PodRow
	PodIP          string            `json:"podIP,omitempty"`
	HostIP         string            `json:"hostIP,omitempty"`
	Priority       *int32            `json:"priority,omitempty"`
	PriorityClass  string            `json:"priorityClass,omitempty"`
	ServiceAccount string            `json:"serviceAccount,omitempty"`
	RestartPolicy  string            `json:"restartPolicy,omitempty"`
	HostNetwork    bool              `json:"hostNetwork"`
	Owners         []Owner           `json:"owners"`
	Conditions     []Condition       `json:"conditions"`
	NodeSelector   map[string]string `json:"nodeSelector"`
	Tolerations    []string          `json:"tolerations"`
	Affinity       []string          `json:"affinity"`
	Containers     []ContainerDetail `json:"containers"`
	Volumes        []Volume          `json:"volumes"`
	Annotations    map[string]string `json:"annotations"`
	Events         []Event           `json:"events"`
	// Services selecting the pod, by name.
	Services []string `json:"services"`
	Message  string   `json:"message,omitempty"`
	// Unschedulable carries the scheduler's message when the pod has no node.
	Unschedulable string `json:"unschedulable,omitempty"`
}

// Condition is a pod condition.
type Condition struct {
	Type    string     `json:"type"`
	Status  string     `json:"status"`
	Reason  string     `json:"reason,omitempty"`
	Message string     `json:"message,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
}

// ContainerDetail is one container of a pod, init or app.
type ContainerDetail struct {
	Name            string        `json:"name"`
	Init            bool          `json:"init"`
	Sidecar         bool          `json:"sidecar"`
	Image           string        `json:"image"`
	ImageID         string        `json:"imageID,omitempty"`
	PullPolicy      string        `json:"pullPolicy,omitempty"`
	Command         []string      `json:"command,omitempty"`
	Args            []string      `json:"args,omitempty"`
	WorkingDir      string        `json:"workingDir,omitempty"`
	Ports           []Port        `json:"ports"`
	Requests        Quantities    `json:"requests"`
	Limits          Quantities    `json:"limits"`
	SecurityContext []string      `json:"securityContext"`
	Probes          []string      `json:"probes"`
	Mounts          []Mount       `json:"mounts"`
	Env             []EnvVar      `json:"env"`
	EnvFrom         []EnvFrom     `json:"envFrom"`
	State           *ContainerRun `json:"state,omitempty"`
	LastState       *ContainerRun `json:"lastState,omitempty"`
	Ready           bool          `json:"ready"`
	Restarts        int32         `json:"restarts"`
}

// Port is a container port.
type Port struct {
	Name     string `json:"name,omitempty"`
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
	HostPort int32  `json:"hostPort,omitempty"`
}

// Quantities are a container's CPU (millicores), memory and ephemeral
// storage (bytes); 0 means none set.
type Quantities struct {
	CPU       int64 `json:"cpu"`
	Memory    int64 `json:"memory"`
	Ephemeral int64 `json:"ephemeral"`
}

// Mount is a volume mounted in a container.
type Mount struct {
	Container string `json:"container,omitempty"`
	Path      string `json:"path"`
	Volume    string `json:"volume"`
	ReadOnly  bool   `json:"readOnly"`
	SubPath   string `json:"subPath,omitempty"`
}

// EnvVar is one environment variable. A value from a Secret is never
// carried, only which Secret and key it comes from (X-01).
type EnvVar struct {
	Name string `json:"name"`
	// Value is the literal value, when the variable has one.
	Value string `json:"value,omitempty"`
	// Source is "Secret", "ConfigMap", "field" or "resource" for a
	// variable that takes its value from elsewhere.
	Source   string `json:"source,omitempty"`
	From     string `json:"from,omitempty"`
	Key      string `json:"key,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// EnvFrom is a whole Secret or ConfigMap loaded as variables.
type EnvFrom struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// ContainerRun is a container's state: waiting, running or terminated.
type ContainerRun struct {
	Kind     string     `json:"kind"`
	Reason   string     `json:"reason,omitempty"`
	Message  string     `json:"message,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
	ExitCode *int32     `json:"exitCode,omitempty"`
	Signal   int32      `json:"signal,omitempty"`
}

// Volume is one volume of a pod and where it is mounted.
type Volume struct {
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	Source string  `json:"source"`
	Mounts []Mount `json:"mounts"`
	Claim  *Claim  `json:"claim,omitempty"`
}

// Claim is a PersistentVolumeClaim a pod mounts.
type Claim struct {
	Name         string   `json:"name"`
	Phase        string   `json:"phase"`
	Volume       string   `json:"volume,omitempty"`
	StorageClass string   `json:"storageClass,omitempty"`
	AccessModes  []string `json:"accessModes"`
	Requested    int64    `json:"requested"`
	Capacity     int64    `json:"capacity"`
}

// Event is one event about an object.
type Event struct {
	Type    string     `json:"type"`
	Reason  string     `json:"reason"`
	Message string     `json:"message"`
	Count   int32      `json:"count"`
	First   *time.Time `json:"first,omitempty"`
	Last    *time.Time `json:"last,omitempty"`
	Source  string     `json:"source,omitempty"`
}

// BuildPodDetails computes the drawer's data for a pod.
func BuildPodDetails(src Source, p *corev1.Pod, now time.Time) PodDetails {
	d := PodDetails{
		PodRow:         BuildPodRow(src, p, now),
		PodIP:          p.Status.PodIP,
		HostIP:         p.Status.HostIP,
		Priority:       p.Spec.Priority,
		PriorityClass:  p.Spec.PriorityClassName,
		ServiceAccount: p.Spec.ServiceAccountName,
		RestartPolicy:  string(p.Spec.RestartPolicy),
		HostNetwork:    p.Spec.HostNetwork,
		Owners:         ownerChain(src, p.Namespace, p.OwnerReferences, 4),
		Conditions:     []Condition{},
		NodeSelector:   p.Spec.NodeSelector,
		Tolerations:    []string{},
		Affinity:       affinityText(p.Spec.Affinity),
		Containers:     []ContainerDetail{},
		Volumes:        []Volume{},
		Annotations:    p.Annotations,
		Events:         []Event{},
		Services:       []string{},
		Message:        p.Status.Message,
	}
	if d.Owners == nil {
		d.Owners = []Owner{}
	}
	if d.NodeSelector == nil {
		d.NodeSelector = map[string]string{}
	}
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	for _, c := range p.Status.Conditions {
		d.Conditions = append(d.Conditions, Condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(&c.LastTransitionTime)})
	}
	if p.Spec.NodeName == "" {
		if c := podCondition(p, corev1.PodScheduled); c != nil && c.Status != corev1.ConditionTrue {
			d.Unschedulable = c.Message
			if d.Unschedulable == "" {
				d.Unschedulable = c.Reason
			}
		}
	}
	for _, t := range p.Spec.Tolerations {
		d.Tolerations = append(d.Tolerations, tolerationText(t))
	}
	statuses := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.InitContainerStatuses {
		statuses["init/"+cs.Name] = cs
	}
	for _, cs := range p.Status.ContainerStatuses {
		statuses[cs.Name] = cs
	}
	for _, c := range p.Spec.InitContainers {
		d.Containers = append(d.Containers, containerDetail(c, statuses["init/"+c.Name], true))
	}
	for _, c := range p.Spec.Containers {
		d.Containers = append(d.Containers, containerDetail(c, statuses[c.Name], false))
	}
	d.Volumes = volumes(src, p)
	d.Events = eventsFor(src, string(p.UID))
	d.Services = servicesSelecting(src, p)
	return d
}

func containerDetail(c corev1.Container, cs corev1.ContainerStatus, init bool) ContainerDetail {
	out := ContainerDetail{
		Name:            c.Name,
		Init:            init,
		Sidecar:         init && c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways,
		Image:           c.Image,
		ImageID:         cs.ImageID,
		PullPolicy:      string(c.ImagePullPolicy),
		Command:         c.Command,
		Args:            c.Args,
		WorkingDir:      c.WorkingDir,
		Ports:           []Port{},
		Requests:        quantities(c.Resources.Requests),
		Limits:          quantities(c.Resources.Limits),
		SecurityContext: securityText(c.SecurityContext),
		Probes:          []string{},
		Mounts:          []Mount{},
		Env:             []EnvVar{},
		EnvFrom:         []EnvFrom{},
		State:           containerRun(cs.State),
		LastState:       containerRun(cs.LastTerminationState),
		Ready:           cs.Ready,
		Restarts:        cs.RestartCount,
	}
	for _, p := range c.Ports {
		out.Ports = append(out.Ports, Port{Name: p.Name, Port: p.ContainerPort, Protocol: string(p.Protocol), HostPort: p.HostPort})
	}
	for name, probe := range map[string]*corev1.Probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe, "startup": c.StartupProbe} {
		if probe != nil {
			out.Probes = append(out.Probes, name+": "+probeText(probe))
		}
	}
	sort.Strings(out.Probes)
	for _, m := range c.VolumeMounts {
		out.Mounts = append(out.Mounts, Mount{Path: m.MountPath, Volume: m.Name, ReadOnly: m.ReadOnly, SubPath: m.SubPath})
	}
	for _, e := range c.Env {
		out.Env = append(out.Env, envVar(e))
	}
	for _, e := range c.EnvFrom {
		switch {
		case e.SecretRef != nil:
			out.EnvFrom = append(out.EnvFrom, EnvFrom{Source: "Secret", Name: e.SecretRef.Name, Prefix: e.Prefix, Optional: e.SecretRef.Optional != nil && *e.SecretRef.Optional})
		case e.ConfigMapRef != nil:
			out.EnvFrom = append(out.EnvFrom, EnvFrom{Source: "ConfigMap", Name: e.ConfigMapRef.Name, Prefix: e.Prefix, Optional: e.ConfigMapRef.Optional != nil && *e.ConfigMapRef.Optional})
		}
	}
	return out
}

// envVar describes a variable without ever carrying a Secret's value.
func envVar(e corev1.EnvVar) EnvVar {
	v := EnvVar{Name: e.Name}
	if e.ValueFrom == nil {
		v.Value = e.Value
		return v
	}
	switch f := e.ValueFrom; {
	case f.SecretKeyRef != nil:
		v.Source, v.From, v.Key = "Secret", f.SecretKeyRef.Name, f.SecretKeyRef.Key
		v.Optional = f.SecretKeyRef.Optional != nil && *f.SecretKeyRef.Optional
	case f.ConfigMapKeyRef != nil:
		v.Source, v.From, v.Key = "ConfigMap", f.ConfigMapKeyRef.Name, f.ConfigMapKeyRef.Key
		v.Optional = f.ConfigMapKeyRef.Optional != nil && *f.ConfigMapKeyRef.Optional
	case f.FieldRef != nil:
		v.Source, v.From = "field", f.FieldRef.FieldPath
	case f.ResourceFieldRef != nil:
		v.Source, v.From = "resource", f.ResourceFieldRef.Resource
		if f.ResourceFieldRef.ContainerName != "" {
			v.From = f.ResourceFieldRef.ContainerName + ": " + v.From
		}
	}
	return v
}

func quantities(l corev1.ResourceList) Quantities {
	q := Quantities{}
	if l == nil {
		return q
	}
	q.CPU = l.Cpu().MilliValue()
	q.Memory = l.Memory().Value()
	if e, ok := l[corev1.ResourceEphemeralStorage]; ok {
		q.Ephemeral = e.Value()
	}
	return q
}

func securityText(sc *corev1.SecurityContext) []string {
	out := []string{}
	if sc == nil {
		return out
	}
	if sc.RunAsUser != nil {
		out = append(out, fmt.Sprintf("runAsUser %d", *sc.RunAsUser))
	}
	if sc.RunAsGroup != nil {
		out = append(out, fmt.Sprintf("runAsGroup %d", *sc.RunAsGroup))
	}
	if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot {
		out = append(out, "runAsNonRoot")
	}
	if sc.Privileged != nil && *sc.Privileged {
		out = append(out, "privileged")
	}
	if sc.ReadOnlyRootFilesystem != nil && *sc.ReadOnlyRootFilesystem {
		out = append(out, "readOnlyRootFilesystem")
	}
	if sc.AllowPrivilegeEscalation != nil && !*sc.AllowPrivilegeEscalation {
		out = append(out, "no privilege escalation")
	}
	if sc.Capabilities != nil {
		if len(sc.Capabilities.Drop) > 0 {
			var d []string
			for _, c := range sc.Capabilities.Drop {
				d = append(d, string(c))
			}
			out = append(out, "drop "+strings.Join(d, ", "))
		}
		if len(sc.Capabilities.Add) > 0 {
			var a []string
			for _, c := range sc.Capabilities.Add {
				a = append(a, string(c))
			}
			out = append(out, "add "+strings.Join(a, ", "))
		}
	}
	if sc.SeccompProfile != nil {
		out = append(out, "seccomp "+string(sc.SeccompProfile.Type))
	}
	return out
}

func probeText(p *corev1.Probe) string {
	var how string
	switch {
	case p.HTTPGet != nil:
		how = fmt.Sprintf("HTTP GET %s on port %s", p.HTTPGet.Path, p.HTTPGet.Port.String())
	case p.TCPSocket != nil:
		how = "TCP port " + p.TCPSocket.Port.String()
	case p.Exec != nil:
		how = "exec " + strings.Join(p.Exec.Command, " ")
	case p.GRPC != nil:
		how = fmt.Sprintf("gRPC on port %d", p.GRPC.Port)
	default:
		how = "probe"
	}
	period := p.PeriodSeconds
	if period == 0 {
		period = 10
	}
	s := fmt.Sprintf("%s every %ds", how, period)
	if p.InitialDelaySeconds > 0 {
		s += fmt.Sprintf(", after %ds", p.InitialDelaySeconds)
	}
	if p.FailureThreshold > 1 {
		s += fmt.Sprintf(", fails after %d", p.FailureThreshold)
	}
	return s
}

func containerRun(s corev1.ContainerState) *ContainerRun {
	switch {
	case s.Waiting != nil:
		return &ContainerRun{Kind: "waiting", Reason: s.Waiting.Reason, Message: s.Waiting.Message}
	case s.Running != nil:
		return &ContainerRun{Kind: "running", Since: timePtr(&s.Running.StartedAt)}
	case s.Terminated != nil:
		t := s.Terminated
		code := t.ExitCode
		return &ContainerRun{Kind: "terminated", Reason: t.Reason, Message: t.Message, Since: timePtr(&t.StartedAt), Finished: timePtr(&t.FinishedAt), ExitCode: &code, Signal: t.Signal}
	}
	return nil
}

func tolerationText(t corev1.Toleration) string {
	s := t.Key
	if s == "" {
		s = "any key"
	}
	if t.Operator == corev1.TolerationOpExists || t.Value == "" {
		if t.Key != "" {
			s += " (any value)"
		}
	} else {
		s += "=" + t.Value
	}
	if t.Effect != "" {
		s += ":" + string(t.Effect)
	}
	if t.TolerationSeconds != nil {
		s += fmt.Sprintf(" · evicted after %ds", *t.TolerationSeconds)
	}
	return s
}

func affinityText(a *corev1.Affinity) []string {
	out := []string{}
	if a == nil {
		return out
	}
	if a.NodeAffinity != nil {
		if r := a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution; r != nil {
			for _, t := range r.NodeSelectorTerms {
				for _, e := range t.MatchExpressions {
					out = append(out, "node must have "+selectorExprText(e.Key, string(e.Operator), e.Values))
				}
			}
		}
		for _, p := range a.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			for _, e := range p.Preference.MatchExpressions {
				out = append(out, fmt.Sprintf("node preferably has %s (weight %d)", selectorExprText(e.Key, string(e.Operator), e.Values), p.Weight))
			}
		}
	}
	if a.PodAffinity != nil {
		for _, t := range a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			out = append(out, "must run with pods "+termText(t))
		}
		for _, p := range a.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			out = append(out, fmt.Sprintf("preferably with pods %s (weight %d)", termText(p.PodAffinityTerm), p.Weight))
		}
	}
	if a.PodAntiAffinity != nil {
		for _, t := range a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			out = append(out, "must not run with pods "+termText(t))
		}
		for _, p := range a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			out = append(out, fmt.Sprintf("preferably not with pods %s (weight %d)", termText(p.PodAffinityTerm), p.Weight))
		}
	}
	return out
}

func selectorExprText(key, op string, values []string) string {
	switch op {
	case "In":
		return key + " in " + strings.Join(values, ", ")
	case "NotIn":
		return key + " not in " + strings.Join(values, ", ")
	case "Exists":
		return key
	case "DoesNotExist":
		return "no " + key
	}
	return key + " " + op + " " + strings.Join(values, ", ")
}

func termText(t corev1.PodAffinityTerm) string {
	sel := "matching anything"
	if t.LabelSelector != nil {
		if s, err := metaSelector(t.LabelSelector); err == nil {
			sel = "matching " + s
		}
	}
	return sel + " per " + t.TopologyKey
}

// volumes lists a pod's volumes with their mounts and claims.
func volumes(src Source, p *corev1.Pod) []Volume {
	mounts := map[string][]Mount{}
	for _, cs := range [][]corev1.Container{p.Spec.InitContainers, p.Spec.Containers} {
		for _, c := range cs {
			for _, m := range c.VolumeMounts {
				mounts[m.Name] = append(mounts[m.Name], Mount{Container: c.Name, Path: m.MountPath, Volume: m.Name, ReadOnly: m.ReadOnly, SubPath: m.SubPath})
			}
		}
	}
	out := []Volume{}
	for _, v := range p.Spec.Volumes {
		vol := Volume{Name: v.Name, Mounts: mounts[v.Name]}
		if vol.Mounts == nil {
			vol.Mounts = []Mount{}
		}
		vol.Type, vol.Source = volumeSource(v.VolumeSource)
		if v.PersistentVolumeClaim != nil {
			if pvc, ok := src.Object("persistentvolumeclaims", p.Namespace, v.PersistentVolumeClaim.ClaimName).(*corev1.PersistentVolumeClaim); ok {
				vol.Claim = claimOf(pvc)
			} else {
				vol.Claim = &Claim{Name: v.PersistentVolumeClaim.ClaimName, Phase: "missing", AccessModes: []string{}}
			}
		}
		out = append(out, vol)
	}
	return out
}

func claimOf(pvc *corev1.PersistentVolumeClaim) *Claim {
	c := &Claim{Name: pvc.Name, Phase: string(pvc.Status.Phase), Volume: pvc.Spec.VolumeName, AccessModes: []string{}}
	if pvc.Spec.StorageClassName != nil {
		c.StorageClass = *pvc.Spec.StorageClassName
	}
	for _, m := range pvc.Spec.AccessModes {
		c.AccessModes = append(c.AccessModes, string(m))
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		c.Requested = q.Value()
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		c.Capacity = q.Value()
	}
	return c
}

func volumeSource(v corev1.VolumeSource) (kind, source string) {
	switch {
	case v.PersistentVolumeClaim != nil:
		return "PVC", v.PersistentVolumeClaim.ClaimName
	case v.ConfigMap != nil:
		return "ConfigMap", v.ConfigMap.Name
	case v.Secret != nil:
		return "Secret", v.Secret.SecretName
	case v.EmptyDir != nil:
		if v.EmptyDir.Medium == corev1.StorageMediumMemory {
			return "emptyDir", "in memory"
		}
		return "emptyDir", "on the node's disk"
	case v.HostPath != nil:
		return "hostPath", v.HostPath.Path
	case v.Projected != nil:
		var parts []string
		for _, s := range v.Projected.Sources {
			switch {
			case s.ServiceAccountToken != nil:
				parts = append(parts, "service account token")
			case s.ConfigMap != nil:
				parts = append(parts, "ConfigMap "+s.ConfigMap.Name)
			case s.Secret != nil:
				parts = append(parts, "Secret "+s.Secret.Name)
			case s.DownwardAPI != nil:
				parts = append(parts, "pod fields")
			case s.ClusterTrustBundle != nil:
				parts = append(parts, "cluster trust bundle")
			}
		}
		return "Projected", strings.Join(parts, " · ")
	case v.DownwardAPI != nil:
		return "DownwardAPI", "pod fields"
	case v.CSI != nil:
		return "CSI", v.CSI.Driver
	case v.NFS != nil:
		return "NFS", v.NFS.Server + ":" + v.NFS.Path
	case v.Ephemeral != nil:
		return "Ephemeral", "a claim made with the pod"
	case v.Image != nil:
		return "Image", v.Image.Reference
	}
	return "Volume", ""
}

// eventsFor lists the events about an object, newest first.
func eventsFor(src Source, uid string) []Event {
	out := []Event{}
	for _, o := range src.ByIndex("events", kube.IndexEventObject, uid) {
		e, ok := o.(*corev1.Event)
		if !ok {
			continue
		}
		out = append(out, eventOf(e))
	}
	sort.Slice(out, func(i, j int) bool { return eventTime(out[i]).After(eventTime(out[j])) })
	return out
}

func eventOf(e *corev1.Event) Event {
	ev := Event{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count, Source: e.Source.Component}
	if ev.Source == "" {
		ev.Source = e.ReportingController
	}
	if !e.FirstTimestamp.IsZero() {
		ev.First = timePtr(&e.FirstTimestamp)
	} else if !e.EventTime.IsZero() {
		t := e.EventTime.Time.UTC()
		ev.First = &t
	}
	if !e.LastTimestamp.IsZero() {
		ev.Last = timePtr(&e.LastTimestamp)
	} else if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		t := e.Series.LastObservedTime.Time.UTC()
		ev.Last = &t
	} else {
		ev.Last = ev.First
	}
	if ev.Count == 0 {
		ev.Count = 1
		if e.Series != nil {
			ev.Count = e.Series.Count
		}
	}
	return ev
}

func eventTime(e Event) time.Time {
	if e.Last != nil {
		return *e.Last
	}
	if e.First != nil {
		return *e.First
	}
	return time.Time{}
}

// servicesSelecting names the Services whose selector matches the pod.
func servicesSelecting(src Source, p *corev1.Pod) []string {
	out := []string{}
	for _, o := range src.Objects("services", p.Namespace) {
		s, ok := o.(*corev1.Service)
		if !ok || len(s.Spec.Selector) == 0 {
			continue
		}
		if labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(p.Labels)) {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out
}

func metaSelector(sel *metav1.LabelSelector) (string, error) {
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return "", err
	}
	return s.String(), nil
}
