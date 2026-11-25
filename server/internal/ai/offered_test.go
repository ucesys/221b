package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The models a provider offers its agents, and Auto by their tiers
// (phase-13.1-ai, A9).

// offer has a SuperAdmin choose the models a provider offers.
func offer(t *testing.T, w *world, id string, list ...auto.Offered) {
	t.Helper()
	ctx := context.Background()
	p, ok := w.ai.ProviderOf(ctx, id)
	if !ok {
		t.Fatalf("no provider %s", id)
	}
	p.Offered, p.OfferedChosen = list, true
	if err := w.ai.SaveProvider(ctx, p, false); err != nil {
		t.Fatal(err)
	}
}

func agentView(t *testing.T, w *world, id string) AgentView {
	t.Helper()
	for _, a := range w.ai.View(context.Background()).Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent %s in the view", id)
	return AgentView{}
}

// An upgrade keeps what was: a provider whose models no SuperAdmin chose
// offers every model it lists; Auto picks on one of kind Anthropic among
// Claude Haiku, Sonnet and Opus 5.5, light, standard and strong, and on an
// OpenAI-compatible one among none, so its model may not be Auto.
func TestAnUpgradeOffersWhatItDidA9(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "gw", Name: "GW", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gw.example/v1"}, Model: "qwen"}, true); err != nil {
		t.Fatal(err)
	}
	def := providerView(t, w.ai, DefaultProvider)
	if !def.OffersAll || len(def.Offers) != 3 || def.Offers[0] != (auto.Offered{Model: auto.Haiku, Tier: auto.Light}) || def.Offers[2] != (auto.Offered{Model: auto.Opus, Tier: auto.Strong}) {
		t.Fatalf("the default provider offers %+v (all %v)", def.Offers, def.OffersAll)
	}
	if gw := providerView(t, w.ai, "gw"); !gw.OffersAll || len(gw.Offers) != 0 {
		t.Fatalf("the OpenAI-compatible provider offers %+v (all %v)", gw.Offers, gw.OffersAll)
	}
	// Any model it lists may be set, and Auto picks among the three.
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{Model: "claude-fable-5-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	a, _ := w.ai.Agent(Auditor, sherlock.Question{})
	if p, _ := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 0}, "", false); p.Model != auto.Opus {
		t.Fatalf("a P0 case on the default provider: %+v", p)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gw", Model: auto.Auto}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "none has a tier") {
		t.Fatalf("Auto's model on a provider with no tier: %v", err)
	}
}

// Auto picks by tier on any kind of provider once its offered models have
// tiers: an OpenAI-compatible one here, whose agents start on its standard
// model and whose runs take its strong model for a P0 case and its light
// one for the daily digest.
func TestAutoOnAnOpenAICompatibleProviderWithTiersA9(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "gw", Name: "GW", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gw.example/v1"}, Model: "qwen3.8-27b"}, true); err != nil {
		t.Fatal(err)
	}
	// It answered its test without a key: usable.
	if err := w.ai.RecordTest(ctx, "gw", "qwen3.8-27b", nil); err != nil {
		t.Fatal(err)
	}
	offer(t, w, "gw", auto.Offered{Model: "qwen3.8-8b", Tier: auto.Light}, auto.Offered{Model: "qwen3.8-27b", Tier: auto.Standard}, auto.Offered{Model: "qwen3.8-235b", Tier: auto.Strong})
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gw", Effort: auto.Auto, Model: auto.Auto, FloorModel: "qwen3.8-8b"}}); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Auditor, sherlock.Question{})
	if err != nil || a.Model != "qwen3.8-27b" || a.Auto == nil {
		t.Fatalf("resolved: %s %v", a.Model, err)
	}
	if p, _ := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 0}, "", false); p.Model != "qwen3.8-235b" || a.Model != "qwen3.8-235b" {
		t.Fatalf("a P0 case: %+v", p)
	}
	a, _ = w.ai.Agent(Auditor, sherlock.Question{})
	p, _ := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1}, "", false)
	if p.Model != "qwen3.8-8b" {
		t.Fatalf("the daily digest: %+v", p)
	}
	// A later step that wants the strong tier moves the run up to it.
	a, _ = w.ai.Agent(Auditor, sherlock.Question{})
	if p, moved := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 1}, "qwen3.8-8b", false); p.Model != "qwen3.8-235b" || moved != "qwen3.8-8b" {
		t.Fatalf("moved up: %+v %q", p, moved)
	}
	if v := agentView(t, w, Auditor); v.AutoNoTier || v.NotOffered || v.Model != auto.Auto {
		t.Fatalf("the auditor's view: %+v", v)
	}
	// Its tiers taken away, Auto's model runs on the provider's default and says so.
	offer(t, w, "gw", auto.Offered{Model: "qwen3.8-8b"}, auto.Offered{Model: "qwen3.8-27b"})
	if v := agentView(t, w, Auditor); !v.AutoNoTier {
		t.Fatalf("no tier left: %+v", v)
	}
	if a, _ := w.ai.Agent(Auditor, sherlock.Question{}); a.Model != "qwen3.8-27b" {
		t.Fatalf("with no tier, on %s", a.Model)
	}
}

