package automation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ucesys/221b/server/internal/kv"
)

// A node's own values and reboot time are the node's: set from one of its
// fleets, they show with every fleet it is in and are kept once, under
// All nodes. A fleet's page sets those of its members and no other node's.
func TestANodesOwnValuesAreTheNodes(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, nil)
	gpu, db := []string{"n1", "n2"}, []string{"n2", "n3"}
	if err := st.SetVariablesFor(ctx, "c", "gpu", FleetVariables{Values: map[string]string{"MODE": "fast"}, Ceiling: PrivilegeElevated, RebootMinutes: 20,
		Nodes: map[string]map[string]string{"n2": {"MODE": "slow"}}, NodeReboot: map[string]int{"n2": 45}}, gpu); err != nil {
		t.Fatal(err)
	}
	kept, _ := st.Variables(ctx, "c")
	if len(kept["gpu"].Nodes) != 0 || len(kept["gpu"].NodeReboot) != 0 || kept[AllNodes].Nodes["n2"]["MODE"] != "slow" || kept[AllNodes].NodeReboot["n2"] != 45 {
		t.Fatalf("kept: %+v", kept)
	}
	if kept["gpu"].Values["MODE"] != "fast" || kept["gpu"].Ceiling != PrivilegeElevated || kept["gpu"].RebootMinutes != 20 || len(kept[AllNodes].Values) != 0 {
		t.Fatalf("the fleet's own: %+v", kept)
	}
	// The other fleet n2 is in shows it; n3's page of it has nothing.
	if v := View(kept, "db", db); v.Nodes["n2"]["MODE"] != "slow" || v.NodeReboot["n2"] != 45 || len(v.Values) != 0 || v.Ceiling != "" {
		t.Fatalf("db's view: %+v", v)
	}
	if v := View(kept, "db", []string{"n3"}); len(v.Nodes) != 0 || len(v.NodeReboot) != 0 {
		t.Fatalf("a view without n2: %+v", v)
	}
	if v := View(kept, AllNodes, nil); v.Nodes["n2"]["MODE"] != "slow" {
		t.Fatalf("All nodes' view: %+v", v)
	}
	// One value for the node, so its fleets' layers agree: no conflict.
	layers := []Layer{{ID: "gpu", Name: "GPU", Params: View(kept, "gpu", gpu)}, {ID: "db", Name: "DB", Params: View(kept, "db", db)},
		{ID: AllNodes, Name: "All nodes", Params: View(kept, AllNodes, nil), Default: true}}
	if v, source, conflict := Inherited(layers, "n2", "MODE"); v != "slow" || source != SourceNode || conflict != nil {
		t.Fatalf("n2 inherits %q from %q, conflict %v", v, source, conflict)
	}
	if m := RebootMinutes(layers, "n2"); m != 45 {
		t.Fatalf("n2's reboot time: %d", m)
	}

	// db's page sets n3's and leaves n2's as it shows it; n1 is not its.
	if err := st.SetVariablesFor(ctx, "c", "db", FleetVariables{Nodes: map[string]map[string]string{"n2": {"MODE": "slow"}, "n3": {"MODE": "safe"}}, NodeReboot: map[string]int{"n2": 45}}, db); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVariablesFor(ctx, "c", "db", FleetVariables{Nodes: map[string]map[string]string{"n1": {"MODE": "x"}}}, db); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a node of another fleet: %v", err)
	}
	// db's page removes n2's own: gone for gpu too. n3's stays.
	if err := st.SetVariablesFor(ctx, "c", "db", FleetVariables{Nodes: map[string]map[string]string{"n3": {"MODE": "safe"}}}, db); err != nil {
		t.Fatal(err)
	}
	kept, _ = st.Variables(ctx, "c")
	if v := View(kept, "gpu", gpu); len(v.Nodes) != 0 || len(v.NodeReboot) != 0 || v.Values["MODE"] != "fast" {
		t.Fatalf("gpu after db's page removed n2's own: %+v", v)
	}
	if kept[AllNodes].Nodes["n3"]["MODE"] != "safe" {
		t.Fatalf("n3's own: %+v", kept[AllNodes])
	}
	// SetVariables sets the nodes it names and leaves the others.
	if err := st.SetVariables(ctx, "c", "gpu", FleetVariables{Values: map[string]string{"MODE": "fast"}, Nodes: map[string]map[string]string{"n1": {"MODE": "own"}}}); err != nil {
		t.Fatal(err)
	}
	kept, _ = st.Variables(ctx, "c")
	if kept[AllNodes].Nodes["n1"]["MODE"] != "own" || kept[AllNodes].Nodes["n3"]["MODE"] != "safe" || kept["gpu"].Ceiling != "" {
		t.Fatalf("after SetVariables: %+v", kept)
	}
	// It names none: every node keeps its own.
	if err := st.SetVariables(ctx, "c", "gpu", FleetVariables{Values: map[string]string{"MODE": "fast"}}); err != nil {
		t.Fatal(err)
	}
	if kept, _ = st.Variables(ctx, "c"); kept[AllNodes].Nodes["n1"]["MODE"] != "own" || kept[AllNodes].Nodes["n3"]["MODE"] != "safe" {
		t.Fatalf("after SetVariables that names no node: %+v", kept)
	}
	if got := NamedNodes(FleetVariables{}); got == nil {
		t.Fatal("no node named is nil, which means every node")
	}
	// All nodes' are replaced whole, every node's own with them.
	if err := st.SetVariables(ctx, "c", AllNodes, FleetVariables{Values: map[string]string{"ZONE": "a"}, Nodes: map[string]map[string]string{"n9": {"MODE": "gone"}}}); err != nil {
		t.Fatal(err)
	}
	kept, _ = st.Variables(ctx, "c")
	if !reflect.DeepEqual(kept[AllNodes].Nodes, map[string]map[string]string{"n9": {"MODE": "gone"}}) || kept[AllNodes].Values["ZONE"] != "a" {
		t.Fatalf("All nodes replaced: %+v", kept[AllNodes])
	}
}

