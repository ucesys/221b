package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

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

// Finding 5: a test run on model auto, or on a model the agent's provider
// does not offer, is refused before it starts, its estimate too; an
// offered model, and the agent's own, are tested on.
func TestATestOnAutoOrAModelNotOfferedIsRefusedReview5(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	tieredA5000(t, w)
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: "qwen3.8-27b"}}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{auto.Auto, "qwen3.8-235b"} {
		o := TestOptions{Model: model}
		if _, _, _, err := w.ai.Estimate(ctx, Troubleshooter, o); !errors.Is(err, ErrInvalid) {
			t.Fatalf("the estimate on %s: %v", model, err)
		}
		if _, err := w.ai.RunTests(ctx, Troubleshooter, o); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a run on %s: %v", model, err)
		}
	}
	for _, model := range []string{"qwen3.8-8b", "qwen3.8-27b"} {
		if _, _, _, err := w.ai.Estimate(ctx, Troubleshooter, TestOptions{Model: model}); err != nil {
			t.Fatalf("the estimate on %s: %v", model, err)
		}
	}
}

// Finding 4: a test run on another model is sent with that model's reply
// bound and, for Sherlock, the form that fits its context: Sherlock on
// the A5000's long model (131,072 tokens) answers whole and unbounded;
// tested on the 27b (16,384), each case asks for 4,096 tokens in the
// compact form, as Sherlock set to the 27b would.
func TestATestOnAnotherModelFollowsItsContextReview4(t *testing.T) {
	w := setup(t, nil)
	w.ai.opts.ToolDefs = func() []sherlock.ToolDef { return testDefs(t) }
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "a5000", Name: "A5000", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: w.ai.opts.DefaultURL + "/open/v1"}, Model: "qwen3.8-27b", ContextWindow: 16384}, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.RecordTest(ctx, "a5000", "qwen3.8-27b", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: "qwen3.8-27b-long", ContextWindow: 131072}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); a.Compact || a.MaxTokens != 0 {
		t.Fatalf("on its own model: compact %v, max %d", a.Compact, a.MaxTokens)
	}
	w.fake.Reset()
	run, err := w.ai.RunTests(ctx, Sherlock, TestOptions{By: "ann", Model: "qwen3.8-27b"})
	if err != nil || run.Cases == 0 {
		t.Fatalf("%+v %v", run, err)
	}
	tested := 0
	for _, raw := range w.fake.Requests() {
		var r struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &r)
		if r.Model != "qwen3.8-27b" {
			continue
		}
		tested++
		if r.MaxTokens != 4096 || len(r.Messages) == 0 || !strings.HasPrefix(r.Messages[0].Content, CompactCore()) {
			t.Fatalf("a case on the 27b: max %d, compact %v", r.MaxTokens, len(r.Messages) > 0 && strings.HasPrefix(r.Messages[0].Content, CompactCore()))
		}
	}
	if tested == 0 {
		t.Fatal("no case ran on the 27b")
	}
}

// Finding 13: the estimate of a run on another model prices the grading
// of a case at the agent's own model, which grades it: one graded case
// on Haiku, the Troubleshooter on Opus, adds Opus's price of a grading.
func TestTheEstimatePricesGradingOnTheAgentsModelReview13(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	o := TestOptions{Model: auto.Haiku}
	estimate := func(points []string) float64 {
		t.Helper()
		c := crashCase("graded?", "")
		c.Expect.Points = points
		saved, err := w.ai.SaveTest(ctx, c, "ann")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.ai.DeleteTest(ctx, saved.ID) }()
		usd, _, _, err := w.ai.Estimate(ctx, Troubleshooter, o)
		if err != nil {
			t.Fatal(err)
		}
		return usd
	}
	graded, plain := estimate([]string{"CrashLoopBackOff"}), estimate(nil)
	opus, _ := sherlock.ModelOf("claude-opus-5-5")
	want := opus.Cost(sherlock.Usage{InputTokens: 1500, OutputTokens: 100})
	if got := graded - plain; math.Abs(got-want) > 1e-9 {
		t.Fatalf("a grading priced at $%.6f, want Opus's $%.6f", got, want)
	}
}

// Finding 3: a run at an effort or on a model of its own says so where
// the test dialog reads it, as the latest run, while the draft keeps its
// own test; a run of the agent's own settings says no effort or model.
func TestAMeasuringRunSaysWhatItRanAtReview3(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Be brief.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	own, err := w.ai.RunTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann"})
	if err != nil || own.Model != "" || own.Effort != "" {
		t.Fatalf("the draft's own test: effort %q model %q, %v", own.Effort, own.Model, err)
	}
	if _, err := w.ai.RunTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann", Model: auto.Haiku}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := w.ai.TestProgress(ctx, Troubleshooter)
	if err != nil || !ok || got.Model != auto.Haiku || got.Effort != "" {
		t.Fatalf("the latest run: effort %q model %q, %v %v", got.Effort, got.Model, ok, err)
	}
	if v, _ := w.ai.Instructions(ctx, Troubleshooter); v.Draft.Test == nil || v.Draft.Test.At.Sub(own.At).Abs() > time.Millisecond || v.Draft.Test.Model != "" {
		t.Fatalf("the draft's test: %+v", v.Draft.Test)
	}
	if _, err := w.ai.RunTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann", Effort: auto.Low}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := w.ai.TestProgress(ctx, Troubleshooter); got.Effort != auto.Low || got.Model != "claude-opus-5-5" {
		t.Fatalf("at an effort alone: effort %q model %q", got.Effort, got.Model)
	}
}
