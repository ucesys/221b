package casework

import (
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- Fixes (wave 6g, CF-50 to CF-55) ---

// Closing a case as Fixed saves its fix, unless Save fix is off; a case
// with the same problem and the same fix joins it; one that fixes it
// otherwise makes both pending (CF-50, CF-53).
func TestClosingAsFixedSavesTheFixCF50(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	first := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits")).Findings[0].Case
	v, err := w.svc.Fix(ctx, first, FixInput{Text: "kubectl set resources deployment/web -n shop --limits=memory=512Mi", RCA: &Completion{Cause: "The chart sets no limits.", Prevention: "Defaults in the chart."}}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if v.Fix != "F-1" {
		t.Fatalf("the case names fix %q", v.Fix)
	}
	f, _ := w.fixes.Get(ctx, "F-1")
	if f.Status != fixes.Active || f.Key != "pod.no-limits" || f.Cause != "The chart sets no limits." || f.Prevention != "Defaults in the chart." || len(f.Cases) != 1 || f.Cases[0].Case != first || f.CreatedBy != "olivia@corp.example" {
		t.Fatalf("the fix: %+v", f)
	}
	if !strings.Contains(v.History[len(v.History)-1].What, "Its fix was saved as F-1") {
		t.Errorf("the history: %+v", v.History[len(v.History)-1])
	}
	// The same problem fixed the same way elsewhere joins F-1.
	second := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/api", "api has no limits")).Findings[0].Case
	v2, err := w.svc.Fix(ctx, second, FixInput{Text: "kubectl set resources deployment/api -n shop --limits=memory=512Mi"}, olivia)
	if err != nil || v2.Fix != "F-1" {
		t.Fatalf("the second case: %v %q", err, v2.Fix)
	}
	if f, _ = w.fixes.Get(ctx, "F-1"); len(f.Cases) != 2 {
		t.Fatalf("F-1's cases: %+v", f.Cases)
	}
	// Fixed otherwise: a new fix, pending beside F-1, each naming the other.
	third := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/db", "db has no limits")).Findings[0].Case
	v3, _ := w.svc.Fix(ctx, third, FixInput{Text: "Moved db to the operator, which sets the limits itself"}, olivia)
	f2, _ := w.fixes.Get(ctx, v3.Fix)
	f, _ = w.fixes.Get(ctx, "F-1")
	if v3.Fix != "F-2" || f2.Status != fixes.Pending || f.Status != fixes.Pending || f2.Contradicts[0] != "F-1" || f.Contradicts[0] != "F-2" {
		t.Fatalf("F-1 %+v, F-2 %+v", f, f2)
	}
	// Save fix off: nothing is saved.
	fourth, _ := w.svc.Create(ctx, NewCase{Title: "Certificates expire on the ingress", Cluster: "prod"}, olivia)
	w.svc.Wait()
	off := false
	v4, err := w.svc.Fix(ctx, fourth.ID, FixInput{Text: "Renewed by hand", SaveFix: &off}, olivia)
	if err != nil || v4.Fix != "" {
		t.Fatalf("Save fix off: %v %q", err, v4.Fix)
	}
	if list, _ := w.fixes.List(ctx); len(list) != 2 {
		t.Errorf("fixes: %d", len(list))
	}
	if !strings.Contains(w.audit.String(), "fix.save") {
		t.Error("no fix.save in the audit log")
	}
}

// Cases fixed together save one fix with all of them (CF-50).
func TestCasesFixedTogetherSaveOneFixCF50(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	a, _ := w.svc.Create(ctx, NewCase{Title: "Disk of db-1 fills", Cluster: "prod"}, olivia)
	b, _ := w.svc.Create(ctx, NewCase{Title: "Disk of db-2 fills", Cluster: "prod"}, olivia)
	w.svc.Wait()
	res, err := w.svc.Bulk(ctx, BulkInput{Cases: []string{a.ID, b.ID}, Action: BulkFix, Text: "Rotated the logs", RCA: &Completion{Cause: "Logs never rotated."}}, olivia)
	if err != nil || !res[0].OK || !res[1].OK {
		t.Fatalf("%v %+v", err, res)
	}
	list, _ := w.fixes.List(ctx)
	if len(list) != 1 || len(list[0].Cases) != 2 || list[0].Steps != "Rotated the logs" || list[0].Cause != "Logs never rotated." {
		t.Fatalf("fixes: %+v", list)
	}
	if w.get(t, a.ID).Fix != list[0].ID || w.get(t, b.ID).Fix != list[0].ID {
		t.Error("the cases do not name their fix")
	}
}

// The cases closed as Fixed before fixes were kept are saved once, with
// the changes run from their drawers that finished done (CF-54).
func TestTheEarlierFixesAreImportedOnceCF54(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	c, _ := w.svc.Create(ctx, NewCase{Title: "20 namespaces have no Pod Security enforcement", Cluster: "local"}, olivia)
	w.svc.Wait()
	w.svc.o.Fixes = nil
	if _, err := w.svc.Fix(ctx, c.ID, FixInput{Text: "Labelled the namespaces", RCA: &Completion{Cause: "Improper initial configuration."}}, olivia); err != nil {
		t.Fatal(err)
	}
	n, _ := number(c.ID)
	w.svc.updateWork(ctx, n, func(wk *Work) error {
		wk.Commands = []Command{{ID: "x", Line: "kubectl label namespace shop pod-security.kubernetes.io/enforce=baseline", Change: true, Status: "done"}}
		return nil
	})
	w.svc.o.Fixes = w.fixes
	got, err := w.svc.ImportFixes(ctx)
	if err != nil || got != 1 {
		t.Fatalf("imported %d: %v", got, err)
	}
	list, _ := w.fixes.List(ctx)
	if len(list) != 1 || list[0].Cause != "Improper initial configuration." || len(list[0].Commands) != 1 || list[0].CreatedBy != "olivia@corp.example" {
		t.Fatalf("fixes: %+v", list)
	}
	if w.get(t, c.ID).Fix != list[0].ID {
		t.Error("the case does not name its fix")
	}
	if again, _ := w.svc.ImportFixes(ctx); again != 0 {
		t.Errorf("it ran again: %d", again)
	}
}

// The housekeeping makes duplicate fixes, known issues and exceptions
// inactive and reports it (CF-53).
func TestTheHousekeepingReportsWhatItDidCF53(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if text, err := w.svc.Housekeep(ctx); err != nil || !strings.Contains(text, "Nothing to do") {
		t.Fatalf("%v: %s", err, text)
	}
	by := fixes.Person{Name: "Olivia"}
	// Two fixes saved apart that say the same (a person's edit made them so).
	w.fixes.SaveFromCase(ctx, fixes.FromCase{Cases: []string{"C-1"}, Title: "Web has no limits", Steps: "Raise the limit"}, by)
	w.fixes.SaveFromCase(ctx, fixes.FromCase{Cases: []string{"C-2"}, Title: "Ingress certificate expired", Steps: "Renew the certificate"}, by)
	title := "Web has no limits"
	w.fixes.Update(ctx, "F-2", fixes.Edit{Title: &title, Steps: ptrS("Raise the limit")}, by)
	w.iss.SaveKnown(ctx, olivia.issue(), "", issues.KnownInput{Title: ptrS("Checkout is slow"), Symptoms: []string{"checkout takes a minute"}})
	w.now = w.now.Add(1)
	w.iss.SaveKnown(ctx, olivia.issue(), "", issues.KnownInput{Title: ptrS("Checkout is slow"), Symptoms: []string{"checkout takes a minute"}})
	for i := 0; i < 2; i++ {
		w.lessons.AddException(ctx, lessons.NewException{Cluster: "prod", Resource: "shop/Deployment/web", Check: "pod.no-limits", Title: "web has no limits", Reason: "a test app"}, lessons.Person{Name: "Olivia"})
		w.now = w.now.Add(1)
	}
	text, err := w.svc.Housekeep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"F-2 made inactive: a duplicate of F-1", "KI-2 made inactive: a duplicate of KI-1", "EX-2 removed: it covers what EX-1 covers"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	f1, _ := w.fixes.Get(ctx, "F-1")
	if len(f1.Cases) != 2 {
		t.Errorf("F-1 keeps both cases: %+v", f1.Cases)
	}
	// The kind is on by default, daily, with no agent.
	k := w.svc.HousekeepingKind()
	if !k.On || k.Agent != "" || k.Default.Schedule == nil || k.Run == nil {
		t.Errorf("the kind: %+v", k)
	}
	if again, _ := w.svc.Housekeep(ctx); !strings.Contains(again, "Nothing to do") {
		t.Errorf("a second round: %s", again)
	}
}

// A case's analysis is given the active fixes of its problem (CF-52).
func TestTheAnalysisIsGivenTheFixesCF52(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	first := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits")).Findings[0].Case
	w.svc.Fix(ctx, first, FixInput{Text: "kubectl set resources deployment/web -n shop --limits=memory=512Mi", RCA: &Completion{Cause: "The chart sets no limits."}}, olivia)
	again := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Deployment/web", "web has no limits")).Findings[0].Case
	got := w.svc.related(ctx, infoOf(w.get(t, again)))
	if !strings.Contains(got, "Fixes of the problem, saved from earlier cases:\n- F-1") || !strings.Contains(got, "changes that worked: kubectl set resources") && !strings.Contains(got, "what fixed it: kubectl set resources") {
		t.Errorf("related: %s", got)
	}
}

// runbooks is Mycroft's side of the proven fixes in a test: it offers
// what it is told, once.
type runbooks struct {
	offer   []string
	started bool
	calls   int
}

func (r *runbooks) OfferFixes(context.Context) ([]string, bool, error) {
	r.calls++
	out := r.offer
	r.offer = nil
	return out, r.started, nil
}

// The housekeeping offers Mycroft the fixes proven in cases, and its
// report says which and whether the Wiki sync job started (KN-34).
func TestTheHousekeepingOffersProvenFixesKN34(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	r := &runbooks{offer: []string{"F-3, seen in 4 cases"}, started: true}
	w.svc.SetRunbooks(r)
	text, err := w.svc.Housekeep(ctx)
	if err != nil || !strings.Contains(text, "### Fixes offered to Mycroft for a runbook") || !strings.Contains(text, "- F-3, seen in 4 cases: its job Wiki sync: Fixes drafts a runbook from it") {
		t.Fatalf("%v: %s", err, text)
	}
	if again, _ := w.svc.Housekeep(ctx); !strings.Contains(again, "Nothing to do") || r.calls != 2 {
		t.Fatalf("a second round, %d calls: %s", r.calls, again)
	}
	r.offer, r.started = []string{"F-4, seen in 3 cases"}, false
	if text, _ := w.svc.Housekeep(ctx); !strings.Contains(text, "F-4, seen in 3 cases: not read yet") {
		t.Fatalf("a paused job: %s", text)
	}
}
