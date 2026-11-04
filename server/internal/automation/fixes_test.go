package automation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// A secret written to the output and the errors at once, a byte at a
// time, is blanked in both: each stream holds its own part of a value.
func TestASecretIsBlankedInBothStreamsAtOnce(t *testing.T) {
	const secret = "s3cr3t-value"
	store := newTail(OutputBytes)
	m := newMask(store, []string{secret})
	var wg sync.WaitGroup
	for _, w := range []interface{ Write([]byte) (int, error) }{m, m.Stderr().(*mask)} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				for j := 0; j < len(secret); j++ {
					w.Write([]byte{secret[j]})
				}
				w.Write([]byte("\n"))
			}
		}()
	}
	wg.Wait()
	m.Flush()
	got := store.String()
	if strings.Contains(got, "s3cr") || strings.Count(got, maskText) != 80 {
		t.Fatalf("output %q", got)
	}
}

// A value inside another is blanked with the longer one: none of it shows.
func TestTheLongerSecretIsBlankedWhole(t *testing.T) {
	store := newTail(OutputBytes)
	m := newMask(store, []string{"abc", "abcdef"})
	m.Write([]byte("x abcdef y abc z\n"))
	m.Flush()
	if got := store.String(); got != "x "+maskText+" y "+maskText+" z\n" {
		t.Fatalf("output %q", got)
	}
}

// A script that runs as nobody cannot have its node rebooted.
func TestANobodyScriptCannotReboot(t *testing.T) {
	f := &fakeExec{}
	e, ended := testEngine(t, f)
	r, err := e.Start(context.Background(), Spec{Script: "s", Version: approved(PrivilegeNobody), Cluster: "local", Nodes: []string{"boot-1"}, Rollout: Rollout{Mode: RolloutAll}})
	if err != nil {
		t.Fatal(err)
	}
	r = wait(t, ended)
	if r.Nodes[0].State != NodeFailed || !strings.Contains(r.Nodes[0].Error, "runs as nobody") || f.reboots != 0 {
		t.Fatalf("%+v, %d reboots", r.Nodes[0], f.reboots)
	}
}

// A node is back from a reboot only from a new boot, and only once
// Kubernetes reports it Ready; a reboot command that fails is a failure.
func TestARebootIsBackOnlyFromANewBootAndReady(t *testing.T) {
	run := func(f *fakeExec, ready bool) NodeResult {
		t.Helper()
		ended := make(chan Run, 1)
		e := NewEngine(Options{Store: NewStore(&kv.Memory{}, nil), Exec: f, Unreachable: func(err error) bool { return errors.Is(err, errDown) },
			OnEnd: func(r Run) { ended <- r }, RebootPoll: time.Millisecond, RebootWait: 200 * time.Millisecond, BeatEvery: time.Hour,
			Node: func(string, string) (NodeState, bool) { return NodeState{Ready: ready}, true }})
		if _, err := e.Start(context.Background(), Spec{Script: "s", Version: approved(PrivilegeElevated), Cluster: "local", Nodes: []string{"boot-1"}, Rollout: Rollout{Mode: RolloutAll}}); err != nil {
			t.Fatal(err)
		}
		return wait(t, ended).Nodes[0]
	}
	if n := run(&fakeExec{}, true); n.State != NodeChanged {
		t.Fatalf("a reboot that came back: %+v", n)
	}
	if n := run(&fakeExec{stuck: map[string]bool{"boot-1": true}}, true); n.State != NodeFailed || !strings.Contains(n.Error, "did not reboot") {
		t.Fatalf("a node that never went down: %+v", n)
	}
	if n := run(&fakeExec{rebootExit: 1}, true); n.State != NodeFailed || !strings.Contains(n.Error, "reboot command exited with 1") {
		t.Fatalf("a reboot command that failed: %+v", n)
	}
	if n := run(&fakeExec{}, false); n.State != NodeFailed || !strings.Contains(n.Error, "did not report it Ready") {
		t.Fatalf("a node that never got Ready: %+v", n)
	}
}

