package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/rbac"
)

// The Access tab (P4-03) reads three kinds of rows: roles with their rules
// and what they add up to, bindings with their subjects and what is risky
// about them, and ServiceAccounts with the pods and long-lived tokens that
// use them. Who can do what is worked out in the browser from the rows a
// person may see, with the API server's rule matching.

// Keys of the access view's rows.
const (
	AccessRole    = "role"
	AccessBinding = "binding"
	AccessSA      = "sa"
)

// RoleKey is "role:ClusterRole/name" or "role:Role/namespace/name".
func RoleKey(kind, namespace, name string) string {
	if kind == "ClusterRole" {
		return AccessRole + ":ClusterRole/" + name
	}
	return AccessRole + ":Role/" + namespace + "/" + name
}

// BindingKey is "binding:ClusterRoleBinding/name" or
// "binding:RoleBinding/namespace/name".
func BindingKey(kind, namespace, name string) string {
	if kind == "ClusterRoleBinding" {
		return AccessBinding + ":ClusterRoleBinding/" + name
	}
	return AccessBinding + ":RoleBinding/" + namespace + "/" + name
}

// SAKey is "sa:namespace/name".
func SAKey(namespace, name string) string { return AccessSA + ":" + namespace + "/" + name }

// SplitAccessKey reads a key back: its prefix, the object's kind (Role,
// ClusterRole, RoleBinding, ClusterRoleBinding or ServiceAccount),
// namespace and name.
func SplitAccessKey(key string) (prefix, kind, namespace, name string, ok bool) {
	prefix, rest, ok := strings.Cut(key, ":")
	if !ok {
		return "", "", "", "", false
	}
	switch prefix {
	case AccessSA:
		namespace, name, ok = strings.Cut(rest, "/")
		return prefix, "ServiceAccount", namespace, name, ok && name != ""
	case AccessRole, AccessBinding:
		kind, rest, ok = strings.Cut(rest, "/")
		if !ok {
			return "", "", "", "", false
		}
		if kind == "Role" || kind == "RoleBinding" {
			namespace, name, ok = strings.Cut(rest, "/")
			return prefix, kind, namespace, name, ok && name != ""
		}
		if kind == "ClusterRole" || kind == "ClusterRoleBinding" {
			return prefix, kind, "", rest, rest != ""
		}
	}
	return "", "", "", "", false
}

// AccessKeys lists every row of the access view.
func AccessKeys(src Source) []string {
	var out []string
	for _, o := range src.Objects("clusterroles", "") {
		if r, ok := o.(*rbacv1.ClusterRole); ok {
			out = append(out, RoleKey("ClusterRole", "", r.Name))
		}
	}
	for _, o := range src.Objects("roles", "") {
		if r, ok := o.(*rbacv1.Role); ok {
			out = append(out, RoleKey("Role", r.Namespace, r.Name))
		}
	}
	for _, o := range src.Objects("clusterrolebindings", "") {
		if b, ok := o.(*rbacv1.ClusterRoleBinding); ok {
			out = append(out, BindingKey("ClusterRoleBinding", "", b.Name))
		}
	}
	for _, o := range src.Objects("rolebindings", "") {
		if b, ok := o.(*rbacv1.RoleBinding); ok {
			out = append(out, BindingKey("RoleBinding", b.Namespace, b.Name))
		}
	}
	for _, o := range src.Objects("serviceaccounts", "") {
		if a, ok := o.(*corev1.ServiceAccount); ok {
			out = append(out, SAKey(a.Namespace, a.Name))
		}
	}
	return out
}

// RBACSource is who made an RBAC object, read from its labels: Kubernetes'
// own defaults, kubeadm, a Helm release, another manager, or nobody.
type RBACSource struct {
	// Kind is k8s, kubeadm, helm, managed or manual.
	Kind    string `json:"kind"`
	Release string `json:"release,omitempty"`
	// Manager is the app.kubernetes.io/managed-by label of a managed object.
	Manager string `json:"manager,omitempty"`
}

