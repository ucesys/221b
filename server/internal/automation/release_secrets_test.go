package automation

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube/kubetest"
)

// A console's sensitive values are only its own release's: another release
// in the namespace labels its Secrets the same way, and the start's
// comparison must not count them (the fourteenth cluster test's N1).
func TestOnlyThisReleasesSensitiveSecretsAreHeld(t *testing.T) {
	ctx := context.Background()
	secrets := kubetest.Cluster().CoreV1().Secrets("ns")
	mine := NewSecretSensitiveOn(secrets, "ns", "mine", nil)
	other := NewSecretSensitiveOn(secrets, "ns", "other", nil)
	for _, s := range []*SecretSensitive{mine, other} {
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.SecretName("local"), Namespace: "ns", Labels: s.labelsOf("local")},
			Data: map[string][]byte{valueKey("hidden:" + s.prefix): []byte("x")}}
		if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := mine.Held(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["local"]) != 1 || all["local"][0] != hiddenPrefix+"mine" {
		t.Fatalf("held %v, want only this release's value", all)
	}
}
