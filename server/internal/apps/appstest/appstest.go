// Package appstest gives a test a store of applications.
package appstest

import (
	"errors"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Namespace and Prefix are where a test's Secrets are, and what their
// names start with.
const (
	Namespace = "console"
	Prefix    = "b221"
)

// Store is a test's store of applications: in memory, or, when TEST_STORE
// is "database", rows of a database of the test's own with the secret part
// in the Secrets of a cluster that is not there, which runs a package's
// tests over the database (Phase 13, DB-62).
func Store(t testing.TB) apps.Store {
	t.Helper()
	if os.Getenv("TEST_STORE") != "database" {
		return apps.NewMemoryStore()
	}
	s, _, _ := PG(t, nil)
	return s
}

// PG is a store over a database of the test's own and the Secrets of a
// cluster that is not there, with both, for a test to look into.
func PG(t testing.TB, log *slog.Logger) (*apps.PGStore, *db.DB, *fake.Clientset) {
	t.Helper()
	d := dbtest.Open(t)
	cs := Cluster()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return apps.NewPGStore(d, Secrets(cs), log), d, cs
}

// Secrets is the store that keeps the applications in the Secrets of cs.
func Secrets(cs *fake.Clientset) *apps.SecretStore {
	return apps.NewSecretStoreOn(cs.CoreV1().Secrets(Namespace), Namespace, Prefix)
}

// SecretName is the name of an application's Secret.
func SecretName(cluster, namespace, name string) string {
	return Prefix + "-app-" + apps.ID(cluster, namespace, name)
}

// Cluster is a cluster that is not there whose Secrets have versions as an
// API server gives them: each write gets the next one, and an update over
// a version that is not the Secret's is refused as a conflict.
func Cluster() *fake.Clientset {
	cs := fake.NewSimpleClientset()
	var mu sync.Mutex
	version := 0
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	cs.PrependReactor("create", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		version++
		a.(k8stesting.CreateAction).GetObject().(*corev1.Secret).ResourceVersion = strconv.Itoa(version)
		return false, nil, nil
	})
	cs.PrependReactor("update", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		sec := a.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		cur, err := cs.Tracker().Get(gvr, a.GetNamespace(), sec.Name)
		if err != nil {
			return false, nil, nil
		}
		if was := cur.(*corev1.Secret).ResourceVersion; sec.ResourceVersion != "" && sec.ResourceVersion != was {
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), sec.Name, errors.New("the object has been modified"))
		}
		version++
		sec.ResourceVersion = strconv.Itoa(version)
		return false, nil, nil
	})
	return cs
}
