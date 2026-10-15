package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/ucesys/221b/server/internal/kube"
)

// Disruption budgets and drains (P4-05), with kubectl's drain logic and
// the eviction API's checks: what kubectl drain --ignore-daemonsets
// --delete-emptydir-data --dry-run=server would refuse, and what a real
// drain would wait for.

// DrainFlags are the flags the verdicts assume.
const DrainFlags = "--ignore-daemonsets --delete-emptydir-data"

// budgetsFor lists the budgets the eviction API applies to a pod: those in
// its namespace whose selector matches it. A nil or empty selector matches
// nothing, as the eviction API reads it.
func budgetsFor(src Source, p *corev1.Pod) []*policyv1.PodDisruptionBudget {
	var out []*policyv1.PodDisruptionBudget
	for _, o := range src.Objects("poddisruptionbudgets", p.Namespace) {
		b, ok := o.(*policyv1.PodDisruptionBudget)
		if !ok || b.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
		if err != nil || sel.Empty() || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// evictionError is what the eviction API answers a dry-run eviction of
// the pod, or "" when it allows it.
func evictionError(p *corev1.Pod, budgets []*policyv1.PodDisruptionBudget) (string, *policyv1.PodDisruptionBudget) {
	// Finished, pending and terminating pods are evicted whatever the budgets.
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodPending || p.DeletionTimestamp != nil {
		return "", nil
	}
	switch len(budgets) {
	case 0:
		return "", nil
	case 1:
	default:
		return "This pod has more than one PodDisruptionBudget, which the eviction subresource does not support.", nil
	}
	b := budgets[0]
	if !podReady(p) {
		if b.Spec.UnhealthyPodEvictionPolicy != nil && *b.Spec.UnhealthyPodEvictionPolicy == policyv1.AlwaysAllow {
			return "", b
		}
		if b.Status.CurrentHealthy >= b.Status.DesiredHealthy && b.Status.DesiredHealthy > 0 {
			return "", b
		}
	}
	if b.Status.ObservedGeneration < b.Generation {
		return "Cannot evict pod as it would violate the pod's disruption budget: the disruption controller has not processed its latest change.", b
	}
	if b.Status.DisruptionsAllowed <= 0 {
		return "Cannot evict pod as it would violate the pod's disruption budget.", b
	}
	return "", b
}

// DrainPod is a pod a drain evicts or stops at.
type DrainPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Workload  string `json:"workload,omitempty"`
	// EmptyDir marks pods whose emptyDir data a drain deletes.
	EmptyDir bool   `json:"emptyDir,omitempty"`
	Budget   string `json:"budget,omitempty"`
	// Error is why kubectl drain stops at the pod.
	Error string `json:"error,omitempty"`
}

// DrainRow is whether a node can be drained now, and what blocks it.
type DrainRow struct {
	Key       string     `json:"key"`
	Node      string     `json:"node"`
	Cordoned  bool       `json:"cordoned,omitempty"`
	Evict     []DrainPod `json:"evict"`
	DaemonSet []string   `json:"daemonSet"`
	Mirror    int        `json:"mirror"`
	// Blockers are the pods kubectl drain stops at: its own filters
	// (no controller, a DaemonSet that is gone) and evictions the API
	// refuses.
	Blockers []DrainPod `json:"blockers"`
	// Budgets are the budgets of the evicted pods, as ns/name.
	Budgets []string `json:"budgets"`
	// Verdict is blocked, waits or clean.
	Verdict string `json:"verdict"`
	Why     string `json:"why"`
	Command string `json:"command"`
}

func drainRow(src Source, node string, now time.Time) (any, bool) {
	n, _ := src.Object("nodes", "", node).(*corev1.Node)
	if n == nil {
		return nil, false
	}
	r := DrainRow{Key: PolDrain + ":" + node, Node: node, Cordoned: n.Spec.Unschedulable, Evict: []DrainPod{}, DaemonSet: []string{}, Blockers: []DrainPod{}, Budgets: []string{}, Command: "kubectl drain " + node + " " + DrainFlags + " --dry-run=server"}
	var pods []*corev1.Pod
	for _, o := range src.ByIndex("pods", kube.IndexPodNode, node) {
		if p, ok := o.(*corev1.Pod); ok {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Namespace+"/"+pods[i].Name < pods[j].Namespace+"/"+pods[j].Name })
	type waits struct {
		b *policyv1.PodDisruptionBudget
		n int
	}
	used := map[string]*waits{}
	budgetSeen := map[string]bool{}
	for _, p := range pods {
		finished := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		ctrl := metav1.GetControllerOf(p)
		dp := DrainPod{Namespace: p.Namespace, Name: p.Name, Workload: workloadName(src, p)}
		// kubectl's filters, in its order.
		if ctrl != nil && ctrl.Kind == "DaemonSet" && !finished {
			if ds, _ := src.Object("daemonsets", p.Namespace, ctrl.Name).(*appsv1.DaemonSet); ds == nil {
				dp.Error = fmt.Sprintf("its DaemonSet %s no longer exists: kubectl drain stops unless --force", ctrl.Name)
				r.Blockers = append(r.Blockers, dp)
			} else {
				r.DaemonSet = append(r.DaemonSet, p.Namespace+"/"+p.Name)
			}
			continue
		}
		if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
			r.Mirror++
			continue
		}
		if !finished {
			for _, v := range p.Spec.Volumes {
				if v.EmptyDir != nil {
					dp.EmptyDir = true
				}
			}
		}
		if ctrl == nil && !finished {
			dp.Error = "it has no controller: kubectl drain stops unless --force, and the pod would be lost"
			r.Blockers = append(r.Blockers, dp)
			continue
		}
		budgets := budgetsFor(src, p)
		for _, b := range budgets {
			k := b.Namespace + "/" + b.Name
			if !budgetSeen[k] {
				budgetSeen[k] = true
				r.Budgets = append(r.Budgets, k)
			}
		}
		msg, b := evictionError(p, budgets)
		if b != nil {
			dp.Budget = b.Name
		}
		if msg != "" {
			dp.Error = msg
			r.Blockers = append(r.Blockers, dp)
		} else if b != nil && podReady(p) && !finished {
			k := b.Namespace + "/" + b.Name
			if used[k] == nil {
				used[k] = &waits{b: b}
			}
			used[k].n++
		}
		r.Evict = append(r.Evict, dp)
	}
	var waitText []string
	keys := make([]string, 0, len(used))
	for k := range used {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w := used[k]
		if int32(w.n) > w.b.Status.DisruptionsAllowed {
			waitText = append(waitText, fmt.Sprintf("%s allows %s at a time and %d of its pods run here", k, plural(int(w.b.Status.DisruptionsAllowed), "disruption", "disruptions"), w.n))
		}
	}
	switch {
	case len(r.Blockers) > 0:
		r.Verdict = "blocked"
		r.Why = blockedWhy(r.Blockers)
	case len(waitText) > 0:
		r.Verdict = "waits"
		r.Why = joinList(waitText) + ": the drain evicts them one at a time, as their replacements become ready."
	default:
		r.Verdict = "clean"
		switch {
		case len(r.Evict) == 0:
			r.Why = "Only static and DaemonSet pods run here."
		case len(r.Budgets) == 0:
			r.Why = fmt.Sprintf("No disruption budget covers its %s.", plural(len(r.Evict), "pod", "pods"))
		default:
			r.Why = fmt.Sprintf("Every budget involved allows its evictions: %s.", joinList(r.Budgets))
		}
	}
	_ = now
	return r, true
}

