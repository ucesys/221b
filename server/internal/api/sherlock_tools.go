package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/rbac"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/views"
)

// Sherlock's tools (P5-02) read the cluster as the person asking: most of
// them are the console's own API, called in-process with the person's
// session, so they see exactly what the person's tabs show and nothing
// more. None of them writes (P5-07); TestSherlockToolsOnlyRead checks
// each one is a read. Sherlock's other tools, the UI actions
// (sherlock/ui.go), change only the asking person's own view.

type toolbox struct {
	s       *Server
	sess    auth.Session
	hasSess bool
	cluster string
	cl      Cluster
}

func (s *Server) toolboxFor(r *http.Request, cluster string, cl Cluster) *toolbox {
	sess, ok := auth.SessionFrom(r.Context())
	return &toolbox{s: s, sess: sess, hasSess: ok, cluster: cluster, cl: cl}
}

// tool is one of Sherlock's tools.
type tool struct {
	def   sherlock.ToolDef
	label func(in map[string]any) string
	run   func(tb *toolbox, ctx context.Context, in map[string]any) sherlock.ToolResult
}

func toolSchema(required []string, props map[string]string) json.RawMessage {
	p := map[string]any{}
	for name, spec := range props {
		typ, desc, _ := strings.Cut(spec, "|")
		prop := map[string]any{"description": desc}
		switch {
		case strings.HasPrefix(typ, "enum:"):
			prop["type"] = "string"
			prop["enum"] = strings.Split(strings.TrimPrefix(typ, "enum:"), ",")
		default:
			prop["type"] = typ
		}
		p[name] = prop
	}
	if required == nil {
		required = []string{}
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false})
	return b
}

func str(in map[string]any, k string) string {
	v, _ := in[k].(string)
	return strings.TrimSpace(v)
}

func num(in map[string]any, k string, def, lo, hi int) int {
	switch v := in[k].(type) {
	case float64:
		def = int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			def = n
		}
	}
	return max(lo, min(hi, def))
}

func flag(in map[string]any, k string) bool {
	v, _ := in[k].(bool)
	return v
}

func nsName(in map[string]any) string {
	if ns := str(in, "namespace"); ns != "" {
		return ns + "/" + str(in, "name")
	}
	return str(in, "name")
}

