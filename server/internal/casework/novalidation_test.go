package casework

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// CF-109: a person may close an agent-found case as fixed without
// validation. It does not wait for a run of its job: none verifies it and
// none reopens it, and a later finding of the same problem is a new case,
// a regression of it.
func TestClosingAsFixedWithoutValidation(t *testing.T) {
	w := setup(t)
	st := w.withCards(t)
	ctx := context.Background()
	f := finding("pod.no-limits", "shop/Deployment/web", "web has no memory limit")
	w.route(t, "alert-diagnosis", true, f)
	// A change applied its fix: open, it waits for a run that re-checks it.
	w.applyTo(t, st, "C-1")
	if c := w.get(t, "C-1"); c.Stage != StageValidation || !w.report(t, "C-1").VerifyPending {
		t.Fatalf("applied, it does not wait for its validation run: stage %q", c.Stage)
	}
	v, err := w.svc.Fix(ctx, "C-1", FixInput{Text: "Set a memory limit of 512Mi.", NoValidation: true}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	r := w.report(t, "C-1")
	if v.Status != issues.StatusClosed || v.Outcome != issues.OutcomeFix || r.VerifyPending || r.Verified != nil || v.Stage != "" || v.VerifyWait != nil {
		t.Fatalf("closed without validation, it still waits or reads as verified: %+v, stage %q", v.Brief, v.Stage)
	}
	if !strings.Contains(historyOf(v), "Closed as fixed, without validation: Set a memory limit of 512Mi.") {
		t.Errorf("the history does not say it was closed without validation:\n%s", historyOf(v))
	}
	if !strings.Contains(w.audit.String(), "without validation, no run verifies it") {
		t.Errorf("the audit log does not say so: %s", w.audit.String())
	}
	// The agent is not asked to re-check it, and a re-check it gives all
	// the same neither verifies nor reopens the case.
	keys, err := w.svc.KeysInUse(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.Recheck {
			t.Errorf("still listed for a re-check: %+v", k)
		}
	}
	w.now = w.now.Add(time.Hour)
	w.routeWith(t, "alert-diagnosis", true, []jobs.Recheck{recheck("pod.no-limits", "shop/Deployment/web", true, "no limit is set")})
	if c := w.get(t, "C-1"); c.Status != issues.StatusClosed || w.report(t, "C-1").Reopened != 0 {
		t.Fatalf("a re-check reopened a case that does not wait: %+v", c.Brief)
	}
	// Found again, it is a new case that names this one.
	res := w.route(t, "alert-diagnosis", true, f)
	if res.Findings[0].Routing != jobs.RoutedRegression || res.Findings[0].Case == "C-1" {
		t.Fatalf("found again: %+v", res.Findings[0])
	}
	if c := w.get(t, "C-1"); c.Status != issues.StatusClosed {
		t.Fatalf("the case closed without validation was reopened: %+v", c.Brief)
	}
}

// The option says nothing on a case no run would re-check: a person's.
func TestWithoutValidationSaysNothingOfAPersonsCase(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	c, err := w.svc.Create(ctx, NewCase{Title: "The checkout page is slow"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	v, err := w.svc.Fix(ctx, c.ID, FixInput{Text: "Added an index.", NoValidation: true}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != issues.StatusClosed || strings.Contains(historyOf(v), "without validation") || !strings.Contains(historyOf(v), "Closed as fixed: Added an index.") {
		t.Errorf("a person's case: %+v\n%s", v.Brief, historyOf(v))
	}
}

// A case closed as fixed that waits for its run may stop waiting: it
// stays closed. One that does not wait has nothing to skip.
func TestSkippingTheValidationOfAClosedCase(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no memory limit"))
	if _, err := w.svc.SkipValidation(ctx, "C-1", olivia); !errors.Is(err, ErrState) {
		t.Fatalf("an open case: %v", err)
	}
	if _, err := w.svc.Fix(ctx, "C-1", FixInput{Text: "Set a memory limit."}, olivia); err != nil {
		t.Fatal(err)
	}
	if c := w.get(t, "C-1"); c.Stage != StageValidation {
		t.Fatalf("closed as fixed, it does not wait: stage %q", c.Stage)
	}
	v, err := w.svc.SkipValidation(ctx, "C-1", olivia)
	if err != nil {
		t.Fatal(err)
	}
	if r := w.report(t, "C-1"); v.Status != issues.StatusClosed || v.Outcome != issues.OutcomeFix || r.VerifyPending || v.Stage != "" {
		t.Fatalf("skipped, it still waits: %+v, stage %q", v.Brief, v.Stage)
	}
	if !strings.Contains(historyOf(v), "Validation skipped: no run re-checks the fix.") || !strings.Contains(messages(v), "Validation skipped: no run re-checks the fix.") {
		t.Errorf("not said on the case:\n%s\n%s", historyOf(v), messages(v))
	}
	if !strings.Contains(w.audit.String(), ActionSkipValidation) {
		t.Errorf("not in the audit log: %s", w.audit.String())
	}
	if _, err := w.svc.SkipValidation(ctx, "C-1", olivia); !errors.Is(err, ErrState) {
		t.Fatalf("skipped twice: %v", err)
	}
	// The next run that still sees it opens a new case; this one stays.
	w.now = w.now.Add(24 * time.Hour)
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no memory limit"))
	if res.Findings[0].Routing != jobs.RoutedRegression || w.get(t, "C-1").Status != issues.StatusClosed {
		t.Fatalf("found again after the validation was skipped: %+v", res.Findings[0])
	}
}

// Several cases closed as fixed together take the option too.
func TestBulkFixWithoutValidation(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no memory limit"), finding("pod.no-probes", "shop/Deployment/api", "api has no probes"))
	out, err := w.svc.Bulk(ctx, BulkInput{Cases: []string{"C-1", "C-2"}, Action: BulkFix, Text: "Set limits and probes.", NoValidation: true}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out {
		if !r.OK {
			t.Fatalf("not fixed: %+v", r)
		}
	}
	for _, id := range []string{"C-1", "C-2"} {
		if r := w.report(t, id); r.Status != issues.StatusClosed || r.VerifyPending {
			t.Errorf("%s waits for a validation run: %+v", id, r)
		}
	}
}
