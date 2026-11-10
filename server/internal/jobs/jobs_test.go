package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/schedule"
)

// clock is a clock a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// fakeAgents answers every agent's call with a text and a cost.
type fakeAgents struct {
	mu    sync.Mutex
	text  string
	cost  float64
	calls []AgentRequest
	err   error
}

func (f *fakeAgents) Run(ctx context.Context, req AgentRequest) AgentResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return AgentResult{Text: f.text, Model: "claude-test", CostUSD: f.cost, Tokens: Tokens{In: 100, Out: 20}, Err: f.err}
}

func (f *fakeAgents) Usable(string, string) string { return "" }

func (f *fakeAgents) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type world struct {
	s      *Service
	clock  *clock
	kv     *kv.Memory
	agents *fakeAgents
	props  *MemoryProposer
	audit  *bytes.Buffer
	kinds  *Registry
	// runs counts the test kind's runs; gate, when set, holds each run
	// until it is closed.
	runs atomic.Int32
	gate chan struct{}
}

var day1 = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

func setup(t *testing.T) *world {
	t.Helper()
	w := &world{clock: &clock{t: day1.Add(time.Hour)}, kv: &kv.Memory{}, agents: &fakeAgents{text: "All is well.\n\n```json\n{\"findings\": [], \"proposals\": []}\n```", cost: 0.5}, props: &MemoryProposer{}, audit: &bytes.Buffer{}, kinds: NewRegistry()}
	w.s = w.service(w.kv)
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", AgentName: "Auditor", Schedulable: true, Events: []string{EventAlert},
		Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}},
		Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
			w.runs.Add(1)
			if w.gate != nil {
				select {
				case <-w.gate:
				case <-ctx.Done():
					return Outcome{}, ctx.Err()
				}
			}
			res, err := env.Ask(ctx, env.Clusters[0], "check")
			if err != nil {
				return Outcome{}, err
			}
			p := Parse(res.Text)
			return Outcome{Report: p.Report, Findings: p.Findings}, nil
		}})
	t.Cleanup(w.s.Stop)
	return w
}

// service is another replica over the same records.
func (w *world) service(b kv.Backend) *Service {
	return New(Options{Backend: b, Kinds: w.kinds, Agents: w.agents, Proposer: w.props, Now: w.clock.Now, Every: 30 * time.Second,
		Audit: audit.New(audit.Options{Out: w.audit}), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clusters: func() []Cluster { return []Cluster{{ID: "local", Name: "local"}, {ID: "prod", Name: "prod-eu"}} }})
}