// sherlockTools is the toolset. Every entry reads.
var sherlockTools = []tool{
	{
		def:   sherlock.ToolDef{Name: "cluster_overview", Description: "A summary of the cluster: its version, nodes ready and not, CPU and memory requested and in use, pods, namespaces and the alerts on now. Start here for broad questions.", InputSchema: toolSchema(nil, map[string]string{})},
		label: func(map[string]any) string { return "Read the cluster overview" },
		run:   (*toolbox).overview,
	},
	{
		def: sherlock.ToolDef{Name: "list_alerts", Description: "What needs attention: pods failing to start or pending (with the scheduler's reason), recently recovered pods, node problems and volume claims waiting, each with its recent warning events. Acknowledged alerts say who acknowledged them.", InputSchema: toolSchema(nil, map[string]string{
			"kind": "enum:pod,node,pvc|Only alerts of this kind (pvc: volume claims).", "namespace": "string|Only this namespace.", "include_recovered": "boolean|Include pods that failed and recovered (default false).",
		})},
		label: func(in map[string]any) string { return "Read the alerts" + suffixIf(" in ", str(in, "namespace")) },
		run:   (*toolbox).alerts,
	},
	{
		def: sherlock.ToolDef{Name: "list_nodes", Description: "Nodes with role, status, problems, taints, zone, kubelet version, pods, and CPU and memory requested, allocatable and in use (millicores and bytes).", InputSchema: toolSchema(nil, map[string]string{
			"query": "string|Only nodes whose row contains this text (a name, label, taint or status).", "with_labels": "boolean|Include each node's labels.", "limit": "integer|At most this many (default 60).",
		})},
		label: func(in map[string]any) string { return "List nodes" + suffixIf(" matching ", str(in, "query")) },
		run:   (*toolbox).nodes,
	},
	{
		def:   sherlock.ToolDef{Name: "get_node", Description: "One node in full: conditions, problems, taints, labels, capacity and allocatable, requested and in use, pods by state, and its recent events.", InputSchema: toolSchema([]string{"name"}, map[string]string{"name": "string|The node's name."})},
		label: func(in map[string]any) string { return "Read node " + str(in, "name") },
		run:   (*toolbox).node,
	},
	{
		def: sherlock.ToolDef{Name: "list_pods", Description: "Pods with status, readiness, restarts, last termination reason, node, owner, age, and CPU and memory in use (from metrics-server).", InputSchema: toolSchema(nil, map[string]string{
			"namespace": "string|Only this namespace.", "node": "string|Only pods on this node.", "status": "string|Only pods whose status contains this (Running, Pending, CrashLoopBackOff, …).", "query": "string|Only pods whose row contains this text.", "limit": "integer|At most this many (default 60).",
		})},
		label: func(in map[string]any) string {
			return "List pods" + suffixIf(" in ", str(in, "namespace")) + suffixIf(" on ", str(in, "node")) + suffixIf(" matching ", firstOf(str(in, "status"), str(in, "query")))
		},
		run: (*toolbox).pods,
	},
	{
		def:   sherlock.ToolDef{Name: "get_pod", Description: "One pod in full: containers and their states, last termination, environment (references, never Secret values), volumes, conditions, scheduling, owner chain, usage and recent events.", InputSchema: toolSchema([]string{"namespace", "name"}, map[string]string{"namespace": "string|The pod's namespace.", "name": "string|The pod's name."})},
		label: func(in map[string]any) string { return "Read pod " + nsName(in) },
		run:   (*toolbox).pod,
	},
	{
		def: sherlock.ToolDef{Name: "pod_logs", Description: "The last lines of a container's log (at most 200), with credentials replaced by [REDACTED]. Set previous to read the run before the last restart, which is where a crash's cause usually is.", InputSchema: toolSchema([]string{"namespace", "name"}, map[string]string{
			"namespace": "string|The pod's namespace.", "name": "string|The pod's name.", "container": "string|The container; the first when not given.", "previous": "boolean|Read the previous run's log.", "tail": "integer|How many lines (default 80, at most 200).",
		})},
		label: func(in map[string]any) string {
			l := "Read the log of " + nsName(in)
			if flag(in, "previous") {
				l = "Read the previous run's log of " + nsName(in)
			}
			return l + suffixIf(", container ", str(in, "container"))
		},
		run: (*toolbox).logs,
	},
	{
		def: sherlock.ToolDef{Name: "list_events", Description: "Kubernetes events, newest first, with count, first and last seen.", InputSchema: toolSchema(nil, map[string]string{
			"namespace": "string|Only this namespace.", "object": "string|Only events about objects with this name.", "kind": "string|Only events about this kind (Pod, Node, Deployment, …).", "warnings_only": "boolean|Only warnings.", "limit": "integer|At most this many (default 40).",
		})},
		label: func(in map[string]any) string {
			return "Read the events" + suffixIf(" of ", str(in, "object")) + suffixIf(" in ", str(in, "namespace"))
		},
		run: (*toolbox).events,
	},
	{
		def: sherlock.ToolDef{Name: "list_objects", Description: "Everything a namespace holds, by kind (Deployments, StatefulSets, Jobs, ConfigMaps, Secrets by key name only, PVCs, Ingresses, NetworkPolicies, Roles, …), each with its status.", InputSchema: toolSchema([]string{"namespace"}, map[string]string{
			"namespace": "string|The namespace.", "kind": "string|Only this kind.", "query": "string|Only objects whose row contains this text.", "limit": "integer|At most this many (default 80).",
		})},
		label: func(in map[string]any) string {
			return "List what " + str(in, "namespace") + " holds" + suffixIf(": ", str(in, "kind"))
		},
		run: (*toolbox).objects,
	},
	{
		def:   sherlock.ToolDef{Name: "get_object", Description: "One object in full by kind: replicas and rollout, schedule, rules, capacity, keys and sizes (never values), conditions, labels, annotations, owner chain, events, its Helm release, and the pods it controls.", InputSchema: toolSchema([]string{"kind", "namespace", "name"}, map[string]string{"kind": "string|The kind, such as Deployment or PersistentVolumeClaim.", "namespace": "string|Its namespace.", "name": "string|Its name."})},
		label: func(in map[string]any) string { return "Read " + str(in, "kind") + " " + nsName(in) },
		run:   (*toolbox).object,
	},
	{
		def:   sherlock.ToolDef{Name: "get_yaml", Description: "An object's manifest as YAML, read live, without managed fields; a Secret's values are replaced by their sizes. Use for spec details the other tools do not show.", InputSchema: toolSchema([]string{"kind", "name"}, map[string]string{"kind": "string|The kind.", "namespace": "string|Its namespace, for a namespaced kind.", "name": "string|Its name."})},
		label: func(in map[string]any) string { return "Read the YAML of " + str(in, "kind") + " " + nsName(in) },
		run:   (*toolbox).yaml,
	},
	{
		def:   sherlock.ToolDef{Name: "list_services", Description: "Services with type, cluster IP, external addresses (or why a LoadBalancer is pending), ports, endpoints ready and not, and the hostnames that publish them.", InputSchema: toolSchema(nil, map[string]string{"namespace": "string|Only this namespace.", "query": "string|Only Services whose row contains this text.", "limit": "integer|At most this many (default 60)."})},
		label: func(in map[string]any) string { return "List Services" + suffixIf(" in ", str(in, "namespace")) },
		run:   (*toolbox).services,
	},
	{
		def:   sherlock.ToolDef{Name: "get_service", Description: "One Service in full: ports, endpoints with the pods and nodes behind them, Ingresses that publish it, events, and why a LoadBalancer has no address.", InputSchema: toolSchema([]string{"namespace", "name"}, map[string]string{"namespace": "string|Its namespace.", "name": "string|Its name."})},
		label: func(in map[string]any) string { return "Read Service " + nsName(in) },
		run:   (*toolbox).service,
	},
	{
		def:   sherlock.ToolDef{Name: "list_helm_releases", Description: "Helm releases with status, chart, app version, revision and when last updated.", InputSchema: toolSchema(nil, map[string]string{"namespace": "string|Only this namespace.", "query": "string|Only releases whose row contains this text."})},
		label: func(in map[string]any) string { return "List Helm releases" + suffixIf(" in ", str(in, "namespace")) },
		run:   (*toolbox).releases,
	},
	{
		def:   sherlock.ToolDef{Name: "get_helm_release", Description: "One Helm release: its history with each revision's status and description (a failed upgrade says why), notes, and its resources with their state. Values are not shown.", InputSchema: toolSchema([]string{"namespace", "name"}, map[string]string{"namespace": "string|Its namespace.", "name": "string|The release's name."})},
		label: func(in map[string]any) string { return "Read Helm release " + nsName(in) },
		run:   (*toolbox).release,
	},
	{
		def:   sherlock.ToolDef{Name: "top", Description: "The pods or nodes using the most CPU or memory now, from metrics-server.", InputSchema: toolSchema([]string{"of", "by"}, map[string]string{"of": "enum:pods,nodes|Pods or nodes.", "by": "enum:cpu,memory|Sort by CPU or memory.", "namespace": "string|Only pods in this namespace.", "limit": "integer|How many (default 15)."})},
		label: func(in map[string]any) string { return "Rank " + str(in, "of") + " by " + str(in, "by") + " in use" },
		run:   (*toolbox).top,
	},
	{
		def:   sherlock.ToolDef{Name: "list_fleets", Description: "The fleets (groups of nodes by labels and taints, or lists of nodes by name kept in the console) with their members, capacity and problems, and the nodes in no fleet.", InputSchema: toolSchema(nil, map[string]string{})},
		label: func(map[string]any) string { return "Read the fleets" },
		run:   (*toolbox).fleets,
	},
	{
		// ST-22: the Storage tab's classes, as the person may read them.
		def:   sherlock.ToolDef{Name: "list_storage_classes", Description: "The storage classes: provisioner, reclaim policy, binding mode, whether they allow expansion, which is the default, parameters, and their totals: volumes, capacity provisioned and used, claims and claims pending.", InputSchema: toolSchema(nil, map[string]string{})},
		label: func(map[string]any) string { return "Read the storage classes" },
		run:   (*toolbox).storageClasses,
	},
	{
		// ST-22: the Storage tab's rows, with the diagnosis of ST-14.
		def: sherlock.ToolDef{Name: "list_volumes", Description: "Persistent volumes and the claims that hold them, as pairs: status and, when not healthy, why (provisioning failed and the provisioner's message, no such class, waits for a pod, no volume matches, Released, Lost, resizing), capacity or the size requested, usage from the kubelets, access modes, class, reclaim policy, the Helm release, and the pods that mount each with their state and node.",
			InputSchema: toolSchema(nil, map[string]string{"namespace": "string|Only claims in this namespace.", "class": "string|Only this storage class (an empty string: volumes made by hand).", "needs_attention": "boolean|Only what needs attention: claims pending with a warning or naming a class that does not exist, lost claims, Released or Failed volumes, volumes 85% full or more.", "query": "string|Only rows whose text contains this.", "limit": "integer|How many (default 60)."})},
		label: func(in map[string]any) string {
			return "List volumes and claims" + suffixIf(" in ", str(in, "namespace"))
		},
		run: (*toolbox).volumes,
	},
	{
		def:   sherlock.ToolDef{Name: "trace_ingress", Description: "The path of a request to an Ingress host, hop by hop: address, Ingress controller, TLS certificate, rule, Service, endpoints, network policy, pods, each with its state and the cause when it fails. Also the network facts: plugin, DNS, IngressClasses.", InputSchema: toolSchema(nil, map[string]string{"host": "string|Only routes for this hostname (or part of it)."})},
		label: func(in map[string]any) string { return "Trace requests" + suffixIf(" to ", str(in, "host")) },
		run:   (*toolbox).trace,
	},
	{
		def: sherlock.ToolDef{Name: "check_connection", Description: "Whether the NetworkPolicies let a pod connect to another pod (or to the pods behind a Service) on a port, and which policies decide.", InputSchema: toolSchema([]string{"from", "to", "port"}, map[string]string{"from": "string|The client pod as namespace/name.", "to": "string|The server pod as namespace/name, or a Service as namespace/name with to_service true.", "to_service": "boolean|to is a Service.", "port": "integer|The port number.", "protocol": "enum:TCP,UDP,SCTP|Default TCP."})},
		label: func(in map[string]any) string {
			return fmt.Sprintf("Check whether %s may connect to %s on port %v", str(in, "from"), str(in, "to"), in["port"])
		},
		run: (*toolbox).netcheck,
	},
	{
		def: sherlock.ToolDef{Name: "list_policies", Description: "Policies by section: netpol (NetworkPolicies and the plugin), pdb (disruption budgets and how many pods each lets go), drain (whether each node can be drained now and what blocks it), quota (quotas used against hard and rollouts they would stall), limits (LimitRanges), psa (Pod Security levels per namespace and the pods a stricter level rejects), webhooks (admission webhooks: failure policy, timeout, reachability), priority (priority classes).", InputSchema: toolSchema([]string{"section"}, map[string]string{"section": "enum:netpol,pdb,drain,quota,limits,psa,webhooks,priority|Which section.", "namespace": "string|Only this namespace.", "query": "string|Only rows containing this text (a node, a name)."})},
		label: func(in map[string]any) string {
			return "Read the policies: " + str(in, "section") + suffixIf(" in ", str(in, "namespace"))
		},
		run: (*toolbox).policies,
	},
	{
		def: sherlock.ToolDef{Name: "who_can", Description: "Who may do a verb on a resource, from the RBAC bindings the person may see: each subject with the binding and role that allow it.", InputSchema: toolSchema([]string{"verb", "resource"}, map[string]string{"verb": "string|get, list, watch, create, update, patch, delete, escalate, bind, impersonate, …", "resource": "string|The resource, plural (pods, secrets, nodes, deployments) or with a subresource (pods/exec).", "group": "string|The API group: \"\" for core, apps, batch, rbac.authorization.k8s.io, …", "namespace": "string|In this namespace; empty for cluster-wide."})},
		label: func(in map[string]any) string {
			return "Find who can " + str(in, "verb") + " " + str(in, "resource") + suffixIf(" in ", str(in, "namespace"))
		},
		run: (*toolbox).whoCan,
	},
	{
		def: sherlock.ToolDef{Name: "list_access", Description: "RBAC as the Access tab has it: bindings with their subjects, roles and risk findings (cluster-admin, wildcards, reading Secrets, escalate, bind, impersonate), roles with their rules, and ServiceAccounts with their pods and long-lived tokens.", InputSchema: toolSchema(nil, map[string]string{"what": "enum:bindings,roles,serviceaccounts,findings|Which rows (default findings: bindings with something to review).", "namespace": "string|Only this namespace.", "query": "string|Only rows containing this text (a subject, a role).", "limit": "integer|At most this many (default 50)."})},
		label: func(in map[string]any) string {
			return "Read the access rules" + suffixIf(" matching ", str(in, "query"))
		},
		run: (*toolbox).access,
	},
	{
		def: sherlock.ToolDef{Name: "review_cluster", Description: "A review of how the cluster is set up, as counted findings with examples and advice, most severe first: reliability (failing and restarting pods, single replicas, workloads without disruption budgets, blocked drains), resources (missing requests and limits, quotas, LimitRanges), security (NetworkPolicies, Pod Security, host networking, RBAC grants) and platform health (nodes, kubelet versions, admission webhooks, Services, Helm releases, DNS). Use it first for questions about deficiencies, best practices or the cluster's overall health.", InputSchema: toolSchema(nil, map[string]string{"namespace": "string|Only this namespace; by default every namespace but the system ones."})},
		label: func(in map[string]any) string {
			return "Review the cluster's setup" + suffixIf(" in ", str(in, "namespace"))
		},
		run: (*toolbox).review,
	},
}

