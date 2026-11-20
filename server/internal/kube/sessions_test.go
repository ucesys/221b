package kube

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// What the move of the sessions into the database does with their
// Secrets (DB-66): they are read from the API server, with no cache
// running, and emptied, each left as an object with no keys.
func TestSessionSecretsAreReadWithoutTheCacheAndEmptiedNotDeleted(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	labels := map[string]string{"app.kubernetes.io/instance": "k8s-console"}
	s := NewSessionStoreFor(cs, "221b", "k8s-console-221b", labels)
	for _, key := range []string{"aaa", "bbb", "ccc"} {
		if wrote, err := s.Put(ctx, key, []byte(`{"user":{"username":"`+key+`"}}`), nil); err != nil || !wrote {
			t.Fatalf("writing a session: %v %v", wrote, err)
		}
	}
	// Other Secrets of the namespace: another release's session, and one
	// that is no session at all.
	for name, component := range map[string]string{"other-221b-session-zzz": "session", "k8s-console-221b-users": "users"} {
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/component": component}}, Data: map[string][]byte{"session.json": []byte(`{}`)}}
		if _, err := cs.CoreV1().Secrets("221b").Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// The cache was never started: List says so, and All reads all the same.
	if _, err := s.List(ctx); err == nil {
		t.Fatal("the cache lists before it is filled")
	}
	keys := func() []string {
		t.Helper()
		all, err := s.All(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for key, b := range all {
			if len(b) == 0 {
				t.Fatalf("the record of %s is empty", key)
			}
			out = append(out, key)
		}
		slices.Sort(out)
		return out
	}
	if got := keys(); !slices.Equal(got, []string{"aaa", "bbb", "ccc"}) {
		t.Fatalf("the sessions read: %v", got)
	}

	if emptied, err := s.Empty(ctx, "bbb"); err != nil || !emptied {
		t.Fatalf("emptying a session's Secret: %v %v", emptied, err)
	}
	sec, err := cs.CoreV1().Secrets("221b").Get(ctx, "k8s-console-221b-session-bbb", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the emptied Secret is gone: %v", err)
	}
	if len(sec.Data) != 0 || sec.Labels["app.kubernetes.io/component"] != "session" || sec.Labels["app.kubernetes.io/instance"] != "k8s-console" {
		t.Fatalf("the emptied Secret: data %v, labels %v", sec.Data, sec.Labels)
	}
	if got := keys(); !slices.Equal(got, []string{"aaa", "ccc"}) {
		t.Fatalf("the sessions read after one was emptied: %v", got)
	}
	// Twice, and one that is not there: nothing to do, and no error.
	for _, key := range []string{"bbb", "nowhere"} {
		if emptied, err := s.Empty(ctx, key); err != nil || emptied {
			t.Fatalf("emptying %s again: %v %v", key, emptied, err)
		}
	}
	// To a console from before the move an emptied Secret is no session:
	// it reads none, and a late write of the last activity does not make
	// the record again.
	if _, ok, err := s.Get(ctx, "bbb"); err != nil || ok {
		t.Fatalf("an emptied Secret reads as a session: %v %v", ok, err)
	}
	if wrote, err := s.Put(ctx, "bbb", []byte(`{}`), func(cur []byte) bool { return cur != nil }); err != nil || wrote {
		t.Fatalf("a late write to an emptied Secret: %v %v", wrote, err)
	}
	// The other Secrets are as they were.
	for _, name := range []string{"other-221b-session-zzz", "k8s-console-221b-users"} {
		if sec, err := cs.CoreV1().Secrets("221b").Get(ctx, name, metav1.GetOptions{}); err != nil || len(sec.Data) != 1 {
			t.Fatalf("%s: %v %v", name, sec, err)
		}
	}

	// A Secret a console from before the move writes while it is emptied:
	// the emptying is over the version read, and is made again.
	conflicts := 0
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "k8s-console-221b-session-aaa", errors.New("changed"))
		}
		return false, nil, nil
	})
	if emptied, err := s.Empty(ctx, "aaa"); err != nil || !emptied || conflicts != 1 {
		t.Fatalf("emptying over a conflict: %v %v, %d conflicts", emptied, err, conflicts)
	}
	// An API server that does not answer: an error that says away, and
	// nothing emptied.
	cs.PrependReactor("*", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	var says interface{ StoreAway() bool }
	if _, err := s.All(ctx); err == nil || !errors.As(err, &says) || !says.StoreAway() {
		t.Fatalf("reading the Secrets from an API server that is away: %v", err)
	}
	if emptied, err := s.Empty(ctx, "ccc"); err == nil || emptied {
		t.Fatalf("emptying with the API server away: %v %v", emptied, err)
	}
}
