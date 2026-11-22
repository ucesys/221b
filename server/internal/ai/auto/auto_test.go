package auto

import (
	"strings"
	"testing"
)

var both = Settings{Effort: Auto, Model: Auto}

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
	p = Rules(Settings{Effort: "high", Model: Auto}, RunInput{Trigger: "event", Event: "case", CasePriority: 0})
	if p.Effort != High || p.Model != Opus {
		t.Fatalf("fixed effort: %+v", p)
	}
	p = Rules(Settings{Effort: Auto, Model: Auto, FloorEffort: Medium, FloorModel: Sonnet}, RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1})
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
	if p.Model != DefaultModel || p.Effort != DefaultEffort || p.By != ByFixed || !strings.Contains(p.Why, "1.5 s") {
		t.Fatalf("timeout: %+v", p)
	}
	// The agent's fixed defaults apply when the classifier is silent.
	p, _ = Conversation(Settings{Effort: Auto, Model: "claude-opus-5-5"}, Turn{Failed: "no classifier"}, Limits{})
	if p.Model != Opus || p.Effort != Medium {
		t.Fatalf("fixed model, no classifier: %+v", p)
	}
	p, _ = Conversation(Settings{Effort: "low", Model: Auto}, Turn{Level: High}, Limits{})
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
	if !MoveUp(Haiku, Sonnet, false, 0) || MoveUp(Sonnet, Sonnet, false, 0) || MoveUp(Haiku, Opus, true, 0) || MoveUp(Haiku, Opus, false, 0.8) {
		t.Fatal("MoveUp")
	}
	if p := Inherited(both, XHigh); p.Effort != XHigh || p.Model != Opus || p.By != ByRules {
		t.Fatalf("inherited: %+v", p)
	}
	if p := Single(Settings{Effort: Auto, Model: Auto, FloorModel: Opus}); p.Effort != Medium || p.Model != Opus || p.By != ByFixed {
		t.Fatalf("single: %+v", p)
	}
	if !ValidEffort("") || !ValidEffort(Auto) || !ValidEffort(Max) || ValidEffort("extreme") {
		t.Fatal("ValidEffort")
	}
	if !strings.Contains(ClassifierPrompt(true), "first message") || !strings.Contains(ClassifierPrompt(false), "latest message") {
		t.Fatal("prompt")
	}
}
