package auto

import (
	"strings"
	"testing"
)

var both = Settings{Effort: Auto, Model: Auto, Ladder: Claude}

// The rules pick a run's step from what started it (AI-45): the table,
// the limits and the fixed settings.
func TestRulesPickFromWhatStartedTheRunAI45(t *testing.T) {
	for _, c := range []struct {
		name                string
		in                  RunInput
		effort, model, says string
	}{
		{"P0", RunInput{Trigger: "event", Event: "case", CasePriority: 0}, XHigh, Opus, "a P0 case"},
		{"P1", RunInput{Trigger: "event", Event: "case", CasePriority: 1}, High, Opus, "a P1 case"},
		{"P3", RunInput{Trigger: "event", Event: "case", CasePriority: 3}, Medium, Sonnet, "a P3 case"},
		{"alert on production", RunInput{Trigger: "event", Event: "alert", CasePriority: -1, Production: true}, High, Sonnet, "an alert on a production cluster"},
		{"alert", RunInput{Trigger: "event", Event: "alert", CasePriority: -1}, Medium, Sonnet, "an alert"},
		{"report", RunInput{Trigger: "event", Event: "report", CasePriority: -1}, Medium, Sonnet, "a problem report"},
		{"digest", RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1}, Low, Haiku, "a schedule, a summary"},
		{"digest on production", RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1, Production: true}, Low, Sonnet, "on a production cluster"},
		{"audit", RunInput{Trigger: "schedule", Kind: "nightly-audit", CasePriority: -1}, Medium, Sonnet, "a schedule, an audit"},
		{"run now", RunInput{Trigger: "manual", Kind: "other", CasePriority: -1}, Low, Sonnet, "Run now"},
		// Past 80% of the day's budget, one level lower.
		{"budget", RunInput{Trigger: "event", Event: "alert", CasePriority: -1, Production: true, DayShare: 0.85}, Medium, Sonnet, "85% spent: one level lower"},
	} {
		p := Rules(both, c.in)
		if p.Effort != c.effort || p.Model != c.model || p.By != ByRules || !strings.Contains(p.Why, c.says) {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
	// What is fixed stays fixed; the floor raises what is left to Auto.
	p := Rules(Settings{Effort: Auto, Model: "claude-fable-5-1"}, RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1})
	if p.Model != "claude-fable-5-1" || p.Effort != Low {
		t.Fatalf("fixed model: %+v", p)
	}
	p = Rules(Settings{Effort: "high", Model: Auto, Ladder: Claude}, RunInput{Trigger: "event", Event: "case", CasePriority: 0})
	if p.Effort != High || p.Model != Opus {
		t.Fatalf("fixed effort: %+v", p)
	}
	p = Rules(Settings{Effort: Auto, Model: Auto, FloorEffort: Medium, FloorModel: Sonnet, Ladder: Claude}, RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1})
	if p.Effort != Medium || p.Model != Sonnet || !strings.Contains(p.Why, "floor") {
		t.Fatalf("floor: %+v", p)
	}
}

// Auto never picks xhigh or max without an open P0 or P1 case (AI-46).
func TestNoXHighWithoutAnUrgentCaseAI46(t *testing.T) {
	p := finish(both, Pick{Effort: Max, Model: Opus, Why: "x"}, Limits{})
	if p.Effort != High || !strings.Contains(p.Why, "no xhigh or max") {
		t.Fatalf("%+v", p)
	}
	p = finish(both, Pick{Effort: Max, Model: Opus}, Limits{Urgent: true})
	if p.Effort != Max {
		t.Fatalf("urgent: %+v", p)
	}
	// Low is not stepped below low.
	p = finish(both, Pick{Effort: Low, Model: Haiku}, Limits{DayShare: 0.95})
	if p.Effort != Low {
		t.Fatalf("budget at low: %+v", p)
	}
}

