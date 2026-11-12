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

// A host outside Kubernetes (Phase 12) is back from a reboot once it
// answers SSH from a new boot: Kubernetes, which will never call it Ready,
// is not waited for. One that never went down still fails.
func TestAHostIsBackFromARebootOnceItAnswersFromANewBoot(t *testing.T) {
	run := func(f *fakeExec) NodeResult {
		t.Helper()
		ended := make(chan Run, 1)
		e := NewEngine(Options{Store: NewStore(&kv.Memory{}, nil), Exec: f, Unreachable: func(err error) bool { return errors.Is(err, errDown) },
			OnEnd: func(r Run) { ended <- r }, RebootPoll: time.Millisecond, RebootWait: 200 * time.Millisecond, BeatEvery: time.Hour,
			Node: func(_, n string) (NodeState, bool) { return NodeState{Host: true}, true }})
		if _, err := e.Start(context.Background(), Spec{Script: "s", Version: approved(PrivilegeElevated), Cluster: "local", Nodes: []string{"boot-host"}, Rollout: Rollout{Mode: RolloutAll}}); err != nil {
			t.Fatal(err)
		}
		return wait(t, ended).Nodes[0]
	}
	f := &fakeExec{}
	if n := run(f); n.State != NodeChanged || f.reboots != 1 {
		t.Fatalf("a host that came back: %+v, %d reboots", n, f.reboots)
	}
	if n := run(&fakeExec{stuck: map[string]bool{"boot-host": true}}); n.State != NodeFailed || !strings.Contains(n.Error, "did not reboot") {
		t.Fatalf("a host that never went down: %+v", n)
	}
}

// Nothing cordons or drains a host, and hosts are never a control plane
// taken one at a time; the nodes of the same run still get their hooks.
func TestAHostHasNoHooksAndIsNoControlPlane(t *testing.T) {
	k := &stubKube{}
	x := &stubExec{}
	e := NewEngine(Options{Store: NewStore(&kv.Memory{}, time.Now), Exec: x, Kube: k,
		Node: func(_, n string) (NodeState, bool) {
			if n == "host-1" {
				// A host is never asked for a turn, whatever else is said of it.
				return NodeState{Host: true, ControlPlane: true}, true
			}
			return NodeState{Ready: true}, true
		}})
	r, err := e.Start(context.Background(), Spec{Script: "s", Version: approvedText(sshText), Cluster: "local", Nodes: []string{"host-1", "a"}, Rollout: Rollout{Mode: RolloutAll},
		Before: []Hook{DrainHook()}, After: []Hook{UncordonHook()}})
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e, r.ID)
	want := []string{"kubectl cordon a", "kubectl drain a --ignore-daemonsets --delete-emptydir-data --timeout=300s", "kubectl uncordon a"}
	if !reflect.DeepEqual(k.lines, want) || r.State != RunSucceeded {
		t.Fatalf("%v %s", k.lines, r.State)
	}
	for _, n := range r.Nodes {
		if n.Cordoned {
			t.Fatalf("%s is left cordoned", n.Node)
		}
	}
	_, outs, _, _ := e.Get(context.Background(), r.ID)
	if !strings.Contains(outs["host-1"], "host-1 is a host outside Kubernetes: the hooks around each node do not run on it") {
		t.Fatalf("the host's output: %q", outs["host-1"])
	}
	if len(x.calls) != 2 {
		t.Fatalf("the script ran %d times", len(x.calls))
	}
}

// A host's bootstrap credential is masked by the same writer as a run's
// secrets, on both streams.
func TestTheMaskIsTheRunsOwn(t *testing.T) {
	store := newTail(OutputBytes)
	m := NewMask(store, []string{"hunter2-boot"})
	m.Write([]byte("sudo read hunter2-"))
	m.Stderr().Write([]byte("sh: hunter2-boot: not found\n"))
	m.Write([]byte("boot\n"))
	m.Flush()
	if got := store.String(); strings.Contains(got, "hunter2") {
		t.Fatalf("output %q", got)
	}
}
