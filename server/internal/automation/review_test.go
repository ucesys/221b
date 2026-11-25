package automation

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The fixes of the review of server/internal/automation (November 24,
// 2026), each with the test that shows it.

// A name marked sensitive while a run that printed its value goes on
// stays blanked (finding 2): the run's later saves, the nodes that end
// later and the run's page all blank the clear value the run holds,
// rather than write it back over the masked rows.
func TestAMarkMadeDuringARunStaysOnItsOutput(t *testing.T) {
	const marker = "MARKERlive7Kq2token"
	d := dbtest.Open(t)
	ctx := context.Background()
	st := pgSensitiveStore(d, NewMemorySensitive(), nil)
	if err := st.SetVariables(ctx, "local", AllNodes, FleetVariables{Values: map[string]string{"TOKEN": marker}}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	x := &stubExec{run: func(node, _, _ string, out io.Writer) (int, error) {
		if node == "n2" {
			<-release
		}
		io.WriteString(out, "TOKEN="+marker+"\n")
		return 0, nil
	}}
	e := NewEngine(Options{Store: st, Exec: x, BeatEvery: 20 * time.Millisecond})
	r, err := e.Start(ctx, Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Fleet: AllNodes, Nodes: []string{"n1", "n2"},
		Rollout: Rollout{Mode: RolloutSize, Size: 1}, Values: map[string]map[string]string{"n1": {"TOKEN": marker}, "n2": {"TOKEN": marker}}})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		run, _, _, err := e.Get(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Nodes[0].Done() && run.Nodes[1].State == NodeRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("n1 did not end: %+v", run.Nodes)
		}
	}
	// The mark, while n2 runs; the beats go on saving the run.
	if err := st.SetVariables(ctx, "local", AllNodes, FleetVariables{Values: map[string]string{"TOKEN": marker}, Sensitive: []string{"TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	run, out, _, err := e.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(run); strings.Contains(string(b), marker) || strings.Contains(out["n1"], marker) {
		t.Fatalf("the running run's page shows the value: %s %q", b, out["n1"])
	}
	if h := dbtest.Holding(t, d, marker); len(h) > 0 {
		t.Fatalf("while it runs, the database holds the value in %v", h)
	}
	close(release)
	waitRun(t, e, r.ID)
	run, out, _, err = e.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(run); strings.Contains(string(b), marker) || strings.Contains(out["n1"]+out["n2"], marker) || !strings.Contains(out["n2"], "TOKEN="+maskText) {
		t.Fatalf("the ended run's page: %s %q", b, out)
	}
	if h := dbtest.Holding(t, d, marker); len(h) > 0 {
		t.Fatalf("once it ended, the database holds the value in %v", h)
	}
}

// A name marked sensitive through an approved change blanks its old
// clear value in the past (finding 3): past outputs and tails, and the
// values recorded under another name, as a mark saved at once does.
func TestAMarkApprovedThroughAChangeBlanksThePast(t *testing.T) {
	const marker = "MARKERpast3Vb8hunter"
	d := dbtest.Open(t)
	ctx := context.Background()
	st := pgSensitiveStore(d, NewMemorySensitive(), nil)
	if err := st.SetVariables(ctx, "local", AllNodes, FleetVariables{Values: map[string]string{"DB_PASSWORD": marker}}); err != nil {
		t.Fatal(err)
	}
	x := &stubExec{run: func(_, _, _ string, out io.Writer) (int, error) {
		io.WriteString(out, "connecting with "+marker+"\n")
		return 0, nil
	}}
	e := NewEngine(Options{Store: st, Exec: x})
	r, err := e.Start(ctx, Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Fleet: AllNodes, Nodes: []string{"n1"},
		Rollout: Rollout{Mode: RolloutAll}, Values: map[string]map[string]string{"n1": {"P": marker}}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, e, r.ID)
	if h := dbtest.Holding(t, d, marker); h["script_run_outputs"] == 0 || h["script_run_values"] == 0 {
		t.Fatalf("the run did not keep what the test looks for: %v", h)
	}
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	c, err := st.NewChange(ctx, VariableChange{Cluster: "local", Fleet: AllNodes, By: ann, State: ChangeWaiting, Changed: []string{"DB_PASSWORD"},
		Before: FleetVariables{Values: map[string]string{"DB_PASSWORD": marker}},
		After:  FleetVariables{Values: map[string]string{"DB_PASSWORD": marker}, Sensitive: []string{"DB_PASSWORD"}},
		Impact: []ImpactItem{{Script: "DB_PASSWORD", Decision: ImpactApproval, Why: "x", By: "rule"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideChange(ctx, "local", c.ID, true, "", sam); err != nil {
		t.Fatal(err)
	}
	vars, _ := st.Variables(ctx, "local")
	if !IsHidden(vars[AllNodes].Values["DB_PASSWORD"]) {
		t.Fatalf("the mark did not hide the variable: %+v", vars[AllNodes])
	}
	if h := dbtest.Holding(t, d, marker); len(h) > 0 {
		t.Fatalf("after the approved mark the database holds the old value in %v", h)
	}
}
