package automation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// What a change alters, read back as variables before and after, is the
// same change; applied to variables that have other things set too, it
// changes what it names and nothing else.
func TestAChangeIsWhatItAlters(t *testing.T) {
	before := FleetVariables{Values: map[string]string{"MODE": "a", "KEEP": "k", "GONE": "g"}, Ceiling: PrivilegeNobody, Sensitive: []string{"MODE", "OLD"}, RebootMinutes: 10,
		Nodes: map[string]map[string]string{"n1": {"MODE": "x", "SAME": "s"}, "n2": {"DISK": "sda"}}, NodeReboot: map[string]int{"n1": 20}}
	after := FleetVariables{Values: map[string]string{"MODE": "b", "KEEP": "k", "NEW": ""}, Ceiling: PrivilegeElevated, Sensitive: []string{"MODE", "NEW"}, RebootMinutes: 15,
		Nodes: map[string]map[string]string{"n1": {"MODE": "y", "SAME": "s"}, "n3": {"DISK": "sdb"}}, NodeReboot: map[string]int{"n3": 30}}
	items := Items(before, after)
	kinds := map[string]int{}
	for _, it := range items {
		kinds[it.Kind]++
	}
	if want := map[string]int{ItemValue: 3, ItemCeiling: 1, ItemSensitive: 2, ItemReboot: 1, ItemNodeValue: 3, ItemNodeReboot: 2}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("items by kind: %v, want %v", kinds, want)
	}
	// The sides hold only what changed, and give the same items again.
	b, a := Sides(items)
	if _, kept := b.Values["KEEP"]; kept || b.Nodes["n1"]["SAME"] != "" {
		t.Fatalf("the sides hold what did not change: %+v", b)
	}
	if again := Items(b, a); !reflect.DeepEqual(again, items) {
		t.Fatalf("the sides' items\n%+v\nwant\n%+v", again, items)
	}
	// Applied where something else was set meanwhile.
	cur := before
	cur.Values = map[string]string{"MODE": "a", "KEEP": "k", "GONE": "g", "OTHER": "o"}
	if done, moved := weigh(cur, items); done || len(moved) != 0 {
		t.Fatalf("before applying: done %v, moved %v", done, moved)
	}
	got := applyItems(cur, items)
	want := after
	want.Values = map[string]string{"MODE": "b", "KEEP": "k", "NEW": "", "OTHER": "o"}
	if !got.Same(want) || RebootChanged(got, want) {
		t.Fatalf("applied\n%+v\nwant\n%+v", got, want)
	}
	if done, _ := weigh(got, items); !done {
		t.Fatal("not done after applying")
	}
	cur.Values["MODE"] = "z"
	if _, moved := weigh(cur, items); !reflect.DeepEqual(moved, []string{"MODE"}) {
		t.Fatalf("a value changed meanwhile: %v", moved)
	}
}

