package watson

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"helm.sh/helm/v4/pkg/storage/driver"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/drain"
	"sigs.k8s.io/yaml"

	release "helm.sh/helm/v4/pkg/release/v1"
)

// What an approval card says a change does (P6-04), worked out from the
// cluster as it is: the objects it acts on, the impact (pods evicted,
// budgets touched, replicas, images, labels), and the guards that void the
// card when those objects change.

type clients struct {
	cs     kubernetes.Interface
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

func clientsFor(ctx context.Context, t Target) (*clients, error) {
	g := newGetter(ctx, t, "", io.Discard)
	cs, err := kubernetes.NewForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return &clients{cs: cs, dyn: dyn, mapper: g.mapper}, nil
}

// guard is one thing a card relies on staying as it was.
type guard struct {
	what string
	want string
	read func(ctx context.Context, cs *clients) (string, error)
}

// checkGuards says what changed since the card appeared, or "".
func checkGuards(ctx context.Context, cs *clients, guards []guard) string {
	if cs == nil {
		return ""
	}
	for _, g := range guards {
		got, err := g.read(ctx, cs)
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("The approval lapsed: %s no longer exists. Send the command again for a fresh card.", g.what)
		}
		if err != nil {
			continue
		}
		if got != g.want {
			return fmt.Sprintf("The approval lapsed: %s changed after this card appeared. Review the fresh state, and send the command again for a new card.", g.what)
		}
	}
	return ""
}

func digest(v ...any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// target is one object a command names.
type target struct {
	gvr  schema.GroupVersionResource
	kind string
	ns   string
	name string
	obj  *unstructured.Unstructured
}

func (t target) String() string {
	k := strings.ToLower(t.kind)
	if t.ns != "" {
		return k + " " + t.ns + "/" + t.name
	}
	return k + " " + t.name
}

func (t target) title() string {
	if t.ns != "" {
		return t.kind + " " + t.ns + "/" + t.name
	}
	return t.kind + " " + t.name
}

// resolveType maps a TYPE argument to its resource.
func (cs *clients) resolveType(typ string) (schema.GroupVersionResource, string, bool, error) {
	gvr, gr := schema.ParseResourceArg(strings.ToLower(typ))
	var full schema.GroupVersionResource
	var err error
	if gvr != nil {
		full, err = cs.mapper.ResourceFor(*gvr)
	}
	if gvr == nil || err != nil {
		full, err = cs.mapper.ResourceFor(gr.WithVersion(""))
	}
	if err != nil {
		return full, "", false, err
	}
	gvk, err := cs.mapper.KindFor(full)
	if err != nil {
		return full, "", false, err
	}
	m, err := cs.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return full, "", false, err
	}
	return full, gvk.Kind, m.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// targetsOf reads the objects a command names: TYPE NAME..., TYPE/NAME...
// or TYPE -l SELECTOR. fixedType is the type of commands that take only
// names (nodes for cordon). The guards hold each selector to the objects
// it matched for the card.
func (cs *clients) targetsOf(ctx context.Context, c *Command, fixedType string, pos []string) ([]target, []guard, error) {
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	type pair struct{ typ, name string }
	var pairs []pair
	switch {
	case fixedType != "":
		for _, n := range pos {
			pairs = append(pairs, pair{fixedType, n})
		}
		if len(pos) == 0 {
			pairs = append(pairs, pair{fixedType, ""})
		}
	case len(pos) > 0 && strings.Contains(pos[0], "/"):
		for _, p := range pos {
			if t, n, ok := strings.Cut(p, "/"); ok {
				pairs = append(pairs, pair{t, n})
			}
		}
	case len(pos) > 0:
		if len(pos) == 1 {
			pairs = append(pairs, pair{pos[0], ""})
		}
		for _, n := range pos[1:] {
			pairs = append(pairs, pair{pos[0], n})
		}
	}
	var out []target
	var sets []guard
	for _, p := range pairs {
		gvr, kind, namespaced, err := cs.resolveType(p.typ)
		if err != nil {
			return out, sets, fmt.Errorf("the server doesn't have a resource type %q", p.typ)
		}
		res := cs.dyn.Resource(gvr)
		var ri dynamic.ResourceInterface = res
		tns := ""
		if namespaced {
			ri, tns = res.Namespace(ns), ns
		}
		if p.name == "" {
			sel := c.Flag("selector")
			if sel == "" {
				continue
			}
			list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: sel})
			if err != nil {
				return out, sets, err
			}
			var names []string
			for i := range list.Items {
				o := &list.Items[i]
				out = append(out, target{gvr: gvr, kind: kind, ns: tns, name: o.GetName(), obj: o})
				names = append(names, o.GetName())
			}
			sets = append(sets, selectorGuard(gvr, tns, sel, names))
			continue
		}
		o, err := ri.Get(ctx, p.name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				out = append(out, target{gvr: gvr, kind: kind, ns: tns, name: p.name})
				continue
			}
			return out, sets, err
		}
		out = append(out, target{gvr: gvr, kind: kind, ns: tns, name: p.name, obj: o})
	}
	return out, sets, nil
}

