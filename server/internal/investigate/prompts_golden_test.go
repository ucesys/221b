package investigate

import (
	"testing"

	"github.com/ucesys/221b/server/internal/ai/prompttest"
	"github.com/ucesys/221b/server/internal/issues"
)

// The report triage job's tasks, a triage and the check of a fix, with a
// sample report, are kept in the golden directory (P-3).
func TestGoldenPrompts(t *testing.T) {
	var r issues.Case
	r.Number = 5
	r.Text = "The shop shows an error when I pay."
	r.Messages = []issues.Message{{Kind: issues.FromReporter, Text: "It started this morning."}}
	services := []issues.ServiceDef{{ID: "shop", Name: "Web shop", Aliases: []string{"store"}, RunsOn: []issues.RunsOn{{Kind: "namespace", Value: "shop", Cluster: "local"}}}}
	known := []issues.KnownIssue{{ID: "KI-3", Title: "Payments time out", Status: issues.KnownInvestigating, Kind: "incident", Symptoms: []string{"payment fails"}}}
	prompttest.Check(t, "report.triage", task(request{Report: r}, "local", services, known, TriageTask))
	r.Cause, r.Technical, r.Evidence = "The payments pod runs out of memory.", "payments-0 is OOMKilled.", []string{"shop/Pod/payments-0: OOMKilled"}
	prompttest.Check(t, "report.verify", task(request{Report: r, Verify: true}, "local", services, known, TriageTask))
}
