package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// What an agent on its own is told about its output, and how the console
// reads it back (JB-09): the report in prose, then one JSON block of the
// findings and the changes it proposes. The block is read, never run; a
// command in it becomes a proposal that waits for a person.

// OutputFormat ends every task of an agent on its own.
const OutputFormat = `Nobody is at the screen: you work on your own, read only, and nothing you write runs. A change you propose waits for a person to approve it.

Write your report for an operator who reads it later: what you checked, what you found and why it matters, most important first, short. Then end with exactly one fenced block of JSON, and nothing after it:

` + "```json" + `
{"findings": [{"severity": "critical|warning|info", "check": "a validation rule's id given above, or other.<slug>", "title": "one line", "detail": "what and why", "evidence": ["what you saw, quoted"], "cluster": "cluster id", "resource": "namespace/Kind/name, Kind/name, or empty for the cluster", "expected": false, "expectedWhy": "", "checkWhy": ""}],
 "proposals": [{"title": "what to change, one line", "reasoning": "why", "cluster": "cluster id", "fixes": [1], "commands": ["kubectl ..."], "impact": "what changes for whom", "risk": "low|medium|high", "evidence": [{"kind": "log|event|fact|finding", "text": "...", "ref": "namespace/kind/name"}]}],
 "rechecked": [{"check": "the rule id of a fix to re-check", "resource": "its object", "still": true, "evidence": "what you read"}]}
` + "```" + `

Use empty lists when there is nothing.
- A finding becomes a case, or joins the one people already have for the same cluster, resource and check: name the same problem the same way every time. Its severity sets the case's priority: critical P1, warning P3, info P4.
- With a check other.<slug>, write "checkWhy": first what your id means, in one sentence, then why none of the ids given fits. Leave it empty for an id given.
- Set "expected" true, with "expectedWhy" giving why and the evidence, only when the cluster shows the system working as intended: a node annotated asleep by its power manager and cordoned just before it stopped reporting, a planned drain or maintenance. An expected finding opens no case, whatever its severity. Otherwise leave it false.
- When fixes to re-check are listed above, give an entry in "rechecked" for every one, with "still" true or false and what you read; a problem still there is also a finding.
- Propose only kubectl or helm commands Watson runs, one per line, and only when the evidence supports them. "fixes" names what a proposal fixes: positions in your findings list (1 is the first) and/or ids of cases (C-12) or known issues (KI-3) from the keys in use; a proposal that fixes nothing you found is not filed.`

// Quote puts text the agent must treat as data, never as instructions,
// between tags, so nothing inside can close them.
func Quote(tag, text string) string {
	closing := "</" + tag + ">"
	text = strings.ReplaceAll(text, closing, "</ "+tag+">")
	text = strings.ReplaceAll(text, "<"+tag+">", "< "+tag+">")
	return "<" + tag + ">\n" + strings.TrimSpace(text) + "\n" + closing
}

// Parsed is an agent's output read back.
type Parsed struct {
	// Report is the prose without the block.
	Report    string
	Findings  []Finding
	Proposals []Proposed
	// Rechecked are what the agent says of the fixes it was given to
	// re-check (W-1), as it wrote them.
	Rechecked []Recheck
	// Raw is the block, for a kind that asked for more fields.
	Raw json.RawMessage
}

// Proposed is a change as an agent writes it.
type Proposed struct {
	Title     string     `json:"title"`
	Reasoning string     `json:"reasoning"`
	Cluster   string     `json:"cluster"`
	Commands  []string   `json:"commands"`
	Impact    string     `json:"impact"`
	Risk      string     `json:"risk"`
	Evidence  []Evidence `json:"evidence"`
	// Fixes is what it fixes (CF-26): findings of the same answer by
	// their position, from 1, and the ids of cases and known issues.
	Fixes []Fix `json:"fixes"`
}

// Fix is one thing a proposal says it fixes (Phase 12, wave 6b, CF-26):
// a finding of the same answer by its position, from 1, or the id of a
// case ("C-12") or a known issue ("KI-3"). It reads a number, a number
// written as text ("2", "#2") or an id; anything else is nothing, so one
// odd entry never makes the block unreadable.
type Fix struct {
	Position int
	ID       string
}

