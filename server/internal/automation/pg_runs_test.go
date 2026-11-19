package automation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// The runs read back from the database as from a ConfigMap: the same
// steps on both leave the same runs, field for field, and another
// replica's store reads the same.
func TestRunsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	// Whole seconds: the database keeps microseconds.
	tick := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { tick = tick.Add(time.Second); return tick }
	open := func() *Store {
		return NewStore(&kv.Keyed{Keys: map[string]kv.Backend{RunsKey: NewRunsPG(d), ScriptsKey: NewScriptsPG(d)}, Rest: &kv.Memory{}}, now)
	}
	ada, bob := Who{Name: "Ada", User: "ada"}, Who{Name: "Bob", User: "bob"}
	steps := func(st *Store) {
		t.Helper()
		if _, err := st.Save(ctx, "restart-agent", "", "echo hi\n", ada, true); err != nil {
			t.Fatal(err)
		}
		at := now()
		exit0, exit3 := 0, 3
		first, err := st.NewRun(ctx, Run{Script: "restart-agent", Version: 1, Hash: "abc123", Privilege: PrivilegeElevated, Cluster: "c1", ClusterName: "Here",
			Fleet: "gpu", FleetName: "GPU", Overrides: map[string]string{"MODE": "fast", "EMPTY": ""}, Rollout: Rollout{Mode: RolloutCanary, Size: 2, StopAfter: 1, TimeoutMinutes: 10},
			By: ada, State: RunRunning, Config: "elevated", Basis: "approved code", Hooks: []string{"drain", "uncordon"}, HookRefs: HookRefs{Before: []string{"pre"}, After: []string{"post", "report"}},
			Request: "req-1", Approvers: []Who{bob, {Name: "Cy"}}, ValuesApproved: &ValuesApproval{Request: 7, By: bob, At: at}, Started: at, Beat: at,
			Nodes: []NodeResult{{Node: "n1", Batch: 1, State: NodeWaiting, Values: map[string]string{"MODE": "fast", "DISK": "sda"}, RebootMinutes: 30},
				{Node: "n2", Batch: 2, State: NodeWaiting, Values: map[string]string{"MODE": "fast"}}}})
		if err != nil || first.ID != 1 {
			t.Fatalf("the first run: %+v: %v", first, err)
		}
		if again, err := st.NewRun(ctx, Run{Script: "restart-agent", Version: 1, Cluster: "c1", By: ada, State: RunRunning, Request: "req-1", Nodes: []NodeResult{{Node: "n9"}}}); !errors.Is(err, ErrSameRequest) || again.ID != 1 {
			t.Fatalf("the same request: %+v: %v", again, err)
		}
		if _, err := st.NewRun(ctx, Run{Script: "restart-agent", Version: 1, Cluster: "c1", By: bob, State: RunRunning, Started: at, Beat: at, Nodes: []NodeResult{{Node: "n2"}}}); !errors.Is(err, ErrBusy) {
			t.Fatalf("a node of a live run: %v", err)
		}
		// A node ran, and the run beat.
		t1 := now()
		first.Nodes[0].State, first.Nodes[0].Exit, first.Nodes[0].Started, first.Nodes[0].Ended = NodeFailed, &exit3, &at, &t1
		first.Nodes[0].Msg, first.Nodes[0].Error, first.Nodes[0].Tail, first.Nodes[0].Cordoned = "restarting", "exit status 3", "line one\nline two\n", true
		first.Beat, first.Note = t1, "stopped after 1 failure"
		if _, err := st.PutRun(ctx, first); err != nil {
			t.Fatal(err)
		}
		if asked, err := st.AskCancel(ctx, first.ID); err != nil || !asked {
			t.Fatalf("asking to cancel: %v, %v", asked, err)
		}
		t2 := now()
		first.Nodes[1].State, first.Nodes[1].Exit, first.Nodes[1].Started, first.Nodes[1].Ended = NodeSkipped, nil, nil, nil
		first.State, first.Ended, first.Beat = RunFailed, &t2, t2
		if cancel, err := st.PutRun(ctx, first); err != nil || !cancel {
			t.Fatalf("the last save: cancel %v: %v", cancel, err)
		}
		// A dry run on the cluster that retries it, by a service account.
		second, err := st.NewRun(ctx, Run{Script: "roll-web", Version: 4, Hash: "def", DryRun: true, Privilege: PrivilegeNobody, Cluster: "c1", Fleet: AllNodes,
			Rollout: Rollout{Mode: RolloutAll, TimeoutMinutes: 5}, By: bob, State: RunRunning, Target: TargetKubernetes, Identity: "ops/deployer", RetryOf: 1, Started: t2, Beat: t2,
			Nodes: []NodeResult{{Node: "cluster", Batch: 1, State: NodeRunning, Started: &t2}}})
		if err != nil || second.ID != 2 {
			t.Fatalf("the second run: %+v: %v", second, err)
		}
		second.Nodes[0].State, second.Nodes[0].Exit, second.State = "done", &exit0, "done"
		t3 := now()
		second.Nodes[0].Ended, second.Ended, second.Beat = &t3, &t3, t3
		if _, err := st.PutRun(ctx, second); err != nil {
			t.Fatal(err)
		}
	}
	read := func(st *Store) string {
		t.Helper()
		runs, err := st.Runs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(runs, "", " ")
		return string(b)
	}
	start := tick
	kept := NewStore(&kv.Memory{}, now)
	steps(kept)
	want := read(kept)
	tick = start
	steps(open())
	if got := read(open()); got != want {
		t.Fatalf("the database's runs differ from the ConfigMap's.\nthe database:\n%s\nthe ConfigMap:\n%s", got, want)
	}
	for _, part := range []string{`"overrides"`, `"hookRefs"`, `"approvers"`, `"valuesApproved"`, `"cancelAsked"`, `"retryOf"`, `"identity"`, `"tail"`, `"cordoned"`, `"rebootMinutes"`, `"exit": 0`, `"exit": 3`, `"EMPTY": ""`} {
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
	if runs, hosts := count("SELECT count(*) FROM script_runs"), count("SELECT count(*) FROM script_run_hosts"); runs != 2 || hosts != 3 {
		t.Errorf("%d runs and %d hosts in the tables, want 2 and 3", runs, hosts)
	}
	// The first run keys to its script's version, which exists, and the
	// second to the run it retries; the second's version does not exist.
	if n := count("SELECT count(*) FROM script_runs r JOIN script_versions v USING (script_version_id) JOIN scripts s USING (script_id) WHERE r.run_number = 1 AND s.name = 'restart-agent'"); n != 1 {
		t.Errorf("the first run's version: %d", n)
	}
	if n := count("SELECT count(*) FROM script_runs r JOIN script_runs o ON o.script_run_id = r.retry_of_script_run_id WHERE r.run_number = 2 AND o.run_number = 1 AND r.script_version_id IS NULL"); n != 1 {
		t.Errorf("the second run's keys: %d", n)
	}
	// What each host got: the view gives its values.
	if n := count("SELECT count(*) FROM script_run_host_values v JOIN script_run_hosts h USING (script_run_host_id) WHERE h.host_name = 'n1' AND ((v.name = 'DISK' AND v.value = 'sda') OR (v.name = 'MODE' AND v.value = 'fast'))"); n != 2 {
		t.Errorf("n1's values through the view: %d", n)
	}
	// A node's row stays when another node of its run is saved.
	var before, after string
	q := "SELECT script_run_host_id::text FROM script_run_hosts WHERE host_name = 'n1'"
	if err := d.Pool().QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	st := open()
	runs, _ := st.Runs(ctx)
	first := runs[len(runs)-1]
	first.Nodes[1].Msg = "skipped: the run stopped"
	if _, err := st.PutRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, q).Scan(&after); err != nil || after != before {
		t.Errorf("n1's row changed from %s to %s: %v", before, after, err)
	}
}

