package apps

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The Secret's resourceVersion is what a save is held to, by the store
// and by the API server for the last instant.
func TestASecretRecordIsSavedOnlyOverWhatWasRead(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	secrets := cs.CoreV1().Secrets("console")
	s := &SecretStore{secrets: secrets, namespace: "console", prefix: "b"}
	rec := Record{Cluster: "c", Namespace: "web", Name: "shop", Values: "a: 1\n"}
	if err := s.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	name := s.name("c", "web", "shop")
	sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sec.ResourceVersion = "5"
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	read, _, err := s.Get(ctx, "c", "web", "shop")
	if err != nil || read.Rev != "5" {
		t.Fatalf("%+v %v", read, err)
	}
	sec.ResourceVersion = "6"
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, read, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale save: %v", err)
	}
	read.Rev = "6"
	if err := s.Save(ctx, read, nil); err != nil {
		t.Fatalf("a save of what is there: %v", err)
	}
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, (*corev1.Secret)(nil), apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, name, errors.New("changed"))
	})
	if err := s.Save(ctx, Record{Cluster: "c", Namespace: "web", Name: "shop"}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("the API server's own refusal: %v", err)
	}
}
