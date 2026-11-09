package casework

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
)

// The simulator's cases, read back by the services as the console reads
// the ConfigMaps: a person's case New, an agent-found one In progress
// seen ×3, one closed as fixed with its RCA and verified, one closed as
// an exception, one as a known issue agents still see, one as no fix
// needed, and a run with mixed routing.
func TestTheSimulatorsCases(t *testing.T) {
	ctx := context.Background()
	seed, err := MakeSeed(time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	load := func(keys map[string]string) *kv.Memory {
		m := &kv.Memory{}
		for k, v := range keys {
			m.SaveKey(ctx, k, []byte(v), "")
		}
		return m
	}
	objs := &kv.MemoryObjects{}
	for n, keys := range seed.Cases {
		version := ""
		for k, v := range keys {
			version, _ = objs.Save(ctx, n, k, []byte(v), version)
		}
	}
	iss := issues.New(issues.Options{Backend: load(seed.Issues), Cases: objs})
	cw := load(seed.Casework)
	svc := New(Options{Backend: cw, Issues: iss, Lessons: lessons.New(lessons.Options{Backend: cw})})
	list, err := svc.List(ctx, olivia, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	has := func(ok func(Brief) bool) bool {
		for _, b := range list {
			if ok(b) {
				return true
			}
		}
		return false
	}
	for what, ok := range map[string]func(Brief) bool{
		"a person's case New": func(b Brief) bool { return !b.AgentFound && b.Status == issues.StatusNew },
		"an agent's case In progress ×3": func(b Brief) bool {
			return b.AgentFound && b.Status == issues.StatusInProgress && b.Seen != nil && b.Seen.Count == 3
		},
		"fixed with its RCA, verified": func(b Brief) bool { return b.Outcome == issues.OutcomeFix && b.RCACause != "" && b.Verified != nil },
		"an exception":                 func(b Brief) bool { return b.Outcome == issues.OutcomeException && b.Exception != "" },
		"a known issue":                func(b Brief) bool { return b.Outcome == issues.OutcomeKnownIssue && b.KnownIssue != "" },
		"no fix needed":                func(b Brief) bool { return b.Outcome == issues.OutcomeNoFixNeeded },
		// The owner's feedback (wave 6c): an incident number and a type, a
		// case queued for its analysis, the agent named as the creator.
		"a report with an incident number": func(b Brief) bool {
			return b.Source.Kind == SourceReport && b.Incident == "INC0012345" && b.Type == issues.TypePerformance
		},
		"a case queued for its analysis": func(b Brief) bool { return b.AnalysisQueued && b.AnalysisState == "queued" && b.Priority == 2 },
		"a security case found by the Auditor": func(b Brief) bool {
			return b.Type == issues.TypeSecurity && b.CreatedBy != nil && *b.CreatedBy == (issues.Actor{Name: "Auditor", Kind: issues.ActorAgent})
		},
	} {
		if !has(ok) {
			t.Errorf("no %s: %+v", what, list)
		}
	}
	var rec struct {
		Next int        `json:"next"`
		Runs []jobs.Run `json:"runs"`
	}
	if err := json.Unmarshal([]byte(seed.Runs[jobs.RunsKey]), &rec); err != nil || len(rec.Runs) != 3 || rec.Next != 3 {
		t.Fatalf("runs: %d %v", len(rec.Runs), err)
	}
	r := rec.Runs[0].Routed
	if rec.Runs[0].ID != "run-3" || r.Seen != 1 || r.Excepted != 1 || r.Known != 1 || r.Opened != 1 || r.Invalid != 1 || r.Verified != 1 || r.Ignored != 1 || len(rec.Runs[0].VerifiedCases) != 1 {
		t.Fatalf("the last run: %+v", rec.Runs[0])
	}
	k, _ := iss.Known(ctx, system, true)
	if len(k) != 1 || k[0].StillPresent == nil || k[0].StillPresent.Count != 1 {
		t.Fatalf("the known issue: %+v", k)
	}
}
