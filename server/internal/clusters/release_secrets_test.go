package clusters

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube/kubetest"
)

// A console lists only its own release's connection Secrets: another
// release in the namespace labels its connection Secrets the same way, and
// they are neither listed nor said to be Secrets without a connection (the
// eleventh cluster test's N1).
func TestOnlyThisReleasesConnectionSecretsAreListed(t *testing.T) {
	ctx := context.Background()
	cs := kubetest.Cluster()
	secrets := cs.CoreV1().Secrets("ns")
	for _, name := range []string{"mine-cluster-a", "other-cluster-local", "mine-clusterish"} {
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{LabelComponent: ComponentValue}},
			Data: map[string][]byte{"token": []byte("x")}}
		if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	st := NewSecretStoreOn(secrets, "ns", "mine", nil)
	all, err := st.all(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "mine-cluster-a" {
		var names []string
		for _, s := range all {
			names = append(names, s.Name)
		}
		t.Fatalf("listed %v, want only mine-cluster-a", names)
	}
	revs, err := st.Revisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) > 1 {
		t.Fatalf("revisions of another release's Secrets: %v", revs)
	}
}