func on(t *testing.T, s *Service, id string, c Change) View {
	t.Helper()
	yes := true
	if c.On == nil {
		c.On = &yes
	}
	v, err := s.Update(context.Background(), id, c, Person{Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func runs(t *testing.T, s *Service) []RunBrief {
	t.Helper()
	list, _, err := s.Runs(context.Background(), RunFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// The kinds' own jobs exist off until someone turns them on; turning one
// on names who did, and the change is audited with its old and new values.
func TestJobsStartOffAndChangesAreAudited(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	list, err := w.s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("jobs: %d", len(list))
	}
	for _, v := range list {
		if v.On || v.NextText != "Paused" {
			t.Fatalf("%s starts on: %+v", v.ID, v)
		}
	}
	tz := "Europe/Berlin"
	v := on(t, w.s, "test", Change{When: &When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "03:30", TimeZone: tz}}, Budget: &Budget{RunUSD: 4, DayUSD: 8}, Clusters: &[]string{"prod"}})
	if !v.On || v.SetBy != "alice@example.com" || v.WhenText != "Every day at 03:30, Europe/Berlin" || v.Next.IsZero() || v.Since.IsZero() {
		t.Fatalf("view: %+v", v)
	}
	got := w.audit.String()
	for _, want := range []string{`"action":"job.update"`, "Test job (Auditor)", "state paused → on", "Every day at 02:00, UTC → Every day at 03:30, Europe/Berlin", "clusters all clusters → prod", "budget per run none → $4.00"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the audit record lacks %q: %s", want, got)
		}
	}
	if _, err := w.s.Update(context.Background(), "test", Change{When: &When{Schedule: &schedule.Spec{Kind: "weekly", At: "03:00"}}}, Person{Email: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a weekly schedule without days: %v", err)
	}
	if _, err := w.s.Update(context.Background(), "test", Change{Clusters: &[]string{"nowhere"}}, Person{Email: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown cluster: %v", err)
	}
	if _, err := w.s.Update(context.Background(), KindAudit, Change{When: &When{Event: EventReport}}, Person{Email: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an event the kind does not take: %v", err)
	}
}

// A time comes: it runs once, on the leader only. A time missed while the
// console was down runs once within the hour; one missed by more waits
// for the next and is noted.
func TestScheduleRunsOnceWithinTheGrace(t *testing.T) {
	w := setup(t)
	leading := false
	w.s.o.Leads = func() bool { return leading }
	on(t, w.s, "test", Change{})
	ctx := context.Background()
	w.clock.Set(day1.Add(2*time.Hour + 40*time.Minute))
	w.s.Tick(ctx)
	w.s.Wait()
	if w.runs.Load() != 0 {
		t.Fatal("a replica that does not lead ran the schedule")
	}
	leading = true
	w.s.Tick(ctx)
	w.s.Wait()
	w.s.Tick(ctx)
	w.s.Wait()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("runs within the grace: %d", n)
	}
	r := runs(t, w.s)[0]
	if r.Status != StatusOK || r.Trigger.Kind != TriggerSchedule || r.Trigger.SetBy != "alice@example.com" || !r.Trigger.Slot.Equal(day1.Add(2*time.Hour)) || r.CostUSD != 0.5 {
		t.Fatalf("run: %+v", r)
	}
	// The next day the console is down from 01:00 to 04:00.
	w.clock.Set(day1.Add(28 * time.Hour))
	w.s.Tick(ctx)
	w.s.Wait()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("a time missed by two hours ran: %d", n)
	}
	r = runs(t, w.s)[0]
	if r.Status != StatusMissed || !strings.Contains(r.Note, "not made") {
		t.Fatalf("missed: %+v", r)
	}
	if !strings.Contains(w.audit.String(), `"action":"job.run"`) {
		t.Fatal("the runs are not audited")
	}
}

// Without catching up, a time missed by more than a tick waits.
func TestNoCatchUpWaitsForTheNextTime(t *testing.T) {
	w := setup(t)
	no := false
	on(t, w.s, "test", Change{CatchUp: &no})
	w.clock.Set(day1.Add(2*time.Hour + 20*time.Minute))
	w.s.Tick(context.Background())
	w.s.Wait()
	if w.runs.Load() != 0 || runs(t, w.s)[0].Status != StatusMissed {
		t.Fatalf("runs %d, %+v", w.runs.Load(), runs(t, w.s))
	}
}

// A run still going makes the next time skip, never pile up.
func TestOverlapSkips(t *testing.T) {
	w := setup(t)
	w.gate = make(chan struct{})
	on(t, w.s, "test", Change{When: &When{Schedule: &schedule.Spec{Kind: schedule.Hours, Every: 1, At: "00:00"}}})
	ctx := context.Background()
	w.clock.Set(day1.Add(2*time.Hour + time.Minute))
	w.s.Tick(ctx)
	waitFor(t, func() bool { return w.runs.Load() == 1 })
	w.clock.Set(day1.Add(3*time.Hour + time.Minute))
	w.s.Tick(ctx)
	list := runs(t, w.s)
	if len(list) != 2 || list[0].Status != StatusSkipped || !strings.Contains(list[0].Note, "still going") || list[1].Status != StatusRunning {
		t.Fatalf("runs: %+v", list)
	}
	if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); !errors.Is(err, ErrRunning) {
		t.Fatalf("run now while running: %v", err)
	}
	close(w.gate)
	w.s.Wait()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("runs: %d", n)
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// Two replicas that both think they lead, as while the Lease moves: the
// conditional write lets one claim the time, and it runs once.
func TestTwoLeadersRunATimeOnce(t *testing.T) {
	w := setup(t)
	other := w.service(w.kv)
	t.Cleanup(other.Stop)
	on(t, w.s, "test", Change{})
	w.clock.Set(day1.Add(2*time.Hour + time.Minute))
	var wg sync.WaitGroup
	for _, s := range []*Service{w.s, other, w.s, other} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			s.Tick(context.Background())
		}(s)
	}
	wg.Wait()
	w.s.Wait()
	other.Wait()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("the time ran %d times", n)
	}
}