// selectorGuard voids a card when the set of objects a selector matches
// changes: a node joining the pool, a pod a scale-up starts. The card
// showed the objects it matched then, and only those may change.
func selectorGuard(gvr schema.GroupVersionResource, ns, sel string, names []string) guard {
	sort.Strings(names)
	what := fmt.Sprintf("the set of %s matching %s", gvr.Resource, sel)
	if ns != "" {
		what += " in " + ns
	}
	return guard{what: what, want: digest(names), read: func(ctx context.Context, cs *clients) (string, error) {
		var ri dynamic.ResourceInterface = cs.dyn.Resource(gvr)
		if ns != "" {
			ri = cs.dyn.Resource(gvr).Namespace(ns)
		}
		list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return "", err
		}
		var now []string
		for i := range list.Items {
			now = append(now, list.Items[i].GetName())
		}
		sort.Strings(now)
		return digest(now), nil
	}}
}

// objectGuard voids a card when the parts of an object a command depends
// on change.
func objectGuard(t target, parts func(o *unstructured.Unstructured) any) guard {
	read := func(ctx context.Context, cs *clients) (string, error) {
		var ri dynamic.ResourceInterface = cs.dyn.Resource(t.gvr)
		if t.ns != "" {
			ri = cs.dyn.Resource(t.gvr).Namespace(t.ns)
		}
		o, err := ri.Get(ctx, t.name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return digest(o.GetUID(), parts(o)), nil
	}
	return guard{what: t.String(), want: digest(t.obj.GetUID(), parts(t.obj)), read: read}
}

func field(o *unstructured.Unstructured, path ...string) any {
	v, _, _ := unstructured.NestedFieldNoCopy(o.Object, path...)
	return v
}

func toTyped[T any](o *unstructured.Unstructured) *T {
	var t T
	if o == nil {
		return &t
	}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &t)
	return &t
}

