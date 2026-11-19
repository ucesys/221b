package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/schedule"
)

// saved writes v under key over what the document holds, and fails the
// test unless another reader of the database then reads v back, field for
// field.
func saved[T any](t *testing.T, doc, other kv.Backend, key string, v T) {
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

func count(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The jobs with every field set read back from the database as they were
// saved, and so does each change: a job changed, the time one handled,
// what one spent, a job back at its kind's defaults.
func TestJobsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	doc, other := NewJobsPG(d), NewJobsPG(d)
	if _, _, err := doc.LoadKey(context.Background(), JobsKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("before anything is saved: %v", err)
	}
	p2 := 2
	rec := jobsRecord{
		Jobs: map[string]Job{
			"audit": {ID: "audit", Kind: "audit", Name: "Nightly audit", Task: "Look at everything.", When: When{Schedule: &schedule.Spec{Kind: schedule.Weekly, At: "02:30", Days: []string{"mon", "thu"}, Every: 2, Cron: "30 2 * * 1,4", TimeZone: "Europe/Warsaw"}},
				Clusters: []string{"local", "prod"}, Budget: Budget{RunUSD: 1.5, DayUSD: 6}, On: true, SkipOverlap: true, CatchUp: true, SetBy: "alice@example.com", SetAt: day1, Since: day1.Add(time.Hour), MinPriority: &p2},
			"added-1": {ID: "added-1", Kind: "alert", When: When{Event: EventAlert, Severity: SeverityError}, Added: true},
		},
		Handled:  map[string]time.Time{"audit": day1.Add(26 * time.Hour), "backup": day1.Add(3 * time.Hour)},
		Spend:    map[string]daySpend{"audit": {Day: "2026-11-03", USD: 4.25, Stopped: "the day's budget of $6 is spent"}, "report": {Day: "2026-11-03", USD: 0.125}},
		Settings: Settings{Editors: []string{"admin", "superViewer"}, ReaderRole: ReaderSuperViewer, ReaderGroup: "ops", SetBy: "alice@example.com", SetAt: day1},
		Next:     1,
	}
	saved(t, doc, other, JobsKey, rec)
	// Every field is a column; a job nobody changed has a row for what is kept of it.
	var zone string
	var days, unkept int
	if err := d.Pool().QueryRow(context.Background(), `SELECT schedule_time_zone, cardinality(schedule_days), (SELECT count(*) FROM jobs WHERE NOT is_kept)
		FROM jobs WHERE job_id = 'audit' AND has_schedule AND min_priority = 2 AND spend_day = '2026-11-03'`).Scan(&zone, &days, &unkept); err != nil || zone != "Europe/Warsaw" || days != 2 || unkept != 2 {
		t.Fatalf("the audit's row: %q %d, %d not kept: %v", zone, days, unkept, err)
	}
	j := rec.Jobs["audit"]
	j.When, j.MinPriority, j.Clusters = When{Event: EventReport}, nil, nil
	rec.Jobs["audit"] = j
	delete(rec.Jobs, "added-1")
	delete(rec.Handled, "backup")
	rec.Spend["report"] = daySpend{Day: "2026-11-04"}
	rec.Settings, rec.Next = Settings{}, 2
	saved(t, doc, other, JobsKey, rec)
	if n := count(t, d, "jobs"); n != 2 {
		t.Fatalf("jobs has %d rows", n)
	}
}

// The runs likewise, with their findings, facts and proposals; a run that
// comes in at the front and one that goes at the end leave the others'
// rows alone, and any other order is kept too.
func TestJobRunsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	doc, other := NewRunsPG(d), NewRunsPG(d)
	run := func(n int) Run {
		return Run{ID: "r" + string(rune('0'+n)), Job: "audit", Kind: "audit", Label: "Audit", Started: day1.Add(time.Duration(n) * time.Hour), Status: StatusRunning}
	}
	full := Run{ID: "r1", Job: "audit", Kind: "audit", Label: "Nightly audit", Agent: "auditor",
		Trigger: Trigger{Kind: TriggerSchedule, What: "every day at 02:00", SetBy: "alice@example.com", By: "bob@example.com", Slot: day1.Add(2 * time.Hour), Event: EventAlert, Key: "KubePodCrashLooping/web", Rebuild: true},
		Subject: "web-1 is crash looping", Repeats: 3, LastSeen: day1.Add(4 * time.Hour), Severity: SeverityError, Fingerprint: "abc123", Ref: "case-7", Clusters: []string{"local", "prod"},
		Model: "claude-opus-5-5", Started: day1.Add(2 * time.Hour), Ended: day1.Add(2*time.Hour + 3*time.Minute), Status: StatusOK, Note: "two clusters", Report: "All is not well.",
		Findings: []Finding{
			{ID: "f1", Severity: "critical", Check: "pod-crash-loop", Title: "web-1 restarts", Detail: "It restarts every minute.", Evidence: []string{"restarts: 41", "last state: OOMKilled"}, Cluster: "prod",
				Resource: "shop/Pod/web-1", Key: &Key{Cluster: "prod", Object: "shop/Deployment/web", Check: "pod-crash-loop"}, InvalidReason: "no such object", Unverified: true, Routing: RoutedOpened,
				Case: "C-12", KnownIssue: "K-3", Exception: "E-1", NewCheck: true, Expected: true, ExpectedWhy: "It is being drained.", Confidence: "high", CheckWhy: "A pod that restarts."},
			{ID: "f2", Severity: "info", Title: "nothing else"},
		},
		Routed: Routed{Opened: 1, Seen: 2, Known: 3, Excepted: 4, Reopened: 5, Regression: 6, Verified: 7, Capped: 8, Invalid: 9, Ignored: 10, Expected: 11}, VerifiedCases: []string{"C-3", "C-4"},
		NewChecks: []string{"pod-crash-loop"}, Proposals: []ProposalRef{{ID: "PC-1", Title: "Raise the memory limit", Resolves: []string{"C-12", "C-13"}, ResolvesTitles: map[string]string{"C-12": "web-1 restarts", "C-13": ""}}, {ID: "PC-2", Title: "Bad", Error: "it names no object"}},
		FailedClusters: []string{"dev"}, LeftOut: &LeftOut{Count: 5, Severities: Counts{Critical: 1, Warning: 2, Info: 2}, Cap: 40}, NotRechecked: 2, Tokens: Tokens{In: 12000, Out: 800}, CostUSD: 0.75,
		NotRouted: "the cases' ConfigMap was full", Reroute: true, Instance: "221b-0", Facts: []Fact{{Label: "Alert", Text: "KubePodCrashLooping", Href: "/alerts", Tone: "bad"}, {Label: "Since", Text: "02:00"}}, NoFindings: true}
	rec := runsRecord{Next: 3, Runs: []Run{run(3), run(2), full}}
	saved(t, doc, other, RunsKey, rec)
	for table, want := range map[string]int{"job_runs": 3, "findings": 2, "job_run_facts": 2, "job_run_proposals": 2, "job_run_proposal_cases": 2} {
		if got := count(t, d, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
	rowOf := func(key string) (id string) {
		t.Helper()
		if err := d.Pool().QueryRow(context.Background(), "SELECT job_run_id::text FROM job_runs WHERE run_key = $1", key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	untouched := rowOf("r1")
	// A run comes in at the front, one changes, the oldest but one goes.
	rec.Next = 4
	changed := run(3)
	changed.Status, changed.Ended, changed.CostUSD = StatusOK, day1.Add(5*time.Hour), 0.5
	rec.Runs = []Run{run(4), changed, full}
	saved(t, doc, other, RunsKey, rec)
	if rowOf("r1") != untouched || count(t, d, "job_runs") != 3 {
		t.Fatal("a run that did not change was written again")
	}
	// Any other order is kept too.
	rec.Runs = []Run{full, run(5), changed, run(4)}
	saved(t, doc, other, RunsKey, rec)
	if rowOf("r1") != untouched {
		t.Fatal("a run that only moved was written again")
	}
	rec.Runs = nil
	saved(t, doc, other, RunsKey, rec)
	if n := count(t, d, "findings") + count(t, d, "job_run_facts") + count(t, d, "job_run_proposal_cases"); n != 0 {
		t.Fatalf("%d rows left of runs that are gone", n)
	}
}