func suffixIf(prefix, v string) string {
	if v == "" {
		return ""
	}
	return prefix + v
}

var toolsByName = func() map[string]tool {
	m := map[string]tool{}
	for _, t := range sherlockTools {
		m[t.def.Name] = t
	}
	return m
}()

// Defs lists the tools.
func (tb *toolbox) Defs() []sherlock.ToolDef {
	out := make([]sherlock.ToolDef, len(sherlockTools))
	for i, t := range sherlockTools {
		out[i] = t.def
	}
	return out
}

func decodeInput(raw json.RawMessage) map[string]any {
	in := map[string]any{}
	_ = json.Unmarshal(raw, &in)
	return in
}

// Label says what a call does.
func (tb *toolbox) Label(name string, raw json.RawMessage) string {
	t, ok := toolsByName[name]
	if !ok {
		return "Unknown tool " + name
	}
	return t.label(decodeInput(raw))
}

// Run runs a call; its result is scrubbed of anything that looks like a
// credential before it goes to the model (P5-03).
func (tb *toolbox) Run(ctx context.Context, name string, raw json.RawMessage) sherlock.ToolResult {
	t, ok := toolsByName[name]
	if !ok {
		return sherlock.ToolResult{Text: "There is no tool named " + name + ".", Error: true}
	}
	r := t.run(tb, ctx, decodeInput(raw))
	return r
}

// ---------- calling the console's own API as the person ----------

func (tb *toolbox) path(p string) string {
	if tb.cluster == "" || tb.cluster == clusters.LocalID {
		return "/api/v1/" + p
	}
	return "/api/v1/clusters/" + url.PathEscape(tb.cluster) + "/" + p
}

