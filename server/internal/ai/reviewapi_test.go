package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The findings of the code review of the AI API changes (phase-13.1-ai,
// November 24, 2026).

// tieredA5000 adds the OpenAI-compatible provider the review's scenarios use:
// qwen3.8-27b by default, with a context window of 16,384 tokens, the 8b
// light and the 27b standard; its last test passed without a credential.
func tieredA5000(t *testing.T, w *world) {
	t.Helper()
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "a5000", Name: "A5000", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "http://vllm.example/v1"}, Model: "qwen3.8-27b", ContextWindow: 16384}, true); err != nil {
		t.Fatal(err)
	}
	offer(t, w, "a5000", auto.Offered{Model: "qwen3.8-8b", Tier: auto.Light}, auto.Offered{Model: "qwen3.8-27b", Tier: auto.Standard})
	if err := w.ai.RecordTest(ctx, "a5000", "qwen3.8-27b", nil); err != nil {
		t.Fatal(err)
	}
}

// Finding 1: an agent whose model is left to Auto on an OpenAI-compatible
// provider keeps the context window of the models Auto puts it on: its
// reply bound and Sherlock's compact form, on the model it starts on and
// on any model Auto moves it to.
func TestAutoKeepsTheContextWindowReview1(t *testing.T) {
	w := setup(t, nil)
	w.ai.opts.ToolDefs = func() []sherlock.ToolDef { return testDefs(t) }
	ctx := context.Background()
	tieredA5000(t, w)
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Model != "qwen3.8-27b" || a.MaxTokens != 4096 || !a.Compact {
		t.Fatalf("Sherlock on Auto: model %s, max %d, compact %v", a.Model, a.MaxTokens, a.Compact)
	}
	if fp := agentView(t, w, Sherlock).Footprint; fp == nil || fp.Window != 16384 || fp.WindowFrom != "provider" || fp.MaxTokens != 4096 {
		t.Fatalf("the footprint: %+v", fp)
	}
	// A job's run Auto moves to the light model keeps the bound.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	b, err := w.ai.Agent(Auditor, sherlock.Question{})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := w.ai.PickRun(ctx, &b, auto.RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1}, "", false); b.Model != p.Model || b.MaxTokens != 4096 {
		t.Fatalf("a run on %s: max %d", b.Model, b.MaxTokens)
	}
}

// Finding 2: an agent on Auto whose provider loses its last tier keeps
// Auto and can still be saved, turned off or its caps lowered; the
// provider's save that took the tiers stays allowed. Auto is still
// refused for an agent that was not on Auto there.
func TestAutoWithNoTierLeftCanBeSavedReview2(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	tieredA5000(t, w)
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	offer(t, w, "a5000", auto.Offered{Model: "qwen3.8-8b"}, auto.Offered{Model: "qwen3.8-27b"})
	if v := agentView(t, w, Auditor); !v.AutoNoTier || v.Model != auto.Auto {
		t.Fatalf("the view: no tier %v, model %s", v.AutoNoTier, v.Model)
	}
	off := false
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: auto.Auto, On: &off, DailyUSD: 1}}); err != nil {
		t.Fatalf("turning it off: %v", err)
	}
	if v := agentView(t, w, Auditor); v.Enabled || v.Settings.Model != auto.Auto || v.Settings.DailyUSD != 1 {
		t.Fatalf("after: %+v", v.Settings)
	}
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: auto.Auto}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Auto newly set with no tier: %v", err)
	}
}
