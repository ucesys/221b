package watson

import (
	"errors"
	"testing"
)

// Flags may come before the verb, as kubectl and helm allow; the blocked
// ones are refused wherever they are.
func TestFlagsBeforeTheVerb(t *testing.T) {
	for _, tc := range []struct {
		line, name, ns string
		pos            []string
	}{
		{"kubectl -n analytics describe pod api-0", "kubectl describe", "analytics", []string{"pod", "api-0"}},
		{"kubectl --namespace=web get pods", "kubectl get", "web", []string{"pods"}},
		{"kubectl -n web rollout restart deployment/api", "kubectl rollout restart", "web", []string{"deployment/api"}},
		{"helm -n shop history shop", "helm history", "shop", []string{"shop"}},
		{"kubectl get pods -n web", "kubectl get", "web", []string{"pods"}},
	} {
		c, err := Parse(tc.line)
		if err != nil {
			t.Fatalf("%s: %v", tc.line, err)
		}
		if c.Name() != tc.name || c.Namespace != tc.ns || len(c.Positionals) != len(tc.pos) {
			t.Fatalf("%s: %s in %q with %v", tc.line, c.Name(), c.Namespace, c.Positionals)
		}
		for i, p := range tc.pos {
			if c.Positionals[i] != p {
				t.Fatalf("%s: positionals %v", tc.line, c.Positionals)
			}
		}
	}
	for line, rule := range map[string]string{
		"kubectl --context prod get pods":    "flag",
		"kubectl --as=admin cordon node-1":   "flag",
		"kubectl -s https://x get nodes":     "flag",
		"helm --kube-context prod list":      "flag",
		"kubectl -n web exec -it api -- sh":  "not supported",
		"kubectl --token abc delete pod x-0": "flag",
	} {
		_, err := Parse(line)
		var r *Refusal
		if !errors.As(err, &r) || r.Rule != rule {
			t.Fatalf("%s: %v, want %s", line, err, rule)
		}
	}
}

// Roles and bindings are not Watson's to change (RB-52).
func TestLabelRefusesAccessRules(t *testing.T) {
	for _, line := range []string{
		"kubectl label clusterrole 221b-viewer a=b",
		"kubectl annotate rolebinding/web-readers -n web a=b",
		"kubectl label clusterrolebindings.rbac.authorization.k8s.io x a=b",
	} {
		_, err := Parse(line)
		var r *Refusal
		if !errors.As(err, &r) || r.Rule != "access rules" {
			t.Errorf("%s: %v", line, err)
		}
	}
	if _, err := Parse("kubectl label pod api-0 a=b -n web"); err != nil {
		t.Errorf("a pod can be labelled: %v", err)
	}
}

// Secret values stay hidden whatever spelling of the type kubectl takes.
func TestGetRefusesSecretValuesInAnySpelling(t *testing.T) {
	for _, line := range []string{
		"kubectl get secrets -o yaml -n prod",
		"kubectl get secrets. -o yaml -n prod",
		"kubectl get secret.v1. -o json -n prod",
		"kubectl get SECRET.v1./db -o json -n prod",
	} {
		_, err := Parse(line)
		var r *Refusal
		if !errors.As(err, &r) || r.Rule != "secret values" {
			t.Errorf("%s: %v", line, err)
		}
	}
	for line, want := range map[string]string{"secrets.": "secrets", "secret.v1.": "secrets", "deployment.v1.apps": "deployments", "deployments.apps": "deployments", "pods.v1.apps": "pods.v1.apps", "secrets.example.com": "secrets.example.com"} {
		if got := resourceOf(line); got != want {
			t.Errorf("resourceOf(%q) = %q, want %q", line, got, want)
		}
	}
}

// Label and annotate arguments are changes or objects as kubectl reads
// them: a prefixed key ending in - removes that key.
func TestIsChange(t *testing.T) {
	for p, want := range map[string]bool{
		"a=b": true, "a-": true, "pod-security.kubernetes.io/enforce-": true, "example.com/x=y": true,
		"ns": false, "prod": false, "pod/api-0": false, "-": false, "=x": false,
	} {
		if got := isChange(p); got != want {
			t.Errorf("isChange(%q) = %v, want %v", p, got, want)
		}
	}
}