// get calls a GET path of the console as the person asking.
func (tb *toolbox) get(ctx context.Context, p string, q url.Values) (int, []byte) {
	u := tb.path(p)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	if tb.hasSess {
		ctx = auth.WithSession(ctx, tb.sess)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://221b.internal"+u, nil)
	if err != nil {
		return http.StatusBadRequest, []byte(`{"error":"bad request"}`)
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	tb.s.mux.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, b
}

func pathEsc(parts ...string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = url.PathEscape(p)
	}
	return strings.Join(out, "/")
}

// failure turns an error answer into a tool result.
func failure(code int, b []byte) sherlock.ToolResult {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	msg := e.Error
	if msg == "" {
		msg = strings.TrimSpace(string(b))
	}
	switch code {
	case http.StatusForbidden:
		return sherlock.ToolResult{Text: "Not allowed: " + msg + ". The person asking may not see this.", Error: true}
	case http.StatusNotFound:
		return sherlock.ToolResult{Text: "Not found: " + msg + ".", Error: true}
	}
	return sherlock.ToolResult{Text: fmt.Sprintf("Failed (%d): %s", code, msg), Error: true}
}

func (tb *toolbox) detail(ctx context.Context, p string, q url.Values, refs ...sherlock.Ref) sherlock.ToolResult {
	code, b := tb.get(ctx, p, q)
	if code != http.StatusOK {
		return failure(code, b)
	}
	return sherlock.ToolResult{Text: string(sherlock.ScrubJSON(b)), Refs: refs}
}

// rows reads a view's rows, as the person may see them.
func (tb *toolbox) rows(ctx context.Context, view string) ([]map[string]any, map[string]json.RawMessage, error) {
	code, b := tb.get(ctx, "snapshot", url.Values{"views": {view}})
	if code != http.StatusOK {
		r := failure(code, b)
		return nil, nil, fmt.Errorf("%s", r.Text)
	}
	var snap struct {
		Views map[string]struct {
			Rows  []map[string]any           `json:"rows"`
			Usage map[string]json.RawMessage `json:"usage"`
		} `json:"views"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, nil, err
	}
	v := snap.Views[view]
	return v.Rows, v.Usage, nil
}

func rowText(r map[string]any) string {
	b, _ := json.Marshal(r)
	return strings.ToLower(string(b))
}

func matches(r map[string]any, query string) bool {
	return query == "" || strings.Contains(rowText(r), strings.ToLower(query))
}

func drop(r map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		delete(r, k)
	}
	return r
}

func sget(r map[string]any, k string) string {
	v, _ := r[k].(string)
	return v
}

// listResult writes rows as a result: how many there are, and at most
// limit of them.
func listResult(what string, rows []map[string]any, limit int, refs []sherlock.Ref) sherlock.ToolResult {
	total := len(rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := map[string]any{"total": total, what: rows}
	if total > len(rows) {
		out["note"] = fmt.Sprintf("Only the first %d of %d are shown; narrow the request.", len(rows), total)
	}
	if total == 0 {
		out["note"] = "None: nothing matches, or the person may not see any."
	}
	b, _ := json.Marshal(out)
	return sherlock.ToolResult{Text: string(sherlock.ScrubJSON(b)), Refs: refs}
}

func ref(kind, ns, name string) sherlock.Ref {
	return sherlock.Ref{Kind: kind, Namespace: ns, Name: name}
}

// ---------- the tools ----------

func (tb *toolbox) overview(ctx context.Context, _ map[string]any) sherlock.ToolResult {
	h := homeCluster{ID: tb.cluster}
	if tb.s.opts.Clusters != nil {
		if sum, ok := tb.s.opts.Clusters.Summary(tb.cluster); ok {
			h = homeOf(sum)
		}
	}
	st, _, ok := tb.store()
	if ok {
		req, _ := http.NewRequestWithContext(tb.ctx(ctx), http.MethodGet, "/", nil)
		tb.s.fillHome(req, &h, st, tb.cl)
	}
	b, _ := json.Marshal(h)
	return sherlock.ToolResult{Text: string(b)}
}

func (tb *toolbox) ctx(ctx context.Context) context.Context {
	if tb.hasSess {
		return auth.WithSession(ctx, tb.sess)
	}
	return ctx
}

func (tb *toolbox) store() (*live.Store, Cluster, bool) {
	if tb.s.opts.Clusters == nil {
		st, _ := tb.s.opts.Store.(*live.Store)
		return st, tb.cl, st != nil
	}
	st, client, ok := tb.s.opts.Clusters.Lookup(tb.cluster)
	cl, _ := client.(Cluster)
	return st, cl, ok && st != nil && cl != nil
}

func (tb *toolbox) alerts(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, _, err := tb.rows(ctx, live.ViewAlerts)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	kind, ns, rec := str(in, "kind"), str(in, "namespace"), flag(in, "include_recovered")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		if kind != "" && sget(r, "kind") != kind || ns != "" && sget(r, "namespace") != ns || !rec && sget(r, "severity") == "recovered" {
			continue
		}
		out = append(out, r)
		refs = append(refs, alertRef(r)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return sevRank(sget(out[i], "severity")) < sevRank(sget(out[j], "severity")) })
	return listResult("alerts", out, 60, refs)
}

func sevRank(s string) int {
	switch s {
	case "failed", "error":
		return 0
	case "pending", "warning":
		return 1
	case "recovered":
		return 3
	}
	return 2
}

func alertRef(r map[string]any) []sherlock.Ref {
	name, ns := sget(r, "name"), sget(r, "namespace")
	var out []sherlock.Ref
	switch sget(r, "kind") {
	case "pod":
		out = append(out, ref("Pod", ns, name))
		if n := sget(r, "node"); n != "" {
			out = append(out, ref("Node", "", n))
		}
	case "node":
		out = append(out, ref("Node", "", name))
	case "pvc":
		out = append(out, ref("PersistentVolumeClaim", ns, name))
	}
	return out
}

func (tb *toolbox) nodes(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, _, err := tb.rows(ctx, live.ViewNodes)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	q, labels := str(in, "query"), flag(in, "with_labels")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		if !matches(r, q) {
			continue
		}
		drop(r, "statusTip", "conditions", "roles", "created", "os", "kernel", "runtime", "arch", "tone")
		if !labels {
			delete(r, "labels")
		}
		out = append(out, r)
		refs = append(refs, ref("Node", "", sget(r, "name")))
	}
	return listResult("nodes", out, num(in, "limit", 60, 1, 200), refs)
}

func (tb *toolbox) node(ctx context.Context, in map[string]any) sherlock.ToolResult {
	name := str(in, "name")
	if name == "" {
		return sherlock.ToolResult{Text: "Name the node.", Error: true}
	}
	code, b := tb.get(ctx, "nodes/"+pathEsc(name), nil)
	if code != http.StatusOK {
		return failure(code, b)
	}
	ev := tb.eventList(ctx, map[string]any{"object": name, "kind": "Node", "limit": float64(20)})
	text := `{"node":` + string(sherlock.ScrubJSON(b)) + `,"events":` + ev + `}`
	return sherlock.ToolResult{Text: text, Refs: []sherlock.Ref{ref("Node", "", name)}}
}

func (tb *toolbox) pods(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, usage, err := tb.rows(ctx, live.ViewPods)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	ns, node, status, q := str(in, "namespace"), str(in, "node"), strings.ToLower(str(in, "status")), str(in, "query")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		if ns != "" && sget(r, "namespace") != ns || node != "" && sget(r, "node") != node || status != "" && !strings.Contains(strings.ToLower(sget(r, "status")), status) || !matches(r, q) {
			continue
		}
		drop(r, "uid", "tone", "statusTip", "labels", "images", "ip", "qos")
		if u, ok := usage[sget(r, "key")]; ok {
			var v map[string]any
			_ = json.Unmarshal(u, &v)
			r["usage"] = v
		}
		out = append(out, r)
		refs = append(refs, ref("Pod", sget(r, "namespace"), sget(r, "name")))
	}
	sort.SliceStable(out, func(i, j int) bool { return podRank(out[i]) < podRank(out[j]) })
	return listResult("pods", out, num(in, "limit", 60, 1, 200), refs)
}

// podRank puts pods with problems first.
func podRank(r map[string]any) int {
	switch s := sget(r, "status"); {
	case s == "Running" || s == "Completed":
		return 2
	case s == "":
		return 1
	}
	return 0
}

func (tb *toolbox) pod(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ns, name := str(in, "namespace"), str(in, "name")
	if ns == "" || name == "" {
		return sherlock.ToolResult{Text: "Give the pod's namespace and name.", Error: true}
	}
	code, b := tb.get(ctx, "pods/"+pathEsc(ns, name), nil)
	if code != http.StatusOK {
		return failure(code, b)
	}
	refs := []sherlock.Ref{ref("Pod", ns, name)}
	var d struct {
		Node   string `json:"node"`
		Owners []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"owners"`
		Release string `json:"release"`
	}
	_ = json.Unmarshal(b, &d)
	if d.Node != "" {
		refs = append(refs, ref("Node", "", d.Node))
	}
	for _, o := range d.Owners {
		if objectKind(o.Kind) {
			refs = append(refs, ref(o.Kind, ns, o.Name))
		}
	}
	if d.Release != "" {
		refs = append(refs, ref("HelmRelease", ns, d.Release))
	}
	return sherlock.ToolResult{Text: string(sherlock.ScrubJSON(b)), Refs: refs}
}

// objectKind says whether the console opens objects of a kind by name.
func objectKind(kind string) bool {
	k, ok := kube.KindOf(kind)
	return ok && k.Namespaced
}

func (tb *toolbox) logs(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ns, name := str(in, "namespace"), str(in, "name")
	if ns == "" || name == "" {
		return sherlock.ToolResult{Text: "Give the pod's namespace and name.", Error: true}
	}
	q := url.Values{"tail": {strconv.Itoa(num(in, "tail", 80, 1, 200))}}
	if c := str(in, "container"); c != "" {
		q.Set("container", c)
	}
	if flag(in, "previous") {
		q.Set("previous", "1")
	}
	code, b := tb.get(ctx, "pods/"+pathEsc(ns, name)+"/log", q)
	if code != http.StatusOK {
		return failure(code, b)
	}
	text := sherlock.Scrub(string(b))
	if strings.TrimSpace(text) == "" {
		text = "(the log is empty)"
	}
	return sherlock.ToolResult{Text: text, Refs: []sherlock.Ref{ref("Pod", ns, name)}}
}

func (tb *toolbox) events(ctx context.Context, in map[string]any) sherlock.ToolResult {
	return sherlock.ToolResult{Text: tb.eventList(ctx, in)}
}

// eventList reads events from the cache, keeping those in namespaces the
// person may list events in (and node events only for one who may list
// nodes).
func (tb *toolbox) eventList(ctx context.Context, in map[string]any) string {
	ns, obj, kind, warn := str(in, "namespace"), str(in, "object"), str(in, "kind"), flag(in, "warnings_only")
	limit := num(in, "limit", 40, 1, 200)
	var a *access.Access
	if p := tb.person(); !p.Nobody() && tb.s.opts.Access != nil {
		a = tb.s.opts.Access.For(tb.cluster, tb.cl, p)
		var pairs []access.Pair
		for _, n := range append(namespaceNames(tb.cl), "default") {
			if ns == "" || n == ns {
				pairs = append(pairs, access.Pair{Resource: "events", Namespace: n})
			}
		}
		pairs = append(pairs, access.Pair{Resource: "nodes"})
		if a.Stale(pairs) {
			c, cancel := context.WithTimeout(ctx, prepareTimeout)
			_, _ = a.Prepare(c, pairs, 0)
			cancel()
		}
	}
	type ev struct {
		Type    string     `json:"type"`
		Reason  string     `json:"reason"`
		Object  string     `json:"object"`
		Message string     `json:"message"`
		Count   int32      `json:"count,omitempty"`
		First   *time.Time `json:"first,omitempty"`
		Last    *time.Time `json:"last,omitempty"`
	}
	var list []ev
	for _, o := range tb.cl.Objects("events", ns) {
		e, ok := o.(*corev1.Event)
		if !ok {
			continue
		}
		inv := e.InvolvedObject
		if a != nil {
			if !a.Namespaced("events", e.Namespace) || inv.Kind == "Node" && !a.Cluster("nodes") {
				continue
			}
		}
		if obj != "" && inv.Name != obj || kind != "" && !strings.EqualFold(inv.Kind, kind) || warn && e.Type != corev1.EventTypeWarning {
			continue
		}
		x := ev{Type: e.Type, Reason: e.Reason, Object: strings.Trim(inv.Kind+" "+strings.Trim(inv.Namespace+"/"+inv.Name, "/"), " "), Message: sherlock.Scrub(e.Message), Count: e.Count}
		first, last := e.FirstTimestamp.Time, e.LastTimestamp.Time
		if last.IsZero() {
			last = e.EventTime.Time
		}
		if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
			last = e.Series.LastObservedTime.Time
			x.Count = max(x.Count, e.Series.Count)
		}
		if !first.IsZero() {
			x.First = &first
		}
		if !last.IsZero() {
			x.Last = &last
		}
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool {
		li, lj := list[i].Last, list[j].Last
		if li == nil || lj == nil {
			return lj == nil && li != nil
		}
		return li.After(*lj)
	})
	total := len(list)
	if len(list) > limit {
		list = list[:limit]
	}
	b, _ := json.Marshal(map[string]any{"total": total, "events": list})
	return string(b)
}