// A job stops for the day when its budget is spent, and says so: a run
// stops at the call past its own budget, and the next run does not start.
func TestBudgetStopsForTheDay(t *testing.T) {
	w := setup(t)
	w.agents.cost = 3
	on(t, w.s, "test", Change{Budget: &Budget{DayUSD: 5}})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); err != nil {
			t.Fatal(err)
		}
		w.s.Wait()
	}
	v, _ := w.s.Get(ctx, "test")
	if v.SpentToday != 6 || !strings.Contains(v.StoppedToday, "Stopped for the day") {
		t.Fatalf("view: spent %v, %q", v.SpentToday, v.StoppedToday)
	}
	if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("a run past the day's budget: %v", err)
	}
	if r := runs(t, w.s)[0]; r.Status != StatusBudget || !strings.Contains(r.Note, "tomorrow") {
		t.Fatalf("run: %+v", r)
	}
	if n := w.agents.n(); n != 2 {
		t.Fatalf("agent calls: %d", n)
	}
	// The next day it runs again.
	w.clock.Set(day1.Add(25 * time.Hour))
	if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	// A run's own budget stops it at the call past it.
	on(t, w.s, "test", Change{Budget: &Budget{RunUSD: 2}})
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", Schedulable: true, Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
		for i := 0; i < 3; i++ {
			if _, err := env.Ask(ctx, "local", "again"); err != nil {
				return Outcome{Report: "part"}, err
			}
		}
		return Outcome{}, nil
	}})
	if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	r, _ := w.s.GetRun(ctx, runs(t, w.s)[0].ID)
	if r.Status != StatusBudget || !strings.Contains(r.Note, "run's budget of $2.00 is spent") || r.CostUSD != 3 || r.Report != "part" {
		t.Fatalf("run: %+v", r)
	}
}

// The alerts there are when a job is turned on are only noted; a new one
// runs once, and repeats of it update that run instead of piling up, also
// on a new leader; a paused job takes none.
func TestAlertRepeatsUpdateOneRun(t *testing.T) {
	w := setup(t)
	alerts := []Alert{{Cluster: "local", Key: "node:worker-3", Kind: "node", Severity: SeverityWarning, Name: "worker-3", Status: "NotReady"}}
	var mu sync.Mutex
	w.s.o.Alerts = func(context.Context) []Alert {
		mu.Lock()
		defer mu.Unlock()
		return append([]Alert(nil), alerts...)
	}
	add := func(a Alert) {
		mu.Lock()
		alerts = append(alerts, a)
		mu.Unlock()
	}
	ctx := context.Background()
	add(Alert{Cluster: "local", Key: "pod:web/old", Kind: "pod", Severity: SeverityError, Name: "old", Namespace: "web", Status: "Error"})
	w.s.Tick(ctx)
	w.s.Wait()
	if w.runs.Load() != 0 {
		t.Fatal("a paused job took an alert")
	}
	on(t, w.s, "test", Change{When: &When{Event: EventAlert, Severity: SeverityWarning}})
	w.s.Tick(ctx)
	w.s.Wait()
	if w.runs.Load() != 0 {
		t.Fatal("an alert there before the job was on was diagnosed")
	}
	add(Alert{Cluster: "prod", Key: "pod:shop/web-7f9", Kind: "pod", Severity: SeverityError, Name: "web-7f9", Namespace: "shop", Status: "CrashLoopBackOff", Reason: "BackOff"})
	w.s.Tick(ctx)
	w.s.Wait()
	w.s.Tick(ctx)
	w.s.Wait()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("runs: %d", n)
	}
	r := runs(t, w.s)[0]
	if r.Subject != "alert:prod/pod:shop/web-7f9" || r.Trigger.Kind != TriggerEvent || !strings.Contains(r.Trigger.What, "Alert BackOff on shop/web-7f9 at") || len(r.Clusters) != 1 || r.Clusters[0] != "prod" {
		t.Fatalf("run: %+v", r)
	}
	// It clears and comes back, here or on another leader: the run takes
	// it in.
	next := w.service(w.kv)
	t.Cleanup(next.Stop)
	if n := next.Fire(Event{Kind: EventAlert, Cluster: "prod", Key: "alert:prod/pod:shop/web-7f9", Severity: SeverityError, Label: "again"}); n != 1 {
		t.Fatalf("fired %d", n)
	}
	next.Wait()
	list := runs(t, w.s)
	if w.runs.Load() != 1 || len(list) != 1 || list[0].Repeats != 1 {
		t.Fatalf("a repeat: %d runs, %+v", w.runs.Load(), list)
	}
	// A day later it runs again.
	w.clock.Set(w.clock.Now().Add(25 * time.Hour))
	w.s.Fire(Event{Kind: EventAlert, Cluster: "prod", Key: "alert:prod/pod:shop/web-7f9", Severity: SeverityError, Label: "a day on"})
	w.s.Wait()
	if n := w.runs.Load(); n != 2 {
		t.Fatalf("runs a day later: %d", n)
	}
	// Errors only: a warning starts nothing.
	on(t, w.s, "test", Change{When: &When{Event: EventAlert, Severity: SeverityError}})
	if n := w.s.Fire(Event{Kind: EventAlert, Cluster: "local", Key: "alert:local/x", Severity: SeverityWarning}); n != 0 {
		t.Fatalf("a warning started %d jobs of errors only", n)
	}
}

