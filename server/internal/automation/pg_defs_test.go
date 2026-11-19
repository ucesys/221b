package automation

import (
	"context"
	"reflect"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// The store keeps its groups and profiles in the database as in a
// ConfigMap; another replica's store reads the same; what is removed is
// gone.
func TestGroupsAndProfilesInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	open := func() *Store {
		return NewStore(&kv.Keyed{Keys: map[string]kv.Backend{GroupsKey: NewGroupsPG(d), ProfilesKey: NewProfilesPG(d)}, Rest: &kv.Memory{}}, nil)
	}
	st := open()
	for _, g := range []Group{{Name: "Patching", Desc: "kernel and package updates"}, {Name: "Storage"}} {
		if err := st.SaveGroup(ctx, "", g); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveGroup(ctx, "Storage", Group{Name: "Disks", Desc: "volumes and arrays"}); err != nil {
		t.Fatal(err)
	}
	saved := RunProfile{ID: "deployer", Name: "deployer", Kind: ProfileKubernetes, Namespace: "ops", ServiceAccount: "deployer", Fleets: []string{"web", "gpu"}, Desc: "rolls the web tier"}
	if err := st.SetProfile(ctx, saved); err != nil {
		t.Fatal(err)
	}
	groups, err := open().Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.Name+"|"+g.Desc)
	}
	if !reflect.DeepEqual(names, []string{"Disks|volumes and arrays", "Patching|kernel and package updates"}) && !reflect.DeepEqual(names, []string{"Patching|kernel and package updates", "Disks|volumes and arrays"}) {
		t.Fatalf("groups read back as %v", names)
	}
	profiles, err := open().Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *RunProfile
	for i := range profiles {
		if profiles[i].ID == saved.ID {
			got = &profiles[i]
		}
	}
	saved.Fleets = []string{"gpu", "web"}
	if got == nil || !reflect.DeepEqual(*got, saved) {
		t.Fatalf("the profile read back as %+v, want %+v (of %d)", got, saved, len(profiles))
	}
	if err := st.DeleteProfile(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(ctx, "Patching"); err != nil {
		t.Fatal(err)
	}
	profiles, _ = open().Profiles(ctx)
	for _, p := range profiles {
		if p.ID == saved.ID {
			t.Fatal("a deleted profile is still there")
		}
	}
	if groups, _ = open().Groups(ctx); len(groups) != 1 || groups[0].Name != "Disks" {
		t.Fatalf("after deleting a group: %+v", groups)
	}
}
