package ai

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// --- Phase 12.1: the prompts' maintenance ---

// Every mark of a prompt file is filled: no agent reads "{{…}}". The
// rounds an answer gets are the engine's (PM-C2), and the rules of an
// Automation script and of its review are the Automation screen's own
// (PM-J9), so a script drafted through a hand-over keeps them too.
func TestEveryMarkOfAPromptFileIsFilled(t *testing.T) {
	texts := map[string]string{"Sherlock, view changes on": Core(Sherlock, true), "Sherlock's compact form": CompactCore()}
	for _, info := range Catalog {
		texts[info.Name] = Core(info.ID, false)
	}
	for name, text := range texts {
		for _, mark := range []string{commandsMark, roundsMark, scriptRulesMark, reviewRulesMark, "{{rounds", "{{automation", "{{watson"} {
			if strings.Contains(text, mark) {
				t.Errorf("%s still holds the mark %s", name, mark)
			}
		}
	}
	rounds := "You have " + strconv.Itoa(sherlock.DefaultMaxRounds) + " rounds of reads for one answer; a hand-over is one."
	for _, id := range []string{Sherlock, Troubleshooter, Auditor, Summarizer} {
		if !strings.Contains(Core(id, false), rounds) {
			t.Errorf("%s is not told its rounds", id)
		}
	}
	if !strings.Contains(Core(AutomationAuthor, false), automation.ScriptRules) || !strings.Contains(automation.DraftPrompt, automation.ScriptRules) {
		t.Error("the script rules are not in the Automation author's core and in the draft's task alike")
	}
	if !strings.Contains(Core(ScriptReviewer, false), automation.ReviewRules) || !strings.Contains(automation.ReviewPrompt, automation.ReviewRules) {
		t.Error("the review's rules are not in the Script reviewer's core and in the review's task alike")
	}
}

// An agent has the tools its instructions send it to (PM-A2, PM-A4, SR-10):
// the Troubleshooter reads policies, Helm releases, Automation's runs and
// their output, Watson's history, the audit log and the cases, the
// Auditor volumes and the Ingress path with its certificates, the
// Summarizer the jobs' runs and the cases.
func TestAnAgentHasTheToolsItsInstructionsName(t *testing.T) {
	for id, tools := range map[string][]string{
		Troubleshooter: {"list_policies", "list_helm_releases", "get_helm_release", "automation_runs", "list_alerts",
			"get_automation_run", "watson_history", "audit_log", "list_cases", "get_case"},
		Auditor:    {"list_volumes", "trace_ingress", "review_cluster"},
		Summarizer: {"list_job_runs", "get_job_run", "list_cases"},
	} {
		info, _ := InfoOf(id)
		for _, tool := range tools {
			if !slices.Contains(info.Tools, tool) {
				t.Errorf("%s has no %s", info.Name, tool)
			}
			if !slices.Contains(readTools, tool) {
				t.Errorf("%s is not one of the toolbox's reads", tool)
			}
		}
	}
}

// The rules every agent keeps say the four things Phase 12.1 added
// (PM-C1 to PM-C4), and the compact form keeps the first in a line.
func TestTheRulesEveryAgentKeeps(t *testing.T) {
	common := promptFile("common")
	for _, want := range []string{
		"- Say only what you read.",
		"Name an object as affected only on its own evidence",
		"never repeat a read with the same input",
		"ask the knowledge base once (ask_knowledge)",
		"Keep an agent's findings as it gave them",
	} {
		if !strings.Contains(common, want) {
			t.Errorf("the rules every agent keeps do not say %q", want)
		}
	}
	if !strings.Contains(CompactCore(), "Say only what you read; call an unconfirmed cause \"likely\"") {
		t.Error("the compact form does not keep the evidence rule")
	}
}
