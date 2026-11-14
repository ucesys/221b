package sherlock

import (
	"strings"
	"testing"
)

// A hand-over's bounds cut at a character, never inside one (PM-D5).
func TestAHandOversTextIsCutAtACharacter(t *testing.T) {
	if got := clipBytes("short", 100); got != "short" {
		t.Errorf("short: %q", got)
	}
	text := strings.Repeat("é", 10) // two bytes each
	got := clipBytes(text, 5)
	if got != "éé…" {
		t.Errorf("cut: %q", got)
	}
	if MaxHandOverTask < 2000 || MaxHandOverContext < MaxHandOverTask {
		t.Errorf("a brief does not fit: task %d, context %d", MaxHandOverTask, MaxHandOverContext)
	}
	if !strings.Contains(HandOverHelp, "sees nothing of your conversation") {
		t.Errorf("hand_over's description: %q", HandOverHelp)
	}
}
