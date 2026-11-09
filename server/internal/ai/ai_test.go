package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// fakeSettings is the console's settings in memory.
type fakeSettings struct {
	mu sync.Mutex
	sh settings.Sherlock
}

func (f *fakeSettings) Current() settings.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	sh := f.sh
	return settings.State{Settings: settings.Settings{Sherlock: &sh}}
}

func (f *fakeSettings) UpdateSherlock(_ context.Context, sh settings.Sherlock) (settings.State, error) {
	if err := sh.Validate(); err != nil {
		return settings.State{}, err
	}
	f.mu.Lock()
	f.sh = sh
	f.mu.Unlock()
	return f.Current(), nil
}

type world struct {
	ai     *Service
	engine *sherlock.Service
	fake   *fakeclaude.Fake
	set    *fakeSettings
	kv     *kv.Memory
	audit  *bytes.Buffer
}

// setup is a console with the agents over a fake Claude API, Sherlock on
// for the local cluster, and a key saved.
func setup(t *testing.T, baselines map[string]string) *world {
	t.Helper()
	fake := fakeclaude.New()
	fake.Delay = 0
	api := httptest.NewServer(fake)
	t.Cleanup(api.Close)
	w := &world{fake: fake, kv: &kv.Memory{}, audit: &bytes.Buffer{}}
	w.set = &fakeSettings{sh: settings.Sherlock{Model: "claude-opus-5-5", DailyUSD: 20, MonthlyUSD: 200, Clusters: map[string]bool{"local": true}}}
	keys := sherlock.NewKeys(nil, "", nil)
	if err := keys.Set(context.Background(), "sk-ant-test-key-0123456789", "test"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w.ai = New(Options{Backend: w.kv, Settings: w.set, Keys: keys, DefaultURL: api.URL, Baselines: baselines, Logger: logger})
	w.engine = sherlock.New(sherlock.Options{
		Agent: w.ai.Agent, OnAnswer: w.ai.OnAnswer,
		Settings: func() sherlock.Settings {
			sh := w.set.Current().SherlockOrDefault()
			return sherlock.Settings{Model: sh.Model, Caps: sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}}
		},
		Audit:  audit.New(audit.Options{Out: w.audit}),
		Logger: logger,
	})
	w.ai.SetEngine(w.engine)
	t.Cleanup(w.engine.Wait)
	return w
}

