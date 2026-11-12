package jobs

import (
	"context"
	"strings"
	"testing"
)

// A Wiki sync job per source (KN-28): a kind of one job, taken off the
// registry with its source; an event for one kind starts only its job; a
// run counts what its kind spent by itself and keeps its facts; the
// console pauses and resumes a job, starts one with its own words, and
// lets one go with its runs kept.
func TestOneJobPerSourceKN28(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	ran := map[string]int{}
	kind := func(id string) Kind {
		return Kind{ID: id, Label: "Wiki sync: " + id, Agent: "mycroft", AgentName: "Mycroft", On: true, Single: true, NoFindings: true, Schedulable: true,
			Events: []string{EventSource}, Default: When{Event: EventSource}, Budget: Budget{RunUSD: 1},
			Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
				w.runs.Add(1)
				ran[id]++
				if err := env.Spend("claude-test", Tokens{In: 900, Out: 100}, 0.4); err != nil {
					t.Errorf("the first spend is within the budget: %v", err)
				}
				return Outcome{Report: "Read.", Facts: []Fact{{Label: "Items read", Text: "2 changed items"}, {Label: "Pages drafted", Text: "1 waits", Href: "#/aiops/inbox/knowledge/r-1"}, {Label: "Bad", Text: "x", Href: "https://elsewhere"}}}, nil
			}}
	}
	w.kinds.Register(kind("wiki-sync-a"))
	w.kinds.Register(kind("wiki-sync-b"))
	if k, _ := w.kinds.Kind("wiki-sync-a"); k.AddMore() {
		t.Fatal("a source's job is its kind's only one")
	}
	if _, err := w.s.Add(ctx, "wiki-sync-a", Person{Email: "alice@example.com"}); err == nil {
		t.Fatal("another job of a source's kind was added")
	}

	// An event for one kind starts its job alone.
	if n := w.s.Fire(Event{Kind: EventSource, JobKind: "wiki-sync-b", Label: "Upload of a.md by Ann"}); n != 1 {
		t.Fatalf("started %d jobs, want 1", n)
	}
	w.s.Wait()
	if ran["wiki-sync-a"] != 0 || ran["wiki-sync-b"] != 1 {
		t.Fatalf("runs: %v", ran)
	}
	runs, _, _ := w.s.Runs(ctx, RunFilter{Job: "wiki-sync-b"})
	r, err := w.s.GetRun(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Trigger.Kind != TriggerEvent || r.Trigger.What != "Upload of a.md by Ann" || !r.NoFindings || r.CostUSD != 0.4 || r.Tokens.In != 900 || r.Model != "claude-test" {
		t.Fatalf("the run: %+v", r)
	}
	if h, _ := r.Health(); h != "" {
		t.Fatalf("a run of a kind without findings has no health: %q", h)
	}
	if len(r.Facts) != 3 || r.Facts[1].Href != "#/aiops/inbox/knowledge/r-1" || r.Facts[2].Href != "" {
		t.Fatalf("the facts: %+v", r.Facts)
	}

	// Started by the console with its own words, as a person asked.
	b, err := w.s.Start(ctx, "wiki-sync-a", "Source added by Ann", Person{Name: "Ann"})
	if err != nil || b.Trigger.What != "Source added by Ann" || b.Trigger.By != "Ann" {
		t.Fatalf("start: %+v %v", b, err)
	}
	w.s.Wait()

	// Paused and resumed for its source, audited with why.
	if err := w.s.SetOn(ctx, "wiki-sync-a", false, Person{Name: "Ann"}, "its source switched off by Ann"); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.s.Get(ctx, "wiki-sync-a"); v.On || v.NextText != "Paused" {
		t.Fatalf("paused: %+v", v)
	}
	if n := w.s.Fire(Event{Kind: EventSource, JobKind: "wiki-sync-a"}); n != 0 {
		t.Fatal("a paused job took an event")
	}
	if !strings.Contains(w.audit.String(), "its source switched off by Ann") {
		t.Fatalf("not audited: %s", w.audit.String())
	}
	if err := w.s.SetOn(ctx, "wiki-sync-a", true, Person{Name: "Ann"}, "its source switched on by Ann"); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.s.Get(ctx, "wiki-sync-a"); !v.On {
		t.Fatal("not resumed")
	}

	// Its source removed: the kind leaves the registry, its job's
	// settings are let go, its runs stay. Dropping again finds nothing.
	w.kinds.Unregister("wiki-sync-a")
	if dropped, err := w.s.Drop(ctx, "wiki-sync-a", Person{Name: "Ann"}, "its source a was removed"); err != nil || !dropped {
		t.Fatalf("drop: %v %v", dropped, err)
	}
	if dropped, err := w.s.Drop(ctx, "wiki-sync-a", Person{Name: "Ann"}, "again"); err != nil || dropped {
		t.Fatalf("dropped twice: %v %v", dropped, err)
	}
	if _, err := w.s.Get(ctx, "wiki-sync-a"); err == nil {
		t.Fatal("the removed source's job is still listed")
	}
	if runs, _, _ := w.s.Runs(ctx, RunFilter{Job: "wiki-sync-a"}); len(runs) != 1 {
		t.Fatalf("its runs: %d", len(runs))
	}
}

// Spend stops a kind that calls its agent by itself once the run's budget
// is spent (JB-10).
func TestSpendSaysWhenTheBudgetIsSpent(t *testing.T) {
	env := &RunEnv{Job: Job{Budget: Budget{RunUSD: 1}}}
	if err := env.Spend("m", Tokens{In: 1}, 0.6); err != nil {
		t.Fatal(err)
	}
	if err := env.Spend("m", Tokens{In: 1}, 0.6); err == nil || !strings.Contains(err.Error(), "the run's budget of $1.00 is spent") {
		t.Fatalf("err %v", err)
	}
	if env.Budget() == nil || env.Cost() != 1.2 {
		t.Fatalf("cost %v", env.Cost())
	}
}

// A kind whose answers reach reporters (the report triage) says so on
// each call of its agent, so ask_knowledge gives it no exception; any
// other kind's calls do not (KN-32).
func TestAKindForReportersSaysSoOnItsCallsKN32(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	ask := func(ctx context.Context, env *RunEnv) (Outcome, error) {
		_, err := env.Ask(ctx, env.Clusters[0], "triage")
		return Outcome{Report: "Done."}, err
	}
	w.kinds.Register(Kind{ID: "triage-test", Label: "Triage", Agent: "troubleshooter", AgentName: "Troubleshooter", On: true, ForReporters: true, Schedulable: true, Run: ask})
	w.kinds.Register(Kind{ID: "audit-test", Label: "Audit", Agent: "auditor", AgentName: "Auditor", On: true, Schedulable: true, Run: ask})
	for _, id := range []string{"triage-test", "audit-test"} {
		if _, err := w.s.Start(ctx, id, "Run now", Person{Name: "Ann"}); err != nil {
			t.Fatal(err)
		}
		w.s.Wait()
	}
	w.agents.mu.Lock()
	defer w.agents.mu.Unlock()
	if len(w.agents.calls) != 2 || !w.agents.calls[0].ForReporters || w.agents.calls[1].ForReporters {
		t.Fatalf("the calls: %+v", w.agents.calls)
	}
}
