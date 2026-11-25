package kube_test

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// A rebooted node does not show the uptime of its last boot while its
// new boot time is read (kube-cut-1).
func TestARebootedNodeDoesNotShowItsOldUptime(t *testing.T) {
	cl, srv, _ := start(t, sim.Options{Nodes: 3, Pods: 6, KubeletStats: true})
	waitFor(t, "boot times", func() bool { return cl.Info().Uptime == kube.UptimeAvailable })
	if _, ok := cl.BootedAt("worker-001"); !ok {
		t.Fatal("no boot time")
	}
	node := cl.Node("worker-001")
	post(t, srv.URL+"/sim/nodes/worker-001/reboot")
	waitFor(t, "the new boot ID", func() bool {
		n := cl.Node("worker-001")
		return n != nil && n.Status.NodeInfo.BootID != node.Status.NodeInfo.BootID
	})
	if at, ok := cl.BootedAt("worker-001"); ok {
		t.Fatalf("the old boot time %v is shown for a new boot", at)
	}
	cl.ReadBootsNow(context.Background())
	if _, ok := cl.BootedAt("worker-001"); !ok {
		t.Fatal("no boot time after the read")
	}
}
