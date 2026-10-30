package main

import (
	"testing"

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
