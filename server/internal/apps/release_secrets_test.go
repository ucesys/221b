package apps

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube/kubetest"
)

// A console lists only its own release's application Secrets: another
// release in the namespace labels its own the same way, and the one-time
// copy and the start's comparison must not read them (the fourteenth
// cluster test's N1).
func TestOnlyThisReleasesApplicationSecretsAreListed(t *testing.T) {
	ctx := context.Background()
	cs := kubetest.Cluster()
	secrets := cs.CoreV1().Secrets("ns")
	spec := []byte(`{"cluster":"local","namespace":"default","name":"podinfo"}`)
	for _, name := range []string{"mine-app-a", "other-app-b", "mine-apple"} {
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{LabelComponent: ComponentValue}},
			Data: map[string][]byte{keySpec: spec}}
		if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	st := NewSecretStoreOn(secrets, "ns", "mine")
	all, err := st.all(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all["mine-app-a"] == nil {
		t.Fatalf("listed %d Secrets, want only mine-app-a: %v", len(all), all)
	}
	recs, err := st.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("read %d applications from the Secrets, want the one of this release", len(recs))
	}
}
