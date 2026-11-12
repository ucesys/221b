package issues

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

func p(n int) *int { return &n }

// A case opened in the console (CU-02) has no reporter, counts as
// responded to, is about any words or nothing, takes P0 to P4, and shares
// the one numbering with the reports; the reporter's view never names
// what it is about (CU-01). Agents never set P0.
func TestACaseOpenedInTheConsole(t *testing.T) {
	f := newFixture(t, Options{})
	rec, err := f.s.Submit(ctx, alice, Submission{Text: "The shop is down\nsince 9"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Report.Status != StatusNew || rec.Report.Priority != DefaultPriority || rec.Report.SLAFrom == nil {
		t.Fatalf("a report: %+v", rec.Report)
	}
	if _, err := f.s.Open(ctx, alice, Opening{Source: CaseManual, Title: "x"}); !errors.Is(err, ErrDenied) {
		t.Errorf("a reporter opens a case: %v", err)
	}
	for _, bad := range []Opening{{Source: CaseReport, Title: "x"}, {Source: CaseManual}, {Source: CaseManual, Title: "x", Priority: p(5)}, {Source: CaseFinding, Title: "x", Priority: p(0)}} {
		if _, err := f.s.Open(ctx, operator, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	c, err := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "Disk 90%", Text: "token: abcdefghijklmnopqrstuvwxyz0123456789", Cluster: "prod", Object: "the disk of db-1, /var", Priority: p(0)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Number != rec.Report.Number+1 || c.Priority != 0 || c.Status != StatusNew || c.FirstResponse == nil || c.SLAFrom == nil || strings.Contains(c.Text, "abcdefghij") || c.OpenedBy == nil {
		t.Fatalf("%+v", c)
	}
	// The objects: one per case, each holding its record.
	names, _ := f.s.cases.List(ctx)
	if fmt.Sprint(names) != "[1 2]" {
		t.Fatalf("objects %v", names)
	}
	st, _ := f.s.load(ctx, true)
	if l := st.line(c.Number); l == nil || l.Cluster != "prod" || l.Priority != 0 || l.AgentFound {
		t.Fatalf("line %+v", l)
	}
}

// New until someone takes it, In progress once someone owns it, Closed
// with one outcome the case allows, and reopened with a reason (CF-01,
// CF-03, CF-02); a question unanswered closes as no action needed; a known
// issue resolved tells its cases and reopens none (CF-06).
func TestTheStatusesAndOutcomes(t *testing.T) {
	f := newFixture(t, Options{})
	k := seed(t, f)
	day := 24 * time.Hour
	rec, _ := f.s.Submit(ctx, alice, Submission{Text: "reports page is slow"})
	n := rec.Report.Number
	if _, err := f.s.SetStatus(ctx, alice, n, StatusClosed, OutcomeFix, "x"); !errors.Is(err, ErrDenied) {
		t.Fatalf("a reporter set the status: %v", err)
	}
	r, err := f.s.Triage(ctx, operator, n, Triage{Owner: &Person{ID: "olga", Name: "Olga"}})
	if err != nil || r.Status != StatusInProgress {
		t.Fatalf("an owner takes it: %+v %v", r, err)
	}
	for _, c := range []struct {
		o   Outcome
		why string
	}{{"", ""}, {OutcomeNoAction, ""}, {"noFixNeeded", ""}, {OutcomeException, "x"}, {OutcomeDuplicate, "x"}, {OutcomeKnownIssue, "x"}, {"wontFix", "x"}} {
		if _, err := f.s.SetStatus(ctx, operator, n, StatusClosed, c.o, c.why); !errors.Is(err, ErrInvalid) {
			t.Errorf("closed as %q (%q): %v", c.o, c.why, err)
		}
	}
	// notAProblem, the outcome before CF-90, closes as noAction.
	if _, err := f.s.SetStatus(ctx, operator, n, StatusClosed, "notAProblem", "A browser extension."); err != nil {
		t.Fatal(err)
	}
	if r, _ = f.s.Report(ctx, operator, n); r.Status != StatusClosed || r.Outcome != OutcomeNoAction || r.Explanation != "A browser extension." || r.ClosedBy == nil || r.Closed == nil {
		t.Fatalf("closed: %+v", r)
	}
	if _, err := f.s.SetStatus(ctx, operator, n, StatusNew, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reopened without a reason: %v", err)
	}
	f.clock.add(6 * day)
	if r, err = f.s.Reopen(ctx, alice, n, ""); err != nil || r.Status != StatusInProgress || r.Outcome != "" || r.Reopened != 1 {
		t.Fatalf("still broken within the window, owned: %+v %v", r, err)
	}
	// A fix at P0-P1 needs its RCA, which the console takes.
	f.s.Triage(ctx, operator, n, Triage{Priority: p(1)})
	if _, err := f.s.SetStatus(ctx, operator, n, StatusClosed, OutcomeFix, "Restarted."); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "RCA") {
		t.Fatalf("a P1 fix without its RCA: %v", err)
	}
	f.s.Triage(ctx, operator, n, Triage{Priority: p(3)})
	if _, err := f.s.SetStatus(ctx, operator, n, StatusClosed, OutcomeFix, "Restarted."); err != nil {
		t.Fatal(err)
	}
	f.clock.add(8 * day)
	if _, err = f.s.Reopen(ctx, alice, n, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "send a new report") {
		t.Fatalf("reopened after the window: %v", err)
	}
	if r, err = f.s.SetStatus(ctx, operator, n, StatusNew, "", "It came back."); err != nil || r.Status != StatusInProgress {
		t.Fatalf("an operator reopens with a reason: %+v %v", r, err)
	}
	// A question unanswered for five days: closed as no action needed; an
	// answer stops the clock.
	ask, _ := f.s.Submit(ctx, carol, Submission{Text: "something about the shop is odd"})
	an := ask.Report.Number
	f.s.Triage(ctx, operator, an, Triage{AskInfo: ptr(true)})
	f.clock.add(4 * day)
	f.s.AddMessage(ctx, carol, an, "It is the basket page.", false)
	f.clock.add(2 * day)
	f.s.Sweep(ctx)
	if r, _ := f.s.Report(ctx, operator, an); r.Status == StatusClosed || r.Waiting != nil {
		t.Fatalf("closed despite the answer: %+v", r)
	}
	f.s.Triage(ctx, operator, an, Triage{AskInfo: ptr(true)})
	f.clock.add(5 * day)
	f.s.Sweep(ctx)
	if r, _ := f.s.Report(ctx, operator, an); r.Status != StatusClosed || r.Outcome != OutcomeNoAction {
		t.Fatalf("%s %s", r.Status, r.Outcome)
	}
	// A report matching a known issue is closed as it; resolving the
	// known issue tells it and reopens nothing.
	linked, _ := f.s.Submit(ctx, alice, Submission{Text: "checkout hangs after pressing pay, 504 gateway timeout"})
	if linked.Known == nil || linked.Report.Status != StatusClosed || linked.Report.Outcome != OutcomeKnownIssue {
		t.Fatalf("not closed as the known issue: %+v", linked.Report)
	}
	if _, err := f.s.AddUpdate(ctx, operator, k.ID, "The payment provider's timeout is back to normal.", KnownResolved); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.s.Report(ctx, alice, linked.Report.Number); r.Status != StatusClosed || !strings.Contains(r.Messages[len(r.Messages)-1].Text, "is resolved") {
		t.Fatalf("%+v", r)
	}
	for _, a := range []string{ActionReport, ActionStatus, ActionClose, ActionMessage, ActionTriage, ActionKnown} {
		if !contains(f.actions(), a) {
			t.Errorf("no %s in the audit log", a)
		}
	}
}

// The outcomes a case allows depend on who opened it (CF-03): a person's
// every one but exception; an agent's every one. No action needed is
// offered for both (CF-90).
func TestOutcomesPerSource(t *testing.T) {
	human := Case{Status: StatusNew, Source: CaseManual}
	agent := Case{Status: StatusNew, Source: CaseFinding}
	if fmt.Sprint(human.AllowedOutcomes()) != "[fix duplicate noAction knownIssue]" {
		t.Errorf("human: %v", human.AllowedOutcomes())
	}
	// Any case may be a duplicate of an open original (CF-42).
	if fmt.Sprint(agent.AllowedOutcomes()) != "[fix duplicate noAction knownIssue exception]" {
		t.Errorf("agent: %v", agent.AllowedOutcomes())
	}
	closed := Case{Status: StatusClosed, Source: CaseManual}
	if len(closed.AllowedOutcomes()) != 0 || closed.MayClose(OutcomeFix) == nil {
		t.Error("a closed case allows an outcome")
	}
	if (&Case{Priority: 1}).RCARequired() != true || (&Case{Priority: 2}).RCARequired() != false {
		t.Error("RCA required at P0-P1 only")
	}
	// What was not a problem and no fix needed is no action needed, for
	// an agent's case too (CF-90).
	for _, o := range []Outcome{OutcomeNoAction, "notAProblem", "noFixNeeded"} {
		if err := (&Case{Status: StatusNew, Source: CaseFinding}).MayClose(o); err != nil {
			t.Errorf("an agent's %s: %v", o, err)
		}
	}
	if err := (&Case{Status: StatusNew, Source: CaseReport}).MayClose(OutcomeException); err == nil {
		t.Error("a person's case accepted as an exception")
	}
}

// The SLA clock (CF-02): a case a person opened has one at every priority;
// an agent-found one only at P0-P1, started when it is raised there and
// stopped when lowered; one without shows no SLA, never late.
func TestTheSLAClock(t *testing.T) {
	f := newFixture(t, Options{})
	agent, err := f.s.Open(ctx, operator, Opening{Source: CaseFinding, Title: "No limits", Priority: p(3), Key: &Key{Cluster: "prod", Object: "shop/Pod/web", Check: "pod.no-limits"}})
	if err != nil {
		t.Fatal(err)
	}
	if agent.SLAFrom != nil {
		t.Fatal("an agent's P3 has a clock")
	}
	v, _ := f.s.CaseView(ctx, operator, agent.Number)
	if v.SLA != nil {
		t.Fatalf("no SLA, yet %+v", v.SLA)
	}
	f.clock.add(time.Hour)
	raised, _ := f.s.Triage(ctx, operator, agent.Number, Triage{Priority: p(1)})
	if raised.SLAFrom == nil || !raised.SLAFrom.Equal(f.clock.now()) {
		t.Fatalf("raised: %+v", raised.SLAFrom)
	}
	f.clock.add(2 * 24 * time.Hour)
	if v, _ = f.s.CaseView(ctx, operator, agent.Number); v.SLA == nil || !v.SLA.ResolveLate || !v.SLA.ResolveDue.Equal(raised.SLAFrom.Add(24*time.Hour)) {
		t.Fatalf("late from when it was raised: %+v", v.SLA)
	}
	lowered, _ := f.s.Triage(ctx, operator, agent.Number, Triage{Priority: p(2)})
	if lowered.SLAFrom != nil {
		t.Fatal("lowered, the clock runs")
	}
	if v, _ = f.s.CaseView(ctx, operator, agent.Number); v.SLA != nil {
		t.Fatalf("lowered, still %+v", v.SLA)
	}
	critical, _ := f.s.Open(ctx, operator, Opening{Source: CaseFinding, Title: "Privileged", Priority: p(1), Key: &Key{Cluster: "prod", Object: "shop/Pod/web", Check: "pod.privileged"}})
	if critical.SLAFrom == nil {
		t.Fatal("an agent's P1 has no clock")
	}
	human, _ := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "A question", Priority: p(4)})
	if human.SLAFrom == nil {
		t.Fatal("a person's P4 has no clock")
	}
	lowHuman, _ := f.s.Triage(ctx, operator, human.Number, Triage{Priority: p(3)})
	if lowHuman.SLAFrom == nil {
		t.Fatal("a person's case lost its clock")
	}
	// The targets are keyed 0 to 4.
	set, _ := f.s.Settings(ctx)
	if len(set.Targets) != 5 || set.Targets["0"].ResponseMinutes != 15 || set.Targets["4"].ResponseMinutes != 2*24*60 {
		t.Fatalf("targets %+v", set.Targets)
	}
	set.Targets["P1"] = Target{}
	if _, err := f.s.SaveSettings(ctx, admin, set); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a target keyed P1: %v", err)
	}
}