func (tb *toolbox) person() kube.Person {
	if tb.s.opts.Auth == nil || tb.s.opts.Access == nil || !tb.hasSess {
		return kube.Person{}
	}
	return tb.s.opts.Impersonation.personOf(tb.sess.User)
}

func (tb *toolbox) objects(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ns := str(in, "namespace")
	if ns == "" {
		return sherlock.ToolResult{Text: "Name the namespace.", Error: true}
	}
	rows, _, err := tb.rows(ctx, live.NamespacePrefix+ns)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	kind, q := str(in, "kind"), str(in, "query")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		if kind != "" && !strings.EqualFold(sget(r, "kind"), kind) || !matches(r, q) {
			continue
		}
		drop(r, "labels", "annotations", "uid", "tone")
		out = append(out, r)
		if k := sget(r, "kind"); objectKind(k) {
			refs = append(refs, ref(k, ns, sget(r, "name")))
		}
	}
	return listResult("objects", out, num(in, "limit", 80, 1, 300), refs)
}

func (tb *toolbox) object(ctx context.Context, in map[string]any) sherlock.ToolResult {
	kind, ns, name := str(in, "kind"), str(in, "namespace"), str(in, "name")
	if kind == "" || ns == "" || name == "" {
		return sherlock.ToolResult{Text: "Give the kind, namespace and name.", Error: true}
	}
	switch strings.ToLower(kind) {
	case "pod", "pods":
		return tb.pod(ctx, in)
	case "service", "services":
		return tb.service(ctx, in)
	}
	var refs []sherlock.Ref
	if k, ok := kube.KindOf(kind); ok {
		refs = append(refs, ref(k.Kind, ns, name))
	} else if k, ok := kube.KindByName(strings.ToLower(kind)); ok {
		refs = append(refs, ref(k.Kind, ns, name))
	}
	return tb.detail(ctx, "objects/"+pathEsc(kind, ns, name), nil, refs...)
}

