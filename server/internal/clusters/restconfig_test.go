package clusters_test

import (
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
)

const kubeconfigZZ = `apiVersion: v1
kind: Config
clusters:
  - name: lakehouse-staging
    cluster:
      server: http://127.0.0.1:7444
contexts:
  - name: lakehouse-staging
    context:
      cluster: lakehouse-staging
      user: console-staging
current-context: lakehouse-staging
users:
  - name: console-staging
    user:
      token: abc.def.ghi
`

func TestKubeconfigTokenOverPlainHTTP(t *testing.T) {
	cfg, err := clusters.RestConfig(clusters.Spec{Server: "http://127.0.0.1:7444", CA: "system", Auth: "kubeconfig"}, clusters.Credential{Kubeconfig: kubeconfigZZ})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BearerToken != "abc.def.ghi" {
		t.Fatalf("token %q", cfg.BearerToken)
	}
}

// The rest config refuses a file the kubeconfig names, even with a token
// inline, so a connection saved before the check never reads one.
func TestKubeconfigFilesRefused(t *testing.T) {
	// Over HTTPS: client-go keeps credentials back from a plain-HTTP server.
	kc := strings.Replace(kubeconfigZZ, "token: abc.def.ghi", "token: abc.def.ghi\n      tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token", 1)
	kc = strings.Replace(kc, "http://127.0.0.1:7444", "https://k8s.example:6443", 1)
	if _, err := clusters.RestConfig(clusters.Spec{Server: "https://k8s.example:6443", CA: "system", Auth: "kubeconfig"}, clusters.Credential{Kubeconfig: kc}); err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("token file accepted: %v", err)
	}
}
