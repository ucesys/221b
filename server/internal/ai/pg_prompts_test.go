package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
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
	want, _ := json.MarshalIndent(v, "", " ")
	if got := kept[T](t, other, key); got != string(want) {
		t.Fatalf("read back\n%s\nsaved\n%s", got, want)
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

// An agent's instructions with every field set read back from the
// database as they were saved: what is in force, the versions, the draft
// with its test run and pilot, and the revisions; and so does each change.
func TestPromptsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	doc, other := NewPromptsPG(d), NewPromptsPG(d)
	if _, _, err := doc.LoadKey(context.Background(), PromptsKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("before anything is saved: %v", err)
	}
	day := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	house, empty := "Answer briefly.", ""
	run := &TestRun{Rev: 4, At: day.Add(5 * time.Minute), By: "ann", Cases: 3, Passed: 2, Same: 1, CostUSD: 0.42, Estimate: 0.5, Error: "the budget ran out", State: RunStopped,
		ReplaysPlanned: 2, StoppedAt: 3,
		Planned: []CaseState{{ID: "b1", Name: "a base case", Base: true, State: CasePassed}, {ID: "t1", Name: "a crash loop", State: CaseFailed}, {ID: "t2", Name: "last", State: CaseWaiting}},
		Results: []TestResult{
			{ID: "b1", Name: "a base case", Base: true, Passed: true, Answer: "It is fine.", Tools: []string{"get_pod", "pod_logs"}, HandOvers: []string{Troubleshooter}, CostUSD: 0.2,
				Question: "Is it fine?", Expect: &ExpectView{Tools: []string{"get_pod"}, HandOvers: []string{Troubleshooter}, Refuse: true, Points: []string{"fine"}, Secrets: 2},
				Checks: []Check{{What: "calls get_pod", OK: true}, {What: "mentions fine", OK: true}}},
			{ID: "t1", Name: "a crash loop", Passed: false, Why: []string{"did not call pod_logs", "it missed: OOMKilled"}, Answer: "", CostUSD: 0.22, Checks: []Check{{What: "calls pod_logs"}}},
		},
		Replays: []ReplayResult{{Question: "What needs attention?", Cluster: "local", Before: "Nothing.", After: "Nothing at all.", Same: true, CostUSD: 0.01}, {Question: "Why?", Cluster: "prod", Error: "timed out"}},
	}
	ps := Prompts{Agents: map[string]*AgentPrompt{
		Sherlock: {
			House: &house, BaseHash: "h0", Base: "The baseline.", Notes: map[string]string{AllClusters: "Every cluster.", "prod": "Careful here."}, Live: 2, Revs: 5,
			Versions: []Version{
				{N: 1, House: "The first.", By: "ann", At: day.Add(-48 * time.Hour), Why: "the baseline taken"},
				{N: 2, House: house, Notes: map[string]string{"prod": "Careful here."}, By: "bob", At: day.Add(-24 * time.Hour), Why: "made live", Parts: map[string]string{PartCore: "The core, edited."},
					Rev: 3, Test: &TestBrief{At: day.Add(-25 * time.Hour), Cases: 3, Passed: 3, State: RunDone}},
			},
			Draft: &Draft{Rev: 5, House: "Answer in haiku.", Notes: map[string]string{"dev": "Play here."}, Stage: StagePilot, By: "ann", At: day, Test: run, Request: 12, Restored: 1,
				Pilot: &Pilot{People: []string{"bob@corp.example", "eve@corp.example"}, Clusters: []string{"dev"}, Started: day.Add(10 * time.Minute), Until: day.Add(70 * time.Minute), By: "ann"},
				Parts: map[string]string{PartCore: "The core, drafted.", PartCompact: ""}},
			BaselineCheck: &TestRun{Rev: 0, At: day.Add(-time.Hour), By: "the console", Cases: 1, Passed: 1, State: RunDone, Results: []TestResult{{ID: "b1", Name: "a base case", Base: true, Passed: true, Answer: "Yes."}}},
			Parts:         map[string]string{PartCore: "The core, edited."}, PartBase: map[string]string{PartCore: "c0", PartCompact: "c1"},
			Revisions: []Revision{
				{Rev: 4, House: "Answer in verse.", Notes: map[string]string{"dev": "Play."}, Parts: map[string]string{PartCore: "x"}, By: "ann", At: day.Add(-time.Hour), Note: "version 1 restored", Discarded: true,
					Test: &TestBrief{At: day.Add(-30 * time.Minute), Cases: 3, Passed: 1, State: RunFailed}},
				{Rev: 5, House: "Answer in haiku.", By: "ann", At: day},
			},
		},
		// Edited to nothing is not the baseline.
		Auditor: {House: &empty},
		// Nothing of its own yet.
		Summarizer: {},
	}}
	saved(t, doc, other, PromptsKey, ps)
	// Every field is a column: rows, and what hangs from them.
	for table, want := range map[string]int{"prompt_versions": 8, "prompt_version_notes": 5, "prompt_version_parts": 6, "test_runs": 2, "test_run_cases": 3,
		"test_case_results": 3, "test_case_result_checks": 3, "test_run_replays": 2} {
		if got := count(t, d, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
	var stage, people string
	var secrets int
	if err := d.Pool().QueryRow(context.Background(), `SELECT p.stage, array_to_string(p.pilot_people, ','), r.expect_secret_count
		FROM prompt_versions p JOIN test_runs t USING (prompt_version_id) JOIN test_case_results r USING (test_run_id)
		WHERE p.agent_id = $1 AND p.state = 'draft' AND r.has_expect`, Sherlock).Scan(&stage, &people, &secrets); err != nil || stage != StagePilot || people != "bob@corp.example,eve@corp.example" || secrets != 2 {
		t.Fatalf("the draft's row: %q %q %d: %v", stage, people, secrets, err)
	}
	// The draft goes live: a version more, no draft, no revisions.
	s := ps.Agents[Sherlock]
	live := "Answer in haiku."
	s.Versions = append(s.Versions, Version{N: 3, House: live, By: "ann", At: day.Add(2 * time.Hour), Why: "made live", Rev: 5, Test: &TestBrief{At: run.At, Cases: 3, Passed: 2, State: RunStopped}})
	s.House, s.Live, s.Draft, s.Revisions, s.Notes = &live, 3, nil, nil, map[string]string{"dev": "Play here."}
	// Another agent's are untouched by it, and one goes back to nothing kept.
	delete(ps.Agents, Auditor)
	saved(t, doc, other, PromptsKey, ps)
	for table, want := range map[string]int{"prompt_versions": 5, "test_runs": 1, "test_case_results": 1, "test_run_replays": 0, "test_run_cases": 0} {
		if got := count(t, d, table); got != want {
			t.Errorf("after going live %s has %d rows, want %d", table, got, want)
		}
	}
	// An agent's instructions point at its row.
	if n := count(t, d, "agents"); n != 3 {
		t.Fatalf("agents has %d rows", n)
	}
}

// The test cases and the pilots' answers likewise.
func TestTestsAndThePilotLogInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	day := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	tests, other := NewTestsPG(d), NewTestsPG(d)
	ts := Tests{Next: 3, List: []TestCase{
		{ID: "t1", Agent: Troubleshooter, Name: "a crash loop", Question: "Why?", Cluster: "prod",
			Recorded: []sherlock.Recorded{{Tool: "get_pod", Input: json.RawMessage(`{"name":"web-1","namespace":"shop"}`), Result: `{"status":"CrashLoopBackOff"}`}, {Tool: "pod_logs", Result: "denied", Error: true}},
			Expect:   Expect{Tools: []string{"get_pod"}, HandOvers: []string{HostDoctor}, Refuse: true, Points: []string{"CrashLoopBackOff"}, Secrets: []string{"PlantedPass-77Qz"}}, Base: true, By: "ann", At: day},
		{ID: "t3", Agent: Auditor, Name: "nothing expected", Question: "Anything?"},
	}}
	saved(t, tests, other, TestsKey, ts)
	if n := count(t, d, "test_case_recordings"); n != 2 {
		t.Fatalf("recordings: %d", n)
	}
	// A case in front moves the others; one changes; one goes.
	ts.Next = 4
	ts.List = []TestCase{{ID: "t4", Agent: Auditor, Name: "new", Question: "New?"}, ts.List[0]}
	ts.List[1].Recorded = ts.List[1].Recorded[:1]
	saved(t, tests, other, TestsKey, ts)
	if n, m := count(t, d, "test_cases"), count(t, d, "test_case_recordings"); n != 2 || m != 1 {
		t.Fatalf("cases %d, recordings %d", n, m)
	}
	ts.List = nil
	saved(t, tests, other, TestsKey, ts)

	pilot, reader := NewPilotPG(d), NewPilotPG(d)
	var l PilotLog
	add := func(n int) {
		for i := 0; i < n; i++ {
			l.Next++
			l.Entries = append(l.Entries, PilotEntry{N: l.Next, Agent: Sherlock, Version: "pilot r5", At: day.Add(time.Duration(l.Next) * time.Second), Who: "bob@corp.example", Cluster: "dev",
				Question: "What needs attention?", Answer: "Nothing.", Model: "claude-opus-5-5", CostUSD: 0.0125})
		}
	}
	add(3)
	saved(t, pilot, reader, PilotKey, l)
	// The log grows at its end and is cut at its start.
	add(2)
	l.Entries = append([]PilotEntry(nil), l.Entries[2:]...)
	saved(t, pilot, reader, PilotKey, l)
	if n := count(t, d, "pilot_entries"); n != 3 {
		t.Fatalf("entries: %d", n)
	}
	// Anything else is written whole.
	l.Entries[1].Answer = "Something."
	saved(t, pilot, reader, PilotKey, l)
	l.Entries = nil
	saved(t, pilot, reader, PilotKey, l)
	if n := count(t, d, "pilot_entries"); n != 0 {
		t.Fatalf("entries left: %d", n)
	}
}

// An agent's instructions keep their rows' ids (DB-81): a test run that
// saves the draft once a case updates the draft's run in place and adds
// one result, and no row of prompt_versions is written; a version added is one row more; the draft gone live loses its
// row, and the others keep theirs.
func TestAnAgentsInstructionsKeepTheirRowsIDs(t *testing.T) {
	d := dbtest.Open(t)
	doc := NewPromptsPG(d)
	ctx := context.Background()
	day := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	house := "Answer briefly."
	run := &TestRun{Rev: 2, At: day, By: "ann", Cases: 3, State: RunRunning,
		Planned: []CaseState{{ID: "b1", Name: "one", Base: true, State: CaseWaiting}, {ID: "t1", Name: "two", State: CaseWaiting}, {ID: "t2", Name: "three", State: CaseWaiting}}}
	ps := Prompts{Agents: map[string]*AgentPrompt{
		Sherlock: {House: &house, Live: 1, Revs: 2, Notes: map[string]string{AllClusters: "Every cluster."},
			Versions:  []Version{{N: 1, House: house, By: "ann", At: day.Add(-time.Hour), Why: "made live"}},
			Draft:     &Draft{Rev: 2, House: "Answer in haiku.", By: "ann", At: day, Test: run},
			Revisions: []Revision{{Rev: 1, House: "Answer in verse.", By: "ann", At: day.Add(-2 * time.Hour)}, {Rev: 2, House: "Answer in haiku.", By: "ann", At: day}}},
		Auditor: {House: &house, Live: 1, Versions: []Version{{N: 1, House: house, By: "bob", At: day}}},
	}}
	saved(t, doc, doc, PromptsKey, ps)
	ids := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		rows, err := d.Pool().Query(ctx, `SELECT agent_id || ' ' || state || ' ' || CASE state WHEN 'version' THEN version_number::text WHEN 'revision' THEN revision_number::text ELSE '' END,
			prompt_version_id::text || ' ' || xmin::text FROM prompt_versions`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			out[k] = v
		}
		return out
	}
	runRow := func() (id string) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, "SELECT test_run_id::text FROM test_runs").Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	was, wasRun := ids(), runRow()
	draft := Sherlock + " draft "
	// The test run goes case by case.
	for i := range run.Planned {
		run.Planned[i].State = CasePassed
		run.Passed++
		run.Results = append(run.Results, TestResult{ID: run.Planned[i].ID, Name: run.Planned[i].Name, Passed: true, Answer: "Yes.", Checks: []Check{{What: "says yes", OK: true}}})
		saved(t, doc, doc, PromptsKey, ps)
		now := ids()
		for k, v := range was {
			if now[k] != v {
				t.Errorf("case %d: %s was %s and is %s", i, k, v, now[k])
			}
		}
		if strings.Fields(now[draft])[0] != strings.Fields(was[draft])[0] || runRow() != wasRun {
			t.Fatalf("case %d: the draft's row or its run has a new id", i)
		}
		was = now
	}
	if n, m := count(t, d, "test_case_results"), count(t, d, "test_case_result_checks"); n != 3 || m != 3 {
		t.Fatalf("%d results, %d checks", n, m)
	}
	// The draft goes live: its row becomes version 2's, with its id (the
	// twelfth cluster test's F2), and the rest keep their ids.
	s := ps.Agents[Sherlock]
	live := "Answer in haiku."
	s.Versions = append(s.Versions, Version{N: 2, House: live, By: "ann", At: day.Add(time.Hour), Why: "made live", Rev: 2})
	s.House, s.Live, s.Draft = &live, 2, nil
	saved(t, doc, doc, PromptsKey, ps)
	now := ids()
	for k, v := range was {
		if k == draft {
			if _, ok := now[k]; ok {
				t.Error("there is still a draft")
			}
			if f := strings.Fields(now[Sherlock+" version 2"]); len(f) == 0 || f[0] != strings.Fields(v)[0] {
				t.Errorf("the draft had the row %s and version 2 has %q", v, now[Sherlock+" version 2"])
			}
			continue
		}
		if f := strings.Fields(now[k]); len(f) == 0 || f[0] != strings.Fields(v)[0] {
			t.Errorf("%s had the row %s and has %q", k, v, now[k])
		}
	}
	if n := count(t, d, "test_runs"); n != 0 {
		t.Fatalf("the draft's test run is kept: %d runs", n)
	}
}
