package ai

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/watson"
)

// rosterIDs are the agents an agent can hand work to.
func rosterIDs(a sherlock.Agent) []string {
	var ids []string
	for _, r := range a.Roster {
		ids = append(ids, r.ID)
	}
	return ids
}

// Sherlock hands each kind of work to its owner and keeps none of it
// (SR-20, PM-D1): with the Troubleshooter in its roster its instructions
// send troubleshooting there and hold no way of troubleshooting of their
// own; with the Troubleshooter off, that work is Sherlock's again, with
// the same manual the Troubleshooter has. So its instructions never say
// both "find the cause" and "hand it over".
func TestSherlockHandsTroubleshootingToTheTroubleshooter(t *testing.T) {
	w := setup(t, nil)
	q := sherlock.Question{Key: sherlock.Key{Cluster: "local"}}
	a, err := w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"You are the front desk of the console's agents",
		"What you hand over whenever its owner is in your list, even when a read or two of your own might answer it:",
		"or a request to troubleshoot or diagnose: the Troubleshooter.",
		"Do not read the cluster for a cause, a plan or a review yourself",
		"\n- Troubleshooter (troubleshooter): ",
	} {
		if !strings.Contains(a.Instructions, want) {
			t.Errorf("Sherlock's instructions lack %q", want)
		}
	}
	if !slices.Contains(rosterIDs(a), Troubleshooter) {
		t.Fatalf("the roster %v has no Troubleshooter", rosterIDs(a))
	}
	// Every owner can be asked: none of their work is Sherlock's.
	for _, own := range []string{"is your own work", sherlock.TroubleshootingManual, sherlock.TroubleshootingReport, sherlock.DeficiencyReview, "How you troubleshoot"} {
		if strings.Contains(a.Instructions, own) {
			t.Errorf("with every owner in its roster Sherlock is still given %q", own[:min(len(own), 60)])
		}
	}
	// Its compact form hands nothing over and keeps troubleshooting itself.
	if c := CompactCore(); strings.Contains(c, "front desk") || strings.Contains(c, "hand_over") {
		t.Error("the compact core delegates, though it has no hand_over")
	}

	off := false
	if err := w.ai.SaveAgent(context.Background(), Troubleshooter, AgentChange{AgentSettings: AgentSettings{On: &off}}); err != nil {
		t.Fatal(err)
	}
	a, err = w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(rosterIDs(a), Troubleshooter) || strings.Contains(a.Instructions, "\n- Troubleshooter (troubleshooter): ") {
		t.Fatalf("the Troubleshooter is off but offered: %v", rosterIDs(a))
	}
	for _, want := range []string{
		"Finding the cause of a failure is your own work here, since no Troubleshooter is in your list of agents.",
		sherlock.TroubleshootingManual,
		sherlock.TroubleshootingReport,
	} {
		if !strings.Contains(a.Instructions, want) {
			t.Errorf("without the Troubleshooter Sherlock lost %q", want[:min(len(want), 60)])
		}
	}
	// Only that lane came back: the Auditor, the Host doctor and the
	// Change planner still own theirs.
	for _, other := range []string{"A review against good practice is your own work", "The checks on a node are your own work", "The commands of a change are your own work"} {
		if strings.Contains(a.Instructions, other) {
			t.Errorf("with its owner on, Sherlock was given %q", other)
		}
	}
}

// The work Sherlock does itself is exactly the lanes whose owners are not
// in its roster (PM-D1); with no agents at all it is everything, said
// without a list to point at, as the engine's own built-in is.
func TestSherlocksOwnWorkFollowsItsRoster(t *testing.T) {
	entry := func(ids ...string) []sherlock.RosterEntry {
		var out []sherlock.RosterEntry
		for _, id := range ids {
			out = append(out, sherlock.RosterEntry{ID: id})
		}
		return out
	}
	if got := OwnLanes(entry(Troubleshooter, Auditor, HostDoctor, ChangePlanner, Mycroft)); got.Any() {
		t.Errorf("every owner listed: %+v", got)
	}
	if got := OwnWork(entry(Troubleshooter, Auditor, HostDoctor, ChangePlanner)); got != "" {
		t.Errorf("every owner listed, own work: %q", got)
	}
	if got := OwnLanes(entry(Troubleshooter, Mycroft)); got != (sherlock.Lanes{Reviews: true, Hosts: true, Changes: true}) {
		t.Errorf("only the Troubleshooter listed: %+v", got)
	}
	if got := OwnLanes(nil); got != sherlock.AllLanes {
		t.Errorf("no agents: %+v", got)
	}
	all := OwnWork(nil)
	if strings.Contains(all, "in your list of agents") || !strings.Contains(all, sherlock.TroubleshootingManual) || !strings.Contains(all, sherlock.DeficiencyReview) {
		t.Errorf("no agents, own work:\n%s", all)
	}
	// Without the agents, the engine's built-in is the front desk's part
	// and all of the work.
	if got, want := sherlock.BuiltIn(true, watson.Commands()), sherlock.FrontDesk(true, watson.Commands())+"\n\n"+sherlock.OwnWork(sherlock.AllLanes, false); got != want {
		t.Error("the engine's built-in is not the front desk's part and every lane")
	}
}

