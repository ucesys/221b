package ai

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// An agent's model and effort as set now, for AIOps › Jobs › Schedule's
// Model and Effort columns (the owner, November 23): the provider's
// default model, a model of its own, or Auto among its provider's tiers
// from its floor model up; the effort, and Auto's floor.
func TestAnAgentsSetupForTheJobsList(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	st, ok := w.ai.SetupOf(ctx, Summarizer)
	if !ok || !st.DefaultModel || st.Model == "" || st.Model == auto.Auto || st.Effort != "" || st.Provider == "" {
		t.Fatalf("out of the box: %+v %v", st, ok)
	}
	if _, ok := w.ai.SetupOf(ctx, "nobody"); ok {
		t.Fatal("an agent that is not there has a setup")
	}
	if err := w.ai.SaveProvider(ctx, Provider{ID: "gw", Name: "GW", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gw.example/v1"}, Model: "qwen3.8-27b"}, true); err != nil {
		t.Fatal(err)
	}
	offer(t, w, "gw", auto.Offered{Model: "qwen3.8-8b", Tier: auto.Light}, auto.Offered{Model: "qwen3.8-27b", Tier: auto.Standard}, auto.Offered{Model: "qwen3.8-235b", Tier: auto.Strong})
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gw", Effort: auto.Auto, FloorEffort: auto.Medium, Model: auto.Auto, FloorModel: "qwen3.8-27b"}}); err != nil {
		t.Fatal(err)
	}
	st, _ = w.ai.SetupOf(ctx, Auditor)
	want := Setup{Model: auto.Auto, AutoFrom: "qwen3.8-27b", AutoTo: "qwen3.8-235b", Provider: "GW", Effort: auto.Auto, FloorEffort: auto.Medium}
	if st != want {
		t.Fatalf("Auto from its floor:\n got %+v\nwant %+v", st, want)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gw", Effort: auto.High, Model: "qwen3.8-8b"}}); err != nil {
		t.Fatal(err)
	}
	st, _ = w.ai.SetupOf(ctx, Auditor)
	if want := (Setup{Model: "qwen3.8-8b", Provider: "GW", Effort: auto.High}); st != want {
		t.Fatalf("a model of its own:\n got %+v\nwant %+v", st, want)
	}
}
