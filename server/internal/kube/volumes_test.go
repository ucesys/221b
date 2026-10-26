package kube_test

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/sim"
)

// ST-08: the server reads each kubelet's Summary API for the claims its
// pods mount, one request per node, and keeps the figures by claim.
func TestVolumeStatsComeFromTheKubelets(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 10, Pods: 60, KubeletStats: true})
	waitFor(t, "the claims", func() bool { return cl.Count("persistentvolumeclaims", "") > 0 })
	cl.ReadVolumesNow(context.Background())
	v, ok := cl.VolumeStats("monitoring", "prometheus-db-prometheus-0")
	if !ok || v.CapacityBytes == 0 || v.Node == "" {
		t.Fatalf("no stats for prometheus: %+v %v", v, ok)
	}
	if pct := float64(v.UsedBytes) * 100 / float64(v.CapacityBytes); pct < 85 || pct > 87 {
		t.Errorf("prometheus is %.1f%% full, want 86%%", pct)
	}
	// A claim no running pod mounts has none.
	if _, ok := cl.VolumeStats("analytics", "data-cache-2"); ok {
		t.Error("a pending claim has stats")
	}
}

// With kubeletStats off nothing is read.
func TestNoVolumeStatsWithoutKubeletStats(t *testing.T) {
	cl, _, _ := start(t, sim.Options{Nodes: 10, Pods: 60, KubeletStats: false})
	cl.ReadVolumesNow(context.Background())
	if _, ok := cl.VolumeStats("monitoring", "prometheus-db-prometheus-0"); ok {
		t.Error("stats without permission to read the kubelets")
	}
}