// The runs are kept by count and by size: the oldest go first.
func TestRunsAreKeptByCountAndSize(t *testing.T) {
	w := setup(t)
	w.s.o.MaxRuns = 5
	w.agents.text = "Found one.\n\n```json\n{\"findings\": [{\"severity\": \"warning\", \"title\": \"A finding\"}]}\n```"
	on(t, w.s, "test", Change{})
	ctx := context.Background()
	if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	w.agents.text = "Nothing."
	for i := 0; i < 7; i++ {
		if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); err != nil {
			t.Fatal(err)
		}
		w.s.Wait()
	}
	list := runs(t, w.s)
	if len(list) != 5 || list[4].ID != "run-4" || list[0].ID != "run-8" {
		t.Fatalf("kept: %+v", list)
	}
	// By size.
	w.s.o.RunsBytes = 4 << 10
	w.agents.text = strings.Repeat("A long report. ", 200)
	if _, err := w.s.RunNow(ctx, "test", Person{Email: "bob"}); err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	raw, _, _ := w.kv.LoadKey(ctx, RunsKey)
	if len(raw) > 4<<10 {
		t.Fatalf("the runs take %d bytes", len(raw))
	}
}

// A run under way is cancelled by a person: it ends "cancelled", with who
// did it, its spend counted and audited; one not running is refused
// (FX-12).
func TestCancelARun(t *testing.T) {
	w := setup(t)
	w.gate = make(chan struct{})
	on(t, w.s, "test", Change{})
	ctx := context.Background()
	b, err := w.s.RunNow(ctx, "test", Person{Email: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Cancel(ctx, "run-9", Person{Email: "carol"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
	c, err := w.s.Cancel(ctx, b.ID, Person{Email: "carol"})
	if err != nil || c.Status != StatusCancelled || !strings.Contains(c.Note, "cancelled by carol") {
		t.Fatalf("cancelled: %+v %v", c, err)
	}
	w.s.Wait()
	if _, err := w.s.Cancel(ctx, b.ID, Person{Email: "carol"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cancelled twice: %v", err)
	}
	if !strings.Contains(w.audit.String(), `"action":"job.cancel"`) || !strings.Contains(w.audit.String(), b.ID+" Test job cancelled while it ran") {
		t.Fatalf("audit: %s", w.audit.String())
	}
	// A run another replica left marked running is marked cancelled at once.
	other := w.service(w.kv)
	defer other.Stop()
	if err := other.addRun(ctx, &Run{Job: "test", Kind: "test", Label: "Test job", Status: StatusRunning, Started: w.clock.Now(), Instance: "gone"}); err != nil {
		t.Fatal(err)
	}
	if c, err := w.s.Cancel(ctx, "run-2", Person{Email: "carol"}); err != nil || c.Status != StatusCancelled || !strings.Contains(c.Note, "no console was running it") {
		t.Fatalf("a run left behind: %+v %v", c, err)
	}
}

// What an agent proposes goes to the Proposer with what started it and
// who set the trigger; a command Watson refuses, or that is not kubectl or
// helm, is left out.
func TestProposalsGoToTheProposer(t *testing.T) {
	w := setup(t)
	w.s.o.CommandOK = func(line string) error {
		if strings.Contains(line, "delete namespace") {
			return errors.New("refused")
		}
		return nil
	}
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "troubleshooter", Events: []string{EventAlert}, Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
		res, err := env.Ask(ctx, env.Clusters[0], "why")
		if err != nil {
			return Outcome{}, err
		}
		var out Outcome
		merge(&out, env, w.s.o.CommandOK, env.Clusters[0], "", Parse(res.Text))
		return out, nil
	}})
	w.agents.text = "The pod is stuck.\n\n```json\n{\"findings\": [], \"proposals\": [{\"title\": \"Restart web\", \"reasoning\": \"stuck\", \"fixes\": [\"C-4\"], \"commands\": [\"kubectl rollout restart deployment/web -n shop\", \"rm -rf /\"], \"impact\": \"a rolling restart\", \"risk\": \"low\"}, {\"title\": \"Remove shop\", \"commands\": [\"kubectl delete namespace shop\"]}]}\n```"
	on(t, w.s, "test", Change{When: &When{Event: EventAlert}})
	if n := w.s.Fire(Event{Kind: EventAlert, Cluster: "prod", Key: "alert:prod/pod:shop/web", Label: "Alert BackOff on shop/web at 03:12", Severity: SeverityError}); n != 1 {
		t.Fatalf("fired %d", n)
	}
	w.s.Wait()
	props := w.props.List()
	if len(props) != 1 {
		t.Fatalf("proposals: %+v", props)
	}
	p := props[0]
	if p.Agent != "troubleshooter" || p.Source.Kind != "alert" || p.Source.ID != "run-1" || p.Source.Label != "Alert BackOff on shop/web at 03:12" || p.Source.SetBy != "alice@example.com" || p.Source.Job != "Test job" ||
		p.Cluster != "prod" || len(p.Actions) != 1 || p.Actions[0].Kind != "kubectl" || p.Actions[0].Risk != "low" || p.Condition.Kind != "alert-firing" || p.Expires.IsZero() || p.DedupKey == "" {
		t.Fatalf("proposal: %+v", p)
	}
	b, _ := json.Marshal(p)
	for _, key := range []string{`"agent"`, `"source"`, `"setBy"`, `"job"`, `"cluster"`, `"title"`, `"reasoning"`, `"actions"`, `"impact"`, `"condition"`, `"dedupKey"`, `"expires"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("the proposal's JSON lacks %s: %s", key, b)
		}
	}
	r, _ := w.s.GetRun(context.Background(), "run-1")
	if len(r.Proposals) != 1 || r.Proposals[0].ID != "proposal-1" || len(r.Proposals[0].Resolves) != 1 || r.Proposals[0].Resolves[0] != "C-4" || !strings.Contains(r.Note, "refuse") || !strings.Contains(r.Note, "not kubectl or helm") {
		t.Fatalf("run: %+v", r)
	}
}

// A run its replica left under way, past the run's time, is let go.
func TestRunsLeftBehindAreSwept(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	_ = w.s.updateRuns(ctx, func(rec *runsRecord) error {
		rec.Next = 1
		rec.Runs = []Run{{ID: "run-1", Job: "test", Status: StatusRunning, Started: w.clock.Now(), Instance: "gone"}}
		return nil
	})
	w.s.Tick(ctx)
	if r := runs(t, w.s)[0]; r.Status != StatusRunning {
		t.Fatalf("a run still within its time was swept: %+v", r)
	}
	w.clock.Set(w.clock.Now().Add(time.Hour))
	w.s.Tick(ctx)
	if r := runs(t, w.s)[0]; r.Status != StatusInterrupted {
		t.Fatalf("run: %+v", r)
	}
}

// A backup holds the jobs and their settings, not the runs; a restore puts
// them back with their schedules counting from it.
func TestBackupRoundTrip(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if x, err := w.s.Export(ctx); err != nil || x != nil {
		t.Fatalf("an untouched console exports %+v %v", x, err)
	}
	on(t, w.s, "test", Change{Budget: &Budget{DayUSD: 3}})
	if _, err := w.s.SaveSettings(ctx, Settings{Editors: []string{"admin"}, ReaderRole: ReaderSuperViewer}, Person{Email: "root"}); err != nil {
		t.Fatal(err)
	}
	x, err := w.s.Export(ctx)
	if err != nil || x == nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(x)
	fresh := w.service(&kv.Memory{})
	t.Cleanup(fresh.Stop)
	w.clock.Set(day1.Add(5 * time.Hour))
	n, err := fresh.Import(ctx, raw, "restorer")
	if err != nil || n != 1 {
		t.Fatalf("restored %d: %v", n, err)
	}
	v, _ := fresh.Get(ctx, "test")
	set, _ := fresh.Settings(ctx)
	if !v.On || v.Budget.DayUSD != 3 || !v.Since.Equal(day1.Add(5*time.Hour)) || set.Role() != ReaderSuperViewer || set.Editors[0] != "admin" {
		t.Fatalf("restored: %+v %+v", v, set)
	}
	if _, err := fresh.Import(ctx, json.RawMessage(`{"settings": {"readerRole": "superAdmin"}}`), "x"); err == nil {
		t.Fatal("a backup that reads as a SuperAdmin was restored")
	}
}

// The settings: who may edit besides a SuperAdmin, and a read-only role
// for agents on their own, never one that may change anything.
func TestSettings(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if _, err := w.s.SaveSettings(ctx, Settings{ReaderRole: "admin"}, Person{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an admin reader: %v", err)
	}
	set, err := w.s.SaveSettings(ctx, Settings{Editors: []string{"admin", "admin", "superAdmin"}, ReaderGroup: "ops"}, Person{Email: "root"})
	if err != nil || len(set.Editors) != 1 || set.Role() != ReaderViewer || set.ReaderGroup != "ops" {
		t.Fatalf("%+v %v", set, err)
	}
	if !set.MayEdit(false, []string{"viewer", "admin"}) || set.MayEdit(false, []string{"viewer"}) || !set.MayEdit(true, nil) {
		t.Fatal("who may edit")
	}
	if !strings.Contains(w.audit.String(), "who may edit jobs: SuperAdmin → SuperAdmin, admin; agents on their own read as viewer → viewer (group ops)") {
		t.Fatalf("audit: %s", w.audit.String())
	}
}

// The API: reading for those who may see the runs, changing for those
// who may edit jobs, the settings for a SuperAdmin.
func TestAPI(t *testing.T) {
	w := setup(t)
	var caller Caller
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return caller, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	do := func(method, path, body string) (int, map[string]any) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	caller = Caller{Person: Person{Email: "viewer@example.com"}, Roles: []string{"viewer"}}
	if code, _ := do("GET", "/api/v1/jobs", ""); code != http.StatusForbidden {
		t.Fatalf("a viewer reads the jobs: %d", code)
	}
	caller = Caller{Person: Person{Email: "sv@example.com"}, Roles: []string{"superViewer"}, Read: true}
	code, out := do("GET", "/api/v1/jobs", "")
	if code != http.StatusOK || out["mayEdit"] != false || len(out["jobs"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, _ := do("PUT", "/api/v1/jobs/test", `{"on": true}`); code != http.StatusForbidden {
		t.Fatalf("a SuperViewer changes a job: %d", code)
	}
	if code, _ := do("PUT", "/api/v1/jobs/settings", `{"editors": ["superViewer"]}`); code != http.StatusForbidden {
		t.Fatalf("a SuperViewer changes the settings: %d", code)
	}
	caller = Caller{Person: Person{Email: "root@example.com"}, Admin: true, Roles: []string{"superAdmin"}, Read: true}
	if code, out := do("PUT", "/api/v1/jobs/settings", `{"editors": ["superViewer"]}`); code != http.StatusOK {
		t.Fatalf("settings: %d %v", code, out)
	}
	caller = Caller{Person: Person{Email: "sv@example.com"}, Roles: []string{"superViewer"}, Read: true}
	if code, out := do("PUT", "/api/v1/jobs/test", `{"on": true, "budget": {"runUSD": 1}}`); code != http.StatusOK || out["on"] != true || out["setBy"] != "sv@example.com" {
		t.Fatalf("an editor changes a job: %d %v", code, out)
	}
	if code, out := do("PUT", "/api/v1/jobs/test", `{"when": {"schedule": {"kind": "cron", "cron": "61 * * * *"}}}`); code != http.StatusBadRequest {
		t.Fatalf("a bad cron: %d %v", code, out)
	}
	if code, out := do("POST", "/api/v1/jobs/schedule", `{"kind": "weekly", "days": ["mon", "fri"], "at": "07:15", "timeZone": "Europe/Berlin"}`); code != http.StatusOK || out["description"] != "Every week on Monday and Friday at 07:15, Europe/Berlin" || len(out["next"].([]any)) != 3 {
		t.Fatalf("preview: %d %v", code, out)
	}
	if code, out := do("POST", "/api/v1/jobs/test/run", `{}`); code != http.StatusAccepted || out["status"] != StatusRunning {
		t.Fatalf("run now: %d %v", code, out)
	}
	w.s.Wait()
	if code, out := do("GET", "/api/v1/jobs/test/runs", ""); code != http.StatusOK || out["total"] != float64(1) {
		t.Fatalf("job runs: %d %v", code, out)
	}
	if code, out := do("GET", "/api/v1/runs/run-1", ""); code != http.StatusOK || out["report"] != "All is well." {
		t.Fatalf("run: %d %v", code, out)
	}
	if code, _ := do("GET", "/api/v1/runs/run-9", ""); code != http.StatusNotFound {
		t.Fatalf("an unknown run: %d", code)
	}
	// The findings have no routes of their own any more: each is routed
	// into a case (CF-01).
	for _, p := range []string{"GET /api/v1/findings", "POST /api/v1/findings/run-1.1/ignore", "PUT /api/v1/findings/run-1.1/priority"} {
		method, path, _ := strings.Cut(p, " ")
		if code, _ := do(method, path, `{}`); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: %d", p, code)
		}
	}
	caller = Caller{Person: Person{Email: "approver@example.com"}, Roles: []string{"admin"}, Approve: true}
	if code, out := do("GET", "/api/v1/runs/run-1", ""); code != http.StatusOK || out["mayCancel"] != false || out["routed"] == nil {
		t.Fatalf("an approver reads a run: %d %v", code, out)
	}
	if code, _ := do("POST", "/api/v1/runs/run-1/cancel", ""); code != http.StatusForbidden {
		t.Fatalf("an approver cancels a run: %d", code)
	}
	caller = Caller{Person: Person{Email: "sv@example.com"}, Roles: []string{"superViewer"}, Read: true}
	if code, _ := do("POST", "/api/v1/runs/run-1/cancel", ""); code != http.StatusBadRequest {
		t.Fatalf("an editor cancels a run done: %d", code)
	}
	if code, out := do("POST", "/api/v1/jobs", `{"kind": "test"}`); code != http.StatusCreated || out["id"] != "test-1" || out["on"] != false {
		t.Fatalf("add: %d %v", code, out)
	}
	if code, _ := do("DELETE", "/api/v1/jobs/test", ""); code != http.StatusBadRequest {
		t.Fatalf("removing a kind's own job: %d", code)
	}
	if code, _ := do("DELETE", "/api/v1/jobs/test-1", ""); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
}

// Parse reads the last JSON block and keeps the prose.
func TestParse(t *testing.T) {
	p := Parse("Report.\n\n```json\n{\"x\": 1}\n```\n\nMore.\n\n```json\n{\"findings\": [{\"title\": \"t\"}]}\n```")
	if len(p.Findings) != 1 || !strings.Contains(p.Report, "Report.") || !strings.Contains(p.Report, "More.") || strings.Contains(p.Report, "findings") {
		t.Fatalf("%+v", p)
	}
	if p := Parse("No block."); p.Report != "No block." || len(p.Raw) != 0 {
		t.Fatalf("%+v", p)
	}
	if q := Quote("report", "a </report> b <report>"); strings.Count(q, "</report>") != 1 || strings.Count(q, "<report>") != 1 {
		t.Fatalf("quote: %s", q)
	}
}