func listText(names []string, max int) string {
	if len(names) <= max {
		return joinAnd(names)
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(" and %d more", len(names)-max)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// cardFor works out a change's card.
func cardFor(ctx context.Context, cs *clients, c *Command, t Target) (targets []string, guards []guard, impact []string) {
	add := func(format string, a ...any) { impact = append(impact, fmt.Sprintf(format, a...)) }
	switch c.Name() {
	case "kubectl cordon", "kubectl uncordon", "kubectl drain":
		nodes, sets, err := cs.targetsOf(ctx, c, "nodes", c.Positionals)
		guards = append(guards, sets...)
		if err != nil {
			add("Watson could not read the nodes: %v", err)
		}
		if len(nodes) == 0 {
			add("No node matches.")
		}
		for _, t := range nodes {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no node %s.", t.name)
				continue
			}
			n := toTyped[corev1.Node](t.obj)
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any {
				return []any{field(o, "spec", "unschedulable"), field(o, "spec", "taints"), o.GetLabels()}
			}))
			pods, _ := cs.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + n.Name})
			var live []corev1.Pod
			if pods != nil {
				for _, p := range pods.Items {
					if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
						live = append(live, p)
					}
				}
			}
			switch c.Verb[0] {
			case "cordon":
				if n.Spec.Unschedulable {
					add("%s is already cordoned; nothing changes.", n.Name)
				} else {
					add("No new pods will be scheduled on %s. Its %s keep running.", n.Name, plural(len(live), "pod", "pods"))
				}
			case "uncordon":
				if !n.Spec.Unschedulable {
					add("%s is not cordoned; nothing changes.", n.Name)
					continue
				}
				add("%s accepts new pods again.", n.Name)
				if pending, err := cs.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"}); err == nil && len(pending.Items) > 0 {
					add("%s Pending in the cluster; the scheduler may place them on %s.", plural(len(pending.Items), "pod is", "pods are"), n.Name)
				}
			case "drain":
				impact = append(impact, drainImpact(ctx, cs, c, n)...)
				nodeName := n.Name
				var names []string
				for _, p := range live {
					names = append(names, p.Namespace+"/"+p.Name)
				}
				sort.Strings(names)
				guards = append(guards, guard{what: "the set of pods on " + nodeName, want: digest(names), read: func(ctx context.Context, cs *clients) (string, error) {
					pods, err := cs.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + nodeName})
					if err != nil {
						return "", err
					}
					var now []string
					for _, p := range pods.Items {
						if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
							now = append(now, p.Namespace+"/"+p.Name)
						}
					}
					sort.Strings(now)
					return digest(now), nil
				}})
			}
		}
	case "kubectl scale":
		ts, sets, err := cs.targetsOf(ctx, c, "", c.Positionals)
		guards = append(guards, sets...)
		if err != nil {
			add("%v", err)
		}
		want, _ := strconv.Atoi(c.Flag("replicas"))
		for _, t := range ts {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no %s.", t.String())
				continue
			}
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any { return []any{o.GetGeneration(), field(o, "spec", "replicas")} }))
			cur := int64(1)
			if v, ok, _ := unstructured.NestedInt64(t.obj.Object, "spec", "replicas"); ok {
				cur = v
			}
			if cur == int64(want) {
				add("%s already has %d replicas; nothing changes.", t.title(), want)
				continue
			}
			add("%s: %d → %d replicas.", t.title(), cur, want)
			if int64(want) < cur {
				n := int(cur) - want
				if t.kind == "StatefulSet" {
					var gone []string
					for i := int(cur) - 1; i >= want; i-- {
						gone = append(gone, fmt.Sprintf("%s-%d", t.name, i))
					}
					add("Removes %s, highest number first: %s. Their volume claims stay.", plural(n, "pod", "pods"), listText(gone, 4))
				} else {
					add("Removes %s.", plural(n, "pod", "pods"))
				}
				if want == 0 {
					add("With no replicas, nothing serves the %s's traffic until it is scaled up again.", strings.ToLower(t.kind))
				}
			} else {
				add("Starts %s, where the scheduler finds room.", plural(want-int(cur), "pod", "pods"))
			}
			if c.Has("current-replicas") {
				add("kubectl changes it only if it still has %s replicas.", c.Flag("current-replicas"))
			}
		}
	case "kubectl rollout restart", "kubectl rollout undo":
		ts, sets, err := cs.targetsOf(ctx, c, "", c.Positionals)
		guards = append(guards, sets...)
		if err != nil {
			add("%v", err)
		}
		for _, t := range ts {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no %s.", t.String())
				continue
			}
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any { return []any{o.GetGeneration(), field(o, "spec", "template")} }))
			if c.Verb[1] == "restart" {
				impact = append(impact, restartImpact(ctx, cs, t)...)
			} else {
				impact = append(impact, undoImpact(ctx, cs, c, t)...)
			}
		}
	case "kubectl delete":
		ts, sets, err := cs.targetsOf(ctx, c, "", c.Positionals)
		guards = append(guards, sets...)
		if err != nil {
			add("%v", err)
		}
		if len(ts) == 0 {
			add("No pod matches.")
		}
		for _, t := range ts {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no %s.", t.String())
				continue
			}
			if t.kind == "PersistentVolumeClaim" {
				guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any {
					return []any{field(o, "spec", "volumeName"), field(o, "status", "phase"), o.GetDeletionTimestamp() != nil}
				}))
				impact = append(impact, claimDeleteImpact(ctx, cs, toTyped[corev1.PersistentVolumeClaim](t.obj))...)
				continue
			}
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any {
				return []any{field(o, "spec", "nodeName"), o.GetDeletionTimestamp() != nil}
			}))
			p := toTyped[corev1.Pod](t.obj)
			add("%s", deleteImpact(p, c))
		}
	case "kubectl patch":
		ts, sets, err := cs.targetsOf(ctx, c, "", c.Positionals)
		guards = append(guards, sets...)
		if err != nil {
			add("%v", err)
		}
		for _, t := range ts {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no %s.", t.String())
				continue
			}
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any {
				return []any{field(o, "spec", "resources"), field(o, "status", "capacity")}
			}))
			impact = append(impact, claimExpandImpact(ctx, cs, c, toTyped[corev1.PersistentVolumeClaim](t.obj))...)
		}
	case "kubectl label", "kubectl annotate":
		var pos, changes []string
		for _, p := range c.Positionals {
			if isChange(p) {
				changes = append(changes, p)
			} else {
				pos = append(pos, p)
			}
		}
		ts, sets, err := cs.targetsOf(ctx, c, "", pos)
		guards = append(guards, sets...)
		if err != nil {
			add("%v", err)
		}
		if len(ts) == 0 {
			add("Nothing matches.")
		}
		annot := c.Verb[0] == "annotate"
		for _, t := range ts {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no %s.", t.String())
				continue
			}
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any {
				if annot {
					return o.GetAnnotations()
				}
				return o.GetLabels()
			}))
			impact = append(impact, labelImpact(ctx, cs, c, t, changes, annot)...)
		}
	case "kubectl taint":
		pos, changes := splitTaints(c.Positionals)
		ts, sets, err := cs.targetsOf(ctx, c, "", pos)
		guards = append(guards, sets...)
		if err != nil {
			add("%v", err)
		}
		if len(ts) == 0 {
			add("No node matches.")
		}
		for _, t := range ts {
			targets = append(targets, t.String())
			if t.obj == nil {
				add("There is no %s.", t.String())
				continue
			}
			guards = append(guards, objectGuard(t, func(o *unstructured.Unstructured) any {
				return field(o, "spec", "taints")
			}))
			impact = append(impact, taintImpact(ctx, cs, c, toTyped[corev1.Node](t.obj), changes)...)
		}
	case "helm rollback":
		ts, gs, im := rollbackImpact(ctx, cs, c)
		targets, guards, impact = ts, gs, im
	case "helm install", "helm upgrade", "helm uninstall":
		ts, gs, im := appImpact(ctx, cs, c, t)
		targets, guards, impact = ts, gs, im
	}
	return targets, guards, impact
}