// The engine runs a script over the database's store as over a
// ConfigMap's: each node's result is saved as it comes, and another
// replica's store reads the finished run with every node's part.
func TestTheEngineRunsOverTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	backend := func() kv.Backend {
		return &kv.Keyed{Keys: map[string]kv.Backend{RunsKey: NewRunsPG(d)}, Rest: &kv.Memory{}}
	}
	x := &stubExec{run: func(node, _, _ string, out io.Writer) (int, error) {
		io.WriteString(out, "hello from "+node+"\n")
		if node == "b" {
			return 3, nil
		}
		return 0, nil
	}}
	st := NewStore(backend(), time.Now)
	e := NewEngine(Options{Store: st, Exec: x})
	r, err := e.Start(ctx, Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Fleet: AllNodes, Nodes: []string{"a", "b", "c"},
		Rollout: Rollout{Mode: RolloutSize, Size: 1}, Values: map[string]map[string]string{"a": {"MODE": "x"}, "b": {"MODE": "y"}, "c": {"MODE": "z"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, e, r.ID)
	if done.State == RunRunning || done.Ended == nil {
		t.Fatalf("the run did not end: %+v", done)
	}
	runs, err := NewStore(backend(), time.Now).Runs(ctx)
	if err != nil || len(runs) != 1 || len(runs[0].Nodes) != 3 {
		t.Fatalf("another replica reads %+v: %v", runs, err)
	}
	got := runs[0]
	if a, b := got.Nodes[0], got.Nodes[1]; a.Node != "a" || a.Exit == nil || *a.Exit != 0 || !strings.Contains(a.Tail, "hello from a") || a.Values["MODE"] != "x" ||
		b.Exit == nil || *b.Exit != 3 || b.State != NodeFailed || b.Started == nil || b.Ended == nil {
		t.Fatalf("the nodes' parts: %+v", got.Nodes)
	}
	want, _ := json.Marshal(done)
	have, _ := json.Marshal(got)
	// The engine's own copy has the times to the nanosecond.
	var w, h Run
	_ = json.Unmarshal(want, &w)
	_ = json.Unmarshal(have, &h)
	if w.State != h.State || w.Note != h.Note || len(w.Nodes) != len(h.Nodes) || w.Nodes[2].State != h.Nodes[2].State {
		t.Fatalf("the run as the engine left it\n%s\nas the database has it\n%s", want, have)
	}
	var hosts, values int
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM script_run_hosts), (SELECT count(*) FROM script_run_values WHERE scope = 'host')").Scan(&hosts, &values); err != nil || hosts != 3 || values != 3 {
		t.Fatalf("%d hosts' rows and %d values' rows: %v", hosts, values, err)
	}
}