func (tb *toolbox) yaml(ctx context.Context, in map[string]any) sherlock.ToolResult {
	kind, ns, name := str(in, "kind"), str(in, "namespace"), str(in, "name")
	k, ok := kube.KindOf(kind)
	if !ok {
		k, ok = kube.KindByName(strings.ToLower(kind))
	}
	if !ok {
		return sherlock.ToolResult{Text: "The console does not read the kind " + kind + ".", Error: true}
	}
	p := "yaml/" + pathEsc(k.Name, name)
	if k.Namespaced {
		if ns == "" {
			return sherlock.ToolResult{Text: "Give the namespace.", Error: true}
		}
		p = "yaml/" + pathEsc(k.Name, ns, name)
	}
	code, b := tb.get(ctx, p, nil)
	if code != http.StatusOK {
		return failure(code, b)
	}
	var refs []sherlock.Ref
	if k.Namespaced {
		refs = append(refs, ref(k.Kind, ns, name))
	} else if k.Kind == "Node" {
		refs = append(refs, ref("Node", "", name))
	}
	return sherlock.ToolResult{Text: sherlock.Scrub(string(b)), Refs: refs}
}

func (tb *toolbox) services(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, _, err := tb.rows(ctx, live.ViewServices)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	ns, q := str(in, "namespace"), str(in, "query")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		if ns != "" && sget(r, "namespace") != ns || !matches(r, q) {
			continue
		}
		drop(r, "labels", "annotations", "uid", "tone", "selector")
		out = append(out, r)
		refs = append(refs, ref("Service", sget(r, "namespace"), sget(r, "name")))
	}
	return listResult("services", out, num(in, "limit", 60, 1, 200), refs)
}

func (tb *toolbox) service(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ns, name := str(in, "namespace"), str(in, "name")
	if ns == "" || name == "" {
		return sherlock.ToolResult{Text: "Give the Service's namespace and name.", Error: true}
	}
	return tb.detail(ctx, "services/"+pathEsc(ns, name), nil, ref("Service", ns, name))
}

func (tb *toolbox) releases(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, _, err := tb.rows(ctx, live.ViewHelm)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	ns, q := str(in, "namespace"), str(in, "query")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		if ns != "" && sget(r, "namespace") != ns || !matches(r, q) {
			continue
		}
		out = append(out, r)
		refs = append(refs, ref("HelmRelease", sget(r, "namespace"), sget(r, "name")))
	}
	return listResult("releases", out, 100, refs)
}

func (tb *toolbox) release(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ns, name := str(in, "namespace"), str(in, "name")
	if ns == "" || name == "" {
		return sherlock.ToolResult{Text: "Give the release's namespace and name.", Error: true}
	}
	code, b := tb.get(ctx, "helm/"+pathEsc(ns, name), nil)
	if code != http.StatusOK {
		return failure(code, b)
	}
	// Values are configuration someone typed, credentials among it: they
	// are left out altogether.
	var d map[string]any
	if err := json.Unmarshal(b, &d); err == nil {
		drop(d, "values", "maskedValues")
		b, _ = json.Marshal(d)
	}
	return sherlock.ToolResult{Text: string(sherlock.ScrubJSON(b)), Refs: []sherlock.Ref{ref("HelmRelease", ns, name)}}
}

func (tb *toolbox) top(ctx context.Context, in map[string]any) sherlock.ToolResult {
	of, by, ns := str(in, "of"), str(in, "by"), str(in, "namespace")
	limit := num(in, "limit", 15, 1, 50)
	type entry struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
		Node      string `json:"node,omitempty"`
		CPUMilli  int64  `json:"cpuMilli"`
		MemBytes  int64  `json:"memoryBytes"`
		CPUReq    int64  `json:"cpuRequestedMilli,omitempty"`
		MemReq    int64  `json:"memoryRequestedBytes,omitempty"`
		CPULimit  int64  `json:"cpuLimitMilli,omitempty"`
		MemLimit  int64  `json:"memoryLimitBytes,omitempty"`
		CPUAlloc  int64  `json:"cpuAllocatableMilli,omitempty"`
		MemAlloc  int64  `json:"memoryAllocatableBytes,omitempty"`
	}
	var list []entry
	var refs []sherlock.Ref
	if of == "nodes" {
		rows, _, err := tb.rows(ctx, live.ViewNodes)
		if err != nil {
			return sherlock.ToolResult{Text: err.Error(), Error: true}
		}
		for _, r := range rows {
			cpu, _ := r["cpu"].(map[string]any)
			mem, _ := r["memory"].(map[string]any)
			e := entry{Name: sget(r, "name"), CPUMilli: int64Of(cpu["used"]), MemBytes: int64Of(mem["used"]), CPUReq: int64Of(cpu["requested"]), MemReq: int64Of(mem["requested"]), CPUAlloc: int64Of(cpu["allocatable"]), MemAlloc: int64Of(mem["allocatable"])}
			list = append(list, e)
			refs = append(refs, ref("Node", "", e.Name))
		}
	} else {
		rows, usage, err := tb.rows(ctx, live.ViewPods)
		if err != nil {
			return sherlock.ToolResult{Text: err.Error(), Error: true}
		}
		for _, r := range rows {
			if ns != "" && sget(r, "namespace") != ns {
				continue
			}
			u, ok := usage[sget(r, "key")]
			if !ok {
				continue
			}
			var v struct {
				CPU int64 `json:"cpuMilli"`
				Mem int64 `json:"memoryBytes"`
			}
			_ = json.Unmarshal(u, &v)
			cpu, _ := r["cpu"].(map[string]any)
			mem, _ := r["memory"].(map[string]any)
			e := entry{Name: sget(r, "name"), Namespace: sget(r, "namespace"), Node: sget(r, "node"), CPUMilli: v.CPU, MemBytes: v.Mem, CPUReq: int64Of(cpu["requested"]), MemReq: int64Of(mem["requested"]), CPULimit: int64Of(cpu["limits"]), MemLimit: int64Of(mem["limits"])}
			list = append(list, e)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if by == "memory" {
			return list[i].MemBytes > list[j].MemBytes
		}
		return list[i].CPUMilli > list[j].CPUMilli
	})
	if len(list) > limit {
		list = list[:limit]
	}
	if of != "nodes" {
		for _, e := range list {
			refs = append(refs, ref("Pod", e.Namespace, e.Name))
		}
	}
	note := ""
	if len(list) == 0 {
		note = "No usage figures: metrics-server has not answered, or the person may not see any."
	}
	b, _ := json.Marshal(map[string]any{"by": by, "top": list, "note": note})
	return sherlock.ToolResult{Text: string(b), Refs: refs}
}

