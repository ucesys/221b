package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// KI-2: any provider may be removed once no agent uses it, on or off; the
// default once another is the default; Make default moves the agents that
// name no provider; the chart's lock keeps the default where it is.
func TestMakeDefaultAndRemove(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	gw := Provider{ID: "gateway", Name: "Gateway", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gateway.example/v1"}, Model: "gpt-5"}
	if err := w.ai.SaveProvider(ctx, gw, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.KeysFor("gateway").Set(ctx, "gw-token-0123456789", "test"); err != nil {
		t.Fatal(err)
	}

	// In use: every agent is named, those that are off too, in words.
	err := w.ai.DeleteProvider(ctx, DefaultProvider)
	if !errors.Is(err, ErrInvalid) || !strings.HasPrefix(err.Error(), "Anthropic cannot be removed: Sherlock, Troubleshooter") || !strings.Contains(err.Error(), "and Summarizer (off) use it. Move them to another provider first.") {
		t.Fatalf("the default in use: %v", err)
	}

	w.ai.opts.ProviderLocked = true
	if err := w.ai.MakeDefault(ctx, "gateway"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("made the default past the chart's lock: %v", err)
	}
	if err := w.ai.DeleteProvider(ctx, DefaultProvider); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "the chart fixes it") {
		t.Fatalf("removed the chart's provider: %v", err)
	}
	w.ai.opts.ProviderLocked = false

	if err := w.ai.MakeDefault(ctx, "gateway"); err != nil {
		t.Fatal(err)
	}
	v := w.ai.View(ctx)
	if len(v.Providers) != 2 || v.Providers[0].Default || !v.Providers[1].Default {
		t.Fatalf("the default: %+v", v.Providers)
	}
	for _, a := range v.Agents {
		if a.ProviderID != "gateway" {
			t.Fatalf("%s stayed on %s", a.ID, a.ProviderID)
		}
	}
	if err := w.ai.DeleteProvider(ctx, "gateway"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the new default in use was removed: %v", err)
	}
	// An agent put on Anthropic by name stays there when the default moves.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: DefaultProvider}}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.DeleteProvider(ctx, DefaultProvider); err == nil || err.Error() != "Anthropic cannot be removed: Auditor uses it. Move it to another provider first." {
		t.Fatalf("one agent: %v", err)
	}
	// Chosen as the default, it follows the default.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gateway"}}); err != nil {
		t.Fatal(err)
	}
	if p := w.ai.read(ctx).agents.Agents[Auditor].Provider; p != "" {
		t.Fatalf("the auditor names %q, not the default", p)
	}

	if err := w.ai.DeleteProvider(ctx, DefaultProvider); err != nil {
		t.Fatal(err)
	}
	v = w.ai.View(ctx)
	if len(v.Providers) != 1 || v.Providers[0].ID != "gateway" || !v.Providers[0].Default {
		t.Fatalf("after removing Anthropic: %+v", v.Providers)
	}
	if w.ai.KeysFor(DefaultProvider).Info().Set {
		t.Fatal("Anthropic's key stayed with it gone")
	}
	if err := w.ai.MakeDefault(ctx, DefaultProvider); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a removed provider made the default: %v", err)
	}
	if err := w.ai.SaveProvider(ctx, Provider{ID: DefaultProvider, Name: "Anthropic", Provider: sherlock.Provider{Kind: sherlock.KindAnthropic}}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a removed provider saved: %v", err)
	}
	if why := w.ai.Usable(Auditor, "local"); why != "" {
		t.Fatalf("the auditor on the new default: %q", why)
	}
	// The default itself cannot go, even with no agent on it.
	if err := w.ai.DeleteProvider(ctx, "gateway"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the last default was removed: %v", err)
	}
}
