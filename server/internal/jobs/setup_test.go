package jobs

import (
	"context"
	"testing"
)

// describingAgents say each agent's model and effort as set now.
type describingAgents struct {
	*fakeAgents
	setups map[string]AgentSetup
}

func (d *describingAgents) SetupOf(_ context.Context, agent string) (AgentSetup, bool) {
	st, ok := d.setups[agent]
	return st, ok
}

// The jobs' list carries each job's agent's model and effort as set now,
// for the Schedule table's Model and Effort columns, so a person who may
// see the jobs but not Administration › AI sees them; a run's row carries
// why Auto picked its effort and where its model moved up, for the Runs
// table's (the owner, November 23).
func TestTheJobsListSaysEachAgentsModelAndEffort(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	auditor := AgentSetup{Model: "auto", AutoFrom: "claude-sonnet-5-5", AutoTo: "claude-opus-5-5", Provider: "Anthropic", Effort: "auto", FloorEffort: "medium"}
	w.s.SetAgents(&describingAgents{fakeAgents: w.agents, setups: map[string]AgentSetup{"auditor": auditor}})
	list, err := w.s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, v := range list {
		switch v.ID {
		case "test", KindAudit:
			if v.Setup == nil || *v.Setup != auditor {
				t.Fatalf("%s: %+v", v.ID, v.Setup)
			}
			seen++
		case KindDigest:
			if v.Setup != nil {
				t.Fatalf("an agent it does not describe: %+v", v.Setup)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("jobs on the Auditor: %d", seen)
	}
	b := brief(Run{ID: "r", Model: "claude-opus-5-5", Effort: "high", PickBy: "rules", PickWhy: "a P0 case", MovedUp: &MovedUp{Step: 2, From: "claude-sonnet-5-5"}})
	if b.PickWhy != "a P0 case" || b.MovedUp == nil || b.MovedUp.From != "claude-sonnet-5-5" {
		t.Fatalf("the run's row: %+v", b)
	}
}