func drainImpact(ctx context.Context, cs *clients, c *Command, n *corev1.Node) []string {
	var out []string
	h := &drain.Helper{Ctx: ctx, Client: cs.cs, Force: c.Has("force"), IgnoreAllDaemonSets: c.Flag("ignore-daemonsets") != "false" && c.Has("ignore-daemonsets"),
		DeleteEmptyDirData: c.Flag("delete-emptydir-data") != "false" && c.Has("delete-emptydir-data"), PodSelector: c.Flag("pod-selector"), Out: io.Discard, ErrOut: io.Discard}
	list, errs := h.GetPodsForDeletion(n.Name)
	cordon := "Cordons " + n.Name
	if n.Spec.Unschedulable {
		cordon = n.Name + " is already cordoned"
	}
	var pods []corev1.Pod
	if list != nil {
		pods = list.Pods()
	}
	if len(pods) == 0 {
		out = append(out, cordon+"; no pod needs to be evicted.")
	} else {
		var names []string
		for _, p := range pods {
			names = append(names, p.Namespace+"/"+p.Name)
		}
		verb := "evicts"
		if c.Has("disable-eviction") {
			verb = "deletes, bypassing every disruption budget,"
		}
		out = append(out, fmt.Sprintf("%s and %s %s: %s.", cordon, verb, plural(len(pods), "pod", "pods"), listText(names, 5)))
	}
	if list != nil {
		if w := list.Warnings(); w != "" {
			out = append(out, "kubectl warns: "+w+".")
		}
	}
	for _, e := range errs {
		out = append(out, "kubectl will stop: "+e.Error()+".")
	}
	var empty []string
	for _, p := range pods {
		for _, v := range p.Spec.Volumes {
			if v.EmptyDir != nil {
				empty = append(empty, p.Namespace+"/"+p.Name)
				break
			}
		}
	}
	if len(empty) > 0 && h.DeleteEmptyDirData {
		out = append(out, fmt.Sprintf("%s lose the data in their emptyDir volumes: %s.", plural(len(empty), "pod", "pods"), listText(empty, 4)))
	}
	if c.Has("disable-eviction") {
		return out
	}
	// The disruption budgets the evictions touch.
	byNS := map[string][]corev1.Pod{}
	for _, p := range pods {
		byNS[p.Namespace] = append(byNS[p.Namespace], p)
	}
	nss := make([]string, 0, len(byNS))
	for ns := range byNS {
		nss = append(nss, ns)
	}
	sort.Strings(nss)
	for _, ns := range nss {
		pdbs, err := cs.cs.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, b := range pdbs.Items {
			out = append(out, budgetLine(b, byNS[ns])...)
		}
	}
	return out
}

func budgetLine(b policyv1.PodDisruptionBudget, pods []corev1.Pod) []string {
	sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
	if err != nil || sel.Empty() {
		return nil
	}
	covered := 0
	for _, p := range pods {
		if sel.Matches(labels.Set(p.Labels)) {
			covered++
		}
	}
	if covered == 0 {
		return nil
	}
	allowed := int(b.Status.DisruptionsAllowed)
	id := b.Namespace + "/" + b.Name
	switch {
	case allowed == 0:
		return []string{fmt.Sprintf("Disruption budget %s allows no disruption now and covers %s here: the drain waits, retrying every 5 seconds, until it allows one (up to --timeout; without it, indefinitely).", id, plural(covered, "pod", "pods"))}
	case covered > allowed:
		return []string{fmt.Sprintf("Disruption budget %s allows %s now and covers %s here: the drain evicts %d, then waits for them to run elsewhere before the rest.", id, plural(allowed, "disruption", "disruptions"), plural(covered, "pod", "pods"), allowed)}
	}
	return []string{fmt.Sprintf("Disruption budget %s allows %s now; the drain evicts %d of its pods.", id, plural(allowed, "disruption", "disruptions"), covered)}
}

func restartImpact(ctx context.Context, cs *clients, t target) []string {
	sel, _, _ := unstructured.NestedMap(t.obj.Object, "spec", "selector")
	var n int
	if sel != nil {
		ls := &metav1.LabelSelector{}
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(sel, ls)
		if s, err := metav1.LabelSelectorAsSelector(ls); err == nil {
			if pods, err := cs.cs.CoreV1().Pods(t.ns).List(ctx, metav1.ListOptions{LabelSelector: s.String()}); err == nil {
				n = len(pods.Items)
			}
		}
	}
	switch t.kind {
	case "Deployment":
		d := toTyped[appsv1.Deployment](t.obj)
		surge, unavail := "25%", "25%"
		if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
			if ru.MaxSurge != nil {
				surge = ru.MaxSurge.String()
			}
			if ru.MaxUnavailable != nil {
				unavail = ru.MaxUnavailable.String()
			}
		}
		if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
			return []string{fmt.Sprintf("Replaces the %s of %s all at once (strategy Recreate): it serves nothing until the new ones are ready.", plural(n, "pod", "pods"), t.title())}
		}
		return []string{fmt.Sprintf("Replaces the %s of %s a few at a time (max surge %s, max unavailable %s).", plural(n, "pod", "pods"), t.title(), surge, unavail)}
	case "StatefulSet":
		return []string{fmt.Sprintf("Replaces the %s of %s one at a time, highest number first; each keeps its name and volumes.", plural(n, "pod", "pods"), t.title())}
	case "DaemonSet":
		return []string{fmt.Sprintf("Replaces the %s of %s node by node.", plural(n, "pod", "pods"), t.title())}
	}
	return nil
}

func images(spec corev1.PodSpec) string {
	var out []string
	for _, c := range spec.Containers {
		out = append(out, c.Image)
	}
	return strings.Join(out, ", ")
}

