package live

import (
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/nodes"
)

// Hosts outside Kubernetes (Phase 12) are rows of the nodes view, and
// fleets take them by their labels; a Kubernetes node of the same name
// comes first, and a host gone is a row gone.
func TestHostsAreNodesOfTheView(t *testing.T) {
	src := fake.New("a")
	s := New(src, Options{Cluster: "local"})
	s.SetSettings(time.Hour, []byte(`{"fleets":{"local":[{"id":"backup","name":"Backup","color":0,"labels":["role=backup"],"taints":[]}]}}`))
	s.Notify(listed())
	s.Process()
	now := time.Now()
	s.SetHosts([]nodes.Row{
		nodes.HostRow("backup-1", map[string]string{"role": "backup"}, now, nodes.Host{Address: "10.0.0.9", Port: 22, LoginUser: "root", State: "paired"}),
		nodes.HostRow("a", nil, now, nodes.Host{Address: "10.0.0.10", Port: 22, LoginUser: "root", State: "pending"}),
	})
	s.Process()
	snap := decodeSnapshot(t, s.Snapshot([]string{ViewNodes, ViewFleets}, nil))
	var names []string
	host := ""
	for _, r := range snap.Views[ViewNodes].Rows {
		names = append(names, rowName(t, r))
		if rowName(t, r) == "backup-1" {
			host = string(r)
		}
	}
	if len(names) != 2 || !strings.Contains(host, `"role":"host"`) || !strings.Contains(host, `"address":"10.0.0.9"`) {
		t.Fatalf("the nodes: %v %s", names, host)
	}
	for _, r := range snap.Views[ViewFleets].Rows {
		if strings.Contains(string(r), `"Backup"`) && !strings.Contains(string(r), `"members":["backup-1"]`) {
			t.Fatalf("the fleet: %s", r)
		}
	}
	s.SetHosts(nil)
	s.Process()
	snap = decodeSnapshot(t, s.Snapshot([]string{ViewNodes}, nil))
	if len(snap.Views[ViewNodes].Rows) != 1 {
		t.Fatalf("the host stayed: %d rows", len(snap.Views[ViewNodes].Rows))
	}
}
