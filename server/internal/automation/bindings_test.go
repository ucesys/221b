package automation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/kv"
)

// --- Run values bound to a variable (FU-04) ---

func bindLayers() map[string][]Layer {
	web := Layer{ID: "web", Name: "Web", Params: FleetVariables{Values: map[string]string{"PKG": "nginx"}, Nodes: map[string]map[string]string{"w2": {"PKG": "nginx-2"}}}}
	db := Layer{ID: "db", Name: "DB", Params: FleetVariables{Values: map[string]string{"DISK": "/dev/sdb"}}}
	all := Layer{ID: "_all", Name: "All nodes", Default: true, Params: FleetVariables{Values: map[string]string{"ZONE": "eu"}}}
	return map[string][]Layer{"w1": {web, all}, "w2": {web, all}, "d1": {db, all}}
}

func TestABindingGivesEachNodeItsOwnValueOfTheVariable(t *testing.T) {
	h := Header{Params: []Param{{Name: "TARGET", Required: true}, {Name: "OPT"}}}
	layers := bindLayers()
	run := map[string]string{"TARGET": "var:PKG"}
	got := map[string]string{}
	for _, n := range []string{"w1", "w2"} {
		r := ResolveLayers(h, layers[n], n, run)
		got[n] = r.Values["TARGET"]
		if r.Sources["TARGET"] != SourceVariable || r.Bound["TARGET"] != "PKG" || len(r.Missing) > 0 {
			t.Fatalf("%s: %+v", n, r)
		}
	}
	if want := map[string]string{"w1": "nginx", "w2": "nginx-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Set for every node: no gap.
	res := map[string]Resolved{"w1": ResolveLayers(h, layers["w1"], "w1", run), "w2": ResolveLayers(h, layers["w2"], "w2", run)}
	if unset, none := BindingGaps(h, run, res); len(unset) != 0 || len(none) != 0 {
		t.Fatalf("gaps %v %v", unset, none)
	}
}

func TestABindingSomeNodesLackIsMissingWhereRequiredAndEmptyWhereNot(t *testing.T) {
	layers := bindLayers()
	for _, required := range []bool{true, false} {
		h := Header{Params: []Param{{Name: "TARGET", Required: required}}}
		run := map[string]string{"TARGET": "var:PKG"}
		res := map[string]Resolved{}
		for _, n := range []string{"w1", "d1"} {
			res[n] = ResolveLayers(h, layers[n], n, run)
		}
		if _, ok := res["d1"].Values["TARGET"]; ok {
			t.Fatalf("d1 has no PKG, so no value: %+v", res["d1"])
		}
		wantMissing := []string(nil)
		if required {
			wantMissing = []string{"TARGET (bound to variable PKG, not set here)"}
		}
		if !reflect.DeepEqual(res["d1"].Missing, wantMissing) {
			t.Fatalf("required %v: missing %v", required, res["d1"].Missing)
		}
		unset, none := BindingGaps(h, run, res)
		if !reflect.DeepEqual(unset, map[string][]string{"TARGET": {"d1"}}) || len(none) != 0 {
			t.Fatalf("required %v: %v %v", required, unset, none)
		}
	}
}

func TestABindingNoNodeHasStopsTheRun(t *testing.T) {
	layers := bindLayers()
	h := Header{Params: []Param{{Name: "TARGET"}}}
	run := map[string]string{"TARGET": "var:NOWHERE"}
	res := map[string]Resolved{"w1": ResolveLayers(h, layers["w1"], "w1", run), "d1": ResolveLayers(h, layers["d1"], "d1", run)}
	_, none := BindingGaps(h, run, res)
	if !reflect.DeepEqual(none, []string{"TARGET is bound to variable NOWHERE, which no node of this run has"}) {
		t.Fatalf("%v", none)
	}
}

func TestAConstantNeverBinds(t *testing.T) {
	layers := bindLayers()
	h := Header{Params: []Param{{Name: "A"}, {Name: "B"}, {Name: "S", Secret: true}}}
	// Escaped, a text that starts like a binding is the text; a secret's
	// value is a Secret's reference and is never read as a binding.
	r := ResolveLayers(h, layers["w1"], "w1", map[string]string{"A": `\var:PKG`, "B": `\\var:x`, "S": "var:PKG"})
	want := map[string]string{"A": "var:PKG", "B": `\var:x`, "S": "var:PKG"}
	if !reflect.DeepEqual(r.Values, want) || len(r.Bound) != 0 {
		t.Fatalf("%+v", r)
	}
	for text, value := range map[string]string{"var:PKG": `\var:PKG`, `\var:x`: `\\var:x`, "plain": "plain", "variable": "variable"} {
		if got := EscapeConstant(text); got != value || Constant(got) != text {
			t.Errorf("%q: %q, back %q", text, got, Constant(got))
		}
	}
	if name, ok := Bound("var:PKG"); !ok || name != "PKG" {
		t.Fatal("var:PKG binds to PKG")
	}
	for _, bad := range []string{"var:pkg", "var:", "var:A B", " var:PKG"} {
		if _, ok := Bound(bad); ok {
			t.Errorf("%q binds", bad)
		}
	}
	if err := ValidateOverrides(map[string]string{"A": "var:lower"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a var: that names no variable: %v", err)
	}
	if err := ValidateOverrides(map[string]string{"A": "var:PKG", "B": `\var:lower`}); err != nil {
		t.Fatal(err)
	}
}

func TestABindingToAVariableTwoFleetsSetIsAConflict(t *testing.T) {
	a := Layer{ID: "a", Name: "A", Params: FleetVariables{Values: map[string]string{"PKG": "x"}}}
	b := Layer{ID: "b", Name: "B", Params: FleetVariables{Values: map[string]string{"PKG": "y"}}}
	h := Header{Params: []Param{{Name: "TARGET", Required: true}}}
	r := ResolveLayers(h, []Layer{a, b}, "n", map[string]string{"TARGET": "var:PKG"})
	if len(r.Conflicts) != 1 || r.Conflicts[0].Param != "PKG" || len(r.Missing) != 0 {
		t.Fatalf("%+v", r)
	}
}

// --- Runs that wait for approval (FU-05) ---

func TestAWaitingRunIsDecidedBySomeoneElse(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, nil)
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	w, err := st.NewWaitingRun(ctx, WaitingRun{Cluster: "c", Script: "s", Version: 2, Fleet: "web", Params: map[string]string{"A": "1"}, By: ann, Request: "r1"})
	if err != nil || w.ID != 1 || w.State != ChangeWaiting {
		t.Fatalf("%+v %v", w, err)
	}
	// Asked twice with the same request, it waits once.
	if again, err := st.NewWaitingRun(ctx, WaitingRun{Cluster: "c", Script: "s", By: ann, Request: "r1"}); err != nil || again.ID != 1 {
		t.Fatalf("%+v %v", again, err)
	}
	if _, err := st.WaitingRunFor(ctx, "c", 1, ann); !errors.Is(err, ErrOwnWork) {
		t.Fatalf("the asker approves: %v", err)
	}
	if _, err := st.DecideWaitingRun(ctx, "c", 1, true, "", ann, 9); !errors.Is(err, ErrOwnWork) {
		t.Fatalf("the asker approves: %v", err)
	}
	if _, err := st.DecideWaitingRun(ctx, "c", 1, false, " ", sam, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a rejection without a reason: %v", err)
	}
	if _, err := st.WaitingRunFor(ctx, "other", 1, sam); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another cluster's: %v", err)
	}
	got, err := st.DecideWaitingRun(ctx, "c", 1, true, "", sam, 7)
	if err != nil || got.State != ChangeApproved || got.Run != 7 || got.DecidedBy.Name != "Sam" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := st.DecideWaitingRun(ctx, "c", 1, false, "late", sam, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("decided twice: %v", err)
	}
	// The asker may withdraw their own.
	w2, _ := st.NewWaitingRun(ctx, WaitingRun{Cluster: "c", Script: "s", By: ann})
	if got, err := st.DecideWaitingRun(ctx, "c", w2.ID, false, "not now", ann, 0); err != nil || got.State != ChangeRejected || got.Comment != "not now" {
		t.Fatalf("%+v %v", got, err)
	}
	list, _ := st.WaitingRuns(ctx, "")
	if len(list) != 2 || list[0].ID != 2 {
		t.Fatalf("newest first: %+v", list)
	}
	// The variable changes kept beside them are untouched.
	if changes, _ := st.Changes(ctx, "c"); len(changes) != 0 {
		t.Fatalf("%+v", changes)
	}
}

