package casework

import (
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
	"github.com/ucesys/221b/server/internal/issues"
)

// The case analysis job's tasks, an analysis and an RCA's draft, with a
// sample case, are kept in the golden directory (P-3).
func TestGoldenPrompts(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	c := caseInfo{ID: "C-12", Number: 12, Title: "web-0 keeps restarting", Priority: 1, Cluster: "local", Resource: "shop/Pod/web-0",
		Detail: "Back-off restarting failed container web.",
		Source: Source{Kind: SourceFinding, Finding: "R-3.1", Run: "R-3", Check: "pod.crashloop", Severity: "critical"}}
	prompttest.Check(t, "case.analysis", task(request{Case: c, Related: "C-7 (closed as Fixed): web-0 ran out of memory; RCA: the limit was too low."}, "local", AnalysisTask))
	c.Analysis = &Analysis{Run: "R-4", At: at, Summary: "The container runs out of memory at start.", Cause: "Its memory limit is 64Mi.", Evidence: []string{"shop/Pod/web-0: Last State Terminated, Reason OOMKilled"},
		Fix: Fix{Summary: "Raise the limit.", Commands: []string{"kubectl rollout restart deployment/web -n shop"}}}
	// Sent back, the task carries what people wrote on the case (PM-J5).
	c.Messages = []string{"2026-11-10 09:00 (operator): Raised the limit to 256Mi."}
	prompttest.Check(t, "case.analysis-again", task(request{Case: c}, "local", AnalysisTask))
	c.History = []Event{{At: at, What: "Fix applied", By: issues.Actor{Name: "Ann", Kind: issues.ActorPerson}}}
	prompttest.Check(t, "case.rca-draft", task(request{Case: c, Draft: true, Cards: []CardRef{{ID: "PC-4", Title: "Raise web's memory limit", State: "applied"}}}, "local", AnalysisTask))
}
