package automation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// The variables read back from the database as from a ConfigMap: the same
// steps on both leave the same variables. In the database a variable is
// on one fleet or on one host, and the ceiling and the reboot times are
// columns of fleets and hosts.
func TestVariablesInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	open := func() *Store {
		return NewStore(&kv.Keyed{Keys: map[string]kv.Backend{VariablesKey: NewVariablesPG(d)}, Rest: &kv.Memory{}}, nil)
	}
	gpu := []string{"n1", "n2"}
	steps := func(st *Store) {
		t.Helper()
		for _, err := range []error{
			st.SetVariablesFor(ctx, "c1", "gpu", FleetVariables{Values: map[string]string{"MODE": "fast", "EMPTY": ""}, Ceiling: PrivilegeElevated, RebootMinutes: 20,
				Sensitive: []string{"MODE", "TOKEN"}, Nodes: map[string]map[string]string{"n1": {"MODE": "slow"}, "n2": {"DISK": "sda"}}, NodeReboot: map[string]int{"n2": 45}}, gpu),
			st.SetVariablesFor(ctx, "c1", "db", FleetVariables{Values: map[string]string{"PORT": "5432"}}, []string{}),
			st.SetVariables(ctx, "c1", AllNodes, FleetVariables{Values: map[string]string{"ZONE": "a"}, Ceiling: PrivilegeNobody,
				Nodes: map[string]map[string]string{"n1": {"MODE": "slow"}, "n2": {"DISK": "sda"}, "n9": {"MODE": "left"}}, NodeReboot: map[string]int{"n2": 45, "n9": 10}}),
			st.SetVariablesFor(ctx, "c2", "gpu", FleetVariables{Values: map[string]string{"MODE": "other"}}, []string{}),
			// Changed, unset, a node's own removed, a fleet emptied.
			st.SetVariablesFor(ctx, "c1", "gpu", FleetVariables{Values: map[string]string{"MODE": "faster"}, Sensitive: []string{"TOKEN"},
				Nodes: map[string]map[string]string{"n1": {"MODE": "slow", "DISK": "sdb"}}}, gpu),
			st.SetVariablesFor(ctx, "c1", "db", FleetVariables{}, []string{}),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(st *Store) string {
		t.Helper()
		all, err := st.AllVariables(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(all, "", " ")
		return string(b)
	}
	kept := NewStore(&kv.Memory{}, nil)
	steps(kept)
	want := read(kept)
	steps(open())
	if got := read(open()); got != want {
		t.Fatalf("the database's variables differ from the ConfigMap's.\nthe database:\n%s\nthe ConfigMap:\n%s", got, want)
	}
	for _, part := range []string{`"faster"`, `"TOKEN"`, `"sdb"`, `"left"`, `"nodeReboot"`, `"ceiling"`, `"other"`} {
		if !contains(want, part) {
			t.Errorf("the scenario has no %s", part)
		}
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// All nodes is the cluster's fleet of every node; gpu, which no list of
	// fleets has here, is unlisted; db, with nothing set, has no row.
	if n := count("SELECT count(*) FROM fleets WHERE cluster_id = 'c1' AND fleet_key = '_all' AND kind = 'all' AND privilege_ceiling = 'none'"); n != 1 {
		t.Errorf("All nodes' row: %d", n)
	}
	if n := count("SELECT count(*) FROM fleets WHERE cluster_id = 'c1' AND fleet_key = 'gpu' AND kind = 'unlisted' AND privilege_ceiling = '' AND reboot_minutes IS NULL"); n != 1 {
		t.Errorf("gpu's row: %d", n)
	}
	if n := count("SELECT count(*) FROM fleets WHERE fleet_key = 'db'"); n != 0 {
		t.Errorf("db's row stayed: %d", n)
	}
	// On one fleet or on one host; a name only marked sensitive has no value.
	if n := count("SELECT count(*) FROM variables v JOIN fleets f USING (fleet_id) WHERE f.cluster_id = 'c1' AND f.fleet_key = 'gpu'"); n != 2 {
		t.Errorf("gpu's variables: %d, want MODE and TOKEN", n)
	}
	if n := count("SELECT count(*) FROM variables v JOIN fleets f USING (fleet_id) WHERE f.fleet_key = 'gpu' AND v.name = 'TOKEN' AND v.value IS NULL AND v.is_sensitive AND v.scope = 'fleet'"); n != 1 {
		t.Errorf("TOKEN, only marked sensitive: %d", n)
	}
	if n := count("SELECT count(*) FROM variables v JOIN hosts h USING (host_id) WHERE h.cluster_id = 'c1' AND h.name = 'n1' AND v.scope = 'host' AND v.fleet_id IS NULL"); n != 2 {
		t.Errorf("n1's own variables: %d", n)
	}
	// n2 has nothing of its own any more, and nothing else points at it.
	if n := count("SELECT count(*) FROM hosts WHERE name = 'n2'"); n != 0 {
		t.Errorf("n2's row stayed: %d", n)
	}

	// A variable that did not change keeps its row.
	var before, after string
	q := "SELECT v.variable_id::text FROM variables v JOIN hosts h USING (host_id) WHERE h.name = 'n1' AND v.name = 'MODE'"
	if err := d.Pool().QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := open().SetVariablesFor(ctx, "c1", "gpu", FleetVariables{Values: map[string]string{"MODE": "fastest"}, Nodes: map[string]map[string]string{"n1": {"MODE": "slow"}}}, gpu); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, q).Scan(&after); err != nil || after != before {
		t.Errorf("the variable's row changed from %s to %s: %v", before, after, err)
	}
}

// A ConfigMap's document from before 0.13, with nodes' own values under
// their fleets, is copied as the nodes' own.
func TestVariablesCopiedFromAConfigMap(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	old := &kv.Memory{}
	raw, _ := json.Marshal(variablesRecord{Clusters: map[string]map[string]FleetVariables{"c": {
		"gpu": {Values: map[string]string{"MODE": "fast"}, Nodes: map[string]map[string]string{"n2": {"MODE": "slow"}}, NodeReboot: map[string]int{"n2": 30}},
	}}})
	if _, err := old.SaveKey(ctx, VariablesKey, raw, ""); err != nil {
		t.Fatal(err)
	}
	doc := NewVariablesPG(d)
	if copied, err := doc.Import(ctx, old, "test"); err != nil || !copied {
		t.Fatalf("copied %v: %v", copied, err)
	}
	st := NewStore(&kv.Keyed{Keys: map[string]kv.Backend{VariablesKey: doc}, Rest: &kv.Memory{}}, nil)
	kept, err := st.Variables(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(kept["gpu"].Nodes) != 0 || kept["gpu"].Values["MODE"] != "fast" || kept[AllNodes].Nodes["n2"]["MODE"] != "slow" || kept[AllNodes].NodeReboot["n2"] != 30 {
		t.Fatalf("copied as %+v", kept)
	}
	var minutes int
	if err := d.Pool().QueryRow(ctx, "SELECT reboot_minutes FROM hosts WHERE cluster_id = 'c' AND name = 'n2'").Scan(&minutes); err != nil || minutes != 30 {
		t.Fatalf("n2's reboot time in its row: %d: %v", minutes, err)
	}
}

// A variable's row names who set it (variables.updated_by, DB-87): the
// person a save is made for, and for an approved change the person who
// asked for it. A value the save leaves as it was keeps its name, and a
// save that says no one names no one.
func TestAVariableNamesWhoSetIt(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	st := NewStore(&kv.Keyed{Keys: map[string]kv.Backend{VariablesKey: NewVariablesPG(d), ChangesKey: NewChangesPG(d)}, Rest: &kv.Memory{}}, nil)
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	by := func(name string) string {
		t.Helper()
		var who string
		if err := d.Pool().QueryRow(ctx, "SELECT updated_by FROM variables WHERE name = $1", name).Scan(&who); err != nil {
			t.Fatal(err)
		}
		return who
	}
	if err := st.SetVariables(SetBy(ctx, ann), "c1", AllNodes, FleetVariables{Values: map[string]string{"MODE": "fast", "ZONE": "a"}}); err != nil {
		t.Fatal(err)
	}
	if by("MODE") != "Ann" || by("ZONE") != "Ann" {
		t.Fatalf("set by Ann, the rows name %q and %q", by("MODE"), by("ZONE"))
	}
	// Sam changes ZONE through a change Ann approves.
	vars, _ := st.Variables(ctx, "c1")
	before := View(vars, AllNodes, nil)
	after := View(vars, AllNodes, nil)
	after.Values = map[string]string{"MODE": "fast", "ZONE": "b"}
	c, err := st.NewChange(ctx, VariableChange{Cluster: "c1", Fleet: AllNodes, By: sam, Changed: []string{"ZONE"}, Before: before, After: after, State: ChangeWaiting})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideChange(SetBy(ctx, ann), "c1", c.ID, true, "", ann); err != nil {
		t.Fatal(err)
	}
	if by("ZONE") != "Sam" || by("MODE") != "Ann" {
		t.Fatalf("after Sam's change, ZONE names %q and MODE %q", by("ZONE"), by("MODE"))
	}
	// A save for no one in particular.
	if err := st.SetVariables(ctx, "c1", AllNodes, FleetVariables{Values: map[string]string{"MODE": "fast", "ZONE": "b", "NEW": "x"}}); err != nil {
		t.Fatal(err)
	}
	if by("NEW") != "" || by("ZONE") != "Sam" {
		t.Fatalf("NEW names %q, ZONE %q", by("NEW"), by("ZONE"))
	}
	// The user, where there is no name.
	if err := st.SetVariables(SetBy(ctx, Who{User: "cy"}), "c1", AllNodes, FleetVariables{Values: map[string]string{"MODE": "slow", "ZONE": "b", "NEW": "x"}}); err != nil {
		t.Fatal(err)
	}
	if by("MODE") != "cy" {
		t.Fatalf("MODE names %q", by("MODE"))
	}
}