// The duplicate check (CF-04) follows the chain to its end, never
// to the case itself, and offers what fits when the original is closed,
// row by row.
func TestTheDuplicateCheck(t *testing.T) {
	f := newFixture(t, Options{})
	k := seed(t, f)
	open := func(title string) int {
		r, err := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return r.Number
	}
	closeAs := func(n int, fn func(tx *Tx, r *Case)) {
		if _, err := f.s.Apply(ctx, n, func(tx *Tx, r *Case) error { fn(tx, r); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	me := open("this one")
	check := func(orig int) DuplicateCheck {
		t.Helper()
		d, err := f.s.DuplicateCheck(ctx, me, orig)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	kinds := func(d DuplicateCheck) string {
		var out []string
		for _, o := range d.Offers {
			out = append(out, o.Kind)
		}
		return strings.Join(out, ",")
	}
	// Open: allowed.
	o := open("open original")
	if d := check(o); !d.Allowed || d.Original != fmt.Sprintf("C-%d", o) || d.Status != StatusNew {
		t.Fatalf("open: %+v", d)
	}
	// Closed as fix: reopen it, or a regression.
	fx := open("fixed")
	closeAs(fx, func(tx *Tx, r *Case) { CloseCase(r, operator.Person, OutcomeFix, "patched", tx.Now()) })
	if d := check(fx); d.Allowed || kinds(d) != "reopenOriginal,regression" {
		t.Fatalf("fix: %+v", d)
	}
	// Closed as a known issue: the same known issue; resolved, a regression.
	ki := open("known")
	closeAs(ki, func(tx *Tx, r *Case) { tx.LinkKnown(r, tx.Known(k.ID), operator.Person) })
	if d := check(ki); d.Allowed || kinds(d) != "sameKnownIssue" || d.Offers[0].KnownIssue != k.ID {
		t.Fatalf("known issue: %+v", d)
	}
	f.s.AddUpdate(ctx, operator, k.ID, "Fixed.", KnownResolved)
	if d := check(ki); kinds(d) != "regression" {
		t.Fatalf("resolved known issue: %+v", d)
	}
	// Closed as an exception: no action needed with its reason, or remove it.
	ex := open("excepted")
	closeAs(ex, func(tx *Tx, r *Case) {
		r.Source = CaseFinding
		CloseCase(r, operator.Person, OutcomeException, "batch jobs run without limits", tx.Now())
		r.Exception = "EX-1"
	})
	if d := check(ex); kinds(d) != "noAction,removeException" || d.Offers[0].Explanation != "batch jobs run without limits" || d.Offers[1].Exception != "EX-1" {
		t.Fatalf("exception: %+v", d)
	}
	// Closed as no action needed: the same explanation, reopen it, or
	// the same as it (what not a problem and no fix needed offered, CF-90).
	np := open("no action")
	closeAs(np, func(tx *Tx, r *Case) {
		CloseCase(r, operator.Person, OutcomeNoAction, "works as designed", tx.Now())
	})
	if d := check(np); kinds(d) != "noAction,reopenOriginal,sameAs" || d.Offers[0].Explanation != "works as designed" || !strings.Contains(d.Reason, "no action needed") {
		t.Fatalf("no action needed: %+v", d)
	}
	// A chain is followed to its end; a chain back to the case itself, or
	// the case itself, is refused.
	a, b := open("a"), open("b")
	closeAs(a, func(tx *Tx, r *Case) { tx.CloseDuplicate(r, b, operator.Person, "same") })
	if d := check(a); !d.Allowed || d.Original != fmt.Sprintf("C-%d", b) {
		t.Fatalf("chain: %+v", d)
	}
	closeAs(b, func(tx *Tx, r *Case) { tx.CloseDuplicate(r, me, operator.Person, "same") })
	if d := check(a); d.Allowed || !strings.Contains(d.Reason, "its own original") {
		t.Fatalf("a loop: %+v", d)
	}
	if d := check(me); d.Allowed {
		t.Fatalf("itself: %+v", d)
	}
	if d := check(999); d.Allowed || !strings.Contains(d.Reason, "no case") {
		t.Fatalf("none: %+v", d)
	}
	// Through the API v1, a refused duplicate says why.
	if _, err := f.s.Triage(ctx, operator, me, Triage{DuplicateOf: p(fx)}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("API v1 duplicate of a fixed case: %v", err)
	}
	// Closing as a duplicate: the reporter follows the original.
	rep, _ := f.s.Submit(ctx, alice, Submission{Text: "the export is slow"})
	if _, err := f.s.Triage(ctx, operator, rep.Report.Number, Triage{DuplicateOf: p(o)}); err != nil {
		t.Fatal(err)
	}
	orig, _ := f.s.Report(ctx, operator, o)
	if !orig.watchedBy(alice.ID) || !strings.Contains(orig.Messages[len(orig.Messages)-1].Text, "duplicate of this case") {
		t.Fatalf("the original: %+v", orig)
	}
	// An agent-found case may be a duplicate too, of an open original: a
	// person sees two cases are one problem though their keys differ.
	ag, _ := f.s.Open(ctx, operator, Opening{Source: CaseFinding, Title: "x", Priority: p(3), Key: &Key{Cluster: "c", Check: "other.x"}})
	open2, _ := f.s.Open(ctx, operator, Opening{Source: CaseFinding, Title: "y", Priority: p(3), Key: &Key{Cluster: "c", Check: "other.y"}})
	if d, _ := f.s.DuplicateCheck(ctx, ag.Number, open2.Number); !d.Allowed {
		t.Fatalf("an agent's duplicate of an open case: %+v", d)
	}
}

// Each case is an object of its own and a line of the index (CF-20): a
// change of one case writes that object and the index, never the others;
// a write over another writer's is conditional; the retention of agent
// cases deletes lines and objects; a restore's objects are indexed again.
func TestEachCaseIsAnObjectOfItsOwn(t *testing.T) {
	objs := &kv.MemoryObjects{}
	mem := &kv.Memory{}
	f := newFixture(t, Options{Backend: mem, Cases: objs})
	var nums []int
	for i := range 5 {
		r, err := f.s.Open(ctx, operator, Opening{Source: CaseFinding, Title: fmt.Sprintf("finding %d", i), Priority: p(3), Key: &Key{Cluster: "prod", Object: fmt.Sprintf("shop/Pod/w%d", i), Check: "pod.no-limits"}})
		if err != nil {
			t.Fatal(err)
		}
		nums = append(nums, r.Number)
	}
	before := objs.Writes
	// Many changes to one case in one Tx write its object once.
	err := f.s.Batch(ctx, func(tx *Tx) error {
		for range 3 {
			r, err := tx.Case(nums[2])
			if err != nil {
				return err
			}
			r.Seen.Count++
		}
		return nil
	})
	if err != nil || objs.Writes != before+1 {
		t.Fatalf("writes %d → %d: %v", before, objs.Writes, err)
	}
	var got Case
	_ = json.Unmarshal([]byte(objs.Keys("3")[CaseKey]), &got)
	if got.Seen.Count != 4 {
		t.Fatalf("seen %+v", got.Seen)
	}
	st, _ := f.s.load(ctx, true)
	if st.line(nums[2]).Seen.Count != 4 {
		t.Fatal("the index did not follow")
	}
	// A read-only Tx writes nothing.
	before = objs.Writes
	_ = f.s.Batch(ctx, func(tx *Tx) error { _, err := tx.Case(nums[0]); return err })
	if objs.Writes != before {
		t.Fatal("a Tx that changed nothing wrote")
	}
	// Another writer's change to the same object first: the change is
	// made again on top of it, neither lost.
	keys, version, _ := objs.Load(ctx, "1")
	var r1 Case
	_ = json.Unmarshal(keys[CaseKey], &r1)
	r1.Title = "renamed elsewhere"
	b, _ := json.Marshal(r1)
	tries := 0
	_, err = f.s.Apply(ctx, nums[0], func(tx *Tx, r *Case) error {
		tries++
		if tries == 1 {
			if _, err := objs.Save(ctx, "1", CaseKey, b, version); err != nil {
				t.Fatal(err)
			}
		}
		r.Priority = 1
		return nil
	})
	after, _ := f.s.Report(ctx, operator, nums[0])
	if err != nil || tries < 2 || after.Title != "renamed elsewhere" || after.Priority != 1 {
		t.Fatalf("conflict: tries %d, %+v %v", tries, after, err)
	}
	// The retention: closed agent-found cases older than the cut go, line
	// and object; open ones and a person's stay.
	human, _ := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "a person's"})
	for _, n := range []int{nums[0], nums[1], human.Number} {
		f.s.Apply(ctx, n, func(tx *Tx, r *Case) error {
			CloseCase(r, operator.Person, OutcomeNoAction, "x", tx.Now())
			return nil
		})
	}
	f.clock.add(100 * 24 * time.Hour)
	gone, err := f.s.DropClosedAgentCases(ctx, f.clock.now().Add(-90*24*time.Hour), operator.Person)
	if err != nil || fmt.Sprint(gone) != fmt.Sprintf("[%d %d]", nums[0], nums[1]) {
		t.Fatalf("dropped %v %v", gone, err)
	}
	if names, _ := objs.List(ctx); len(names) != 4 {
		t.Fatalf("objects left %v", names)
	}
	if _, err := f.s.Report(ctx, operator, nums[0]); !errors.Is(err, ErrNotFound) {
		t.Fatal("a dropped case is still there")
	}
	// An object without a line (a restore made it) is indexed again.
	r1.Number = 50
	b, _ = json.Marshal(r1)
	objs.Save(ctx, "50", CaseKey, b, "")
	if n, err := f.s.Reindex(ctx); err != nil || n != 1 {
		t.Fatalf("reindexed %d %v", n, err)
	}
	if c, _ := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "next"}); c.Number != 51 {
		t.Fatalf("numbered %d after a restore", c.Number)
	}
}

