package kube

import (
	"testing"

	"github.com/ucesys/221b/server/internal/roles"
)

// The Viewer rules are generated from the console's own list of watched
// kinds (RB-10): a kind the console watches that the roles do not list
// fails here, so a role cannot lag behind the screens.
func TestEveryWatchedKindIsInTheRoles(t *testing.T) {
	listed := map[[2]string]bool{}
	for _, w := range roles.Watched {
		listed[[2]string{w.Group, w.Resource}] = w.Namespaced
	}
	for _, k := range Kinds {
		if k.Secret {
			continue // the Secret rules follow secrets.read, not the list
		}
		ns, ok := listed[[2]string{k.Group, k.Resource}]
		if !ok {
			t.Errorf("%s (%s) is watched but missing from roles.Watched: add it, then run go test ./internal/roles -run Shipped -update", k.Name, k.Resource)
			continue
		}
		if ns != k.Namespaced {
			t.Errorf("%s: roles.Watched says namespaced=%v, the console says %v", k.Name, ns, k.Namespaced)
		}
	}
}
