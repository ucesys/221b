package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
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

// racing is a backend that, armed, runs on once in the middle of the
// first read of the changes that follows: what another admin does while a
// decision is between its check and its effect.
type racing struct {
	kv.Backend
	armed atomic.Bool
	on    func()
}

func (r *racing) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	b, v, err := r.Backend.LoadKey(ctx, key)
	if key == ChangesKey && r.armed.CompareAndSwap(true, false) {
		r.on()
	}
	return b, v, err
}

// An approval and a rejection of one change never both take effect
// (finding 5): whichever comes first decides it, and the other finds it
// decided; the variables hold the change's values exactly when it is
// approved. In a store of documents and in the database.
func TestAnApprovalAndARejectionNeverBothTakeEffect(t *testing.T) {
	for _, mode := range []string{"documents", "database"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			ann, sam, bob := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}, Who{Name: "Bob", User: "bob"}
			var st *Store
			r := &racing{}
			if mode == "documents" {
				r.Backend = &kv.Memory{}
				st = NewStore(r, nil)
			} else {
				d := dbtest.Open(t)
				r.Backend = NewChangesPG(d)
				st = NewStore(&kv.Keyed{Keys: map[string]kv.Backend{VariablesKey: NewVariablesPG(d), ChangesKey: r, RunsKey: NewRunsPG(d), ScriptsKey: NewScriptsPG(d)}, Rest: &kv.Memory{}}, nil)
				st.SensitiveInTables(d, NewMemorySensitive(), nil)
				st.KeepRunsIn(d)
			}
			if err := st.SetVariables(ctx, "local", AllNodes, FleetVariables{Values: map[string]string{"THRESHOLD": "80"}}); err != nil {
				t.Fatal(err)
			}
			c, err := st.NewChange(ctx, VariableChange{Cluster: "local", Fleet: AllNodes, By: ann, State: ChangeWaiting, Changed: []string{"THRESHOLD"},
				Before: FleetVariables{Values: map[string]string{"THRESHOLD": "80"}}, After: FleetVariables{Values: map[string]string{"THRESHOLD": "90"}},
				Impact: []ImpactItem{{Script: "s", Version: 1, Decision: ImpactApproval, Why: "a condition reads THRESHOLD", By: "rule"}}})
			if err != nil {
				t.Fatal(err)
			}
			rejected := make(chan error, 1)
			r.on = func() {
				go func() {
					_, err := st.DecideChange(ctx, "local", c.ID, false, "not now", bob)
					rejected <- err
				}()
				// Long enough for a rejection that does not wait to be done.
				time.Sleep(300 * time.Millisecond)
			}
			r.armed.Store(true)
			_, approveErr := st.DecideChange(ctx, "local", c.ID, true, "", sam)
			var rejectErr error
			if r.armed.Load() {
				// Not armed by the approval (the database's transaction reads
				// no document): the rejection comes after it.
				r.armed.Store(false)
				_, rejectErr = st.DecideChange(ctx, "local", c.ID, false, "not now", bob)
			} else {
				rejectErr = <-rejected
			}
			if (approveErr == nil) == (rejectErr == nil) {
				t.Fatalf("approved: %v; rejected: %v", approveErr, rejectErr)
			}
			list, err := st.Changes(ctx, "local")
			if err != nil {
				t.Fatal(err)
			}
			vars, _ := st.Variables(ctx, "local")
			state, value := list[0].State, vars[AllNodes].Values["THRESHOLD"]
			if (state == ChangeApproved) != (value == "90") || (state != ChangeApproved && state != ChangeRejected) {
				t.Fatalf("change %d is %s and THRESHOLD is %s (approved: %v; rejected: %v)", c.ID, state, value, approveErr, rejectErr)
			}
			if approveErr != nil && !errors.Is(approveErr, ErrInvalid) {
				t.Fatalf("the approval that came second: %v", approveErr)
			}
		})
	}
}

