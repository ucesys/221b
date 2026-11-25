package jobs

import (
	"context"
	"strings"
	"testing"
)

// A finding that names a namespaced object without its namespace has no
// key (kube#12): asked about, "Service/events" read the Events of a
// namespace named services and was taken as there, and "Deployment/web"
// as not on the cluster although shop/web is.
func TestAFindingOfANamespacedObjectNeedsItsNamespace(t *testing.T) {
	asked := 0
	s := &Service{o: Options{Objects: func(context.Context, string, Object) (bool, bool) { asked++; return true, true }}}
	for _, res := range []string{"Service/events", "deploy/web", "Pod/web-1"} {
		f := Finding{Check: "other.x", Cluster: "prod", Resource: res}
		s.keyOf(context.Background(), Kind{}, builtinRules{}, &f)
		if f.Routing != RoutedInvalid || f.Key != nil || !strings.Contains(f.InvalidReason, "names no namespace") {
			t.Errorf("%s: %+v", res, f)
		}
	}
	if asked != 0 {
		t.Errorf("the cluster was asked %d times", asked)
	}
	// Cluster-scoped, or with a namespace, or a kind it does not know: as
	// before.
	for _, res := range []string{"Node/worker-1", "shop/Deployment/web", "Widget/w"} {
		f := Finding{Check: "other.x", Cluster: "prod", Resource: res}
		s.keyOf(context.Background(), Kind{}, builtinRules{}, &f)
		if f.Key == nil {
			t.Errorf("%s: %+v", res, f)
		}
	}
}