func int64Of(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func (tb *toolbox) fleets(ctx context.Context, _ map[string]any) sherlock.ToolResult {
	rows, _, err := tb.rows(ctx, live.ViewFleets)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	return listResult("fleets", rows, 100, nil)
}

// storageRows reads the storage view as the person, so it holds only what
// they may list, as the tab does (ST-20).
func (tb *toolbox) storageRows(ctx context.Context) ([]views.StorageRow, error) {
	rows, _, err := tb.rows(ctx, live.ViewStorage)
	if err != nil {
		return nil, err
	}
	out := make([]views.StorageRow, 0, len(rows))
	for _, r := range rows {
		b, _ := json.Marshal(r)
		var sr views.StorageRow
		if json.Unmarshal(b, &sr) == nil {
			out = append(out, sr)
		}
	}
	return out, nil
}

func (tb *toolbox) storageClasses(ctx context.Context, _ map[string]any) sherlock.ToolResult {
	rows, err := tb.storageRows(ctx)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	type totals struct {
		volumes, claims, pending int
		provisioned, used        int64
	}
	by := map[string]*totals{}
	for _, r := range rows {
		if p := r.Pair; p != nil {
			t := by[p.Class]
			if t == nil {
				t = &totals{}
				by[p.Class] = t
			}
			if p.Volume != "" && p.State != views.StatePending {
				t.volumes++
				t.provisioned += p.Capacity
			}
			if p.Claim != "" {
				t.claims++
			}
			if p.State == views.StatePending {
				t.pending++
			}
			if p.Usage != nil {
				t.used += p.Usage.Used
			}
		}
	}
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		c := r.Class
		if c == nil {
			continue
		}
		t := by[c.Name]
		if t == nil {
			t = &totals{}
		}
		out = append(out, map[string]any{"name": c.Name, "default": c.Default, "provisioner": c.Provisioner, "reclaimPolicy": c.Reclaim, "bindingMode": c.Binding, "allowsExpansion": c.Expansion, "parameters": c.Parameters, "mountOptions": c.MountOptions,
			"volumes": t.volumes, "provisioned": views.Bytes(t.provisioned), "used": views.Bytes(t.used), "claims": t.claims, "claimsPending": t.pending})
		refs = append(refs, ref("StorageClass", "", c.Name))
	}
	if len(out) == 0 && len(rows) > 0 {
		return sherlock.ToolResult{Text: `{"total":0,"note":"The person may not read storage classes: their role is bound to namespaces. list_volumes shows their claims."}`}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return listResult("classes", out, 100, refs)
}

func (tb *toolbox) volumes(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, err := tb.storageRows(ctx)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	ns, query, attention := str(in, "namespace"), str(in, "query"), flag(in, "needs_attention")
	class, byClass := in["class"].(string)
	limit := 60
	if n, ok := in["limit"].(float64); ok && n > 0 {
		limit = int(n)
	}
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		p := r.Pair
		if p == nil || (ns != "" && p.Namespace != ns) || (byClass && p.Class != class) || (attention && !p.Attention) {
			continue
		}
		m := map[string]any{"state": p.State, "status": p.Phase, "class": p.Class}
		if p.Claim != "" {
			m["claim"] = p.Namespace + "/" + p.Claim
			refs = append(refs, ref("PersistentVolumeClaim", p.Namespace, p.Claim))
		}
		if p.Volume != "" && p.State != views.StatePending {
			m["volume"] = p.Volume
		}
		if p.WasClaim != "" {
			m["wasClaim"] = p.WasClaim
		}
		if p.ClassMissing {
			m["classNotFound"] = true
		}
		if p.Reason != nil {
			m["why"] = p.Reason.Text
		}
		if p.Attention {
			m["needsAttention"] = true
		}
		if p.State == views.StatePending {
			m["requested"] = views.Bytes(p.Requested)
		} else if p.Capacity > 0 {
			m["capacity"] = views.Bytes(p.Capacity)
		}
		if u := p.Usage; u != nil {
			m["used"] = fmt.Sprintf("%s of %s (%.0f%%)", views.Bytes(u.Used), views.Bytes(u.Capacity), u.Pct)
		}
		m["access"] = views.AccessText(p.AccessModes)
		if p.Reclaim != "" {
			m["reclaimPolicy"] = p.Reclaim
		}
		if p.Release != "" {
			m["helmRelease"] = p.Release
		}
		if p.Owner != nil {
			m["owner"] = p.Owner.Kind + " " + p.Owner.Name
		}
		if len(p.Pods) > 0 {
			var pods []map[string]any
			for _, x := range p.Pods {
				pm := map[string]any{"pod": x.Name, "status": x.Status}
				if x.Node != "" {
					pm["node"] = x.Node
				}
				if x.MountPath != "" {
					pm["mountPath"] = x.MountPath
				}
				if x.Note != "" {
					pm["note"] = x.Note
				}
				if x.Why != "" {
					pm["scheduler"] = x.Why
				}
				pods = append(pods, pm)
			}
			m["pods"] = pods
		}
		if !matches(m, query) {
			continue
		}
		out = append(out, m)
	}
	// What needs attention first, then by claim or volume.
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i]["needsAttention"] == true, out[j]["needsAttention"] == true
		if ai != aj {
			return ai
		}
		return fmt.Sprint(out[i]["claim"], out[i]["volume"]) < fmt.Sprint(out[j]["claim"], out[j]["volume"])
	})
	return listResult("volumes", out, limit, refs)
}

func (tb *toolbox) trace(ctx context.Context, in map[string]any) sherlock.ToolResult {
	rows, _, err := tb.rows(ctx, live.ViewNetworks)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	host := strings.ToLower(str(in, "host"))
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		key := sget(r, "key")
		if strings.HasPrefix(key, "node:") {
			continue
		}
		if host != "" && !strings.HasPrefix(key, "facts:") && !strings.Contains(strings.ToLower(sget(r, "host")), host) {
			continue
		}
		out = append(out, r)
		if ing := sget(r, "ingress"); ing != "" {
			refs = append(refs, ref("Ingress", sget(r, "namespace"), ing))
		}
		if hops, ok := r["hops"].([]any); ok {
			for _, h := range hops {
				if hm, ok := h.(map[string]any); ok {
					if l, ok := hm["link"].(map[string]any); ok {
						if k := sget(l, "kind"); k != "" {
							refs = append(refs, ref(k, sget(l, "namespace"), sget(l, "name")))
						}
					}
				}
			}
		}
	}
	return listResult("routes", out, 40, refs)
}

func (tb *toolbox) netcheck(ctx context.Context, in map[string]any) sherlock.ToolResult {
	q := url.Values{"from": {str(in, "from")}, "port": {fmt.Sprint(num(in, "port", 0, 0, 65535))}}
	if p := str(in, "protocol"); p != "" {
		q.Set("protocol", p)
	}
	if flag(in, "to_service") {
		q.Set("toService", str(in, "to"))
	} else {
		q.Set("to", str(in, "to"))
	}
	var refs []sherlock.Ref
	if ns, name, ok := strings.Cut(str(in, "from"), "/"); ok {
		refs = append(refs, ref("Pod", ns, name))
	}
	if ns, name, ok := strings.Cut(str(in, "to"), "/"); ok {
		k := "Pod"
		if flag(in, "to_service") {
			k = "Service"
		}
		refs = append(refs, ref(k, ns, name))
	}
	return tb.detail(ctx, "netcheck", q, refs...)
}

var policyPrefixes = map[string][]string{
	"netpol":   {"np", "cni", "ns"},
	"pdb":      {"pdb"},
	"drain":    {"drain"},
	"quota":    {"quota", "rollout"},
	"limits":   {"limits"},
	"psa":      {"ns"},
	"webhooks": {"hook"},
	"priority": {"pc", "pcnone"},
}

var policyKinds = map[string]string{"np": "NetworkPolicy", "pdb": "PodDisruptionBudget", "quota": "ResourceQuota", "limits": "LimitRange", "rollout": "Deployment"}

