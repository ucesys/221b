package roles

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// What the Roles and Permissions pages show (RB-41 to RB-44), built from
// the roles and bindings a cluster has. The API server reads those from its
// cache and hands them here, so the rules of what is shown are tested
// without a cluster.

// Deployed is a role as a cluster has it.
type Deployed struct {
	Present bool
	Rules   []Rule
}

// Binding is one RoleBinding or ClusterRoleBinding to one of the roles.
type Binding struct {
	// Role is the ClusterRole's name.
	Role string
	// Namespace is empty for a ClusterRoleBinding.
	Namespace string
	Subjects  []BoundSubject
	// Managed is true when 221B's chart made the binding.
	Managed bool
}

// BoundSubject is who a binding names.
type BoundSubject struct {
	Kind string // User, Group or ServiceAccount
	Name string
}

// Description is what a role is for, in a line.
func Description(id ID) string {
	switch id {
	case SuperAdmin:
		return "Everything, including connections, settings and users"
	case SuperViewer:
		return "Reads everything except Secrets"
	case Admin:
		return "Viewer plus the namespaced changes Watson supports"
	}
	return "Reads the namespaced kinds; Secrets as keys and sizes"
}

// Scope is where a role applies.
func Scope(id ID) string {
	if id.Super() {
		return "Every namespace, every cluster"
	}
	return "Bound namespaces"
}

// Can lists what a role allows, in words, for its definition.
func Can(id ID, t Template) []string {
	switch id {
	case SuperAdmin:
		if t == ReadOnly {
			return []string{"Read everything, Secrets included", "Manage connections, settings and users in 221B"}
		}
		return []string{"Read everything, Secrets included", "Make every change Watson supports", "Cordon, drain and label nodes", "Manage connections, settings and users in 221B"}
	case SuperViewer:
		return []string{"Read every shown kind, in every namespace and cluster"}
	case Admin:
		if t == ReadOnly {
			return []string{"Read every shown kind in the bound namespaces"}
		}
		return []string{"Read every shown kind in the bound namespaces", "Scale, restart, roll back", "Delete a pod", "Label and annotate", "Roll back a Helm release"}
	}
	return []string{"Read every shown kind in the bound namespaces"}
}

// Cannot lists what a role does not allow, in words.
func Cannot(id ID, t Template) []string {
	switch id {
	case SuperAdmin:
		if t == ReadOnly {
			return []string{"Change anything in a cluster", "Change roles or bindings"}
		}
		return []string{"Change roles or bindings in the console"}
	case SuperViewer:
		return []string{"Change anything", "Read Secrets"}
	case Admin:
		if t == ReadOnly {
			return []string{"Change anything"}
		}
		return []string{"Cordon or drain nodes", "Change roles or bindings", "Open a shell in a pod", "Act outside the bound namespaces"}
	}
	return []string{"Change anything"}
}

// SubjectRow is one subject of a role on the Roles page.
type SubjectRow struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Source     string   `json:"source"`
	Namespaces []string `json:"namespaces"`
	Members    int      `json:"members,omitempty"`
	Managed    bool     `json:"managed"`
	Orphaned   bool     `json:"orphaned,omitempty"`
}

// Holders counts the people and groups 221B holds a super role for.
type Holders struct {
	Groups int `json:"groups"`
	Users  int `json:"users"`
}

