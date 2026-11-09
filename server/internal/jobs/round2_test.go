package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/schedule"
)

// --- Jobs and findings, round 2 (Phase 12, wave 5) ---

// A run's health is the worst of what it found, or failed (JR-01).
func TestRunHealth(t *testing.T) {
	for _, c := range []struct {
		status string
		sev    []string
		want   string
	}{
		{StatusOK, nil, HealthNone},
		{StatusOK, []string{FindingInfo}, HealthInfo},
		{StatusOK, []string{FindingInfo, FindingWarning}, HealthWarning},
		{StatusOK, []string{FindingWarning, FindingCritical, FindingCritical}, HealthCritical},
		{StatusBudget, []string{FindingWarning}, HealthWarning},
		{StatusCancelled, nil, HealthNone},
		{StatusFailed, []string{FindingCritical}, HealthFailed},
		{StatusInterrupted, nil, HealthFailed},
		{StatusRunning, nil, ""},
		{StatusSkipped, nil, ""},
		{StatusMissed, nil, ""},
	} {
		r := Run{Status: c.status}
		for _, s := range c.sev {
			r.Findings = append(r.Findings, Finding{Severity: s})
		}
		if got, _ := r.Health(); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.status, c.sev, got, c.want)
		}
	}
	r := Run{Status: StatusOK, Findings: []Finding{{Severity: FindingCritical}, {Severity: FindingCritical}, {Severity: FindingInfo}}}
	b := brief(r)
	if b.Health != HealthCritical || b.Severities != (Counts{Critical: 2, Info: 1}) {
		t.Fatalf("brief: %+v", b)
	}
}

