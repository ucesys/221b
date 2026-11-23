package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// checkingAgents are agents that say, before a run by hand, whether each
// can answer (JB-25): off ones in the agents' words, broken ones with
// their provider's reason.
type checkingAgents struct {
	*fakeAgents
	off    map[string]string
	broken map[string]string
}

func (c *checkingAgents) Available(_ context.Context, agent string) error {
	if why, ok := c.off[agent]; ok {
		return AgentOff(why)
	}
	if why, ok := c.broken[agent]; ok {
		return errors.New(why)
	}
	return nil
}

// Run now of a job whose agent is off is refused before it starts, in a
// sentence that names the job, the agent and where to turn it on; no run
// is recorded and no agent is called. Rebuild now and a start by hand
// (a source added) are refused alike; a provider that cannot answer is
// refused with its own reason. The job's row says its agent is off,
// paused or on, and turning it on is allowed (JB-25).
func TestRunByHandIsRefusedWhileTheAgentIsOff(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	w.kinds.Register(Kind{ID: "wiki-sync-a", Label: "Wiki sync: A", Agent: "mycroft", AgentName: "Mycroft", Schedulable: true, Rebuild: true,
		Run: func(context.Context, *RunEnv) (Outcome, error) { return Outcome{}, nil }})
	agents := &checkingAgents{fakeAgents: w.agents, off: map[string]string{agentSummarizer: "the Summarizer is off", "mycroft": "the Mycroft is off"}, broken: map[string]string{}}
	w.s.SetAgents(agents)
	ctx := context.Background()
	bob := Person{Email: "bob"}

	_, err := w.s.RunNow(ctx, KindDigest, bob)
	if !errors.Is(err, ErrNoAgent) || err.Error() != "The Daily digest runs on the Summarizer, which is off: turn it on in Administration › AI › Agents." {
		t.Fatalf("Run now with the Summarizer off: %v", err)
	}
	rec := httptest.NewRecorder()
	failErr(rec, err)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "which is off: turn it on in Administration › AI › Agents.") {
		t.Fatalf("the API's answer: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := w.s.RebuildNow(ctx, "wiki-sync-a", bob); !errors.Is(err, ErrNoAgent) || !strings.HasPrefix(err.Error(), "The Wiki sync: A runs on the Mycroft, which is off") {
		t.Fatalf("Rebuild now with Mycroft off: %v", err)
	}
	if _, err := w.s.Start(ctx, "wiki-sync-a", "Source added by ann", bob); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("a start by hand with Mycroft off: %v", err)
	}
	agents.broken["auditor"] = `the Auditor's provider "gone" is gone; pick another in Administration › AI › Agents`
	if _, err := w.s.RunNow(ctx, "test", bob); !errors.Is(err, ErrNoAgent) || err.Error() != `The Test job runs on the Auditor, which cannot answer now: the Auditor's provider "gone" is gone; pick another in Administration › AI › Agents.` {
		t.Fatalf("Run now with the provider gone: %v", err)
	}
	w.s.Wait()
	if n := len(runs(t, w.s)); n != 0 || w.agents.n() != 0 {
		t.Fatalf("a refused run is recorded or calls an agent: %d runs, %d calls", n, w.agents.n())
	}

	v, err := w.s.Get(ctx, KindDigest)
	if err != nil {
		t.Fatal(err)
	}
	if v.AgentOff != "the Summarizer is off" || v.Unusable != "" {
		t.Fatalf("the paused digest's view: agentOff %q, unusable %q", v.AgentOff, v.Unusable)
	}
	if v = on(t, w.s, KindDigest, Change{}); !v.On || v.AgentOff != "the Summarizer is off" {
		t.Fatalf("turning it on while its agent is off: %+v", v)
	}
	if v, _ = w.s.Get(ctx, KindAudit); v.AgentOff != "" {
		t.Fatalf("the audit's agent is on: %q", v.AgentOff)
	}

	// Turned on, the agent runs it.
	delete(agents.off, agentSummarizer)
	if v, _ = w.s.Get(ctx, KindDigest); v.AgentOff != "" {
		t.Fatalf("the Summarizer on: %q", v.AgentOff)
	}
	delete(agents.broken, "auditor")
	if _, err := w.s.RunNow(ctx, "test", bob); err != nil {
		t.Fatalf("Run now with its agent on: %v", err)
	}
	w.s.Wait()
	if n := len(runs(t, w.s)); n != 1 {
		t.Fatalf("runs: %d", n)
	}
}

// A scheduled run of a job whose agent is off is not refused: nobody is
// at the screen, so it runs and fails, recorded with the note (JB-25).
func TestAScheduledRunOfAnAgentOffFailsWithTheNote(t *testing.T) {
	w := setup(t)
	agents := &checkingAgents{fakeAgents: w.agents, off: map[string]string{"auditor": "the Auditor is off"}}
	w.s.SetAgents(agents)
	w.agents.err = errors.New("the Auditor is off")
	on(t, w.s, "test", Change{})
	w.clock.Set(day1.Add(2*time.Hour + 10*time.Minute))
	w.s.Tick(context.Background())
	w.s.Wait()
	rs := runs(t, w.s)
	if len(rs) != 1 || rs[0].Status != StatusFailed || !strings.Contains(rs[0].Note, "the Auditor is off") {
		t.Fatalf("the scheduled run: %+v", rs)
	}
}