// A document from before 0.13 kept a node's own values under the fleet
// they were set from. They are read as the node's: what two fleets agreed
// on once, the first fleet's where they differed (a conflict that stopped
// the node's runs then), the longer of two reboot times.
func TestNodeValuesKeptUnderFleetsBecomeTheNodes(t *testing.T) {
	ctx := context.Background()
	b := &kv.Memory{}
	old := variablesRecord{Clusters: map[string]map[string]FleetVariables{"c": {
		"gpu":    {Values: map[string]string{"MODE": "fast"}, Nodes: map[string]map[string]string{"n2": {"MODE": "slow", "DISK": "sda"}}, NodeReboot: map[string]int{"n2": 30}},
		"db":     {Nodes: map[string]map[string]string{"n2": {"MODE": "other", "PORT": "5432"}, "n3": {"MODE": "safe"}}, NodeReboot: map[string]int{"n2": 60}},
		AllNodes: {Values: map[string]string{"ZONE": "a"}, Nodes: map[string]map[string]string{"n2": {"DISK": "nvme0"}}},
	}}}
	raw, _ := json.Marshal(old)
	if _, err := b.SaveKey(ctx, VariablesKey, raw, ""); err != nil {
		t.Fatal(err)
	}
	st := NewStore(b, nil)
	kept, err := st.Variables(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]FleetVariables{
		"gpu": {Values: map[string]string{"MODE": "fast"}},
		AllNodes: {Values: map[string]string{"ZONE": "a"}, NodeReboot: map[string]int{"n2": 60},
			Nodes: map[string]map[string]string{"n2": {"DISK": "nvme0", "MODE": "other", "PORT": "5432"}, "n3": {"MODE": "safe"}}},
	}
	if !reflect.DeepEqual(kept, want) {
		t.Fatalf("read as\n%+v\nwant\n%+v", kept, want)
	}
	// The next save keeps the document that way; a console from before
	// 0.13 reads a node's own values under All nodes as the node's too.
	if err := st.SetVariablesFor(ctx, "c", "gpu", FleetVariables{Values: map[string]string{"MODE": "faster"}}, []string{}); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = b.LoadKey(ctx, VariablesKey)
	var saved variablesRecord
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	want["gpu"] = FleetVariables{Values: map[string]string{"MODE": "faster"}}
	if !reflect.DeepEqual(saved.Clusters["c"], want) {
		t.Fatalf("saved as\n%+v\nwant\n%+v", saved.Clusters["c"], want)
	}
}