func undoImpact(ctx context.Context, cs *clients, c *Command, t target) []string {
	if t.kind != "Deployment" {
		rev := "the previous revision"
		if v := c.Flag("to-revision"); v != "" && v != "0" {
			rev = "revision " + v
		}
		return []string{fmt.Sprintf("Rolls %s back to %s; its pods are replaced as in a rollout.", t.title(), rev)}
	}
	d := toTyped[appsv1.Deployment](t.obj)
	sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return nil
	}
	list, err := cs.cs.AppsV1().ReplicaSets(t.ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil
	}
	type rev struct {
		n  int
		rs appsv1.ReplicaSet
	}
	var revs []rev
	for _, rs := range list.Items {
		if ref := metav1.GetControllerOf(&rs); ref == nil || ref.UID != d.UID {
			continue
		}
		n, _ := strconv.Atoi(rs.Annotations["deployment.kubernetes.io/revision"])
		revs = append(revs, rev{n, rs})
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].n > revs[j].n })
	if len(revs) == 0 {
		return []string{t.title() + " has no rollout history to go back to."}
	}
	cur := revs[0]
	var to *rev
	want, _ := strconv.Atoi(c.Flag("to-revision"))
	for i := range revs {
		if (want > 0 && revs[i].n == want) || (want == 0 && i == 1) {
			to = &revs[i]
			break
		}
	}
	if to == nil {
		if want > 0 {
			return []string{fmt.Sprintf("%s has no revision %d: kubectl will refuse.", t.title(), want)}
		}
		return []string{t.title() + " has no earlier revision: kubectl will refuse."}
	}
	from, toImg := images(d.Spec.Template.Spec), images(to.rs.Spec.Template.Spec)
	out := []string{fmt.Sprintf("Rolls %s back from revision %d to revision %d; Kubernetes records it as revision %d.", t.title(), cur.n, to.n, cur.n+1)}
	if from != toImg {
		out = append(out, fmt.Sprintf("Image %s → %s.", from, toImg))
	} else {
		out = append(out, "The images stay the same; the pod template's other settings change.")
	}
	out = append(out, fmt.Sprintf("Replaces the %s a few at a time, as a rollout does.", plural(int(d.Status.Replicas), "pod", "pods")))
	return out
}

func deleteImpact(p *corev1.Pod, c *Command) string {
	id := p.Namespace + "/" + p.Name
	if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return fmt.Sprintf("Pod %s mirrors a static pod: the kubelet on %s recreates it at once.", id, p.Spec.NodeName)
	}
	ref := metav1.GetControllerOf(p)
	tail := ""
	if c.Has("force") && (c.Flag("grace-period") == "0" || c.Has("now")) {
		tail = " It is removed at once, without waiting for its containers to stop: they may keep running on the node for a while."
	}
	if ref == nil {
		return fmt.Sprintf("Pod %s has no controller: nothing recreates it, so it is gone for good.%s", id, tail)
	}
	switch ref.Kind {
	case "StatefulSet":
		return fmt.Sprintf("Pod %s is deleted; StatefulSet %s recreates it with the same name and volumes.%s", id, ref.Name, tail)
	case "DaemonSet":
		return fmt.Sprintf("Pod %s is deleted; DaemonSet %s starts a new one on %s.%s", id, ref.Name, p.Spec.NodeName, tail)
	case "Job":
		return fmt.Sprintf("Pod %s is deleted; Job %s starts another if it has completions left.%s", id, ref.Name, tail)
	}
	return fmt.Sprintf("Pod %s is deleted; %s %s creates a replacement, which the scheduler may place on another node.%s", id, ref.Kind, ref.Name, tail)
}

const psaPrefix = "pod-security.kubernetes.io/"

// claimDeleteImpact says what deleting a claim does to its data (ST-18):
// the reclaim policy of its volume decides, a pod that mounts it holds it,
// and a StatefulSet's template makes it again.
func claimDeleteImpact(ctx context.Context, cs *clients, c *corev1.PersistentVolumeClaim) []string {
	id := c.Namespace + "/" + c.Name
	var out []string
	switch {
	case c.Spec.VolumeName == "":
		out = append(out, fmt.Sprintf("Claim %s has no volume yet: deleting it deletes no data.", id))
	default:
		pv, err := cs.cs.CoreV1().PersistentVolumes().Get(ctx, c.Spec.VolumeName, metav1.GetOptions{})
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("Deletes claim %s. Its volume %s could not be read, so what happens to the data is not known: %v", id, c.Spec.VolumeName, err))
		case pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete:
			out = append(out, fmt.Sprintf("Deletes claim %s and, under its reclaim policy Delete, the volume %s and all its data.", id, pv.Name))
		default:
			out = append(out, fmt.Sprintf("Deletes claim %s. Its volume %s and the data stay, Released (reclaim policy %s), until someone reuses or deletes it.", id, pv.Name, pv.Spec.PersistentVolumeReclaimPolicy))
		}
	}
	if pods, err := cs.cs.CoreV1().Pods(c.Namespace).List(ctx, metav1.ListOptions{}); err == nil {
		var users []string
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == c.Name {
					users = append(users, p.Name)
				}
			}
		}
		if len(users) > 0 {
			sort.Strings(users)
			out = append(out, fmt.Sprintf("%s mount it: the claim stays Terminating until %s, and only then goes.", listText(users, 5), plural(len(users), "that pod is gone", "those pods are gone")))
		}
	}
	if sets, err := cs.cs.AppsV1().StatefulSets(c.Namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for _, set := range sets.Items {
			for _, t := range set.Spec.VolumeClaimTemplates {
				if strings.HasPrefix(c.Name, t.Name+"-"+set.Name+"-") {
					out = append(out, fmt.Sprintf("StatefulSet %s makes it from its %s template: it creates the claim again, empty, for its pod.", set.Name, t.Name))
				}
			}
		}
	}
	return out
}

