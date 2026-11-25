package kube_test

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// A namespaced kind read without a namespace is refused, not sent as
// /api/v1/namespaces//services/events, which reads the Events of a
// namespace named services (kube#12).
func TestAYAMLReadOfANamespacedKindNeedsANamespace(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 2, Pods: 4})
	svc, _ := kube.KindOf("Service")
	if _, err := cl.ObjectYAML(context.Background(), kube.Person{}, svc, "", "events"); !apierrors.IsBadRequest(err) {
		t.Fatalf("a Service with no namespace: %v", err)
	}
	node, _ := kube.KindOf("Node")
	if _, err := cl.ObjectYAML(context.Background(), kube.Person{}, node, "", "worker-001"); err != nil {
		t.Fatalf("a Node: %v", err)
	}
}
