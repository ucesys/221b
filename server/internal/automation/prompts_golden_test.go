package automation

import (
	"testing"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
)

// The Automation prompts are kept in the golden directory (P-3).
func TestGoldenPrompts(t *testing.T) {
	prompttest.Check(t, "automation.impact", ImpactPrompt)
	prompttest.Check(t, "automation.draft", DraftPrompt)
	prompttest.Check(t, "automation.review", ReviewPrompt)
}
