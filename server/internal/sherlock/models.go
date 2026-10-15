package sherlock

import "math"

// Model is a Claude model an administrator may pick, with its list prices
// in US dollars per million tokens.
type Model struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	In    float64 `json:"in"`
	Out   float64 `json:"out"`
	// LongIn and LongOut, when set, apply to a whole request whose prompt
	// is over LongAbove tokens.
	LongIn    float64 `json:"longIn,omitempty"`
	LongOut   float64 `json:"longOut,omitempty"`
	LongAbove int     `json:"longAbove,omitempty"`
	Note      string  `json:"note,omitempty"`
}

// Models are the models offered in the settings, with the prices
// published for them (platform.claude.com/docs/en/about-claude/pricing,
// October 2026). An administrator may name another model; its spend is
// counted at the highest price here, so a cap is never passed unnoticed.
var Models = []Model{
	{ID: "claude-opus-5-5", Label: "Claude Opus 5.5", In: 4, Out: 20, Note: "Recommended: the most thorough at finding a cause."},
	{ID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5", In: 2, Out: 10, Note: "Fast, at half the price."},
	{ID: "claude-haiku-5-5", Label: "Claude Haiku 5.5", In: 0.10, Out: 0.50, LongIn: 0.50, LongOut: 2.50, LongAbove: 100_000, Note: "The cheapest; best for simple questions."},
	{ID: "claude-fable-5-1", Label: "Claude Fable 5.1", In: 10, Out: 50, Note: "The most capable, at the highest price."},
}

// DefaultModel is what a new installation uses.
const DefaultModel = "claude-opus-5-5"

// ModelOf finds a model by id; an unknown one is priced as the dearest.
func ModelOf(id string) (Model, bool) {
	for _, m := range Models {
		if m.ID == id {
			return m, true
		}
	}
	dear := Models[0]
	for _, m := range Models {
		if m.Out > dear.Out {
			dear = m
		}
	}
	dear.ID, dear.Label, dear.Note = id, id, "Not one of the listed models: its spend is counted at the highest listed price."
	return dear, false
}

// Cost is what a request cost in US dollars. Writing to the prompt cache
// costs a quarter more than plain input and reading from it a tenth.
func (m Model) Cost(u Usage) float64 {
	in, out := m.In, m.Out
	if m.LongAbove > 0 && u.Prompt() > m.LongAbove {
		in, out = m.LongIn, m.LongOut
	}
	c := (float64(u.InputTokens)*in + float64(u.CacheCreationInputTokens)*in*1.25 + float64(u.CacheReadInputTokens)*in*0.1 + float64(u.OutputTokens)*out) / 1e6
	return math.Round(c*1e6) / 1e6
}
