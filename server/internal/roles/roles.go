// Package roles holds the four roles of 221B and the two definitions of
// Admin and SuperAdmin (RB-01 to RB-13). It has no dependency on Kubernetes
// or on the rest of the console, so the same rules can be compiled into the
// server, written out for the chart (files/roles.json, which the chart
// installs as ClusterRoles) and compared with what a cluster has (RB-36).
//
// A role is a list of rules. What a rule allows is read the way the API
// server reads it; see internal/rbac for the matching.
package roles

import (
	"slices"
	"sort"
)

// ID names one of the four roles.
type ID string

// The four roles. The values are what the API and the chart call them.
const (
	Viewer      ID = "viewer"
	SuperViewer ID = "superViewer"
	Admin       ID = "admin"
	SuperAdmin  ID = "superAdmin"
)

// All lists the roles from the most to the least privileged, the order the
// pages show them in.
var All = []ID{SuperAdmin, SuperViewer, Admin, Viewer}

// Valid reports whether id names a role.
func Valid(id string) bool { return slices.Contains(All, ID(id)) }

// Super reports whether the role is held in 221B and applies to every
// namespace and cluster (RB-05).
func (id ID) Super() bool { return id == SuperAdmin || id == SuperViewer }

// Bound reports whether the role is bound one namespace at a time (RB-06).
func (id ID) Bound() bool { return id == Admin || id == Viewer }

// Name is how the role is shown.
func (id ID) Name() string {
	switch id {
	case SuperAdmin:
		return "SuperAdmin"
	case SuperViewer:
		return "SuperViewer"
	case Admin:
		return "Admin"
	case Viewer:
		return "Viewer"
	}
	return string(id)
}

// ClusterRole is the fixed name of the role's ClusterRole (RB-01).
func (id ID) ClusterRole() string {
	switch id {
	case SuperAdmin:
		return "221b-super-admin"
	case SuperViewer:
		return "221b-super-viewer"
	case Admin:
		return "221b-admin"
	case Viewer:
		return "221b-viewer"
	}
	return ""
}

// NodesClusterRole is the ClusterRole that lets everyone who signs in see
// the nodes and nothing else cluster-scoped (RB-07).
const NodesClusterRole = "221b-nodes"

// Template is the definition of Admin or SuperAdmin to install (RB-02).
type Template string

// The two definitions. Viewer and SuperViewer have the read-only one only.
const (
	Full     Template = "full"
	ReadOnly Template = "readOnly"
)

// Valid reports whether t is a definition.
func (t Template) Valid() bool { return t == Full || t == ReadOnly }

// Templates say which definition Admin and SuperAdmin use.
type Templates struct {
	Admin      Template
	SuperAdmin Template
}

// Defaults are the full definitions (RB-02).
func Defaults() Templates { return Templates{Admin: Full, SuperAdmin: Full} }

// Of is the definition in use for a role.
func (t Templates) Of(id ID) Template {
	switch id {
	case Admin:
		if t.Admin != "" {
			return t.Admin
		}
	case SuperAdmin:
		if t.SuperAdmin != "" {
			return t.SuperAdmin
		}
	}
	if id == Admin || id == SuperAdmin {
		return Full
	}
	return ReadOnly
}

// ReadOnlyOnly reports the fully protected read-only deployment: both
// templates are read-only, so no role of 221B can change anything (RB-03).
func (t Templates) ReadOnlyOnly() bool {
	return t.Of(Admin) == ReadOnly && t.Of(SuperAdmin) == ReadOnly
}

// Options vary the definitions without changing the roles.
type Options struct {
	// Secrets adds the rules on Secrets (RB-10): reads for Viewer, Admin
	// and the read-only SuperAdmin, and Helm's release records for Admin.
	// SuperViewer never has them (RB-11).
	Secrets bool
}

// Rule is one rule of a role, as a ClusterRole has it.
type Rule struct {
	APIGroups       []string `json:"apiGroups,omitempty"`
	Resources       []string `json:"resources,omitempty"`
	ResourceNames   []string `json:"resourceNames,omitempty"`
	NonResourceURLs []string `json:"nonResourceURLs,omitempty"`
	Verbs           []string `json:"verbs"`
}

