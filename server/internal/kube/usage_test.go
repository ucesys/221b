package kube

import (
	"testing"

	"github.com/ucesys/221b/server/internal/nodes"
)

// Two readings of different pods are not the same because both read zero
// (kube-cut-7): a key missing on one side is a difference.
func TestSameUsageComparesKeys(t *testing.T) {
	a := map[string]nodes.Usage{"shop/web-1": {}}
	b := map[string]nodes.Usage{"shop/web-2": {}}
	if sameUsage(a, b) {
		t.Error("readings of different pods compared the same")
	}
	if !sameUsage(a, map[string]nodes.Usage{"shop/web-1": {}}) {
		t.Error("the same readings compared different")
	}
}