// claimExpandImpact says what growing a claim does (ST-18): whether its
// class allows it, and when the filesystem follows.
func claimExpandImpact(ctx context.Context, cs *clients, c *Command, pvc *corev1.PersistentVolumeClaim) []string {
	id := pvc.Namespace + "/" + pvc.Name
	want, err := ClaimSize(c.Flag("patch"))
	if err != nil {
		return []string{err.Error()}
	}
	have := pvc.Status.Capacity[corev1.ResourceStorage]
	if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok && req.Cmp(have) > 0 {
		have = req
	}
	if want.Cmp(have) <= 0 {
		return []string{fmt.Sprintf("Claim %s has %s; a claim only grows, so the API server will refuse %s.", id, have.String(), want.String())}
	}
	out := []string{fmt.Sprintf("Grows claim %s from %s to %s.", id, have.String(), want.String())}
	class := ""
	if pvc.Spec.StorageClassName != nil {
		class = *pvc.Spec.StorageClassName
	}
	if class == "" {
		return append(out, "It has no class, so nothing grows its volume: the API server will refuse.")
	}
	sc, err := cs.cs.StorageV1().StorageClasses().Get(ctx, class, metav1.GetOptions{})
	switch {
	case err != nil:
		out = append(out, fmt.Sprintf("Its class %s could not be read: whether it allows expansion is not known.", class))
	case sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion:
		out = append(out, fmt.Sprintf("Its class %s does not allow expansion: the API server will refuse.", class))
	default:
		out = append(out, fmt.Sprintf("Its class %s allows expansion. %s grows the volume; the filesystem follows while the pod runs if the driver expands online, otherwise when the pod next starts. A volume never shrinks.", class, sc.Provisioner))
	}
	return out
}

// defaultClassImpact says what changing the default-class annotation does
// (ST-18): claims that name no class get the default, and two defaults
// leave Kubernetes to pick the newest.
func defaultClassImpact(ctx context.Context, cs *clients, name string, changes []string) []string {
	const key = "storageclass.kubernetes.io/is-default-class"
	on, touched := false, false
	for _, ch := range changes {
		if k, v, ok := strings.Cut(ch, "="); ok && k == key {
			on, touched = v == "true", true
		} else if strings.TrimSuffix(ch, "-") == key {
			touched = true
		}
	}
	if !touched {
		return nil
	}
	if !on {
		return []string{fmt.Sprintf("%s is no longer the default class: new claims that name no class get none until another class is set as the default. Bound claims keep their class.", name)}
	}
	out := []string{fmt.Sprintf("%s becomes the default class: new claims that name no class get it. Bound claims keep their class.", name)}
	if list, err := cs.cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{}); err == nil {
		var others []string
		for _, sc := range list.Items {
			if sc.Name != name && (sc.Annotations[key] == "true" || sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true") {
				others = append(others, sc.Name)
			}
		}
		if len(others) > 0 {
			out = append(out, fmt.Sprintf("%s is the default too: with two, Kubernetes gives new claims the newest. Remove the default there first.", joinAnd(others)))
		}
	}
	return out
}

// readTaint reads KEY[=VALUE][:EFFECT] as kubectl taint writes it.
func readTaint(s string) corev1.Taint {
	var t corev1.Taint
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s, t.Effect = s[:i], corev1.TaintEffect(s[i+1:])
	}
	t.Key, t.Value, _ = strings.Cut(s, "=")
	return t
}

func taintText(t corev1.Taint) string {
	s := t.Key
	if t.Value != "" {
		s += "=" + t.Value
	}
	if t.Effect != "" {
		s += ":" + string(t.Effect)
	}
	return s
}

