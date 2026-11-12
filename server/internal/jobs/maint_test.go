package jobs

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// --- Phase 12.1: the prompts' maintenance ---

// PM-J1c: "still": null says the agent could not read the object. It is no
// answer: read as false it would verify a fix nobody re-checked.
func TestAStillOfNullIsNoAnswer(t *testing.T) {
	p := Parse("Done.\n\n```json\n" + `{"findings": [], "proposals": [], "rechecked": [
 {"check": "pod.no-limits", "resource": "shop/Deployment/web", "still": null, "evidence": "not allowed to read it"},
 {"check": "pod.no-probes", "resource": "shop/Deployment/api", "evidence": "the tool failed"},
 {"check": "pod.root-user", "resource": "shop/Deployment/db", "still": false, "evidence": "runAsNonRoot is set"}]}` + "\n```")
	if len(p.Rechecked) != 3 {
		t.Fatalf("parsed: %+v", p.Rechecked)
	}
	if x := p.Rechecked[0]; x.Still != nil || x.Evidence != "not allowed to read it" {
		t.Errorf("null: %+v", x)
	}
	if x := p.Rechecked[1]; x.Still != nil {
		t.Errorf("no still at all: %+v", x)
	}
	if x := p.Rechecked[2]; x.Still == nil || *x.Still {
		t.Errorf("false: %+v", x)
	}
}

// PM-J1d: a finding's confidence is one of two words, in any case; anything
// else, an answer of before the field among them, is none.
func TestAFindingsConfidence(t *testing.T) {
	for in, want := range map[string]string{"confirmed": ConfidenceConfirmed, " Likely ": ConfidenceLikely, "": "", "sure": "", "confirmed|likely": ""} {
		if got := NormalConfidence(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	p := Parse("```json\n" + `{"findings": [{"severity": "warning", "check": "pod.oomkilled", "title": "web is killed for memory", "confidence": "likely"}], "proposals": [], "rechecked": []}` + "\n```")
	if len(p.Findings) != 1 || p.Findings[0].Confidence != "likely" {
		t.Fatalf("parsed: %+v", p.Findings)
	}
}

// The output format says what the three severities mean, which object a
// finding names, and what "still" may be (PM-J1a to PM-J1c).
func TestTheOutputFormatSaysWhatItsWordsMean(t *testing.T) {
	for _, want := range []string{
		"- Severity: critical when a service is down",
		"- Name the object that owns the problem: the Deployment, StatefulSet, DaemonSet or CronJob",
		`"still" is false only when you read the object in this run and the problem is gone`,
		"null when you could not read it",
		`"confidence": "confirmed|likely"`,
		"how to undo it, and the read that shows it worked",
	} {
		if !strings.Contains(OutputFormat, want) {
			t.Errorf("the output format does not say %q", want)
		}
	}
}

// seqAgents answers each call with the next of its texts.
type seqAgents struct {
	mu    sync.Mutex
	texts []string
	tasks []string
}

func (f *seqAgents) Run(_ context.Context, req AgentRequest) AgentResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, req.Task)
	text := ""
	if n := len(f.tasks) - 1; n < len(f.texts) {
		text = f.texts[n]
	}
	return AgentResult{Text: text, Model: "claude-test", CostUSD: 0.25}
}

func (f *seqAgents) Usable(string, string) string { return "" }

const sampleBlock = "```json\n" + `{"findings": [{"severity": "warning", "check": "pod.no-limits", "title": "No limits on web"}], "proposals": [], "rechecked": []}` + "\n```"

// PM-C6: an answer without a block the console can read is noted on the
// run, and the agent is asked once for the block alone, which is read
// with the first answer's report.
func TestAnAnswerWithoutABlockIsAskedForOnce(t *testing.T) {
	ctx := context.Background()
	env := func(texts ...string) (*RunEnv, *seqAgents) {
		a := &seqAgents{texts: texts}
		w := setup(t)
		w.s.o.Agents = a
		return &RunEnv{s: w.s, Kind: Kind{ID: KindAudit, Label: "Nightly audit", Agent: agentAuditor}, Job: Job{ID: KindAudit}}, a
	}

	// A block at once: one call, no note.
	e, a := env("All well.\n\n" + sampleBlock)
	p, err := e.AskParsed(ctx, "local", "the task", "local")
	if err != nil || len(p.Findings) != 1 || p.Report != "All well." || len(a.tasks) != 1 || len(e.notes) != 0 {
		t.Fatalf("a block at once: %+v %v, %d calls, notes %v", p, err, len(a.tasks), e.notes)
	}

	// None, then one when asked: the first answer's report, the second's
	// findings, and a note. The second task carries the first answer.
	e, a = env("I looked at the pods and found one without limits, and then I was cut", sampleBlock)
	p, err = e.AskParsed(ctx, "local", "the task", "local")
	if err != nil || len(p.Findings) != 1 || !strings.HasPrefix(p.Report, "I looked at the pods") {
		t.Fatalf("repaired: %+v %v", p, err)
	}
	if len(a.tasks) != 2 || !strings.HasPrefix(a.tasks[1], RepairTask) || !strings.Contains(a.tasks[1], "<answer>\nI looked at the pods") || !strings.HasSuffix(a.tasks[1], OutputFormat) {
		t.Errorf("the second task: %q", a.tasks)
	}
	if len(e.notes) != 1 || !strings.Contains(e.notes[0], "asked for the block alone, it gave one") {
		t.Errorf("notes: %v", e.notes)
	}
	if e.cost != 0.5 {
		t.Errorf("both calls are counted: $%.2f", e.cost)
	}

	// None twice: the report is kept, there are no findings, and the
	// note says so; it is asked once, never a third time.
	e, a = env("Only words.", "Still only words.")
	p, err = e.AskParsed(ctx, "local", "the task", "local")
	if err != nil || len(p.Findings) != 0 || p.Report != "Only words." || len(a.tasks) != 2 {
		t.Fatalf("no block twice: %+v %v, %d calls", p, err, len(a.tasks))
	}
	if len(e.notes) != 1 || !strings.Contains(e.notes[0], "has no findings from it") {
		t.Errorf("notes: %v", e.notes)
	}
}

// PM-R1, PM-R2: a rule two jobs share is one rule, said one way, and every
// rule has a type the cases know.
func TestTheBuiltInRulesAgree(t *testing.T) {
	says := map[string]Check{}
	for _, k := range BuiltinRules {
		seen := map[string]bool{}
		for _, c := range k.Checks {
			if seen[c.ID] {
				t.Errorf("%s lists %s twice", k.Label, c.ID)
			}
			seen[c.ID] = true
			if c.Says == "" || c.Type == "" {
				t.Errorf("%s: %+v", k.Label, c)
			}
			if was, ok := says[c.ID]; ok && (was.Says != c.Says || was.Type != c.Type) {
				t.Errorf("%s is said two ways: %+v and %+v", c.ID, was, c)
			}
			says[c.ID] = c
		}
	}
	// The Alerts tab raises alerts for claims that wait and volumes that
	// do not mount: the alert diagnosis has a rule for each.
	for _, id := range []string{"pvc.unbound", "pvc.mount-failed", "webhook.unreachable"} {
		if !(builtinRules{}).Known(Kind{Checks: AlertChecks}, id) {
			t.Errorf("the alert diagnosis has no rule %s", id)
		}
	}
}
