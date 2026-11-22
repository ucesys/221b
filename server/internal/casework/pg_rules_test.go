package casework

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// savedDoc writes v under key over what the document holds, and fails the
// test unless another reader of the database then reads v back, field for
// field.
func savedDoc[T any](t *testing.T, doc, other kv.Backend, key string, v T) {
	t.Helper()
	ctx := context.Background()
	_, version, err := doc.LoadKey(ctx, key)
	if err != nil && !errors.Is(err, kv.ErrNotFound) {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if _, err := doc.SaveKey(ctx, key, b, version); err != nil {
		t.Fatal(err)
	}
	var got T
	if _, err := loadJSON(ctx, other, key, &got); err != nil {
		t.Fatal(err)
	}
	x, _ := json.MarshalIndent(got, "", " ")
	y, _ := json.MarshalIndent(v, "", " ")
	if string(x) != string(y) {
		t.Fatalf("read back\n%s\nsaved\n%s", x, y)
	}
}

func rowsOf(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The checks seen on each cluster, the rules with their suggestions, and
// what the runs found read back from the database as they were saved,
// every field set, and so does each change.
func TestChecksRulesAndFoundInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	day := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	checks, otherChecks := NewChecksPG(d), NewChecksPG(d)
	if _, _, err := checks.LoadKey(context.Background(), ChecksKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("before anything is saved: %v", err)
	}
	cr := checksRecord{Clusters: map[string][]string{"local": {"pod-crash-loop", "node-not-ready"}, "prod": {}, "dev": {"pod-crash-loop"}}, FirstRun: map[string]string{"local": "run-1", "edge": "run-9"}}
	savedDoc(t, checks, otherChecks, ChecksKey, cr)
	cr.Clusters["local"] = []string{"node-not-ready", "pod-crash-loop", "other.new"}
	delete(cr.Clusters, "dev")
	delete(cr.FirstRun, "edge")
	cr.FirstRun["prod"] = "run-12"
	savedDoc(t, checks, otherChecks, ChecksKey, cr)
	if n, m := rowsOf(t, d, "checked_clusters"), rowsOf(t, d, "cluster_checks"); n != 2 || m != 3 {
		t.Fatalf("clusters %d, checks %d", n, m)
	}

	rules, otherRules := NewRulesPG(d), NewRulesPG(d)
	none, two := []string{}, []string{"audit", "alert"}
	describe := func(n int) []Description {
		out := []Description{}
		for i := 0; i < n; i++ {
			out = append(out, Description{ID: "other.etcd-slow", Job: "nightly-audit", Kind: "audit", Text: "etcd answers slowly.", Run: "run-3", At: day.Add(time.Duration(i) * time.Minute)})
		}
		return out
	}
	rr := rulesRecord{
		Rules: map[string]*storedRule{
			"pod-crash-loop": {Says: "A pod keeps restarting.", Type: "workload", Jobs: &two, Control: "CIS 5.1", Auto: true, Updated: day, By: "alice@example.com"},
			"etcd-slow":      {Says: "etcd is slow.", Type: "platform", Jobs: &none, Added: true, Retired: true, ReplacedBy: "etcd-latency", Updated: day.Add(time.Hour), By: "bob@example.com"},
			"untouched":      {},
		},
		Suggestions: []Suggestion{
			{ID: "etcd-slow", Says: "etcd is slow.", Type: "platform", Jobs: []string{"audit"}, From: []string{"other.etcd-slow", "other.etcd-latency"}, Runs: 4, Clusters: 2, Created: day, Why: "Four runs found it.", Descriptions: describe(2)},
			{ID: "bare", Says: "", Type: "", Jobs: []string{}, From: []string{}, Created: day},
		},
		Dismissed: []string{"other.noise", "other.flaky"},
	}
	savedDoc(t, rules, otherRules, RulesKey, rr)
	var kinds int
	if err := d.Pool().QueryRow(context.Background(), `SELECT cardinality(job_kinds) FROM validation_rules
		WHERE validation_rule_id = 'etcd-slow' AND state = 'retired' AND NOT is_built_in AND replaced_by_validation_rule_id = 'etcd-latency'`).Scan(&kinds); err != nil || kinds != 0 {
		t.Fatalf("the retired rule's row: %d %v", kinds, err)
	}
	delete(rr.Rules, "untouched")
	rr.Rules["pod-crash-loop"].Jobs, rr.Rules["pod-crash-loop"].Says = nil, "A pod restarts again and again."
	rr.Suggestions, rr.Dismissed = rr.Suggestions[:1], append(rr.Dismissed, "bare")
	savedDoc(t, rules, otherRules, RulesKey, rr)
	if n, m, k := rowsOf(t, d, "validation_rules"), rowsOf(t, d, "rule_suggestions"), rowsOf(t, d, "rule_suggestion_descriptions"); n != 2 || m != 1 || k != 2 {
		t.Fatalf("rules %d, suggestions %d, descriptions %d", n, m, k)
	}
	// The ids dismissed are rows, one an id: the list grows at its end, is
	// cut at its start, and any other change is kept too.
	rr.Dismissed = append(rr.Dismissed[1:], "other.late")
	savedDoc(t, rules, otherRules, RulesKey, rr)
	rr.Dismissed = []string{"other.late", "other.flaky"}
	savedDoc(t, rules, otherRules, RulesKey, rr)
	if n := rowsOf(t, d, "dismissed_checks"); n != 2 {
		t.Fatalf("dismissed %d", n)
	}

	found, otherFound := NewFoundPG(d), NewFoundPG(d)
	fr := foundRecord{
		IDs: map[string]*foundStat{
			"pod-crash-loop": {Findings: 7, Runs: 3, Clusters: []string{"local", "prod"}, Jobs: []string{"audit", "alert"}, Titles: []TitleCount{{Text: "web-1 restarts", N: 5}, {Text: "api restarts", N: 2}},
				Kinds: map[string]int{"Pod": 6, "Deployment": 1}, First: day.Add(-48 * time.Hour), Last: day, RunIDs: []string{"run-1", "run-2", "run-3"},
				Latest: []FoundFinding{{Run: "run-3", ID: "f1", At: day, Check: "pod-crash-loop", Cluster: "prod", Object: "shop/Pod/web-1", Title: "web-1 restarts", Severity: "critical", Routing: "opened", Case: "C-12", KnownIssue: "K-3", Exception: "E-1"}, {Run: "run-2", At: day.Add(-time.Hour)}},
				Whys:   describe(3)},
			"other.new": {Findings: 1, Runs: 1, First: day, Last: day},
		},
		Counted: []string{"run-1", "run-2", "run-3"}, Tracked: true,
	}
	savedDoc(t, found, otherFound, FoundKey, fr)
	for table, want := range map[string]int{"check_found": 2, "check_found_titles": 2, "check_found_kinds": 2, "check_found_findings": 2, "check_found_descriptions": 3} {
		if got := rowsOf(t, d, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
	delete(fr.IDs, "pod-crash-loop")
	fr.IDs["other.new"].Findings, fr.IDs["other.new"].Latest = 2, []FoundFinding{{Run: "run-4", At: day}}
	fr.Counted = append(fr.Counted, "run-4")
	savedDoc(t, found, otherFound, FoundKey, fr)
	if n := rowsOf(t, d, "check_found_titles") + rowsOf(t, d, "check_found_descriptions"); n != 0 {
		t.Fatalf("%d rows left of a check that is gone", n)
	}
	if n := rowsOf(t, d, "counted_runs"); n != 4 {
		t.Fatalf("runs counted %d", n)
	}
	fr.Counted, fr.Tracked = fr.Counted[2:], false
	savedDoc(t, found, otherFound, FoundKey, fr)
	if n := rowsOf(t, d, "counted_runs"); n != 2 {
		t.Fatalf("runs counted after the oldest went %d", n)
	}
}
