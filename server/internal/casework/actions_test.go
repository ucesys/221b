package casework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
)

// A fix needs what was done, and its RCA at P0-P1, which is the case's,
// its fix what was done (CF-44); the case closes as fix (CF-03).
func TestFixAndItsRCA(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	p1, _ := w.svc.Create(ctx, NewCase{Title: "Checkout down", Cluster: "prod", Priority: pr(1)}, olivia)
	if _, err := w.svc.Fix(ctx, p1.ID, FixInput{}, olivia); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no text: %v", err)
	}
	if _, err := w.svc.Fix(ctx, p1.ID, FixInput{Text: "Restarted"}, olivia); !errors.Is(err, ErrRCARequired) {
		t.Fatalf("a P1 fix without an RCA: %v", err)
	}
	if _, err := w.svc.Fix(ctx, p1.ID, FixInput{Text: "Restarted", RCA: &Completion{Prevention: "x"}}, olivia); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an RCA without a cause: %v", err)
	}
	// A fix given in the RCA is left out: the RCA's fix is what was done.
	v, err := w.svc.Fix(ctx, p1.ID, FixInput{Text: "Raised the limit", RCA: &Completion{Cause: "The limit was low. password=hunter2", Fix: "something else", Prevention: "Load tests"}}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != issues.StatusClosed || v.Outcome != issues.OutcomeFix || v.RCA != "" || v.RCADetail == nil || v.FixApplied == nil || !v.MayReopen || len(v.AllowedOutcomes) != 0 || v.Explanation != "Raised the limit" {
		t.Fatalf("fixed: %+v", v.Brief)
	}
	if r := v.RCADetail; r.ID != "" || r.Case != p1.ID || strings.Contains(r.Cause, "hunter2") || r.Fix != "Raised the limit" || r.Prevention != "Load tests" || !strings.HasPrefix(v.RCACause, "The limit was low.") {
		t.Fatalf("the RCA: %+v %q", r, v.RCACause)
	}
	if raw, _, _ := w.lessons.Backend().LoadKey(ctx, lessons.RCAsKey); len(raw) != 0 {
		t.Fatalf("an RCA kept outside its case: %s", raw)
	}
	if strings.Contains(w.audit.String(), "rca.add") || !strings.Contains(w.audit.String(), "closed as fixed, with its RCA") {
		t.Fatalf("audit: %s", w.audit.String())
	}
	// At P2 the RCA is optional.
	p2, _ := w.svc.Create(ctx, NewCase{Title: "Slow", Priority: pr(2)}, olivia)
	if v, err := w.svc.Fix(ctx, p2.ID, FixInput{Text: "Cleared the cache"}, olivia); err != nil || v.RCADetail != nil || v.RCACause != "" {
		t.Fatalf("a P2 fix: %+v %v", v.Brief, err)
	}
	if _, err := w.svc.Fix(ctx, p2.ID, FixInput{Text: "again"}, olivia); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a closed case fixed again: %v", err)
	}
	if !strings.Contains(w.audit.String(), `"action":"case.fix"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// Close takes duplicate, not a problem and no fix needed, with an
// explanation always; a duplicate of a closed original is refused with
// what fits instead (409 with the check through the API); not a problem
// is no outcome for an agent's finding (CF-03, CF-04).
func TestCloseAndTheDuplicateRule(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	a, _ := w.svc.Create(ctx, NewCase{Title: "Shop slow"}, olivia)
	b, _ := w.svc.Create(ctx, NewCase{Title: "Shop slow too"}, olivia)
	fixed, _ := w.svc.Create(ctx, NewCase{Title: "Fixed one"}, olivia)
	w.svc.Fix(ctx, fixed.ID, FixInput{Text: "done"}, olivia)
	for _, in := range []CloseInput{{Outcome: issues.OutcomeNoFixNeeded}, {Outcome: issues.OutcomeFix, Explanation: "x"}, {Outcome: issues.OutcomeDuplicate, Explanation: "x"}, {Outcome: issues.OutcomeDuplicate, Explanation: "x", Original: b.ID + "x"}} {
		if _, err := w.svc.Close(ctx, a.ID, in, olivia); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	_, err := w.svc.Close(ctx, a.ID, CloseInput{Outcome: issues.OutcomeDuplicate, Explanation: "the same", Original: fixed.ID}, olivia)
	var d *DuplicateError
	if !errors.As(err, &d) || d.Check.Allowed || len(d.Check.Offers) != 2 || d.Check.Offers[0].Kind != issues.OfferReopenOriginal {
		t.Fatalf("a closed original: %v", err)
	}
	if _, err := w.svc.Close(ctx, a.ID, CloseInput{Outcome: issues.OutcomeDuplicate, Explanation: "the same", Original: a.ID}, olivia); !errors.As(err, &d) || !strings.Contains(d.Check.Reason, "own original") {
		t.Fatalf("itself: %v", err)
	}
	v, err := w.svc.Close(ctx, a.ID, CloseInput{Outcome: issues.OutcomeDuplicate, Explanation: "the same", Original: b.ID}, olivia)
	if err != nil || v.Outcome != issues.OutcomeDuplicate || v.DuplicateOf != b.ID {
		t.Fatalf("a duplicate: %+v %v", v.Brief, err)
	}
	if bv := w.get(t, b.ID); !strings.Contains(bv.History[len(bv.History)-1].What, a.ID+" (Shop slow) was closed as a duplicate") {
		t.Fatalf("the original's history: %+v", bv.History)
	}
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/web", "No limits"))
	ag := res.Findings[0].Case
	if _, err := w.svc.Close(ctx, ag, CloseInput{Outcome: issues.OutcomeNotAProblem, Explanation: "fine"}, olivia); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not a problem for an agent's finding: %v", err)
	}
	// An agent-found case may be a duplicate of an open original (CF-42).
	if dc, err := w.svc.DuplicateCheck(ctx, ag, b.ID, olivia); err != nil || !dc.Allowed {
		t.Fatalf("an agent's duplicate of an open case: %+v %v", dc, err)
	}
	if v, err := w.svc.Close(ctx, ag, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "One-off"}, olivia); err != nil || v.Outcome != issues.OutcomeNoFixNeeded {
		t.Fatalf("no fix needed: %+v %v", v.Brief, err)
	}
	// The check, as the UI asks it.
	chk, err := w.svc.DuplicateCheck(ctx, b.ID, fixed.ID, olivia)
	if err != nil || chk.Allowed || chk.Original != fixed.ID || chk.Outcome != issues.OutcomeFix {
		t.Fatalf("check: %+v %v", chk, err)
	}
}

// What a duplicate check offers instead is applied on the server, which
// checks again that the original still offers it (CF-04).
func TestTheOffers(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	mk := func(title string) string {
		v, _ := w.svc.Create(ctx, NewCase{Title: title}, olivia)
		return v.ID
	}
	// reopenOriginal: the original reopens, this one closes as its duplicate.
	fx := mk("fixed")
	w.svc.Fix(ctx, fx, FixInput{Text: "done"}, olivia)
	me := mk("me")
	v, err := w.svc.Offer(ctx, me, issues.Offer{Kind: issues.OfferReopenOriginal, Original: fx}, olivia)
	if err != nil || v.Outcome != issues.OutcomeDuplicate || v.DuplicateOf != fx {
		t.Fatalf("reopen the original: %+v %v", v.Brief, err)
	}
	o := w.get(t, fx)
	held := false
	for _, m := range o.Messages {
		held = held || strings.Contains(m.Text, "did not hold")
	}
	if o.Status == issues.StatusClosed || !held {
		t.Fatalf("the original: %+v %+v", o.Brief, o.Messages)
	}
	// regression: kept open, marked.
	fx2 := mk("fixed two")
	w.svc.Fix(ctx, fx2, FixInput{Text: "done"}, olivia)
	me2 := mk("me two")
	if v, err := w.svc.Offer(ctx, me2, issues.Offer{Kind: issues.OfferRegression, Original: fx2}, olivia); err != nil || v.Status.Open() != true || v.RegressionOf != fx2 {
		t.Fatalf("regression: %+v %v", v.Brief, err)
	}
	// An offer the original does not make is refused with the check.
	var d *DuplicateError
	if _, err := w.svc.Offer(ctx, me2, issues.Offer{Kind: issues.OfferSameAs, Original: fx2}, olivia); !errors.As(err, &d) {
		t.Fatalf("an offer not made: %v", err)
	}
	// notAProblem: with the original's explanation.
	np := mk("not a problem")
	w.svc.Close(ctx, np, CloseInput{Outcome: issues.OutcomeNotAProblem, Explanation: "Works as designed."}, olivia)
	me3 := mk("me three")
	if v, err := w.svc.Offer(ctx, me3, issues.Offer{Kind: issues.OfferNotAProblem, Original: np}, olivia); err != nil || v.Outcome != issues.OutcomeNotAProblem || v.Explanation != "Works as designed." {
		t.Fatalf("not a problem: %+v %v", v.Brief, err)
	}
	// sameAs: linked, kept open.
	nf := mk("no fix")
	w.svc.Close(ctx, nf, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "transient"}, olivia)
	me4 := mk("me four")
	if v, err := w.svc.Offer(ctx, me4, issues.Offer{Kind: issues.OfferSameAs, Original: nf}, olivia); err != nil || !v.Pending || v.SameAs != nf {
		t.Fatalf("same as: %+v %v", v.Brief, err)
	}
	// sameKnownIssue: closed as the original's known issue.
	ki := mk("known")
	w.svc.Known(ctx, ki, KnownLink{Create: &NewKnown{Title: "Known thing"}}, olivia)
	kid := w.get(t, ki).KnownIssue
	me5 := mk("me five")
	if v, err := w.svc.Offer(ctx, me5, issues.Offer{Kind: issues.OfferSameKnownIssue, Original: ki}, olivia); err != nil || v.Outcome != issues.OutcomeKnownIssue || v.KnownIssue != kid {
		t.Fatalf("same known issue: %+v %v", v.Brief, err)
	}
	// removeException: by an approver only; the case stays open.
	res := w.route(t, "nightly-audit", true, finding("pod.privileged", "ci/Pod/runner", "Privileged"))
	ex := res.Findings[0].Case
	w.svc.Exception(ctx, ex, ExceptionInput{Reason: "CI needs it"}, olivia)
	me6 := mk("me six")
	if _, err := w.svc.Offer(ctx, me6, issues.Offer{Kind: issues.OfferRemoveException, Original: ex}, otto); !errors.Is(err, ErrDenied) {
		t.Fatalf("not an approver: %v", err)
	}
	if v, err := w.svc.Offer(ctx, me6, issues.Offer{Kind: issues.OfferRemoveException, Original: ex}, olivia); err != nil || !v.Pending {
		t.Fatalf("remove the exception: %+v %v", v.Brief, err)
	}
	if e, _ := w.lessons.Exception(ctx, "EX-1"); e.RemovedAt.IsZero() {
		t.Fatal("the exception stays")
	}
	if !strings.Contains(w.audit.String(), `"action":"case.offer"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// A known issue closes a case (CF-03): an existing one or one made from
// the case; an exception closes an agent's finding, by approvers only,
// with a reason and an optional last day; reopening takes a reason, and
// removes the exception (CF-03, CF-02).
func TestKnownIssueExceptionAndReopen(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	h, _ := w.svc.Create(ctx, NewCase{Title: "Uploads fail over 10 MB"}, olivia)
	v, err := w.svc.Known(ctx, h.ID, KnownLink{Create: &NewKnown{Title: "", Workaround: "Split the file"}}, olivia)
	if err != nil || v.Outcome != issues.OutcomeKnownIssue || v.KnownIssue != "KI-1" || v.Known == nil || v.Known.Workaround != "Split the file" {
		t.Fatalf("a known issue made: %+v %v", v.Brief, err)
	}
	if k, _ := w.iss.KnownIssue(ctx, system, "KI-1"); k.Title != "Uploads fail over 10 MB" {
		t.Fatalf("the known issue: %+v", k)
	}
	if _, err := w.svc.Known(ctx, h.ID, KnownLink{ID: "KI-1"}, olivia); !errors.Is(err, ErrState) {
		t.Fatalf("closed already: %v", err)
	}
	h2, _ := w.svc.Create(ctx, NewCase{Title: "Same uploads"}, olivia)
	if v, err := w.svc.Known(ctx, h2.ID, KnownLink{ID: "KI-1"}, olivia); err != nil || v.KnownIssue != "KI-1" {
		t.Fatalf("linked: %+v %v", v.Brief, err)
	}
	if kv, _ := w.iss.Known(ctx, system, true); len(kv) != 1 {
		t.Fatal("a second known issue")
	}
	// Exceptions: an agent's finding only, approvers only, a reason.
	if _, err := w.svc.Exception(ctx, h2.ID, ExceptionInput{Reason: "x"}, olivia); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a person's case: %v", err)
	}
	res := w.route(t, "nightly-audit", true, finding("pod.privileged", "ci/Pod/runner", "Privileged runner"))
	ag := res.Findings[0].Case
	if _, err := w.svc.Exception(ctx, ag, ExceptionInput{Reason: "x"}, otto); !errors.Is(err, ErrDenied) {
		t.Fatalf("not an approver: %v", err)
	}
	for _, in := range []ExceptionInput{{}, {Reason: "x", Expires: "31/12/2026"}} {
		if _, err := w.svc.Exception(ctx, ag, in, olivia); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	v, err = w.svc.Exception(ctx, ag, ExceptionInput{Reason: "The CI runner builds images.", Expires: "2027-01-31"}, olivia)
	if err != nil || v.Outcome != issues.OutcomeException || v.Exception != "EX-1" || v.Explanation != "The CI runner builds images." {
		t.Fatalf("an exception: %+v %v", v.Brief, err)
	}
	e, _ := w.lessons.Exception(ctx, "EX-1")
	if e.Cluster != "prod" || e.Resource != "ci/Pod/runner" || e.Check != "pod.privileged" || e.Case != ag || e.Expires.Format("2006-01-02") != "2027-01-31" {
		t.Fatalf("the exception: %+v", e)
	}
	// Reopening: a reason; the exception goes, by an approver.
	if _, err := w.svc.Reopen(ctx, ag, "", olivia); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := w.svc.Reopen(ctx, ag, "It should not be privileged", otto); !errors.Is(err, ErrDenied) {
		t.Fatalf("not an approver: %v", err)
	}
	v, err = w.svc.Reopen(ctx, ag, "It should not be privileged", olivia)
	if err != nil || v.Status != issues.StatusNew || v.Outcome != "" || v.Exception != "" || len(v.AllowedOutcomes) == 0 || !strings.Contains(v.History[len(v.History)-1].What, "EX-1 is removed") {
		t.Fatalf("reopened: %+v %v", v.Brief, err)
	}
	if e, _ = w.lessons.Exception(ctx, "EX-1"); e.Active(w.now) {
		t.Fatal("the exception stays")
	}
	if _, err := w.svc.Reopen(ctx, ag, "again", olivia); !errors.Is(err, ErrState) {
		t.Fatalf("an open case reopened: %v", err)
	}
	for _, a := range []string{"case.known", "case.exception", "case.reopen", "exception.add", "exception.remove"} {
		if !strings.Contains(w.audit.String(), `"action":"`+a+`"`) {
			t.Errorf("no %s in the audit log", a)
		}
	}
}

// New Case shows what is like the case being typed (CF-12): an open case
// on the same object, a known issue it matches, an exception on the same
// object, and cases closed lately as not a problem, no fix needed or
// fixed (with its RCA); Add to C-n follows it and comments; a case may be
// created as a regression, or closed as a known issue at once.
func TestNewCaseSimilarAndJoin(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	open, _ := w.svc.Create(ctx, NewCase{Title: "Checkout returns 502", Cluster: "prod", Resource: "shop/Deployment/checkout"}, olivia)
	np, _ := w.svc.Create(ctx, NewCase{Title: "Checkout button grey", Cluster: "prod"}, olivia)
	w.svc.Close(ctx, np.ID, CloseInput{Outcome: issues.OutcomeNotAProblem, Explanation: "Grey while the cart loads."}, olivia)
	fx, _ := w.svc.Create(ctx, NewCase{Title: "Checkout slow at night", Cluster: "prod", Priority: pr(1)}, olivia)
	w.svc.Fix(ctx, fx.ID, FixInput{Text: "Moved the backup", RCA: &Completion{Cause: "The backup ran at peak"}}, olivia)
	old, _ := w.svc.Create(ctx, NewCase{Title: "Checkout grey long ago", Cluster: "prod"}, olivia)
	w.svc.Close(ctx, old.ID, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "old"}, olivia)
	w.iss.SaveKnown(ctx, olivia.issue(), "", issues.KnownInput{Title: ptrS("Checkout fails with 502 errors"), Symptoms: []string{"checkout returns 502 bad gateway"}, Workaround: ptrS("Retry")})
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/checkout", "No limits"))
	w.svc.Exception(ctx, res.Findings[0].Case, ExceptionInput{Reason: "Load-tested"}, olivia)
	// The look-back: a case closed long ago is not shown.
	w.now = w.now.Add(24 * time.Hour)
	w.svc.SaveSettings(ctx, Settings{KeepAgentClosedDays: 90, LookBackDays: 2, ExceptionStaleDays: 90, ExceptionsPerAgent: 50, NewCasesPerRun: 50}, sam)
	w.iss.Apply(ctx, 4, func(tx *issues.Tx, r *issues.Report) error {
		at := w.now.Add(-72 * time.Hour)
		r.Closed = &at
		return nil
	})
	sim, err := w.svc.SimilarTo(ctx, SimilarInput{Title: "Checkout returns 502 bad gateway", Cluster: "prod", Resource: "shop/deploy/checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sim.Open) == 0 || sim.Open[0].ID != open.ID || sim.Open[0].Score != 100 {
		t.Errorf("open: %+v", sim.Open)
	}
	if len(sim.Known) != 1 || sim.Known[0].ID != "KI-1" || sim.Known[0].Workaround != "Retry" {
		t.Errorf("known: %+v", sim.Known)
	}
	if len(sim.Exceptions) != 1 || sim.Exceptions[0].Reason != "Load-tested" || sim.Exceptions[0].By == "" {
		t.Errorf("exceptions: %+v", sim.Exceptions)
	}
	sim, _ = w.svc.SimilarTo(ctx, SimilarInput{Title: "Checkout button grey and slow at night", Cluster: "prod"})
	var noProblem, fixedOnes []string
	for _, c := range sim.RecentNoProblem {
		noProblem = append(noProblem, c.ID+" "+c.Explanation)
	}
	for _, c := range sim.RecentFixed {
		fixedOnes = append(fixedOnes, c.ID+" "+c.RCACause)
	}
	if fmt.Sprint(noProblem) != "["+np.ID+" Grey while the cart loads.]" || fmt.Sprint(fixedOnes) != "["+fx.ID+" The backup ran at peak]" {
		t.Errorf("recent: %v %v", noProblem, fixedOnes)
	}
	if empty, _ := w.svc.SimilarTo(ctx, SimilarInput{}); len(empty.Open) != 0 || empty.Known == nil {
		t.Errorf("nothing typed: %+v", empty)
	}
	// Add to C-n.
	if _, err := w.svc.Join(ctx, open.ID, " ", sam); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty comment: %v", err)
	}
	v, err := w.svc.Join(ctx, open.ID, "I see it too, from the EU.", sam)
	if err != nil || len(v.Messages) != 1 || !v.Messages[0].Internal || v.Messages[0].From.ID != "sam" {
		t.Fatalf("joined: %+v %v", v.Messages, err)
	}
	if r := w.report(t, open.ID); len(r.Watchers) != 1 || r.Watchers[0].ID != "sam" {
		t.Fatalf("followers: %+v", r.Watchers)
	}
	// Create as a regression, or closed as a known issue.
	reg, err := w.svc.Create(ctx, NewCase{Title: "Slow again", RegressionOf: fx.ID}, olivia)
	if err != nil || reg.RegressionOf != fx.ID || !reg.Pending {
		t.Fatalf("a regression: %+v %v", reg.Brief, err)
	}
	ki, err := w.svc.Create(ctx, NewCase{Title: "502 on checkout", KnownIssue: "KI-1"}, olivia)
	if err != nil || ki.Status != issues.StatusClosed || ki.Outcome != issues.OutcomeKnownIssue || ki.KnownIssue != "KI-1" {
		t.Fatalf("closed as a known issue: %+v %v", ki.Brief, err)
	}
}