// UnmarshalJSON reads a number or a string; anything else is no fix.
func (f *Fix) UnmarshalJSON(b []byte) error {
	*f = Fix{}
	var n float64
	if json.Unmarshal(b, &n) == nil {
		if n >= 1 && n == float64(int(n)) {
			f.Position = int(n)
		}
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	s = strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if p, err := strconv.Atoi(s); err == nil {
		if p >= 1 {
			f.Position = p
		}
		return nil
	}
	f.ID = s
	return nil
}

// MarshalJSON writes it back as the agent would.
func (f Fix) MarshalJSON() ([]byte, error) {
	if f.Position > 0 {
		return json.Marshal(f.Position)
	}
	return json.Marshal(f.ID)
}

var jsonBlock = regexp.MustCompile("(?s)```(?:json)?[ \t]*\n(.*?)\n?```")

// Parse reads an agent's output: its last JSON block, and the prose
// before. An answer without a block is all report.
func Parse(text string) Parsed {
	out := Parsed{Report: strings.TrimSpace(text)}
	m := jsonBlock.FindAllStringSubmatchIndex(text, -1)
	for i := len(m) - 1; i >= 0; i-- {
		body := text[m[i][2]:m[i][3]]
		var b struct {
			Findings  []Finding  `json:"findings"`
			Proposals []Proposed `json:"proposals"`
			Rechecked []Recheck  `json:"rechecked"`
		}
		if json.Unmarshal([]byte(body), &b) != nil {
			continue
		}
		out.Report = strings.TrimSpace(text[:m[i][0]] + text[m[i][1]:])
		out.Findings, out.Proposals, out.Raw = b.Findings, b.Proposals, json.RawMessage(body)
		out.Rechecked = b.Rechecked
		return out
	}
	return out
}

// Changes turns what the agent proposed into proposals: kubectl and
// helm commands Watson runs, each an action. A proposal is filed whole or
// not at all (W-6, CF-98): one with a command Watson would refuse, or one
// that is not kubectl or helm, is dropped with every other command it
// has, and the notes say which command and why. A request is about one
// cluster (W-5, CF-97): a proposal that names another is dropped too,
// with a note; one that names none is the cluster's.
func (p Parsed) Changes(check func(string) error, cluster string, evidence ...Evidence) ([]Proposal, []string) {
	var out []Proposal
	var notes []string
	for _, c := range p.Proposals {
		title := strings.TrimSpace(c.Title)
		risk := strings.ToLower(strings.TrimSpace(c.Risk))
		if risk != "low" && risk != "medium" && risk != "high" {
			risk = "medium"
		}
		if cl := strings.TrimSpace(c.Cluster); cl != "" && !strings.EqualFold(cl, cluster) {
			notes = append(notes, fmt.Sprintf("a proposed change named the cluster %q, not %s, which it was asked about, and was not filed: %s", clip(cl, 60), cluster, clip(title, 120)))
			continue
		}
		actions, why := commands(c.Commands, check, c.Impact, risk)
		if why != "" {
			notes = append(notes, fmt.Sprintf("a proposed change was not filed, with none of its commands, because %s: %s", why, clip(title, 120)))
			continue
		}
		if len(actions) == 0 || title == "" {
			continue
		}
		ev := append(append([]Evidence(nil), c.Evidence...), evidence...)
		for i := range ev {
			if ev[i].Kind == "" {
				ev[i].Kind = "fact"
			}
		}
		out = append(out, Proposal{Cluster: cluster, Title: c.Title, Reasoning: c.Reasoning, Evidence: ev, Actions: actions, Fixes: c.Fixes})
	}
	return out, notes
}

// commands are a proposal's command lines as actions, all of them, or
// none and why: the first line that is not kubectl or helm, or that
// Watson would refuse, named with the reason (W-6). Blank lines are no
// commands.
func commands(lines []string, check func(string) error, impact, risk string) ([]Action, string) {
	var actions []Action
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kind, _, _ := strings.Cut(line, " ")
		if kind != "kubectl" && kind != "helm" {
			return nil, fmt.Sprintf("“%s” is not a kubectl or helm command", clip(line, 200))
		}
		if check != nil {
			if err := check(line); err != nil {
				return nil, fmt.Sprintf("Watson would refuse “%s”: %v", clip(line, 200), err)
			}
		}
		actions = append(actions, Action{Kind: kind, Args: map[string]any{"command": line}, Impact: impact, Risk: risk})
	}
	return actions, ""
}
