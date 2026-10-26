package roles

import (
	"sort"
	"strconv"
	"strings"
)

// Manifest is the YAML that puts the four roles on a cluster the chart is
// not installed in (RB-29): the same ClusterRoles, the nodes role, the
// reserved-group bindings and the RoleBindings of the groups in values, in
// the definitions the deployment uses. The console cannot apply it, since
// it never writes a role (RB-52); a person with the right does.

// BindingSpec is one RoleBinding the chart renders: a role given to a group
// in one namespace. The chart writes them to the file the console reads, so
// that the YAML here is the same as the chart's.
type BindingSpec struct {
	Name string `json:"name"`
	Role ID     `json:"role"`
	// Subject is the group as Kubernetes sees it, with its prefix; Group is
	// the name without it.
	Subject   string `json:"subject"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace"`
}

// ManifestInput says what to write.
type ManifestInput struct {
	Templates Templates
	// Secrets adds the rules on Secrets (auth.secretsRead).
	Secrets bool
	// Exec adds pods/exec to the full Admin (rbac.roles.admin.exec).
	Exec     bool
	Bindings []BindingSpec
}

func q(s string) string { return strconv.Quote(s) }

func quoted(list []string) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = q(s)
	}
	return "[" + strings.Join(out, ", ") + "]"
}

func writeRules(b *strings.Builder, rules []Rule) {
	b.WriteString("rules:\n")
	for _, r := range rules {
		first := true
		line := func(key string, vals []string) {
			if len(vals) == 0 {
				return
			}
			if first {
				b.WriteString("  - " + key + ": " + quoted(vals) + "\n")
				first = false
				return
			}
			b.WriteString("    " + key + ": " + quoted(vals) + "\n")
		}
		if r.APIGroups != nil {
			// An empty group is the core group and must be written.
			line("apiGroups", r.APIGroups)
		}
		line("resources", r.Resources)
		line("resourceNames", r.ResourceNames)
		line("nonResourceURLs", r.NonResourceURLs)
		line("verbs", r.Verbs)
	}
}

func header(b *strings.Builder, kind, name, namespace string, extra map[string]string) {
	b.WriteString("---\napiVersion: rbac.authorization.k8s.io/v1\nkind: " + kind + "\nmetadata:\n  name: " + q(name) + "\n")
	if namespace != "" {
		b.WriteString("  namespace: " + q(namespace) + "\n")
	}
	b.WriteString("  labels:\n    app.kubernetes.io/name: \"221b\"\n    app.kubernetes.io/managed-by: \"221b\"\n    221b/managed: \"true\"\n")
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("    " + q(k) + ": " + q(extra[k]) + "\n")
	}
}

func binding(b *strings.Builder, kind, name, namespace, role, group string) {
	header(b, kind, name, namespace, nil)
	b.WriteString("roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: " + q(role) + "\n")
	b.WriteString("subjects:\n  - apiGroup: rbac.authorization.k8s.io\n    kind: Group\n    name: " + q(group) + "\n")
}

// Manifest writes the YAML.
func Manifest(in ManifestInput) string {
	var b strings.Builder
	b.WriteString("# The roles 221B uses on this cluster. Apply with kubectl apply -f, as someone who may create roles.\n")
	b.WriteString("# 221B never writes a role or a binding itself.\n")
	opts := Options{Secrets: in.Secrets, Exec: in.Exec}
	for _, id := range []ID{Admin, SuperAdmin, SuperViewer, Viewer} {
		def := in.Templates.For(id, opts)
		header(&b, "ClusterRole", def.ClusterRole, "", map[string]string{"221b/role": string(id), "221b/definition": string(def.Template)})
		writeRules(&b, def.Rules)
	}
	nodes := Nodes()
	header(&b, "ClusterRole", nodes.ClusterRole, "", map[string]string{"221b/role": "nodes"})
	writeRules(&b, nodes.Rules)
	binding(&b, "ClusterRoleBinding", nodes.ClusterRole, "", nodes.ClusterRole, GroupConsoleUsers)
	for _, id := range []ID{SuperAdmin, SuperViewer} {
		binding(&b, "ClusterRoleBinding", id.ClusterRole(), "", id.ClusterRole(), SuperGroup(id))
	}
	specs := append([]BindingSpec(nil), in.Bindings...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	for _, s := range specs {
		if s.Role.Bound() {
			binding(&b, "RoleBinding", s.Name, s.Namespace, s.Role.ClusterRole(), s.Subject)
		}
	}
	return b.String()
}
