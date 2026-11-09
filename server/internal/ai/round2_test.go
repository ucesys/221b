package ai

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// AI settings, round 2 (Phase 12, wave 5).

// An agent's built-in text shows pre-filled and may be edited (AS-01): a
// draft's edit of a part goes live like the house instructions, replaces
// that part whole, keeps the rule that cluster text is data, is kept with
// its version, and a reset to the built-in text is no edit at all.
func TestTheBuiltInTextIsEditable(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	v, err := w.ai.Instructions(ctx, Troubleshooter)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Parts) != 1 || v.Parts[0].Name != PartCore || v.Parts[0].Text != Core(Troubleshooter, false) || v.Parts[0].Edited || !strings.HasPrefix(v.Parts[0].Text, "You are the Troubleshooter") {
		t.Fatalf("the parts: %+v", v.Parts)
	}
	if s, _ := w.ai.Instructions(ctx, Sherlock); len(s.Parts) != 2 || s.Parts[1].Name != PartCompact || s.Parts[1].Text != CompactCore() {
		t.Fatalf("Sherlock's parts: %+v", s.Parts)
	}
	edit := "You are the Troubleshooter of Acme. Look at the ingress first."
	if _, err := w.ai.SaveDraftParts(ctx, Troubleshooter, "", nil, map[string]string{PartCore: edit}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.SaveDraftParts(ctx, Troubleshooter, "", nil, map[string]string{"view": "x"}, true, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a part it does not have: %v", err)
	}
	if _, err := w.ai.SaveDraftParts(ctx, Troubleshooter, "", nil, map[string]string{PartCore: strings.Repeat("word ", MaxPartWords+1)}, true, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a part over its words: %v", err)
	}
	if _, err := w.ai.SaveDraftParts(ctx, Troubleshooter, "Cite our runbooks.", nil, map[string]string{PartCore: edit}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	// A draft changes nothing in force; its preview shows it.
	if a, _ := w.ai.Agent(Troubleshooter, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); strings.Contains(a.Instructions, "Acme") {
		t.Fatal("a draft is in force")
	}
	if a, _ := w.ai.PreviewDraft(ctx, Troubleshooter, "local"); !strings.HasPrefix(a.Instructions, edit) || a.Version != "draft" {
		t.Fatalf("the draft's preview: %q", a.Instructions)
	}
	v, _ = w.ai.Instructions(ctx, Troubleshooter)
	if v.Parts[0].Draft == nil || *v.Parts[0].Draft != edit || v.Parts[0].Edited {
		t.Fatalf("the draft's part: %+v", v.Parts[0])
	}
	if _, err := w.ai.ApplyLive(ctx, Troubleshooter, v.Draft.Rev, "sam"); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Troubleshooter, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.Instructions, edit) || strings.Contains(a.Instructions, "Rules every agent") || !strings.Contains(a.Instructions, "Cite our runbooks.") {
		t.Fatalf("the edit is not in force whole:\n%s", a.Instructions)
	}
	if core, _ := sherlock.AgentPromptText(a, sherlock.Question{Cluster: "local"}, time.Now()); !strings.Contains(core, sherlock.ClusterTextRule) {
		t.Fatal("an edited core lost the rule that cluster text is data")
	}
	v, _ = w.ai.Instructions(ctx, Troubleshooter)
	if !v.Parts[0].Edited || v.Parts[0].Text != edit || v.Parts[0].BuiltInChanged || v.Versions[0].Parts[PartCore] != edit {
		t.Fatalf("after going live: %+v %+v", v.Parts[0], v.Versions[0])
	}
	// The house instructions saved alone keep the edit in force.
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Cite our runbooks by name.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.ai.Instructions(ctx, Troubleshooter); v.Draft.Parts[PartCore] != edit {
		t.Fatalf("the draft dropped the edit: %+v", v.Draft.Parts)
	}
	// Reset to built-in: the part given as its built-in text is no edit.
	if _, err := w.ai.SaveDraftParts(ctx, Troubleshooter, "", nil, map[string]string{PartCore: Core(Troubleshooter, false)}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	v, _ = w.ai.Instructions(ctx, Troubleshooter)
	if len(v.Draft.Parts) != 0 {
		t.Fatalf("a reset kept an edit: %+v", v.Draft.Parts)
	}
	if _, err := w.ai.ApplyLive(ctx, Troubleshooter, v.Draft.Rev, "sam"); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.Agent(Troubleshooter, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); !strings.HasPrefix(a.Instructions, "You are the Troubleshooter") {
		t.Fatal("the reset is not in force")
	}
	// Restoring the version with the edit brings the edit back.
	v, _ = w.ai.Instructions(ctx, Troubleshooter)
	if err := w.ai.Restore(ctx, Troubleshooter, v.Versions[1].N, "ann"); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.ai.Instructions(ctx, Troubleshooter); v.Draft.Parts[PartCore] != edit {
		t.Fatalf("the restored draft: %+v", v.Draft)
	}
	// Sherlock's built-in core follows view changes: an edit made with
	// them on says the built-in text changed once they are off.
	if _, err := w.ai.SaveDraftParts(ctx, Sherlock, "", nil, map[string]string{PartCore: "You are Sherlock of Acme."}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	s, _ := w.ai.Instructions(ctx, Sherlock)
	if _, err := w.ai.ApplyLive(ctx, Sherlock, s.Draft.Rev, "sam"); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{ViewControl: &off}); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.ai.Instructions(ctx, Sherlock); !s.Parts[0].BuiltInChanged || s.Parts[0].Text != "You are Sherlock of Acme." {
		t.Fatalf("Sherlock's edit: %+v", s.Parts[0])
	}
}

// An in-house endpoint that takes no credential (the owner's A5000) may
// be used once a test passed without one, and only with its settings as
// tested (AS-04).
func TestAProviderWithoutACredentialIsUsableOnceTested(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	a5000 := Provider{ID: "a5000", Name: "A5000", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: srv.URL + "/open/v1"}, Model: "qwen3.8-27b"}
	if err := w.ai.SaveProvider(ctx, a5000, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "a5000"}}); err != nil {
		t.Fatal(err)
	}
	if why := w.ai.Usable(Auditor, "local"); !strings.Contains(why, "has no credential") {
		t.Fatalf("usable untested: %q", why)
	}
	// A failed test leaves it unusable; a passed one, without a key, not.
	if err := w.ai.RecordTest(ctx, "a5000", "qwen3.8-27b", errors.New("refused")); err != nil {
		t.Fatal(err)
	}
	if why := w.ai.Usable(Auditor, "local"); why == "" {
		t.Fatal("usable after a failed test")
	}
	if err := w.ai.RecordTest(ctx, "a5000", "qwen3.8-27b", nil); err != nil {
		t.Fatal(err)
	}
	if why := w.ai.Usable(Auditor, "local"); why != "" {
		t.Fatalf("unusable after a passed test: %q", why)
	}
	pv := providerView(t, w.ai, "a5000")
	if !pv.KeylessOK || pv.Tested == nil || !pv.Tested.Keyless || pv.Key.Set {
		t.Fatalf("the provider's view: %+v", pv)
	}
	res := w.run(t, Auditor, "Review the cluster.")
	if res.Err != nil {
		t.Fatalf("the auditor on the in-house endpoint: %v", res.Err)
	}
	// Saving it again keeps the test: the request never sets one.
	a5000.Tested = &ProviderTest{OK: false}
	a5000.Name = "A5000 rack"
	if err := w.ai.SaveProvider(ctx, a5000, false); err != nil {
		t.Fatal(err)
	}
	if why := w.ai.Usable(Auditor, "local"); why != "" {
		t.Fatalf("a rename dropped the test: %q", why)
	}
	// Another endpoint needs a new test.
	a5000.Endpoint = srv.URL + "/open2/v1"
	if err := w.ai.SaveProvider(ctx, a5000, false); err != nil {
		t.Fatal(err)
	}
	if why := w.ai.Usable(Auditor, "local"); !strings.Contains(why, "no credential") {
		t.Fatalf("a new endpoint kept the old test: %q", why)
	}
}