func rbacSource(m metav1.Object) RBACSource {
	l := m.GetLabels()
	switch {
	case l["kubernetes.io/bootstrapping"] == "rbac-defaults":
		return RBACSource{Kind: "k8s"}
	case strings.HasPrefix(m.GetName(), "kubeadm:"):
		return RBACSource{Kind: "kubeadm"}
	}
	if r := objectRelease(m); r != "" {
		return RBACSource{Kind: "helm", Release: r}
	}
	if mgr := l["app.kubernetes.io/managed-by"]; mgr != "" {
		return RBACSource{Kind: "managed", Manager: mgr}
	}
	return RBACSource{Kind: "manual"}
}

// Default says whether Kubernetes or kubeadm made the object.
func (s RBACSource) Default() bool { return s.Kind == "k8s" || s.Kind == "kubeadm" }

// RBACRule is one rule of a role.
type RBACRule struct {
	Verbs           []string `json:"verbs"`
	APIGroups       []string `json:"apiGroups,omitempty"`
	Resources       []string `json:"resources,omitempty"`
	ResourceNames   []string `json:"resourceNames,omitempty"`
	NonResourceURLs []string `json:"nonResourceURLs,omitempty"`
}

// RoleRow is a ClusterRole or a Role.
type RoleRow struct {
	Key       string     `json:"key"`
	Kind      string     `json:"kind"`
	Namespace string     `json:"namespace,omitempty"`
	Name      string     `json:"name"`
	Rules     []RBACRule `json:"rules"`
	// Aggregated says the rules are gathered from other ClusterRoles by
	// label, which the API server keeps filled in.
	Aggregated bool       `json:"aggregated,omitempty"`
	Powers     []string   `json:"powers"`
	Source     RBACSource `json:"source"`
	Created    *time.Time `json:"created,omitempty"`
}

// RBACSubject is a subject of a binding.
type RBACSubject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// Missing marks a ServiceAccount that does not exist.
	Missing bool `json:"missing,omitempty"`
}

// Finding is something about a binding worth reviewing.
type Finding struct {
	// Severity is high, medium, low or info.
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	// Subject is who the finding is about, when it is one of the
	// binding's subjects.
	Subject *RBACSubject `json:"subject,omitempty"`
	// Command is what would fix it, shown to copy, never run.
	Command string `json:"command,omitempty"`
}

// BindingRow is a ClusterRoleBinding or a RoleBinding.
type BindingRow struct {
	Key         string        `json:"key"`
	Kind        string        `json:"kind"`
	Namespace   string        `json:"namespace,omitempty"`
	Name        string        `json:"name"`
	RoleKind    string        `json:"roleKind"`
	RoleName    string        `json:"roleName"`
	RoleMissing bool          `json:"roleMissing,omitempty"`
	Subjects    []RBACSubject `json:"subjects"`
	Source      RBACSource    `json:"source"`
	Created     *time.Time    `json:"created,omitempty"`
	Findings    []Finding     `json:"findings"`
}

