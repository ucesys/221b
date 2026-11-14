package ai

import (
	"context"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Every agent's instructions as a question about a cluster gets them,
// with the agents it may hand work to, and Sherlock's other parts, are
// kept in the golden directory (P-3).
func TestGoldenPrompts(t *testing.T) {
	w := setup(t, nil)
	on := true
	if err := w.ai.SaveAgent(context.Background(), Summarizer, AgentChange{AgentSettings: AgentSettings{On: &on}}); err != nil {
		t.Fatal(err)
	}
	q := sherlock.Question{Key: sherlock.Key{Cluster: "local"}}
	for _, info := range Catalog {
		a, err := w.ai.Agent(info.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		prompttest.Check(t, "agent."+info.ID, a.Instructions)
	}
	s, err := w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	_, cluster := sherlock.AgentPromptText(s, sherlock.Question{Cluster: "lab", Version: "v1.34.1"}, time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC))
	prompttest.Check(t, "agent.sherlock.cluster-block", cluster)
	prompttest.Check(t, "sherlock.core-view-changes-off", Core(Sherlock, false))
	prompttest.Check(t, "sherlock.compact", CompactCore())
	// The work Sherlock does itself where its owner cannot be asked
	// (PM-D1): all of it without the agents, as the engine's own built-in
	// has it, and one lane with only its owner off.
	prompttest.Check(t, "sherlock.own-work", OwnWork(nil))
	prompttest.Check(t, "sherlock.built-in-without-agents", sherlock.BuiltIn(true, "Watson runs kubectl get."))
	prompttest.Check(t, "skill.aiops", AIOpsSkill())
	prompttest.Check(t, "skill.setup", SetupSkill())
}
