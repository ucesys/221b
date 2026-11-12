package casework

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- Phase 12.1: the prompts' maintenance ---

// PM-J1d, PM-J5: an analysis says how sure it is of its cause and what it
// ruled out. A likely cause proposes nothing by itself: its fix is
// shown, and waits for no approval until the cause is confirmed.
func TestALikelyCauseProposesNothingByItself(t *testing.T) {
	w := setup(t)
	w.jobOn(t, true)
	ctx := context.Background()
	w.agents.answer = `{"summary": "web restarts; the node's kernel log would confirm it.", "cause": "The node runs out of memory.", "confidence": "Likely",
 "evidence": ["shop/Pod/web-0: OOMKilled"], "ruledOut": ["shop/Deployment/web: image pulls fine", " ", "shop/Service/web: endpoints ready"],
 "fix": {"summary": "Restart web", "commands": ["kubectl rollout restart deployment/web -n shop"]}, "proposals": []}`
	if _, err := w.svc.Create(ctx, NewCase{Title: "Slow", Cluster: "local"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	a := w.get(t, "C-1").Analysis
	if a == nil || a.Confidence != "likely" || strings.Join(a.RuledOut, "|") != "shop/Deployment/web: image pulls fine|shop/Service/web: endpoints ready" || len(a.Fix.Commands) != 1 {
		t.Fatalf("the analysis: %+v", a)
	}
	if props := w.props.List(); len(props) != 0 {
		t.Errorf("a likely cause proposed %+v", props)
	}
	// Confirmed, the same fix waits as a card; ruledOut written as one
	// text is read all the same, and a confidence in other words is none.
	w.agents.answer = `{"summary": "s", "cause": "The limit is too low.", "confidence": "confirmed", "evidence": [], "ruledOut": "the node is fine",
 "fix": {"summary": "Restart web", "commands": ["kubectl rollout restart deployment/web -n shop"]}, "proposals": []}`
	if _, err := w.svc.Create(ctx, NewCase{Title: "Slow too", Cluster: "local"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if a := w.get(t, "C-2").Analysis; a == nil || a.Confidence != "confirmed" || len(a.RuledOut) != 1 || a.RuledOut[0] != "the node is fine" {
		t.Fatalf("the second analysis: %+v", a)
	}
	if props := w.props.List(); len(props) != 1 || props[0].Source.ID != "C-2" {
		t.Errorf("a confirmed cause's fix: %+v", props)
	}
}

// PM-J6: an RCA's draft carries its timeline, how the problem was detected
// and what it affected as the RCA's free text, a line each; a field the
// agent wrote as a list is joined, and one of another shape is left out
// without losing the rest.
func TestAnRCADraftsFreeText(t *testing.T) {
	var a answer
	if err := json.Unmarshal([]byte(`{"cause": "The limit was too low.", "fixApplied": "Raised it.", "prevention": "Alert on memory.",
 "timeline": ["started 09:00", "detected 09:05", "fixed 09:30"], "detection": "the alert KubePodCrashLooping", "impact": {"odd": true}}`), &a); err != nil {
		t.Fatal(err)
	}
	if got, want := draftText(a), "Timeline: started 09:00; detected 09:05; fixed 09:30\nDetected by: the alert KubePodCrashLooping"; got != want {
		t.Errorf("the free text: %q, want %q", got, want)
	}
	if a.Cause != "The limit was too low." || a.Prevention != "Alert on memory." {
		t.Errorf("the rest: %+v", a)
	}
	if got := draftText(answer{}); got != "" {
		t.Errorf("nothing given: %q", got)
	}
	for _, want := range []string{`"timeline":`, `"detection":`, `"impact":`} {
		if !strings.Contains(draftFormat, want) {
			t.Errorf("the draft's format does not ask for %s", want)
		}
	}
}
