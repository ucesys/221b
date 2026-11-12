package sherlock

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// What an agent's request carries before the first word of a question
// (Phase 12, AS-07): its instructions and the definitions of the tools it
// is offered. A model with a small context (an in-house model served
// with 16k or 32k tokens) cannot take Sherlock's whole form, so package
// ai measures it against the model's context window and resolves the
// compact form when it does not fit.

// Footprint is an agent's instructions and tools, in estimated tokens.
type Footprint struct {
	Instructions int `json:"instructions"`
	Tools        int `json:"tools"`
	// ToolCount is how many tools are offered.
	ToolCount int `json:"toolCount"`
}

// Total is the instructions and the tools together.
func (f Footprint) Total() int { return f.Instructions + f.Tools }

// EstimateTokens estimates the tokens a text of instructions takes.
// Without the model's own tokenizer it counts one token per 4.2
// characters, which Qwen 3's and OpenAI's tokenizers come within about
// 5% of on the console's English prompts, a little over rather than
// under (measured November 2026: Sherlock's core, 14,482 characters, is
// 3,299 Qwen 3 tokens).
func EstimateTokens(s string) int {
	return (utf8.RuneCountInString(s)*10 + 41) / 42
}

// estimateDefs estimates the tokens of tool definitions as JSON: one per
// 3.6 characters (the toolbox's 38 definitions, 24,516 characters, are
// 6,752 Qwen 3 tokens in the Chat Completions shape).
func estimateDefs(b []byte) int {
	return (len(b)*10 + 35) / 36
}

// OfferedDefs are the tool definitions an agent is offered with a
// toolbox of these definitions: its tools of the toolbox, its own (a
// hand-over, the knowledge base, …), and unless it is compact the view
// actions: all of them for Sherlock with view changes on, else
// view_state.
func OfferedDefs(a Agent, toolbox []ToolDef, viewChanges bool) []ToolDef {
	defs := defsFor(&a, defsOnly(toolbox), 0)
	if a.Compact {
		return defs
	}
	return append(defs, uiDefs(viewChanges && a.ID == SherlockID)...)
}

// Measure is an agent's footprint with a toolbox of these definitions.
func Measure(a Agent, toolbox []ToolDef, viewChanges bool) Footprint {
	defs := OfferedDefs(a, toolbox, viewChanges)
	b, _ := json.Marshal(defs)
	f := Footprint{Instructions: EstimateTokens(a.Instructions), ToolCount: len(defs)}
	if len(defs) > 0 {
		f.Tools = estimateDefs(b)
	}
	if a.Instructions != "" && !strings.Contains(a.Instructions, clusterTextRule) {
		f.Instructions += EstimateTokens(clusterTextRule)
	}
	return f
}

// defsOnly is a toolbox that only lists its definitions, for measuring.
type defsOnly []ToolDef

func (d defsOnly) Defs() []ToolDef { return d }

func (defsOnly) Label(name string, _ json.RawMessage) string { return name }

func (defsOnly) Run(context.Context, string, json.RawMessage) ToolResult { return ToolResult{} }