// A model released after this console is used once a SuperAdmin offers it
// with a tier: no release needed.
func TestANewModelIsUsedOnceTieredA9(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	offer(t, w, DefaultProvider, auto.Offered{Model: "claude-fable-6", Tier: auto.Strong}, auto.Offered{Model: auto.Opus, Tier: auto.Strong},
		auto.Offered{Model: auto.Sonnet, Tier: auto.Standard}, auto.Offered{Model: auto.Haiku, Tier: auto.Light})
	a, _ := w.ai.Agent(Auditor, sherlock.Question{})
	if p, _ := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 0}, "", false); p.Model != "claude-fable-6" || a.Model != "claude-fable-6" {
		t.Fatalf("a P0 case: %+v", p)
	}
	if v := providerView(t, w.ai, DefaultProvider); v.OffersAll || len(v.Offers) != 4 || !v.OfferedChosen {
		t.Fatalf("the default provider's view: %+v", v)
	}
	// Kept: read again, the same.
	w.ai.forget()
	if v := providerView(t, w.ai, DefaultProvider); len(v.Offered) != 4 || v.Offered[0].Model != "claude-fable-6" {
		t.Fatalf("read again: %+v", v.Offered)
	}
}

// A model taken off the list: an agent set to it keeps answering on it,
// its drawer says it is no longer offered, its other settings may still
// be saved; another model not offered, or a floor without a tier, may not
// be set. The default model is always offered.
func TestAModelRemovedFromTheOfferedListA9(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{Model: auto.Haiku}}); err != nil {
		t.Fatal(err)
	}
	offer(t, w, DefaultProvider, auto.Offered{Model: auto.Sonnet, Tier: auto.Standard})
	v := agentView(t, w, Troubleshooter)
	if !v.NotOffered || v.Model != auto.Haiku {
		t.Fatalf("the troubleshooter's view: %+v", v)
	}
	if a, err := w.ai.Agent(Troubleshooter, sherlock.Question{}); err != nil || a.Model != auto.Haiku {
		t.Fatalf("keeps answering on its model: %s %v", a.Model, err)
	}
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{Model: auto.Haiku, DailyUSD: 3}}); err != nil {
		t.Fatalf("its other settings: %v", err)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Model: auto.Haiku}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "does not offer claude-haiku-5-5") {
		t.Fatalf("a model not offered: %v", err)
	}
	// The default model (Opus) is offered though not in the list.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Model: auto.Opus}}); err != nil {
		t.Fatalf("the default model: %v", err)
	}
	if p := providerView(t, w.ai, DefaultProvider); len(p.Offers) != 2 || p.Offers[1] != (auto.Offered{Model: auto.Opus}) {
		t.Fatalf("offers %+v", p.Offers)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: auto.Auto, FloorModel: auto.Opus}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a floor without a tier: %v", err)
	}
	// Auto picks the one tiered model for every tier.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	a, _ := w.ai.Agent(Auditor, sherlock.Question{})
	if p, _ := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 0}, "", false); p.Model != auto.Sonnet {
		t.Fatalf("a P0 case with one tiered model: %+v", p)
	}
	// What is offered is checked: a tier that is none of the three, a model twice.
	p, _ := w.ai.ProviderOf(ctx, DefaultProvider)
	for _, bad := range [][]auto.Offered{{{Model: "x", Tier: "heavy"}}, {{Model: "x"}, {Model: "x"}}, {{Model: "not a model"}}} {
		p.Offered, p.OfferedChosen = bad, true
		if err := w.ai.SaveProvider(ctx, p, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}
