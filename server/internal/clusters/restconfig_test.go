package clusters_test

import (
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
