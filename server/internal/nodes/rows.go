// Package nodes turns Kubernetes nodes and the pods bound to them into the
// rows the Nodes tab shows. Everything here is a pure function of its
// inputs, so it is tested without a cluster.
package nodes

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

// Usage is what metrics-server reports a node is using.
type Usage struct {
	CPUMilli    int64
	MemoryBytes int64
}

// Row is one node as the Nodes tab shows it.
type Row struct {
	Name       string            `json:"name"`
	Role       string            `json:"role"`
	Roles      []string          `json:"roles"`
	Status     string            `json:"status"`
	Tone       string            `json:"tone"`
	StatusTip  string            `json:"statusTip"`
	Cordoned   bool              `json:"cordoned"`
	Problems   []Problem         `json:"problems"`
	Errors     int               `json:"errors"`
	Warnings   int               `json:"warnings"`
	CPU        Resource          `json:"cpu"`
	Memory     Resource          `json:"memory"`
	Pods       PodCounts         `json:"pods"`
	Taints     []Taint           `json:"taints"`
	Zone       string            `json:"zone"`
	Kubelet    string            `json:"kubelet"`
	Created    time.Time         `json:"created"`
	InternalIP string            `json:"internalIP"`
	OS         string            `json:"os"`
	Kernel     string            `json:"kernel"`
	Runtime    string            `json:"runtime"`
	Arch       string            `json:"arch"`
	Labels     map[string]string `json:"labels"`
	Conditions []Condition       `json:"conditions"`
}

// Resource is a node's CPU (in millicores) or memory (in bytes).
type Resource struct {
	Requested   int64 `json:"requested"`
	Limits      int64 `json:"limits"`
	Allocatable int64 `json:"allocatable"`
	Capacity    int64 `json:"capacity"`
	// Used is nil when metrics-server has no reading for the node.
	Used *int64 `json:"used"`
}

// PodCounts counts the pods bound to a node.
type PodCounts struct {
	// Running is every pod bound to the node that has not finished; each
	// one takes a pod slot.
	Running  int `json:"running"`
	Capacity int `json:"capacity"`
	// NotReady is Failing + Starting + Unready.
	NotReady int `json:"notReady"`
	// Failing pods cannot start or keep crashing.
	Failing int `json:"failing"`
	// Starting pods are still being created after the grace period.
	Starting int `json:"starting"`
	// Unready pods run but fail their readiness checks.
	Unready int `json:"unready"`
	// Completed pods have finished (succeeded or failed) and take no slot.
	Completed int `json:"completed"`
}

// Problem is something about a node that needs attention.
type Problem struct {
	Title    string  `json:"title"`
	Severity string  `json:"severity"` // "error" or "warning"
	Summary  string  `json:"summary"`
	Entries  []Entry `json:"entries"`
}

// Entry is one piece of evidence for a problem.
type Entry struct {
	Reason  string     `json:"reason"`
	Message string     `json:"message"`
	Object  string     `json:"object,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
}

// Condition is a node condition.
type Condition struct {
	Type    string     `json:"type"`
	Status  string     `json:"status"`
	Reason  string     `json:"reason,omitempty"`
	Message string     `json:"message,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
}

// Taint is a node taint.
type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

const (
	// SeverityError and SeverityWarning grade problems.
	SeverityError   = "error"
	SeverityWarning = "warning"

	// StartGrace is how long a pod may take to start, or to pass its
	// readiness checks, before it counts as a problem. Rollouts would
	// otherwise flash problems on every node they touch.
	StartGrace = 2 * time.Minute

	// maxPodEntries caps the pods listed under one problem.
	maxPodEntries = 10
)

// failingReasons are container states that will not resolve by waiting.
var failingReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"ErrImageNeverPull":          true,
	"ImageInspectError":          true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"ContainerCannotRun":         true,
	"PreStartHookError":          true,
	"PostStartHookError":         true,
}

