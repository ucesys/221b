package automation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

func TestGroupsLiveBeforeTheirScriptsAndKeepThemWhenRenamed(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	ann := Who{Name: "Ann"}
	if err := st.SaveGroup(ctx, "", Group{Name: "storage", Desc: "disks"}); err != nil {
		t.Fatal(err)
	}
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho x\n"
	if _, err := st.Save(ctx, "s", "storage", text, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(ctx, "old", "made-by-a-script", text, ann, true); err != nil {
		t.Fatal(err)
	}
	all, err := st.Groups(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "made-by-a-script" || all[1].Desc != "disks" {
		t.Fatalf("%+v %v", all, err)
	}
	if err := st.SaveGroup(ctx, "", Group{Name: "storage"}); !errors.Is(err, ErrExists) {
		t.Fatalf("a second group of that name: %v", err)
	}
	if err := st.DeleteGroup(ctx, "storage"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "s") {
		t.Fatalf("a group with a script was deleted: %v", err)
	}
	if err := st.SaveGroup(ctx, "storage", Group{Name: "disks", Desc: "d"}); err != nil {
		t.Fatal(err)
	}
	s, _, _ := st.Script(ctx, "s")
	if s.Group != "disks" {
		t.Fatalf("the script stayed in %q", s.Group)
	}
	if _, err := st.Save(ctx, "s", "", text, ann, false); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(ctx, "disks"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveGroup(ctx, "gone", Group{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestANodeInheritsFromItsFleetsAndItsOwnValueWins(t *testing.T) {
	h := Header{Params: []Param{{Name: "A", Required: true}, {Name: "B"}, {Name: "C"}, {Name: "D"}}}
	layers := []Layer{
		{ID: "web", Name: "Web", Params: FleetVariables{Values: map[string]string{"A": "web", "C": "web"}, Nodes: map[string]map[string]string{"n1": {"C": "mine"}}}},
		{ID: "gpu", Name: "GPU", Params: FleetVariables{Values: map[string]string{"A": "gpu", "B": "gpu", "C": "gpu"}}},
		{ID: "_all", Name: "All nodes", Default: true, Params: FleetVariables{Values: map[string]string{"B": "all", "D": "all"}}},
	}
	r := ResolveLayers(h, layers, "n1", nil)
	if r.Values["B"] != "gpu" || r.Values["C"] != "mine" || r.Sources["C"] != SourceNode || r.Values["D"] != "all" {
		t.Fatalf("%+v", r)
	}
	want := []Conflict{{Node: "n1", Param: "A", Fleets: []string{"GPU", "Web"}}}
	if !reflect.DeepEqual(r.Conflicts, want) {
		t.Fatalf("%+v", r.Conflicts)
	}
	if _, ok := r.Values["A"]; ok {
		t.Fatal("a conflict took a value")
	}
	if got := ResolveLayers(h, layers, "n1", map[string]string{"A": "run"}); len(got.Conflicts) != 0 || got.Values["A"] != "run" {
		t.Fatalf("the run's own value does not settle it: %+v", got)
	}
	if got := VariableConflicts(map[string][]Layer{"n1": layers, "n2": layers[2:]}); !reflect.DeepEqual(got, want) {
		// n1's C has a node value, so only A conflicts; n2 is in no fleet.
		t.Fatalf("%+v", got)
	}
}

func TestAGroupStaysAfterItsLastScriptUntilItIsDeleted(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho x\n"
	if _, err := st.Save(ctx, "s", "storage", text, Who{Name: "Ann"}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	all, err := st.Groups(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "storage" {
		t.Fatalf("the group went with its last script: %+v %v", all, err)
	}
	if err := st.DeleteGroup(ctx, "storage"); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.Groups(ctx); len(all) != 0 {
		t.Fatalf("%+v", all)
	}
}

func TestToApproveLeavesOutWhatThePersonWrote(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	ann, sam := Who{Name: "Ann"}, Who{Name: "Sam"}
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho x\n"
	st.Save(ctx, "s", "", text, ann, true)
	st.Submit(ctx, "s", 1, ann)
	if got, _ := st.ToApprove(ctx, ann); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if got, _ := st.ToApprove(ctx, sam); len(got) != 1 || got[0].Script != "s" {
		t.Fatalf("%+v", got)
	}
}

// A header says nobody or elevated; none and full, the earlier words, still
// read, and a version keeps the value it was hashed and approved with.
func TestAHeaderSaysNobodyOrElevated(t *testing.T) {
	for line, want := range map[string]string{"nobody": PrivilegeNobody, "elevated": PrivilegeElevated, "none": PrivilegeNobody, "full": PrivilegeElevated} {
		h, fs := Check("#!/bin/sh\n# 221b:describe x\n# 221b:privilege " + line + "\necho ok\n")
		if h.Privilege != want {
			t.Fatalf("%s: privilege %q, findings %v", line, h.Privilege, fs)
		}
	}
	if PrivilegeName(PrivilegeElevated) != "elevated" || PrivilegeName(PrivilegeNobody) != "nobody" {
		t.Fatal("the names a person reads")
	}
}
