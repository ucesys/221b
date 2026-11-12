package ai

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// guide is Getting started with a step open or not.
type guide struct {
	open  atomic.Bool
	reads atomic.Int32
}

func (g *guide) Open(context.Context) bool { return g.open.Load() }

func (g *guide) Status(context.Context) string {
	g.reads.Add(1)
	return "Getting started, as the console is now: 4 of 9 steps done, 0 dismissed, 5 open.\n\nNext: step 5 of 9, Knowledge: connect Mycroft's sources. Open it with [Administration › Settings › Knowledge](#/admin/settings/knowledge)."
}

// The setup skill (GS-07): Sherlock's, for a SuperAdmin, while a step is
// open; its section follows the core and comes before the house
// instructions, and setup_status reads the guide.
func TestTheSetupSkillIsSherlocksWhileAStepIsOpen(t *testing.T) {
	w := setup(t, map[string]string{Sherlock: "Answer in English."})
	g := &guide{}
	g.open.Store(true)
	w.ai.SetSetup(g)
	q := sherlock.Question{Key: sherlock.Key{Cluster: "local"}, SuperAdmin: true}
	a, err := w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	skill := SetupSkill()
	if !strings.HasPrefix(skill, "Setting the console up.") || strings.Contains(skill, "<!--") {
		t.Fatalf("the skill's text: %q", skill)
	}
	i, j := strings.Index(a.Instructions, skill), strings.Index(a.Instructions, "Answer in English.")
	if i < 0 || j < i || !slices.Contains(a.Tools, sherlock.ToolSetupStatus) || a.Setup == nil {
		t.Fatalf("the skill is not on: tools %v", a.Tools)
	}
	if !strings.Contains(skill, "never ask for one in the chat") {
		t.Fatal("the skill does not keep secrets out of the chat")
	}
	for name, q := range map[string]sherlock.Question{"a person who is not a SuperAdmin": {Key: q.Key}} {
		a, _ := w.ai.Agent(Sherlock, q)
		if strings.Contains(a.Instructions, skill) || slices.Contains(a.Tools, sherlock.ToolSetupStatus) {
			t.Errorf("%s has the skill", name)
		}
	}
	if a, _ := w.ai.Agent(Troubleshooter, q); strings.Contains(a.Instructions, skill) || a.Setup != nil {
		t.Error("another agent has the skill")
	}
	// Asked what is next, it reads the guide and answers from it.
	res := w.engine.RunAgent(context.Background(), a, sherlock.Question{Key: q.Key, Cluster: "local", SuperAdmin: true}, "What's next?")
	if res.Err != nil || !slices.Contains(res.Tools, sherlock.ToolSetupStatus) || !strings.Contains(res.Text, "Knowledge: connect Mycroft's sources") || g.reads.Load() != 1 {
		t.Fatalf("%+v (read %d)", res, g.reads.Load())
	}
	// Every step done or dismissed: the skill leaves the instructions.
	g.open.Store(false)
	a, _ = w.ai.Agent(Sherlock, q)
	if strings.Contains(a.Instructions, skill) || slices.Contains(a.Tools, sherlock.ToolSetupStatus) || a.Setup != nil {
		t.Fatal("the skill stayed after setup ended")
	}
}

// Sherlock's shipped test cases of the skill pass against the fake
// model: what is next from the checklist, a token pasted into the chat
// refused and never repeated, and a choice explained from the knowledge
// base.
func TestTheSetupSkillsBaseCasesPass(t *testing.T) {
	w := setup(t, nil)
	ids := map[string]bool{}
	for _, c := range BaseTests() {
		if strings.HasPrefix(c.ID, "base-sherlock-setup-") {
			ids[c.ID] = true
		}
	}
	if len(ids) < 3 {
		t.Fatalf("the skill ships %d test cases", len(ids))
	}
	run, err := w.ai.RunTests(context.Background(), Sherlock, TestOptions{By: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range run.Results {
		if !ids[r.ID] {
			continue
		}
		seen++
		if !r.Passed {
			t.Errorf("%s failed: %v; answer %q", r.Name, r.Why, r.Answer)
		}
	}
	if seen != len(ids) {
		t.Fatalf("ran %d of the skill's %d cases", seen, len(ids))
	}
	for _, req := range w.fake.Requests() {
		if strings.Contains(string(req), `"name":"setup_status"`) && !strings.Contains(string(req), "Setting the console up.") {
			t.Fatal("setup_status was offered without the skill's instructions")
		}
	}
}
