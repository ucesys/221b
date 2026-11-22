package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// Everything the store keeps of a script reads back from the database as
// it does from a ConfigMap: the same steps on both leave the same scripts,
// field for field, and another replica's store reads the same.
func TestScriptsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	// Whole seconds: the database keeps microseconds.
	tick := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { tick = tick.Add(time.Second); return tick }
	open := func() *Store {
		return NewStore(&kv.Keyed{Keys: map[string]kv.Backend{ScriptsKey: NewScriptsPG(d), GroupsKey: NewGroupsPG(d)}, Rest: &kv.Memory{}}, now)
	}
	ada, bob, cy := Who{Name: "Ada", User: "ada"}, Who{Name: "Bob", User: "bob"}, Who{Name: "Cy", User: "cy"}
	node := "#!/bin/sh\n# 221b: describe: Restart the agent\n# 221b: param: UNIT required the unit to restart\n# 221b: param: TOKEN optional\n" +
		"# 221b: privilege: full\n# 221b: dry-run: yes\n# 221b: facts: os.kernel os.family\n[ -n \"$DRY_RUN\" ] && exit 0\nsystemctl restart \"$UNIT\"\n"
	kube := "# 221b: describe: Roll the web tier\n# 221b: target: kubernetes\n# 221b: k8s: patch deployments\n# 221b: k8s: get pods\n" +
		"# 221b: runs-as: ops/deployer\n# 221b: privilege: none\nkubectl rollout restart deployment/web\n"
	steps := func(st *Store) {
		t.Helper()
		must := func(_ Script, err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(st.Save(ctx, "restart-agent", "Patching", node, ada, true))
		must(st.Save(ctx, "restart-agent", "Patching", node+"# again\n", bob, false))
		must(st.Submit(ctx, "restart-agent", 1, ada))
		must(st.SetReview(ctx, "restart-agent", 1, Review{Summary: "Restarts one unit.", At: now(),
			Findings: []ReviewFinding{{Level: LevelWarning, Line: 8, Msg: "no check that the unit exists"}, {Level: LevelWarning, Msg: "no timeout"}}}))
		must(st.Approve(ctx, "restart-agent", 1, cy))
		must(st.Revoke(ctx, "restart-agent", 1, cy))
		must(st.Save(ctx, "restart-agent", "Patching", node+"# second\n", ada, false))
		must(st.Submit(ctx, "restart-agent", 2, ada))
		must(st.Reject(ctx, "restart-agent", 2, cy, "say what it restarts"))
		must(st.Save(ctx, "restart-agent", "Patching", node+"# second\n", ada, false))
		must(st.Save(ctx, "roll-web", "", kube, bob, true))
		must(st.Save(ctx, "scratch", "Storage", "echo hello\n", bob, true))
		if err := st.Delete(ctx, "scratch"); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveGroup(ctx, "Patching", Group{Name: "Updates", Desc: "kernel and packages"}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(st *Store) string {
		t.Helper()
		list, err := st.Scripts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		groups, err := st.Groups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(map[string]any{"scripts": list, "groups": groups}, "", " ")
		return string(b)
	}

	start := tick
	kept := NewStore(&kv.Memory{}, now)
	steps(kept)
	want := read(kept)
	tick = start
	steps(open())
	got := read(open())
	if got != want {
		t.Fatalf("the database's scripts differ from the ConfigMap's.\nthe database:\n%s\nthe ConfigMap:\n%s", got, want)
	}
	// The scenario reaches every part of a version.
	for _, part := range []string{`"editors"`, `"submitted"`, `"revoked"`, `"withdrawn"`, `"rejected"`, `"sameAs"`, `"review"`, `"k8s"`, `"runsAs"`, `"facts"`, `"findings"`, `"Updates"`} {
		if !json.Valid([]byte(want)) || !contains(want, part) {
			t.Errorf("the scenario has no %s", part)
		}
	}
	// A row a script, a row a version, and nothing left of what was deleted.
	var scripts, versions, groups int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM scripts), (SELECT count(*) FROM script_versions),
		(SELECT count(*) FROM script_groups)`).Scan(&scripts, &versions, &groups); err != nil {
		t.Fatal(err)
	}
	if scripts != 2 || versions != 4 {
		t.Errorf("%d scripts and %d versions in the tables, want 2 and 4", scripts, versions)
	}
	// A version keeps its row when its script is saved again.
	var before, after string
	q := "SELECT script_version_id::text FROM script_versions v JOIN scripts s USING (script_id) WHERE s.name = 'roll-web' AND v.version_number = 1"
	if err := d.Pool().QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := open().SetReview(ctx, "roll-web", 1, Review{Summary: "Restarts the rollout.", At: now()}); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, q).Scan(&after); err != nil || after != before {
		t.Errorf("the version's row changed from %s to %s: %v", before, after, err)
	}
}

func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