// Read verbs; every other verb writes (RB-44).
var readVerbs = []string{"get", "list", "watch"}

// IsRead reports whether a verb only reads.
func IsRead(verb string) bool { return slices.Contains(readVerbs, verb) }

// Role is a role's definition.
type Role struct {
	ID          ID       `json:"id"`
	ClusterRole string   `json:"clusterRole"`
	Template    Template `json:"template"`
	Rules       []Rule   `json:"rules"`
}

// Resource is a kind the console reads.
type Resource struct {
	Group    string
	Resource string
	// Namespaced is false for kinds that belong to the cluster.
	Namespaced bool
	// Verbs are the read verbs the console uses; empty means all three.
	Verbs []string
}

// Watched lists what the console reads (RB-10), except Secrets, which
// Options.Secrets controls. A test in internal/kube fails when the console
// watches a kind that is not here.
var Watched = []Resource{
	{"", "pods", true, nil},
	{"", "pods/log", true, []string{"get"}},
	{"", "services", true, nil},
	{"", "endpoints", true, nil},
	{"", "persistentvolumeclaims", true, nil},
	{"", "configmaps", true, nil},
	{"", "serviceaccounts", true, nil},
	{"", "resourcequotas", true, nil},
	{"", "limitranges", true, nil},
	{"", "events", true, nil},
	{"apps", "deployments", true, nil},
	{"apps", "statefulsets", true, nil},
	{"apps", "daemonsets", true, nil},
	{"apps", "replicasets", true, nil},
	{"apps", "controllerrevisions", true, nil},
	{"batch", "jobs", true, nil},
	{"batch", "cronjobs", true, nil},
	{"discovery.k8s.io", "endpointslices", true, nil},
	{"networking.k8s.io", "ingresses", true, nil},
	{"networking.k8s.io", "networkpolicies", true, nil},
	{"rbac.authorization.k8s.io", "roles", true, nil},
	{"rbac.authorization.k8s.io", "rolebindings", true, nil},
	{"policy", "poddisruptionbudgets", true, nil},
	{"autoscaling", "horizontalpodautoscalers", true, nil},
	{"coordination.k8s.io", "leases", true, nil},
	{"metrics.k8s.io", "pods", true, []string{"get", "list"}},

	{"", "nodes", false, nil},
	{"", "namespaces", false, nil},
	{"", "persistentvolumes", false, nil},
	{"storage.k8s.io", "storageclasses", false, nil},
	{"apiextensions.k8s.io", "customresourcedefinitions", false, nil},
	{"rbac.authorization.k8s.io", "clusterroles", false, nil},
	{"rbac.authorization.k8s.io", "clusterrolebindings", false, nil},
	{"scheduling.k8s.io", "priorityclasses", false, nil},
	{"admissionregistration.k8s.io", "validatingwebhookconfigurations", false, nil},
	{"admissionregistration.k8s.io", "mutatingwebhookconfigurations", false, nil},
	{"networking.k8s.io", "ingressclasses", false, nil},
	{"metrics.k8s.io", "nodes", false, []string{"get", "list"}},
}

// readRules turns watched resources into one rule per group and verb set,
// in a stable order.
func readRules(namespaced bool, skip func(Resource) bool) []Rule {
	type key struct{ group, verbs string }
	var order []key
	byKey := map[key]*Rule{}
	for _, r := range Watched {
		if r.Namespaced != namespaced || (skip != nil && skip(r)) {
			continue
		}
		verbs := r.Verbs
		if len(verbs) == 0 {
			verbs = readVerbs
		}
		k := key{r.Group, join(verbs)}
		rule, ok := byKey[k]
		if !ok {
			rule = &Rule{APIGroups: []string{r.Group}, Verbs: slices.Clone(verbs)}
			byKey[k] = rule
			order = append(order, k)
		}
		rule.Resources = append(rule.Resources, r.Resource)
	}
	out := make([]Rule, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func join(s []string) string {
	out := ""
	for _, x := range s {
		out += x + ","
	}
	return out
}

var secretReads = Rule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: readVerbs}

// helmRecords are what Helm rollback writes: a release's records are
// Secrets of its namespace (RB-12).
var helmRecords = Rule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "update"}}

