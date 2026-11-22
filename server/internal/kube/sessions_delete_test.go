package kube

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The owner, November 20, 2026 (answer 1): once the sessions' copy is
// recorded, this release's session Secrets that hold no data are deleted;
// one that holds a session stays, and no other Secret is touched: another
// release's empty session Secret, a Secret with no labels and no data,
// and one of this release that is no session.
func TestOnlyThisReleasesEmptySessionSecretsAreDeleted(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	const ns, release = "221b", "k8s-console-221b"
	s := NewSessionStoreFor(cs, ns, release, map[string]string{"app.kubernetes.io/instance": release})
	create := func(name string, labels map[string]string, data map[string][]byte) {
		t.Helper()
		if _, err := cs.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Data: data}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	ours := map[string]string{"app.kubernetes.io/component": "session", "app.kubernetes.io/instance": release}
	create(release+"-session-empty1", ours, nil)
	create(release+"-session-empty2", ours, map[string][]byte{})
	create(release+"-session-full", ours, map[string][]byte{"session.json": []byte(`{}`)})
	create("other-221b-session-empty", map[string]string{"app.kubernetes.io/component": "session", "app.kubernetes.io/instance": "other-221b"}, nil)
	create(release+"-session-unlabelled", nil, nil)
	create(release+"-users", map[string]string{"app.kubernetes.io/component": "users", "app.kubernetes.io/instance": release}, nil)
	n, err := s.DeleteEmpty(ctx)
	if err != nil || n != 2 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	list, err := cs.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, sec := range list.Items {
		left = append(left, sec.Name)
	}
	slices.Sort(left)
	if want := []string{release + "-session-full", release + "-session-unlabelled", release + "-users", "other-221b-session-empty"}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	// Again: nothing more; and with no instance label, nothing at all.
	if n, err := s.DeleteEmpty(ctx); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
	if n, err := NewSessionStoreFor(cs, ns, "other-221b", nil).DeleteEmpty(ctx); err != nil || n != 0 {
		t.Fatalf("without an instance label: %d %v", n, err)
	}
}