// Every kind of work has one owner, said the same way to every agent
// (PM-D2): the rules every agent keeps say so in one line and leave the
// owners to the list of agents (PM-D6: no second list), each agent's own
// core says where its work ends, and no core tells its agent to do
// another's work.
func TestEveryKindOfWorkHasOneOwner(t *testing.T) {
	common := promptFile("common")
	for _, want := range []string{
		"Every kind of work has one owner.",
		"the list of agents after these instructions says whose the rest is",
		"If the person or a job asked you, hand that part to its owner with hand_over",
		"If your task begins \"" + strings.TrimSpace(sherlock.HandedBy) + "\", hand nothing on",
		"If the owner is not in your list or could not be asked, do that part yourself and say so",
		"Keep an agent's findings as it gave them",
		"answer it complete, as the person should read it",
	} {
		if !strings.Contains(common, want) {
			t.Errorf("the rules every agent keeps do not say %q", want)
		}
	}
	// The two rules that used to contradict each other (PM-D2): a
	// hand-over is by owner, never by how small the work looks.
	for _, gone := range []string{"hand over what one or two reads answer", "answer it briefly", "Whose work is whose", "- Troubleshooter: "} {
		if strings.Contains(common, gone) {
			t.Errorf("the rules every agent keeps still say %q", gone)
		}
	}
	// Each agent's core names where its work ends, and holds no
	// instruction to do the neighbour's.
	ends := map[string][]string{
		Troubleshooter:   {"Your work ends at the cause and what must change, said in words", "never a command of your own"},
		Auditor:          {"Your work ends at the finding and what must change", "leave its cause to the Troubleshooter"},
		ChangePlanner:    {"You plan the change you are given, and diagnose nothing"},
		HostDoctor:       {"Why a pod, a Service or a volume fails is the Troubleshooter's", "A script is the Automation author's"},
		AutomationAuthor: {"What is wrong on a node is the Host doctor's to say", "never you your own"},
		ScriptReviewer:   {"You review; you never rewrite."},
		Mycroft:          {"Live state is the other agents' work"},
		Summarizer:       {"You report; you do not diagnose."},
	}
	for id, wants := range ends {
		core := Core(id, false)
		for _, want := range wants {
			if !strings.Contains(core, want) {
				t.Errorf("%s is not told %q", id, want)
			}
		}
	}
	for id, bad := range map[string][]string{
		Troubleshooter: {"Give the commands of a plain fix yourself", "Hand a cause in the node's operating system to the Host doctor"},
		Auditor:        {"Hand a fix larger than a few plain commands"},
	} {
		for _, b := range bad {
			if strings.Contains(Core(id, false), b) {
				t.Errorf("%s is still told %q", id, b)
			}
		}
	}
	// The manual and the two reports are said once and held by their
	// owners (PM-D3).
	if c := Core(Troubleshooter, false); !strings.Contains(c, sherlock.TroubleshootingManual) || !strings.Contains(c, sherlock.TroubleshootingReport) {
		t.Error("the Troubleshooter does not hold the manual and its report")
	}
	if c := Core(Auditor, false); !strings.Contains(c, sherlock.DeficiencyReview) {
		t.Error("the Auditor does not hold the deficiency review")
	}
	// Sherlock's own core gives it none of that work, and no rule against
	// its own: it stays on the cluster and the console.
	sh := Core(Sherlock, true)
	for _, own := range []string{sherlock.TroubleshootingManual, sherlock.TroubleshootingReport, sherlock.DeficiencyReview, "Find out why something in the cluster is wrong"} {
		if strings.Contains(sh, own) {
			t.Errorf("Sherlock's core still holds %q", own[:min(len(own), 60)])
		}
	}
	if !strings.Contains(sh, "Stay on this cluster and this console") {
		t.Error("Sherlock's core does not let it answer about the console")
	}
}

// An agent's instructions stay short (PM-D6): what every agent is told is
// said once, the owners of the work are named by the list of agents and
// nowhere else, and only an agent that may give Watson's commands is told
// what Watson runs. The limits are bytes of the assembled instructions
// with every agent on, before the cluster's description; a change that
// passes one should shorten something else rather than raise it.
func TestInstructionsStayShort(t *testing.T) {
	limits := map[string]int{Sherlock: 14000, Troubleshooter: 10500}
	for _, info := range Catalog {
		var roster []sherlock.RosterEntry
		for _, other := range Catalog {
			if other.ID != info.ID && other.ID != Sherlock {
				roster = append(roster, sherlock.RosterEntry{ID: other.ID, Name: other.Name, Purpose: other.Purpose})
			}
		}
		text := Assemble(Core(info.ID, true), roster, "")
		limit := limits[info.ID]
		if limit == 0 {
			limit = 7000
		}
		if n := len(text); n > limit {
			t.Errorf("%s's instructions are %d bytes, over %d", info.Name, n, limit)
		}
		told := strings.Contains(text, watson.Commands())
		if want := info.ID == Sherlock || GivesCommands(info.ID); told != want {
			t.Errorf("%s told what Watson runs: %v, want %v", info.Name, told, want)
		}
	}
	if n := len(promptFile("common")); n > 3200 {
		t.Errorf("the rules every agent keeps are %d bytes, over 3200", n)
	}
}
