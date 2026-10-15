package rbac

import (
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func r(groups, resources, verbs []string, names ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: groups, Resources: resources, Verbs: verbs, ResourceNames: names}
}

func TestRuleMatching(t *testing.T) {
	core := []string{""}
	for name, c := range map[string]struct {
		rule rbacv1.PolicyRule
		req  Request
		want bool
	}{
		"exact":                 {r(core, []string{"pods"}, []string{"get"}), Request{Verb: "get", Resource: "pods"}, true},
		"other verb":            {r(core, []string{"pods"}, []string{"get"}), Request{Verb: "list", Resource: "pods"}, false},
		"other group":           {r([]string{"apps"}, []string{"pods"}, []string{"get"}), Request{Verb: "get", Resource: "pods"}, false},
		"pods is not pods/exec": {r(core, []string{"pods"}, []string{"create"}), Request{Verb: "create", Resource: "pods", Subresource: "exec"}, false},
		"subresource exactly":   {r(core, []string{"pods/exec"}, []string{"create"}), Request{Verb: "create", Resource: "pods", Subresource: "exec"}, true},
		"*/subresource":         {r(core, []string{"*/scale"}, []string{"get"}), Request{Verb: "get", Group: "", Resource: "deployments", Subresource: "scale"}, true},
		"*/subresource, no sub": {r(core, []string{"*/scale"}, []string{"get"}), Request{Verb: "get", Resource: "deployments"}, false},
		"wildcards":             {r([]string{"*"}, []string{"*"}, []string{"*"}), Request{Verb: "delete", Group: "apps", Resource: "deployments"}, true},
		"named, no name asked":  {r(core, []string{"secrets"}, []string{"get"}, "tls"), Request{Verb: "get", Resource: "secrets"}, false},
		"named, that name":      {r(core, []string{"secrets"}, []string{"get"}, "tls"), Request{Verb: "get", Resource: "secrets", Name: "tls"}, true},
		"named, another":        {r(core, []string{"secrets"}, []string{"get"}, "tls"), Request{Verb: "get", Resource: "secrets", Name: "db"}, false},
		"non-resource rule":     {rbacv1.PolicyRule{Verbs: []string{"get"}, NonResourceURLs: []string{"*"}}, Request{Verb: "get", Resource: "pods"}, false},
	} {
		if got := RuleAllows(c.rule, c.req); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestPowers(t *testing.T) {
	core := []string{""}
	rbacG := []string{rbacv1.GroupName}
	for name, c := range map[string]struct {
		rules []rbacv1.PolicyRule
		want  []string
	}{
		"cluster-admin": {[]rbacv1.PolicyRule{r([]string{"*"}, []string{"*"}, []string{"*"})}, []string{PowerEverything}},
		"secrets":       {[]rbacv1.PolicyRule{r(core, []string{"secrets"}, []string{"list"})}, []string{PowerSecrets}},
		"named secrets": {[]rbacv1.PolicyRule{r(core, []string{"secrets"}, []string{"get"}, "tls")}, []string{PowerNamedSecrets}},
		"watch only":    {[]rbacv1.PolicyRule{r(core, []string{"secrets"}, []string{"watch"})}, nil},
		"rbac writer":   {[]rbacv1.PolicyRule{r(rbacG, []string{"rolebindings"}, []string{"create"})}, []string{PowerChangesRBAC}},
		"escalate bind": {[]rbacv1.PolicyRule{r(rbacG, []string{"clusterroles"}, []string{"escalate", "bind"})}, []string{PowerEscalate, PowerBind}},
		"impersonate":   {[]rbacv1.PolicyRule{r(core, []string{"users", "groups"}, []string{"impersonate"})}, []string{PowerImpersonate}},
		"edit-like":     {[]rbacv1.PolicyRule{r(core, []string{"pods", "pods/exec"}, []string{"create"}), r(core, []string{"serviceaccounts/token"}, []string{"create"})}, []string{PowerExec, PowerPods, PowerTokens}},
		"operator":      {[]rbacv1.PolicyRule{r(rbacG, []string{"roles"}, []string{"*"}), r([]string{"x.io"}, []string{"*"}, []string{"*"})}, []string{PowerChangesRBAC, PowerEscalate, PowerBind, PowerWildcards}},
	} {
		if got := Powers(c.rules); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
