package casework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// on is a finding on a cluster.
func on(cluster, check, object, title string) jobs.Finding {
	f := finding(check, object, title)
	f.Cluster, f.Key.Cluster = cluster, cluster
	return f
}

// The validation rules are a SuperAdmin's (CF-93): anyone else, an
// approver or a reader of the cases, is refused.
func TestOnlyASuperAdminReadsTheRulesCF93(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	for _, c := range []Caller{olivia, otto, {Person: Person{Subject: "rita"}, ID: "rita", Read: true}} {
		if _, err := w.svc.Rules().View(ctx, c); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s read the rules: %v", c.ID, err)
		}
	}
	if _, err := w.svc.Rules().View(ctx, sam); err != nil {
		t.Fatal(err)
	}
}

func (w *world) rules(t *testing.T) RulesView {
	t.Helper()
	v, err := w.svc.Rules().View(context.Background(), sam)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ruleOf(v RulesView, id string) (RuleView, bool) {
	for _, r := range v.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return RuleView{}, false
}

func foundOf(v RulesView, id string) (FoundID, bool) {
	for _, f := range v.Found {
		if f.ID == id {
			return f, true
		}
	}
	return FoundID{}, false
}

func offered(w *world, kind string) []string {
	var out []string
	for _, c := range w.svc.Rules().Book(context.Background()).Offered(jobs.Kind{ID: kind}) {
		out = append(out, c.ID)
	}
	return out
}

var auditKind = jobs.Kind{ID: jobs.KindAudit}

// The list as it stands (ADR-138): the built-ins from code, one row per id
// with every job whose catalogue holds it; a rule added is offered to its
// jobs; a built-in changed shows as edited, and set back to the shipped
// words it is not; a change kept for a built-in a later version wording
// differs from still applies, and a later version's new built-ins come
// through whatever is kept.
func TestTheMergedListOfValidationRules(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	v := w.rules(t)
	total := 0
	for _, k := range jobs.BuiltinRules {
		total += len(k.Checks)
	}
	pvc, ok := ruleOf(v, "pvc.nearly-full")
	if !ok || !pvc.BuiltIn || pvc.State != RuleActive || fmt.Sprint(pvc.Jobs) != "["+jobs.KindAudit+" "+jobs.KindAlert+"]" || len(v.Rules) >= total || !v.MayChange || len(v.Jobs) != 4 {
		t.Fatalf("the built-ins: %+v of %d", pvc, len(v.Rules))
	}
	if v.ClusterNames["prod"] != "Production" || v.ClusterNames["local"] != "local" {
		t.Errorf("the clusters' names by id: %v", v.ClusterNames)
	}
	if got := offered(w, jobs.KindAudit); len(got) != len(jobs.AuditChecks) || got[0] != jobs.AuditChecks[0].ID {
		t.Fatalf("offered to the audit: %v", got)
	}
	v, err := w.svc.Rules().Add(ctx, RuleInput{ID: "pod.host-port", Says: "a pod uses a host port", Type: "security", Jobs: []string{jobs.KindAudit}}, sam)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := ruleOf(v, "pod.host-port"); r.BuiltIn || r.State != RuleActive || r.By != "sam@corp.example" || !slices.Contains(offered(w, jobs.KindAudit), "pod.host-port") || slices.Contains(offered(w, jobs.KindAlert), "pod.host-port") {
		t.Fatalf("added: %+v", r)
	}
	says := "a container has no CPU or memory limit set"
	v, err = w.svc.Rules().Edit(ctx, "pod.no-limits", RulePatch{Says: &says}, sam)
	if r, _ := ruleOf(v, "pod.no-limits"); err != nil || !r.Edited || r.Says != says || w.svc.Rules().Meaning("pod.no-limits") != says {
		t.Fatalf("edited: %+v %v", r, err)
	}
	shipped := shippedSays("pod.no-limits")
	v, _ = w.svc.Rules().Edit(ctx, "pod.no-limits", RulePatch{Says: &shipped}, sam)
	if r, _ := ruleOf(v, "pod.no-limits"); r.Edited || r.Says != shipped {
		t.Fatalf("set back: %+v", r)
	}
	if !strings.Contains(w.audit.String(), `"rule.add"`) || !strings.Contains(w.audit.String(), `"rule.edit"`) {
		t.Errorf("audit: %s", w.audit.String())
	}
	// What a later version finds kept: a change to a built-in and a rule
	// added; every built-in it ships is on the list.
	jl := []string{jobs.KindAlert}
	rs := mergeRules(rulesRecord{Rules: map[string]*storedRule{
		"pod.root-user": {Type: "compliance"},
		"gone.builtin":  {Retired: true},
		"pod.extra":     {Added: true, Says: "extra", Type: "other", Jobs: &jl},
	}})
	if r, _ := rs.get("pod.root-user"); r.Type != "compliance" || !r.Edited || r.Says != shippedSays("pod.root-user") {
		t.Errorf("a kept change: %+v", r)
	}
	if _, ok := rs.get("gone.builtin"); ok {
		t.Error("a change to a built-in no longer shipped is on the list")
	}
	for _, k := range jobs.BuiltinRules {
		for _, c := range k.Checks {
			if _, ok := rs.get(c.ID); !ok {
				t.Errorf("%s is missing", c.ID)
			}
		}
	}
	if !rs.Known(jobs.Kind{ID: jobs.KindAlert}, "pod.extra") || rs.Known(auditKind, "pod.extra") {
		t.Error("an added rule is its jobs'")
	}
}

// The inventory's and the drift check's differences are validation rules
// too (KN-31): listed with what they mean under the Drift check, built
// in, and offered to no audit job, so an exception keyed by one stops a
// proposal as it stops a finding.
func TestTheInventoryRulesAreInTheCatalogueKN31(t *testing.T) {
	w := setup(t)
	v := w.rules(t)
	for _, id := range []string{"inventory.host-missing", "inventory.host-unreachable", "inventory.address-differs", "inventory.host-unlisted", "inventory.fleet-missing"} {
		r, ok := ruleOf(v, id)
		if !ok || !r.BuiltIn || r.State != RuleActive || r.Says == "" || fmt.Sprint(r.Jobs) != "["+jobs.KindDrift+"]" {
			t.Errorf("%s: %+v", id, r)
		}
		if w.svc.Rules().Meaning(id) != r.Says || jobs.RuleMeaning(id) == "" {
			t.Errorf("%s has no meaning", id)
		}
		for _, k := range []string{jobs.KindAudit, jobs.KindAlert, jobs.KindDigest} {
			if slices.Contains(offered(w, k), id) {
				t.Errorf("%s is offered to %s", id, k)
			}
		}
	}
	if jobLabel(jobs.KindDrift) != "Drift check" {
		t.Errorf("the drift check's label: %q", jobLabel(jobs.KindDrift))
	}
}

// Ids never change (ADR-138): a rule's id cannot be edited; a built-in is
// never deleted, nor one in use; a rule added that nothing holds is; an
// id on the list is not added again. Retire stops offering a rule, which
// stays valid for its findings and keeps matching; Make active offers it
// again.
func TestRuleIdsAreImmutableAndRetireKeepsThemMatching(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	r := w.svc.Rules()
	if _, err := r.Add(ctx, RuleInput{ID: "pod.no-limits", Says: "again", Type: "capacity", Jobs: []string{jobs.KindAudit}}, sam); !errors.Is(err, ErrInvalid) {
		t.Errorf("added again: %v", err)
	}
	for _, bad := range []RuleInput{{ID: "Pod.X", Says: "x", Type: "other", Jobs: []string{jobs.KindAudit}}, {ID: "nodot", Says: "x", Type: "other", Jobs: []string{jobs.KindAudit}},
		{ID: "pod.x", Says: "", Type: "other", Jobs: []string{jobs.KindAudit}}, {ID: "pod.x", Says: "x", Type: "nope", Jobs: []string{jobs.KindAudit}},
		{ID: "pod.x", Says: "x", Type: "other"}, {ID: "pod.x", Says: "x", Type: "other", Jobs: []string{"case-analysis"}}} {
		if _, err := r.Add(ctx, bad, sam); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v taken: %v", bad, err)
		}
	}
	if _, err := r.Delete(ctx, "pod.no-limits", sam); !errors.Is(err, ErrState) {
		t.Errorf("a built-in deleted: %v", err)
	}
	if _, err := r.Add(ctx, RuleInput{ID: "pod.unused", Says: "nothing holds it", Type: "other", Jobs: []string{jobs.KindAudit}}, sam); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(ctx, RuleInput{ID: "pod.used", Says: "a run found it", Type: "other", Jobs: []string{jobs.KindAudit}}, sam); err != nil {
		t.Fatal(err)
	}
	w.route(t, "nightly-audit", true, finding("pod.used", "shop/Pod/web", "Used"))
	if _, err := r.Delete(ctx, "pod.used", sam); !errors.Is(err, ErrState) {
		t.Errorf("a rule in use deleted: %v", err)
	}
	if v, err := r.Delete(ctx, "pod.unused", sam); err != nil || slices.ContainsFunc(v.Rules, func(x RuleView) bool { return x.ID == "pod.unused" }) {
		t.Errorf("an unused rule kept: %v", err)
	}
	// The API has no field for an id.
	h := w.rulesAPI(sam)
	if code, _ := call(h, "PATCH", RulesPath+"/pod.used", `{"id": "pod.renamed"}`); code != http.StatusBadRequest {
		t.Errorf("renamed: %d", code)
	}
	// Retire: no longer offered, still valid, still matching its case.
	v, err := r.Retire(ctx, "pod.used", sam)
	if x, _ := ruleOf(v, "pod.used"); err != nil || x.State != RuleRetired || slices.Contains(offered(w, jobs.KindAudit), "pod.used") || !x.Used || x.OpenCases != 1 {
		t.Fatalf("retired: %+v %v", x, err)
	}
	if !r.Book(ctx).Known(jobs.Kind{ID: jobs.KindAlert}, "pod.used") {
		t.Error("a retired rule is no longer valid")
	}
	if res := w.route(t, "nightly-audit", true, finding("pod.used", "shop/Pod/web", "Used")); res.Findings[0].Routing != jobs.RoutedSeen || res.Findings[0].Case != "C-1" {
		t.Errorf("a retired rule's finding: %+v", res.Findings[0])
	}
	if _, err := r.Retire(ctx, "pod.used", sam); !errors.Is(err, ErrState) {
		t.Errorf("retired twice: %v", err)
	}
	if v, err = r.Activate(ctx, "pod.used", sam); err != nil || !slices.Contains(offered(w, jobs.KindAudit), "pod.used") {
		t.Errorf("made active: %v", err)
	}
}