// taintImpact says what kubectl taint changes on a node, and what that
// means for its pods: NoExecute evicts those that do not tolerate it.
func taintImpact(ctx context.Context, cs *clients, c *Command, n *corev1.Node, changes []string) []string {
	var diffs, out []string
	for _, ch := range changes {
		if strings.HasSuffix(ch, "-") {
			want := readTaint(strings.TrimSuffix(ch, "-"))
			var gone []string
			for _, t := range n.Spec.Taints {
				if t.Key == want.Key && (want.Effect == "" || t.Effect == want.Effect) {
					gone = append(gone, taintText(t))
				}
			}
			if len(gone) == 0 {
				diffs = append(diffs, "has no taint "+taintText(want)+": kubectl will refuse")
				continue
			}
			diffs = append(diffs, "removes "+joinAnd(gone))
			out = append(out, fmt.Sprintf("Pods that do not tolerate %s may be scheduled on %s again.", want.Key, n.Name))
			continue
		}
		want := readTaint(ch)
		var same *corev1.Taint
		for i, t := range n.Spec.Taints {
			if t.Key == want.Key && t.Effect == want.Effect {
				same = &n.Spec.Taints[i]
			}
		}
		switch {
		case same != nil && same.Value == want.Value:
			diffs = append(diffs, taintText(want)+" is already there")
			continue
		case same != nil && !c.Has("overwrite"):
			diffs = append(diffs, fmt.Sprintf("it has %s, and --overwrite is false: kubectl will refuse", taintText(*same)))
			continue
		case same != nil:
			diffs = append(diffs, fmt.Sprintf("%s → %s", taintText(*same), taintText(want)))
		default:
			diffs = append(diffs, "adds "+taintText(want))
		}
		switch want.Effect {
		case corev1.TaintEffectNoSchedule:
			out = append(out, fmt.Sprintf("New pods that do not tolerate %s will not be scheduled on %s. Its running pods keep running.", want.Key, n.Name))
		case corev1.TaintEffectPreferNoSchedule:
			out = append(out, fmt.Sprintf("The scheduler avoids %s for new pods that do not tolerate %s.", n.Name, want.Key))
		case corev1.TaintEffectNoExecute:
			pods, _ := cs.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + n.Name})
			var evicted []string
			if pods != nil {
				for _, p := range pods.Items {
					if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
						continue
					}
					tolerated := false
					for _, tol := range p.Spec.Tolerations {
						if tolerates(tol, want) {
							tolerated = true
							break
						}
					}
					if !tolerated {
						evicted = append(evicted, p.Namespace+"/"+p.Name)
					}
				}
			}
			sort.Strings(evicted)
			if len(evicted) == 0 {
				out = append(out, fmt.Sprintf("Every pod on %s tolerates %s; none is evicted.", n.Name, want.Key))
			} else {
				out = append(out, fmt.Sprintf("Evicts the %s on %s that do not tolerate %s: %s.", plural(len(evicted), "pod", "pods"), n.Name, want.Key, listText(evicted, 5)))
			}
		}
	}
	return append([]string{"Node " + n.Name + ": " + strings.Join(diffs, "; ") + "."}, out...)
}

// tolerates says whether a toleration matches a taint, as the scheduler
// reads it: an empty key with Exists matches every taint.
func tolerates(tol corev1.Toleration, t corev1.Taint) bool {
	if tol.Effect != "" && tol.Effect != t.Effect {
		return false
	}
	if tol.Key != "" && tol.Key != t.Key {
		return false
	}
	switch tol.Operator {
	case corev1.TolerationOpExists:
		return true
	case "", corev1.TolerationOpEqual:
		return tol.Key != "" && tol.Value == t.Value
	}
	return false
}

func labelImpact(ctx context.Context, cs *clients, c *Command, t target, changes []string, annot bool) []string {
	cur := t.obj.GetLabels()
	word := "label"
	if annot {
		cur, word = t.obj.GetAnnotations(), "annotation"
	}
	after := map[string]string{}
	for k, v := range cur {
		after[k] = v
	}
	var diffs, out []string
	for _, ch := range changes {
		if k, v, ok := strings.Cut(ch, "="); ok {
			old, has := cur[k]
			switch {
			case has && old == v:
				diffs = append(diffs, k+" is already "+v)
			case has && !c.Has("overwrite"):
				diffs = append(diffs, fmt.Sprintf("'%s' already has a value (%s), and --overwrite is false: kubectl will refuse", k, old))
			case has:
				diffs = append(diffs, fmt.Sprintf("%s: %s → %s", k, old, v))
				after[k] = v
			default:
				diffs = append(diffs, "adds "+k+"="+v)
				after[k] = v
			}
			continue
		}
		k := strings.TrimSuffix(ch, "-")
		if old, has := cur[k]; has {
			diffs = append(diffs, "removes "+k+"="+old)
			delete(after, k)
		} else {
			diffs = append(diffs, "has no "+word+" "+k)
		}
	}
	out = append(out, t.title()+": "+strings.Join(diffs, "; ")+".")
	if annot && t.kind == "StorageClass" {
		return append(out, defaultClassImpact(ctx, cs, t.name, changes)...)
	}
	if annot {
		return out
	}
	switch t.kind {
	case "Node":
		out = append(out, nodeSelectorImpact(ctx, cs, t.name, cur, after)...)
	case "Namespace":
		for _, mode := range []string{"enforce", "warn", "audit"} {
			k := psaPrefix + mode
			before, now := cur[k], after[k]
			if before == now {
				continue
			}
			switch {
			case mode == "enforce" && now != "":
				out = append(out, fmt.Sprintf("Pod Security: the API server rejects new pods in %s that break the %s level. Running pods keep running.", t.name, now))
			case mode == "enforce":
				out = append(out, fmt.Sprintf("Pod Security: new pods in %s are no longer checked against a level.", t.name))
			case now != "":
				out = append(out, fmt.Sprintf("Pod Security: pods in %s that break the %s level are %s; nothing is rejected.", t.name, now, map[string]string{"warn": "warned about", "audit": "noted in the audit log"}[mode]))
			}
		}
	}
	return out
}