// A conversation starts on the model of its first message's level, moves
// up once to Opus on a later high message, never down; the classifier's
// silence is the defaults (AI-44, AI-45).
func TestAConversationMovesUpOnceAI44(t *testing.T) {
	p, moved := Conversation(both, Turn{Level: Low}, Limits{})
	if p.Model != Haiku || p.Effort != Low || p.By != ByClassifier || moved {
		t.Fatalf("first low: %+v", p)
	}
	p, moved = Conversation(both, Turn{Level: Medium, Model: Haiku}, Limits{})
	if p.Model != Haiku || p.Effort != Medium || moved {
		t.Fatalf("later medium stays on its model: %+v", p)
	}
	p, moved = Conversation(both, Turn{Level: High, Model: Haiku}, Limits{})
	if p.Model != Opus || p.Effort != High || !moved || !strings.Contains(p.Why, "moved up from Claude Haiku 5.5") {
		t.Fatalf("later high moves up: %+v %v", p, moved)
	}
	p, moved = Conversation(both, Turn{Level: High, Model: Sonnet, MovedUp: true}, Limits{})
	if p.Model != Sonnet || moved {
		t.Fatalf("only once: %+v", p)
	}
	p, moved = Conversation(both, Turn{Level: Low, Model: Opus}, Limits{})
	if p.Model != Opus || p.Effort != Low || moved {
		t.Fatalf("never down: %+v", p)
	}
	p, moved = Conversation(both, Turn{Level: High, Model: Sonnet}, Limits{DayShare: 0.9})
	if p.Model != Sonnet || moved || p.Effort != Medium {
		t.Fatalf("no move up past 80%%, one level lower: %+v", p)
	}
	p, _ = Conversation(both, Turn{Failed: "the classifier did not answer in 1.5 s"}, Limits{})
	if p.Model != Sonnet || p.Effort != DefaultEffort || p.By != ByFixed || !strings.Contains(p.Why, "1.5 s") {
		t.Fatalf("timeout: %+v", p)
	}
	// The agent's fixed defaults apply when the classifier is silent.
	p, _ = Conversation(Settings{Effort: Auto, Model: "claude-opus-5-5"}, Turn{Failed: "no classifier"}, Limits{})
	if p.Model != Opus || p.Effort != Medium {
		t.Fatalf("fixed model, no classifier: %+v", p)
	}
	p, _ = Conversation(Settings{Effort: "low", Model: Auto, Ladder: Claude}, Turn{Level: High}, Limits{})
	if p.Model != Opus || p.Effort != Low {
		t.Fatalf("fixed effort: %+v", p)
	}
	// A fixed model never moves.
	p, moved = Conversation(Settings{Effort: Auto, Model: Sonnet}, Turn{Level: High, Model: Sonnet}, Limits{})
	if p.Model != Sonnet || moved {
		t.Fatalf("fixed model moved: %+v", p)
	}
}

func TestSaidAndRuns(t *testing.T) {
	for in, want := range map[string]string{"high": High, " Low.\n": Low, "MEDIUM": Medium, "maybe": "", "xhigh": ""} {
		if got := Said(in); got != want {
			t.Errorf("Said(%q) = %q", in, got)
		}
	}
	if !MoveUp(Claude, Haiku, Sonnet, false, 0) || MoveUp(Claude, Sonnet, Sonnet, false, 0) || MoveUp(Claude, Haiku, Opus, true, 0) || MoveUp(Claude, Haiku, Opus, false, 0.8) {
		t.Fatal("MoveUp")
	}
	if p := Inherited(both, XHigh); p.Effort != XHigh || p.Model != Opus || p.By != ByRules {
		t.Fatalf("inherited: %+v", p)
	}
	if p := Single(Settings{Effort: Auto, Model: Auto, FloorModel: Opus, Ladder: Claude}); p.Effort != Medium || p.Model != Opus || p.By != ByFixed {
		t.Fatalf("single: %+v", p)
	}
	if !ValidEffort("") || !ValidEffort(Auto) || !ValidEffort(Max) || ValidEffort("extreme") {
		t.Fatal("ValidEffort")
	}
	if !strings.Contains(ClassifierPrompt(true), "first message") || !strings.Contains(ClassifierPrompt(false), "latest message") {
		t.Fatal("prompt")
	}
}

