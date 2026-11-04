package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/leader"
)

// A single replica, with no Lease, always leads: nothing changes for it.
func TestASingleReplicaAlwaysReads(t *testing.T) {
	e := leader.New(leader.Options{})
	if !e.Single() || !e.IsLeader() {
		t.Fatal("a replica without a Lease client stands alone and leads")
	}
	act := leader.NewActivity(false)
	if e.Single() {
		act.Set(true)
	}
	if !act.Active() || act.Standby() {
		t.Fatal("a single replica reads from the start")
	}
}

// Losing the Lease makes a warm standby stop its pollers and nothing
// more; a cold standby, whose watches cannot start again, exits instead
// (ADR-70).
func TestLosingTheLeaseStopsAWarmStandbyAndEndsAColdOne(t *testing.T) {
	for _, warm := range []bool{true, false} {
		act := leader.NewActivity(warm)
		var states []bool
		act.OnChange(func(active bool) { states = append(states, active) })
		exits := 0
		onLead, onStop := roleSwitch(act, func() { exits++ })
		onLead()
		if !act.Active() {
			t.Fatalf("warm=%v: winning the Lease did not make the replica active", warm)
		}
		onStop()
		if act.Active() || len(states) != 2 || states[0] != true || states[1] != false {
			t.Fatalf("warm=%v: the readers saw %v", warm, states)
		}
		if warm && exits != 0 {
			t.Fatal("a warm standby exited on losing the Lease")
		}
		if !warm && exits != 1 {
			t.Fatalf("a cold standby exited %d times on losing the Lease, want once", exits)
		}
	}
}

// A stopping console is out before the kubelet kills it: the wait for
// answers and commands, their stop and closing the audit log all fit in
// the pod's termination grace period, and a short one drops the wait.
func TestStoppingFitsTheTerminationGracePeriod(t *testing.T) {
	for _, grace := range []time.Duration{20 * time.Second, 30 * time.Second, time.Hour} {
		if got := workWithin(grace) + settleWithin + closeWithin; got >= grace {
			t.Errorf("%s of grace: stopping takes %s", grace, got)
		}
	}
	if got := workWithin(20 * time.Second); got != 14*time.Second {
		t.Errorf("the chart's 20 seconds wait %s", got)
	}
	if got := workWithin(3 * time.Second); got != 0 {
		t.Errorf("3 seconds wait %s", got)
	}
}

// persistent is a storage that outlasts a restart, as the ConfigMap does.
type persistent struct{ kv.Memory }

// The Audit log page says events are in memory exactly when they are.
func TestTheAuditStoreIsOnlyUsedOverStorageThatPersists(t *testing.T) {
	mem := audit.New(audit.Options{})
	if st := keepAudit(mem, &kv.Memory{}, slog.Default()); st != nil || mem.Stored() {
		t.Fatalf("over memory: store %v, stored %v", st, mem.Stored())
	}
	mem.Record(audit.Event{Action: "signin"})
	if got, _ := mem.Events(context.Background()); len(got) != 1 {
		t.Fatalf("the page lists %+v", got)
	}
	kept := audit.New(audit.Options{})
	st := keepAudit(kept, &persistent{}, slog.Default())
	if st == nil || !kept.Stored() {
		t.Fatalf("over a ConfigMap: store %v, stored %v", st, kept.Stored())
	}
	if err := st.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