func ptrS(s string) *string { return &s }

// Settings › Cases (CF-19): a SuperAdmin changes them, within their
// ranges; the retention deletes closed agent-found cases past
// keepAgentClosedDays, once a day (CF-21).
func TestSettingsAndRetention(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if set, _ := w.svc.Settings(ctx); !reflect.DeepEqual(set, DefaultSettings()) {
		t.Fatalf("defaults %+v", set)
	}
	bad := DefaultSettings()
	if _, err := w.svc.SaveSettings(ctx, bad, olivia); !errors.Is(err, ErrDenied) {
		t.Fatalf("not a SuperAdmin: %v", err)
	}
	for _, f := range []func(*Settings){func(s *Settings) { s.KeepAgentClosedDays = 0 }, func(s *Settings) { s.LookBackDays = 3651 }, func(s *Settings) { s.NewCasesPerRun = 501 }, func(s *Settings) { s.ExceptionsPerAgent = -1 }} {
		s := DefaultSettings()
		f(&s)
		if _, err := w.svc.SaveSettings(ctx, s, sam); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", s, err)
		}
	}
	set := DefaultSettings()
	set.KeepAgentClosedDays = 10
	if got, err := w.svc.SaveSettings(ctx, set, sam); err != nil || got.KeepAgentClosedDays != 10 {
		t.Fatalf("saved %+v %v", got, err)
	}
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/a", "a"), finding("pod.no-limits", "shop/Pod/b", "b"))
	h, _ := w.svc.Create(ctx, NewCase{Title: "A person's"}, olivia)
	for _, id := range []string{res.Findings[0].Case, h.ID} {
		w.svc.Close(ctx, id, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "x"}, olivia)
	}
	w.svc.Tick(ctx)
	if names, _ := w.objects.List(ctx); len(names) != 3 {
		t.Fatalf("deleted too soon: %v", names)
	}
	w.now = w.now.Add(11 * 24 * time.Hour)
	w.svc.Tick(ctx)
	names, _ := w.objects.List(ctx)
	if fmt.Sprint(names) != "[2 3]" {
		t.Fatalf("left %v", names)
	}
	if _, err := w.svc.Get(ctx, olivia, res.Findings[0].Case); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted case: %v", err)
	}
	if !strings.Contains(w.audit.String(), `"action":"case.retention"`) || !strings.Contains(w.audit.String(), `"action":"case.settings"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// The SLA targets are keyed 0 to 4, set by a SuperAdmin (CU-06, CF-02).
func TestTheSLA(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	sla, err := w.svc.SLA(ctx)
	if err != nil || len(sla) != 5 || sla["4"].ResponseMinutes == 0 || sla["0"].ResolveMinutes != 240 {
		t.Fatalf("%+v %v", sla, err)
	}
	sla["1"] = issues.Target{ResponseMinutes: 30, ResolveMinutes: 1}
	if _, err := w.svc.SaveSLA(ctx, sla, olivia); !errors.Is(err, ErrDenied) {
		t.Errorf("not a SuperAdmin: %v", err)
	}
	if got, err := w.svc.SaveSLA(ctx, sla, sam); err != nil || got["1"].ResolveMinutes != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	sla["5"] = issues.Target{}
	if _, err := w.svc.SaveSLA(ctx, sla, sam); !errors.Is(err, ErrInvalid) {
		t.Errorf("5: %v", err)
	}
	if _, err := w.svc.Create(ctx, NewCase{Title: "Blocking", Priority: pr(1)}, olivia); err != nil {
		t.Fatal(err)
	}
	v := w.get(t, "C-1")
	if v.SLA == nil || v.SLA.Target.ResolveMinutes != 1 || v.SLA.ResolveDue == nil || !v.SLAApplies {
		t.Errorf("%+v", v.SLA)
	}
}

// A backup holds every case's ConfigMap whole, Settings › Cases and the
// checks seen; a restore adds what is missing and keeps what is here
// (CF-22).
func TestBackupOfTheCases(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.svc.Create(ctx, NewCase{Title: "One", Detail: "first"}, olivia)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/a", "a"))
	w.svc.Keep(ctx, "C-1", DraftInput{Reply: "half"}, olivia)
	set := DefaultSettings()
	set.NewCasesPerRun = 7
	w.svc.SaveSettings(ctx, set, sam)
	x, err := w.svc.Export(ctx)
	if err != nil || x == nil || len(x.Cases) != 2 || x.Settings.NewCasesPerRun != 7 || len(x.Checks["prod"]) != 1 || x.Cases["1"][issues.WorkKey] == "" {
		t.Fatalf("export %+v %v", x, err)
	}
	raw, _ := json.Marshal(x)
	g := setup(t)
	g.svc.Create(ctx, NewCase{Title: "Here already"}, olivia)
	added, kept, err := g.svc.Import(ctx, raw, "root")
	if err != nil || added != 1 || kept != 1 {
		t.Fatalf("import %d %d %v", added, kept, err)
	}
	if v := g.get(t, "C-2"); !v.AgentFound || v.Key == nil {
		t.Fatalf("restored %+v", v.Brief)
	}
	if v := g.get(t, "C-1"); v.Title != "Here already" {
		t.Fatalf("kept %+v", v.Brief)
	}
	if s, _ := g.svc.Settings(ctx); s.NewCasesPerRun != 7 {
		t.Fatalf("settings %+v", s)
	}
	if c, _ := g.svc.Create(ctx, NewCase{Title: "Next"}, olivia); c.Number != 3 {
		t.Fatalf("numbered %d", c.Number)
	}
}

// The HTTP API (the contract): routes and JSON names, who may do what,
// and a refused duplicate's 409 with its check.
func TestTheHTTPContract(t *testing.T) {
	w := setup(t)
	caller := olivia
	mux := http.NewServeMux()
	for _, rt := range w.svc.Routes(func(*http.Request) (Caller, bool) { return caller, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := do("POST", Prefix, `{"title": "Shop slow", "cluster": "prod", "resource": "shop/deploy/web", "priority": 0}`)
	if code != http.StatusCreated || out["id"] != "C-1" || out["priority"] != float64(0) || out["status"] != "new" || out["agentFound"] != false || out["slaApplies"] != true ||
		out["rcaRequired"] != true || out["mayReopen"] != false || out["related"] == nil || len(out["allowedOutcomes"].([]any)) != 5 || out["resource"] != "shop/Deployment/web" {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, _ := do("POST", Prefix, `{"title": "x", "fromFinding": "run-1.1"}`); code != http.StatusBadRequest {
		t.Fatalf("fromFinding is gone: %d", code)
	}
	do("POST", Prefix, `{"title": "Fixed"}`)
	do("POST", Prefix+"/C-2/fix", `{"text": "done"}`)
	code, out = do("POST", Prefix+"/C-1/close", `{"outcome": "duplicate", "explanation": "same", "original": "C-2"}`)
	check, _ := out["check"].(map[string]any)
	if code != http.StatusConflict || check == nil || check["allowed"] != false || check["original"] != "C-2" || len(check["offers"].([]any)) != 2 {
		t.Fatalf("409: %d %v", code, out)
	}
	if code, out := do("GET", Prefix+"/C-1/duplicate-check?original=C-2", ""); code != http.StatusOK || out["status"] != "closed" || out["outcome"] != "fix" {
		t.Fatalf("duplicate-check: %d %v", code, out)
	}
	if code, out := do("POST", Prefix+"/C-1/offer", `{"kind": "regression", "original": "C-2"}`); code != http.StatusOK || out["regressionOf"] != "C-2" {
		t.Fatalf("offer: %d %v", code, out)
	}
	if code, out := do("POST", Prefix+"/C-1/fix", `{"text": "done"}`); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "RCA") {
		t.Fatalf("a P0 fix without an RCA: %d %v", code, out)
	}
	if code, out := do("POST", Prefix+"/C-1/join", `{"text": "me too"}`); code != http.StatusOK || len(out["messages"].([]any)) != 1 {
		t.Fatalf("join: %d %v", code, out)
	}
	if code, _ := do("POST", Prefix+"/C-1/complete", `{"cause": "x"}`); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("complete is gone: %d", code)
	}
	if code, out := do("POST", SimilarPath, `{"title": "Shop slow", "cluster": "prod", "resource": "shop/Deployment/web"}`); code != http.StatusOK || len(out["open"].([]any)) != 1 || out["recentFixed"] == nil {
		t.Fatalf("similar: %d %v", code, out)
	}
	if code, out := do("GET", SettingsPath, ""); code != http.StatusOK || out["newCasesPerRun"] != float64(50) || out["keepAgentClosedDays"] != float64(90) {
		t.Fatalf("settings: %d %v", code, out)
	}
	if code, _ := do("PUT", SettingsPath, `{"keepAgentClosedDays": 30, "lookBackDays": 30, "exceptionStaleDays": 30, "exceptionsPerAgent": 5, "newCasesPerRun": 5}`); code != http.StatusForbidden {
		t.Fatalf("settings by an approver: %d", code)
	}
	if code, out := do("GET", SLAPath, ""); code != http.StatusOK || out["targets"].(map[string]any)["0"] == nil {
		t.Fatalf("sla: %d %v", code, out)
	}
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/a", "a"))
	if code, out := do("GET", Prefix+"?source=agents", ""); code != http.StatusOK || len(out["cases"].([]any)) != 1 {
		t.Fatalf("agents: %d %v", code, out)
	}
	if code, out := do("GET", Prefix+"?source=people&status=closed&outcome=fix", ""); code != http.StatusOK || len(out["cases"].([]any)) != 1 {
		t.Fatalf("people, closed as fix: %d %v", code, out)
	}
	if code, out := do("GET", Prefix+"?priority=0", ""); code != http.StatusOK || len(out["cases"].([]any)) != 1 {
		t.Fatalf("P0: %d %v", code, out)
	}
	caller = otto
	if code, _ := do("POST", Prefix+"/C-3/exception", `{"reason": "fine"}`); code != http.StatusForbidden {
		t.Fatalf("an exception by a triager: %d", code)
	}
	caller = Caller{Person: Person{Name: "Viv"}, ID: "viv", Read: true}
	if code, _ := do("POST", Prefix+"/C-3/reopen", `{"reason": "x"}`); code != http.StatusForbidden {
		t.Fatalf("a reader changes a case: %d", code)
	}
	if code, out := do("GET", Prefix+"/C-3", ""); code != http.StatusOK || out["key"] == nil || out["seen"] == nil || out["mayChange"] != false {
		t.Fatalf("a reader reads: %d %v", code, out)
	}
}

// The clean start deletes the Cases area's records and nothing else:
// every key outside the list stays byte for byte; a dry run deletes
// nothing and leaves no marker; the same token again does nothing; the SLA
// targets are renumbered P1-P5 to 0-4 with their values (CF-23).
func TestTheCleanStartTouchesOnlyTheCasesArea(t *testing.T) {
	ctx := context.Background()
	type cm struct {
		name string
		mem  *kv.Memory
		keys map[string]string
	}
	maps := map[string]*cm{}
	add := func(name string, keys map[string]string) *kv.Memory {
		m := &kv.Memory{}
		for k, v := range keys {
			m.SaveKey(ctx, k, []byte(v), "")
		}
		maps[name] = &cm{name: name, mem: m, keys: keys}
		return m
	}
	issuesCM := add("issues", map[string]string{
		issues.IndexKey: `{"next":3,"cases":[{"number":1},{"number":2}]}`,
		issues.KnownKey: `{"next":2,"known":[{"id":"KI-1"}]}`,
		// What a build before wave 6 kept: the reports, every case.
		"issues-reports.json": `{"next":5,"reports":[{"number":1},{"number":4}]}`,
		issues.ConfigKey:      `{"settings":{"company":"Example","targets":{"P1":{"responseMinutes":15,"resolveMinutes":240},"P5":{"responseMinutes":2880,"resolveMinutes":0}}},"services":[{"id":"shop"}],"tokens":[{"id":"t1"}]}`,
	})
	caseworkCM := add("casework", map[string]string{
		"exceptions.json": `{"next":1,"items":[{"id":"EX-1"}]}`,
		"cases.json":      `{"next":1,"work":{"1":{}}}`,
		"rcas.json":       `{"next":1,"items":[{"id":"RCA-1"}]}`,
		ChecksKey:         `{"clusters":{"prod":["pod.no-limits"]}}`,
		SettingsKey:       `{"settings":{"keepAgentClosedDays":30,"lookBackDays":90,"exceptionStaleDays":90,"exceptionsPerAgent":50,"newCasesPerRun":50},"duplicatesOfAgentCases":4,"analyzing":{"1":"2026-11-01T00:00:00Z"}}`,
	})
	runsCM := add("runs", map[string]string{
		"runs.json": `{"next":2,"runs":[{"id":"run-1","findings":[{"id":"run-1.1"}]},{"id":"run-2"}]}`,
		"jobs.json": `{"jobs":{"nightly-audit":{"id":"nightly-audit","task":"my own instructions","on":true}},"settings":{"editors":["admin"]}}`,
	})
	cardsCM := add("cards", map[string]string{
		"cards.json":          `{"next":5,"cards":[{"id":"PC-1","source":{"kind":"job"}},{"id":"PC-2","source":{"kind":"chat"}},{"id":"PC-3","source":{"kind":"case","id":"C-1"}},{"id":"PC-4","source":{"kind":"alert"}},{"id":"PC-5","source":{"kind":"knowledge"}}]}`,
		"cards-standing.json": `{"standing":[{"id":"s1"}]}`,
		"cards-notify.json":   `{"channels":[{"id":"n1"}]}`,
	})
	knowledgeCM := add("knowledge", map[string]string{
		"knowledge-settings.json": `{"sources":[{"id":"src"}]}`,
		"knowledge-drafts.json":   `{"runs":[{"id":"r1"}]}`,
		"knowledge-status.json":   `{"sources":{}}`,
		"confluence.json":         `{"space":"OPS"}`,
	})
	// A ConfigMap the clean start is never given: everything outside Cases.
	settingsCM := add("settings", map[string]string{"settings.json": `{"fleets":[]}`, "ai-providers.json": `{"providers":[]}`})
	objs := &kv.MemoryObjects{}
	objs.Save(ctx, "1", issues.CaseKey, []byte(`{"number":1}`), "")
	objs.Save(ctx, "2", issues.CaseKey, []byte(`{"number":2}`), "")
	var wiped []bool
	st := CleanStores{Issues: issuesCM, Cases: objs, Casework: caseworkCM, Runs: runsCM, Cards: cardsCM, Knowledge: knowledgeCM,
		Wiki: func(_ context.Context, dry bool) ([]string, error) {
			wiped = append(wiped, dry)
			return []string{"pages/a.md", "excerpts/b.md"}, nil
		}}
	logs := &strings.Builder{}
	log := slogTo(logs)
	snapshot := func() map[string]string {
		out := map[string]string{}
		for name, c := range maps {
			for k := range c.keys {
				if b, _, err := c.mem.LoadKey(ctx, k); err == nil {
					out[name+"/"+k] = string(b)
				}
			}
			for _, k := range []string{CleanKey} {
				if b, _, err := c.mem.LoadKey(ctx, k); err == nil {
					out[name+"/"+k] = string(b)
				}
			}
		}
		return out
	}
	before := snapshot()
	// No token: nothing.
	if res, err := CleanStart(ctx, st, "", false, time.Now(), log, nil); err != nil || res.Done {
		t.Fatalf("no token: %+v %v", res, err)
	}
	// A dry run: says what, one line per item, deletes nothing, no marker.
	res, err := CleanStart(ctx, st, "2026-11-07", true, time.Now(), log, nil)
	if err != nil || !res.Done || !res.DryRun || len(res.Items) < 10 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if fmt.Sprint(snapshot()) != fmt.Sprint(before) || objs.Writes != 2 || fmt.Sprint(wiped) != "[true]" {
		t.Fatal("the dry run changed something")
	}
	if !strings.Contains(logs.String(), "would delete") || !strings.Contains(logs.String(), "object=issues key=issues-index.json") || !strings.Contains(logs.String(), "count=2") {
		t.Fatalf("logs: %s", logs.String())
	}
	// The clean start.
	auditBuf := &strings.Builder{}
	res, err = CleanStart(ctx, st, "2026-11-07", false, time.Now(), log, auditTo(auditBuf))
	if err != nil || !res.Done {
		t.Fatalf("clean: %+v %v", res, err)
	}
	after := snapshot()
	deleted := map[string]bool{"issues/" + issues.IndexKey: true, "issues/" + issues.KnownKey: true, "casework/exceptions.json": true, "casework/rcas.json": true,
		"issues/issues-reports.json": true, "casework/cases.json": true,
		"casework/" + ChecksKey: true, "runs/runs.json": true, "knowledge/knowledge-drafts.json": true, "knowledge/knowledge-status.json": true}
	changed := map[string]bool{"issues/" + issues.ConfigKey: true, "casework/" + SettingsKey: true, "cards/cards.json": true, "casework/" + CleanKey: true}
	for k, v := range before {
		switch {
		case deleted[k]:
			if _, ok := after[k]; ok {
				t.Errorf("%s stayed", k)
			}
		case changed[k]:
		default:
			if after[k] != v {
				t.Errorf("%s was touched: %s → %s", k, v, after[k])
			}
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok && !changed[k] {
			t.Errorf("%s was written", k)
		}
	}
	if names, _ := objs.List(ctx); len(names) != 0 {
		t.Fatalf("case ConfigMaps left: %v", names)
	}
	var cfg struct {
		Settings struct {
			Company string                   `json:"company"`
			Targets map[string]issues.Target `json:"targets"`
		} `json:"settings"`
		Services []map[string]any `json:"services"`
		Tokens   []map[string]any `json:"tokens"`
	}
	json.Unmarshal([]byte(after["issues/"+issues.ConfigKey]), &cfg)
	if cfg.Settings.Company != "Example" || cfg.Settings.Targets["0"].ResolveMinutes != 240 || cfg.Settings.Targets["4"].ResponseMinutes != 2880 || cfg.Settings.Targets["P1"] != (issues.Target{}) || len(cfg.Services) != 1 || len(cfg.Tokens) != 1 {
		t.Fatalf("the issue settings: %+v", cfg)
	}
	var rec record
	json.Unmarshal([]byte(after["casework/"+SettingsKey]), &rec)
	if rec.Settings.KeepAgentClosedDays != 30 || rec.Duplicates != 0 || len(rec.Analyzing) != 0 {
		t.Fatalf("Settings › Cases: %+v", rec)
	}
	var cardsDoc struct {
		Next  int `json:"next"`
		Cards []struct {
			ID string `json:"id"`
		} `json:"cards"`
	}
	json.Unmarshal([]byte(after["cards/cards.json"]), &cardsDoc)
	if cardsDoc.Next != 5 || len(cardsDoc.Cards) != 2 || cardsDoc.Cards[0].ID != "PC-2" || cardsDoc.Cards[1].ID != "PC-5" {
		t.Fatalf("cards: %s", after["cards/cards.json"])
	}
	if fmt.Sprint(wiped) != "[true false]" || !strings.Contains(auditBuf.String(), `"action":"cases.clean"`) {
		t.Fatalf("wiki %v, audit %s", wiped, auditBuf.String())
	}
	// The same token again: nothing.
	objs.Save(ctx, "9", issues.CaseKey, []byte(`{"number":9}`), "")
	if res, err := CleanStart(ctx, st, "2026-11-07", false, time.Now(), log, nil); err != nil || res.Done {
		t.Fatalf("again: %+v %v", res, err)
	}
	if names, _ := objs.List(ctx); len(names) != 1 {
		t.Fatal("the same token ran again")
	}
	_ = settingsCM
	_ = jobs.KindAudit
}

func slogTo(b *strings.Builder) *slog.Logger { return slog.New(slog.NewTextHandler(b, nil)) }

func auditTo(b *strings.Builder) *audit.Log { return audit.New(audit.Options{Out: b}) }