// A node's own reboot timeout wins over its fleets'; among several, the
// longest.
func TestTheRebootTimeoutComesFromTheNodeThenTheLongestFleet(t *testing.T) {
	layers := []Layer{
		{ID: "a", Name: "A", Params: FleetVariables{RebootMinutes: 5, NodeReboot: map[string]int{"n1": 3}}},
		{ID: "b", Name: "B", Params: FleetVariables{RebootMinutes: 20}},
		{ID: "_all", Name: "All nodes", Default: true, Params: FleetVariables{RebootMinutes: 15}},
	}
	if got := RebootMinutes(layers, "n1"); got != 3 {
		t.Fatalf("the node's own: %d", got)
	}
	if got := RebootMinutes(layers, "n2"); got != 20 {
		t.Fatalf("the longest fleet's: %d", got)
	}
	layers[1].Params.NodeReboot = map[string]int{"n1": 7}
	if got := RebootMinutes(layers, "n1"); got != 7 {
		t.Fatalf("the node's own, set twice: %d", got)
	}
	if got := RebootMinutes(nil, "n1"); got != 0 {
		t.Fatalf("none set: %d", got)
	}
	for _, bad := range []FleetVariables{{RebootMinutes: -1}, {RebootMinutes: MaxRebootMinutes + 1}, {NodeReboot: map[string]int{"n": 0}}} {
		if ValidateVariables(bad) == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}

// A node runs under the strictest ceiling of its fleets, whichever fleet
// the run picked; in none, under All nodes'.
func TestANodeRunsUnderItsStrictestFleet(t *testing.T) {
	all := Layer{ID: "_all", Name: "All nodes", Default: true, Params: FleetVariables{Ceiling: PrivilegeElevated}}
	db := Layer{ID: "db", Name: "db", Params: FleetVariables{}}
	web := Layer{ID: "web", Name: "web", Params: FleetVariables{Ceiling: PrivilegeElevated}}
	if c, from := NodeCeiling([]Layer{web, db, all}); c != PrivilegeNobody || from != "db" {
		t.Fatalf("%s from %s", c, from)
	}
	if c, from := NodeCeiling([]Layer{web, all}); c != PrivilegeElevated || from != "web" {
		t.Fatalf("%s from %s", c, from)
	}
	if c, from := NodeCeiling([]Layer{all}); c != PrivilegeElevated || from != "All nodes" {
		t.Fatalf("%s from %s", c, from)
	}
}

// A condition reads a parameter in any of a shell's tests, wherever on
// the line.
func TestAConditionIsSeenInEveryFormOfTest(t *testing.T) {
	re := ConditionRE("MODE")
	for _, line := range []string{
		`[ "$MODE" = wipe ] && rm -rf /data`,
		`[[ $MODE == x ]]`,
		`ready && [ "${MODE}" ]`,
		`if [ -n "$MODE" ]; then`,
		`test "$MODE" = a`,
		`until [ $MODE ]; do sleep 1; done`,
		`case "$MODE" in`,
		`x || [ "$MODE" = y ] || exit`,
	} {
		if !re.MatchString(line) {
			t.Errorf("not seen as a condition: %s", line)
		}
	}
	for _, line := range []string{`echo "$MODE"`, `rm -rf /tmp/$MODE`, `MODE2=$MODE`} {
		if re.MatchString(line) {
			t.Errorf("seen as a condition: %s", line)
		}
	}
}

// Only the newest change on a fleet can be approved, and approving it
// applies it over the values it was asked against.
func TestOnlyTheNewestChangeOnAFleetCanBeApproved(t *testing.T) {
	ctx := context.Background()
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	v0 := FleetVariables{Values: map[string]string{"MODE": "a"}}
	setup := func(newer string) (*Store, VariableChange) {
		st := NewStore(&kv.Memory{}, nil)
		if err := st.SetVariables(ctx, "c", "db", v0); err != nil {
			t.Fatal(err)
		}
		old, _ := st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", By: ann, State: ChangeWaiting, Before: v0, After: FleetVariables{Values: map[string]string{"MODE": "b"}}})
		if newer != "" {
			st.NewChange(ctx, VariableChange{Cluster: "c", Fleet: "db", By: ann, State: newer, Before: v0, After: FleetVariables{Values: map[string]string{"MODE": "c"}}})
		}
		return st, old
	}
	for _, newer := range []string{ChangeApplied, ChangeWaiting, ChangeApproved} {
		st, old := setup(newer)
		_, err := st.DecideChange(ctx, "c", old.ID, true, "", sam)
		if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "change 2 on fleet db is newer") || !strings.Contains(err.Error(), "Reject change 1") {
			t.Fatalf("newer %s: %v", newer, err)
		}
		if _, err := st.DecideChange(ctx, "c", old.ID, false, "overtaken", sam); err != nil {
			t.Fatalf("newer %s: rejecting the older one: %v", newer, err)
		}
	}
	st, old := setup(ChangeRejected)
	if _, err := st.DecideChange(ctx, "c", old.ID, true, "", sam); err != nil {
		t.Fatalf("a newer rejected change held it: %v", err)
	}
	if got, _ := st.Variables(ctx, "c"); got["db"].Values["MODE"] != "b" {
		t.Fatalf("not applied: %+v", got)
	}
	// Values changed behind its back, with no change record.
	st, old = setup("")
	st.SetVariables(ctx, "c", "db", FleetVariables{Values: map[string]string{"MODE": "z"}})
	if _, err := st.DecideChange(ctx, "c", old.ID, true, "", sam); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "MODE differ") {
		t.Fatalf("values changed since: %v", err)
	}
	list, _ := st.Changes(ctx, "c")
	if list[0].Overtaken != "" {
		t.Fatalf("the newest is marked overtaken: %+v", list[0])
	}
}

