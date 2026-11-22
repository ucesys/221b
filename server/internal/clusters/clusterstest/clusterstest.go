// Package clusterstest gives a test a store of cluster connections.
package clusterstest

import (
	"log/slog"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/clusters"
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

// Store is a test's store of connections: in memory, or, when TEST_STORE
// is "database", rows of a database of the test's own with the
// credentials in the Secrets of a cluster that is not there, which runs a
// package's tests over the database (Phase 13, DB-64).
func Store(t testing.TB) clusters.Store {
	t.Helper()
	if os.Getenv("TEST_STORE") != "database" {
		return clusters.NewMemoryStore()
	}
	s, _, _ := PG(t, nil)
	return s
}

// PG is a store over a database of the test's own and the Secrets of a
// cluster that is not there, with both, for a test to look into.
func PG(t testing.TB, log *slog.Logger) (*clusters.PGStore, *db.DB, *fake.Clientset) {
	t.Helper()
	d := dbtest.Open(t)
	cs := kubetest.Cluster()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return clusters.NewPGStore(d, Secrets(cs), log), d, cs
}

// Secrets is the store that keeps the connections, or with a database
// their credentials, in the Secrets of cs.
func Secrets(cs *fake.Clientset) *clusters.SecretStore {
	return clusters.NewSecretStoreOn(cs.CoreV1().Secrets(Namespace), Namespace, Prefix, map[string]string{"app.kubernetes.io/instance": Prefix})
}

// SecretName is the name of a connection's Secret.
func SecretName(id string) string { return Prefix + "-cluster-" + id }
