package kube

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"
)

type recorded struct{ got []string }

func (r *recorded) HandleWarningHeaderWithContext(_ context.Context, _ int, _ string, text string) {
	r.got = append(r.got, text)
}

// Watching v1 Endpoints, which the console reads on purpose, does not log
// the API server's deprecation warning at every watch (kube-cut-6); other
// warnings still go to the log.
func TestTheEndpointsDeprecationIsNotLogged(t *testing.T) {
	c, err := New(&rest.Config{Host: "http://127.0.0.1:1"}, Options{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	h, ok := c.clientCfg.WarningHandlerWithContext.(quietWarnings)
	if !ok {
		t.Fatalf("the clients' warning handler is %T", c.clientCfg.WarningHandlerWithContext)
	}
	rec := &recorded{}
	h.next = rec
	h.HandleWarningHeaderWithContext(context.Background(), 299, "-", "v1 Endpoints is deprecated in v1.33+; use discovery.k8s.io/v1 EndpointSlice")
	h.HandleWarningHeaderWithContext(context.Background(), 299, "-", "policy/v1beta1 PodSecurityPolicy is deprecated")
	if len(rec.got) != 1 || rec.got[0] != "policy/v1beta1 PodSecurityPolicy is deprecated" {
		t.Fatalf("logged %q", rec.got)
	}
}