// A node is in one run at a time; a dry run, or a run whose console
// stopped beating, holds nothing; a start asked for twice is one run.
func TestANodeIsInOneRunAtATime(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := NewStore(&kv.Memory{}, func() time.Time { return now })
	ann := Who{Name: "Ann", User: "ann"}
	run := func(dry bool, req string, nodes ...string) (Run, error) {
		r := Run{Script: "s", Version: 1, Cluster: "c", State: RunRunning, Beat: now, DryRun: dry, By: ann, Request: req}
		for _, n := range nodes {
			r.Nodes = append(r.Nodes, NodeResult{Node: n})
		}
		return st.NewRun(ctx, r)
	}
	first, err := run(false, "r1", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(false, "r2", "b", "c"); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "b is in run #1") {
		t.Fatalf("an overlapping run: %v", err)
	}
	if _, err := run(true, "r3", "b"); err != nil {
		t.Fatalf("a dry run: %v", err)
	}
	if again, err := run(false, "r1", "a", "b"); !errors.Is(err, ErrSameRequest) || again.ID != first.ID {
		t.Fatalf("asked twice: %+v %v", again, err)
	}
	now = now.Add(staleAfter + time.Second)
	if _, err := run(false, "r4", "b"); err != nil {
		t.Fatalf("a stale run held its node: %v", err)
	}
}

// A run that ended stays as it ended whatever a late save says, and a
// cancel asked for through the record reaches the console running it.
func TestAnEndedRunStaysEndedAndACancelGoesThroughTheRecord(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, nil)
	r, _ := st.NewRun(ctx, Run{Script: "s", Cluster: "c", State: RunRunning, Beat: time.Now(), Nodes: []NodeResult{{Node: "a"}}})
	r.State = RunSucceeded
	st.PutRun(ctx, r)
	late := r
	late.State = RunRunning
	st.PutRun(ctx, late)
	if list, _ := st.Runs(ctx); list[0].State != RunSucceeded {
		t.Fatalf("a late beat undid the end: %s", list[0].State)
	}

	f := &fakeExec{block: make(chan struct{})}
	ended := make(chan Run, 1)
	e := NewEngine(Options{Store: NewStore(&kv.Memory{}, nil), Exec: f, OnEnd: func(r Run) { ended <- r }, BeatEvery: 5 * time.Millisecond})
	r, err := e.Start(ctx, Spec{Script: "s", Version: approved(PrivilegeElevated), Cluster: "c", Nodes: []string{"ok-1"}, Rollout: Rollout{Mode: RolloutAll}})
	if err != nil {
		t.Fatal(err)
	}
	if asked, err := e.opts.Store.AskCancel(ctx, r.ID); err != nil || !asked {
		t.Fatalf("%v %v", asked, err)
	}
	if got := wait(t, ended); got.State != RunCancelled {
		t.Fatalf("%s", got.State)
	}
}

