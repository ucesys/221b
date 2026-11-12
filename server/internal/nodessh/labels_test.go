package nodessh

import (
	"context"
	"errors"
	"testing"

	"github.com/ucesys/221b/server/internal/kv"
)

// A host's labels are replaced whole, checked as they are when it is
// added (Phase 12, a Watson card's host.label).
func TestAHostsLabelsAreReplacedAndChecked(t *testing.T) {
	ctx := context.Background()
	svc := New(Options{CA: NewCA(nil), Pairings: NewPairings(&kv.Memory{}), Hosts: NewHosts(&kv.Memory{})})
	if _, err := svc.AddHost(ctx, Host{Cluster: "local", Name: "db-1", Address: "10.0.0.7", LoginUser: "root", Labels: map[string]string{"role": "db"}}, "Pat"); err != nil {
		t.Fatal(err)
	}
	h, err := svc.LabelHost(ctx, "local", "db-1", map[string]string{"role": "db", "zone": "a"})
	if err != nil || len(h.Labels) != 2 || h.Labels["zone"] != "a" || h.State != HostPending {
		t.Fatalf("labelled %+v, %v", h, err)
	}
	if _, err := svc.LabelHost(ctx, "local", "db-1", map[string]string{"bad key!": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad label: %v", err)
	}
	if _, err := svc.LabelHost(ctx, "local", "nope", map[string]string{}); !errors.Is(err, ErrNoHost) {
		t.Fatalf("no host: %v", err)
	}
	if list, _ := svc.Hosts(ctx, "local"); len(list) != 1 || list[0].Labels["zone"] != "a" {
		t.Fatalf("kept %+v", list)
	}
}