// BuildRow computes a node's row. pods are the pods bound to the node;
// usage is nil when metrics-server has no reading for it.
func BuildRow(node *corev1.Node, pods []*corev1.Pod, usage *Usage, now time.Time) Row {
	r := Row{
		Name:       node.Name,
		Roles:      roles(node.Labels),
		Cordoned:   node.Spec.Unschedulable,
		Zone:       zone(node.Labels),
		Kubelet:    node.Status.NodeInfo.KubeletVersion,
		Created:    node.CreationTimestamp.UTC(),
		InternalIP: address(node, corev1.NodeInternalIP),
		OS:         node.Status.NodeInfo.OSImage,
		Kernel:     node.Status.NodeInfo.KernelVersion,
		Runtime:    node.Status.NodeInfo.ContainerRuntimeVersion,
		Arch:       node.Status.NodeInfo.Architecture,
		Labels:     node.Labels,
		Taints:     []Taint{},
		Conditions: []Condition{},
		Problems:   []Problem{},
	}
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	r.Role = "worker"
	for _, role := range r.Roles {
		if role == "control-plane" || role == "master" {
			r.Role = "control-plane"
		}
	}
	for _, t := range node.Spec.Taints {
		r.Taints = append(r.Taints, Taint{Key: t.Key, Value: t.Value, Effect: string(t.Effect)})
	}
	for _, c := range node.Status.Conditions {
		r.Conditions = append(r.Conditions, Condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, Since: timePtr(c.LastTransitionTime.Time),
		})
	}

	alloc, capacity := node.Status.Allocatable, node.Status.Capacity
	r.CPU = Resource{Allocatable: alloc.Cpu().MilliValue(), Capacity: capacity.Cpu().MilliValue()}
	r.Memory = Resource{Allocatable: alloc.Memory().Value(), Capacity: capacity.Memory().Value()}
	r.Pods.Capacity = int(alloc.Pods().Value())
	if usage != nil {
		cpu, mem := usage.CPUMilli, usage.MemoryBytes
		r.CPU.Used, r.Memory.Used = &cpu, &mem
	}

	var unwell []podState
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			r.Pods.Completed++
			continue
		}
		r.Pods.Running++
		// As the scheduler counts them: with in-place resizes, the larger of
		// what is asked for and what is allocated or applied.
		reqs := resourcehelper.PodRequests(p, resourcehelper.PodResourcesOptions{UseStatusResources: true})
		lims := resourcehelper.PodLimits(p, resourcehelper.PodResourcesOptions{UseStatusResources: true})
		r.CPU.Requested += reqs.Cpu().MilliValue()
		r.Memory.Requested += reqs.Memory().Value()
		r.CPU.Limits += lims.Cpu().MilliValue()
		r.Memory.Limits += lims.Memory().Value()
		if s, bad := classify(p, now); bad {
			unwell = append(unwell, s)
			switch s.kind {
			case kindFailing:
				r.Pods.Failing++
			case kindStarting:
				r.Pods.Starting++
			default:
				r.Pods.Unready++
			}
		}
	}
	r.Pods.NotReady = r.Pods.Failing + r.Pods.Starting + r.Pods.Unready

	r.Status, r.Tone, r.StatusTip = status(node, now)
	r.Problems = append(r.Problems, conditionProblems(node, now)...)
	if node.Spec.Unschedulable {
		r.Problems = append(r.Problems, Problem{
			Title:    "Cordoned",
			Severity: SeverityWarning,
			Summary:  fmt.Sprintf("New pods are not scheduled here. %s still running on it until the node is drained.", countWord(r.Pods.Running, "pod is", "pods are")),
			Entries:  []Entry{},
		})
	}
	if len(unwell) > 0 {
		r.Problems = append(r.Problems, podProblem(unwell, r.Pods))
	}
	for _, p := range r.Problems {
		if p.Severity == SeverityError {
			r.Errors++
		} else {
			r.Warnings++
		}
	}
	return r
}

func status(node *corev1.Node, now time.Time) (text, tone, tip string) {
	ready := condition(node, corev1.NodeReady)
	switch {
	case ready == nil:
		return "Unknown", "err", "The node has no Ready condition yet."
	case ready.Status == corev1.ConditionTrue && node.Spec.Unschedulable:
		return "Cordoned", "warn", "Ready, SchedulingDisabled: new pods are not scheduled here."
	case ready.Status == corev1.ConditionTrue:
		return "Ready", "ok", "Ready since " + stamp(ready.LastTransitionTime.Time) + "."
	case ready.Status == corev1.ConditionUnknown:
		tip = "The kubelet stopped posting status " + ago(ready.LastTransitionTime.Time, now) + "."
		if node.Spec.Unschedulable {
			tip += " It is also cordoned."
		}
		return "Unknown", "err", tip
	default:
		tip = "Not ready since " + stamp(ready.LastTransitionTime.Time) + "."
		if ready.Message != "" {
			tip += " " + ready.Message
		}
		if node.Spec.Unschedulable {
			tip += " It is also cordoned."
		}
		return "Not ready", "err", tip
	}
}