// A kubectl line is allowed only when its header declares the verb on
// every resource it acts on (finding 7): each TYPE/NAME, each type of a
// list, past the value of every flag that takes one; a flag the console
// does not know must have its value joined to it.
func TestAKubernetesLineIsCheckedOnEveryResourceItNames(t *testing.T) {
	label, restart, get := "label deployments", "rollout restart deployments", "get pods"
	// allowed says whether the static check takes the line under a header
	// that declares only what is given.
	allowed := func(declared, line string) (bool, error) {
		_, fs := Check("#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:k8s " + declared + "\n" + line + "\n")
		for _, f := range fs {
			switch f.Code {
			case "k8s-undeclared":
				return false, nil
			case "k8s-line":
				return false, errors.New(f.Msg)
			}
		}
		return true, nil
	}
	for _, c := range []struct {
		h    string
		line string
		want bool
	}{
		{label, "kubectl label deploy/a namespace/prod pod-security.kubernetes.io/enforce=privileged --overwrite", false},
		{label, "kubectl label deploy/a team=web --overwrite", true},
		{label, "kubectl label deployments a b team- -n web", true},
		{restart, "kubectl rollout restart deploy/a daemonset/b", false},
		{restart, "kubectl rollout restart deployment a -n web", true},
		{get, "kubectl get -L pods configmaps -o yaml", false},
		{get, "kubectl get pods,configmaps -A", false},
		{get, "kubectl get pods -n kube-system -o wide", true},
		{get, "kubectl get pods --sort-by .metadata.name", true},
	} {
		if got, err := allowed(c.h, c.line); err != nil || got != c.want {
			t.Errorf("%q: allowed %v (%v), want %v", c.line, got, err, c.want)
		}
	}
	if _, err := allowed("get pods", "kubectl get --unknown-flag configmaps pods"); err == nil {
		t.Error("a flag the console does not know, its value apart, is taken")
	}
	// The static check says so too, and the engine refuses the line.
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:k8s label deployments\nkubectl label deploy/a namespace/prod pod-security.kubernetes.io/enforce=privileged --overwrite\n"
	_, fs := Check(text)
	undeclared := false
	for _, f := range fs {
		undeclared = undeclared || f.Code == "k8s-undeclared"
	}
	if !undeclared {
		t.Errorf("no k8s-undeclared finding: %+v", fs)
	}
	k := &stubKube{}
	e, _ := stubEngine(&stubExec{}, k, nil)
	r, err := e.Start(context.Background(), Spec{Script: "s", Version: approvedText(text), Cluster: "local", Fleet: "_cluster", Nodes: []string{"local"}, Rollout: Rollout{Mode: RolloutAll}})
	if err != nil {
		t.Fatal(err)
	}
	if done := waitRun(t, e, r.ID); done.Nodes[0].State != NodeFailed || len(k.lines) != 0 {
		t.Fatalf("the engine ran it: %+v %v", done.Nodes, k.lines)
	}
}

