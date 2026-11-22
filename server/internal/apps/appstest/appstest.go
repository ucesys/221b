// Package appstest gives a test a store of applications.
package appstest

import (
	"log/slog"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube/kubetest"
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
	cs := kubetest.Cluster()
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
