// Package views builds the rows and details of the Phase 2 tabs from the
// cached objects: pods, namespaces, services and Helm releases. Like the
// nodes package, everything here is a pure function of its inputs.
package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	resourcehelper "k8s.io/component-helpers/resource"

	"github.com/ucesys/221b/server/internal/kube"
)

// Source is the cache the views read; *kube.Cluster implements it.
type Source interface {
	Objects(kind, namespace string) []runtime.Object
	Object(kind, namespace, name string) runtime.Object
	ByIndex(kind, index, value string) []runtime.Object
	Count(kind, namespace string) int
	Kinds() []kube.Kind
	SecretsRead() bool
}

// PodRow is one pod as the Pods tab shows it. CPU and memory in use are
// not part of the row: they change every refresh, so the store sends them
// apart from the rows.
type PodRow struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
	Node      string `json:"node"`
	// Status is what kubectl shows: Running, Pending, CrashLoopBackOff,
	// Completed, Terminating and so on.
	Status    string `json:"status"`
	Tone      string `json:"tone"`
	StatusTip string `json:"statusTip,omitempty"`
	Phase     string `json:"phase"`
	// Ready and Total count app containers. ReadyUnknown says Ready is
	// the last the node reported before it stopped reporting.
	Ready        int   `json:"ready"`
	Total        int   `json:"total"`
	ReadyUnknown bool  `json:"readyUnknown,omitempty"`
	Restarts     int32 `json:"restarts"`
	// LastRestart is when a container last restarted, and LastTermination
	// why it stopped: "Error, exit 1", "OOMKilled".
	LastRestart     *time.Time `json:"lastRestart,omitempty"`
	LastTermination string     `json:"lastTermination,omitempty"`
	// Requests and limits, in millicores and bytes; 0 means none set.
	CPURequest    int64 `json:"cpuRequest"`
	CPULimit      int64 `json:"cpuLimit"`
	MemoryRequest int64 `json:"memoryRequest"`
	MemoryLimit   int64 `json:"memoryLimit"`
	// Started is the pod's start time; Created when it was made.
	Started *time.Time `json:"started,omitempty"`
	Created time.Time  `json:"created"`
	// Owner is the controller that manages the pod, through ReplicaSets
	// to their Deployment.
	OwnerKind string `json:"ownerKind,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	// Release is the Helm release the pod belongs to, if any.
	Release string            `json:"release,omitempty"`
	QoS     string            `json:"qos,omitempty"`
	Labels  map[string]string `json:"labels"`
	// Done is true for pods that finished (Succeeded or Failed).
	Done bool `json:"done"`
}

// BuildPodRow computes a pod's row.
func BuildPodRow(src Source, p *corev1.Pod, now time.Time) PodRow {
	r := PodRow{
		Key:       p.Namespace + "/" + p.Name,
		Name:      p.Name,
		Namespace: p.Namespace,
		UID:       string(p.UID),
		Node:      p.Spec.NodeName,
		Phase:     string(p.Status.Phase),
		Created:   p.CreationTimestamp.UTC(),
		Started:   timePtr(p.Status.StartTime),
		Labels:    p.Labels,
		QoS:       string(p.Status.QOSClass),
		Release:   podRelease(p),
		Done:      p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed,
		Total:     len(p.Spec.Containers),
	}
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	reqs := resourcehelper.PodRequests(p, resourcehelper.PodResourcesOptions{UseStatusResources: true})
	lims := resourcehelper.PodLimits(p, resourcehelper.PodResourcesOptions{UseStatusResources: true})
	r.CPURequest, r.MemoryRequest = reqs.Cpu().MilliValue(), reqs.Memory().Value()
	r.CPULimit, r.MemoryLimit = lims.Cpu().MilliValue(), lims.Memory().Value()
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			r.Ready++
		}
		r.Restarts += cs.RestartCount
		if t := cs.LastTerminationState.Terminated; t != nil {
			if r.LastRestart == nil || t.FinishedAt.Time.After(*r.LastRestart) {
				at := t.FinishedAt.Time.UTC()
				r.LastRestart = &at
				r.LastTermination = terminationText(t)
			}
		}
	}
	r.Status, r.Tone, r.StatusTip = podStatus(p, now)
	// On a node that stopped reporting, the pod's status is the last its
	// kubelet sent: kubectl still says Running 1/1, but nobody knows.
	if !r.Done && p.DeletionTimestamp == nil && p.Spec.NodeName != "" {
		if n, _ := src.Object("nodes", "", p.Spec.NodeName).(*corev1.Node); n != nil {
			if c := nodeReady(n); c != nil && c.Status == corev1.ConditionUnknown {
				r.Status, r.Tone, r.ReadyUnknown = "Unknown", "err", true
				r.StatusTip = "Node " + n.Name + " stopped reporting; the pod's state is unknown."
			}
		}
	}
	chain := ownerChain(src, p.Namespace, p.OwnerReferences, 4)
	if len(chain) > 0 {
		owner := chain[len(chain)-1]
		r.OwnerKind, r.OwnerName = owner.Kind, owner.Name
	}
	if r.Release == "" {
		r.Release = releaseOwning(src, p.Namespace, chain)
	}
	return r
}

// releaseOwning names the Helm release whose manifest creates one of the
// pod's owners, for charts that do not put Helm's labels on their pods.
func releaseOwning(src Source, namespace string, owners []Owner) string {
	if len(owners) == 0 {
		return ""
	}
	for _, o := range src.Objects("secrets", namespace) {
		sum, ok := o.(*kube.SecretSummary)
		if !ok || sum.Release == nil {
			continue
		}
		for _, res := range sum.Release.Resources {
			if res.Namespace != "" && res.Namespace != namespace {
				continue
			}
			for _, owner := range owners {
				if owner.Kind == res.Kind && owner.Name == res.Name {
					return sum.Release.Name
				}
			}
		}
	}
	return ""
}

func terminationText(t *corev1.ContainerStateTerminated) string {
	reason := t.Reason
	if reason == "" {
		reason = "Terminated"
	}
	if t.ExitCode != 0 {
		return fmt.Sprintf("%s, exit %d", reason, t.ExitCode)
	}
	return reason
}

// podStatus is kubectl's STATUS column, with a tone and a tip.
func podStatus(p *corev1.Pod, now time.Time) (status, tone, tip string) {
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}
	tone = "muted"
	// Init containers run first; one still running or failing names the status.
	initializing := false
	for i, cs := range p.Status.InitContainerStatuses {
		restartable := i < len(p.Spec.InitContainers) && p.Spec.InitContainers[i].RestartPolicy != nil && *p.Spec.InitContainers[i].RestartPolicy == corev1.ContainerRestartPolicyAlways
		switch {
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0:
			continue
		case restartable && cs.Started != nil && *cs.Started:
			continue
		case cs.State.Terminated != nil:
			t := cs.State.Terminated
			if t.Reason == "" {
				reason = fmt.Sprintf("Init:ExitCode:%d", t.ExitCode)
			} else {
				reason = "Init:" + t.Reason
			}
			tone, tip = "err", fmt.Sprintf("Init container %s %s.", cs.Name, terminationText(t))
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + cs.State.Waiting.Reason
			tone, tip = waitingTone(cs.State.Waiting.Reason), "Init container "+cs.Name+": "+cs.State.Waiting.Message
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
			tone, tip = "warn", "Init container "+cs.Name+" is running."
		}
		initializing = true
		break
	}
	if !initializing {
		running := false
		for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
			cs := p.Status.ContainerStatuses[i]
			switch {
			case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
				reason = cs.State.Waiting.Reason
				tone, tip = waitingTone(reason), "Container "+cs.Name+": "+strings.TrimSpace(cs.State.Waiting.Message)
			case cs.State.Terminated != nil && cs.State.Terminated.Reason != "":
				reason = cs.State.Terminated.Reason
				tone, tip = terminatedTone(cs.State.Terminated), "Container "+cs.Name+" "+terminationText(cs.State.Terminated)+"."
			case cs.State.Terminated != nil:
				t := cs.State.Terminated
				if t.Signal != 0 {
					reason = fmt.Sprintf("Signal:%d", t.Signal)
				} else {
					reason = fmt.Sprintf("ExitCode:%d", t.ExitCode)
				}
				tone, tip = terminatedTone(t), "Container "+cs.Name+" "+terminationText(t)+"."
			case cs.Ready && cs.State.Running != nil:
				running = true
			}
		}
		if reason == "Completed" && running {
			if hasPodReadyCondition(p.Status.Conditions) {
				reason, tone = "Running", "ok"
			} else {
				reason, tone = "NotReady", "warn"
			}
		}
	}
	if p.DeletionTimestamp != nil && p.Status.Reason == "NodeLost" {
		reason, tone, tip = "Unknown", "err", "The node stopped reporting; the pod's state is unknown."
	} else if p.DeletionTimestamp != nil {
		reason, tone = "Terminating", "info"
		tip = "Deleted " + ago(p.DeletionTimestamp.Time, now) + "; the kubelet is stopping its containers."
	}
	switch reason {
	case "Running":
		ready := hasPodReadyCondition(p.Status.Conditions)
		if ready {
			tone, tip = "ok", "Running and ready."
		} else {
			tone = "warn"
			if tip == "" {
				tip = "Running but not ready: a readiness check is failing."
			}
		}
	case "Pending":
		tone = "warn"
		if tip == "" {
			if p.Spec.NodeName == "" {
				tip = "Waiting for a node."
				if c := podCondition(p, corev1.PodScheduled); c != nil && c.Message != "" {
					tip = c.Message
				}
			} else {
				tip = "Starting on " + p.Spec.NodeName + "."
			}
		}
	case "Succeeded":
		reason, tone, tip = "Completed", "muted", "Finished with exit code 0."
	case "Failed":
		tone = "err"
		if tip == "" {
			tip = "Finished with an error."
			if p.Status.Message != "" {
				tip = p.Status.Message
			}
		}
	case "Completed":
		tone = "muted"
	}
	return reason, tone, tip
}

func waitingTone(reason string) string {
	switch reason {
	case "ContainerCreating", "PodInitializing":
		return "warn"
	}
	return "err"
}

func terminatedTone(t *corev1.ContainerStateTerminated) string {
	if t.ExitCode == 0 && t.Signal == 0 {
		return "muted"
	}
	return "err"
}

func hasPodReadyCondition(conds []corev1.PodCondition) bool {
	for _, c := range conds {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func nodeReady(n *corev1.Node) *corev1.NodeCondition {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == corev1.NodeReady {
			return &n.Status.Conditions[i]
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

// Owner is one step of an owner chain.
type Owner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// controllerOf follows the controlling owner reference to the workload
// people recognise: a Deployment rather than its ReplicaSet, a CronJob
// rather than its Job.
func controllerOf(src Source, namespace string, refs []metav1.OwnerReference) *Owner {
	chain := ownerChain(src, namespace, refs, 4)
	if len(chain) == 0 {
		return nil
	}
	return &chain[len(chain)-1]
}

// ownerChain lists the controlling owners outward: ReplicaSet, then its
// Deployment.
func ownerChain(src Source, namespace string, refs []metav1.OwnerReference, depth int) []Owner {
	var out []Owner
	for depth > 0 {
		depth--
		var ref *metav1.OwnerReference
		for i := range refs {
			if refs[i].Controller != nil && *refs[i].Controller {
				ref = &refs[i]
				break
			}
		}
		if ref == nil && len(refs) > 0 {
			ref = &refs[0]
		}
		if ref == nil {
			break
		}
		out = append(out, Owner{Kind: ref.Kind, Name: ref.Name})
		kind, ok := kube.KindOf(ref.Kind)
		if !ok {
			break
		}
		obj := src.Object(kind.Name, namespace, ref.Name)
		if obj == nil {
			break
		}
		m, ok := obj.(metav1.Object)
		if !ok {
			break
		}
		refs = m.GetOwnerReferences()
	}
	return out
}

func podRelease(p *corev1.Pod) string { return objectRelease(p) }

func timePtr(t *metav1.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "a while ago"
	}
	return dur(now.Sub(t)) + " ago"
}

// dur formats a duration the way the prototype does: 45s, 12m, 3h 5m, 2d 4h.
func dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
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

// sortedKeys returns a map's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