// In the database a document has no ConfigMap's byte limit (finding 8):
// many run requests that wait do not stop a change from being asked for
// and decided, and a cluster's variables may pass 64 KiB. In a store of
// documents, a revocation of a nearly full scripts.json drops old
// versions as a save does, rather than fail.
func TestTheByteLimitsAreTheConfigMapsAlone(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	st := pgSensitiveStore(d, NewMemorySensitive(), nil)
	targets := make([]string, 500)
	for i := range targets {
		targets[i] = fmt.Sprintf("node-%03d-%s", i, strings.Repeat("x", 40))
	}
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	for i := 0; i < 12; i++ {
		if _, err := st.NewWaitingRun(ctx, WaitingRun{Cluster: "local", Script: "s", Version: 1, Fleet: AllNodes, Targets: targets, By: ann, Rollout: Rollout{Mode: RolloutAll}}); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{}
	for i := 0; i < 100; i++ {
		values[fmt.Sprintf("V%03d", i)] = strings.Repeat("v", 900)
	}
	if err := st.SetVariables(ctx, "local", AllNodes, FleetVariables{Values: values}); err != nil {
		t.Fatalf("variables of %d KiB: %v", 100*900>>10, err)
	}
	c, err := st.NewChange(ctx, VariableChange{Cluster: "local", Fleet: AllNodes, By: ann, State: ChangeWaiting, Changed: []string{"V000"},
		Before: FleetVariables{Values: map[string]string{"V000": values["V000"]}}, After: FleetVariables{Values: map[string]string{"V000": "new"}},
		Impact: []ImpactItem{{Script: "s", Version: 1, Decision: ImpactApproval, Why: "x", By: "rule"}}})
	if err != nil {
		t.Fatalf("a change beside 12 run requests of 500 nodes: %v", err)
	}
	if got, err := st.DecideChange(ctx, "local", c.ID, true, "", sam); err != nil || got.State != ChangeApproved {
		t.Fatalf("approving it: %+v %v", got, err)
	}
	// A request is bounded on its own.
	huge := make([]string, 3000)
	for i := range huge {
		huge[i] = fmt.Sprintf("node-%04d-%s", i, strings.Repeat("y", 40))
	}
	if _, err := st.NewWaitingRun(ctx, WaitingRun{Cluster: "local", Script: "s", Version: 1, Fleet: AllNodes, Targets: huge, By: ann}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a request of %d nodes: %v", len(huge), err)
	}

	// A store of documents, its scripts.json nearly full.
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	mem := newStore(&now)
	if _, err := mem.Save(ctx, "keep", "", goodScript, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Submit(ctx, "keep", 1, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Approve(ctx, "keep", 1, sam); err != nil {
		t.Fatal(err)
	}
	var rec scriptsRecord
	if _, err := mem.load(ctx, ScriptsKey, &rec); err != nil {
		t.Fatal(err)
	}
	old := Script{Name: "old", Created: now, Updated: now, Versions: []Version{{N: 1, Text: "x", State: StateSuperseded, Created: now.Add(-time.Hour)}, {N: 2, Text: "y", State: StateDraft, Created: now}}}
	rec.Scripts["old"] = old
	b, _ := json.Marshal(rec)
	old.Versions[0].Text = strings.Repeat("x", maxScriptsBytes-len(b)-20)
	rec.Scripts["old"] = old
	b, _ = json.Marshal(rec)
	if _, err := mem.backend.SaveKey(ctx, ScriptsKey, b, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Revoke(ctx, "keep", 1, sam); err != nil {
		t.Fatalf("revoking with scripts.json at %d of %d bytes: %v", len(b), maxScriptsBytes, err)
	}
}

// A zero byte in what a script prints is kept as U+FFFD (finding 9), as
// the audit log keeps one: every save of the run goes through, and it
// ends as it ended, with each host's output.
func TestAZeroByteInOutputDoesNotStopTheRunsSaves(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	st := pgSensitiveStore(d, NewMemorySensitive(), nil)
	x := &stubExec{run: func(_, _, _ string, out io.Writer) (int, error) {
		io.WriteString(out, "/sbin/init\x00splash\x00\n")
		io.WriteString(out, `{"changed":false,"msg":"read\u0000it"}`+"\n")
		return 0, nil
	}}
	e := NewEngine(Options{Store: st, Exec: x, BeatEvery: 20 * time.Millisecond})
	r, err := e.Start(ctx, Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Fleet: AllNodes, Nodes: []string{"n1"},
		Rollout: Rollout{Mode: RolloutAll}, Values: map[string]map[string]string{"n1": {}}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, e, r.ID)
	other := NewEngine(Options{Store: pgSensitiveStore(d, NewMemorySensitive(), nil), Exec: x})
	run, out, found, err := other.Get(ctx, r.ID)
	if err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	if run.State != RunSucceeded || !strings.Contains(run.Nodes[0].Tail, "init�splash") || run.Nodes[0].Msg != "read�it" || !strings.Contains(out["n1"], "init�splash") {
		t.Fatalf("as the database keeps it: %+v %q", run, out)
	}
}

// A rebooted Kubernetes node is back only once Kubernetes reports it Ready
// from its new boot (finding 10): Ready from the boot before, which the
// node controller keeps for 40 s or so, is not it.
func TestARebootedNodeIsBackOnlyWhenReadyFromItsNewBoot(t *testing.T) {
	run := func(boot func(calls int) string) (NodeResult, int) {
		t.Helper()
		ended := make(chan Run, 1)
		calls := 0
		var mu sync.Mutex
		e := NewEngine(Options{Store: NewStore(&kv.Memory{}, nil), Exec: &fakeExec{}, OnEnd: func(r Run) { ended <- r },
			RebootPoll: time.Millisecond, RebootWait: 300 * time.Millisecond, BeatEvery: time.Hour,
			Node: func(string, string) (NodeState, bool) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				return NodeState{Ready: true, BootID: boot(calls)}, true
			}})
		if _, err := e.Start(context.Background(), Spec{Script: "s", Version: approved(PrivilegeElevated), Cluster: "local", Nodes: []string{"boot-1"}, Rollout: Rollout{Mode: RolloutAll}}); err != nil {
			t.Fatal(err)
		}
		n := wait(t, ended).Nodes[0]
		mu.Lock()
		defer mu.Unlock()
		return n, calls
	}
	// The fake node answers from boot-0, and from boot-1 once rebooted.
	if n, _ := run(func(int) string { return "boot-0" }); n.State != NodeFailed || !strings.Contains(n.Error, "Ready from its new boot") {
		t.Fatalf("Ready from the boot before only: %+v", n)
	}
	if n, calls := run(func(calls int) string {
		if calls < 20 {
			return "boot-0"
		}
		return "boot-1"
	}); n.State != NodeChanged || calls < 20 {
		t.Fatalf("Ready from the new boot after %d reads: %+v", calls, n)
	}
}