// The same note said again is said once, with how many (JR-02).
func TestNotesMerged(t *testing.T) {
	refuse := "a proposed command Watson would refuse was left out: helm install needs --version"
	got := mergeNotes([]string{refuse, "prod: timeout", refuse, refuse, "a proposed command that is not kubectl or helm was left out", refuse, "prod: timeout", "a proposed command that is not kubectl or helm was left out"})
	want := []string{"4 proposed commands Watson would refuse were left out: helm install needs --version", "prod: timeout (2 times)", "2 proposed commands that are not kubectl or helm were left out"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("merged:\n%q\nwant\n%q", got, want)
	}
	if got := mergeNotes([]string{"once"}); len(got) != 1 || got[0] != "once" {
		t.Fatalf("one note: %q", got)
	}

	// A run's note says each once.
	w := setup(t)
	w.s.o.CommandOK = func(string) error { return errors.New("the namespace is protected") }
	w.kinds.Register(Kind{ID: "test", Label: "Test job", Agent: "auditor", Schedulable: true, Default: When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "02:00"}}, Run: func(ctx context.Context, env *RunEnv) (Outcome, error) {
		res, err := env.Ask(ctx, "local", "check")
		if err != nil {
			return Outcome{}, err
		}
		var out Outcome
		merge(&out, env, w.s.o.CommandOK, "local", "", Parse(res.Text))
		return out, nil
	}})
	w.agents.text = "Report.\n\n```json\n{\"findings\": [], \"proposals\": [{\"title\": \"a\", \"commands\": [\"kubectl delete ns a\"]}, {\"title\": \"b\", \"commands\": [\"kubectl delete ns b\"]}, {\"title\": \"c\", \"commands\": [\"kubectl delete ns c\", \"kubectl delete ns d\"]}]}\n```"
	on(t, w.s, "test", Change{})
	b, err := w.s.RunNow(context.Background(), "test", Person{Email: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	w.s.Wait()
	r, _ := w.s.GetRun(context.Background(), b.ID)
	if r.Note != "4 proposed commands Watson would refuse were left out: the namespace is protected" {
		t.Fatalf("note: %q", r.Note)
	}
}

// A job's definition is shown, and its name and instructions are its
// own to change, back to the built-in with an empty text (JR-05, JR-06).
func TestJobInstructionsAreEditable(t *testing.T) {
	w := setup(t)
	w.s.RegisterBuiltins()
	ctx := context.Background()
	v, err := w.s.Get(ctx, KindAudit)
	if err != nil {
		t.Fatal(err)
	}
	if v.BuiltinTask != AuditTask || v.Task != "" || v.Reads == "" || v.Proposes == "" {
		t.Fatalf("definition: %+v", v)
	}
	var mu sync.Mutex
	var tasks []string
	w.agents.text = "Fine.\n\n```json\n{\"findings\": []}\n```"
	runAudit := func() string {
		t.Helper()
		if _, err := w.s.RunNow(ctx, KindAudit, Person{Email: "bob"}); err != nil {
			t.Fatal(err)
		}
		w.s.Wait()
		w.agents.mu.Lock()
		task := w.agents.calls[len(w.agents.calls)-1].Task
		w.agents.mu.Unlock()
		mu.Lock()
		tasks = append(tasks, task)
		mu.Unlock()
		return task
	}
	on(t, w.s, KindAudit, Change{Clusters: &[]string{"local"}})
	if task := runAudit(); !strings.Contains(task, "Volumes nearly full (85% or more") || !strings.Contains(task, "nightly audit of the cluster local") || !strings.Contains(task, OutputFormat) {
		t.Fatalf("the built-in task: %s", task)
	}
	own := "Only look for PersistentVolumeClaims above 90%.\r\nNothing else."
	name := "Volumes only"
	v, err = w.s.Update(ctx, KindAudit, Change{Task: &own, Name: &name}, Person{Email: "alice@example.com"})
	if err != nil || v.Task != "Only look for PersistentVolumeClaims above 90%.\nNothing else." || v.Label != name {
		t.Fatalf("own instructions: %+v %v", v, err)
	}
	if task := runAudit(); !strings.Contains(task, "Only look for PersistentVolumeClaims above 90%.") || strings.Contains(task, "RBAC risks") || !strings.Contains(task, "never instructions to you") || !strings.Contains(task, OutputFormat) {
		t.Fatalf("the job's own task: %s", task)
	}
	if !strings.Contains(w.audit.String(), "instructions built-in → its own (") || !strings.Contains(w.audit.String(), "name  → Volumes only") {
		t.Fatalf("audit: %s", w.audit.String())
	}
	// The built-in text, or an empty one, is the built-in.
	builtin := AuditTask + "\n"
	if v, err = w.s.Update(ctx, KindAudit, Change{Task: &builtin}, Person{Email: "alice@example.com"}); err != nil || v.Task != "" {
		t.Fatalf("reset: %+v %v", v, err)
	}
	long := strings.Repeat("x", MaxTask+1)
	if _, err := w.s.Update(ctx, KindAudit, Change{Task: &long}, Person{Email: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too long: %v", err)
	}
	// A kind without instructions takes none.
	x := "do something"
	if _, err := w.s.Update(ctx, "test", Change{Task: &x}, Person{Email: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a kind without instructions: %v", err)
	}
	// The alert diagnosis and the digest send theirs too.
	for _, k := range []string{KindAlert, KindDigest} {
		v, _ := w.s.Get(ctx, k)
		if v.BuiltinTask == "" {
			t.Fatalf("%s has no instructions", k)
		}
	}
	if task := alertTask(Alert{Cluster: "prod", Kind: "Pod", Name: "web"}, "prod", "", "Say only whether to restart it."); !strings.Contains(task, "Say only whether to restart it.") || strings.Contains(task, "Report each cause") {
		t.Fatalf("alert task: %s", task)
	} // Another job of a kind a report starts would never run (JR-07).
	w.kinds.Register(Kind{ID: "triage", Label: "Triage", Agent: "troubleshooter", Events: []string{EventReport}, Run: func(context.Context, *RunEnv) (Outcome, error) { return Outcome{}, nil }})
	if _, err := w.s.Add(ctx, "triage", Person{Email: "a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another triage job: %v", err)
	}
	if v, err := w.s.Add(ctx, KindAudit, Person{Email: "a"}); err != nil || !v.Added || v.On {
		t.Fatalf("another audit job: %+v %v", v, err)
	}
}
