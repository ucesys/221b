package knowledge

import (
	"testing"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
)

// What Mycroft is told when it writes the wiki is kept in the golden
// directory (P-3).
func TestGoldenPrompts(t *testing.T) {
	prompttest.Check(t, "wiki.sync", syncPrompt)
}