func conditionProblems(node *corev1.Node, now time.Time) []Problem {
	var out []Problem
	if c := condition(node, corev1.NodeReady); c != nil && c.Status != corev1.ConditionTrue {
		p := Problem{Severity: SeverityError, Entries: []Entry{conditionEntry(c)}}
		if c.Status == corev1.ConditionUnknown {
			p.Title = "Kubelet stopped reporting"
			p.Summary = "The node controller has not heard from the kubelet for " + dur(now.Sub(c.LastTransitionTime.Time)) + ". Pods on it are not being managed."
		} else {
			p.Title = "Not ready"
			p.Summary = "The kubelet reports the node is not ready, so no new pods start here."
		}
		out = append(out, p)
	}
	pressure := []struct {
		t       corev1.NodeConditionType
		title   string
		summary string
	}{
		{corev1.NodeMemoryPressure, "Memory pressure", "Memory is running low. The kubelet evicts pods to recover and new pods are not scheduled here."},
		{corev1.NodeDiskPressure, "Disk pressure", "Disk space or inodes are running low. The kubelet evicts pods and removes unused images to recover."},
		{corev1.NodePIDPressure, "PID pressure", "Too many processes are running. The kubelet evicts pods to recover."},
	}
	for _, x := range pressure {
		if c := condition(node, x.t); c != nil && c.Status == corev1.ConditionTrue {
			out = append(out, Problem{Title: x.title, Severity: SeverityWarning, Summary: x.summary, Entries: []Entry{conditionEntry(c)}})
		}
	}
	if c := condition(node, corev1.NodeNetworkUnavailable); c != nil && c.Status == corev1.ConditionTrue {
		out = append(out, Problem{Title: "Network unavailable", Severity: SeverityError, Summary: "The node's network is not configured, so pods on it cannot reach the rest of the cluster.", Entries: []Entry{conditionEntry(c)}})
	}
	return out
}

func conditionEntry(c *corev1.NodeCondition) Entry {
	reason := c.Reason
	if reason == "" {
		reason = string(c.Type) + " is " + string(c.Status)
	}
	return Entry{Reason: reason, Message: c.Message, Object: "condition " + string(c.Type), Since: timePtr(c.LastTransitionTime.Time)}
}

type podKind int

const (
	kindFailing podKind = iota
	kindStarting
	kindUnready
)

type podState struct {
	kind     podKind
	name     string
	reason   string
	message  string
	since    time.Time
	restarts int32
}

// classify decides whether a pod that has not finished is a problem.
func classify(p *corev1.Pod, now time.Time) (podState, bool) {
	s := podState{name: p.Namespace + "/" + p.Name}
	for _, cs := range p.Status.ContainerStatuses {
		s.restarts += cs.RestartCount
	}
	// The deletion timestamp is the deadline the pod had to stop by.
	if p.DeletionTimestamp != nil {
		deadline := p.DeletionTimestamp.Time
		if now.Sub(deadline) < StartGrace {
			return s, false
		}
		s.kind, s.reason, s.since = kindUnready, "Terminating", deadline
		s.message = "Still terminating " + dur(now.Sub(deadline)) + " after its deletion deadline."
		return s, true
	}
	// A failing container makes the pod a problem at once.
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if w := cs.State.Waiting; w != nil && failingReasons[w.Reason] {
				s.kind, s.reason = kindFailing, w.Reason
				s.message = containerMessage(cs, w.Message)
				s.since = startOf(p)
				return s, true
			}
			// Between back-offs a crashing container shows as terminated
			// with an error before the kubelet restarts it.
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 && p.Spec.RestartPolicy != corev1.RestartPolicyNever {
				s.kind, s.reason = kindFailing, "CrashLoopBackOff"
				s.message = containerMessage(cs, fmt.Sprintf("It exited with code %d (%s) and is being restarted.", t.ExitCode, orDefault(t.Reason, "Error")))
				s.since = startOf(p)
				return s, true
			}
		}
	}
	ready := podCondition(p, corev1.PodReady)
	if ready != nil && ready.Status == corev1.ConditionTrue {
		return s, false
	}
	since := startOf(p)
	if ready != nil && !ready.LastTransitionTime.IsZero() {
		since = ready.LastTransitionTime.Time
	}
	if now.Sub(since) < StartGrace {
		return s, false
	}
	s.since = since
	if p.Status.Phase == corev1.PodPending {
		s.kind, s.reason = kindStarting, "Pending"
		for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
			for _, cs := range list {
				if w := cs.State.Waiting; w != nil && w.Reason != "" {
					s.reason, s.message = w.Reason, containerMessage(cs, w.Message)
					return s, true
				}
			}
		}
		s.message = "Not started after " + dur(now.Sub(since)) + "."
		return s, true
	}
	s.kind, s.reason = kindUnready, "NotReady"
	s.message = "Running but not ready for " + dur(now.Sub(since)) + "."
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			s.message = "Container " + cs.Name + " is running but not ready, for " + dur(now.Sub(since)) + "."
			break
		}
	}
	return s, true
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func containerMessage(cs corev1.ContainerStatus, msg string) string {
	out := "Container " + cs.Name
	if cs.RestartCount > 0 {
		out += fmt.Sprintf(" has restarted %s", countWord(int(cs.RestartCount), "time", "times"))
		if t := cs.LastTerminationState.Terminated; t != nil {
			out += fmt.Sprintf("; it last exited with code %d (%s)", t.ExitCode, t.Reason)
		}
	}
	out += "."
	if msg != "" {
		out += " " + msg
	}
	return out
}

