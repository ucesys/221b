package automation

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// stubExec is a node: it remembers commands and answers from a function.
type stubExec struct {
	mu    sync.Mutex
	calls []string
	stdin []string
	run   func(node, command, stdin string, out io.Writer) (int, error)
}

func (f *stubExec) Exec(_ context.Context, _, node, _, command string, _ bool, stdin io.Reader, out io.Writer) (int, error) {
	b, _ := io.ReadAll(stdin)
	f.mu.Lock()
	f.calls = append(f.calls, node+": "+command)
	f.stdin = append(f.stdin, string(b))
	f.mu.Unlock()
	if f.run != nil {
		return f.run(node, command, string(b), out)
	}
	return 0, nil
}

// stubKube records kubectl lines.
type stubKube struct {
	mu    sync.Mutex
	lines []string
	fail  string
}

func (f *stubKube) RunLine(_ context.Context, _ string, _ Person, identity, line string, dry bool, out io.Writer) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tag := line
	if dry {
		tag += " [dry]"
	}
	if identity != "" {
		tag += " [as " + identity + "]"
	}
	f.lines = append(f.lines, tag)
	if f.fail != "" && strings.Contains(line, f.fail) {
		io.WriteString(out, "refused\n")
		return 1, nil
	}
	io.WriteString(out, "done\n")
	return 0, nil
}