func (w *world) run(t *testing.T, id, task string) sherlock.RunResult {
	t.Helper()
	a, err := w.ai.Agent(id, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	return w.engine.RunAgent(context.Background(), a, sherlock.Question{Key: sherlock.Key{Cluster: "local"}, Cluster: "local"}, task)
}

// The default provider is Sherlock's from before, with its key and model;
// another provider is added with a credential of its own, and an agent
// moved to it uses its model. One no agent uses can go; one in use cannot.
func TestProvidersAndAgents(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	v := w.ai.View(ctx)
	if len(v.Providers) != 1 || v.Providers[0].ID != DefaultProvider || v.Providers[0].Name != "Anthropic" || v.Providers[0].Kind != sherlock.KindAnthropic || !v.Providers[0].Key.Set || v.DefaultModel != "claude-opus-5-5" {
		t.Fatalf("providers: %+v", v.Providers)
	}
	for _, a := range v.Agents {
		if a.ProviderID != DefaultProvider || a.Model != "claude-opus-5-5" || !a.DefaultModel {
			t.Fatalf("an agent does not start on the default: %+v", a)
		}
		if a.Enabled != (a.ID != Summarizer) {
			t.Fatalf("%s on %v", a.ID, a.Enabled)
		}
	}
	second := Provider{ID: "gateway", Name: "Gateway", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gateway.example/v1"}, Model: "gpt-5", PriceIn: 1, PriceOut: 4}
	if err := w.ai.SaveProvider(ctx, second, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SaveProvider(ctx, second, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("added twice: %v", err)
	}
	if err := w.ai.SaveProvider(ctx, Provider{ID: "Bad ID", Name: "x", Provider: sherlock.Provider{Kind: "anthropic"}}, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad id: %v", err)
	}
	// Without its credential, an agent on it cannot answer.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gateway"}}); err != nil {
		t.Fatal(err)
	}
	if why := w.ai.Usable(Auditor, "local"); !strings.Contains(why, "no credential") {
		t.Fatalf("usable without a key: %q", why)
	}
	if err := w.ai.KeysFor("gateway").Set(ctx, "gw-token-0123456789", "test"); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Auditor, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	if err != nil || a.Model != "gpt-5" || a.Price.In != 1 || a.Price.Out != 4 {
		t.Fatalf("the auditor on the gateway: %+v %v", a, err)
	}
	if !strings.Contains(strings.Join(a.Secrets, " "), "gw-token-0123456789") || !strings.Contains(strings.Join(a.Secrets, " "), "sk-ant-test-key-0123456789") {
		t.Fatal("an agent's secrets leave out a provider's credential")
	}
	if err := w.ai.DeleteProvider(ctx, "gateway"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a provider in use was removed: %v", err)
	}
	// Tools are a subset of the purpose's, never more.
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Tools: []string{"review_cluster", "pod_logs"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a tool outside the purpose: %v", err)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Tools: []string{"review_cluster"}, Model: "claude-haiku-5-5", DailyUSD: 2, MonthlyUSD: 20, Clusters: []string{"prod"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.Agent(Auditor, sherlock.Question{Key: sherlock.Key{Cluster: "local"}}); err == nil || !strings.Contains(err.Error(), "does not work on this cluster") {
		t.Fatalf("an agent off its clusters: %v", err)
	}
	a, err = w.ai.Agent(Auditor, sherlock.Question{Key: sherlock.Key{Cluster: "prod"}})
	if err != nil || a.Model != "claude-haiku-5-5" || len(a.Tools) != 1 || a.Caps.DailyUSD != 2 {
		t.Fatalf("the auditor: %+v %v", a, err)
	}
	if err := w.ai.DeleteProvider(ctx, "gateway"); err != nil {
		t.Fatal(err)
	}
	if w.ai.KeysFor("gateway").Info().Set {
		t.Fatal("the credential stayed with its provider gone")
	}
	// An agent that is off is not on anyone's roster.
	off := false
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{On: &off}}); err != nil {
		t.Fatal(err)
	}
	s, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	for _, r := range s.Roster {
		if r.ID == Troubleshooter || r.ID == Sherlock || r.ID == Summarizer {
			t.Fatalf("roster: %+v", s.Roster)
		}
	}
	if !strings.Contains(s.Instructions, "- Change planner (changePlanner): ") || strings.Contains(s.Instructions, "(troubleshooter)") {
		t.Fatalf("the roster in the prompt:\n%s", s.Instructions)
	}
}

