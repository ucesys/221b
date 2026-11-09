package casework

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- A proposed change names the cases it resolves (Phase 12, wave 6b) ---

// withCards gives the world a cards' store of its own and makes the cases
// the jobs' router, as the console does.
func (w *world) withCards(t *testing.T) *cards.Store {
	t.Helper()
	st := cards.New(cards.Options{Backend: &kv.Memory{}, Now: func() time.Time { return w.now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w.svc.SetCards(st)
	w.jobs.SetRouter(w.svc)
	return st
}

// card is a card as the store keeps it, on prod, resolving refs, in a
// state: applied by Pat, rejected by Ann.
func (w *world) card(id, state string, refs ...string) cards.Card {
	c := cards.Card{ID: id, Proposal: cards.Proposal{Agent: "auditor", Source: cards.Source{Kind: cards.SourceJob, ID: "run-1", Label: "Nightly audit"}, Cluster: "prod",
		Title: "Set web's limits", Resolves: refs, ResolvesBy: map[string]string{}}, State: state, Created: w.now, Updated: w.now}
	for _, r := range refs {
		c.ResolvesBy[r] = cards.LinkedByAgent
	}
	switch state {
	case cards.Applied:
		c.Approvals = []cards.Decision{{By: cards.Person{Key: "pat", Name: "Pat"}, At: w.now}}
		c.History = []cards.Event{{At: w.now.Add(-time.Hour), What: "proposed by Auditor"}, {At: w.now, What: "applied", By: "Pat"}}
	case cards.Rejected:
		c.Rejection = &cards.Decision{By: cards.Person{Key: "ann", Name: "Ann"}, At: w.now, Comment: "not now"}
	case cards.Failed:
		c.Reason = "action 1 of 1 failed: exit 1"
	}
	return c
}

func put(t *testing.T, st *cards.Store, cs ...cards.Card) {
	t.Helper()
	if _, _, err := st.Import(context.Background(), cards.Backup{Cards: cs, Next: 100}, (&backup.Env{}).Open); err != nil {
		t.Fatal(err)
	}
}

func messages(v View) string {
	var b strings.Builder
	for _, m := range v.Messages {
		b.WriteString(m.Text + "\n")
	}
	return b.String()
}

// A change applied that resolves an agent-found case records the fix (by
// the approver, at the time), takes the case In progress with the
// approver as its owner, and has the next run of the job that found it
// verify it; the hook and the leader's catch-up act once between them. A
// case a person opened only records the fix; a known issue gets an update,
// once. Rejected, failed and expired changes leave a note and change
// nothing else (CF-29, CF-31).
func TestAChangeAppliedActsOnTheCasesItResolvesOnceCF29(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	st := w.withCards(t)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	human, err := w.svc.Create(ctx, NewCase{Title: "The shop is slow", Cluster: "prod"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	ki, err := w.iss.SaveKnown(ctx, olivia.issue(), "", issues.KnownInput{Title: ptrS("Certificates renewed by hand")})
	if err != nil {
		t.Fatal(err)
	}
	applied := w.card("PC-1", cards.Applied, "C-1", human.ID, ki.ID)
	put(t, st, applied)
	w.now = w.now.Add(time.Minute)
	w.svc.CardChanged(applied)
	// Again, from the hook and from the leader's catch-up: nothing more.
	w.svc.CardChanged(applied)
	w.svc.Tick(ctx)
	c := w.get(t, "C-1")
	if c.Status != issues.StatusInProgress || c.Assignee == nil || c.Assignee.ID != "pat" || c.Verification != issues.VerifyWaiting || c.VerifyJob == nil || c.VerifyJob.ID != "nightly-audit" || c.VerifyJob.Label == "" {
		t.Fatalf("the agent-found case: %+v %+v", c.Brief, c.VerifyJob)
	}
	if f := c.FixApplied; f == nil || f.Change != "PC-1" || f.By != "Pat" || f.Text != "Fix applied by change PC-1: Set web's limits" || !f.At.Equal(applied.AppliedAt()) {
		t.Errorf("fix applied %+v", c.FixApplied)
	}
	if c.AppliedBy == nil || c.AppliedBy.Card != "PC-1" || c.AppliedBy.By != "Pat" {
		t.Errorf("applied by %+v", c.AppliedBy)
	}
	if len(c.Proposals) != 1 || c.Proposals[0].ID != "PC-1" || c.Proposals[0].State != cards.Applied || c.Proposals[0].LinkedBy != cards.LinkedByAgent || c.Proposals[0].AppliedAt == nil || len(c.Cards) != 1 {
		t.Errorf("proposals %+v", c.Proposals)
	}
	if n := strings.Count(messages(c), "Fix applied by change PC-1"); n != 1 {
		t.Errorf("noted %d times: %s", n, messages(c))
	}
	if n := strings.Count(w.audit.String(), `"action":"case.fix-applied"`); n != 2 {
		t.Errorf("audited %d times, not once per case: %s", n, w.audit.String())
	}
	if r := w.report(t, "C-1"); !r.VerifyPending || r.Job != "nightly-audit" {
		t.Errorf("not waiting for its job's next run: %+v", r)
	}
	if list, _ := w.svc.List(ctx, olivia, Filter{}); list[0].Verification != issues.VerifyWaiting && list[1].Verification != issues.VerifyWaiting {
		t.Errorf("the rows do not say it waits: %+v", list)
	}
	h := w.get(t, human.ID)
	if h.Status != issues.StatusInProgress || h.Verification != "" || h.VerifyJob != nil || h.FixApplied == nil || h.FixApplied.Change != "PC-1" || w.report(t, human.ID).VerifyPending {
		t.Errorf("a person's case only records the fix: %+v %+v", h.Brief, h.FixApplied)
	}
	k, _ := w.iss.KnownIssue(ctx, system, ki.ID)
	n := 0
	for _, u := range k.Updates {
		if u.Text == "Fix applied by change PC-1" {
			n++
		}
	}
	if n != 1 || k.Status == issues.KnownResolved {
		t.Errorf("the known issue: %d updates, %s", n, k.Status)
	}
	// Rejected, failed and expired: a note each, nothing else.
	w.route(t, "nightly-audit", true, finding("pod.no-probes", "shop/Deployment/api", "api has no probes"))
	for _, x := range []cards.Card{w.card("PC-2", cards.Rejected, "C-3"), w.card("PC-3", cards.Failed, "C-3"), w.card("PC-4", cards.Expired, "C-3")} {
		put(t, st, x)
		w.svc.CardChanged(x)
	}
	c3 := w.get(t, "C-3")
	for _, want := range []string{"Change PC-2 was rejected by Ann: not now", "Change PC-3 failed: action 1 of 1 failed: exit 1", "Change PC-4 expired unapproved"} {
		if !strings.Contains(messages(c3), want) {
			t.Errorf("no note %q: %s", want, messages(c3))
		}
	}
	if c3.Status != issues.StatusNew || c3.Verification != "" || c3.FixApplied != nil || len(c3.Proposals) != 3 {
		t.Errorf("a change not applied changed the case: %+v %+v", c3.Brief, c3.Proposals)
	}
}

// A change applied while nobody heard it (another replica, the hook not
// set yet) acts on its cases on the leader's next tick (CF-29).
func TestTheLeaderCatchesUpWithChangesItMissedCF29(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	st := w.withCards(t)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	put(t, st, w.card("PC-1", cards.Applied, "C-1"))
	w.svc.Tick(ctx)
	if c := w.get(t, "C-1"); c.Status != issues.StatusInProgress || c.Verification != issues.VerifyWaiting || c.FixApplied == nil {
		t.Fatalf("not caught up: %+v", c.Brief)
	}
	// Later ticks read only what changed since, and act on nothing twice.
	w.now = w.now.Add(time.Hour)
	w.svc.Tick(ctx)
	if n := strings.Count(messages(w.get(t, "C-1")), "Fix applied by change PC-1"); n != 1 {
		t.Errorf("noted %d times", n)
	}
}

// applyTo makes card PC-1 applied for a case, as the hook hears it.
func (w *world) applyTo(t *testing.T, st *cards.Store, ref string) {
	t.Helper()
	c := w.card("PC-1", cards.Applied, ref)
	put(t, st, c)
	w.svc.CardChanged(c)
}

// Below P1, the next run of the job that does not see the case's key
// closes it as fixed by the system, with what fixed it, no RCA, the fix
// verified; a run of another job does not (CF-30).
func TestAVerifiedFixClosesTheCaseBelowP1CF30(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	st := w.withCards(t)
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	w.applyTo(t, st, "C-1")
	w.now = w.now.Add(time.Hour)
	w.route(t, "alert-diagnosis", true)
	if c := w.get(t, "C-1"); c.Status != issues.StatusInProgress || c.Verification != issues.VerifyWaiting {
		t.Fatalf("another job's run verified it: %+v", c.Brief)
	}
	res := w.route(t, "nightly-audit", true)
	c := w.get(t, "C-1")
	want := "Fixed by change PC-1 (Set web's limits), applied by Pat on 2026-11-06; verified by run-3 (Nightly audit)"
	if c.Status != issues.StatusClosed || c.Outcome != issues.OutcomeFix || c.Explanation != want || c.ClosedBy == nil || !c.ClosedBy.System || c.Verified == nil || c.RCA != "" || c.Verification != "" {
		t.Fatalf("not closed as fixed by itself: %+v", c.Brief)
	}
	if r := w.report(t, "C-1"); r.VerifyPending {
		t.Errorf("still waiting: %+v", r)
	}
	if len(res.Verified) != 1 || res.Verified[0] != "C-1" {
		t.Errorf("the run's verified fixes %v", res.Verified)
	}
	if !strings.Contains(w.audit.String(), "case C-1 closed as fixed by the system, no RCA, verified by run-3") || !strings.Contains(w.audit.String(), `"action":"case.fix"`) {
		t.Errorf("audit %s", w.audit.String())
	}
	if !strings.Contains(c.History[len(c.History)-1].What, "Closed as fixed: "+want) {
		t.Errorf("history %+v", c.History)
	}
	_ = ctx
}

// At P0-P1 the case is marked verified and waits for its RCA: a fix
// without one is refused, and with it the case closes, its fix staying
// verified (CF-30).
func TestAVerifiedFixWaitsForItsRCAAtP1CF30(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	st := w.withCards(t)
	f := finding("pod.privileged", "ci/DaemonSet/runner", "The runner is privileged")
	f.Severity = jobs.FindingCritical
	w.route(t, "nightly-audit", true, f)
	w.applyTo(t, st, "C-1")
	w.now = w.now.Add(time.Hour)
	w.route(t, "nightly-audit", true)
	c := w.get(t, "C-1")
	if c.Priority != 1 || c.Status != issues.StatusInProgress || c.Verification != issues.VerifyVerified || c.Verified == nil || !c.RCARequired {
		t.Fatalf("not waiting for its RCA: %+v", c.Brief)
	}
	if !strings.Contains(messages(c), "add the RCA to close it") {
		t.Errorf("no note: %s", messages(c))
	}
	if _, err := w.svc.Fix(ctx, "C-1", FixInput{Text: "Dropped privileged"}, olivia); !errors.Is(err, ErrRCARequired) {
		t.Fatalf("a fix without its RCA at P1: %v", err)
	}
	v, err := w.svc.Fix(ctx, "C-1", FixInput{Text: "Dropped privileged", RCA: &Completion{Cause: "The chart asked for it.", Prevention: "Policy."}}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != issues.StatusClosed || v.Outcome != issues.OutcomeFix || v.Verified == nil || v.RCADetail == nil || v.RCACause == "" || v.FixApplied == nil || v.FixApplied.Change != "PC-1" {
		t.Fatalf("closed: %+v %+v", v.Brief, v.FixApplied)
	}
	if r := w.report(t, "C-1"); r.VerifyPending {
		t.Error("a verified fix waits for another run")
	}
}

// When the next run still sees the key, the fix did not hold: a note, the
// pending verification cleared, and the case stays In progress, not
// reopened (CF-30).
func TestAFixThatDidNotHoldCF30(t *testing.T) {
	w := setup(t)
	st := w.withCards(t)
	f := finding("pod.no-limits", "shop/Deployment/web", "web has no limits")
	w.route(t, "nightly-audit", true, f)
	w.applyTo(t, st, "C-1")
	w.now = w.now.Add(time.Hour)
	res := w.route(t, "nightly-audit", true, f)
	c := w.get(t, "C-1")
	if res.Findings[0].Routing != jobs.RoutedSeen || c.Status != issues.StatusInProgress || c.Verification != issues.VerifyNotHeld || c.Seen.Count != 2 {
		t.Fatalf("after it was found again: %+v %+v", res.Findings[0], c.Brief)
	}
	if !strings.Contains(messages(c), "The fix did not hold: run-2 (Nightly audit) found it again after change PC-1.") {
		t.Errorf("no note: %s", messages(c))
	}
	if r := w.report(t, "C-1"); r.VerifyPending || r.Reopened != 0 {
		t.Errorf("still pending, or reopened: %+v", r)
	}
	// The run after does not close it.
	w.route(t, "nightly-audit", true)
	if c := w.get(t, "C-1"); c.Status != issues.StatusInProgress {
		t.Errorf("closed after the fix did not hold: %+v", c.Brief)
	}
}

// What a run's proposal names is checked with the cases (CF-26): a case
// there, open and on the proposal's cluster (or on none), a known issue
// accepted.
func TestWhatAProposalMayResolveCF26(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	if _, err := w.svc.Create(ctx, NewCase{Title: "Nowhere in particular"}, olivia); err != nil {
		t.Fatal(err)
	}
	closed, _ := w.svc.Create(ctx, NewCase{Title: "Done", Cluster: "prod"}, olivia)
	if _, err := w.svc.Fix(ctx, closed.ID, FixInput{Text: "done"}, olivia); err != nil {
		t.Fatal(err)
	}
	ki, _ := w.iss.SaveKnown(ctx, olivia.issue(), "", issues.KnownInput{Title: ptrS("Known")})
	for id, ok := range map[string]bool{"C-1": true, "C-2": true, ki.ID: true, "C-3": false, "C-9": false, "KI-99": false} {
		if err := w.svc.Resolvable(ctx, "prod", id); (err == nil) != ok {
			t.Errorf("%s on prod: %v", id, err)
		}
	}
	if err := w.svc.Resolvable(ctx, "local", "C-1"); err == nil || !strings.Contains(err.Error(), "on the cluster prod") {
		t.Errorf("a case on another cluster: %v", err)
	}
}

// A person who works the case links a change to it and unlinks one a
// person linked; the 404s and 409s of the contract; a change applied
// already records its fix at once (CF-28).
func TestLinkingAChangeToACaseCF28(t *testing.T) {
	w := setup(t)
	st := w.withCards(t)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	chat := w.card("PC-5", cards.Queued)
	chat.Source = cards.Source{Kind: cards.SourceChat, Label: "Sherlock chat", SetBy: "Olivia"}
	elsewhere := w.card("PC-6", cards.Queued)
	elsewhere.Cluster = "local"
	agents := w.card("PC-7", cards.Queued, "C-1")
	put(t, st, chat, elsewhere, agents, w.card("PC-8", cards.Rejected), w.card("PC-9", cards.Applied))
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
	code, out := do("POST", Prefix+"/C-1/changes", `{"card": "PC-5"}`)
	props, _ := out["proposals"].([]any)
	if code != http.StatusOK || len(props) != 2 {
		t.Fatalf("link: %d %v", code, out)
	}
	byID := map[string]map[string]any{}
	for _, p := range props {
		m := p.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if byID["PC-5"]["linkedBy"] != "Olivia" || byID["PC-5"]["state"] != "queued" || byID["PC-7"]["linkedBy"] != "agent" {
		t.Errorf("proposals %v", props)
	}
	if c, _ := st.Get(ctx, "PC-5"); len(c.Resolves) != 1 || c.Resolves[0] != "C-1" || c.ResolvesBy["C-1"] != "Olivia" {
		t.Errorf("the card does not resolve the case: %+v", c.Proposal)
	}
	for _, x := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", Prefix + "/C-1/changes", `{"card": "PC-5"}`, http.StatusConflict},   // linked already
		{"POST", Prefix + "/C-1/changes", `{"card": "PC-6"}`, http.StatusConflict},   // another cluster
		{"POST", Prefix + "/C-1/changes", `{"card": "PC-8"}`, http.StatusConflict},   // rejected
		{"POST", Prefix + "/C-1/changes", `{"card": "PC-404"}`, http.StatusNotFound}, // no such card
		{"POST", Prefix + "/C-404/changes", `{"card": "PC-5"}`, http.StatusNotFound}, // no such case
		{"POST", Prefix + "/C-1/changes", `{}`, http.StatusBadRequest},               // no card
		{"DELETE", Prefix + "/C-1/changes/PC-7", "", http.StatusConflict},            // the agent's link
		{"DELETE", Prefix + "/C-1/changes/PC-6", "", http.StatusNotFound},            // not linked
		{"DELETE", Prefix + "/C-404/changes/PC-5", "", http.StatusNotFound},          // no such case
		{"DELETE", Prefix + "/C-1/changes/PC-5", "", http.StatusOK},                  // a person's link
		{"POST", Prefix + "/C-1/changes", `{"card": "PC-9"}`, http.StatusOK},         // applied: the fix is recorded
	} {
		code, out := do(x.method, x.path, x.body)
		if code != x.code {
			t.Errorf("%s %s %s: %d %v, want %d", x.method, x.path, x.body, code, out, x.code)
		}
		if code == http.StatusConflict && out["error"] == "" {
			t.Errorf("a 409 without its error: %v", out)
		}
	}
	if c, _ := st.Get(ctx, "PC-5"); len(c.Resolves) != 0 {
		t.Errorf("unlinked, the card still resolves %v", c.Resolves)
	}
	c := w.get(t, "C-1")
	if c.Status != issues.StatusInProgress || c.Verification != issues.VerifyWaiting || c.FixApplied == nil || c.FixApplied.Change != "PC-9" {
		t.Errorf("linking an applied change: %+v %+v", c.Brief, c.FixApplied)
	}
	for _, want := range []string{`"action":"cases.change-link"`, `"action":"cases.change-unlink"`, "the change PC-5 (queued) linked by Olivia", "the change PC-5 unlinked by Olivia"} {
		if !strings.Contains(w.audit.String(), want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	// A closed case links nothing.
	if _, err := w.svc.Create(ctx, NewCase{Title: "Closed one", Cluster: "prod"}, olivia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Fix(ctx, "C-2", FixInput{Text: "done"}, olivia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.LinkChange(ctx, "C-2", "PC-5", olivia); !errors.Is(err, ErrState) {
		t.Errorf("a closed case: %v", err)
	}
}

// A case analysis's change resolves its case, and the other open cases
// on its cluster it names; a case that is not there, or a known issue, it
// does not (CF-26).
func TestACaseAnalysisResolvesItsCaseCF26(t *testing.T) {
	w := setup(t)
	w.jobs.SetRouter(w.svc)
	w.jobOn(t, true)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits"))
	w.agents.answer = strings.Replace(fixAnswer, `"risk": "low"}]`, `"risk": "low", "fixes": ["c-1", "C-404", "KI-1"]}]`, 1)
	if _, err := w.svc.Create(ctx, NewCase{Title: "The shop is slow", Cluster: "prod", Resource: "shop/Deployment/web"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	props := w.props.List()
	if len(props) != 1 || strings.Join(props[0].Resolves, ",") != "C-2,C-1" {
		t.Fatalf("proposals %+v", props)
	}
	run, _ := w.jobs.GetRun(ctx, w.get(t, "C-2").Analysis.Run)
	if len(run.Proposals) != 1 || strings.Join(run.Proposals[0].Resolves, ",") != "C-2,C-1" || !strings.Contains(run.Note, "named C-404") {
		t.Errorf("the run %+v: %s", run.Proposals, run.Note)
	}
}
