package sherlock

import "testing"

// PM-C5: on the last round the engine allows, the last message also says
// so, and the history itself is left as it was.
func TestTheLastRoundIsSaid(t *testing.T) {
	history := []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "Why is web failing?"}}},
		{Role: "assistant", Content: []Block{{Type: "tool_use", ID: "t1", Name: "get_pod"}}},
		{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: "t1", Content: "…"}}},
	}
	for round := 0; round < DefaultMaxRounds-1; round++ {
		if got := lastRound(history, round, DefaultMaxRounds); len(got[2].Content) != 1 {
			t.Fatalf("round %d already says it: %+v", round, got[2])
		}
	}
	got := lastRound(history, DefaultMaxRounds-1, DefaultMaxRounds)
	if len(got) != 3 || len(got[2].Content) != 2 || got[2].Content[0].Type != "tool_result" || got[2].Content[1].Text != LastRoundNote {
		t.Fatalf("the last round: %+v", got[2])
	}
	if len(history[2].Content) != 1 {
		t.Errorf("the history was changed: %+v", history[2])
	}
	// An engine with one round has no round to warn in, and a history
	// that ends with the model's turn is left alone.
	if got := lastRound(history, 0, 1); len(got[2].Content) != 1 {
		t.Errorf("one round: %+v", got[2])
	}
	if got := lastRound(history[:2], DefaultMaxRounds-1, DefaultMaxRounds); len(got[1].Content) != 1 {
		t.Errorf("after the model's turn: %+v", got[1])
	}
}