// Approving a change sets what it alters: a value of the fleet that
// someone set meanwhile, and that the change does not name, neither holds
// it nor is lost.
func TestAnApprovedChangeLeavesWhatItDoesNotName(t *testing.T) {
	ctx := context.Background()
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	st := NewStore(&kv.Memory{}, nil)
	v0 := FleetVariables{Values: map[string]string{"MODE": "a", "PORT": "1"}}
	if err := st.SetVariablesFor(ctx, "c", "db", v0, []string{}); err != nil {
		t.Fatal(err)
	}
	ch, err := st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", By: ann, State: ChangeWaiting, Before: v0, After: FleetVariables{Values: map[string]string{"MODE": "b", "PORT": "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetVariablesFor(ctx, "c", "db", FleetVariables{Values: map[string]string{"MODE": "a", "PORT": "2", "ZONE": "z"}}, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideChange(ctx, "c", ch.ID, true, "", sam); err != nil {
		t.Fatalf("approving: %v", err)
	}
	kept, _ := st.Variables(ctx, "c")
	if want := map[string]string{"MODE": "b", "PORT": "2", "ZONE": "z"}; !reflect.DeepEqual(kept["db"].Values, want) {
		t.Fatalf("after the approval %v, want %v", kept["db"].Values, want)
	}
}

// The changes read back from the database as from a ConfigMap, each as
// what it alters; approving one there sets the same variables.
func TestVariableChangesInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	tick := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { tick = tick.Add(time.Second); return tick }
	open := func() *Store {
		return NewStore(&kv.Keyed{Keys: map[string]kv.Backend{VariablesKey: NewVariablesPG(d), ChangesKey: NewChangesPG(d)}, Rest: &kv.Memory{}}, now)
	}
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	members := []string{"n1", "n2"}
	v0 := FleetVariables{Values: map[string]string{"MODE": "a", "PORT": "1"}, Sensitive: []string{"MODE"}, Nodes: map[string]map[string]string{"n1": {"MODE": "x"}}}
	v1 := FleetVariables{Values: map[string]string{"MODE": "b", "PORT": "1"}, Sensitive: []string{"MODE"}, Ceiling: PrivilegeElevated, RebootMinutes: 15,
		Nodes: map[string]map[string]string{"n2": {"DISK": "sda"}}, NodeReboot: map[string]int{"n1": 30}}
	steps := func(st *Store) {
		t.Helper()
		if err := st.SetVariablesFor(ctx, "c", "db", v0, members); err != nil {
			t.Fatal(err)
		}
		applied, err := st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", FleetName: "DB", By: ann, Reason: "first", State: ChangeApplied, Changed: []string{"PORT"},
			Before: FleetVariables{}, After: v0, Impact: []ImpactItem{}})
		if err != nil || applied.ID != 1 {
			t.Fatalf("the first change: %+v: %v", applied, err)
		}
		rejected, _ := st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", FleetName: "DB", By: ann, State: ChangeWaiting, Changed: []string{"MODE"},
			Before: v0, After: FleetVariables{Values: map[string]string{"PORT": "1"}}, Note: "Sherlock was off"})
		if _, err := st.DecideChange(ctx, "c", rejected.ID, false, "not this week", sam); err != nil {
			t.Fatal(err)
		}
		waiting, _ := st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", FleetName: "DB", By: ann, Reason: "faster", State: ChangeWaiting,
			Changed: []string{"MODE", "DISK", "privilege ceiling", "reboot timeout"}, Before: v0, After: v1,
			Impact: []ImpactItem{{Script: "restart", Version: 2, Decision: ImpactApproval, Why: "a condition reads MODE", By: "rule"}, {Script: "every script of this fleet", Decision: ImpactApproval, Why: "the ceiling rises to full", By: "rule"}}})
		if _, err := st.DecideChange(ctx, "c", waiting.ID, true, "", ann); !errors.Is(err, ErrOwnWork) {
			t.Fatalf("approved by who asked: %v", err)
		}
		if _, err := st.DecideChange(ctx, "c", waiting.ID, true, "", sam); err != nil {
			t.Fatalf("approving: %v", err)
		}
		if _, err := st.NewChange(ctx, VariableChange{Cluster: "c2", Fleet: AllNodes, By: sam, State: ChangeWaiting, Changed: []string{"ZONE"},
			Before: FleetVariables{}, After: FleetVariables{Values: map[string]string{"ZONE": "a"}}}); err != nil {
			t.Fatal(err)
		}
	}
	// As the database keeps them: what each change alters, and no nil list.
	read := func(st *Store) string {
		t.Helper()
		all, err := st.AllChanges(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range all {
			all[i].Before, all[i].After = Sides(Items(all[i].Before, all[i].After))
			if all[i].Impact == nil {
				all[i].Impact = []ImpactItem{}
			}
		}
		vars, err := st.AllVariables(ctx)
		if err != nil {
			t.Fatal(err)
		}
		waiting, _ := st.WaitingChanges(ctx)
		b, _ := json.MarshalIndent(map[string]any{"changes": all, "variables": vars, "waiting": len(waiting)}, "", " ")
		return string(b)
	}
	start := tick
	kept := NewStore(&kv.Memory{}, now)
	steps(kept)
	want := read(kept)
	tick = start
	steps(open())
	if got := read(open()); got != want {
		t.Fatalf("the database's changes differ from the ConfigMap's.\nthe database:\n%s\nthe ConfigMap:\n%s", got, want)
	}
	for _, part := range []string{`"approved"`, `"rejected"`, `"not this week"`, `"decidedBy"`, `"Sherlock was off"`, `"a condition reads MODE"`, `"nodeReboot"`, `"sda"`, `"full"`} {
		if !contains(want, part) {
			t.Errorf("the scenario has no %s", part)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT count(*) FROM variable_changes"); n != 4 {
		t.Errorf("%d changes' rows, want 4", n)
	}
	// The approved change: MODE, the ceiling, the reboot time, n1's own MODE
	// gone, n1's reboot time, n2's DISK. PORT and the mark did not change.
	// The change marks MODE sensitive, so what it found of MODE and what it
	// set are digests, never the values (DB-71).
	if n := count("SELECT count(*) FROM variable_change_items i JOIN variable_changes c USING (variable_change_id) WHERE c.change_number = 3"); n != 6 {
		t.Errorf("the approved change has %d items, want 6", n)
	}
	if n := count("SELECT count(*) FROM variable_change_items i JOIN variable_changes c USING (variable_change_id) WHERE c.change_number = 3 AND i.kind = 'node_value' AND i.node = 'n1' AND i.name = 'MODE' AND i.old_value IS NULL AND i.old_digest LIKE 'hidden:%' AND i.new_value IS NULL AND i.new_digest IS NULL"); n != 1 {
		t.Errorf("n1's own MODE, removed: %d rows", n)
	}
	if n := count("SELECT count(*) FROM variable_change_impacts i JOIN variable_changes c USING (variable_change_id) WHERE c.change_number = 3"); n != 2 {
		t.Errorf("the approved change touches %d versions, want 2", n)
	}
}