// Replace by merges two rules (ADR-138): the old id is an alias; a
// finding under it routes to the case keyed by the new id, and a finding
// under the new id joins a case keyed by the old one; an exception keyed
// by either still applies to findings of both; the agents are told the
// new id only, and the keys in use name the rule.
func TestAnAliasRoutesToTheCaseOfItsRuleAndExceptionsStillApply(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	r := w.svc.Rules()
	if _, err := r.Add(ctx, RuleInput{ID: "pod.restart-loop", Says: "a container keeps restarting", Type: "outage", Jobs: []string{jobs.KindAudit}}, sam); err != nil {
		t.Fatal(err)
	}
	// C-1 is keyed by the new id, C-2 by the old one on another object,
	// and C-3 by the old one, closed as an exception.
	w.route(t, "nightly-audit", true, finding("pod.crashloop", "shop/Pod/web", "web restarts"), finding("pod.restart-loop", "shop/Pod/api", "api restarts"),
		finding("pod.restart-loop", "batch/Pod/runner", "runner restarts"))
	if _, err := w.svc.Exception(ctx, "C-3", ExceptionInput{Reason: "The runner restarts by design."}, olivia); err != nil {
		t.Fatal(err)
	}
	v, err := r.Replace(ctx, "pod.restart-loop", "pod.crashloop", sam)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := ruleOf(v, "pod.restart-loop")
	merged, _ := ruleOf(v, "pod.crashloop")
	if old.State != RuleReplaced || old.ReplacedBy != "pod.crashloop" || fmt.Sprint(merged.Aliases) != "[pod.restart-loop]" || merged.OpenCases != 2 || merged.Exceptions != 1 {
		t.Fatalf("replaced: %+v / %+v", old, merged)
	}
	if slices.Contains(offered(w, jobs.KindAudit), "pod.restart-loop") || !r.Book(ctx).Known(auditKind, "pod.restart-loop") || r.Book(ctx).Canon("pod.restart-loop") != "pod.crashloop" {
		t.Error("the alias is offered, or not valid, or routes by itself")
	}
	res := w.route(t, "nightly-audit", true,
		// The old id on C-1's object: C-1, keyed by the new id.
		finding("pod.restart-loop", "shop/Pod/web", "web restarts"),
		// The new id on C-2's object: C-2, keyed by the old id.
		finding("pod.crashloop", "shop/Pod/api", "api restarts"),
		// The new id on the exception's object: the exception, keyed by the
		// old id, still applies.
		finding("pod.crashloop", "batch/Pod/runner", "runner restarts"),
		// The old id on a new object: a new case, keyed by the new id, of
		// the rule's type.
		finding("pod.restart-loop", "shop/Pod/cart", "cart restarts"))
	f := res.Findings
	if f[0].Routing != jobs.RoutedSeen || f[0].Case != "C-1" || f[0].Key.Check != "pod.crashloop" || f[0].Check != "pod.restart-loop" {
		t.Errorf("the old id: %+v", f[0])
	}
	if f[1].Routing != jobs.RoutedSeen || f[1].Case != "C-2" {
		t.Errorf("the new id on a case keyed by the old: %+v", f[1])
	}
	if f[2].Routing != jobs.RoutedExcepted || f[2].Exception != "EX-1" {
		t.Errorf("the exception keyed by the old id: %+v", f[2])
	}
	if f[3].Routing != jobs.RoutedOpened {
		t.Fatalf("a new case: %+v", f[3])
	}
	if c := w.get(t, f[3].Case); c.Key.Check != "pod.crashloop" || c.Type != issues.TypeAvailability {
		t.Errorf("the new case: %+v %s", c.Key, c.Type)
	}
	// An exception keyed by the new id applies to findings under the old.
	if _, err := w.svc.Exception(ctx, f[3].Case, ExceptionInput{Reason: "cart restarts nightly"}, olivia); err != nil {
		t.Fatal(err)
	}
	res = w.route(t, "nightly-audit", true, finding("pod.restart-loop", "shop/Pod/cart", "cart restarts"))
	if res.Findings[0].Routing != jobs.RoutedExcepted || res.Findings[0].Exception != "EX-2" {
		t.Errorf("the exception keyed by the new id: %+v", res.Findings[0])
	}
	inUse, err := w.svc.KeysInUse(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range inUse {
		if k.Check == "pod.restart-loop" {
			t.Errorf("a key in use under the alias: %+v", k)
		}
	}
	// A replaced rule is not replaced again, nor by a rule not active; Make
	// active undoes the merge.
	if _, err := r.Replace(ctx, "pod.restart-loop", "pod.oomkilled", sam); !errors.Is(err, ErrState) {
		t.Errorf("replaced twice: %v", err)
	}
	if _, err := r.Retire(ctx, "pod.oomkilled", sam); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Replace(ctx, "pod.pending", "pod.oomkilled", sam); !errors.Is(err, ErrInvalid) {
		t.Errorf("replaced by a retired rule: %v", err)
	}
	if _, err := r.Activate(ctx, "pod.restart-loop", sam); err != nil || r.Book(ctx).Canon("pod.restart-loop") != "pod.restart-loop" {
		t.Errorf("made active: %v", err)
	}
	if !strings.Contains(w.audit.String(), `"rule.replace"`) {
		t.Error("the merge is not audited")
	}
}

// Promote (ADR-138): the ids found outside the list are listed with how
// often, in how many runs, on which clusters, and their cases; promoted
// as it is, an id is offered to its jobs and leaves the list of those
// found; promoted under a clean id, the ids it came from are aliases and
// their findings route by it; promoted to a rule on the list, they join
// it.
func TestPromoteWithAndWithoutRename(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	r := w.svc.Rules()
	for i := 0; i < 3; i++ {
		w.route(t, "nightly-audit", true, finding("other.etcd-slow", "Node/cp-1", "etcd is slow on cp-1"))
	}
	w.route(t, "nightly-audit", true, on("dev", "other.etcd-slow", "Node/cp-9", "etcd is slow on cp-9"), finding("other.disk-a", "Node/w-1", "disk a"), finding("other.disk-b", "Node/w-2", "disk b"))
	v := w.rules(t)
	f, ok := foundOf(v, "other.etcd-slow")
	if !ok || f.Findings != 4 || f.Runs != 4 || fmt.Sprint(f.Clusters) != "[prod dev]" || f.Cases != 2 || f.OpenCases != 2 || f.Title != "etcd is slow on cp-1" || f.Type != "other" || fmt.Sprint(f.Jobs) != "["+jobs.KindAudit+"]" {
		t.Fatalf("found: %+v %v", f, v.Found)
	}
	// Not a SuperAdmin: refused.
	if _, err := r.Promote(ctx, PromoteInput{From: []string{"other.etcd-slow"}, Says: "etcd answers slowly", Type: "performance", Jobs: []string{jobs.KindAudit}}, olivia); !errors.Is(err, ErrDenied) {
		t.Errorf("olivia promoted: %v", err)
	}
	v, err := r.Promote(ctx, PromoteInput{From: []string{"other.etcd-slow"}, Says: "etcd answers slowly", Type: "performance", Jobs: []string{jobs.KindAudit, jobs.KindAlert}}, sam)
	if err != nil {
		t.Fatal(err)
	}
	if rule, _ := ruleOf(v, "other.etcd-slow"); rule.State != RuleActive || rule.BuiltIn || rule.OpenCases != 2 || !slices.Contains(offered(w, jobs.KindAlert), "other.etcd-slow") {
		t.Errorf("kept: %+v", rule)
	}
	if _, still := foundOf(v, "other.etcd-slow"); still {
		t.Error("a promoted id is still found outside the list")
	}
	if res := w.route(t, "nightly-audit", true, finding("other.etcd-slow", "Node/cp-2", "etcd")); w.get(t, res.Findings[0].Case).Type != "performance" {
		t.Error("a promoted rule's case is not of its type")
	}
	// Under a clean id: both ids aliases of it.
	v, err = r.Promote(ctx, PromoteInput{From: []string{"other.disk-a", "other.disk-b"}, ID: "node.disk-slow", Says: "a node's disk is slow", Type: "performance", Jobs: []string{jobs.KindAudit}}, sam)
	if err != nil {
		t.Fatal(err)
	}
	rule, _ := ruleOf(v, "node.disk-slow")
	alias, _ := ruleOf(v, "other.disk-b")
	if fmt.Sprint(rule.Aliases) != "[other.disk-a other.disk-b]" || rule.OpenCases != 2 || alias.State != RuleReplaced || alias.ReplacedBy != "node.disk-slow" {
		t.Fatalf("renamed: %+v / %+v", rule, alias)
	}
	if res := w.route(t, "nightly-audit", true, finding("other.disk-b", "Node/w-2", "disk b")); res.Findings[0].Case != "C-4" {
		t.Errorf("an alias's finding: %+v", res.Findings[0])
	}
	// Into a rule on the list: aliases of it, its words kept.
	w.route(t, "nightly-audit", true, finding("other.mem-pressure", "Node/w-3", "memory"))
	v, err = r.Promote(ctx, PromoteInput{From: []string{"other.mem-pressure"}, ID: "node.memory-pressure"}, sam)
	if err != nil {
		t.Fatal(err)
	}
	if rule, _ := ruleOf(v, "node.memory-pressure"); rule.Says != shippedSays("node.memory-pressure") || fmt.Sprint(rule.Aliases) != "[other.mem-pressure]" {
		t.Errorf("joined: %+v", rule)
	}
	// What is on the list is not promoted again; only other.<slug> is.
	if _, err := r.Promote(ctx, PromoteInput{From: []string{"other.disk-a"}, Says: "x", Type: "other", Jobs: []string{jobs.KindAudit}}, sam); !errors.Is(err, ErrState) {
		t.Errorf("promoted twice: %v", err)
	}
	if _, err := r.Promote(ctx, PromoteInput{From: []string{"pod.crashloop"}, Says: "x", Type: "other", Jobs: []string{jobs.KindAudit}}, sam); !errors.Is(err, ErrInvalid) {
		t.Errorf("a rule promoted: %v", err)
	}
	if !strings.Contains(w.audit.String(), `"rule.promote"`) {
		t.Error("not audited")
	}
}

// The housekeeping's suggestions (ADR-138): an id found in three runs or
// on two clusters is a candidate; its likely variants (alike words) are
// grouped with it; the draft has a clean id after its objects' kind, the
// findings' most frequent title and the jobs that found it; a dismissed
// suggestion is not made again. Nothing is promoted without a person.
func TestSuggestionsGroupLikelyVariants(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		w.route(t, "nightly-audit", true, finding("other.etcd-slow", "Node/cp-1", "etcd is slow"))
	}
	w.route(t, "nightly-audit", true, finding("other.etcd-slow-latency", "Node/cp-2", "etcd latency"), on("dev", "other.cert-rotation", "", "certificates rotate"))
	w.route(t, "nightly-audit", true, on("stage", "other.cert-rotation", "", "certificates rotate"), finding("other.lonely", "", "once"))
	text, err := w.svc.Housekeep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := w.rules(t)
	if len(v.Suggestions) != 2 {
		t.Fatalf("suggestions: %+v\n%s", v.Suggestions, text)
	}
	etcd, cert := v.Suggestions[0], v.Suggestions[1]
	if etcd.ID != "node.etcd-slow" || fmt.Sprint(etcd.From) != "[other.etcd-slow other.etcd-slow-latency]" || etcd.Runs != 4 || etcd.Clusters != 1 || etcd.Says != "etcd is slow" ||
		etcd.Type != "other" || fmt.Sprint(etcd.Jobs) != "["+jobs.KindAudit+"]" {
		t.Errorf("the etcd variants: %+v", etcd)
	}
	if cert.ID != "other.cert-rotation" || cert.Clusters != 2 || len(cert.From) != 1 {
		t.Errorf("the certificates: %+v", cert)
	}
	if len(w.rules(t).Rules) != len(mergeRules(rulesRecord{}).list) {
		t.Error("a suggestion was promoted by itself")
	}
	if !strings.Contains(text, "### Validation rules suggested") || !strings.Contains(text, "node.etcd-slow suggested, from other.etcd-slow, other.etcd-slow-latency: found in 4 runs on 1 clusters") {
		t.Errorf("the report:\n%s", text)
	}
	// Again: the same suggestions, kept from the first time.
	w.now = w.now.Add(24 * time.Hour)
	w.svc.Housekeep(ctx)
	if again := w.rules(t).Suggestions; len(again) != 2 || !again[0].Created.Equal(etcd.Created) {
		t.Errorf("again: %+v", again)
	}
	if _, err := w.svc.Rules().Dismiss(ctx, "other.cert-rotation", olivia); !errors.Is(err, ErrDenied) {
		t.Errorf("olivia dismissed: %v", err)
	}
	if v, err := w.svc.Rules().Dismiss(ctx, "other.cert-rotation", sam); err != nil || len(v.Suggestions) != 1 {
		t.Fatalf("dismissed: %+v %v", v.Suggestions, err)
	}
	w.svc.Housekeep(ctx)
	if again := w.rules(t).Suggestions; len(again) != 1 || again[0].ID != "node.etcd-slow" {
		t.Errorf("a dismissed suggestion came back: %+v", again)
	}
	// Promoting a suggestion drops it.
	if v, err := w.svc.Rules().Promote(ctx, PromoteInput{From: etcd.From, ID: etcd.ID, Says: "etcd answers slowly", Type: "performance", Jobs: etcd.Jobs}, sam); err != nil || len(v.Suggestions) != 0 {
		t.Errorf("promoted: %+v %v", v.Suggestions, err)
	}
}