// nodeSelectorImpact says which workloads that select nodes by label gain
// or lose a node.
func nodeSelectorImpact(ctx context.Context, cs *clients, node string, before, after map[string]string) []string {
	var out []string
	check := func(kind, ns, name string, sel map[string]string) {
		if len(sel) == 0 {
			return
		}
		s := labels.SelectorFromSet(sel)
		was, is := s.Matches(labels.Set(before)), s.Matches(labels.Set(after))
		if was == is {
			return
		}
		id := kind + " " + ns + "/" + name + " selects " + labels.FormatLabels(sel)
		switch {
		case kind == "DaemonSet" && was:
			out = append(out, id+": it will remove its pod from "+node+".")
		case kind == "DaemonSet":
			out = append(out, id+": it will start a pod on "+node+".")
		case was:
			out = append(out, id+": its pods there keep running, but new ones won't be scheduled on "+node+".")
		default:
			out = append(out, id+": its pods can now be scheduled on "+node+".")
		}
	}
	if l, err := cs.cs.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, d := range l.Items {
			check("DaemonSet", d.Namespace, d.Name, d.Spec.Template.Spec.NodeSelector)
		}
	}
	if l, err := cs.cs.AppsV1().Deployments("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, d := range l.Items {
			check("Deployment", d.Namespace, d.Name, d.Spec.Template.Spec.NodeSelector)
		}
	}
	if l, err := cs.cs.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, d := range l.Items {
			check("StatefulSet", d.Namespace, d.Name, d.Spec.Template.Spec.NodeSelector)
		}
	}
	return out
}

func rollbackImpact(ctx context.Context, cs *clients, c *Command) (targets []string, guards []guard, impact []string) {
	ns := helmNamespace(c)
	name := c.Positionals[0]
	targets = []string{"release " + ns + "/" + name}
	read := func(ctx context.Context, cs *clients) ([]*release.Release, error) {
		d := driver.NewSecrets(cs.cs.CoreV1().Secrets(ns))
		res, err := d.Query(map[string]string{"name": name, "owner": "helm"})
		if err != nil {
			return nil, err
		}
		rels, err := v1List(res)
		if err != nil {
			return nil, err
		}
		sort.Slice(rels, func(i, j int) bool { return rels[i].Version > rels[j].Version })
		return rels, nil
	}
	rels, err := read(ctx, cs)
	if err != nil || len(rels) == 0 {
		return targets, nil, []string{fmt.Sprintf("There is no release %s in %s: helm will refuse.", name, ns)}
	}
	cur := rels[0]
	want := cur.Version - 1
	if len(c.Positionals) > 1 {
		if v, err := strconv.Atoi(c.Positionals[1]); err == nil && v > 0 {
			want = v
		}
	}
	var to *release.Release
	for _, r := range rels {
		if r.Version == want {
			to = r
		}
	}
	guards = append(guards, guard{what: "release " + ns + "/" + name, want: digest(cur.Version, cur.Info.Status.String()), read: func(ctx context.Context, cs *clients) (string, error) {
		rels, err := read(ctx, cs)
		if err != nil || len(rels) == 0 {
			return "", err
		}
		return digest(rels[0].Version, rels[0].Info.Status.String()), nil
	}})
	if to == nil {
		return targets, guards, []string{fmt.Sprintf("Release %s/%s has no revision %d: helm will refuse.", ns, name, want)}
	}
	impact = append(impact, fmt.Sprintf("Rolls release %s/%s back from revision %d (%s, app %s, %s) to revision %d (%s, app %s). Helm records it as revision %d.",
		ns, name, cur.Version, chartName(cur.Chart), appVersion(cur.Chart), cur.Info.Status, to.Version, chartName(to.Chart), appVersion(to.Chart), cur.Version+1))
	changed, added, removed := manifestDiff(cur.Manifest, to.Manifest)
	if len(changed)+len(added)+len(removed) == 0 {
		impact = append(impact, "The objects are the same in both revisions: Helm applies them again and records the new revision.")
	}
	if len(changed) > 0 {
		impact = append(impact, fmt.Sprintf("Changes %s: %s.", plural(len(changed), "object", "objects"), listText(changed, 6)))
	}
	if len(added) > 0 {
		impact = append(impact, fmt.Sprintf("Creates %s that revision %d does not have: %s.", plural(len(added), "object", "objects"), cur.Version, listText(added, 6)))
	}
	if len(removed) > 0 {
		impact = append(impact, fmt.Sprintf("Deletes %s that revision %d does not have: %s.", plural(len(removed), "object", "objects"), to.Version, listText(removed, 6)))
	}
	return targets, guards, impact
}

// manifestDiff compares two rendered manifests object by object.
func manifestDiff(from, to string) (changed, added, removed []string) {
	objs := func(m string) map[string]string {
		out := map[string]string{}
		for _, doc := range strings.Split(m, "\n---") {
			var o struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if yaml.Unmarshal([]byte(doc), &o) != nil || o.Kind == "" {
				continue
			}
			out[o.Kind+" "+o.Metadata.Name] = strings.TrimSpace(doc)
		}
		return out
	}
	a, b := objs(from), objs(to)
	for k, v := range b {
		if old, ok := a[k]; !ok {
			added = append(added, k)
		} else if old != v {
			changed = append(changed, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	return changed, added, removed
}
