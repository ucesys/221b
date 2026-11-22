package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// An agent's effort and Auto are validated and kept (AI-43, AI-44, AI-47):
// the effort one of the levels or auto, the model auto only on a provider
// of kind Anthropic, the floor at most high and one of Auto's models.
func TestTheEffortAndAutoSettingsAI43(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "gw", Name: "GW", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gw.example/v1"}, Model: "qwen"}, true); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []AgentSettings{
		{Effort: "extreme"},
		{Effort: auto.Auto, FloorEffort: auto.XHigh},
		{Effort: auto.Auto, FloorModel: "claude-fable-5-1"},
		{Provider: "gw", Model: auto.Auto},
	} {
		if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	// Effort Auto on the OpenAI-compatible provider is fine: its thinking.
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{Provider: "gw", Effort: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	set := AgentSettings{Effort: auto.Auto, Model: auto.Auto, FloorEffort: auto.Medium, FloorModel: auto.Sonnet}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: set}); err != nil {
		t.Fatal(err)
	}
	for _, a := range w.ai.View(ctx).Agents {
		if a.ID == Auditor && (a.Model != auto.Auto || a.Settings.Effort != auto.Auto || a.Settings.FloorEffort != auto.Medium || a.Settings.FloorModel != auto.Sonnet) {
			t.Fatalf("the auditor's view: %+v", a)
		}
	}
	a, err := w.ai.Agent(Auditor, sherlock.Question{})
	if err != nil || a.Auto == nil || a.Model != auto.Sonnet || a.Effort != "" {
		t.Fatalf("resolved: %+v %v", a, err)
	}
	// A fixed effort is sent as set, with no Auto.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Effort: auto.High}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := w.ai.Agent(Auditor, sherlock.Question{}); a.Auto != nil || a.Effort != auto.High || a.Model != "claude-opus-5-5" {
		t.Fatalf("fixed: %+v", a)
	}
}

// a5000 is an OpenAI-compatible provider on the fake's in-house endpoint,
// tested, made the classifier's.
func a5000(t *testing.T, w *world, endpoint string) {
	t.Helper()
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "a5000", Name: "A5000", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: endpoint}, Model: "qwen3.8-27b", PriceIn: 0.01, PriceOut: 0.01}, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.RecordTest(ctx, "a5000", "qwen3.8-27b", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SetClassifier(ctx, "a5000"); err != nil {
		t.Fatal(err)
	}
}

// Sherlock left to Auto is picked by the classifier: the model at the
// conversation's start, the effort for each message; the classifier's
// provider is chosen in Administration › AI and only an OpenAI-compatible
// one may be (AI-45).
func TestTheClassifierPicksAConversationAI45(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	if err := w.ai.SetClassifier(ctx, DefaultProvider); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the default provider as the classifier: %v", err)
	}
	a5000(t, w, srv.URL+"/open/v1")
	if v := w.ai.View(ctx); v.Classifier != "a5000" || v.ClassifierOff != "" || len(v.AutoModels) != 3 {
		t.Fatalf("view: %q %q %v", v.Classifier, v.ClassifierOff, v.AutoModels)
	}
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	k := sherlock.Key{Session: "s", Cluster: "local"}
	c, err := w.engine.Ask(sherlock.Question{Key: k, Text: "What is the status of pod web/api-0?", Cluster: "local"})
	if err != nil {
		t.Fatal(err)
	}
	w.engine.Wait()
	m := c.View().Messages[1]
	if m.Model != auto.Haiku || m.Effort != auto.Low || m.PickBy != auto.ByClassifier || m.PickCostUSD <= 0 || m.Status != "done" {
		t.Fatalf("a lookup: %+v", m)
	}
	if _, err := w.engine.Ask(sherlock.Question{Key: k, Text: "Now there is an outage: find the root cause", Cluster: "local"}); err != nil {
		t.Fatal(err)
	}
	w.engine.Wait()
	m = c.View().Messages[3]
	if m.Model != auto.Opus || m.Effort != auto.High || m.MovedUpFrom != auto.Haiku {
		t.Fatalf("an outage: %+v", m)
	}
	// The classifier's request is the constrained one.
	var got struct {
		StructuredOutputs struct{ Choice []string } `json:"structured_outputs"`
		MaxTokens         int                       `json:"max_tokens"`
	}
	reqs := fake.Requests()
	_ = json.Unmarshal(reqs[len(reqs)-1], &got)
	if len(got.StructuredOutputs.Choice) != 3 || got.MaxTokens != 5 {
		t.Fatalf("the classifier's request: %s", reqs[len(reqs)-1])
	}
	// The provider removed, the classifier is off.
	if err := w.ai.SetClassifier(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if v := w.ai.View(ctx); v.Classifier != "" {
		t.Fatalf("off: %q", v.Classifier)
	}
}