func providerView(t *testing.T, s *Service, id string) ProviderView {
	t.Helper()
	for _, p := range s.View(context.Background()).Providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no provider %s", id)
	return ProviderView{}
}

// On a model whose context Sherlock's whole form does not fit, it
// answers in its compact form (AS-07): the compact core with the house
// instructions, the essential reads only, no hand-over, no skills, no
// view actions, and a reply bound that fits; the view says so with both
// sizes. It may also be chosen always or never.
func TestSherlockIsCompactOnASmallModel(t *testing.T) {
	w := setup(t, map[string]string{Sherlock: "Our on-call rota is in the wiki."})
	w.ai.opts.ToolDefs = func() []sherlock.ToolDef { return testDefs(t) }
	ctx := context.Background()
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	if err := w.ai.SaveProvider(ctx, Provider{ID: "a5000", Name: "A5000", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: srv.URL + "/open/v1"}, Model: "qwen3.8-27b", ContextWindow: 16384}, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.RecordTest(ctx, "a5000", "qwen3.8-27b", nil); err != nil {
		t.Fatal(err)
	}
	// On the Claude API, Sherlock is whole.
	whole, err := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}, SuperAdmin: true, Cases: true})
	if err != nil || whole.Compact || whole.MaxTokens != 0 || len(whole.Roster) == 0 {
		t.Fatalf("on the Claude API: compact %v, max %d, roster %d, %v", whole.Compact, whole.MaxTokens, len(whole.Roster), err)
	}
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Provider: "a5000"}}); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}, Cases: true})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Compact || !strings.HasPrefix(a.Instructions, CompactCore()) || !strings.Contains(a.Instructions, "Our on-call rota") || strings.Contains(a.Instructions, "hand_over") || strings.Contains(a.Instructions, CasesSkill()) || len(a.Roster) != 0 || a.MaxTokens != 4096 {
		t.Fatalf("compact %v, roster %d, max %d:\n%s", a.Compact, len(a.Roster), a.MaxTokens, a.Instructions)
	}
	for _, tool := range a.Tools {
		if !strings.Contains(strings.Join(CompactTools, " "), tool) {
			t.Fatalf("a tool beyond the compact ones: %s", tool)
		}
	}
	f := sherlock.Measure(a, testDefs(t), true)
	if f.Total() > 4000 || f.ToolCount != len(a.Tools) {
		t.Fatalf("the compact form is not small: %+v", f)
	}
	for _, d := range sherlock.OfferedDefs(a, testDefs(t), true) {
		if sherlock.IsUITool(d.Name) {
			t.Fatalf("a view action in the compact form: %s", d.Name)
		}
	}
	var sv AgentView
	for _, x := range w.ai.View(ctx).Agents {
		if x.ID == Sherlock {
			sv = x
		}
	}
	if fp := sv.Footprint; fp == nil || !fp.Compact || fp.Window != 16384 || fp.WindowFrom != "provider" || fp.Whole.Total() <= 8192 || fp.Total() >= fp.Whole.Total() || fp.MaxTokens != 4096 {
		t.Fatalf("the footprint: %+v", sv.Footprint)
	}
	// Never, always, and a large context.
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Compact: CompactNever}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); a.Compact {
		t.Fatal("compact though never")
	}
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Provider: "a5000", Model: "qwen3.8-27b-long", ContextWindow: 131072}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); a.Compact || a.MaxTokens != 0 {
		t.Fatalf("compact on a large context: max %d", a.MaxTokens)
	}
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Compact: CompactAlways}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); !a.Compact {
		t.Fatal("not compact though always")
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Compact: CompactAlways}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another agent made compact: %v", err)
	}
	if err := w.ai.SaveProvider(ctx, Provider{ID: "tiny", Name: "Tiny", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: srv.URL + "/open/v1"}, ContextWindow: 100}, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a context window of 100 tokens: %v", err)
	}
	// An edited compact core is the one used.
	if _, err := w.ai.SaveDraftParts(ctx, Sherlock, "", nil, map[string]string{PartCompact: "You are Sherlock, briefly."}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.PreviewDraft(ctx, Sherlock, "local"); !strings.HasPrefix(a.Instructions, "You are Sherlock, briefly.") || !a.Compact {
		t.Fatalf("the edited compact core: %q", a.Instructions)
	}
}