// SARow is a ServiceAccount: the pods that run as it and the Secrets that
// hold a token for it that never expires.
type SARow struct {
	Key       string     `json:"key"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Created   *time.Time `json:"created,omitempty"`
	Pods      []SAPod    `json:"pods"`
	Tokens    []SAToken  `json:"tokens"`
}

// SAPod is a pod that runs as a ServiceAccount.
type SAPod struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Tone   string `json:"tone"`
}

// SAToken is a Secret of type kubernetes.io/service-account-token.
type SAToken struct {
	Name    string     `json:"name"`
	Created *time.Time `json:"created,omitempty"`
}

// BuildAccess builds one row of the access view.
func BuildAccess(src Source, key string, now time.Time) (any, bool) {
	prefix, kind, ns, name, ok := SplitAccessKey(key)
	if !ok {
		return nil, false
	}
	switch prefix {
	case AccessRole:
		if kind == "ClusterRole" {
			r, _ := src.Object("clusterroles", "", name).(*rbacv1.ClusterRole)
			if r == nil {
				return nil, false
			}
			return roleRow(key, kind, &r.ObjectMeta, r.Rules, r.AggregationRule != nil), true
		}
		r, _ := src.Object("roles", ns, name).(*rbacv1.Role)
		if r == nil {
			return nil, false
		}
		return roleRow(key, kind, &r.ObjectMeta, r.Rules, false), true
	case AccessBinding:
		if kind == "ClusterRoleBinding" {
			b, _ := src.Object("clusterrolebindings", "", name).(*rbacv1.ClusterRoleBinding)
			if b == nil {
				return nil, false
			}
			return bindingRow(src, key, kind, &b.ObjectMeta, b.RoleRef, b.Subjects), true
		}
		b, _ := src.Object("rolebindings", ns, name).(*rbacv1.RoleBinding)
		if b == nil {
			return nil, false
		}
		return bindingRow(src, key, kind, &b.ObjectMeta, b.RoleRef, b.Subjects), true
	case AccessSA:
		a, _ := src.Object("serviceaccounts", ns, name).(*corev1.ServiceAccount)
		if a == nil {
			return nil, false
		}
		return saRow(src, a, now), true
	}
	return nil, false
}

func created(m metav1.Object) *time.Time {
	t := m.GetCreationTimestamp()
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func roleRow(key, kind string, m *metav1.ObjectMeta, rules []rbacv1.PolicyRule, aggregated bool) RoleRow {
	out := RoleRow{Key: key, Kind: kind, Namespace: m.Namespace, Name: m.Name, Rules: make([]RBACRule, len(rules)), Aggregated: aggregated, Powers: rbac.Powers(rules), Source: rbacSource(m), Created: created(m)}
	for i, r := range rules {
		out.Rules[i] = RBACRule{Verbs: r.Verbs, APIGroups: r.APIGroups, Resources: r.Resources, ResourceNames: r.ResourceNames, NonResourceURLs: r.NonResourceURLs}
	}
	if out.Powers == nil {
		out.Powers = []string{}
	}
	return out
}

// roleRules finds the rules a binding grants, or ok=false when its role
// does not exist.
func roleRules(src Source, namespace string, ref rbacv1.RoleRef) ([]rbacv1.PolicyRule, bool) {
	if ref.Kind == "Role" {
		r, _ := src.Object("roles", namespace, ref.Name).(*rbacv1.Role)
		if r == nil {
			return nil, false
		}
		return r.Rules, true
	}
	r, _ := src.Object("clusterroles", "", ref.Name).(*rbacv1.ClusterRole)
	if r == nil {
		return nil, false
	}
	return r.Rules, true
}

func bindingRow(src Source, key, kind string, m *metav1.ObjectMeta, ref rbacv1.RoleRef, subjects []rbacv1.Subject) BindingRow {
	out := BindingRow{Key: key, Kind: kind, Namespace: m.Namespace, Name: m.Name, RoleKind: ref.Kind, RoleName: ref.Name, Subjects: []RBACSubject{}, Source: rbacSource(m), Created: created(m), Findings: []Finding{}}
	for _, s := range subjects {
		sub := RBACSubject{Kind: s.Kind, Name: s.Name, Namespace: s.Namespace}
		if s.Kind == rbacv1.ServiceAccountKind {
			if sub.Namespace == "" {
				// The API server reads a ServiceAccount without a namespace
				// as one in the binding's.
				sub.Namespace = m.Namespace
			}
			sub.Missing = src.Object("serviceaccounts", sub.Namespace, s.Name) == nil
		}
		out.Subjects = append(out.Subjects, sub)
	}
	rules, ok := roleRules(src, m.Namespace, ref)
	out.RoleMissing = !ok
	out.Findings = findings(out, rbac.Powers(rules))
	return out
}

// SubjectText names a subject: "ServiceAccount web/api", "Group ops".
func SubjectText(s RBACSubject) string {
	if s.Kind == rbacv1.ServiceAccountKind {
		return "ServiceAccount " + s.Namespace + "/" + s.Name
	}
	return s.Kind + " " + s.Name
}

// bindingText names a binding: "ClusterRoleBinding ci" or "RoleBinding web/ci".
func bindingText(b BindingRow) string {
	if b.Namespace == "" {
		return b.Kind + " " + b.Name
	}
	return b.Kind + " " + b.Namespace + "/" + b.Name
}

func deleteCommand(b BindingRow) string {
	if b.Namespace == "" {
		return "kubectl delete clusterrolebinding " + b.Name
	}
	return "kubectl delete rolebinding " + b.Name + " -n " + b.Namespace
}

// findings reviews a binding (P4-03): who it gives everything, Secrets or
// RBAC to, ServiceAccounts it names that do not exist, and people named
// directly rather than through a group.
func findings(b BindingRow, powers []string) []Finding {
	out := []Finding{}
	has := func(p string) bool {
		for _, x := range powers {
			if x == p {
				return true
			}
		}
		return false
	}
	cluster := b.Kind == "ClusterRoleBinding"
	where := "everywhere"
	if !cluster {
		where = "in " + b.Namespace
	}
	for i := range b.Subjects {
		s := b.Subjects[i]
		switch {
		case s.Kind == rbacv1.GroupKind && s.Name == "system:masters":
			out = append(out, Finding{Severity: "info", Title: "system:masters bypasses RBAC", Subject: &b.Subjects[i],
				Text: "Kubernetes allows every request from a member of system:masters before it looks at any role, so removing this binding takes nothing away from them. A client certificate that names the group cannot be revoked until it expires; keep such certificates for break-glass use."})
		case cluster && has(rbac.PowerEverything) && s.Kind == rbacv1.ServiceAccountKind:
			out = append(out, Finding{Severity: "high", Title: fmt.Sprintf("ServiceAccount %s/%s has %s", s.Namespace, s.Name, b.RoleName), Subject: &b.Subjects[i], Command: deleteCommand(b),
				Text: fmt.Sprintf("%s grants it every permission in every namespace. Its token is a password that its pods carry: anyone who can read the token, or run a pod as %s/%s, can do anything in the cluster. Bind it to a role with only what it needs, in the namespaces it works in.", bindingText(b), s.Namespace, s.Name)})
		case cluster && !b.Source.Default() && !has(rbac.PowerEverything) && has(rbac.PowerSecrets) && s.Kind == rbacv1.ServiceAccountKind:
			out = append(out, Finding{Severity: "medium", Title: fmt.Sprintf("ServiceAccount %s/%s can read every Secret in the cluster", s.Namespace, s.Name), Subject: &b.Subjects[i],
				Text: fmt.Sprintf("%s grants %s %s, which reads Secrets in every namespace: list counts as read, since kubectl get secrets -o yaml returns every value. If it needs only some, grant them in their namespaces, or by name.", bindingText(b), SubjectText(s), roleText(b))})
		}
		if s.Kind == rbacv1.ServiceAccountKind && s.Missing && !b.Source.Default() {
			out = append(out, Finding{Severity: "medium", Title: bindingText(b) + " names a ServiceAccount that doesn’t exist", Subject: &b.Subjects[i], Command: deleteCommand(b),
				Text: fmt.Sprintf("There is no ServiceAccount %s/%s, so whoever creates one with that name gets %s %s. Delete the binding if nothing needs it, or remove the subject.", s.Namespace, s.Name, roleText(b), where)})
		}
		if s.Kind == rbacv1.UserKind && !strings.HasPrefix(s.Name, "system:") && !b.Source.Default() {
			out = append(out, Finding{Severity: "low", Title: fmt.Sprintf("%s has %s %s directly", s.Name, b.RoleName, where), Subject: &b.Subjects[i], Command: deleteCommand(b),
				Text: fmt.Sprintf("Access granted to one person outlives their job changes and is easy to miss in reviews. Grant it through a group from the identity provider instead, then remove %s.", bindingText(b))})
		}
	}
	if cluster && !b.Source.Default() && !has(rbac.PowerEverything) && len(b.Subjects) > 0 {
		var what []string
		for _, p := range []string{rbac.PowerChangesRBAC, rbac.PowerEscalate, rbac.PowerBind, rbac.PowerImpersonate} {
			if has(p) {
				what = append(what, p)
			}
		}
		if len(what) > 0 {
			subjects := make([]string, len(b.Subjects))
			for i, s := range b.Subjects {
				subjects[i] = SubjectText(s)
			}
			out = append(out, Finding{Severity: "info", Title: strings.Join(subjects, ", ") + " can change RBAC",
				Text: fmt.Sprintf("%s grants %s, which %s. With escalate or bind, or a wildcard, a holder can grant itself any permission; without them Kubernetes refuses grants beyond what the holder already has.", bindingText(b), roleText(b), powersText(what))})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return sevRank[out[i].Severity] < sevRank[out[j].Severity] })
	return out
}

var sevRank = map[string]int{"high": 0, "medium": 1, "low": 2, "info": 3}

func roleText(b BindingRow) string { return b.RoleKind + " " + b.RoleName }

func powersText(p []string) string {
	words := map[string]string{rbac.PowerChangesRBAC: "creates or changes roles and bindings", rbac.PowerEscalate: "may escalate roles", rbac.PowerBind: "may bind any role", rbac.PowerImpersonate: "may impersonate others"}
	out := make([]string, len(p))
	for i, x := range p {
		out[i] = words[x]
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

func saRow(src Source, a *corev1.ServiceAccount, now time.Time) SARow {
	out := SARow{Key: SAKey(a.Namespace, a.Name), Namespace: a.Namespace, Name: a.Name, Created: created(a), Pods: []SAPod{}, Tokens: []SAToken{}}
	for _, o := range src.Objects("pods", a.Namespace) {
		p, ok := o.(*corev1.Pod)
		if !ok || podServiceAccount(p) != a.Name || isMirror(p) {
			continue
		}
		row := BuildPodRow(src, p, now)
		if row.Done {
			continue
		}
		out.Pods = append(out.Pods, SAPod{Name: p.Name, Status: row.Status, Tone: row.Tone})
	}
	sort.Slice(out.Pods, func(i, j int) bool { return out.Pods[i].Name < out.Pods[j].Name })
	for _, o := range src.Objects("secrets", a.Namespace) {
		s, ok := o.(*kube.SecretSummary)
		if !ok || s.Type != corev1.SecretTypeServiceAccountToken || s.Annotations[corev1.ServiceAccountNameKey] != a.Name {
			continue
		}
		out.Tokens = append(out.Tokens, SAToken{Name: s.Name, Created: created(s)})
	}
	sort.Slice(out.Tokens, func(i, j int) bool { return out.Tokens[i].Name < out.Tokens[j].Name })
	return out
}

func podServiceAccount(p *corev1.Pod) string {
	if p.Spec.ServiceAccountName == "" {
		return "default"
	}
	return p.Spec.ServiceAccountName
}

// isMirror says whether a pod is the API server's copy of a static pod,
// which runs with no ServiceAccount token.
func isMirror(p *corev1.Pod) bool {
	_, ok := p.Annotations[corev1.MirrorPodAnnotationKey]
	return ok
}

// PodServiceAccount names the ServiceAccount a pod runs as.
func PodServiceAccount(p *corev1.Pod) string { return podServiceAccount(p) }