func blockedWhy(bs []DrainPod) string {
	var parts []string
	byBudget := map[string][]string{}
	var order []string
	for _, b := range bs {
		if strings.HasPrefix(b.Error, "Cannot evict") && b.Budget != "" {
			k := b.Namespace + "/" + b.Budget
			if byBudget[k] == nil {
				order = append(order, k)
			}
			byBudget[k] = append(byBudget[k], b.Name)
			continue
		}
		if strings.HasPrefix(b.Error, "This pod has more than one") {
			parts = append(parts, fmt.Sprintf("%s/%s has more than one disruption budget, which eviction does not support", b.Namespace, b.Name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s: %s", b.Namespace, b.Name, b.Error))
	}
	for _, k := range order {
		names := byBudget[k]
		verb := "runs"
		if len(names) > 1 {
			verb = "run"
		}
		parts = append(parts, fmt.Sprintf("%s allows no disruption now, and %s %s here", k, joinList(names), verb))
	}
	return joinList(parts) + "."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func joinList(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// PDBRow is a PodDisruptionBudget.
type PDBRow struct {
	Key       string `json:"key"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Selector  string `json:"selector"`
	// Rule is "minAvailable 1" or "maxUnavailable 25%".
	Rule               string   `json:"rule"`
	CurrentHealthy     int32    `json:"currentHealthy"`
	DesiredHealthy     int32    `json:"desiredHealthy"`
	ExpectedPods       int32    `json:"expectedPods"`
	DisruptionsAllowed int32    `json:"disruptionsAllowed"`
	Unhealthy          string   `json:"unhealthyPolicy,omitempty"`
	Pods               []PDBPod `json:"pods"`
	// Overlap are its pods another budget also selects.
	Overlap []string   `json:"overlap"`
	Release string     `json:"release,omitempty"`
	Created *time.Time `json:"created,omitempty"`
}

// PDBPod is a pod a budget selects.
type PDBPod struct {
	Name  string `json:"name"`
	Node  string `json:"node,omitempty"`
	Ready bool   `json:"ready"`
}

func pdbRow(src Source, ns, name string, now time.Time) (any, bool) {
	b, _ := src.Object("poddisruptionbudgets", ns, name).(*policyv1.PodDisruptionBudget)
	if b == nil {
		return nil, false
	}
	r := PDBRow{Key: PolPDB + ":" + ns + "/" + name, Namespace: ns, Name: name, Selector: "nothing", CurrentHealthy: b.Status.CurrentHealthy, DesiredHealthy: b.Status.DesiredHealthy, ExpectedPods: b.Status.ExpectedPods, DisruptionsAllowed: b.Status.DisruptionsAllowed, Pods: []PDBPod{}, Overlap: []string{}, Release: objectRelease(b), Created: created(b)}
	if b.Spec.Selector != nil {
		r.Selector = selectorWords(b.Spec.Selector)
	}
	switch {
	case b.Spec.MinAvailable != nil:
		r.Rule = "minAvailable " + intOrPercent(*b.Spec.MinAvailable)
	case b.Spec.MaxUnavailable != nil:
		r.Rule = "maxUnavailable " + intOrPercent(*b.Spec.MaxUnavailable)
	}
	if b.Spec.UnhealthyPodEvictionPolicy != nil {
		r.Unhealthy = string(*b.Spec.UnhealthyPodEvictionPolicy)
	}
	for _, p := range podsIn(src, ns) {
		if !live(p) {
			continue
		}
		bs := budgetsFor(src, p)
		mine := false
		for _, x := range bs {
			if x.Name == name {
				mine = true
			}
		}
		if !mine {
			continue
		}
		r.Pods = append(r.Pods, PDBPod{Name: p.Name, Node: p.Spec.NodeName, Ready: podReady(p)})
		if len(bs) > 1 {
			r.Overlap = append(r.Overlap, p.Name)
		}
	}
	_ = now
	return r, true
}

func intOrPercent(v intstr.IntOrString) string { return v.String() }

func selectorWords(s *metav1.LabelSelector) string {
	if len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0 {
		return "nothing (an empty selector)"
	}
	var parts []string
	for k, v := range s.MatchLabels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	for _, e := range s.MatchExpressions {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", e.Key, strings.ToLower(string(e.Operator)), strings.Join(e.Values, ", ")))
	}
	return strings.Join(parts, ", ")
}
