package fleets_test

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/fleets"
)

// seed builds the prototype's ten nodes: three control-plane nodes, four
// general workers and three Dremio executors, worker-07 cordoned.
func seed() []fleets.Node {
	var out []fleets.Node
	zones := []string{"rack-a", "rack-b", "rack-c"}
	add := func(name string, i int, extra map[string]string, taints []corev1.Taint, cores int64) {
		labels := map[string]string{
			"beta.kubernetes.io/arch": "amd64", "beta.kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64",
			"kubernetes.io/hostname": name, "kubernetes.io/os": "linux", "topology.kubernetes.io/zone": zones[i%3],
		}
		for k, v := range extra {
			labels[k] = v
		}
		out = append(out, fleets.Node{Name: name, Labels: labels, Taints: taints, CPU: cores * 1000, Memory: cores << 32, CPUReq: cores * 250, Pods: 5})
	}
	for i := 1; i <= 3; i++ {
		add(fmt.Sprintf("cp-%02d", i), i-1, map[string]string{"node-role.kubernetes.io/control-plane": "", "node.kubernetes.io/exclude-from-external-load-balancers": ""},
			[]corev1.Taint{{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}}, 8)
	}
	for i := 1; i <= 4; i++ {
		add(fmt.Sprintf("worker-%02d", i), i-1, map[string]string{"node-role.kubernetes.io/worker": "", "node.longhorn.io/create-default-disk": "true", "workload": "general"}, nil, 16)
	}
	for i := 5; i <= 7; i++ {
		taints := []corev1.Taint{{Key: "dedicated", Value: "dremio", Effect: corev1.TaintEffectNoSchedule}}
		if i == 7 {
			taints = append(taints, corev1.Taint{Key: "node.kubernetes.io/unschedulable", Effect: corev1.TaintEffectNoSchedule})
		}
		add(fmt.Sprintf("worker-%02d", i), i-1, map[string]string{"node-role.kubernetes.io/worker": "", "node.longhorn.io/create-default-disk": "true", "workload": "dremio-executor", "disktype": "nvme"}, taints, 32)
	}
	out[9].Cordoned = true
	out[9].Errors = 1
	return out
}

var defs = []fleets.Fleet{
	{ID: "cp", Name: "Control plane", Color: 0, Labels: []string{"node-role.kubernetes.io/control-plane="}, Taints: []string{"node-role.kubernetes.io/control-plane:NoSchedule"}},
	{ID: "general", Name: "General workers", Color: 1, Labels: []string{"workload=general"}, Taints: []string{}},
	{ID: "dremio", Name: "Dremio executors", Color: 2, Labels: []string{"workload=dremio-executor", "disktype=nvme"}, Taints: []string{"dedicated=dremio:NoSchedule"}},
}

func rowsByKey(rows []fleets.Row) map[string]fleets.Row {
	m := map[string]fleets.Row{}
	for _, r := range rows {
		m[r.Key] = r
	}
	return m
}

func TestMembershipAndTotals(t *testing.T) {
	if err := fleets.Validate(defs); err != nil {
		t.Fatal(err)
	}
	rows := rowsByKey(fleets.Build(defs, seed()))
	want := map[string]string{
		"fleet:cp":      "cp-01,cp-02,cp-03",
		"fleet:general": "worker-01,worker-02,worker-03,worker-04",
		"fleet:dremio":  "worker-05,worker-06,worker-07",
		"none":          "",
	}
	for key, members := range want {
		if got := strings.Join(rows[key].Members, ","); got != members {
			t.Errorf("%s: %q, want %q", key, got, members)
		}
	}
	d := rows["fleet:dremio"].Stats
	if d.Nodes != 3 || d.CPU != 96000 || d.CPUReq != 24000 || d.Pods != 15 || d.Cordoned != 1 || d.Errors != 1 {
		t.Errorf("dremio totals: %+v", d)
	}
	// Every node is in a fleet: nothing to suggest.
	for _, r := range rows {
		if r.Kind == fleets.KindSuggestion {
			t.Errorf("suggested %+v", r)
		}
	}
}

