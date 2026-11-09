package casework

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// finding is a finding of a run with its key, as package jobs gives it.
func finding(check, object, title string) jobs.Finding {
	return jobs.Finding{Severity: jobs.FindingWarning, Check: check, Title: title, Cluster: "prod", Resource: object, Key: &jobs.Key{Cluster: "prod", Object: object, Check: check}}
}

// route routes one run of a job's findings.
func (w *world) route(t *testing.T, job string, ok bool, fs ...jobs.Finding) jobs.RouteResult {
	t.Helper()
	w.runs++
	run := fmt.Sprintf("run-%d", w.runs)
	for i := range fs {
		fs[i].ID = fmt.Sprintf("%s.%d", run, i+1)
	}
	return w.svc.Route(context.Background(), jobs.RouteInput{Run: run, Job: job, Kind: jobs.KindAudit, Label: "Nightly audit", Clusters: []string{"prod"}, OK: ok, Findings: fs})
}

func (w *world) report(t *testing.T, id string) issues.Report {
	t.Helper()
	n, _ := number(id)
	r, err := w.iss.Report(context.Background(), system, n)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The routing table (CF-01), row by row: a finding joins the open case of
// its key; follows a duplicate to its original; is recorded on the known
// issue its case was closed as; is dropped while its exception holds;
// reopens a case closed as fixed on the first run of its job since, and
// opens a regression later; opens a case with the earlier explanation
// after no fix needed; and opens a new one when there is none. Agent
// repeats never match a person's case, which they list as related.
func TestTheRoutingTable(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.now = time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	// None: a new agent-found case, P3 from a warning, its key and source.
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/web", "No limits on web"))
	f := res.Findings[0]
	if f.Routing != jobs.RoutedOpened || f.Case != "C-1" || res.Routed.Opened != 1 {
		t.Fatalf("none: %+v %+v", f, res.Routed)
	}
	c := w.get(t, "C-1")
	if !c.AgentFound || c.Priority != 3 || c.Status != issues.StatusNew || c.Key == nil || c.Key.Check != "pod.no-limits" || c.Seen.Count != 1 || c.Source.Kind != SourceFinding ||
		c.Source.Run != "run-1" || c.Source.Job != "nightly-audit" || c.Source.Finding != "run-1.1" || c.SLAApplies || fmt.Sprint(c.AllowedOutcomes) != "[fix duplicate noFixNeeded knownIssue exception]" {
		t.Fatalf("the case: %+v", c.Brief)
	}
	// Open: the next run joins it, seen ×2, last seen; nothing new.
	w.now = w.now.Add(24 * time.Hour)
	res = w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/web", "No limits on web, again"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedSeen || f.Case != "C-1" || res.Routed.Seen != 1 {
		t.Fatalf("open: %+v", f)
	}
	if c = w.get(t, "C-1"); c.Seen.Count != 2 || !c.Seen.Last.Equal(w.now) {
		t.Fatalf("seen: %+v", c.Seen)
	}
	if list, _ := w.svc.List(ctx, olivia, Filter{Agents: true}); len(list) != 1 {
		t.Fatalf("a repeat made a case: %+v", list)
	}
	// Closed as a duplicate: a repeat of its original. A possible duplicate
	// confirmed (the agent's case for another key on the same object).
	res = w.route(t, "nightly-audit", true, finding("pod.root-user", "shop/Pod/web", "No limits on web as root"))
	if res.Findings[0].Case != "C-2" || w.get(t, "C-2").PossibleDuplicateOf != "C-1" {
		t.Fatalf("a possible duplicate: %+v %+v", res.Findings[0], w.get(t, "C-2").Brief)
	}
	if v, err := w.svc.Close(ctx, "C-2", CloseInput{Outcome: issues.OutcomeDuplicate, Explanation: "the same pod", Original: "C-1"}, olivia); err != nil || v.DuplicateOf != "C-1" {
		t.Fatalf("a possible duplicate confirmed: %+v %v", v.Brief, err)
	}
	if set, _ := w.svc.Settings(ctx); set.DuplicatesOfAgentCases != 1 {
		t.Fatalf("the counter: %+v", set)
	}
	res = w.route(t, "nightly-audit", true, finding("pod.root-user", "shop/Pod/web", "Root"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedSeen || f.Case != "C-1" {
		t.Fatalf("duplicate: %+v", f)
	}
	// Closed as a known issue: recorded on it, still present, nothing new.
	res = w.route(t, "nightly-audit", true, finding("cert.expiring", "shop/Certificate/tls", "The certificate expires"))
	ki := res.Findings[0].Case
	if _, err := w.svc.Known(ctx, ki, KnownLink{Create: &NewKnown{Title: "Certificates are renewed by hand", Workaround: "Renew it"}}, olivia); err != nil {
		t.Fatal(err)
	}
	kid := w.get(t, ki).KnownIssue
	res = w.route(t, "nightly-audit", true, finding("cert.expiring", "shop/Certificate/tls", "The certificate expires"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedKnown || f.KnownIssue != kid || res.Routed.Known != 1 {
		t.Fatalf("known: %+v", f)
	}
	k, _ := w.iss.KnownIssue(ctx, system, kid)
	if k.StillPresent == nil || k.StillPresent.Count != 1 || !k.StillPresent.Last.Equal(w.now) || len(k.Keys) != 1 || k.Keys[0].Job != "nightly-audit" {
		t.Fatalf("still present: %+v", k)
	}
	// Closed as an exception, active: dropped and counted on it.
	res = w.route(t, "nightly-audit", true, finding("pod.privileged", "batch/Pod/runner", "Privileged runner"))
	ex := res.Findings[0].Case
	if _, err := w.svc.Exception(ctx, ex, ExceptionInput{Reason: "The CI runner needs it."}, olivia); err != nil {
		t.Fatal(err)
	}
	res = w.route(t, "nightly-audit", true, finding("pod.privileged", "batch/Pod/runner", "Privileged runner"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedExcepted || f.Exception != "EX-1" || res.Routed.Excepted != 1 {
		t.Fatalf("excepted: %+v", f)
	}
	if e, _ := w.lessons.Exception(ctx, "EX-1"); e.Matches != 1 || !e.LastMatched.Equal(w.now) || e.Case != ex {
		t.Fatalf("the exception: %+v", e)
	}
	// Closed as fixed: the first run of the same job since reopens it, the
	// fix did not hold.
	res = w.route(t, "nightly-audit", true, finding("image.latest-tag", "shop/Deployment/api", "api uses latest"))
	fx := res.Findings[0].Case
	if _, err := w.svc.Fix(ctx, fx, FixInput{Text: "Pinned the tag."}, olivia); err != nil {
		t.Fatal(err)
	}
	if r := w.report(t, fx); !r.VerifyPending || r.Job != "nightly-audit" {
		t.Fatalf("a fix to verify: %+v", r)
	}
	res = w.route(t, "nightly-audit", true, finding("image.latest-tag", "shop/Deployment/api", "api uses latest"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedReopened || f.Case != fx || res.Routed.Reopened != 1 {
		t.Fatalf("reopened: %+v", f)
	}
	if c = w.get(t, fx); c.Status != issues.StatusNew || c.Outcome != "" || c.Seen.Count != 2 {
		t.Fatalf("the fix did not hold: %+v", c.Brief)
	}
	// Closed as fixed, verified since: a new case, a regression of it.
	if _, err := w.svc.Fix(ctx, fx, FixInput{Text: "Pinned it, really."}, olivia); err != nil {
		t.Fatal(err)
	}
	res = w.route(t, "nightly-audit", true)
	if res.Routed.Verified != 1 || fmt.Sprint(res.Verified) != "["+fx+"]" {
		t.Fatalf("verified: %+v", res)
	}
	if c = w.get(t, fx); c.Verified == nil {
		t.Fatalf("not verified: %+v", c.Brief)
	}
	res = w.route(t, "nightly-audit", true, finding("image.latest-tag", "shop/Deployment/api", "api uses latest"))
	f = res.Findings[0]
	if f.Routing != jobs.RoutedRegression || f.Case == fx {
		t.Fatalf("regression: %+v", f)
	}
	if c = w.get(t, f.Case); c.RegressionOf != fx || c.Previous == nil || c.Previous.Outcome != issues.OutcomeFix || c.Previous.Case != fx {
		t.Fatalf("the regression: %+v", c.Brief)
	}
	// Closed as no fix needed: a new case showing the earlier explanation,
	// who wrote it and when.
	res = w.route(t, "nightly-audit", true, finding("node.disk-pressure", "Node/worker-3", "Disk pressure"))
	nf := res.Findings[0].Case
	if _, err := w.svc.Close(ctx, nf, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "A log burst; it cleared."}, olivia); err != nil {
		t.Fatal(err)
	}
	res = w.route(t, "nightly-audit", true, finding("node.disk-pressure", "Node/worker-3", "Disk pressure"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedOpened || f.Case == nf {
		t.Fatalf("after no fix needed: %+v", f)
	}
	if c = w.get(t, f.Case); c.Previous == nil || c.Previous.Explanation != "A log burst; it cleared." || c.Previous.By != "Olivia" || c.Previous.Case != nf || c.RegressionOf != "" {
		t.Fatalf("previous: %+v", c.Previous)
	}
	// A person's open case on an object: an agent's finding on it never
	// joins it, and lists it as related.
	h, _ := w.svc.Create(ctx, NewCase{Title: "The db pod is odd", Cluster: "prod", Resource: "db/Pod/pg-0"}, olivia)
	res = w.route(t, "nightly-audit", true, finding("pod.no-probes", "db/Pod/pg-0", "No probes on pg-0"))
	if f = res.Findings[0]; f.Routing != jobs.RoutedOpened || f.Case == h.ID {
		t.Fatalf("a person's case matched: %+v", f)
	}
	if c = w.get(t, f.Case); len(c.Related) != 1 || c.Related[0].ID != h.ID {
		t.Fatalf("related: %+v", c.Related)
	}
	if hv := w.get(t, h.ID); len(hv.Related) != 1 || hv.Related[0].ID != f.Case {
		t.Fatalf("related the other way: %+v", hv.Related)
	}
	if !strings.Contains(w.audit.String(), `"action":"case.route"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// A run opens at most newCasesPerRun cases; the rest are capped. An
// invalid finding is not routed. A check never seen on its cluster is new
// (CF-01, CF-18).
func TestTheCapInvalidAndNewChecks(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	set := DefaultSettings()
	set.NewCasesPerRun = 2
	if _, err := w.svc.SaveSettings(ctx, set, sam); err != nil {
		t.Fatal(err)
	}
	bad := jobs.Finding{Severity: jobs.FindingCritical, Title: "x", Routing: jobs.RoutedInvalid, InvalidReason: "no check"}
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/a", "a"), finding("pod.no-limits", "shop/Pod/b", "b"), bad,
		finding("pod.no-limits", "shop/Pod/c", "c"), finding("pod.no-limits", "shop/Pod/a", "a again"))
	r := res.Routed
	if r.Opened != 2 || r.Capped != 1 || r.Invalid != 1 || r.Seen != 1 {
		t.Fatalf("routed %+v", r)
	}
	if res.Findings[3].Routing != jobs.RoutedCapped || res.Findings[3].Case != "" || res.Findings[4].Case != "C-1" {
		t.Fatalf("findings %+v", res.Findings)
	}
	if fmt.Sprint(res.NewChecks) != "[pod.no-limits]" || !res.Findings[0].NewCheck || res.Findings[2].NewCheck {
		t.Fatalf("new checks %v %+v", res.NewChecks, res.Findings)
	}
	res = w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/d", "d"))
	if len(res.NewChecks) != 0 || res.Findings[0].NewCheck {
		t.Fatalf("seen before: %v", res.NewChecks)
	}
	// A critical finding is P1, with an SLA clock.
	crit := finding("pod.privileged", "shop/Pod/e", "e")
	crit.Severity = jobs.FindingCritical
	res = w.route(t, "nightly-audit", true, crit)
	if c := w.get(t, res.Findings[0].Case); c.Priority != 1 || !c.SLAApplies || c.SLA == nil {
		t.Fatalf("critical: %+v", c.Brief)
	}
	// The case's view says what its validation rule means; New exception
	// offers the rules seen on each cluster (build 14).
	if c := w.get(t, res.Findings[0].Case); c.RuleMeaning != "a container runs privileged or with host namespaces" {
		t.Fatalf("rule meaning: %q", c.RuleMeaning)
	}
	seen := w.svc.ChecksSeen(ctx)
	if k := res.Findings[0].Key; k == nil || fmt.Sprint(seen[k.Cluster]) != "[pod.no-limits pod.privileged]" {
		t.Fatalf("seen: %v", seen)
	}
}

// A resolved known issue: the next run of each job that saw it checks
// it; still seen reopens it (investigating, an update saying so) and puts
// it in its owner's Inbox; not seen, the job is done with it (CF-06).
func TestAResolvedKnownIssueIsVerified(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	res := w.route(t, "nightly-audit", true, finding("cert.expiring", "shop/Certificate/tls", "Expiring"), finding("cert.expiring", "web/Certificate/tls", "Expiring too"))
	if _, err := w.svc.Known(ctx, res.Findings[0].Case, KnownLink{Create: &NewKnown{Title: "Certificates by hand"}}, olivia); err != nil {
		t.Fatal(err)
	}
	kid := w.get(t, res.Findings[0].Case).KnownIssue
	if _, err := w.svc.Known(ctx, res.Findings[1].Case, KnownLink{ID: kid}, olivia); err != nil {
		t.Fatal(err)
	}
	owner := issues.Person{ID: "olivia", Name: "Olivia"}
	if _, err := w.iss.SaveKnown(ctx, olivia.issue(), kid, issues.KnownInput{Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.iss.AddUpdate(ctx, olivia.issue(), kid, "Automated with cert-manager.", issues.KnownResolved); err != nil {
		t.Fatal(err)
	}
	k, _ := w.iss.KnownIssue(ctx, system, kid)
	if fmt.Sprint(k.VerifyJobs) != "[nightly-audit]" || len(k.Keys) != 2 {
		t.Fatalf("to verify: %+v", k)
	}
	// Another job's run changes nothing; a failed run of the job neither.
	w.route(t, "alert-diagnosis", true)
	w.route(t, "nightly-audit", false)
	if k, _ = w.iss.KnownIssue(ctx, system, kid); k.Status != issues.KnownResolved || len(k.VerifyJobs) != 1 {
		t.Fatalf("not its job: %+v", k)
	}
	// Still seen: reopened, for its owner.
	w.route(t, "nightly-audit", true, finding("cert.expiring", "web/Certificate/tls", "Expiring"))
	k, _ = w.iss.KnownIssue(ctx, system, kid)
	if k.Status != issues.KnownInvestigating || !k.Pending || len(k.VerifyJobs) != 0 || !strings.Contains(k.LastUpdate().Text, "Still seen") {
		t.Fatalf("reopened: %+v", k)
	}
	kv, _ := w.svc.KnownOpen(ctx, olivia)
	if len(kv) != 1 {
		t.Fatalf("open known issues: %+v", kv)
	}
	// Resolved again and not seen: the job is done with it.
	w.iss.AddUpdate(ctx, olivia.issue(), kid, "Really automated.", issues.KnownResolved)
	if k, _ = w.iss.KnownIssue(ctx, system, kid); k.Pending {
		t.Fatal("still pending after its owner's update")
	}
	w.route(t, "nightly-audit", true)
	if k, _ = w.iss.KnownIssue(ctx, system, kid); k.Status != issues.KnownResolved || len(k.VerifyJobs) != 0 {
		t.Fatalf("verified: %+v", k)
	}
}

// The keys in use on a cluster, for the agents' tasks (CF-10): the open
// agent-found cases, the known issues still present, and the active
// exceptions up to exceptionsPerAgent, the most recently matched first.
func TestKeysInUse(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	set := DefaultSettings()
	set.ExceptionsPerAgent = 1
	w.svc.SaveSettings(ctx, set, sam)
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/a", "open one"), finding("pod.privileged", "x/Pod/p", "old exception"), finding("pod.privileged", "y/Pod/q", "new exception"))
	w.svc.Exception(ctx, res.Findings[1].Case, ExceptionInput{Reason: "old"}, olivia)
	w.svc.Exception(ctx, res.Findings[2].Case, ExceptionInput{Reason: "new"}, olivia)
	w.now = w.now.Add(time.Hour)
	w.route(t, "nightly-audit", true, finding("pod.privileged", "y/Pod/q", "new exception"))
	keys := w.svc.KeysInUse(ctx, "prod")
	var got []string
	for _, k := range keys {
		got = append(got, k.Object+" "+k.What)
	}
	if fmt.Sprint(got) != "[shop/Pod/a case C-1 y/Pod/q exception EX-2]" {
		t.Fatalf("keys %v", got)
	}
	if len(w.svc.KeysInUse(ctx, "dev")) != 0 {
		t.Fatal("another cluster's keys")
	}
}

// "Suggest an exception" shows on the next case of a check closed as no
// fix needed three times on its cluster (CF-13).
func TestSuggestException(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	for i := range 3 {
		res := w.route(t, "nightly-audit", true, finding("pod.no-limits", fmt.Sprintf("shop/Pod/w%d", i), "No limits"))
		if c := w.get(t, res.Findings[0].Case); c.SuggestException {
			t.Fatalf("suggested after %d", i)
		}
		w.svc.Close(ctx, res.Findings[0].Case, CloseInput{Outcome: issues.OutcomeNoFixNeeded, Explanation: "batch"}, olivia)
	}
	res := w.route(t, "nightly-audit", true, finding("pod.no-limits", "shop/Pod/w9", "No limits"))
	if c := w.get(t, res.Findings[0].Case); !c.SuggestException {
		t.Fatalf("not suggested: %+v", c.Brief)
	}
	if list, _ := w.svc.List(ctx, olivia, Filter{Open: true}); !list[0].SuggestException {
		t.Fatal("not in the list")
	}
}
