package casework

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- The phase 12 review: whole fixes, exceptions that fail closed, and a
// case put right with its work (W-6, W-7, W-9) ---

// downKV is a store that cannot be read or written.
type downKV struct{}

func (downKV) LoadKey(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("the store is down")
}

func (downKV) SaveKey(context.Context, string, []byte, string) (string, error) {
	return "", errors.New("the store is down")
}

// The analysis's suggested fix and the changes it proposes are kept whole
// or not at all: one with a command Watson would refuse is left out, and
// the run and the analysis say which command and why (W-6, CF-98).
func TestAnAnalysisKeepsAFixWholeOrNotAtAllCF98(t *testing.T) {
	w := setup(t)
	w.jobOn(t, true)
	ctx := context.Background()
	w.agents.answer = `{"summary": "s", "cause": "c", "evidence": [],
 "fix": {"summary": "Clean up and restart", "commands": ["kubectl delete namespace scratch", "kubectl rollout restart deployment/web -n shop"]},
 "proposals": [{"title": "Restart and clean", "commands": ["kubectl rollout restart deployment/web -n shop", "kubectl delete namespace scratch"]}]}`
	if _, err := w.svc.Create(ctx, NewCase{Title: "Slow", Cluster: "prod"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	a := w.get(t, "C-1").Analysis
	if a == nil || a.Fix.Summary != "" || len(a.Fix.Commands) != 0 || len(a.LeftOut) != 2 {
		t.Fatalf("analysis %+v", a)
	}
	if a.LeftOut[0] != "The suggested fix, with all its commands, because Watson would refuse “kubectl delete namespace scratch”: Watson does not delete namespaces" ||
		a.LeftOut[1] != "A proposed change was not filed, with none of its commands, because Watson would refuse “kubectl delete namespace scratch”: Watson does not delete namespaces: Restart and clean" {
		t.Errorf("left out %q", a.LeftOut)
	}
	if props := w.props.List(); len(props) != 0 {
		t.Errorf("filed %+v", props)
	}
	run, err := w.jobs.GetRun(ctx, a.Run)
	if err != nil || !strings.Contains(run.Note, "the suggested fix was left out, with all its commands, because Watson would refuse") || !strings.Contains(run.Note, "not filed, with none of its commands") {
		t.Errorf("the run %+v %v", run.Note, err)
	}
	// More commands than a fix keeps: left out too, never cut short.
	w.agents.answer = `{"summary": "s", "cause": "c", "evidence": [], "fix": {"summary": "Many", "commands": ["kubectl get a", "kubectl get b", "kubectl get c", "kubectl get d", "kubectl get e", "kubectl get f"]}, "proposals": []}`
	if _, err := w.svc.Create(ctx, NewCase{Title: "Slow too", Cluster: "prod"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if a := w.get(t, "C-2").Analysis; a == nil || len(a.Fix.Commands) != 0 || len(a.LeftOut) != 1 || !strings.Contains(a.LeftOut[0], "it has more than 5 commands") {
		t.Errorf("a long fix %+v", a)
	}
}

// Exceptions that cannot be read fail closed (W-7, CF-99): the run's
// findings are not routed, and say why, so no case opens for what an
// exception may cover; the keys in use and New case's similar cases
// return the error.
func TestAFailedReadOfTheExceptionsOpensNoCaseCF99(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.svc.o.Lessons = lessons.New(lessons.Options{Backend: downKV{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return w.now }})
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/web", "No limits on web"))
	if !strings.HasPrefix(res.NotRouted, "the exceptions could not be read") || res.Findings[0].Case != "" || res.Routed.Opened != 0 {
		t.Fatalf("routed %+v", res)
	}
	if lines, err := w.iss.Index(ctx); err != nil || len(lines) != 0 {
		t.Fatalf("cases %+v %v", lines, err)
	}
	if _, err := w.svc.KeysInUse(ctx, "prod"); err == nil {
		t.Error("the keys in use without the exceptions")
	}
	if _, err := w.svc.SimilarTo(ctx, SimilarInput{Title: "web", Cluster: "prod", Resource: "shop/Pod/web"}); err == nil {
		t.Error("similar cases without the exceptions")
	}
}

// R2-12: with the cases' store down, the keys in use are an error, as
// for the exceptions, never a list without the cases.
func TestAFailedReadOfTheCasesGivesNoKeysR212(t *testing.T) {
	w := setup(t)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/web", "No limits on web"))
	w.svc.o.Issues = issues.New(issues.Options{Backend: downKV{}, Cases: w.objects, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return w.now }})
	keys, err := w.svc.KeysInUse(context.Background(), "prod")
	if err == nil || !strings.HasPrefix(err.Error(), "the cases could not be read: the store is down") || keys != nil {
		t.Fatalf("keys %+v, %v", keys, err)
	}
}

// Housekeeping puts right what a case's second write left behind (W-9,
// CF-100): a closed case's conversation and commands, its waiting
// changes, and an applied change its case does not record; each in the
// case's history and the audit log, once.
func TestHousekeepingPutsACaseRightWithItsWorkCF100(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	st := w.withCards(t)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	closed, err := w.svc.Create(ctx, NewCase{Title: "The shop is slow", Cluster: "prod"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Close(ctx, closed.ID, CloseInput{Outcome: issues.OutcomeNoAction, Explanation: "It went away."}, olivia); err != nil {
		t.Fatal(err)
	}
	// What the close's second writes would have done, left undone.
	if err := w.svc.updateWork(ctx, 2, func(wk *Work) error {
		wk.Commands = append(wk.Commands, Command{ID: "w1", Line: "kubectl get pods -n shop", Status: "done"}, Command{ID: "w2", Line: "kubectl rollout restart deployment/web -n shop", Change: true, Status: "done"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, keys, version, err := w.svc.caseOpen(ctx, 2); err != nil || keys == nil {
		t.Fatal(err)
	} else if _, err := w.svc.cases().Save(ctx, "2", ChatKey, []byte(`{"messages": [{}, {}]}`), version); err != nil {
		t.Fatal(err)
	}
	waiting := w.card("PC-2", cards.Queued, "C-2")
	waiting.Expires = w.now.Add(cards.Lifetime)
	// An applied change the open case did not record: its work says it
	// acted, the case does not name it.
	applied := w.card("PC-1", cards.Applied, "C-1")
	put(t, st, applied, waiting)
	if err := w.svc.updateWork(ctx, 1, func(wk *Work) error {
		wk.Proposals, wk.Handled = []string{"PC-1"}, map[string]string{"PC-1": cards.Applied}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	text, err := w.svc.Housekeep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"### Cases put right with their work",
		"- C-2: the case is closed: its conversation and the commands other than the changes that finished done were let go of",
		"- C-2: the case is closed: its waiting changes PC-2 were settled",
		"- C-1: the change PC-1 was applied, and the case did not record it: it does now"} {
		if !strings.Contains(text, line) {
			t.Errorf("the report lacks %q:\n%s", line, text)
		}
	}
	if chat, commands, err := w.svc.leftOpen(ctx, 2); err != nil || chat || commands != 0 {
		t.Errorf("still open: chat %v, commands %d, %v", chat, commands, err)
	}
	if c, _ := st.Get(ctx, "PC-2"); c.State != cards.Rejected {
		t.Errorf("the waiting change %+v", c.State)
	}
	c := w.get(t, "C-1")
	if c.AppliedBy == nil || c.AppliedBy.Card != "PC-1" || c.Status != issues.StatusInProgress || c.Verification != issues.VerifyWaiting {
		t.Errorf("the case that missed its change: %+v %+v", c.Brief, c.AppliedBy)
	}
	if !strings.Contains(c.History[len(c.History)-1].What, "Housekeeping: the change PC-1 was applied") {
		t.Errorf("history %+v", c.History)
	}
	if n := strings.Count(w.audit.String(), `"action":"case.reconcile"`); n != 3 {
		t.Errorf("audited %d times: %s", n, w.audit.String())
	}
	// Once: again, there is nothing to put right.
	if again, _ := w.svc.Housekeep(ctx); strings.Contains(again, "Cases put right") {
		t.Errorf("again:\n%s", again)
	}
}