// Only an exact repeat is promoted by itself (ADR-138): one id found in
// AutoPromoteRuns runs, with no variant, becomes a rule as it is, marked
// as the housekeeping's, reported in its run and audited; retired, it is
// neither promoted nor suggested again. An id with a variant is only
// suggested, however often it is found.
func TestExactRepeatsArePromotedByThemselves(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	for i := 0; i < AutoPromoteRuns; i++ {
		w.route(t, "nightly-audit", true, finding("other.quota-exceeded", "Namespace/shop", "shop is over its quota"), finding("other.dns-timeout", "", "DNS times out"))
	}
	w.route(t, "nightly-audit", true, finding("other.dns-timeout-errors", "", "DNS timeout errors"))
	text, err := w.svc.Housekeep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := w.rules(t)
	rule, ok := ruleOf(v, "other.quota-exceeded")
	if !ok || !rule.Auto || rule.State != RuleActive || rule.Says != "shop is over its quota" || rule.By != "Housekeeping" || !slices.Contains(offered(w, jobs.KindAudit), "other.quota-exceeded") {
		t.Fatalf("auto-promoted: %+v", rule)
	}
	if _, ok := ruleOf(v, "other.dns-timeout"); ok {
		t.Error("an id with a variant was promoted by itself")
	}
	if len(v.Suggestions) != 1 || fmt.Sprint(v.Suggestions[0].From) != "[other.dns-timeout other.dns-timeout-errors]" {
		t.Errorf("suggested: %+v", v.Suggestions)
	}
	if !strings.Contains(text, "### Validation rules promoted (exact repeats)") || !strings.Contains(text, "other.quota-exceeded promoted to a validation rule as it is") || !strings.Contains(text, "retire it") {
		t.Errorf("the report:\n%s", text)
	}
	if !strings.Contains(w.audit.String(), `"rule.auto-promote"`) {
		t.Error("not audited")
	}
	if _, err := w.svc.Rules().Retire(ctx, "other.quota-exceeded", sam); err != nil {
		t.Fatal(err)
	}
	w.route(t, "nightly-audit", true, finding("other.quota-exceeded", "Namespace/shop", "shop is over its quota"))
	text, _ = w.svc.Housekeep(ctx)
	v = w.rules(t)
	if rule, _ := ruleOf(v, "other.quota-exceeded"); rule.State != RuleRetired || strings.Contains(text, "other.quota-exceeded promoted") || len(v.Suggestions) != 1 {
		t.Errorf("after retiring: %+v %+v\n%s", rule, v.Suggestions, text)
	}
}