// Similar cases for New Case (CF-12): the same object on the same cluster
// first, then titles alike; a possible duplicate for an agent's finding.
func TestSimilarCases(t *testing.T) {
	f := newFixture(t, Options{})
	a, _ := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "Checkout returns 502 errors", Cluster: "prod", Object: "shop/Deployment/checkout"})
	b, _ := f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "Disk almost full on db-1", Cluster: "prod"})
	_, _ = f.s.Open(ctx, operator, Opening{Source: CaseManual, Title: "Unrelated thing", Cluster: "dev"})
	got, err := f.s.Similar(ctx, "something odd", "prod", "shop/deployment/checkout", 0)
	if err != nil || len(got) != 1 || got[0].Number != a.Number || got[0].Score != 100 {
		t.Fatalf("by object: %+v %v", got, err)
	}
	got, _ = f.s.Similar(ctx, "db-1 disk full", "", "", 0)
	if len(got) == 0 || got[0].Number != b.Number || got[0].Score < similarScore {
		t.Fatalf("by title: %+v", got)
	}
	err = f.s.Batch(ctx, func(tx *Tx) error {
		if n := tx.SimilarOpen("Checkout returns 502 errors again", "prod", "shop/Deployment/checkout", 0); n != a.Number {
			t.Errorf("possible duplicate %d", n)
		}
		if n := tx.SimilarOpen("Memory leak", "prod", "shop/Deployment/checkout", 0); n != 0 {
			t.Errorf("a title unlike: %d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A reply moves the case (CF-67, CF-45): a person's question puts a New
// case In progress, waiting for its reporter; the reporter's answer ends
// the wait and it stays In progress; a plain reply, or an agent's draft a
// person sends, puts a New case In progress; an internal note, and
// anything an agent does alone, leave it New. Each move is audited as a
// status change.
func TestAReplyMovesTheCaseCF67(t *testing.T) {
	f := newFixture(t, Options{})
	agent := Caller{Person: Person{ID: "system:cases", Name: "Cases", System: true}, Operator: true}
	statusAudits := func(n int) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		k := 0
		for _, a := range f.audits {
			if a.Action == ActionStatus && a.Target == fmt.Sprintf("case C-%d", n) && strings.Contains(a.Detail, "inProgress") {
				k++
			}
		}
		return k
	}
	submit := func(text string) int {
		rec, err := f.s.Submit(ctx, carol, Submission{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return rec.Report.Number
	}
	// Ask for info: In progress and waiting.
	asked := submit("the shop shows an error")
	if _, err := f.s.AddMessage(ctx, operator, asked, "What app is it?", false); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.Triage(ctx, operator, asked, Triage{AskInfo: ptr(true)})
	if err != nil || r.Status != StatusInProgress || r.Waiting == nil {
		t.Fatalf("asked: %s %v %v", r.Status, r.Waiting, err)
	}
	if statusAudits(asked) != 1 {
		t.Errorf("the move is audited once: %v", f.actions())
	}
	// The reporter answers: still In progress, no longer waiting; the
	// event says it answered.
	if r, err = f.s.AddMessage(ctx, carol, asked, "The shop.", false); err != nil || r.Status != StatusInProgress || r.Waiting != nil {
		t.Fatalf("answered: %s %v %v", r.Status, r.Waiting, err)
	}
	f.mu.Lock()
	last := f.events[len(f.events)-1]
	f.mu.Unlock()
	if last.Kind != EventReportMessage || !last.Answered {
		t.Errorf("the answer's event %+v", last)
	}
	// A question asked alone, through the API, moves it too.
	alone := submit("the reports page is blank")
	if r, _ = f.s.Triage(ctx, operator, alone, Triage{AskInfo: ptr(true)}); r.Status != StatusInProgress || r.Waiting == nil {
		t.Errorf("asked through the API: %s %v", r.Status, r.Waiting)
	}
	// A case left waiting while New (before CF-67) goes In progress when
	// its reporter answers.
	old := submit("the dashboards are slow")
	if _, err := f.s.Apply(ctx, old, func(tx *Tx, r *Case) error { r.Waiting = ptr(f.clock.now()); return nil }); err != nil {
		t.Fatal(err)
	}
	if r, _ = f.s.AddMessage(ctx, carol, old, "Since this morning.", false); r.Status != StatusInProgress || r.Waiting != nil {
		t.Errorf("an old wait answered: %s %v", r.Status, r.Waiting)
	}
	// The reporter writing unasked moves nothing.
	unasked := submit("the shop is slow")
	if r, _ = f.s.AddMessage(ctx, carol, unasked, "Still slow.", false); r.Status != StatusNew {
		t.Errorf("the reporter's own message: %s", r.Status)
	}
	// A plain reply: In progress, not waiting.
	plain := submit("checkout is odd")
	if r, _ = f.s.AddMessage(ctx, operator, plain, "We are looking at it.", false); r.Status != StatusInProgress || r.Waiting != nil {
		t.Errorf("a plain reply: %s %v", r.Status, r.Waiting)
	}
	// An internal note changes nothing.
	noted := submit("search is odd")
	if r, _ = f.s.AddMessage(ctx, operator, noted, "Probably the index.", true); r.Status != StatusNew {
		t.Errorf("a note: %s", r.Status)
	}
	// An agent alone moves nothing: its draft, its reply, its question.
	if r, _ = f.s.Draft(ctx, operator, noted, "We rebuild the search index.", "agent"); r.Status != StatusNew {
		t.Errorf("an agent's draft: %s", r.Status)
	}
	if r, _ = f.s.AddMessage(ctx, agent, noted, "An agent's words.", false); r.Status != StatusNew {
		t.Errorf("an agent's reply: %s", r.Status)
	}
	if r, _ = f.s.Triage(ctx, agent, noted, Triage{AskInfo: ptr(false)}); r.Status != StatusNew {
		t.Errorf("an agent's triage: %s", r.Status)
	}
	if r, _ = f.s.Triage(ctx, agent, noted, Triage{AskInfo: ptr(true)}); r.Status != StatusNew || r.Waiting == nil {
		t.Errorf("an agent's question: %s %v", r.Status, r.Waiting)
	}
	// A person sends the agent's draft: it is their reply.
	var draft int
	for _, m := range r.Messages {
		if m.Draft {
			draft = m.ID
		}
	}
	if r, err = f.s.Send(ctx, operator, noted, draft, ""); err != nil || r.Status != StatusInProgress {
		t.Errorf("the agent's draft sent by a person: %s %v", r.Status, err)
	}
	// A closed case stays closed.
	closed := submit("the old page")
	f.s.SetStatus(ctx, operator, closed, StatusClosed, OutcomeNoAction, "It works as designed.")
	if r, _ = f.s.AddMessage(ctx, operator, closed, "It works as designed.", false); r.Status != StatusClosed {
		t.Errorf("a reply to a closed case: %s", r.Status)
	}
}

// No action needed replaces not a problem and no fix needed (CF-90): the
// issue API still takes the old values and closes as noAction; a case
// stored with one keeps its record and reads as noAction, in a list's
// filter too; a reporter reads "closed: no action needed".
func TestNoActionReplacesTheOldOutcomesCF90(t *testing.T) {
	// Stored before: the record is read as noAction, the case and its
	// line alike.
	var r Case
	if err := json.Unmarshal([]byte(`{"number":3,"status":"closed","outcome":"notAProblem","explanation":"As designed."}`), &r); err != nil || r.Outcome != OutcomeNoAction {
		t.Fatalf("a stored notAProblem: %q %v", r.Outcome, err)
	}
	var l Line
	if err := json.Unmarshal([]byte(`{"number":4,"status":"closed","outcome":"noFixNeeded"}`), &l); err != nil || l.Outcome != OutcomeNoAction {
		t.Fatalf("a stored noFixNeeded: %q %v", l.Outcome, err)
	}
	for o, want := range map[Outcome]Outcome{"notAProblem": OutcomeNoAction, "noFixNeeded": OutcomeNoAction, OutcomeNoAction: OutcomeNoAction, OutcomeFix: OutcomeFix, "wontFix": "wontFix"} {
		if o.Canonical() != want {
			t.Errorf("%s reads as %s", o, o.Canonical())
		}
	}
	if Outcome("notAProblem").Valid() || Outcome("noFixNeeded").Valid() || !OutcomeNoAction.Valid() {
		t.Error("the old outcomes are listed")
	}

	// Through the issue API, as before.
	f := newFixture(t, Options{})
	h := f.s.Handler(func(*http.Request) (Caller, bool) { return operator, true })
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for i, old := range []string{"notAProblem", "noFixNeeded", "noAction"} {
		rec, err := f.s.Submit(ctx, alice, Submission{Text: fmt.Sprintf("the page %d is grey", i)})
		if err != nil {
			t.Fatal(err)
		}
		n := rec.Report.Number
		code, out := call("PATCH", fmt.Sprintf("%sreports/%d", Prefix, n), `{"status":"closed","outcome":"`+old+`","explanation":"It went away."}`)
		if code != http.StatusOK || out["outcome"] != "noAction" {
			t.Fatalf("closed as %s through the API: %d %v", old, code, out)
		}
	}
	code, out := call("GET", Prefix+"reports?scope=all&outcome=notAProblem", "")
	if list, _ := out["reports"].([]any); code != http.StatusOK || len(list) != 3 {
		t.Fatalf("the old value as a filter: %d %v", code, out)
	}
	notices, err := f.s.Updates(ctx, alice, time.Time{})
	if err != nil || len(notices) == 0 || !strings.Contains(fmt.Sprint(notices), "closed: no action needed") {
		t.Fatalf("what the reporter reads: %+v %v", notices, err)
	}
}
