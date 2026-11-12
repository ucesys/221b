package sherlock

import (
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
)

// The agents' own tools and the view actions, as the model reads them,
// and the answer to ask_knowledge when nothing is connected, are kept in
// the golden directory (P-3). Sherlock's built-in instructions are kept
// with package ai's, as an agent gets them.
func TestGoldenPrompts(t *testing.T) {
	a := &Agent{ID: SherlockID, Tools: []string{ToolAskKnowledge, ToolSetupStatus, ToolCheckCommand},
		Roster: []RosterEntry{{ID: "troubleshooter", Name: "Troubleshooter"}, {ID: "changePlanner", Name: "Change planner"}},
		Setup:  func(context.Context) string { return "" }}
	var b strings.Builder
	for _, d := range agentToolDefs(a, 0) {
		b.WriteString("## " + d.Name + "\n\n" + d.Description + "\n\n" + string(d.InputSchema) + "\n\n")
	}
	b.WriteString("## when no knowledge is connected\n\n" + NoKnowledge + "\n")
	prompttest.Check(t, "sherlock.agent-tools", b.String())
	b.Reset()
	for _, d := range uiDefs(true) {
		b.WriteString("## " + d.Name + "\n\n" + d.Description + "\n\n" + string(d.InputSchema) + "\n\n")
	}
	prompttest.Check(t, "sherlock.view-actions", b.String())
}
