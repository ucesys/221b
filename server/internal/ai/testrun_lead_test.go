package ai

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// heldRun starts a run of the troubleshooter's tests with one case held
// at the fake API; ended carries the run when it ends.
func heldRun(t *testing.T, w *world, held chan struct{}) chan TestRun {
	t.Helper()
	ctx := context.Background()
	if _, err := w.ai.SaveTest(ctx, TestCase{Agent: Troubleshooter, Name: "held", Question: "HOLD-HERE: what runs?"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Be brief.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	ended := make(chan TestRun, 1)
	if err := w.ai.StartTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann"}, func(r TestRun, _ error) { ended <- r }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, held, "the held case")
	return ended
}

func waitRun(t *testing.T, ended chan TestRun) TestRun {
	t.Helper()
	done := make(chan struct{})
	var r TestRun
	go func() { r = <-ended; close(done) }()
	waitFor(t, done, "the run to end")
	return r
}

// A replica that loses the lead stops its test runs: the next leader
// could neither see nor stop them.
func TestLosingTheLeadStopsTestRuns(t *testing.T) {
	wrap, held, release := holdOn(t, "HOLD-HERE")
	w := setupWith(t, nil, wrap)
	t.Cleanup(release)
	ended := heldRun(t, w, held)
	w.ai.StopAllTests()
	if r := waitRun(t, ended); r.State != RunStopped {
		t.Fatalf("after losing the lead: %+v", r)
	}
	if w.ai.testing(Troubleshooter) {
		t.Fatal("still testing")
	}
}

// Only a run that ran every case to the end tests a draft: one stopped
// is kept and shown, but the pilot and Make live still wait for a test,
// and Make live untested is offered.
func TestAStoppedRunDoesNotTestTheDraft(t *testing.T) {
	wrap, held, release := holdOn(t, "HOLD-HERE")
	w := setupWith(t, nil, wrap)
	t.Cleanup(release)
	ended := heldRun(t, w, held)
	if err := w.ai.StopTests(Troubleshooter); err != nil {
		t.Fatal(err)
	}
	if r := waitRun(t, ended); r.State != RunStopped || r.Complete() {
		t.Fatalf("the run: %+v", r)
	}
	ctx := context.Background()
	v, _ := w.ai.Instructions(ctx, Troubleshooter)
	if v.Draft == nil || v.Draft.Test == nil || v.Draft.Test.State != RunStopped || v.Draft.Stage != StageDraft {
		t.Fatalf("kept: %+v", v.Draft)
	}
	if _, err := w.ai.ReadyForLive(ctx, Troubleshooter); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "did not run every case") {
		t.Fatalf("ready for live: %v", err)
	}
	if err := w.ai.StartPilot(ctx, Troubleshooter, []string{"dev@corp.example"}, nil, time.Hour, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a pilot: %v", err)
	}
	if _, err := w.ai.ReadyUntested(ctx, Troubleshooter); err != nil {
		t.Fatal(err)
	}
	// A run kept before Phase 12.1 is complete unless its error says not.
	if !(&TestRun{}).Complete() || (&TestRun{Error: "stopped"}).Complete() || (&TestRun{State: RunFailed}).Complete() {
		t.Fatal("complete")
	}
}

// A test run ends with the process, though started from a request that
// ended long before.
func TestATestRunEndsWithTheProcess(t *testing.T) {
	wrap, held, release := holdOn(t, "HOLD-HERE")
	w := setupWith(t, nil, wrap)
	t.Cleanup(release)
	proc, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	w.ai.opts.Context = proc
	ended := heldRun(t, w, held)
	shutdown()
	if r := waitRun(t, ended); r.State != RunStopped {
		t.Fatalf("after shutdown: %+v", r)
	}
}

// A cap lowered during a run holds at once: the cases in flight are not
// graded past it, and no case starts after it.
func TestACapLoweredDuringARunHolds(t *testing.T) {
	wrap, held, open := holdOn(t, "HOLD-HERE")
	release := sync.OnceFunc(open)
	w := setupWith(t, nil, wrap)
	t.Cleanup(release)
	ctx := context.Background()
	for i := range TestParallel + 3 {
		if _, err := w.ai.SaveTest(ctx, TestCase{Agent: Troubleshooter, Name: "case " + strconv.Itoa(i), Question: "HOLD-HERE " + strconv.Itoa(i), Expect: Expect{Points: []string{"something"}}}, "ann"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.ai.SaveDraft(ctx, Troubleshooter, "Be brief.", nil, "ann"); err != nil {
		t.Fatal(err)
	}
	ended := make(chan TestRun, 1)
	if err := w.ai.StartTests(ctx, Troubleshooter, TestOptions{Draft: true, By: "ann"}, func(r TestRun, _ error) { ended <- r }); err != nil {
		t.Fatal(err)
	}
	// Every slot holds one of these cases: nothing else is in flight.
	for range TestParallel {
		waitFor(t, held, "the held cases")
	}
	if err := w.ai.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{DailyUSD: 0.000001}}); err != nil {
		t.Fatal(err)
	}
	w.engine.Ledger().AddFor(ctx, Troubleshooter, DefaultProvider, 0.01, false)
	release()
	r := waitRun(t, ended)
	waiting := 0
	for _, c := range r.Planned {
		if c.State == CaseWaiting {
			waiting++
		}
	}
	for _, res := range r.Results {
		if strings.HasPrefix(res.Name, "case ") && !strings.Contains(strings.Join(res.Why, " "), "could not be graded") && !strings.Contains(strings.Join(res.Why, " "), "did not finish") {
			t.Fatalf("a case graded past the cap: %+v", res)
		}
	}
	if r.State != RunFailed || !strings.Contains(r.Error, "own daily spending cap") || waiting == 0 {
		t.Fatalf("the run: %s %q, %d waiting", r.State, r.Error, waiting)
	}
}