// testDefs are tool definitions of the toolbox's names, for measuring.
func testDefs(t *testing.T) []sherlock.ToolDef {
	t.Helper()
	var out []sherlock.ToolDef
	for _, name := range readTools {
		out = append(out, sherlock.ToolDef{Name: name, Description: strings.Repeat("Reads "+strings.ReplaceAll(name, "_", " ")+" from the cluster, with what matters about each, newest first, as the person may see it. ", 4), InputSchema: []byte(`{"type":"object","properties":{"namespace":{"type":"string","description":"Only this namespace."},"query":{"type":"string","description":"Only rows containing this text."}}}`)})
	}
	return out
}

// Back to draft ends a pilot, drops the test and takes a new revision, so
// an approval asked for the draft no longer applies (AS-03).
func TestBackToDraft(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.BackToDraft(ctx, Auditor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("without a draft: %v", err)
	}
	if _, err := w.ai.SaveDraft(ctx, Auditor, "Weigh PCI findings first.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.RunTests(ctx, Auditor, TestOptions{Draft: true, By: "ann"}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.StartPilot(ctx, Auditor, []string{"ann@corp.example"}, nil, time.Hour, "ann"); err != nil {
		t.Fatal(err)
	}
	rev, err := w.ai.ReadyForLive(ctx, Auditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.ai.BackToDraft(ctx, Auditor); err != nil {
		t.Fatal(err)
	}
	v, _ := w.ai.Instructions(ctx, Auditor)
	if v.Draft.Stage != StageDraft || v.Draft.Test != nil || v.Draft.Pilot != nil || v.Draft.Rev == rev || v.Draft.House != "Weigh PCI findings first." {
		t.Fatalf("back to draft: %+v", v.Draft)
	}
	if _, err := w.ai.ApplyLive(ctx, Auditor, rev, "sam"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an approval of the revision before applied: %v", err)
	}
	if _, err := w.ai.ReadyForLive(ctx, Auditor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("untested again, it may go live: %v", err)
	}
}
