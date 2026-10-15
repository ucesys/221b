// Package rbac reads Kubernetes RBAC rules the way the API server's
// authorizer does: which requests a rule allows, and what a role's rules
// add up to (P4-03). The browser has the same matching in
// web/src/lib/rbac.ts, which works out who can do what; both follow the
// API server's rule matching (k8s.io/component-helpers/auth/rbac/validation).
package rbac

import (
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
)

// Request is what a rule is asked about: a verb on a resource of an API
// group, optionally a subresource and an object's name.
type Request struct {
	Verb        string
	Group       string
	Resource    string
	Subresource string
	Name        string
}

// RuleAllows says whether a rule allows a request, and whether only
// because it names the object (resourceNames). A rule with resourceNames
// allows a request without a name not at all, as the API server decides.
func RuleAllows(r rbacv1.PolicyRule, q Request) bool {
	if len(r.NonResourceURLs) > 0 && len(r.Resources) == 0 {
		return false
	}
	if !has(r.Verbs, q.Verb) || !has(r.APIGroups, q.Group) || !resourceMatches(r.Resources, q.Resource, q.Subresource) {
		return false
	}
	return len(r.ResourceNames) == 0 || slices.Contains(r.ResourceNames, q.Name)
}

// RuleCovers is RuleAllows ignoring names: whether the rule allows the
// request for some object, and if so, the names it is limited to (nil
// when it is not).
func RuleCovers(r rbacv1.PolicyRule, q Request) (ok bool, names []string) {
	if len(r.NonResourceURLs) > 0 && len(r.Resources) == 0 {
		return false, nil
	}
	if !has(r.Verbs, q.Verb) || !has(r.APIGroups, q.Group) || !resourceMatches(r.Resources, q.Resource, q.Subresource) {
		return false, nil
	}
	return true, r.ResourceNames
}

// RolesCover says how a set of rules covers a request for any object:
// "all", "named" with the names, or "" when it does not.
func RolesCover(rules []rbacv1.PolicyRule, q Request) (level string, names []string) {
	for _, r := range rules {
		ok, n := RuleCovers(r, q)
		if !ok {
			continue
		}
		if len(n) == 0 {
			return "all", nil
		}
		level = "named"
		for _, x := range n {
			if !slices.Contains(names, x) {
				names = append(names, x)
			}
		}
	}
	return level, names
}

func has(list []string, v string) bool {
	return slices.Contains(list, rbacv1.VerbAll) || slices.Contains(list, v)
}

// resourceMatches follows the API server: "*" matches anything, the rule
// may name "pods/exec" exactly, and "*/exec" matches exec of any resource.
func resourceMatches(rules []string, resource, sub string) bool {
	combined := resource
	if sub != "" {
		combined += "/" + sub
	}
	for _, r := range rules {
		if r == rbacv1.ResourceAll || r == combined {
			return true
		}
		if sub != "" && r == "*/"+sub {
			return true
		}
	}
	return false
}

// Powers are what a role's rules add up to, in the words the Access tab
// shows, most dangerous first. "everything" stands alone: it implies the
// rest.
func Powers(rules []rbacv1.PolicyRule) []string {
	for _, r := range rules {
		if slices.Contains(r.APIGroups, "*") && slices.Contains(r.Resources, "*") && slices.Contains(r.Verbs, "*") {
			return []string{PowerEverything}
		}
	}
	allows := func(verbs []string, group string, resources ...string) bool {
		for _, v := range verbs {
			for _, res := range resources {
				res, sub, _ := strings.Cut(res, "/")
				for _, r := range rules {
					if RuleAllowsAnyName(r, Request{Verb: v, Group: group, Resource: res, Subresource: sub}) {
						return true
					}
				}
			}
		}
		return false
	}
	unnamed := func(verbs []string, group string, resources ...string) bool {
		for _, v := range verbs {
			for _, res := range resources {
				res, sub, _ := strings.Cut(res, "/")
				for _, r := range rules {
					if RuleAllows(r, Request{Verb: v, Group: group, Resource: res, Subresource: sub}) {
						return true
					}
				}
			}
		}
		return false
	}
	var out []string
	if allows([]string{"create", "update", "patch"}, rbacv1.GroupName, "clusterrolebindings", "rolebindings", "clusterroles", "roles") {
		out = append(out, PowerChangesRBAC)
	}
	if allows([]string{"escalate"}, rbacv1.GroupName, "clusterroles", "roles") {
		out = append(out, PowerEscalate)
	}
	if allows([]string{"bind"}, rbacv1.GroupName, "clusterroles", "roles") {
		out = append(out, PowerBind)
	}
	if allows([]string{"impersonate"}, "", "users", "groups", "serviceaccounts") || allows([]string{"impersonate"}, "authentication.k8s.io", "uids", "userextras") {
		out = append(out, PowerImpersonate)
	}
	switch {
	case unnamed([]string{"get", "list"}, "", "secrets"):
		out = append(out, PowerSecrets)
	case allows([]string{"get", "list"}, "", "secrets"):
		out = append(out, PowerNamedSecrets)
	}
	if allows([]string{"create"}, "", "pods/exec") {
		out = append(out, PowerExec)
	}
	if allows([]string{"create"}, "", "pods") {
		out = append(out, PowerPods)
	}
	if allows([]string{"create"}, "", "serviceaccounts/token") {
		out = append(out, PowerTokens)
	}
	for _, r := range rules {
		if len(r.Resources) > 0 && (slices.Contains(r.Resources, "*") || slices.Contains(r.Verbs, "*")) {
			out = append(out, PowerWildcards)
			break
		}
	}
	return out
}

// RuleAllowsAnyName is RuleAllows for some object, named or not.
func RuleAllowsAnyName(r rbacv1.PolicyRule, q Request) bool {
	ok, _ := RuleCovers(r, q)
	return ok
}

// The powers, as the Access tab names them.
const (
	PowerEverything   = "everything"
	PowerChangesRBAC  = "changes RBAC"
	PowerEscalate     = "escalate"
	PowerBind         = "bind"
	PowerImpersonate  = "impersonates"
	PowerSecrets      = "reads Secrets"
	PowerNamedSecrets = "reads named Secrets"
	PowerExec         = "exec into pods"
	PowerPods         = "creates pods"
	PowerTokens       = "acts as service accounts"
	PowerWildcards    = "wildcards"
)

// ServiceAccountUser is the user name a ServiceAccount authenticates as.
func ServiceAccountUser(namespace, name string) string {
	return "system:serviceaccount:" + namespace + ":" + name
}
