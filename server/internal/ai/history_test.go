package ai

import (
	"context"
	"strings"
	"testing"
)

// Every save of a draft is kept whole until a draft goes live (AI-32):
// sent back to its first stage or restored from a version, it is kept
// again, saying so; its test lands on it; going live drops them all and
// keeps the draft that went live as its version, with its revision and
// test.
func TestHistoryKeepsTheDraftsUntilOneGoesLive(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if _, err := w.ai.SaveDraftParts(ctx, Auditor, "One.", map[string]string{AllClusters: "Note."}, map[string]string{PartCore: "My core."}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.SaveDraft(ctx, Auditor, "Two.", nil, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.RunTests(ctx, Auditor, TestOptions{Draft: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.BackToDraft(ctx, Auditor, "cat"); err != nil {
		t.Fatal(err)
	}
	list, err := w.ai.History(ctx, Auditor)
	if err != nil {
		t.Fatal(err)
	}
	// The baseline in force, draft 1, draft 2 (tested) and draft 3, sent back.
	if len(list) != 4 || list[0].Rev != 3 || !list[0].Current || list[0].By != "cat" || !strings.Contains(list[0].Note, "draft 2 sent back") || list[1].Rev != 2 || list[1].Test == nil || list[2].Rev != 1 || list[3].N != 0 || !list[3].InForce {
		t.Fatalf("history: %+v", list)
	}
	one, err := w.ai.HistoryItem(ctx, Auditor, HistoryDraft, 1)
	if err != nil {
		t.Fatal(err)
	}
	if one.House != "One." || one.Notes[AllClusters] != "Note." || one.Parts[0].Name != PartCore || one.Parts[0].Text != "My core." {
		t.Fatalf("draft 1: %+v", one)
	}
	two, _ := w.ai.HistoryItem(ctx, Auditor, HistoryDraft, 2)
	if two.Parts[0].Text != BuiltInPart(Auditor, PartCore, false) {
		t.Fatalf("draft 2's core is the built-in one: %q", two.Parts[0].Text)
	}
	base, _ := w.ai.HistoryItem(ctx, Auditor, HistoryLive, 0)
	if base.House != w.ai.Baseline(Auditor) || !base.InForce {
		t.Fatalf("the baseline: %+v", base)
	}
	rev, _ := w.ai.ReadyUntested(ctx, Auditor)
	v, err := w.ai.ApplyLiveWith(ctx, Auditor, rev, "dan", "hotfix")
	if err != nil {
		t.Fatal(err)
	}
	if v.Rev != 3 || v.N != 1 {
		t.Fatalf("the version: %+v", v)
	}
	if list, _ = w.ai.History(ctx, Auditor); len(list) != 1 || list[0].N != 1 || list[0].Rev != 3 || !list[0].InForce {
		t.Fatalf("after live: %+v", list)
	}
	// Restored, a version is a draft again, which says so.
	if _, err := w.ai.SaveDraft(ctx, Auditor, "Four.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	rev, _ = w.ai.ReadyUntested(ctx, Auditor)
	if _, err := w.ai.ApplyLiveWith(ctx, Auditor, rev, "dan", "again"); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.Restore(ctx, Auditor, 1, "ann"); err != nil {
		t.Fatal(err)
	}
	list, _ = w.ai.History(ctx, Auditor)
	if len(list) != 3 || !list[0].Current || list[0].Note != "restored version 1" || list[1].N != 2 || !list[1].InForce || list[2].N != 1 || list[2].InForce {
		t.Fatalf("restored: %+v", list)
	}
}

// A draft saved before drafts were kept is listed from itself, and the
// drafts kept go first when the key runs short, never the draft now.
func TestHistoryOfAnOlderDraftAndPruning(t *testing.T) {
	ap := &AgentPrompt{Live: 1, Versions: []Version{{N: 1, House: "Live."}}, Draft: &Draft{Rev: 4, House: "Old draft."}}
	list := historyOf(ap)
	if len(list) != 2 || !list[0].entry.Current || list[0].house != "Old draft." || list[1].entry.N != 1 {
		t.Fatalf("%+v", list)
	}
	big := strings.Repeat("word ", 20000)
	ps := &Prompts{Agents: map[string]*AgentPrompt{Auditor: {Draft: &Draft{Rev: 9, House: "Now."}}}}
	p := ps.Agents[Auditor]
	for i := 1; i <= 9; i++ {
		p.Revisions = append(p.Revisions, Revision{Rev: i, House: big})
	}
	p.Revisions[8].House = "Now."
	prune(ps)
	if len(p.Revisions) == 0 || p.Revisions[len(p.Revisions)-1].Rev != 9 || p.Revisions[0].Rev == 1 {
		t.Fatalf("pruned: %d left, first %d", len(p.Revisions), p.Revisions[0].Rev)
	}
}
