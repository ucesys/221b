package sherlocktools

import (
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
)

// The descriptions of Sherlock's toolbox are kept in the golden directory
// (P-3).
func TestGoldenPrompts(t *testing.T) {
	var b strings.Builder
	for _, d := range Defs() {
		b.WriteString("## " + d.Name + "\n\n" + d.Description + "\n\n" + string(d.InputSchema) + "\n\n")
	}
	prompttest.Check(t, "sherlock.toolbox", b.String())
}