func waitRun(t *testing.T, e *Engine, id int) Run {
	t.Helper()
	for i := 0; i < 500; i++ {
		if !e.Running(id) {
			r, _, _, err := e.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the run did not end")
	return Run{}
}

func stubEngine(x Executor, k KubeRunner, on func(Run, string, string, string)) (*Engine, *Store) {
	st := NewStore(&kv.Memory{}, time.Now)
	return NewEngine(Options{Store: st, Exec: x, Kube: k, OnCommand: on}), st
}

const sshText = "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho hi\n"

func approvedText(text string) Version {
	h, fs := Check(text)
	return Version{N: 3, Text: text, Hash: Hash(text), Header: h, Findings: fs, State: StateApproved}
}

func TestFactsAreCollectedKeptAndOnlyKnownKeysStay(t *testing.T) {
	x := &stubExec{run: func(node, _, _ string, out io.Writer) (int, error) {
		if node == "b" {
			return 1, nil
		}
		io.WriteString(out, "os.family=debian\ncpu.cores=8\nevil.key=x\nnot a line\n")
		return 0, nil
	}}
	e, st := stubEngine(x, nil, nil)
	if _, err := e.CollectFacts(context.Background(), "local", []string{"a", "b"}, Who{Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	list, err := st.Facts(context.Background(), "local")
	if err != nil || len(list) != 2 {
		t.Fatalf("%v, %v", list, err)
	}
	if !reflect.DeepEqual(list[0].Facts, map[string]string{"os.family": "debian", "cpu.cores": "8"}) {
		t.Fatalf("%+v", list[0])
	}
	if list[1].Error == "" {
		t.Fatalf("b failed and says nothing: %+v", list[1])
	}
	if other, _ := st.Facts(context.Background(), "other"); len(other) != 0 {
		t.Fatalf("another cluster sees %+v", other)
	}
}

func TestAScriptThatDeclaresFactsGetsThemFresh(t *testing.T) {
	x := &stubExec{run: func(_, command, stdin string, out io.Writer) (int, error) {
		if stdin == FactsScript {
			io.WriteString(out, "os.family=redhat\n")
		}
		return 0, nil
	}}
	e, _ := stubEngine(x, nil, nil)
	v := approvedText("#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:facts os.family\necho $FACT_OS_FAMILY\n")
	r, err := e.Start(context.Background(), Spec{Script: "s", Version: v, Cluster: "local", Nodes: []string{"a"}, Rollout: Rollout{Mode: RolloutAll}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, e, r.ID)
	if len(x.calls) != 2 || !strings.Contains(x.calls[1], "FACT_OS_FAMILY='redhat'") {
		t.Fatalf("%v", x.calls)
	}
}

func TestEveryCommandIsAuditedInWatsonsWords(t *testing.T) {
	var mu sync.Mutex
	var got []string
	e, _ := stubEngine(&stubExec{}, nil, func(r Run, node, what, outcome string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r.Watson()+"|"+node+"|"+what+"|"+outcome)
	})
	r, _ := e.Start(context.Background(), Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Nodes: []string{"a"}, Rollout: Rollout{Mode: RolloutAll}})
	r = waitRun(t, e, r.ID)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "Watson via s v3, run #") || !strings.Contains(got[0], "|a|script sha256") {
		t.Fatalf("%v", got)
	}
	_, outs, _, _ := e.Get(context.Background(), r.ID)
	if !strings.Contains(outs["a"], "Watson via s v3, run #") {
		t.Fatalf("the output does not open with Watson's line: %q", outs["a"])
	}
}

func TestSecretsGoInOnStandardInputAndNeverIntoAnythingKept(t *testing.T) {
	const secret = "hunter2-value"
	x := &stubExec{run: func(_, _, stdin string, out io.Writer) (int, error) {
		io.WriteString(out, "token is "+secret+" here\n")
		return 0, nil
	}}
	e, st := stubEngine(x, nil, nil)
	v := approvedText("#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:param DB_PASS secret the password\necho ok\n")
	r, err := e.Start(context.Background(), Spec{Script: "s", Version: v, Cluster: "local", Nodes: []string{"a"}, Rollout: Rollout{Mode: RolloutAll},
		Values:  map[string]map[string]string{"a": {"DB_PASS": "secret:ns/db/password"}},
		Secrets: map[string]map[string]string{"a": {"DB_PASS": secret}}})
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e, r.ID)
	if strings.Contains(x.calls[0], secret) {
		t.Fatalf("the value is on the command line: %s", x.calls[0])
	}
	if !strings.Contains(x.stdin[0], "DB_PASS='"+secret+"'") {
		t.Fatalf("stdin: %q", x.stdin[0])
	}
	_, outs, _, _ := e.Get(context.Background(), r.ID)
	if strings.Contains(outs["a"], secret) || !strings.Contains(outs["a"], "token is •••••• here") {
		t.Fatalf("output: %q", outs["a"])
	}
	runs, _ := st.Runs(context.Background())
	if strings.Contains(strings.Join([]string{runs[0].Nodes[0].Tail, runs[0].Nodes[0].Values["DB_PASS"]}, " "), secret) {
		t.Fatalf("the record holds it: %+v", runs[0].Nodes[0])
	}
}

func TestAMaskCatchesAValueSplitAcrossWrites(t *testing.T) {
	var b strings.Builder
	m := newMask(&b, []string{"abcdef"})
	io.WriteString(m, "x abc")
	io.WriteString(m, "def y")
	m.Close()
	if b.String() != "x •••••• y" {
		t.Fatalf("%q", b.String())
	}
}

const kubeText = `#!/bin/sh
# 221b:describe Restart the frontend
# 221b:target kubernetes
# 221b:privilege none
# 221b:k8s rollout restart deployments
# 221b:k8s get pods
# 221b:runs-as ops/deployer
kubectl -n $NS rollout restart deploy/frontend
kubectl -n $NS get pods
`

func TestAKubernetesScriptRunsItsLinesAndOnlyWhatItDeclares(t *testing.T) {
	v := approvedText(kubeText)
	if HasErrors(v.Findings) {
		t.Fatalf("%+v", v.Findings)
	}
	if v.Header.RunsAs != "ops/deployer" || v.Header.Target != TargetKubernetes {
		t.Fatalf("%+v", v.Header)
	}
	k := &stubKube{}
	e, _ := stubEngine(&stubExec{}, k, nil)
	r, err := e.Start(context.Background(), Spec{Script: "s", Version: v, Cluster: "local", Nodes: []string{"local"}, Identity: "ops/deployer", Rollout: Rollout{Mode: RolloutAll},
		Values: map[string]map[string]string{"local": {"NS": "web"}}})
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e, r.ID)
	want := []string{"kubectl -n web rollout restart deploy/frontend [as ops/deployer]", "kubectl -n web get pods [as ops/deployer]"}
	if !reflect.DeepEqual(k.lines, want) {
		t.Fatalf("%v", k.lines)
	}
	if r.State != RunSucceeded {
		t.Fatalf("%+v", r)
	}
	bad := v
	bad.Text = strings.Replace(kubeText, "$NS get pods\n", "$NS delete pods --all\n", 1)
	k.lines = nil
	r, _ = e.Start(context.Background(), Spec{Script: "s", Version: bad, Cluster: "local", Nodes: []string{"local"}, Rollout: Rollout{Mode: RolloutAll},
		Values: map[string]map[string]string{"local": {"NS": "web"}}})
	r = waitRun(t, e, r.ID)
	if r.State == RunSucceeded || len(k.lines) != 1 || strings.Contains(strings.Join(k.lines, ""), "delete") {
		t.Fatalf("an undeclared line ran: %v, %s", k.lines, r.State)
	}
}

func TestAKubernetesScriptIsCheckedBeforeItIsApproved(t *testing.T) {
	for text, code := range map[string]string{
		strings.Replace(kubeText, "$NS get pods\n", "$NS get pods | grep x\n", 1):    "k8s-shell",
		strings.Replace(kubeText, "$NS get pods\n", "$NS delete pods --all\n", 1):    "k8s-undeclared",
		strings.Replace(kubeText, "kubectl -n $NS get pods\n", "rm -rf /tmp/x\n", 1): "k8s-line",
	} {
		_, fs := Check(text)
		found := false
		for _, f := range fs {
			found = found || f.Code == code
		}
		if !found {
			t.Errorf("%s not found in %+v", code, fs)
		}
	}
}

