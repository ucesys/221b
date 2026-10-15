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