// Auto picks by tier among what the provider offers, on any provider (A9):
// a ladder of in-house models is picked like Claude's; the preferred model
// of a tier is the first offered; a tier with no model takes the next tier
// up with one, else the next down; "move up" is to the strong tier's model,
// or the strongest above the current with none; a model with no tier or
// not offered is never picked nor moved from; the floor is a tiered model.
func TestAutoPicksByTierAmongTheOfferedModelsA9(t *testing.T) {
	qwen := Ladder{{Model: "qwen3.8-8b", Tier: Light}, {Model: "qwen3.8-27b", Tier: Standard}, {Model: "qwen3.8-27b-fp8", Tier: Standard},
		{Model: "embed-1"}, {Model: "qwen3.8-235b", Tier: Strong}}
	s := Settings{Effort: Auto, Model: Auto, Ladder: qwen}
	if p := Rules(s, RunInput{Trigger: "event", Event: "case", CasePriority: 1}); p.Model != "qwen3.8-235b" || p.Effort != High {
		t.Fatalf("a P1 case: %+v", p)
	}
	if p := Rules(s, RunInput{Trigger: "event", Event: "alert", CasePriority: -1}); p.Model != "qwen3.8-27b" {
		t.Fatalf("the first standard model is preferred: %+v", p)
	}
	if p, _ := Conversation(s, Turn{Level: Low}, Limits{}); p.Model != "qwen3.8-8b" {
		t.Fatalf("a low first message: %+v", p)
	}
	p, moved := Conversation(s, Turn{Level: High, Model: "qwen3.8-8b"}, Limits{})
	if p.Model != "qwen3.8-235b" || !moved || !strings.Contains(p.Why, "moved up from qwen3.8-8b") {
		t.Fatalf("moved up: %+v %v", p, moved)
	}
	// A model no longer offered (or with no tier) is not moved from.
	if p, moved := Conversation(s, Turn{Level: High, Model: "embed-1"}, Limits{}); p.Model != "embed-1" || moved {
		t.Fatalf("an untiered model moved: %+v", p)
	}
	// Gaps: no light model, so light takes standard; no strong model, so
	// strong takes standard and a high message on standard does not move.
	gaps := Settings{Effort: Auto, Model: Auto, Ladder: Ladder{{Model: "m-std", Tier: Standard}}}
	if p := Rules(gaps, RunInput{Trigger: "event", Event: "source", CasePriority: -1}); p.Model != "m-std" {
		t.Fatalf("light with none: %+v", p)
	}
	if p, moved := Conversation(gaps, Turn{Level: High, Model: "m-std"}, Limits{}); p.Model != "m-std" || moved {
		t.Fatalf("nothing stronger: %+v", p)
	}
	two := Ladder{{Model: "a", Tier: Light}, {Model: "b", Tier: Standard}}
	if two.Of(Strong) != "b" || two.Of(Light) != "a" || Ladder(nil).Of(Standard) != "" || !(Ladder{{Model: "x"}}).Empty() || two.Empty() {
		t.Fatal("Of and Empty")
	}
	if p, moved := Conversation(Settings{Effort: Auto, Model: Auto, Ladder: two}, Turn{Level: High, Model: "a"}, Limits{}); p.Model != "b" || !moved {
		t.Fatalf("moved up to the strongest there is: %+v", p)
	}
	// The floor: a tiered model of the ladder.
	if p := Rules(Settings{Effort: Auto, Model: Auto, Ladder: qwen, FloorModel: "qwen3.8-27b-fp8"}, RunInput{Trigger: "event", Event: "source", CasePriority: -1}); p.Model != "qwen3.8-27b-fp8" {
		t.Fatalf("floor: %+v", p)
	}
	if !MoveUp(qwen, "qwen3.8-8b", "qwen3.8-27b", false, 0) || MoveUp(qwen, "qwen3.8-27b", "qwen3.8-27b-fp8", false, 0) || MoveUp(qwen, "gone", "qwen3.8-235b", false, 0) {
		t.Fatal("MoveUp by tier")
	}
	for in, want := range map[string]string{Opus: "Claude Opus 5.5", "claude-fable-5-1": "Claude Fable 5.1", "claude-sonnet-6": "Claude Sonnet 6", "qwen3.8-27b": "qwen3.8-27b", "claude-x-y": "claude-x-y"} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q", in, got)
		}
	}
	if !ValidTier("") || !ValidTier(Strong) || ValidTier("heavy") {
		t.Fatal("ValidTier")
	}
}
