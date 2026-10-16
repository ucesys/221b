package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// review_cluster (Phase 6 review): how the cluster is set up, as counted
// findings with examples, for the home page's "Find deficiencies" and any
// question about best practices. It reads the same views as the tabs, as
// the person asking, in one call, and judges nothing a tab does not show.

type finding struct {
	Area     string   `json:"area"`
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	Count    int      `json:"count"`
	Examples []string `json:"examples,omitempty"`
	Advice   string   `json:"advice"`
}

func f64(r map[string]any, k string) float64  { v, _ := r[k].(float64); return v }
func flagOf(r map[string]any, k string) bool  { v, _ := r[k].(bool); return v }
func listOf(r map[string]any, k string) []any { v, _ := r[k].([]any); return v }
func mapOf(r map[string]any, k string) map[string]any {
	v, _ := r[k].(map[string]any)
	return v
}

// systemNamespace is one Kubernetes and its add-ons own.
func systemNamespace(ns string) bool {
	return strings.HasPrefix(ns, "kube-") || ns == "kube-system"
}

func (tb *toolbox) viewRows(ctx context.Context, views ...string) (map[string][]map[string]any, error) {
	code, b := tb.get(ctx, "snapshot", url.Values{"views": {strings.Join(views, ",")}})
	if code != http.StatusOK {
		return nil, fmt.Errorf("%s", failure(code, b).Text)
	}
	var snap struct {
		Views map[string]struct {
			Rows []map[string]any `json:"rows"`
		} `json:"views"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	out := map[string][]map[string]any{}
	for v, x := range snap.Views {
		out[v] = x.Rows
	}
	return out, nil
}

func (tb *toolbox) review(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, err := tb.viewRows(ctx, live.ViewPods, live.ViewNodes, live.ViewPolicies, live.ViewNamespaces, live.ViewServices, live.ViewHelm)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	// Access and networks are read separately: a person may not see them.
	extra, _ := tb.viewRows(ctx, live.ViewAccess, live.ViewNetworks)
	onlyNS := str(in, "namespace")
	keepNS := func(ns string) bool {
		if onlyNS != "" {
			return ns == onlyNS
		}
		return !systemNamespace(ns)
	}

	var out []finding
	var refs []sherlock.Ref
	add := func(f finding) {
		if f.Count == 0 {
			return
		}
		if len(f.Examples) > 6 {
			f.Examples = f.Examples[:6]
		}
		out = append(out, f)
	}
	pods := rows[live.ViewPods]
	pol := rows[live.ViewPolicies]
	byPrefix := map[string][]map[string]any{}
	for _, r := range pol {
		p, _, _ := strings.Cut(sget(r, "key"), ":")
		byPrefix[p] = append(byPrefix[p], r)
	}

	// ---- reliability ----
	single := finding{Area: "reliability", Severity: "medium", Title: "Deployments that run a single replica", Advice: "Run at least two replicas of anything that serves traffic, so a node drain or a crash does not take it down."}
	for _, r := range byPrefix["rollout"] {
		if keepNS(sget(r, "namespace")) && f64(r, "replicas") == 1 {
			single.Count++
			single.Examples = append(single.Examples, sget(r, "namespace")+"/"+sget(r, "name"))
		}
	}
	add(single)

	covered := map[string]bool{}
	for _, r := range byPrefix["pdb"] {
		for _, p := range listOf(r, "pods") {
			if pm, ok := p.(map[string]any); ok {
				covered[sget(r, "namespace")+"/"+sget(pm, "name")] = true
			}
		}
	}
	type wl struct{ pods, covered int }
	workloads := map[string]*wl{}
	bare := finding{Area: "reliability", Severity: "low", Title: "Pods no controller manages", Advice: "Run pods through a Deployment, StatefulSet, DaemonSet or Job, so they come back when they or their node fail."}
	failing := finding{Area: "reliability", Severity: "high", Title: "Pods failing now", Advice: "Open each on the Alerts tab and use Troubleshoot."}
	restarting := finding{Area: "reliability", Severity: "medium", Title: "Pods that restarted five times or more", Advice: "Read the previous run's log; set memory limits and probes that match what the application needs."}
	pending := finding{Area: "reliability", Severity: "medium", Title: "Pods waiting to be scheduled", Advice: "Check the scheduler's reason: room on nodes, taints, affinity or volume claims."}
	noReq := finding{Area: "resources", Severity: "medium", Title: "Pods without CPU or memory requests", Advice: "Set requests, so the scheduler places pods where they fit and they are not the first evicted under pressure."}
	noLim := finding{Area: "resources", Severity: "low", Title: "Pods without a memory limit", Advice: "Set a memory limit, so one leaking container cannot starve its node."}
	for _, p := range pods {
		ns := sget(p, "namespace")
		if !keepNS(ns) || flagOf(p, "done") {
			continue
		}
		key := ns + "/" + sget(p, "name")
		owner := sget(p, "ownerKind")
		if owner == "Deployment" || owner == "StatefulSet" {
			k := ns + "/" + owner + "/" + sget(p, "ownerName")
			w := workloads[k]
			if w == nil {
				w = &wl{}
				workloads[k] = w
			}
			w.pods++
			if covered[key] {
				w.covered++
			}
		}
		if owner == "" {
			bare.Count++
			bare.Examples = append(bare.Examples, key)
		}
		switch {
		case sget(p, "tone") == "err":
			failing.Count++
			failing.Examples = append(failing.Examples, key+" ("+sget(p, "status")+")")
			refs = append(refs, ref("Pod", ns, sget(p, "name")))
		case sget(p, "phase") == "Pending":
			pending.Count++
			pending.Examples = append(pending.Examples, key)
		}
		if f64(p, "restarts") >= 5 {
			restarting.Count++
			restarting.Examples = append(restarting.Examples, fmt.Sprintf("%s (%d restarts)", key, int(f64(p, "restarts"))))
		}
		if f64(p, "cpuRequest") == 0 || f64(p, "memoryRequest") == 0 {
			noReq.Count++
			noReq.Examples = append(noReq.Examples, key)
		}
		if f64(p, "memoryLimit") == 0 {
			noLim.Count++
			noLim.Examples = append(noLim.Examples, key)
		}
	}
	noPDB := finding{Area: "reliability", Severity: "medium", Title: "Replicated workloads without a disruption budget", Advice: "Give each a PodDisruptionBudget, so a node drain keeps enough of it running."}
	var keys []string
	for k := range workloads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if w := workloads[k]; w.pods >= 2 && w.covered == 0 {
			noPDB.Count++
			noPDB.Examples = append(noPDB.Examples, k)
		}
	}
	add(failing)
	add(noPDB)
	blocked := finding{Area: "reliability", Severity: "medium", Title: "Nodes a drain is blocked on now", Advice: "See Policies → Disruption budgets: a budget allows no disruption, so upgrades and repairs stall."}
	for _, r := range byPrefix["drain"] {
		if sget(r, "verdict") == "blocked" {
			blocked.Count++
			blocked.Examples = append(blocked.Examples, sget(r, "node")+": "+sget(r, "why"))
		}
	}
	add(blocked)
	add(restarting)
	add(pending)
	add(bare)

	// ---- resources ----
	add(noReq)
	add(noLim)
	noQuota := finding{Area: "resources", Severity: "low", Title: "Namespaces without a ResourceQuota", Advice: "Quotas keep one team or runaway job from taking the cluster."}
	noLR := finding{Area: "resources", Severity: "low", Title: "Namespaces without a LimitRange", Advice: "A LimitRange gives containers default requests and limits when their manifests have none."}
	for _, n := range rows[live.ViewNamespaces] {
		name := sget(n, "name")
		if !keepNS(name) || name == "default" && onlyNS == "" {
			continue
		}
		c := mapOf(n, "counts")
		if f64(c, "pods") == 0 {
			continue
		}
		if f64(c, "resourcequotas") == 0 {
			noQuota.Count++
			noQuota.Examples = append(noQuota.Examples, name)
		}
		if f64(c, "limitranges") == 0 {
			noLR.Count++
			noLR.Examples = append(noLR.Examples, name)
		}
	}
	add(noQuota)
	add(noLR)
	full := finding{Area: "resources", Severity: "medium", Title: "Quotas at 90% or more", Advice: "New pods or rollouts there will be refused soon; raise the quota or trim requests."}
	for _, r := range byPrefix["quota"] {
		for _, it := range listOf(r, "items") {
			if im, ok := it.(map[string]any); ok && f64(im, "pct") >= 90 {
				full.Count++
				full.Examples = append(full.Examples, fmt.Sprintf("%s/%s %s at %.0f%%", sget(r, "namespace"), sget(r, "name"), sget(im, "resource"), f64(im, "pct")))
			}
		}
	}
	add(full)
	stalls := finding{Area: "resources", Severity: "medium", Title: "Rollouts a quota would stall", Advice: "The surge pods of the next rollout do not fit in the namespace's quota."}
	for _, r := range byPrefix["rollout"] {
		if flagOf(r, "stalls") && keepNS(sget(r, "namespace")) {
			stalls.Count++
			stalls.Examples = append(stalls.Examples, sget(r, "namespace")+"/"+sget(r, "name"))
		}
	}
	add(stalls)

	// ---- security ----
	enforces := true
	for _, r := range byPrefix["cni"] {
		if sget(r, "enforces") == "no" {
			enforces = false
			add(finding{Area: "security", Severity: "high", Title: "The network plugin does not enforce NetworkPolicies", Count: 1, Examples: []string{sget(r, "name")}, Advice: "Policies there have no effect; use a plugin that enforces them (Calico, Cilium, …)."})
		}
	}
	noNP := finding{Area: "security", Severity: "medium", Title: "Namespaces with pods and no NetworkPolicy", Advice: "Every pod there accepts traffic from anywhere in the cluster; add a default-deny policy and allow what is needed."}
	noPSA := finding{Area: "security", Severity: "medium", Title: "Namespaces without Pod Security enforcement", Advice: "Label them pod-security.kubernetes.io/enforce=baseline (or restricted) once their pods pass."}
	failBase := finding{Area: "security", Severity: "high", Title: "Pods that break the baseline Pod Security level", Advice: "These run privileged, share the host or add capabilities; fix them or isolate them."}
	hostNet := finding{Area: "security", Severity: "medium", Title: "Pods on the host network outside the system namespaces", Advice: "They bypass NetworkPolicies and see the node's traffic."}
	for _, r := range byPrefix["ns"] {
		name := sget(r, "name")
		if !keepNS(name) || f64(r, "pods") == 0 {
			continue
		}
		if f64(r, "policies") == 0 && enforces {
			noNP.Count++
			noNP.Examples = append(noNP.Examples, name)
		}
		if sget(r, "enforce") == "" || sget(r, "enforce") == "privileged" {
			noPSA.Count++
			noPSA.Examples = append(noPSA.Examples, name)
		}
		for _, f := range listOf(mapOf(r, "baseline"), "fail") {
			if fm, ok := f.(map[string]any); ok {
				failBase.Count++
				failBase.Examples = append(failBase.Examples, name+"/"+sget(fm, "name"))
			}
		}
		if n := f64(r, "hostNetwork"); n > 0 {
			hostNet.Count += int(n)
			hostNet.Examples = append(hostNet.Examples, fmt.Sprintf("%s (%d)", name, int(n)))
		}
	}
	add(failBase)
	add(noNP)
	add(noPSA)
	add(hostNet)
	rbac := map[string]*finding{}
	for _, r := range extra[live.ViewAccess] {
		for _, f := range listOf(r, "findings") {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			sev := sget(fm, "severity")
			if sev != "high" && sev != "medium" {
				continue
			}
			fd := rbac[sev]
			if fd == nil {
				fd = &finding{Area: "security", Severity: sev, Title: "RBAC grants to review (" + sev + ")", Advice: "See the Access tab: narrow the role or bind it in one namespace."}
				rbac[sev] = fd
			}
			fd.Count++
			fd.Examples = append(fd.Examples, sget(fm, "title"))
		}
	}
	for _, sev := range []string{"high", "medium"} {
		if fd := rbac[sev]; fd != nil {
			add(*fd)
		}
	}

	// ---- platform ----
	nodes := rows[live.ViewNodes]
	down := finding{Area: "platform", Severity: "high", Title: "Nodes with errors", Advice: "Open them on the Nodes tab."}
	warn := finding{Area: "platform", Severity: "medium", Title: "Nodes with warnings", Advice: "Pressure, cordons or kubelet trouble; open them on the Nodes tab."}
	versions := map[string]int{}
	cp := 0
	for _, n := range nodes {
		name := sget(n, "name")
		if f64(n, "errors") > 0 {
			down.Count++
			down.Examples = append(down.Examples, name+" ("+sget(n, "status")+")")
			refs = append(refs, ref("Node", "", name))
		} else if f64(n, "warnings") > 0 {
			warn.Count++
			warn.Examples = append(warn.Examples, name)
		}
		versions[sget(n, "kubelet")]++
		if sget(n, "role") == "control-plane" {
			cp++
		}
	}
	add(down)
	add(warn)
	if len(versions) > 1 {
		var vs []string
		for v, n := range versions {
			vs = append(vs, fmt.Sprintf("%s on %d", v, n))
		}
		sort.Strings(vs)
		add(finding{Area: "platform", Severity: "low", Title: "Nodes run different kubelet versions", Count: len(versions), Examples: vs, Advice: "Finish the upgrade; keep kubelets within the skew the control plane supports."})
	}
	if cp == 1 && len(nodes) > 1 {
		add(finding{Area: "platform", Severity: "low", Title: "A single control-plane node", Count: 1, Advice: "One control-plane node is a single point of failure; three survive the loss of one."})
	}
	hooks := finding{Area: "platform", Severity: "high", Title: "Admission webhooks that fail closed and cannot be reached", Advice: "With failurePolicy Fail and no ready endpoint, the requests they match are refused."}
	slow := finding{Area: "platform", Severity: "low", Title: "Admission webhooks with timeouts over 10 seconds", Advice: "A slow webhook slows every request it matches."}
	for _, r := range byPrefix["hook"] {
		reach := sget(mapOf(r, "reach"), "state")
		if sget(r, "failurePolicy") == "Fail" && reach != "" && reach != "ok" && reach != "ready" {
			hooks.Count++
			hooks.Examples = append(hooks.Examples, sget(r, "webhook")+" ("+reach+")")
		}
		if f64(r, "timeout") > 10 {
			slow.Count++
			slow.Examples = append(slow.Examples, sget(r, "webhook"))
		}
	}
	add(hooks)
	add(slow)
	lb := finding{Area: "platform", Severity: "medium", Title: "LoadBalancer Services without an address", Advice: "No load-balancer controller answered; see the Service's events."}
	noEP := finding{Area: "platform", Severity: "medium", Title: "Services with no ready endpoint", Advice: "Their pods are missing or not ready, so requests to them fail."}
	for _, s := range rows[live.ViewServices] {
		ns := sget(s, "namespace")
		if !keepNS(ns) {
			continue
		}
		key := ns + "/" + sget(s, "name")
		if flagOf(s, "pending") {
			lb.Count++
			lb.Examples = append(lb.Examples, key)
		}
		if flagOf(s, "selector") && f64(s, "ready") == 0 {
			noEP.Count++
			noEP.Examples = append(noEP.Examples, key)
		}
	}
	add(noEP)
	add(lb)
	rel := finding{Area: "platform", Severity: "medium", Title: "Helm releases not deployed", Advice: "A failed or pending release leaves its resources half changed; see the Helm tab's history."}
	for _, h := range rows[live.ViewHelm] {
		if st := sget(h, "status"); st != "deployed" && keepNS(sget(h, "namespace")) {
			rel.Count++
			rel.Examples = append(rel.Examples, sget(h, "namespace")+"/"+sget(h, "name")+" ("+st+")")
		}
	}
	add(rel)
	for _, r := range extra[live.ViewNetworks] {
		if dns := mapOf(r, "dns"); dns != nil && f64(dns, "total") > 0 && f64(dns, "ready") < 2 {
			add(finding{Area: "platform", Severity: "medium", Title: "Cluster DNS has fewer than two ready pods", Count: 1, Examples: []string{sget(dns, "service")}, Advice: "Every name lookup depends on it; run at least two replicas on different nodes."})
		}
	}

	rank := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	counts := map[string]int{}
	for _, f := range out {
		counts[f.Severity]++
	}
	scope := "every namespace but the system ones"
	if onlyNS != "" {
		scope = "namespace " + onlyNS
	}
	res := map[string]any{"scope": scope, "nodes": len(nodes), "pods": len(pods), "findings": out, "bySeverity": counts}
	if len(out) == 0 {
		res["note"] = "No finding: nothing these checks look for, in what the person may see."
	}
	b, _ := json.Marshal(res)
	if len(refs) > 20 {
		refs = refs[:20]
	}
	return sherlock.ToolResult{Text: string(sherlock.ScrubJSON(b)), Refs: refs}
}
