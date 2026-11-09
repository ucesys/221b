package ai

import (
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// The Cases skill (IB-10): Sherlock's while the person asks from the
// Cases area, after its core and before the house instructions; never
// another agent's, and gone when the question comes from elsewhere.
func TestTheCasesSkillIsSherlocksInCases(t *testing.T) {
	w := setup(t, map[string]string{Sherlock: "Answer in English."})
	skill := CasesSkill()
	if !strings.HasPrefix(skill, "The Cases area.") || strings.Contains(skill, "<!--") {
		t.Fatalf("the skill's text: %q", skill)
	}
	for _, want := range []string{"Proposed changes", "Jobs › Runs", "Exceptions", "You change nothing in Cases yourself", "who approved it and when"} {
		if !strings.Contains(skill, want) && !strings.Contains(strings.ToLower(skill), strings.ToLower(want)) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	q := sherlock.Question{Key: sherlock.Key{Cluster: "local"}, Cases: true}
	a, err := w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(a.Instructions, skill), strings.Index(a.Instructions, "Answer in English.")
	if i < 0 || j < i {
		t.Fatalf("the skill is not in Sherlock's core: %d %d", i, j)
	}
	if a, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: q.Key}); strings.Contains(a.Instructions, skill) {
		t.Error("asked from a cluster's tabs, Sherlock has the Cases skill")
	}
	if a, _ := w.ai.Agent(Troubleshooter, q); strings.Contains(a.Instructions, skill) {
		t.Error("another agent has the Cases skill")
	}
}