func (tb *toolbox) policies(ctx context.Context, in map[string]any) sherlock.ToolResult {
	section := str(in, "section")
	prefixes, ok := policyPrefixes[section]
	if !ok {
		return sherlock.ToolResult{Text: "Unknown section " + section + ".", Error: true}
	}
	rows, _, err := tb.rows(ctx, live.ViewPolicies)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	ns, q := str(in, "namespace"), str(in, "query")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range rows {
		prefix, rest, _ := strings.Cut(sget(r, "key"), ":")
		if !contains(prefixes, prefix) {
			continue
		}
		rns, name, _ := strings.Cut(rest, "/")
		if ns != "" {
			if prefix == "ns" && rest != ns || prefix != "ns" && prefix != "drain" && prefix != "cni" && rns != ns {
				continue
			}
		}
		if !matches(r, q) {
			continue
		}
		out = append(out, r)
		switch {
		case prefix == "drain":
			refs = append(refs, ref("Node", "", rest))
		case policyKinds[prefix] != "":
			refs = append(refs, ref(policyKinds[prefix], rns, name))
		case prefix == "ns":
			refs = append(refs, ref("Namespace", "", rest))
		}
	}
	return listResult("rows", out, 80, refs)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type accessRows struct {
	roles    map[string]accessRole
	bindings []accessBinding
	sas      []map[string]any
	raw      []map[string]any
}

type accessRole struct {
	Key   string         `json:"key"`
	Kind  string         `json:"kind"`
	Name  string         `json:"name"`
	Rules []accessRule   `json:"rules"`
	Raw   map[string]any `json:"-"`
}

type accessRule struct {
	Verbs           []string `json:"verbs"`
	APIGroups       []string `json:"apiGroups"`
	Resources       []string `json:"resources"`
	ResourceNames   []string `json:"resourceNames"`
	NonResourceURLs []string `json:"nonResourceURLs"`
}

type accessBinding struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	RoleKind  string `json:"roleKind"`
	RoleName  string `json:"roleName"`
	Subjects  []struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"subjects"`
	Findings []map[string]any `json:"findings"`
	Raw      map[string]any   `json:"-"`
}

func (tb *toolbox) accessRows(ctx context.Context) (accessRows, error) {
	rows, _, err := tb.rows(ctx, live.ViewAccess)
	if err != nil {
		return accessRows{}, err
	}
	out := accessRows{roles: map[string]accessRole{}, raw: rows}
	for _, r := range rows {
		key := sget(r, "key")
		b, _ := json.Marshal(r)
		switch {
		case strings.HasPrefix(key, "role:"):
			var x accessRole
			_ = json.Unmarshal(b, &x)
			x.Raw = r
			out.roles[key] = x
		case strings.HasPrefix(key, "binding:"):
			var x accessBinding
			_ = json.Unmarshal(b, &x)
			x.Raw = r
			out.bindings = append(out.bindings, x)
		case strings.HasPrefix(key, "sa:"):
			out.sas = append(out.sas, r)
		}
	}
	return out, nil
}

func (tb *toolbox) whoCan(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ar, err := tb.accessRows(ctx)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	verb, res, group, ns := strings.ToLower(str(in, "verb")), strings.ToLower(str(in, "resource")), str(in, "group"), str(in, "namespace")
	if verb == "" || res == "" {
		return sherlock.ToolResult{Text: "Give the verb and the resource.", Error: true}
	}
	resource, sub, _ := strings.Cut(res, "/")
	if _, ok := in["group"]; !ok {
		group = groupOf(resource)
	}
	q := rbac.Request{Verb: verb, Group: group, Resource: resource, Subresource: sub}
	type who struct {
		Subject string   `json:"subject"`
		Binding string   `json:"binding"`
		Role    string   `json:"role"`
		Scope   string   `json:"scope"`
		Names   []string `json:"onlyNames,omitempty"`
	}
	var list []who
	var refs []sherlock.Ref
	for _, b := range ar.bindings {
		if b.Kind == "RoleBinding" && ns != "" && b.Namespace != ns || b.Kind == "RoleBinding" && ns == "" {
			continue
		}
		roleKey := views.RoleKey(b.RoleKind, b.Namespace, b.RoleName)
		role, ok := ar.roles[roleKey]
		if !ok {
			continue
		}
		rules := make([]rbacv1.PolicyRule, 0, len(role.Rules))
		for _, r := range role.Rules {
			rules = append(rules, rbacv1.PolicyRule{Verbs: r.Verbs, APIGroups: r.APIGroups, Resources: r.Resources, ResourceNames: r.ResourceNames, NonResourceURLs: r.NonResourceURLs})
		}
		level, names := rbac.RolesCover(rules, q)
		if level == "" {
			continue
		}
		scope := "cluster-wide"
		if b.Kind == "RoleBinding" {
			scope = "in " + b.Namespace
		}
		for _, s := range b.Subjects {
			subj := s.Kind + " " + s.Name
			if s.Namespace != "" {
				subj = s.Kind + " " + s.Namespace + "/" + s.Name
			}
			list = append(list, who{Subject: subj, Binding: b.Kind + " " + strings.Trim(b.Namespace+"/"+b.Name, "/"), Role: b.RoleKind + " " + b.RoleName, Scope: scope, Names: names})
			if s.Kind == "ServiceAccount" {
				refs = append(refs, ref("ServiceAccount", s.Namespace, s.Name))
			}
		}
		if b.Kind == "RoleBinding" {
			refs = append(refs, ref("RoleBinding", b.Namespace, b.Name))
		}
	}
	note := "From the bindings the person may see. Groups such as system:masters may hold more than RBAC shows."
	b, _ := json.Marshal(map[string]any{"request": fmt.Sprintf("%s %s%s", verb, res, suffixIf(" in ", ns)), "who": list, "note": note})
	return sherlock.ToolResult{Text: string(b), Refs: refs}
}

func (tb *toolbox) access(ctx context.Context, in map[string]any) sherlock.ToolResult {
	ar, err := tb.accessRows(ctx)
	if err != nil {
		return sherlock.ToolResult{Text: err.Error(), Error: true}
	}
	what, ns, q := firstOf(str(in, "what"), "findings"), str(in, "namespace"), str(in, "query")
	var out []map[string]any
	var refs []sherlock.Ref
	for _, r := range ar.raw {
		key := sget(r, "key")
		prefix, _, _ := strings.Cut(key, ":")
		switch what {
		case "bindings":
			if prefix != "binding" {
				continue
			}
		case "roles":
			if prefix != "role" {
				continue
			}
		case "serviceaccounts":
			if prefix != "sa" {
				continue
			}
		default:
			f, _ := r["findings"].([]any)
			if prefix != "binding" || len(f) == 0 {
				continue
			}
		}
		if ns != "" && sget(r, "namespace") != ns || !matches(r, q) {
			continue
		}
		out = append(out, r)
		switch prefix {
		case "sa":
			refs = append(refs, ref("ServiceAccount", sget(r, "namespace"), sget(r, "name")))
		case "binding", "role":
			if k := sget(r, "kind"); k == "Role" || k == "RoleBinding" {
				refs = append(refs, ref(k, sget(r, "namespace"), sget(r, "name")))
			}
		}
	}
	return listResult(what, out, num(in, "limit", 50, 1, 200), refs)
}