func podProblem(unwell []podState, counts PodCounts) Problem {
	sort.SliceStable(unwell, func(i, j int) bool {
		if unwell[i].kind != unwell[j].kind {
			return unwell[i].kind < unwell[j].kind
		}
		return unwell[i].name < unwell[j].name
	})
	var parts []string
	if counts.Failing > 0 {
		parts = append(parts, fmt.Sprintf("%d failing", counts.Failing))
	}
	if counts.Starting > 0 {
		parts = append(parts, fmt.Sprintf("%d still starting", counts.Starting))
	}
	if counts.Unready > 0 {
		parts = append(parts, fmt.Sprintf("%d failing readiness checks", counts.Unready))
	}
	p := Problem{
		Title:    countWord(len(unwell), "pod not ready", "pods not ready"),
		Severity: SeverityWarning,
		Summary:  capitalize(strings.Join(parts, ", ")) + ".",
		Entries:  []Entry{},
	}
	if counts.Failing > 0 {
		p.Severity = SeverityError
	}
	for i, s := range unwell {
		if i == maxPodEntries {
			p.Summary += fmt.Sprintf(" The first %d are listed.", maxPodEntries)
			break
		}
		p.Entries = append(p.Entries, Entry{Reason: s.reason, Message: s.message, Object: "pod " + s.name, Since: timePtr(s.since)})
	}
	return p
}

func roles(labels map[string]string) []string {
	out := []string{}
	for k, v := range labels {
		if role, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && role != "" {
			out = append(out, role)
		} else if k == "kubernetes.io/role" && v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func zone(labels map[string]string) string {
	if z := labels[corev1.LabelTopologyZone]; z != "" {
		return z
	}
	return labels[corev1.LabelFailureDomainBetaZone]
}

func address(node *corev1.Node, t corev1.NodeAddressType) string {
	for _, a := range node.Status.Addresses {
		if a.Type == t {
			return a.Address
		}
	}
	return ""
}

func condition(node *corev1.Node, t corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == t {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}

func podCondition(p *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == t {
			return &p.Status.Conditions[i]
		}
	}
	return nil
}

func startOf(p *corev1.Pod) time.Time {
	if p.Status.StartTime != nil {
		return p.Status.StartTime.Time
	}
	return p.CreationTimestamp.Time
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "an unknown time"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "a while ago"
	}
	return dur(now.Sub(t)) + " ago"
}

// dur formats a duration the way the prototype does: 45s, 12m, 3h 5m, 2d 4h.
func dur(d time.Duration) string {
	if d < time.Minute {
		s := int(d.Round(time.Second) / time.Second)
		if s < 1 {
			s = 1
		}
		return fmt.Sprintf("%ds", s)
	}
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	h, m := m/60, m%60
	if h < 24 {
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days, h := h/24, h%24
	if days < 10 && h > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}

func countWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