// RoleRow is one role on the Roles page.
type RoleRow struct {
	ID          ID           `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Scope       string       `json:"scope"`
	Definition  Template     `json:"definition"`
	ClusterRole string       `json:"clusterRole"`
	Present     bool         `json:"present"`
	Can         []string     `json:"can"`
	Cannot      []string     `json:"cannot"`
	Subjects    []SubjectRow `json:"subjects"`
	Holders     Holders      `json:"holders"`
}

// DefinitionsOf names the definitions in use.
type DefinitionsOf struct {
	Admin      Template `json:"admin"`
	SuperAdmin Template `json:"superAdmin"`
}

// RolesResponse is the Roles page.
type RolesResponse struct {
	Roles       []RoleRow     `json:"roles"`
	Visible     bool          `json:"visible"`
	Definitions DefinitionsOf `json:"definitions"`
	NodesRole   string        `json:"nodesRole"`
	Message     string        `json:"message,omitempty"`
}

// RolesInput is what the Roles page is built from.
type RolesInput struct {
	Templates Templates
	Deployed  map[ID]Deployed
	Bindings  []Binding
	Prefixes  Prefixes
	// Visible is false when the person may not list RoleBindings.
	Visible bool
	// Members counts the users in a local group; Exists says whether a local
	// user exists. Either may be nil.
	Members func(group string) int
	Exists  func(user string) bool
	// Holders are what 221B holds for the super roles, shown to SuperAdmin.
	Holders map[ID]Holders
}

// sourceOf says where a bound name comes from, by its prefix, and the name
// without it (RB-15).
func (in RolesInput) sourceOf(kind, name string) (string, string) {
	p := in.Prefixes
	switch {
	case kind == "ServiceAccount":
		return "other", name
	case p.Local != "" && strings.HasPrefix(name, p.Local):
		return string(Local), strings.TrimPrefix(name, p.Local)
	case p.LDAP != "" && strings.HasPrefix(name, p.LDAP):
		return string(LDAP), strings.TrimPrefix(name, p.LDAP)
	}
	oidc := p.OIDCUser
	if kind == "Group" {
		oidc = p.OIDCGroup
	}
	if oidc != "" && strings.HasPrefix(name, oidc) {
		return string(OIDC), strings.TrimPrefix(name, oidc)
	}
	return "other", name
}

// BuildRoles builds the Roles page.
func BuildRoles(in RolesInput) RolesResponse {
	out := RolesResponse{Roles: []RoleRow{}, Visible: in.Visible, Definitions: DefinitionsOf{Admin: in.Templates.Of(Admin), SuperAdmin: in.Templates.Of(SuperAdmin)}, NodesRole: NodesClusterRole}
	missing := 0
	for _, id := range All {
		t := in.Templates.Of(id)
		dep := in.Deployed[id]
		if !dep.Present {
			missing++
		}
		row := RoleRow{ID: id, Name: id.Name(), Description: Description(id), Scope: Scope(id), Definition: t, ClusterRole: id.ClusterRole(), Present: dep.Present,
			Can: Can(id, t), Cannot: Cannot(id, t), Subjects: []SubjectRow{}, Holders: in.Holders[id]}
		if in.Visible {
			row.Subjects = in.subjects(id)
		}
		out.Roles = append(out.Roles, row)
	}
	if missing == len(All) {
		out.Message = "The four 221B roles are not on this cluster, so people see an empty console here. Download the roles as YAML from this page and apply them, or install the chart on this cluster."
	} else if missing > 0 {
		out.Message = "Some of the 221B roles are missing on this cluster; the Permissions page names them."
	}
	return out
}

func (in RolesInput) subjects(id ID) []SubjectRow {
	type key struct{ kind, name string }
	rows := map[key]*SubjectRow{}
	var order []key
	for _, b := range in.Bindings {
		if b.Role != id.ClusterRole() {
			continue
		}
		for _, s := range b.Subjects {
			src, name := in.sourceOf(s.Kind, s.Name)
			k := key{s.Kind, s.Name}
			r, ok := rows[k]
			if !ok {
				r = &SubjectRow{Kind: s.Kind, Name: name, Source: src, Namespaces: []string{}, Managed: b.Managed}
				if src == string(Local) && s.Kind == "Group" && in.Members != nil {
					r.Members = in.Members(name)
				}
				if src == string(Local) && s.Kind == "User" && in.Exists != nil && !in.Exists(name) {
					r.Orphaned = true
				}
				rows[k] = r
				order = append(order, k)
			}
			r.Managed = r.Managed && b.Managed
			if b.Namespace != "" && !slices.Contains(r.Namespaces, b.Namespace) {
				r.Namespaces = append(r.Namespaces, b.Namespace)
			}
		}
	}
	out := make([]SubjectRow, 0, len(order))
	for _, k := range order {
		sort.Strings(rows[k].Namespaces)
		out = append(out, *rows[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PermRule is one rule on the Permissions page.
type PermRule struct {
	Group         string   `json:"group"`
	Resource      string   `json:"resource"`
	Verbs         []string `json:"verbs"`
	Kind          string   `json:"kind"`
	Scope         string   `json:"scope"`
	Flags         []string `json:"flags"`
	ResourceNames []string `json:"resourceNames,omitempty"`
	NonResource   bool     `json:"nonResource,omitempty"`
}

// PermRole is a role's rules on the Permissions page.
type PermRole struct {
	ID          ID         `json:"id"`
	Name        string     `json:"name"`
	ClusterRole string     `json:"clusterRole"`
	Definition  Template   `json:"definition"`
	Present     bool       `json:"present"`
	Rules       []PermRule `json:"rules"`
}

// RoleDrift is how one deployed role differs from the shipped one.
type RoleDrift struct {
	ID          ID       `json:"id"`
	Differences []string `json:"differences"`
}

// Drift is the comparison of every deployed role with the shipped ones.
type Drift struct {
	// State is match, differs, missing or unknown (not read yet).
	State string      `json:"state"`
	Roles []RoleDrift `json:"roles"`
	// More says that some role allows more than the shipped one.
	More      bool      `json:"-"`
	CheckedAt time.Time `json:"-"`
}

// PermissionsResponse is the Permissions page.
type PermissionsResponse struct {
	Roles       []PermRole    `json:"roles"`
	Drift       Drift         `json:"drift"`
	Enforce     string        `json:"enforce"`
	CheckedAt   *time.Time    `json:"checkedAt,omitempty"`
	Definitions DefinitionsOf `json:"definitions"`
}

// permRules turns rules into rows with the scope they apply in.
func permRules(id ID, rules []Rule) []PermRule {
	var out []PermRule
	for _, r := range Normalized(rules) {
		flags := r.Flags()
		if flags == nil {
			flags = []string{}
		}
		scope := "bound"
		if id.Super() {
			scope = "all"
		}
		kind := "read"
		if r.Writes() {
			kind = "write"
		}
		for _, u := range r.NonResourceURLs {
			out = append(out, PermRule{Group: "", Resource: u, Verbs: r.Verbs, Kind: kind, Scope: "cluster", Flags: flags, NonResource: true})
		}
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				s := scope
				if id.Super() && isClusterScoped(g, res) {
					s = "cluster"
				}
				out = append(out, PermRule{Group: g, Resource: res, Verbs: r.Verbs, Kind: kind, Scope: s, Flags: flags, ResourceNames: r.ResourceNames})
			}
		}
	}
	return out
}

func isClusterScoped(group, resource string) bool {
	for _, w := range Watched {
		if w.Group == group && w.Resource == resource {
			return !w.Namespaced
		}
	}
	return false
}

// BuildPermissions builds the Permissions page. nodes is the nodes role as
// deployed, which everyone who signs in holds (RB-07).
func BuildPermissions(templates Templates, deployed map[ID]Deployed, nodes Deployed, enforce string, drift Drift) PermissionsResponse {
	out := PermissionsResponse{Roles: []PermRole{}, Drift: drift, Enforce: enforce, Definitions: DefinitionsOf{Admin: templates.Of(Admin), SuperAdmin: templates.Of(SuperAdmin)}}
	if out.Enforce != "strict" {
		out.Enforce = "off"
	}
	if !drift.CheckedAt.IsZero() {
		t := drift.CheckedAt
		out.CheckedAt = &t
	}
	if out.Drift.Roles == nil {
		out.Drift.Roles = []RoleDrift{}
	}
	for _, id := range All {
		dep := deployed[id]
		pr := PermRole{ID: id, Name: id.Name(), ClusterRole: id.ClusterRole(), Definition: templates.Of(id), Present: dep.Present, Rules: []PermRule{}}
		if dep.Present {
			pr.Rules = permRules(id, dep.Rules)
			if !id.Super() {
				// The Namespaces tab lists a person's own namespaces, worked out
				// from what they can read; no right to list namespaces is
				// granted for it (RB-08).
				pr.Rules = append(pr.Rules, PermRule{Group: "", Resource: "namespaces", Verbs: slices.Clone(readVerbs), Kind: "read", Scope: "own", Flags: []string{}})
			}
			if nodes.Present {
				for _, r := range permRules(id, nodes.Rules) {
					r.Scope = "cluster"
					pr.Rules = append(pr.Rules, r)
				}
			}
		}
		out.Roles = append(out.Roles, pr)
	}
	return out
}

// Check compares every deployed role with the definition shipped for it
// (RB-36). A role that is not there is "missing", and does not count as
// allowing more.
func Check(templates Templates, o Options, deployed map[ID]Deployed, at time.Time) Drift {
	d := Drift{State: "match", Roles: []RoleDrift{}, CheckedAt: at}
	missing := 0
	for _, id := range All {
		dep := deployed[id]
		if !dep.Present {
			missing++
			d.Roles = append(d.Roles, RoleDrift{ID: id, Differences: []string{"the ClusterRole " + id.ClusterRole() + " does not exist on this cluster"}})
			continue
		}
		diff := Compare(templates.For(id, o).Rules, dep.Rules)
		if diff.Empty() {
			continue
		}
		d.Roles = append(d.Roles, RoleDrift{ID: id, Differences: diff.Lines()})
		if diff.MoreThanShipped() {
			d.More = true
		}
	}
	switch {
	case missing == len(All):
		d.State = "missing"
	case len(d.Roles) > 0:
		d.State = "differs"
	}
	return d
}

// Monitor keeps the last comparison for each cluster, for the Permissions
// page, the audit log and rbac.enforce (RB-36).
type Monitor struct {
	mu    sync.Mutex
	byKey map[string]Drift
}

// Set stores a comparison and says whether it differs from the last one,
// so that only a change is audited.
func (m *Monitor) Set(cluster string, d Drift) (changed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byKey == nil {
		m.byKey = map[string]Drift{}
	}
	old, had := m.byKey[cluster]
	m.byKey[cluster] = d
	return !had || old.State != d.State || !sameDrift(old, d)
}

func sameDrift(a, b Drift) bool {
	if len(a.Roles) != len(b.Roles) {
		return false
	}
	for i := range a.Roles {
		if a.Roles[i].ID != b.Roles[i].ID || !slices.Equal(a.Roles[i].Differences, b.Roles[i].Differences) {
			return false
		}
	}
	return true
}

// Get returns the last comparison.
func (m *Monitor) Get(cluster string) (Drift, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.byKey[cluster]
	return d, ok
}

// Refusal says why every change on a cluster is refused, or "" when it is
// not: with rbac.enforce strict, a cluster whose roles allow more than the
// shipped ones refuses every change, naming the difference (RB-36).
func (m *Monitor) Refusal(cluster, enforce string) string {
	if enforce != "strict" {
		return ""
	}
	d, ok := m.Get(cluster)
	if !ok || !d.More {
		return ""
	}
	var lines []string
	for _, r := range d.Roles {
		for _, diff := range r.Differences {
			if strings.HasPrefix(diff, "allows ") {
				lines = append(lines, r.ID.Name()+" "+diff)
			}
		}
	}
	return "Changes are refused on this cluster because its roles allow more than the ones 221B ships (rbac.enforce is strict): " + Summary(lines, 3) + "."
}