// --- An empty value (FU-07) ---

func TestAnEmptyRunValueIsTheEmptyStringAndNoValueWhenRequired(t *testing.T) {
	layers := bindLayers()
	h := Header{Params: []Param{{Name: "PKG"}, {Name: "ZONE", Required: true}, {Name: "OPT"}}}
	r := ResolveLayers(h, layers["w1"], "w1", map[string]string{"PKG": "", "ZONE": ""})
	// PKG: the empty string wins over the fleet's nginx, and is set.
	if v, ok := r.Values["PKG"]; !ok || v != "" || r.Sources["PKG"] != SourceRun {
		t.Fatalf("PKG: %+v", r)
	}
	// ZONE is required: an empty string is no value, whatever All nodes sets.
	if want := []string{"ZONE (an empty string)"}; !reflect.DeepEqual(r.Missing, want) {
		t.Fatalf("missing %v, want %v", r.Missing, want)
	}
	// OPT, not given and set nowhere, is not set at all.
	if _, ok := r.Values["OPT"]; ok {
		t.Fatalf("OPT: %+v", r)
	}
	env := Env(Run{ID: 1}, "w1", r.Values)
	if v, ok := env["PKG"]; !ok || v != "" {
		t.Fatalf("env PKG %q %v", v, ok)
	}
	if _, ok := env["OPT"]; ok {
		t.Fatal("OPT is in the environment")
	}
	if cmd := Command(env); !strings.Contains(cmd, " PKG='' ") {
		t.Fatalf("command %s", cmd)
	}
}