// workloadKinds are what Admin may restart and roll back.
var workloadKinds = []string{"deployments", "statefulsets", "daemonsets"}

// labelled are the namespaced kinds Admin may label and annotate. Roles,
// bindings, ServiceAccounts and Secrets are not among them.
var labelled = []Rule{
	{APIGroups: []string{""}, Resources: []string{"pods", "services", "configmaps", "persistentvolumeclaims"}, Verbs: []string{"patch"}},
	{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets", "daemonsets", "replicasets"}, Verbs: []string{"patch"}},
	{APIGroups: []string{"batch"}, Resources: []string{"jobs", "cronjobs"}, Verbs: []string{"patch"}},
	{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"patch"}},
}

// adminChanges are the namespaced changes of Watson (P6-02): scale, restart
// and undo, delete a pod, label and annotate. Nothing on RBAC, no
// pods/exec, no pods/eviction (draining needs nodes), nothing cluster-wide.
func adminChanges() []Rule {
	return []Rule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments/scale", "statefulsets/scale", "replicasets/scale"}, Verbs: []string{"get", "update", "patch"}},
		{APIGroups: []string{"apps"}, Resources: workloadKinds, Verbs: []string{"patch"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"delete"}},
		labelled[0], labelled[1], labelled[2], labelled[3],
	}
}

// Definition is the rules of a role in a definition.
func Definition(id ID, t Template, o Options) Role {
	r := Role{ID: id, ClusterRole: id.ClusterRole(), Template: t}
	switch id {
	case Viewer:
		r.Template = ReadOnly
		r.Rules = readRules(true, nil)
		if o.Secrets {
			r.Rules = append(r.Rules, secretReads)
		}
	case SuperViewer:
		r.Template = ReadOnly
		r.Rules = append(readRules(true, nil), readRules(false, nil)...)
	case Admin:
		r.Rules = readRules(true, nil)
		if o.Secrets {
			r.Rules = append(r.Rules, secretReads)
		}
		if t == Full {
			r.Rules = append(r.Rules, adminChanges()...)
			if o.Secrets {
				r.Rules = append(r.Rules, helmRecords)
			}
		} else {
			r.Template = ReadOnly
		}
	case SuperAdmin:
		if t == Full {
			r.Rules = []Rule{
				{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}},
				{NonResourceURLs: []string{"*"}, Verbs: []string{"*"}},
			}
		} else {
			r.Template = ReadOnly
			r.Rules = superReadOnly()
		}
	}
	return r
}

// superGetCore are the core kinds and subresources the read-only SuperAdmin
// may get, Secrets included (RB-13). get on "*" would also grant
// nodes/proxy, pods/exec, pods/attach, pods/portforward, pods/proxy and
// services/proxy, and the API server and the kubelet authorize a GET (a
// WebSocket upgrade included) on those as get: a command in any pod, which
// no read-only role may run. RBAC cannot deny, so the core group is listed.
var superGetCore = []string{
	"componentstatuses", "configmaps", "endpoints", "events", "limitranges",
	"namespaces", "namespaces/status", "nodes", "nodes/status",
	"persistentvolumeclaims", "persistentvolumeclaims/status", "persistentvolumes", "persistentvolumes/status",
	"pods", "pods/ephemeralcontainers", "pods/log", "pods/status", "podtemplates",
	"replicationcontrollers", "replicationcontrollers/scale", "replicationcontrollers/status",
	"resourcequotas", "resourcequotas/status", "secrets", "serviceaccounts", "services", "services/status",
}