func TestAValueCannotSplitAKubectlLine(t *testing.T) {
	if _, err := ExpandKube("kubectl get pods -n $NS", map[string]string{"NS": "a --all-namespaces"}); err == nil {
		t.Fatal("a value with a space went into the line")
	}
	if _, err := ExpandKube("kubectl get pods -n $NS", map[string]string{"NS": "--all-namespaces"}); err == nil {
		t.Fatal("a value went in as a flag")
	}
	if _, err := ExpandKube("kubectl get pods -n $GONE", nil); err == nil {
		t.Fatal("a variable with no value went through")
	}
	if got, err := ExpandKube("kubectl get pods -n ${NS}", map[string]string{"NS": "web"}); err != nil || got != "kubectl get pods -n web" {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestHooksRunAroundEachNodeAndAFailureLeavesItCordoned(t *testing.T) {
	k := &stubKube{}
	e, _ := stubEngine(&stubExec{}, k, nil)
	sp := Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Nodes: []string{"a"}, Rollout: Rollout{Mode: RolloutAll},
		Before: []Hook{DrainHook()}, After: []Hook{UncordonHook()}}
	r, _ := e.Start(context.Background(), sp)
	r = waitRun(t, e, r.ID)
	want := []string{"kubectl cordon a", "kubectl drain a --ignore-daemonsets --delete-emptydir-data --timeout=300s", "kubectl uncordon a"}
	if !reflect.DeepEqual(k.lines, want) || r.State != RunSucceeded {
		t.Fatalf("%v %s", k.lines, r.State)
	}
	if !reflect.DeepEqual(r.Hooks, []string{"before: cordon and drain", "after: uncordon"}) {
		t.Fatalf("%v", r.Hooks)
	}

	// The script fails: the node stays cordoned.
	k.lines = nil
	bad := &stubExec{run: func(string, string, string, io.Writer) (int, error) { return 3, nil }}
	e2, _ := stubEngine(bad, k, nil)
	r, _ = e2.Start(context.Background(), sp)
	r = waitRun(t, e2, r.ID)
	if len(k.lines) != 2 || r.State != RunFailed || !strings.Contains(r.Nodes[0].Error, "stays cordoned") {
		t.Fatalf("%v %+v", k.lines, r.Nodes[0])
	}

	// A drain that fails skips the script.
	k.lines, k.fail = nil, "drain"
	x := &stubExec{}
	e3, _ := stubEngine(x, k, nil)
	r, _ = e3.Start(context.Background(), sp)
	r = waitRun(t, e3, r.ID)
	if len(x.calls) != 0 || r.State != RunFailed {
		t.Fatalf("%v %s", x.calls, r.State)
	}
}

func TestARunIsRefusedAConfigurationThatGivesTooLittleOrTooMuch(t *testing.T) {
	list := append(builtinProfiles(), RunProfile{ID: "web-root", Name: "web-root", Kind: ProfileSSH, Privilege: PrivilegeElevated, Fleets: []string{"web"}})
	full, none := Header{Privilege: PrivilegeElevated}, Header{Privilege: PrivilegeNobody}
	elevated := FleetVariables{Ceiling: PrivilegeElevated}
	if c, err := ForRun(list, "", full, false, "web", elevated); err != nil || c.ID != ProfileSSHPaired {
		t.Fatalf("%+v, %v", c, err)
	}
	if c, err := ForRun(list, "", none, false, "web", elevated); err != nil || c.ID != ProfileNobody {
		t.Fatalf("%+v, %v", c, err)
	}
	if c, err := ForRun(list, ProfileSSHPaired, full, true, "web", elevated); err != nil || c.ID != ProfileNobody {
		t.Fatalf("a dry run must be read-only: %+v, %v", c, err)
	}
	if _, err := ForRun(list, ProfileNobody, full, false, "web", elevated); err == nil {
		t.Fatal("read-only ran a full script")
	}
	if _, err := ForRun(list, "", full, false, "web", FleetVariables{Ceiling: PrivilegeNobody}); err == nil {
		t.Fatal("full ran on a fleet that caps at none")
	}
	if _, err := ForRun(list, "", full, false, "web", FleetVariables{}); err == nil {
		t.Fatal("full ran on a fleet with no ceiling, which is nobody")
	}
	if _, err := ForRun(list, "web-root", full, false, "db", elevated); err == nil {
		t.Fatal("a configuration reached a fleet it does not name")
	}
	if _, err := ForRun(list, ProfileLoginUser, full, false, "web", elevated); err == nil {
		t.Fatal("a Kubernetes configuration ran a node script")
	}
}

// A run profile limited to some fleets reaches those alone: a run on every
// node, or a script on the whole cluster, needs one that reaches them all.
func TestAProfileLimitedToFleetsDoesNotReachTheWholeCluster(t *testing.T) {
	list := append(builtinProfiles(),
		RunProfile{ID: "web-root", Name: "web-root", Kind: ProfileSSH, Privilege: PrivilegeElevated, Fleets: []string{"web"}},
		RunProfile{ID: "web-sa", Name: "web-sa", Kind: ProfileKubernetes, Namespace: "ops", ServiceAccount: "deployer", Fleets: []string{"web"}},
		RunProfile{ID: "any-sa", Name: "any-sa", Kind: ProfileKubernetes, Namespace: "ops", ServiceAccount: "deployer"})
	full, kube := Header{Privilege: PrivilegeElevated}, Header{Privilege: PrivilegeNobody, Target: TargetKubernetes}
	elevated := FleetVariables{Ceiling: PrivilegeElevated}
	if _, err := ForRun(list, "web-root", full, false, "_all", elevated); err == nil || !strings.Contains(err.Error(), "all nodes") {
		t.Fatalf("a profile limited to web ran on all nodes: %v", err)
	}
	if _, err := ForRun(list, "web-sa", kube, false, "_cluster", FleetVariables{}); err == nil || !strings.Contains(err.Error(), "whole cluster") {
		t.Fatalf("a Kubernetes profile limited to web ran on the cluster: %v", err)
	}
	if c, err := ForRun(list, "any-sa", kube, false, "_cluster", FleetVariables{}); err != nil || c.ID != "any-sa" {
		t.Fatalf("%+v, %v", c, err)
	}
	if c, err := ForRun(list, "", kube, false, "_cluster", FleetVariables{}); err != nil || c.ID != ProfileLoginUser {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestAChangeThatMattersWaitsAndSherlockOnlyAddsToTheList(t *testing.T) {
	cond := approvedText("#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:param MODE required m\nif [ \"$MODE\" = a ]; then echo a; fi\n")
	pass := approvedText("#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:param MODE required m\necho \"$MODE\"\n")
	other := approvedText("#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho other\n")
	scripts := []Script{{Name: "cond", Versions: []Version{cond}}, {Name: "pass", Versions: []Version{pass}}, {Name: "other", Versions: []Version{other}}}
	items := Impact(scripts, FleetVariables{}, []string{"MODE"})
	if len(items) != 2 || items[0].Decision != ImpactApproval || items[1].Decision != ImpactSafe {
		t.Fatalf("%+v", items)
	}
	if got := Impact(scripts, FleetVariables{Sensitive: []string{"MODE"}}, []string{"MODE"}); got[1].Decision != ImpactApproval || !strings.Contains(got[1].Why, "sensitive") {
		t.Fatalf("%+v", got)
	}
	raised := RaiseImpact(items, map[string]string{"pass v3": "it matters", "cond v3": "never mind"})
	if raised[1].Decision != ImpactApproval || raised[1].By != "sherlock" {
		t.Fatalf("%+v", raised)
	}
	if raised[0].Decision != ImpactApproval || raised[0].By != "rule" {
		t.Fatalf("Sherlock changed what the rule said: %+v", raised[0])
	}

	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	c, err := st.NewChange(ctx, VariableChange{Cluster: "prod", Fleet: "web", By: Who{Name: "Ann"}, State: ChangeWaiting, Impact: items})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.Changes(ctx, "prod")
	if Suspended(list, "web", "cond", 3) != c.ID || Suspended(list, "web", "pass", 3) != 0 || Suspended(list, "db", "cond", 3) != 0 {
		t.Fatalf("%+v", list)
	}
	if _, err := st.DecideChange(ctx, "prod", c.ID, true, "", Who{Name: "Ann"}); err == nil {
		t.Fatal("the person who asked approved it")
	}
	if _, err := st.DecideChange(ctx, "prod", c.ID, true, "", Who{Name: "Sam"}); err != nil {
		t.Fatal(err)
	}
	list, _ = st.Changes(ctx, "prod")
	if Suspended(list, "web", "cond", 3) != 0 {
		t.Fatal("still suspended after the approval")
	}
	if got := ChangedVariables(FleetVariables{Values: map[string]string{"A": "1", "B": "2"}}, FleetVariables{Values: map[string]string{"A": "9", "C": "3"}, Nodes: map[string]map[string]string{"n": {"D": "4"}}}); !reflect.DeepEqual(got, []string{"A", "B", "C", "D"}) {
		t.Fatalf("%v", got)
	}
}