// A change that waits is compared and applied over the nodes it names:
// another node's own values, set meanwhile, neither hold it nor are lost.
func TestAnApprovedChangeSetsTheNodesItNames(t *testing.T) {
	ctx := context.Background()
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	st := NewStore(&kv.Memory{}, nil)
	members := []string{"n1", "n2"}
	v0 := FleetVariables{Values: map[string]string{"MODE": "a"}, Nodes: map[string]map[string]string{"n1": {"MODE": "x"}}}
	if err := st.SetVariablesFor(ctx, "c", "db", v0, members); err != nil {
		t.Fatal(err)
	}
	after := FleetVariables{Values: map[string]string{"MODE": "b"}, NodeReboot: map[string]int{"n1": 30}}
	ch, err := st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", By: ann, State: ChangeWaiting, Before: v0, After: after})
	if err != nil {
		t.Fatal(err)
	}
	// n2 gets a value of its own meanwhile, from another of its fleets.
	if err := st.SetVariablesFor(ctx, "c", "web", FleetVariables{Nodes: map[string]map[string]string{"n2": {"MODE": "y"}}}, []string{"n2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideChange(ctx, "c", ch.ID, true, "", sam); err != nil {
		t.Fatalf("approving: %v", err)
	}
	kept, _ := st.Variables(ctx, "c")
	got := View(kept, "db", members)
	want := FleetVariables{Values: map[string]string{"MODE": "b"}, Nodes: map[string]map[string]string{"n2": {"MODE": "y"}}, NodeReboot: map[string]int{"n1": 30}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after the approval\n%+v\nwant\n%+v", got, want)
	}
	// A node the change names, changed meanwhile, holds it.
	ch, _ = st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", By: ann, State: ChangeWaiting, Before: got, After: FleetVariables{Values: map[string]string{"MODE": "b"}}})
	if err := st.SetVariablesFor(ctx, "c", "web", FleetVariables{Nodes: map[string]map[string]string{"n2": {"MODE": "z"}}}, []string{"n2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideChange(ctx, "c", ch.ID, true, "", sam); !errors.Is(err, ErrBusy) {
		t.Fatalf("approving over a node changed meanwhile: %v", err)
	}
}

// A run keeps the fleets its nodes are in, each once, with what each has
// for the parameters the script declares, and each node's fleets.
func TestFleetsOfARun(t *testing.T) {
	h := Header{Params: []Param{{Name: "MODE"}, {Name: "ZONE"}}}
	gpu := Layer{ID: "gpu", Name: "GPU", Params: FleetVariables{Values: map[string]string{"MODE": "fast", "OTHER": "x"}}}
	all := Layer{ID: AllNodes, Name: "All nodes", Params: FleetVariables{Values: map[string]string{"ZONE": "a"}}, Default: true}
	db := Layer{ID: "db", Name: "DB"}
	fleets, of := FleetsOf(h, map[string][]Layer{"n1": {gpu, all}, "n2": {gpu, db, all}, "n3": {all}}, []string{"n1", "n2"})
	want := []RunFleet{{ID: "gpu", Name: "GPU", Values: map[string]string{"MODE": "fast"}}, {ID: AllNodes, Name: "All nodes", Values: map[string]string{"ZONE": "a"}}, {ID: "db", Name: "DB"}}
	if !reflect.DeepEqual(fleets, want) {
		t.Fatalf("the run's fleets\n%+v\nwant\n%+v", fleets, want)
	}
	if !reflect.DeepEqual(of, map[string][]string{"n1": {"gpu", AllNodes}, "n2": {"gpu", "db", AllNodes}}) {
		t.Fatalf("the nodes' fleets: %+v", of)
	}
}