// rulesAPI serves the rules' routes to a caller.
func (w *world) rulesAPI(c Caller) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range w.svc.Routes(func(*http.Request) (Caller, bool) { return c, true }) {
		mux.HandleFunc(rt.Pattern, rt.Handler)
	}
	return mux
}

func call(h http.Handler, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// Only a SuperAdmin reads the rules (CF-93) and changes them, every
// route (ADR-138).
func TestOnlyASuperAdminChangesTheRules(t *testing.T) {
	w := setup(t)
	w.route(t, "nightly-audit", true, finding("other.x-y", "", "x"))
	reader := w.rulesAPI(olivia)
	if code, _ := call(reader, "GET", RulesPath, ""); code != http.StatusForbidden {
		t.Fatalf("read by an approver: %d", code)
	}
	code, body := call(w.rulesAPI(sam), "GET", RulesPath, "")
	if code != http.StatusOK || body["mayChange"] != true || len(body["rules"].([]any)) == 0 || len(body["found"].([]any)) != 1 {
		t.Fatalf("read: %d %v", code, body["mayChange"])
	}
	writes := [][3]string{
		{"POST", RulesPath, `{"id": "pod.a-b", "says": "a", "type": "other", "jobs": ["nightly-audit"]}`},
		{"PATCH", RulesPath + "/pod.no-limits", `{"says": "changed"}`},
		{"POST", RulesPath + "/pod.no-limits/retire", ``},
		{"POST", RulesPath + "/pod.no-limits/activate", ``},
		{"POST", RulesPath + "/pod.no-limits/replace", `{"by": "pod.oomkilled"}`},
		{"DELETE", RulesPath + "/pod.a-b", ``},
		{"POST", RulesPath + "/promote", `{"from": ["other.x-y"], "says": "x", "type": "other", "jobs": ["nightly-audit"]}`},
		{"DELETE", RulesPath + "/suggestions/other.x-y", ``},
	}
	for _, x := range writes {
		if code, _ := call(reader, x[0], x[1], x[2]); code != http.StatusForbidden {
			t.Errorf("%s %s by olivia: %d", x[0], x[1], code)
		}
	}
	nobody := w.rulesAPI(Caller{Person: Person{Name: "Nobody"}})
	if code, _ := call(nobody, "GET", RulesPath, ""); code != http.StatusForbidden {
		t.Errorf("read by nobody: %d", code)
	}
	admin := w.rulesAPI(sam)
	want := []int{200, 200, 200, 200, 200, 200, 200, 404}
	for i, x := range writes {
		if code, body := call(admin, x[0], x[1], x[2]); code != want[i] {
			t.Errorf("%s %s by sam: %d %v", x[0], x[1], code, body["error"])
		}
	}
}

// routeJob routes one run of a job's findings, as w.route does, for a
// job of a given id, a minute after the run before.
func (w *world) routeJob(t *testing.T, job string, fs ...jobs.Finding) jobs.RouteResult {
	t.Helper()
	w.now = w.now.Add(time.Minute)
	w.runs++
	run := fmt.Sprintf("run-%d", w.runs)
	for i := range fs {
		fs[i].ID = fmt.Sprintf("%s.%d", run, i+1)
	}
	return w.svc.Route(context.Background(), jobs.RouteInput{Run: run, Job: job, Kind: jobs.KindAudit, Label: "Nightly audit", Clusters: []string{"prod"}, OK: true, Findings: fs})
}

// One table, a drawer for every row (CF-85): a row's counts come with
// the lists they stand for: its cases with their status, the runs that
// found it (the newest first), its latest findings with what became of
// each (a case, an exception, ignored), its active exceptions; for a
// rule too, with its runs and clusters. A found id a suggestion groups
// names it; one dismissed alone is not suggested.
func TestTheRowsListWhatTheirCountsStandForCF85(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.route(t, "nightly-audit", true, finding("other.dangling-binding-subject", "ClusterRoleBinding/a", "a names a missing ServiceAccount"),
		finding("other.dangling-binding-subject", "ClusterRoleBinding/b", "b names a missing ServiceAccount"))
	if _, err := w.svc.Exception(ctx, "C-2", ExceptionInput{Reason: "b is kept on purpose."}, olivia); err != nil {
		t.Fatal(err)
	}
	info := on("dev", "other.dangling-binding-subject", "ClusterRoleBinding/c", "c names a missing ServiceAccount")
	info.Severity = jobs.FindingInfo
	min := 3
	w.now = w.now.Add(time.Hour)
	w.runs++
	fs := []jobs.Finding{finding("other.dangling-binding-subject", "ClusterRoleBinding/b", "b again"), info, finding("pod.privileged", "shop/Pod/web", "web runs privileged")}
	for i := range fs {
		fs[i].ID = fmt.Sprintf("run-%d.%d", w.runs, i+1)
	}
	w.svc.Route(ctx, jobs.RouteInput{Run: fmt.Sprintf("run-%d", w.runs), Job: "nightly-audit", Kind: jobs.KindAudit, Clusters: []string{"prod", "dev"}, OK: true, MinPriority: &min, Findings: fs})
	v := w.rules(t)
	f, ok := foundOf(v, "other.dangling-binding-subject")
	if !ok || f.Findings != 4 || f.Runs != 2 || fmt.Sprint(f.RunIDs) != "[run-2 run-1]" || fmt.Sprint(f.ExceptionIDs) != "[EX-1]" || len(f.CaseList) != 2 {
		t.Fatalf("the found id: %+v", f)
	}
	if c := f.CaseList[0]; c.ID != "C-1" || !c.Open || c.Status != string(issues.StatusNew) || c.Title == "" {
		t.Errorf("its open case first: %+v", f.CaseList)
	}
	if c := f.CaseList[1]; c.ID != "C-2" || c.Open || c.Outcome == "" {
		t.Errorf("its closed case: %+v", c)
	}
	// The latest findings, the newest first, each with its run and what
	// became of it.
	routing := map[string]string{}
	for _, x := range f.Latest {
		routing[x.ID] = x.Routing + " " + x.Case + x.Exception
		if x.Run == "" || x.At.IsZero() || x.Cluster == "" {
			t.Errorf("a finding without its run, time or cluster: %+v", x)
		}
	}
	if len(f.Latest) != 4 || routing["run-1.1"] != "opened C-1" || routing["run-2.1"] != "excepted EX-1" || routing["run-2.2"] != "ignored " || f.Latest[0].Run != "run-2" {
		t.Errorf("the latest findings: %v %+v", routing, f.Latest)
	}
	if f.Kinds["ClusterRoleBinding"] != 4 || len(f.Titles) != 3 {
		t.Errorf("kinds and titles: %v %v", f.Kinds, f.Titles)
	}
	rule, _ := ruleOf(v, "pod.privileged")
	if rule.Runs != 1 || fmt.Sprint(rule.Clusters) != "[prod]" || fmt.Sprint(rule.RunIDs) != "[run-2]" || len(rule.CaseList) != 1 || len(rule.Latest) != 1 || rule.FirstFound == nil {
		t.Errorf("a rule's lists: %+v", rule)
	}
	if none, _ := ruleOf(v, "pod.no-limits"); none.CaseList == nil || none.RunIDs == nil || none.Latest == nil || none.ExceptionIDs == nil || none.Clusters == nil {
		t.Errorf("empty lists are lists: %+v", none)
	}
	// The JSON has the lists.
	code, body := call(w.rulesAPI(sam), "GET", RulesPath, "")
	found := body["found"].([]any)[0].(map[string]any)
	if code != http.StatusOK || len(found["caseList"].([]any)) != 2 || len(found["runIds"].([]any)) != 2 || len(found["latest"].([]any)) != 4 || len(found["exceptionIds"].([]any)) != 1 {
		t.Errorf("the API: %d %v", code, found)
	}
	// A found id dismissed alone is not suggested; only a SuperAdmin
	// dismisses one, once.
	for i := 0; i < 3; i++ {
		w.route(t, "nightly-audit", true, finding("other.lonely-id", "", "lonely"))
	}
	if _, err := w.svc.Rules().Dismiss(ctx, "other.lonely-id", olivia); !errors.Is(err, ErrDenied) {
		t.Errorf("olivia dismissed: %v", err)
	}
	v, err := w.svc.Rules().Dismiss(ctx, "other.lonely-id", sam)
	if f, _ := foundOf(v, "other.lonely-id"); err != nil || !f.Dismissed {
		t.Fatalf("dismissed: %+v %v", f, err)
	}
	if _, err := w.svc.Rules().Dismiss(ctx, "other.lonely-id", sam); !errors.Is(err, ErrState) {
		t.Errorf("dismissed twice: %v", err)
	}
	if _, err := w.svc.Rules().Dismiss(ctx, "pod.privileged", sam); !errors.Is(err, ErrNotFound) {
		t.Errorf("a rule dismissed: %v", err)
	}
	w.svc.Housekeep(ctx)
	v = w.rules(t)
	for _, s := range v.Suggestions {
		if slices.Contains(s.From, "other.lonely-id") {
			t.Errorf("a dismissed id suggested: %+v", s)
		}
	}
	// The dangling binding, found on two clusters, is suggested; its row
	// names the suggestion.
	if f, _ := foundOf(v, "other.dangling-binding-subject"); f.Suggestion == "" || f.Suggestion != v.Suggestions[0].ID {
		t.Errorf("in a suggestion: %+v %+v", f.Suggestion, v.Suggestions)
	}
}

// keptRuns is the jobs' service with the runs kept given.
type keptRuns struct {
	Runner
	runs []jobs.Run
}

func (k *keptRuns) FoundRuns(context.Context) ([]jobs.Run, error) { return k.runs, nil }

// keptRun is a run kept, ended at a time, with findings keyed.
func keptRun(id string, at time.Time, fs ...jobs.Finding) jobs.Run {
	for i := range fs {
		fs[i].ID = fmt.Sprintf("%s.%d", id, i+1)
	}
	return jobs.Run{ID: id, Job: "nightly-audit", Kind: jobs.KindAudit, Status: jobs.StatusOK, Started: at.Add(-time.Minute), Ended: at, Findings: fs}
}

// Older runs are counted (CF-85): the runs kept that the record has not
// counted are counted at the leader's start and at the housekeeping's,
// each once, with their own time; a record from before the runs counted
// were named counted every run that ended since its first count, and
// those before are counted now. Again, nothing changes.
func TestRecountIsIdempotentCF85(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	t0 := w.now.Add(-24 * time.Hour)
	// The record as the counter before CF-85 left it: run-12 counted.
	if err := update(ctx, w.backend, FoundKey, func(rec *foundRecord) error {
		rec.IDs = map[string]*foundStat{"other.node-cordoned-by-power-manager": {Findings: 1, Runs: 1, Clusters: []string{"prod"}, Jobs: []string{jobs.KindAlert}, First: t0, Last: t0,
			Titles: []TitleCount{{Text: "Cordoned by its power manager", N: 1}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dangling := func(o string) jobs.Finding {
		return finding("other.dangling-binding-subject", "ClusterRoleBinding/"+o, o+" names a missing ServiceAccount")
	}
	kept := &keptRuns{Runner: w.jobs, runs: []jobs.Run{
		keptRun("run-1", t0.Add(-30*time.Hour), dangling("a"), dangling("b")),
		keptRun("run-12", t0.Add(-time.Second), finding("other.node-cordoned-by-power-manager", "Node/n1", "Cordoned by its power manager")),
	}}
	w.svc.o.Jobs = kept
	n, err := w.svc.Rules().Recount(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recounted %d: %v", n, err)
	}
	v := w.rules(t)
	d, _ := foundOf(v, "other.dangling-binding-subject")
	c, _ := foundOf(v, "other.node-cordoned-by-power-manager")
	if d.Findings != 2 || d.Runs != 1 || fmt.Sprint(d.RunIDs) != "[run-1]" || d.Last == nil || !d.Last.Equal(t0.Add(-30*time.Hour)) || len(d.Latest) != 2 {
		t.Errorf("run-1 counted: %+v", d)
	}
	if c.Findings != 1 || c.Runs != 1 {
		t.Errorf("run-12 counted twice: %+v", c)
	}
	// Again, at the housekeeping and on the leader: nothing more.
	before, _, _ := w.backend.LoadKey(ctx, FoundKey)
	if n, err := w.svc.Rules().Recount(ctx); n != 0 || err != nil {
		t.Errorf("again: %d %v", n, err)
	}
	w.svc.Housekeep(ctx)
	w.svc.Rules().recountOnce(ctx)
	after, _, _ := w.backend.LoadKey(ctx, FoundKey)
	if string(before) != string(after) {
		t.Errorf("the record changed:\n%s\n%s", before, after)
	}
	// A run routed now is counted as it is routed, and not again once
	// kept.
	w.runs = 20
	res := w.route(t, "nightly-audit", true, dangling("c"))
	kept.runs = append(kept.runs, keptRun("run-21", w.now, res.Findings...))
	if n, _ := w.svc.Rules().Recount(ctx); n != 0 {
		t.Errorf("a routed run counted again: %d", n)
	}
	if d, _ := foundOf(w.rules(t), "other.dangling-binding-subject"); d.Findings != 3 || d.Runs != 2 {
		t.Errorf("after a run: %+v", d)
	}
	// A new console's record: every run kept is counted, once.
	w2 := setup(t)
	w2.svc.o.Jobs = &keptRuns{Runner: w2.jobs, runs: kept.runs[:2]}
	if n, _ := w2.svc.Rules().Recount(ctx); n != 2 {
		t.Errorf("a fresh record: %d", n)
	}
	if n, _ := w2.svc.Rules().Recount(ctx); n != 0 {
		t.Errorf("a fresh record again: %d", n)
	}
}

// The agents describe the ids they make up (CF-86): the latest
// description per job is kept on the id, cut to maxWhy, at most maxWhys
// jobs; none is kept for a rule on the list. A suggestion says why it was
// made from facts, carries the agents' descriptions, and drafts its
// meaning from the latest.
func TestTheAgentsDescriptionIsKeptCF86(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	why := func(f jobs.Finding, text string) jobs.Finding {
		f.CheckWhy = text
		return f
	}
	w.routeJob(t, "nightly-audit", why(finding("other.etcd-slow", "Node/cp-1", "etcd is slow"), "etcd answers late. No rule is about the control plane's store."))
	w.routeJob(t, "nightly-audit", why(finding("other.etcd-slow", "Node/cp-1", "etcd is slow"), "etcd answers writes late. Nothing on the list is about etcd."))
	w.routeJob(t, "audit-2", why(finding("other.etcd-slow", "Node/cp-2", "etcd is slow"), strings.Repeat("long ", 200)))
	w.routeJob(t, "nightly-audit", why(finding("pod.privileged", "shop/Pod/web", "privileged"), "not kept"))
	v := w.rules(t)
	f, _ := foundOf(v, "other.etcd-slow")
	if len(f.Descriptions) != 2 || f.Descriptions[0].Job != "audit-2" || len([]rune(f.Descriptions[0].Text)) != maxWhy || f.Descriptions[1].Text != "etcd answers writes late. Nothing on the list is about etcd." || f.Descriptions[1].Run != "run-2" {
		t.Fatalf("the descriptions: %+v", f.Descriptions)
	}
	if r, _ := ruleOf(v, "pod.privileged"); len(r.Descriptions) != 0 {
		t.Errorf("a rule's finding kept a description: %+v", r.Descriptions)
	}
	for i := 0; i < 4; i++ {
		w.routeJob(t, fmt.Sprintf("job-%d", i), why(finding("other.etcd-slow", "Node/cp-1", "etcd is slow"), fmt.Sprintf("etcd is slow, job %d.", i)))
	}
	if f, _ := foundOf(w.rules(t), "other.etcd-slow"); len(f.Descriptions) != maxWhys || f.Descriptions[0].Job != "job-3" {
		t.Errorf("bounded: %+v", f.Descriptions)
	}
	// The record stays within its bytes, however much is found.
	for i := 0; i < 300; i++ {
		w.routeJob(t, "nightly-audit", why(finding(fmt.Sprintf("other.id-%d", i), "Node/cp-1", strings.Repeat("t", 100)), strings.Repeat("w", 500)))
	}
	raw, _, _ := w.backend.LoadKey(ctx, FoundKey)
	var rec foundRecord
	if err := json.Unmarshal(raw, &rec); err != nil || len(raw) > foundBudget || len(rec.IDs) > maxFound || len(rec.Counted) != w.runs {
		t.Errorf("the record: %d bytes, %d ids, %d runs counted: %v", len(raw), len(rec.IDs), len(rec.Counted), err)
	}

	// A suggestion's reasons, from facts, and the agents' descriptions.
	w = setup(t)
	for i := 0; i < 3; i++ {
		w.route(t, "nightly-audit", true, why(finding("other.clock-skew", "", "The nodes' clocks differ"), "The nodes' clocks are not in step. No rule covers time."))
	}
	w.route(t, "nightly-audit", true, finding("other.clock-skew-ntp", "", "NTP is off"), on("dev", "other.cert-rotation", "", "certificates rotate"))
	w.route(t, "nightly-audit", true, on("stage", "other.cert-rotation", "", "certificates rotate"))
	if _, err := w.svc.Housekeep(ctx); err != nil {
		t.Fatal(err)
	}
	v = w.rules(t)
	if len(v.Suggestions) != 2 {
		t.Fatalf("suggestions: %+v", v.Suggestions)
	}
	clock, cert := v.Suggestions[0], v.Suggestions[1]
	if clock.Says != "The nodes' clocks are not in step." || len(clock.Descriptions) != 1 || clock.Descriptions[0].ID != "other.clock-skew" ||
		clock.Why != "Found 4 times in 4 runs on 1 cluster by Nightly audit; grouped with other.clock-skew-ntp because their ids are alike in words (clock skew; clock skew ntp)." {
		t.Errorf("the clock suggestion: %+v", clock)
	}
	if cert.Says != "certificates rotate" || cert.Why != "Found 2 times in 2 runs on 2 clusters by Nightly audit: 2 clusters or more make it a candidate." {
		t.Errorf("the certificates' suggestion: %+v", cert)
	}
}

// Suggested rules are Inbox rows for a SuperAdmin only (CF-87): the Inbox
// reads them from GET …/rules/suggestions, which answers a SuperAdmin and
// refuses anyone else.
func TestSuggestionsAreInboxRowsForASuperAdminCF87(t *testing.T) {
	w := setup(t)
	for i := 0; i < 3; i++ {
		w.route(t, "nightly-audit", true, finding("other.etcd-slow", "Node/cp-1", "etcd is slow"))
	}
	if _, err := w.svc.Housekeep(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, body := call(w.rulesAPI(sam), "GET", RulesPath+"/suggestions", "")
	list, _ := body["suggestions"].([]any)
	if code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["id"] != "node.etcd-slow" || list[0].(map[string]any)["why"] == "" {
		t.Fatalf("a SuperAdmin's: %d %v", code, body)
	}
	for _, c := range []Caller{olivia, otto, {Person: Person{Name: "Nobody"}, Read: true}} {
		if code, body := call(w.rulesAPI(c), "GET", RulesPath+"/suggestions", ""); code != http.StatusForbidden || body["suggestions"] != nil {
			t.Errorf("%s: %d %v", c.Person.Name, code, body)
		}
	}
}

// shippedSays is what a built-in rule of the nightly audit says as it
// ships, found by its id: the list grows, so its places move.
func shippedSays(id string) string {
	for _, c := range jobs.AuditChecks {
		if c.ID == id {
			return c.Says
		}
	}
	return ""
}
