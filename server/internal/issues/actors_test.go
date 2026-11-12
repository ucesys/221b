package issues

import (
	"strings"
	"testing"
)

// --- The owner's feedback on Cases (Phase 12, wave 6c, CF-35 to CF-37) ---

// A case keeps who opened it and who changed it last, with when, on its
// record and its index line (CF-35); its type and incident number are
// changed by an operator, listed by type, found by the incident number
// (CF-36, CF-37).
func TestACaseNamesWhoChangedItAndItsTypeAndIncident(t *testing.T) {
	f := newFixture(t, Options{})
	rec, err := f.s.Submit(ctx, alice, Submission{Text: "The reports page is slow"})
	if err != nil {
		t.Fatal(err)
	}
	n := rec.Report.Number
	if r := rec.Report; r.CreatedBy == nil || *r.CreatedBy != (Actor{Name: "Alice", Kind: ActorPerson}) || r.LastBy == nil || r.LastBy.Name != "Alice" || r.CaseType() != TypeOther {
		t.Fatalf("submitted: %+v %+v %q", r.CreatedBy, r.LastBy, r.Type)
	}
	typ, inc := TypePerformance, "INC42"
	r, err := f.s.Triage(ctx, operator, n, Triage{Type: &typ, Incident: &inc})
	if err != nil || r.Type != TypePerformance || r.Incident != "INC42" || r.LastBy == nil || *r.LastBy != (Actor{Name: "Olga", Kind: ActorPerson}) || r.LastAt == nil {
		t.Fatalf("triaged: %+v %v", r, err)
	}
	lines, _ := f.s.Lines(ctx, operator, Filter{Type: TypePerformance})
	if len(lines) != 1 || lines[0].Incident != "INC42" || lines[0].LastBy == nil || lines[0].LastBy.Name != "Olga" {
		t.Fatalf("lines: %+v", lines)
	}
	if lines, _ := f.s.Lines(ctx, operator, Filter{Type: TypeSecurity}); len(lines) != 0 {
		t.Errorf("type=security: %+v", lines)
	}
	if lines, _ := f.s.Lines(ctx, operator, Filter{Query: "inc42"}); len(lines) != 1 {
		t.Errorf("the search: %+v", lines)
	}
	bad := "nonsense"
	if _, err := f.s.Triage(ctx, operator, n, Triage{Type: &bad}); err == nil {
		t.Error("a type that is none was taken")
	}
	// A timer's change is the system's.
	if err := f.s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.actions(), " "), ActionTriage) {
		t.Errorf("audit: %v", f.actions())
	}
}

// An incident number is 1 to 64 printable characters (CF-36).
func TestIncidentNumbersArePrintable(t *testing.T) {
	for in, ok := range map[string]bool{"INC0012345": true, " CHG-7 ": true, "": true, strings.Repeat("9", 64): true, strings.Repeat("9", 65): false, "INC\n1": false, "a\x07b": false} {
		if _, err := CleanIncident(in); (err == nil) != ok {
			t.Errorf("%q: %v", in, err)
		}
	}
	if !ValidType("security") || ValidType("Security") || ValidType("") {
		t.Error("ValidType")
	}
	if a := ActorOf(Person{ID: "system:servicenow", Name: "ServiceNow", System: true}); a.Kind != ActorSystem || a.Name != "ServiceNow" {
		t.Errorf("a token: %+v", a)
	}
	if a := AgentActor("Auditor"); a.Label() != "Auditor (agent)" {
		t.Errorf("an agent: %q", a.Label())
	}
}
