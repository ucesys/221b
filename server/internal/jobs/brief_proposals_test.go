package jobs

import "testing"

// A run's summary counts the proposed changes it filed apart from those it
// made that were not filed: "34 findings, 1 proposed change" read as if a
// change waited in the Inbox when it was refused for resolving no case
// (CF-27; the owner, November 22, 2026).
func TestARunsBriefCountsTheProposalsNotFiledApart(t *testing.T) {
	r := Run{ID: "run-1", Proposals: []ProposalRef{
		{ID: "card-1", Title: "Raise the limit", Resolves: []string{"C-1"}},
		{Title: "Enforce Pod Security", Error: NamesNothing},
	}}
	b := brief(r)
	if b.Proposals != 1 || b.NotFiled != 1 {
		t.Fatalf("filed %d, not filed %d; want 1 and 1", b.Proposals, b.NotFiled)
	}
	if b := brief(Run{ID: "run-2", Proposals: []ProposalRef{{Title: "x", Error: NamesNothing}}}); b.Proposals != 0 || b.NotFiled != 1 {
		t.Fatalf("a run whose only proposal was refused: filed %d, not filed %d", b.Proposals, b.NotFiled)
	}
}
