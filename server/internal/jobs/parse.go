package jobs

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// What an agent on its own is told about its output, and how the console
// reads it back (JB-09): the report in prose, then one JSON block of the
// findings and the changes it proposes. The block is read, never run; a
// command in it becomes a proposal that waits for a person.

// OutputFormat ends every task of an agent on its own.
const OutputFormat = `Nobody is at the screen: you work on your own and read only. You cannot change anything, and nothing you write runs; a change you think is needed becomes a proposal a person approves or not.

Write your report for an operator who reads it later: what you checked, what you found and why it matters, most important first, short. Then end with exactly one fenced block of JSON, and nothing after it:

` + "```json" + `
{"findings": [{"severity": "critical|warning|info", "check": "a check id given above, or other.<slug>", "title": "one line", "detail": "what and why", "evidence": ["what you saw, quoted"], "cluster": "cluster id", "resource": "namespace/Kind/name, Kind/name, or empty for the cluster", "expected": false, "expectedWhy": "", "checkWhy": ""}],
 "proposals": [{"title": "what to change, one line", "reasoning": "why", "cluster": "cluster id", "fixes": [1], "commands": ["kubectl ..."], "impact": "what changes for whom", "risk": "low|medium|high", "evidence": [{"kind": "log|event|fact|finding", "text": "...", "ref": "namespace/kind/name"}]}]}
` + "```" + `

Use empty lists when there is nothing. Each finding becomes a case, or joins the one people already have for the same cluster, resource and check: name the check the same way every time for the same problem. When no check id given fits and you name a finding's check other.<slug>, also write its "checkWhy": first what your id means, in one sentence, then why none of the ids given fits; leave it empty for a check id given. Set "expected" true on a finding, with "expectedWhy" saying why and the evidence that shows it, only when what you found is the system working as intended, and the cluster shows it: a node annotated asleep by its power manager and cordoned just before it stopped reporting, a planned drain or maintenance. An expected finding opens no case, whatever its severity. Otherwise leave it false and give the severity as usual. Propose only kubectl or helm commands Watson runs, one per line, and only when the evidence supports them. Every proposal names in "fixes" what it fixes: the positions of findings in your findings list above (1 is the first), and/or ids of cases (C-12) or known issues (KI-3) from the keys in use; a proposal that fixes nothing you found is not filed. Never put a secret's value, a token or a password in the report or the block.`

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
		}
		if json.Unmarshal([]byte(body), &b) != nil {
			continue
		}
		out.Report = strings.TrimSpace(text[:m[i][0]] + text[m[i][1]:])
		out.Findings, out.Proposals, out.Raw = b.Findings, b.Proposals, json.RawMessage(body)
		return out
	}
	return out
}

// Changes turns what the agent proposed into proposals: kubectl and
// helm commands Watson runs, each an action; one with none left is
// dropped, with why in the notes.
func (p Parsed) Changes(check func(string) error, cluster string, evidence ...Evidence) ([]Proposal, []string) {
	var out []Proposal
	var notes []string
	for _, c := range p.Proposals {
		var actions []Action
		risk := strings.ToLower(strings.TrimSpace(c.Risk))
		if risk != "low" && risk != "medium" && risk != "high" {
			risk = "medium"
		}
		for _, line := range c.Commands {
			line = strings.TrimSpace(line)
			kind, _, _ := strings.Cut(line, " ")
			if kind != "kubectl" && kind != "helm" {
				notes = append(notes, "a proposed command that is not kubectl or helm was left out")
				continue
			}
			if check != nil {
				if err := check(line); err != nil {
					notes = append(notes, "a proposed command Watson would refuse was left out: "+err.Error())
					continue
				}
			}
			actions = append(actions, Action{Kind: kind, Args: map[string]any{"command": line}, Impact: c.Impact, Risk: risk})
		}
		if len(actions) == 0 || strings.TrimSpace(c.Title) == "" {
			continue
		}
		cl := strings.TrimSpace(c.Cluster)
		if cl == "" {
			cl = cluster
		}
		ev := append(append([]Evidence(nil), c.Evidence...), evidence...)
		for i := range ev {
			if ev[i].Kind == "" {
				ev[i].Kind = "fact"
			}
		}
		out = append(out, Proposal{Cluster: cl, Title: c.Title, Reasoning: c.Reasoning, Evidence: ev, Actions: actions, Fixes: c.Fixes})
	}
	return out, notes
}