// A console that loses the lead stops its runs, and says why.
func TestLosingTheLeadStopsTheRuns(t *testing.T) {
	f := &fakeExec{block: make(chan struct{})}
	e, ended := testEngine(t, f)
	if _, err := e.Start(context.Background(), Spec{Script: "s", Version: approved(PrivilegeElevated), Cluster: "c", Nodes: []string{"ok-1"}, Rollout: Rollout{Mode: RolloutAll}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	e.StopAll("lost the lead")
	if got := wait(t, ended); got.State != RunInterrupted || got.Note != "lost the lead" {
		t.Fatalf("%s %q", got.State, got.Note)
	}
}

// A production run stops before a batch when its version lost its
// approval or its text since the run started.
func TestAProductionRunStopsWhenItsVersionIsRevoked(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, nil)
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	st.Save(ctx, "s", "", goodScript, ann, true)
	st.Submit(ctx, "s", 1, ann)
	sc, _ := st.Approve(ctx, "s", 1, sam)
	f := &fakeExec{block: make(chan struct{})}
	ended := make(chan Run, 1)
	e := NewEngine(Options{Store: st, Exec: f, OnEnd: func(r Run) { ended <- r }, BeatEvery: time.Hour})
	v, _ := sc.Version(1)
	if _, err := e.Start(ctx, Spec{Script: "s", Version: v, Cluster: "c", Nodes: []string{"ok-1", "ok-2"}, Rollout: Rollout{Mode: RolloutSize, Size: 1}, Recheck: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revoke(ctx, "s", 1, sam); err != nil {
		t.Fatal(err)
	}
	close(f.block)
	got := wait(t, ended)
	if got.State != RunStopped || !strings.Contains(got.Note, "no longer approved") || got.Nodes[1].State != NodeSkipped {
		t.Fatalf("%s %q %+v", got.State, got.Note, got.Nodes)
	}
	sc, _, _ = st.Script(ctx, "s")
	if v, _ := sc.Version(1); len(v.Withdrawn) != 1 || !v.Withdrawn[0].By.Same(sam) {
		t.Fatalf("revoking lost who had approved: %+v", v)
	}
}

// Control-plane nodes are worked on one at a time, even in one batch.
func TestControlPlaneNodesGoOneAtATime(t *testing.T) {
	f := &fakeExec{}
	ended := make(chan Run, 1)
	e := NewEngine(Options{Store: NewStore(&kv.Memory{}, nil), Exec: f, OnEnd: func(r Run) { ended <- r }, BeatEvery: time.Hour,
		Node: func(_, n string) (NodeState, bool) { return NodeState{Ready: true, ControlPlane: true}, true }})
	if _, err := e.Start(context.Background(), Spec{Script: "s", Version: approved(PrivilegeElevated), Cluster: "c", Nodes: []string{"cp-1", "cp-2", "cp-3"}, Rollout: Rollout{Mode: RolloutAll}}); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, ended); got.State != RunSucceeded || f.peak != 1 {
		t.Fatalf("%s, %d at once", got.State, f.peak)
	}
}

// The newest run alone too big for its record still saves, without its
// nodes' values.
func TestTheNewestRunAloneTooBigStillFits(t *testing.T) {
	rec := &runsRecord{}
	r := Run{ID: 1, State: RunSucceeded}
	for i := 0; i < MaxNodes; i++ {
		r.Nodes = append(r.Nodes, NodeResult{Node: strings.Repeat("n", 40) + string(rune('a'+i%26)), Tail: strings.Repeat("t", 2000), Values: map[string]string{"V": strings.Repeat("v", 1000)}})
	}
	rec.Runs = []Run{r}
	pruneRuns(rec)
	if len(rec.Runs) != 1 || rec.Runs[0].Nodes[0].Values != nil || !strings.Contains(rec.Runs[0].Note, "values not kept") {
		t.Fatalf("%d runs, note %q", len(rec.Runs), rec.Runs[0].Note)
	}
}

// The same text as a rejected version is a new version, which says so.
func TestTheSameTextAfterARejectionIsANewVersion(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, nil)
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	st.Save(ctx, "s", "", goodScript, ann, true)
	st.Submit(ctx, "s", 1, ann)
	st.Reject(ctx, "s", 1, sam, "no")
	sc, err := st.Save(ctx, "s", "", goodScript, ann, false)
	if err != nil || len(sc.Versions) != 2 || sc.Latest().SameAs != 1 || sc.Latest().State != StateDraft {
		t.Fatalf("%+v %v", sc.Latest(), err)
	}
}

// The runs and facts live in their own storage, and move there from the
// scripts' on the first save.
func TestRunsMoveToTheirOwnStorage(t *testing.T) {
	ctx := context.Background()
	main, runs := &kv.Memory{}, &kv.Memory{}
	old := NewStore(main, nil)
	old.NewRun(ctx, Run{Script: "s", Cluster: "c", State: RunSucceeded})
	st := NewStore(main, nil)
	st.SetRunsBackend(runs)
	if list, _ := st.Runs(ctx); len(list) != 1 {
		t.Fatalf("the old runs are not read: %d", len(list))
	}
	st.NewRun(ctx, Run{Script: "s", Cluster: "c", State: RunSucceeded})
	if _, _, err := main.LoadKey(ctx, RunsKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("the runs stayed with the scripts: %v", err)
	}
	if list, _ := st.Runs(ctx); len(list) != 2 {
		t.Fatalf("%d runs", len(list))
	}
}

// What a person may do with a version is worked out once, by the console,
// for the page to show.
func TestWhatAPersonMayDoWithAVersion(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, nil)
	ann, sam, lee := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}, Who{Name: "Lee", User: "lee"}
	st.Save(ctx, "s", "", goodScript, ann, true)
	sc, _ := st.Submit(ctx, "s", 1, ann)
	v, _ := sc.Version(1)
	if m := May(sc, v, ann); m.Approve || m.Reject || !m.Involved || m.RunProduction || !m.RunElsewhere || !m.Dry {
		t.Fatalf("the author, in review: %+v", m)
	}
	if m := May(sc, v, sam); !m.Approve || !m.Reject || m.Involved {
		t.Fatalf("someone else, in review: %+v", m)
	}
	sc, _ = st.Approve(ctx, "s", 1, sam)
	v, _ = sc.Version(1)
	if m := May(sc, v, lee); m.Approve || m.Reject || !m.RunProduction {
		t.Fatalf("approved by someone else: %+v", m)
	}
}