func TestANewNodeJoinsByItsLabelsAndTaints(t *testing.T) {
	nodes := seed()
	// A general worker joins General workers.
	nodes = append(nodes, fleets.Node{Name: "worker-11", Labels: map[string]string{"workload": "general"}})
	// A Dremio node without the dedicated taint joins no fleet.
	nodes = append(nodes, fleets.Node{Name: "worker-12", Labels: map[string]string{"workload": "dremio-executor", "disktype": "nvme"}})
	// The same taint with another effect is not the fleet's taint.
	nodes = append(nodes, fleets.Node{Name: "worker-13", Labels: map[string]string{"workload": "dremio-executor", "disktype": "nvme"},
		Taints: []corev1.Taint{{Key: "dedicated", Value: "dremio", Effect: corev1.TaintEffectNoExecute}}})
	rows := rowsByKey(fleets.Build(defs, nodes))
	if !strings.Contains(strings.Join(rows["fleet:general"].Members, ","), "worker-11") {
		t.Error("worker-11 did not join General workers")
	}
	if got := strings.Join(rows["none"].Members, ","); got != "worker-12,worker-13" {
		t.Errorf("in no fleet: %q", got)
	}
}

func TestSuggestionsForNodesInNoFleet(t *testing.T) {
	// Without General workers, its four nodes are in no fleet, and the
	// group they form is suggested, picked by workload=general.
	rows := fleets.Build([]fleets.Fleet{defs[0], defs[2]}, seed())
	var sugg []fleets.Row
	for _, r := range rows {
		if r.Kind == fleets.KindSuggestion {
			sugg = append(sugg, r)
		}
	}
	if len(sugg) != 1 {
		t.Fatalf("suggestions: %+v", sugg)
	}
	s := sugg[0]
	if s.Name != "Workers · workload=general" || strings.Join(s.Labels, ",") != "workload=general" || len(s.Taints) != 0 || len(s.Members) != 4 {
		t.Errorf("suggestion: %+v", s)
	}
}

func TestValidate(t *testing.T) {
	bad := []struct {
		list []fleets.Fleet
		want string
	}{
		{[]fleets.Fleet{{ID: "a", Name: " ", Labels: []string{"a=b"}}}, "Give the fleet a name."},
		{[]fleets.Fleet{{ID: "a", Name: "A", Labels: []string{"a=b"}}, {ID: "b", Name: "a", Labels: []string{"a=c"}}}, "Another fleet is already called “a”."},
		{[]fleets.Fleet{{ID: "a", Name: "A"}}, "Pick at least one label or taint."},
		{[]fleets.Fleet{{ID: "a", Name: "A", Labels: []string{"bad key=b"}}}, "isn’t a valid label or taint key"},
		{[]fleets.Fleet{{ID: "a", Name: "A", Labels: []string{"a=b", "a=c"}}}, "one value per label"},
		{[]fleets.Fleet{{ID: "a", Name: "A", Taints: []string{"gpu=true:Sometimes"}}}, "A taint is key=value"},
		{[]fleets.Fleet{{ID: "A B", Name: "A", Labels: []string{"a=b"}}}, "is not valid"},
		{[]fleets.Fleet{{ID: "a", Name: "A", Color: 6, Labels: []string{"a=b"}}}, "color"},
	}
	for _, b := range bad {
		err := fleets.Validate(fleets.Clean(b.list))
		if err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%+v: %v, want %q", b.list, err, b.want)
		}
	}
	if err := fleets.Validate([]fleets.Fleet{{ID: "gpu", Name: "GPU", Labels: []string{"nvidia.com/gpu.present=true"}, Taints: []string{"nvidia.com/gpu:NoSchedule"}}}); err != nil {
		t.Error(err)
	}
}
