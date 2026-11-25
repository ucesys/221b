package jobs

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/schedule"
)

// A run's steps are picked by Auto's rules from what started the run and
// what each step is about; the run keeps its provider, the highest effort
// and how it was picked, and the step that moved its model up (AI-45,
// AI-48). Its brief carries what the row shows.
func TestARunKeepsWhatItsStepsRanWithAI48(t *testing.T) {
	w := setup(t)
	var inputs []AutoStep
	w.agents.picked = func(req AgentRequest) AgentResult {
		inputs = append(inputs, *req.Auto)
		if req.Auto.Input.Production {
			r := AgentResult{Model: auto.Sonnet, Provider: "Anthropic", Effort: auto.Medium, PickBy: auto.ByRules, PickWhy: "Run now on a production cluster"}
			if req.Auto.Model == auto.Haiku && !req.Auto.MovedUp {
				r.MovedUpFrom = auto.Haiku
				r.PickWhy += "; moved up from Claude Haiku 5.5"
			}
			return r
		}
		return AgentResult{Model: auto.Haiku, Provider: "Anthropic", Effort: auto.Low, PickBy: auto.ByRules, PickWhy: "Run now"}
	}
	w.kinds.Register(Kind{ID: "two", Label: "Two clusters", Agent: "auditor", AgentName: "Auditor", Schedulable: true,
		Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			for _, c := range []string{"local", "prod", "prod"} {
				if _, err := env.Ask(ctx, c, "check"); err != nil {
					return Outcome{}, err
				}
			}
			return Outcome{Report: "done"}, nil
		}})
	on(t, w.s, "two", Change{})
	if _, err := w.s.RunNow(context.Background(), "two", Person{Email: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	if len(inputs) != 3 || inputs[0].Input.Trigger != TriggerManual || inputs[0].Input.CasePriority != -1 || inputs[0].Input.Production || inputs[0].Model != "" ||
		!inputs[1].Input.Production || inputs[1].Model != auto.Haiku || inputs[1].MovedUp || !inputs[2].MovedUp || inputs[2].Model != auto.Sonnet || inputs[0].Input.Kind != "two" {
		t.Fatalf("what the rules read: %+v", inputs)
	}
	list := runs(t, w.s)
	b := list[0]
	if b.Model != auto.Sonnet || b.Provider != "Anthropic" || b.Effort != auto.Medium || b.PickBy != auto.ByRules {
		t.Fatalf("brief: %+v", b)
	}
	r, err := w.s.GetRun(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.MovedUp == nil || r.MovedUp.Step != 2 || r.MovedUp.From != auto.Haiku || r.PickWhy != "Run now on a production cluster; moved up from Claude Haiku 5.5" {
		t.Fatalf("run: %+v %+v", r.MovedUp, r)
	}
}