// The Script reviewer gives a second opinion from another provider: on
// the author's, it is refused until a SuperAdmin confirms the console has
// only one.
func TestTheReviewerWantsAnotherProvider(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveAgent(ctx, ScriptReviewer, AgentChange{AgentSettings: AgentSettings{Model: "claude-sonnet-5-5"}}); !errors.Is(err, ErrSameProvider) {
		t.Fatalf("saved on the author's provider: %v", err)
	}
	if err := w.ai.SaveAgent(ctx, ScriptReviewer, AgentChange{AgentSettings: AgentSettings{Model: "claude-sonnet-5-5"}, ConfirmSingleProvider: true}); err != nil {
		t.Fatal(err)
	}
	if !w.ai.View(ctx).SingleProvider {
		t.Fatal("the confirmation was not kept")
	}
	// Once confirmed, later saves need no confirmation.
	if err := w.ai.SaveAgent(ctx, ScriptReviewer, AgentChange{AgentSettings: AgentSettings{Model: "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
}

// Instructions come in layers: the fixed core, the agents it may hand
// work to, the house instructions, then in the cluster's block the notes
// for every cluster and the cluster's own.
func TestThePromptIsCoreThenHouseThenCluster(t *testing.T) {
	w := setup(t, map[string]string{Troubleshooter: "Cite our runbooks."})
	ctx := context.Background()
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Cite our runbooks by name.", map[string]string{AllClusters: "Every cluster runs RKE2.", "local": "Maintenance on Sundays."}, "ann"); err != nil {
		t.Fatal(err)
	}
	v, _ := w.ai.Instructions(ctx, Troubleshooter)
	if _, err := w.ai.ApplyLive(ctx, Troubleshooter, v.Draft.Rev, "sam"); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Troubleshooter, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	core, cluster := sherlock.AgentPromptText(a, sherlock.Question{Cluster: "local", Version: "v1.37.1"}, time.Now())
	iCore, iRules, iRoster, iHouse := strings.Index(core, "You are the Troubleshooter"), strings.Index(core, "Rules every agent"), strings.Index(core, "hand work to with hand_over"), strings.Index(core, "Cite our runbooks by name.")
	if iCore != 0 || iRules < iCore || iRoster < iRules || iHouse < iRoster {
		t.Fatalf("the order: core %d, rules %d, roster %d, house %d\n%s", iCore, iRules, iRoster, iHouse, core)
	}
	if !strings.Contains(core, sherlock.ClusterTextRule) || !strings.Contains(core, "Secrets never leave the console") {
		t.Fatal("the core lost a rule")
	}
	if strings.Contains(core, "Every cluster runs RKE2.") || !strings.Contains(cluster, "Every cluster runs RKE2.\n\nMaintenance on Sundays.") || strings.Index(cluster, "Kubernetes v1.37.1") > strings.Index(cluster, "RKE2") {
		t.Fatalf("the cluster's block:\n%s", cluster)
	}
	b, _ := w.ai.Agent(Troubleshooter, sherlock.Question{Key: sherlock.Key{Cluster: "prod"}})
	if strings.Contains(b.Notes, "Sundays") || !strings.Contains(b.Notes, "RKE2") {
		t.Fatalf("another cluster's notes: %q", b.Notes)
	}
	// Sherlock's core keeps its built-in instructions, with the front
	// desk's part; only Sherlock is told it may change the view.
	s, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}})
	if !strings.HasPrefix(s.Instructions, sherlock.BuiltIn(true)) || !strings.Contains(s.Instructions, "front desk") {
		t.Fatal("Sherlock's core")
	}
	if strings.Contains(a.Instructions, "The view_* tools") {
		t.Fatal("another agent was told it may change the view")
	}
}

