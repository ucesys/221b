package ai

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

// An agent's history (AI-32): every version once live, and the drafts
// saved since the last went live, each whole, to compare any two in the
// drawer as Helm › History compares a release's revisions.

// Kinds of a history entry.
const (
	HistoryLive      = "live"
	HistoryDraft     = "draft"
	HistoryDiscarded = "discarded"
)

// HistoryEntry is one version or draft of an agent's instructions.
type HistoryEntry struct {
	Kind string `json:"kind"`
	// N is the version, for one once live (0 is the chart's baseline with
	// no version); Rev the draft's revision, or the draft a version was.
	N   int       `json:"n,omitempty"`
	Rev int       `json:"rev,omitempty"`
	By  string    `json:"by,omitempty"`
	At  time.Time `json:"at,omitzero"`
	// Note says how it came to be: made live untested and why, a version
	// restored, a draft sent back.
	Note string `json:"note,omitempty"`
	// Request is the two-person request that approved a version, by its
	// number; 0 when none did (the thirteenth cluster test's N2).
	Request int `json:"request,omitempty"`
	// InForce marks the version in force; Current the draft now.
	InForce bool       `json:"inForce,omitempty"`
	Current bool       `json:"current,omitempty"`
	Test    *TestBrief `json:"test,omitempty"`
}

// HistoryText is an entry with its whole text: the house instructions,
// the notes by cluster, and every part of the built-in text, edited or as
// the release ships it.
type HistoryText struct {
	HistoryEntry
	House string            `json:"house"`
	Notes map[string]string `json:"notes,omitempty"`
	Parts []PartText        `json:"parts"`
}

// PartText is one part of the built-in text as an entry has it.
type PartText struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

// History is the agent's versions and drafts, newest first.
func (s *Service) History(ctx context.Context, id string) ([]HistoryEntry, error) {
	if _, ok := InfoOf(id); !ok {
		return nil, ErrNotFound
	}
	out := []HistoryEntry{}
	for _, t := range historyOf(s.read(ctx).prompts.Agents[id]) {
		out = append(out, t.entry)
	}
	return out, nil
}

// HistoryItem is one entry whole: a version once live by its number (0
// the baseline when no version is in force), or a draft by its revision,
// which a draft that went live finds as its version.
func (s *Service) HistoryItem(ctx context.Context, id, kind string, n int) (HistoryText, error) {
	if _, ok := InfoOf(id); !ok {
		return HistoryText{}, ErrNotFound
	}
	ap := s.read(ctx).prompts.Agents[id]
	list := historyOf(ap)
	var found *historyItem
	switch kind {
	case HistoryLive:
		if i := slices.IndexFunc(list, func(t historyItem) bool { return t.entry.Kind == HistoryLive && t.entry.N == n }); i >= 0 {
			found = &list[i]
		}
	case HistoryDraft, HistoryDiscarded:
		if i := slices.IndexFunc(list, func(t historyItem) bool { return t.entry.Kind != HistoryLive && t.entry.Rev == n }); i >= 0 {
			found = &list[i]
		} else if i := slices.IndexFunc(list, func(t historyItem) bool { return t.entry.Kind == HistoryLive && t.entry.Rev == n }); i >= 0 {
			found = &list[i]
		}
	default:
		return HistoryText{}, fmt.Errorf("%w: a kind is live or draft", ErrInvalid)
	}
	if found == nil {
		return HistoryText{}, ErrNotFound
	}
	out := HistoryText{HistoryEntry: found.entry, House: found.house, Notes: found.notes, Parts: []PartText{}}
	if found.baseline {
		out.House = s.Baseline(id)
	}
	builtIn := s.builtIn(id)
	for _, name := range PartsOf(id) {
		t, ok := found.parts[name]
		if !ok {
			t = builtIn(name)
		}
		out.Parts = append(out.Parts, PartText{Name: name, Label: partLabel(name), Text: t})
	}
	return out, nil
}

// historyItem is an entry with what it holds.
type historyItem struct {
	entry    HistoryEntry
	house    string
	notes    map[string]string
	parts    map[string]string
	baseline bool
}

// historyOf lists an agent's entries newest first. A draft saved before
// drafts were kept is listed from itself.
func historyOf(ap *AgentPrompt) []historyItem {
	var out []historyItem
	if ap == nil || ap.Live == 0 {
		it := historyItem{entry: HistoryEntry{Kind: HistoryLive, Note: "the chart's baseline", InForce: true}, baseline: true}
		if ap != nil {
			it.notes, it.parts = ap.Notes, ap.Parts
		}
		out = append(out, it)
	}
	if ap == nil {
		return out
	}
	for _, v := range ap.Versions {
		out = append(out, historyItem{entry: HistoryEntry{Kind: HistoryLive, N: v.N, Rev: v.Rev, By: v.By, At: v.At, Note: v.Why, Request: v.Request, InForce: v.N == ap.Live, Test: v.Test}, house: v.House, notes: v.Notes, parts: v.Parts})
	}
	current := false
	for _, r := range ap.Revisions {
		e := HistoryEntry{Kind: HistoryDraft, Rev: r.Rev, By: r.By, At: r.At, Note: r.Note, Test: r.Test}
		if r.Discarded {
			e.Kind = HistoryDiscarded
		}
		if d := ap.Draft; d != nil && d.Rev == r.Rev {
			e.Current, current = true, true
			if d.Test != nil && d.Test.Rev == d.Rev {
				e.Test = briefOf(d.Test)
			}
		}
		out = append(out, historyItem{entry: e, house: r.House, notes: r.Notes, parts: r.Parts})
	}
	if d := ap.Draft; d != nil && !current {
		e := HistoryEntry{Kind: HistoryDraft, Rev: d.Rev, By: d.By, At: d.At, Current: true}
		if d.Restored > 0 {
			e.Note = fmt.Sprintf("restored version %d", d.Restored)
		}
		if d.Test != nil && d.Test.Rev == d.Rev {
			e.Test = briefOf(d.Test)
		}
		out = append(out, historyItem{entry: e, house: d.House, notes: d.Notes, parts: d.Parts})
	}
	slices.SortStableFunc(out, func(a, b historyItem) int {
		if c := b.entry.At.Compare(a.entry.At); c != 0 {
			return c
		}
		return cmp.Compare(max(b.entry.N, b.entry.Rev), max(a.entry.N, a.entry.Rev))
	})
	return out
}