// A classifier slower than 1.5 seconds is not waited for: the agent's
// fixed defaults apply, and the answer says why (AI-45).
func TestASlowClassifierFallsBackAI45(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	fake := fakeclaude.New()
	fake.Delay = 0
	slow := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
			return
		}
		fake.ServeHTTP(rw, r)
	}))
	t.Cleanup(slow.Close)
	a5000(t, w, slow.URL+"/open/v1")
	if err := w.ai.SaveAgent(ctx, Sherlock, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c, err := w.engine.Ask(sherlock.Question{Key: sherlock.Key{Session: "s", Cluster: "local"}, Text: "What is the status of pod web/api-0?", Cluster: "local"})
	if err != nil {
		t.Fatal(err)
	}
	w.engine.Wait()
	m := c.View().Messages[1]
	if took := time.Since(start); took > 2900*time.Millisecond {
		t.Fatalf("waited %v for the classifier", took)
	}
	if m.Model != "claude-opus-5-5" || m.Effort != auto.DefaultEffort || m.PickBy != auto.ByFixed || !strings.Contains(m.PickWhy, "did not answer in 1.5 s") {
		t.Fatalf("%+v", m)
	}
}

// A job's run is picked by the rules alone: the first step sets the model,
// a later step that wants a stronger one moves it up once (AI-45).
func TestPickRunAppliesTheRulesAI45(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Effort: auto.Auto, Model: auto.Auto}}); err != nil {
		t.Fatal(err)
	}
	a, _ := w.ai.Agent(Auditor, sherlock.Question{})
	p, moved := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1}, "", false)
	if p.Model != auto.Haiku || p.Effort != auto.Low || moved != "" || a.Model != auto.Haiku || a.Auto != nil || a.PickBy != auto.ByRules || a.Price.ID != auto.Haiku {
		t.Fatalf("first step: %+v %+v", p, a)
	}
	a, _ = w.ai.Agent(Auditor, sherlock.Question{})
	p, moved = w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "schedule", Kind: "daily-digest", CasePriority: -1, Production: true}, auto.Haiku, false)
	if p.Model != auto.Sonnet || moved != auto.Haiku || !strings.Contains(p.Why, "moved up from Claude Haiku 5.5") {
		t.Fatalf("a production step: %+v %q", p, moved)
	}
	a, _ = w.ai.Agent(Auditor, sherlock.Question{})
	if p, moved = w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 0}, auto.Sonnet, true); p.Model != auto.Sonnet || moved != "" || p.Effort != auto.XHigh {
		t.Fatalf("moved once already: %+v %q", p, moved)
	}
	// An agent set by hand is left as it is.
	a, _ = w.ai.Agent(Troubleshooter, sherlock.Question{})
	if p, _ := w.ai.PickRun(ctx, &a, auto.RunInput{Trigger: "event", Event: "case", CasePriority: 0}, "", false); p.By != "" || a.Model != "claude-opus-5-5" {
		t.Fatalf("fixed: %+v", p)
	}
}

// The test dialog's run takes an effort and a model to test at (AI-48,
// for measuring the floor): the cases run with them, graded on the
// agent's own model, and the run is not kept as the draft's test.
func TestATestRunAtAnEffortAndModelAI48(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	for _, bad := range []TestOptions{{Effort: "auto"}, {Effort: "extreme"}, {Model: "not a model"}} {
		if err := bad.CheckOverride(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Be brief.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	w.fake.Reset()
	run, err := w.ai.RunTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann", Effort: auto.Low, Model: auto.Haiku})
	if err != nil || run.Cases == 0 {
		t.Fatalf("%+v %v", run, err)
	}
	tested, graded := 0, 0
	for _, raw := range w.fake.Requests() {
		var r sherlock.Request
		_ = json.Unmarshal(raw, &r)
		switch {
		case r.Model == auto.Haiku && r.OutputConfig != nil && r.OutputConfig.Effort == auto.Low:
			tested++
		case r.Model == "claude-opus-5-5" && r.OutputConfig == nil:
			graded++
		default:
			t.Fatalf("a request at neither: %s %+v", r.Model, r.OutputConfig)
		}
	}
	if tested == 0 {
		t.Fatal("no case ran at the effort and model given")
	}
	if v, _ := w.ai.Instructions(ctx, Troubleshooter); v.Draft.Test != nil {
		t.Fatalf("a measuring run was kept as the draft's test: %+v", v.Draft.Test)
	}
	_ = graded
}