// An edit in the console overrides the chart's baseline; an upgrade with
// a new baseline is flagged and replaces nothing until a SuperAdmin keeps
// the edit or takes the baseline; an edit equal to the baseline is
// dropped, back in sync.
func TestBaselineAndOverride(t *testing.T) {
	w := setup(t, map[string]string{Auditor: "Weigh PCI findings first."})
	ctx := context.Background()
	state := func(s *Service) string {
		v, err := s.Instructions(ctx, Auditor)
		if err != nil {
			t.Fatal(err)
		}
		return v.State + ":" + v.House
	}
	if got := state(w.ai); got != InSync+":Weigh PCI findings first." {
		t.Fatal(got)
	}
	if _, err := w.ai.SaveDraft(ctx, Auditor, "Weigh PCI and SOC 2 findings first.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if got := state(w.ai); got != InSync+":Weigh PCI findings first." {
		t.Fatalf("a draft changed what is in force: %s", got)
	}
	if _, err := w.ai.ReadyForLive(ctx, Auditor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an untested draft may go live: %v", err)
	}
	if _, err := w.ai.ApplyLive(ctx, Auditor, 1, "sam"); err != nil {
		t.Fatal(err)
	}
	if got := state(w.ai); got != Edited+":Weigh PCI and SOC 2 findings first." {
		t.Fatal(got)
	}
	// The upgrade: the same storage, a new baseline.
	up := func(baseline string) *Service {
		return New(Options{Backend: w.kv, Settings: w.set, Baselines: map[string]string{Auditor: baseline}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}
	s2 := up("Weigh PCI and HIPAA findings first.")
	if got := state(s2); got != BaselineChanged+":Weigh PCI and SOC 2 findings first." {
		t.Fatalf("after the upgrade: %s", got)
	}
	// Both sides show: the baseline the edit was made over, and the new one.
	if v, _ := s2.Instructions(ctx, Auditor); v.PreviousBaseline != "Weigh PCI findings first." || v.Baseline != "Weigh PCI and HIPAA findings first." {
		t.Fatalf("%+v", v)
	}
	if err := s2.ResolveBaseline(ctx, Auditor, KeepEdit, "ann"); err != nil {
		t.Fatal(err)
	}
	if got := state(s2); got != Edited+":Weigh PCI and SOC 2 findings first." {
		t.Fatalf("kept: %s", got)
	}
	s3 := up("Weigh every finding by severity.")
	if err := s3.ResolveBaseline(ctx, Auditor, TakeBaseline, "ann"); err != nil {
		t.Fatal(err)
	}
	if got := state(s3); got != InSync+":Weigh every finding by severity." {
		t.Fatalf("taken: %s", got)
	}
	// An edit the chart catches up with is dropped at start.
	if _, err := s3.SaveDraft(ctx, Auditor, "Weigh PCI first.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s3.Instructions(ctx, Auditor); v.Draft == nil || v.Draft.Rev != 2 {
		t.Fatalf("a draft's revision repeats: %+v", v.Draft)
	}
	if _, err := s3.ApplyLive(ctx, Auditor, 2, "sam"); err != nil {
		t.Fatal(err)
	}
	s4 := up("Weigh PCI first.")
	if err := s4.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	var ps Prompts
	b, _, _ := w.kv.LoadKey(ctx, PromptsKey)
	_ = json.Unmarshal(b, &ps)
	if ps.Agents[Auditor].House != nil || state(s4) != InSync+":Weigh PCI first." {
		t.Fatalf("the edit equal to the baseline stayed: %+v", ps.Agents[Auditor])
	}
}

// Each live version is kept; one restored becomes the draft, which may go
// live again without a new test.
func TestVersionsRestore(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	for i, text := range []string{"First.", "Second."} {
		if _, err := w.ai.SaveDraft(ctx, Mycroft, text, nil, "ann"); err != nil {
			t.Fatal(err)
		}
		v, _ := w.ai.Instructions(ctx, Mycroft)
		if got, err := w.ai.ApplyLive(ctx, Mycroft, v.Draft.Rev, "sam"); err != nil || got.N != i+1 {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if err := w.ai.Restore(ctx, Mycroft, 1, "ann"); err != nil {
		t.Fatal(err)
	}
	rev, err := w.ai.ReadyForLive(ctx, Mycroft)
	if err != nil {
		t.Fatalf("a restored version needs a test: %v", err)
	}
	got, err := w.ai.ApplyLive(ctx, Mycroft, rev, "sam")
	if err != nil || got.N != 3 || got.House != "First." || got.Why != "restored version 1" {
		t.Fatalf("%+v %v", got, err)
	}
	v, _ := w.ai.Instructions(ctx, Mycroft)
	if v.House != "First." || v.Live != 3 || len(v.Versions) != 3 || v.Versions[0].N != 3 || v.Draft != nil {
		t.Fatalf("%+v", v)
	}
	if err := w.ai.Restore(ctx, Mycroft, 99, "ann"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restored a version never kept: %v", err)
	}
	// A draft saved again after the approval was asked for is refused.
	if _, err := w.ai.SaveDraft(ctx, Mycroft, "Third.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.ApplyLive(ctx, Mycroft, 1, "sam"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a stale approval applied: %v", err)
	}
}

// A hand-over asks another agent, which works with its own tools and
// budget; a chain goes at most three deep and never asks one agent twice;
// each call is audited.
func TestHandOverLimits(t *testing.T) {
	w := setup(t, nil)
	res := w.run(t, Sherlock, "[[chain troubleshooter,auditor,hostDoctor,mycroft]]")
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if strings.Join(res.HandOvers, ",") != "troubleshooter,auditor,hostDoctor" {
		t.Fatalf("the chain: %v", res.HandOvers)
	}
	deepest := res.Hands[0].Hands[0].Hands[0]
	if deepest.To != HostDoctor || len(deepest.Hands) != 0 || deepest.Status != "done" {
		t.Fatalf("the third: %+v", deepest)
	}
	if !strings.Contains(deepest.Text, "handed on at most 3 times") {
		t.Fatalf("the fourth was not refused: %q", deepest.Text)
	}
	if res.Hands[0].FromName != "Sherlock" || res.Hands[0].ToName != "Troubleshooter" || res.Hands[0].Model == "" || res.Hands[0].CostUSD <= 0 {
		t.Fatalf("the first hand: %+v", res.Hands[0])
	}
	spend := w.engine.Ledger().Now(context.Background())
	for _, id := range []string{Sherlock, Troubleshooter, Auditor, HostDoctor} {
		if spend.Agent(id).DayUSD <= 0 {
			t.Fatalf("%s was not charged: %+v", id, spend.Agents)
		}
	}
	if spend.Agent(Mycroft).DayUSD != 0 {
		t.Fatal("a refused agent was charged")
	}
	if total := spend.Agent(Sherlock).DayUSD + spend.Agent(Troubleshooter).DayUSD + spend.Agent(Auditor).DayUSD + spend.Agent(HostDoctor).DayUSD; total-spend.DayUSD > 1e-9 || spend.DayUSD-total > 1e-9 {
		t.Fatalf("the agents' shares %v do not add up to the total %v", total, spend.DayUSD)
	}
	if n := strings.Count(w.audit.String(), `"ai.agent"`); n != 4 {
		t.Fatalf("%d agent calls audited:\n%s", n, w.audit.String())
	}
	if !strings.Contains(w.audit.String(), "agent auditor; asked by troubleshooter; model claude-opus-5-5") {
		t.Fatalf("the audit:\n%s", w.audit.String())
	}

	// The same agent twice in a chain is refused.
	res = w.run(t, Sherlock, "[[chain troubleshooter,auditor,troubleshooter]]")
	if inner := res.Hands[0].Hands[0]; !strings.Contains(inner.Text, "already working on this question") || len(inner.Hands) != 0 {
		t.Fatalf("a repeat: %+v", inner)
	}
}

// A chain stops when any budget in it is spent: the agent over its cap
// stops, and every agent above it with it.
func TestAHandOverChainStopsAtABudget(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{DailyUSD: 0.000001}}); err != nil {
		t.Fatal(err)
	}
	res := w.run(t, Sherlock, "[[chain troubleshooter,auditor,hostDoctor]]")
	var capErr *sherlock.ErrCap
	if !errors.As(res.Err, &capErr) || capErr.Agent != "Auditor" {
		t.Fatalf("the chain went on: %v %+v", res.Err, res)
	}
	if len(res.HandOvers) != 2 || res.Hands[0].Status != "failed" || res.Hands[0].Hands[0].Status != "failed" || len(res.Hands[0].Hands[0].Hands) != 0 {
		t.Fatalf("hands: %v %+v", res.HandOvers, res.Hands[0])
	}
	// Spent, the auditor is not asked again.
	res = w.run(t, Sherlock, "[[chain auditor]]")
	if !errors.As(res.Err, &capErr) || res.Hands[0].Status != "refused" {
		t.Fatalf("asked over its cap: %v %+v", res.Err, res.Hands)
	}
}

// A question's answer shows its hand-overs as blocks of the message, and
// they reach every window with it.
func TestTheAnswerShowsItsHandOvers(t *testing.T) {
	w := setup(t, nil)
	c, err := w.engine.Ask(sherlock.Question{Key: sherlock.Key{Session: "s", Cluster: "local"}, Text: "[[chain troubleshooter]]", Cluster: "local"})
	if err != nil {
		t.Fatal(err)
	}
	w.engine.Wait()
	v := c.View()
	m := v.Messages[len(v.Messages)-1]
	if m.Status != "done" || len(m.Hands) != 1 || m.Hands[0].To != Troubleshooter || m.Hands[0].Task != "[[chain ]]" || m.Hands[0].Text != "End of the chain." || len(m.Steps) != 1 || m.Steps[0].Label != "Ask troubleshooter" {
		t.Fatalf("%+v", m)
	}
	if !strings.Contains(w.audit.String(), "hand-overs [troubleshooter]") {
		t.Fatalf("the question's record leaves out its hand-over:\n%s", w.audit.String())
	}
}

// A test runs the draft against recorded tool results through the fake
// API: the tools called, the points graded by a second call, a planted
// secret kept out; the result lands on the draft, and the cost estimate
// comes first.
func TestTheTestRunnerUsesRecordedResults(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	pod := `{"name":"web-1","status":"CrashLoopBackOff","password":"PlantedPass-77Qz"}`
	good, err := w.ai.SaveCase(ctx, TestCase{Agent: Troubleshooter, Name: "a crash loop", Question: `Why? [[get_pod {"namespace":"shop","name":"web-1"}]]`,
		Recorded: []sherlock.Recorded{{Tool: "get_pod", Input: json.RawMessage(`{"name":"web-1","namespace":"shop"}`), Result: pod}},
		Expect:   Expect{Tools: []string{"get_pod"}, Points: []string{"CrashLoopBackOff"}, Secrets: []string{"PlantedPass-77Qz"}}}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.SaveCase(ctx, TestCase{Agent: Troubleshooter, Name: "misses a point", Question: `Why? [[get_pod {"namespace":"shop","name":"web-1"}]]`,
		Recorded: []sherlock.Recorded{{Tool: "get_pod", Result: pod}}, Expect: Expect{Tools: []string{"pod_logs"}, Points: []string{"OOMKilled"}}}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Be brief.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	usd, cases, _, err := w.ai.Estimate(ctx, Troubleshooter, TestOptions{Draft: true})
	base := 0
	for _, c := range BaseCases() {
		if c.Agent == Troubleshooter {
			base++
		}
	}
	if err != nil || usd <= 0 || cases != base+2 {
		t.Fatalf("estimate %v for %d cases: %v", usd, cases, err)
	}
	w.fake.Reset()
	run, err := w.ai.RunTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]CaseResult{}
	for _, r := range run.Results {
		byName[r.Name] = r
	}
	if r := byName["a crash loop"]; !r.Passed || r.ID != good.ID {
		t.Fatalf("the good case failed: %+v", r)
	}
	if r := byName["misses a point"]; r.Passed || !strings.Contains(strings.Join(r.Why, "; "), "did not call pod_logs") || !strings.Contains(strings.Join(r.Why, "; "), "it missed: OOMKilled") {
		t.Fatalf("the bad case: %+v", r)
	}
	// The planted secret never reached the model: the result is scrubbed
	// as a live tool's is.
	for _, req := range w.fake.Requests() {
		if strings.Contains(string(req), "PlantedPass-77Qz") {
			t.Fatal("a planted secret was sent to the model")
		}
	}
	v, _ := w.ai.Instructions(ctx, Troubleshooter)
	if v.Draft.Test == nil || v.Draft.Test.Cases != run.Cases || v.Draft.Stage != StageTest || v.Draft.Test.Estimate <= 0 {
		t.Fatalf("the draft: %+v", v.Draft)
	}
	if _, err := w.ai.ReadyForLive(ctx, Troubleshooter); err != nil {
		t.Fatalf("a tested draft cannot go live: %v", err)
	}
}

// A pilot serves the draft to the people and clusters named, until it
// ends; every answer of the agent meanwhile is logged and can be rated.
func TestAPilotServesTheDraftToItsPeople(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if _, err := w.ai.SaveDraft(ctx, Sherlock, "Answer in haiku.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.StartPilot(ctx, Sherlock, []string{"bob@corp.example"}, nil, time.Hour, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an untested draft was piloted: %v", err)
	}
	if _, err := w.ai.RunTests(ctx, Sherlock, TestOptions{Draft: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.StartPilot(ctx, Sherlock, []string{"bob@corp.example"}, nil, time.Hour, "ann"); err != nil {
		t.Fatal(err)
	}
	ask := func(email string) sherlock.Agent {
		a, err := w.ai.Agent(Sherlock, sherlock.Question{Key: sherlock.Key{Cluster: "local"}, Who: audit.Who{Email: email}})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := ask("BOB@corp.example"); !strings.Contains(a.Instructions, "Answer in haiku.") || !strings.Contains(a.Version, "pilot") {
		t.Fatalf("bob: %s", a.Version)
	}
	if a := ask("eve@corp.example"); strings.Contains(a.Instructions, "Answer in haiku.") || a.Version != "live" {
		t.Fatalf("eve: %s", a.Version)
	}
	for _, who := range []string{"bob@corp.example", "eve@corp.example"} {
		c, err := w.engine.Ask(sherlock.Question{Key: sherlock.Key{Session: who, Cluster: "local"}, Text: "What needs attention?", Cluster: "local", Who: audit.Who{Email: who}})
		if err != nil {
			t.Fatal(err)
		}
		w.engine.Wait()
		_ = c
	}
	var log []PilotEntry
	for i := 0; i < 100 && len(log) < 2; i++ {
		time.Sleep(10 * time.Millisecond)
		log, _ = w.ai.Pilot(ctx, Sherlock)
	}
	if len(log) != 2 || !strings.Contains(log[0].Version+log[1].Version, "pilot") || !strings.Contains(log[0].Version+log[1].Version, "live") {
		t.Fatalf("the log: %+v", log)
	}
	if err := w.ai.Rate(ctx, log[0].N, 1, "ann"); err != nil {
		t.Fatal(err)
	}
	if log, _ = w.ai.Pilot(ctx, Sherlock); log[0].Rating != 1 || log[0].RatedBy != "ann" {
		t.Fatalf("rated: %+v", log[0])
	}
	if err := w.ai.StopPilot(ctx, Sherlock); err != nil {
		t.Fatal(err)
	}
	if a := ask("bob@corp.example"); a.Version != "live" {
		t.Fatal("the pilot outlived its stop")
	}
}

// An upgrade from 0.11 needs nothing: Sherlock's provider, key and model
// are the default provider's, every agent is on its model, the overall
// limits are Sherlock's caps, and its instructions and notes become its
// house instructions and the notes for every cluster, once.
func TestMigrationFrom011(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	w.set.sh.Instructions = "Answer as briefly as you can."
	w.set.sh.Notes = "Our nodes run Debian 12."
	w.set.sh.Model = "claude-sonnet-5-5"
	w.set.sh.Provider = &sherlock.Provider{Kind: sherlock.KindBedrock, Region: "eu-west-1"}
	if err := w.ai.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ := w.ai.Instructions(ctx, Sherlock)
	if v.House != "Answer as briefly as you can." || v.Notes[AllClusters] != "Our nodes run Debian 12." || v.Live != 1 {
		t.Fatalf("%+v", v)
	}
	o := w.ai.View(ctx)
	if o.Providers[0].Name != "Amazon Bedrock" || o.Providers[0].Resolved.Endpoint != "https://bedrock-runtime.eu-west-1.amazonaws.com" || o.DailyUSD != 20 || o.MonthlyUSD != 200 {
		t.Fatalf("%+v", o.Providers[0])
	}
	for _, a := range o.Agents {
		if a.Model != "claude-sonnet-5-5" {
			t.Fatalf("%s on %s", a.ID, a.Model)
		}
	}
	// Once only: an edit made since stays.
	if err := w.ai.SetLive(ctx, Sherlock, "Edited since.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.ai.Instructions(ctx, Sherlock); v.House != "Edited since." {
		t.Fatal("migrated twice")
	}
	// The built-in text itself is not carried over: it is the core now.
	w2 := setup(t, nil)
	w2.set.sh.Instructions = sherlock.BuiltIn(true)
	if err := w2.ai.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := w2.ai.Instructions(ctx, Sherlock); v.State != InSync || v.House != "" {
		t.Fatalf("%+v", v)
	}
}

// Instructions are kept to their word limit; lines that look like facts
// warn without refusing.
func TestTextLimitsAndFactWarnings(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if _, err := w.ai.SaveDraft(ctx, Auditor, strings.Repeat("word ", MaxWords+1), nil, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over the limit: %v", err)
	}
	warn, err := w.ai.SaveDraft(ctx, Auditor, "Be thorough.\nThe database is at 10.0.4.17 port 5432.\nPrefer short answers.", map[string]string{"local": "Grafana lives on grafana.corp.example."}, "ann")
	if err != nil || len(warn) != 2 || !strings.HasPrefix(warn[0], "Line 2 ") {
		t.Fatalf("%v %v", warn, err)
	}
}

// A backup carries the AI keys with a placeholder where a provider has a
// credential, and never the credential; a restore puts them back.
func TestBackupKeys(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "gw", Name: "GW", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gw.example/v1"}}, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.KeysFor("gw").Set(ctx, "gw-secret-0123456789", "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.SaveDraft(ctx, Auditor, "Be thorough.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	out, err := w.ai.Export(ctx, (&backup.Env{}).Seal)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := json.Marshal(out)
	if strings.Contains(string(all), "gw-secret") || !strings.Contains(string(out[ProvidersKey]), clusters.Placeholder) || out[PromptsKey] == nil {
		t.Fatalf("%s", all)
	}
	w2 := setup(t, nil)
	if err := w2.ai.Import(ctx, out); err != nil {
		t.Fatal(err)
	}
	if o := w2.ai.View(ctx); len(o.Providers) != 2 || o.Providers[1].Key.Set {
		t.Fatalf("%+v", o.Providers)
	}
	if v, _ := w2.ai.Instructions(ctx, Auditor); v.Draft == nil || v.Draft.House != "Be thorough." {
		t.Fatalf("%+v", v.Draft)
	}
}

// Every agent has its core file, written in plain words, and the base
// test cases are each an agent's.
func TestTheCatalog(t *testing.T) {
	for _, info := range Catalog {
		if info.ID != Sherlock && !strings.HasPrefix(promptFile(info.ID), "You are ") {
			t.Errorf("%s has no core", info.ID)
		}
		if len(info.Tools) == 0 {
			t.Errorf("%s has no tools", info.ID)
		}
	}
	seen := map[string]int{}
	for _, c := range BaseCases() {
		if _, ok := InfoOf(c.Agent); !ok {
			t.Errorf("%s is for no agent", c.ID)
		}
		seen[c.Agent]++
	}
	for _, info := range Catalog {
		if seen[info.ID] == 0 {
			t.Errorf("%s ships no test case", info.ID)
		}
	}
}