// superGetGroups are the built-in API groups, whose kinds the read-only
// SuperAdmin may get whole: none has a subresource that opens a shell or a
// proxy. Custom resources are listed and watched through the first rule;
// getting one by name needs its group here or the full definition.
var superGetGroups = []string{
	"admissionregistration.k8s.io", "apiextensions.k8s.io", "apiregistration.k8s.io", "apps",
	"autoscaling", "batch", "certificates.k8s.io", "coordination.k8s.io", "discovery.k8s.io",
	"events.k8s.io", "flowcontrol.apiserver.k8s.io", "internal.apiserver.k8s.io", "metrics.k8s.io",
	"networking.k8s.io", "node.k8s.io", "policy", "rbac.authorization.k8s.io", "resource.k8s.io",
	"scheduling.k8s.io", "storage.k8s.io", "storagemigration.k8s.io",
}

// superReadOnly is the read-only SuperAdmin (RB-13): list and watch on
// everything, get on everything but the subresources that run commands,
// and get on non-resource URLs.
func superReadOnly() []Rule {
	return []Rule{
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"list", "watch"}},
		{APIGroups: []string{""}, Resources: slices.Clone(superGetCore), Verbs: []string{"get"}},
		{APIGroups: slices.Clone(superGetGroups), Resources: []string{"*"}, Verbs: []string{"get"}},
		{NonResourceURLs: []string{"*"}, Verbs: []string{"get"}},
	}
}

// For is the definition of a role under the deployment's templates.
func (t Templates) For(id ID, o Options) Role { return Definition(id, t.Of(id), o) }

// Nodes is the nodes ClusterRole (RB-07): the nodes, and their metrics.
func Nodes() Role {
	return Role{ID: "nodes", ClusterRole: NodesClusterRole, Template: ReadOnly, Rules: []Rule{
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: readVerbs},
		{APIGroups: []string{"metrics.k8s.io"}, Resources: []string{"nodes"}, Verbs: []string{"get", "list"}},
	}}
}

// Wildcard reports whether a rule has a wildcard anywhere.
func (r Rule) Wildcard() bool {
	return slices.Contains(r.APIGroups, "*") || slices.Contains(r.Resources, "*") || slices.Contains(r.Verbs, "*")
}

// Writes reports whether any verb of the rule changes something.
func (r Rule) Writes() bool {
	for _, v := range r.Verbs {
		if !IsRead(v) {
			return true
		}
	}
	return false
}

// sensitive are subresources and kinds the Permissions page flags (RB-44).
var sensitive = map[string]string{
	"secrets":               "Secrets",
	"pods/exec":             "pods/exec",
	"pods/attach":           "pods/attach",
	"pods/portforward":      "pods/portforward",
	"nodes/proxy":           "nodes/proxy",
	"serviceaccounts/token": "serviceaccounts/token",
}

// Flags are what to point out about a rule, in a fixed order: a wildcard,
// Secrets, escalate, bind, impersonate, and the subresources that open a
// shell or a proxy (RB-44).
func (r Rule) Flags() []string {
	var out []string
	if r.Wildcard() || slices.Contains(r.NonResourceURLs, "*") {
		out = append(out, "wildcard")
	}
	for _, res := range r.Resources {
		if f := sensitive[res]; f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	for _, v := range []string{"escalate", "bind", "impersonate"} {
		if slices.Contains(r.Verbs, v) {
			out = append(out, v)
		}
	}
	return out
}

// Normalized returns a copy of the rules with every list sorted and the
// rules in a fixed order, so that two sets that allow the same compare equal.
func Normalized(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i, r := range rules {
		out[i] = Rule{
			APIGroups:       sorted(r.APIGroups),
			Resources:       sorted(r.Resources),
			ResourceNames:   sorted(r.ResourceNames),
			NonResourceURLs: sorted(r.NonResourceURLs),
			Verbs:           sorted(r.Verbs),
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return ruleKey(out[i]) < ruleKey(out[j]) })
	return out
}

func sorted(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	o := slices.Clone(s)
	sort.Strings(o)
	return o
}

func ruleKey(r Rule) string {
	return join(r.APIGroups) + "|" + join(r.Resources) + "|" + join(r.ResourceNames) + "|" + join(r.NonResourceURLs) + "|" + join(r.Verbs)
}
