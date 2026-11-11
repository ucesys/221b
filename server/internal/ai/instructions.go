package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/watson"
)

// An agent's instructions come in three layers: the fixed core, in code;
// the house instructions, whose baseline the chart gives
// (agents.<id>.instructions) and which a SuperAdmin may edit in the
// console; and the notes per cluster, kept in the console only. An edit
// goes Draft → Test → Pilot → Live; live needs a second SuperAdmin where
// there is one, and each live version is kept, to restore in one click.

// InstructionsView is an agent's instructions for its drawer.
type InstructionsView struct {
	Agent string `json:"agent"`
	// Core is the fixed core, as the default cluster gets it.
	Core string `json:"core"`
	// Baseline is the chart's house instructions; House the ones in force.
	// PreviousBaseline, when the baseline changed under an edit, is the one
	// the edit was made over.
	Baseline         string `json:"baseline"`
	PreviousBaseline string `json:"previousBaseline,omitempty"`

	House     string            `json:"house"`
	Notes     map[string]string `json:"notes,omitempty"`
	State     string            `json:"state"`
	ValuesKey string            `json:"valuesKey"`
	Live      int               `json:"live"`
	Versions  []Version         `json:"versions"`
	Draft     *Draft            `json:"draft,omitempty"`
	// Warnings are the lines of the draft, or of what is in force, that
	// look like facts.
	Warnings      []string `json:"warnings,omitempty"`
	MaxWords      int      `json:"maxWords"`
	BaselineCheck *TestRun `json:"baselineCheck,omitempty"`
	// --- AI settings, round 2 (Phase 12, wave 5) ---
	// Parts are the agent's built-in text by part, pre-filled and
	// editable (AS-01).
	Parts        []PartView `json:"parts"`
	MaxPartWords int        `json:"maxPartWords"`
}

// PartView is one editable part of an agent's built-in text.
type PartView struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// BuiltIn is the part's built-in text, as the release ships it.
	BuiltIn string `json:"builtIn"`
	// Text is the part in force: its edit, or the built-in text.
	Text   string `json:"text"`
	Edited bool   `json:"edited"`
	// BuiltInChanged says the built-in text changed since the edit was
	// made: an upgrade, or for Sherlock's core view changes turned on or
	// off.
	BuiltInChanged bool `json:"builtInChanged,omitempty"`
	// Draft is the draft's text of the part, when there is a draft.
	Draft *string `json:"draft,omitempty"`
}

// partLabel names a part for people.
func partLabel(name string) string {
	if name == PartCompact {
		return "Compact form, for a model with a small context"
	}
	return "Built-in instructions"
}

// partsView are an agent's editable parts with the edits in force and
// the draft's.
func (s *Service) partsView(id string, ap *AgentPrompt) []PartView {
	view := id == Sherlock && s.sherlockSettings().ChangesView()
	var out []PartView
	for _, name := range PartsOf(id) {
		b := BuiltInPart(id, name, view)
		v := PartView{Name: name, Label: partLabel(name), BuiltIn: b, Text: b}
		if ap != nil {
			if t, ok := ap.Parts[name]; ok {
				v.Text, v.Edited, v.BuiltInChanged = t, true, ap.PartBase[name] != hash(b)
			}
			if ap.Draft != nil {
				t, ok := ap.Draft.Parts[name]
				if !ok {
					t = b
				}
				v.Draft = &t
			}
		}
		out = append(out, v)
	}
	return out
}

// Instructions is an agent's instructions as its drawer shows them.
func (s *Service) Instructions(ctx context.Context, id string) (InstructionsView, error) {
	info, ok := InfoOf(id)
	if !ok {
		return InstructionsView{}, ErrNotFound
	}
	snap := s.read(ctx)
	ap := snap.prompts.Agents[id]
	baseline := s.Baseline(id)
	v := InstructionsView{Agent: id, Core: Core(id, id == Sherlock && s.sherlockSettings().ChangesView()), Baseline: baseline, House: ap.LiveHouse(baseline), State: ap.State(baseline), ValuesKey: ValuesKey(info.ID), MaxWords: MaxWords, Versions: []Version{}, Parts: s.partsView(id, ap), MaxPartWords: MaxPartWords}
	if ap != nil {
		v.Notes, v.Live, v.Draft, v.BaselineCheck = ap.Notes, ap.Live, ap.Draft, ap.BaselineCheck
		if v.State == BaselineChanged {
			v.PreviousBaseline = ap.Base
		}
		for i := len(ap.Versions) - 1; i >= 0; i-- {
			v.Versions = append(v.Versions, ap.Versions[i])
		}
	}
	text := v.House
	notes := v.Notes
	if v.Draft != nil {
		text, notes = v.Draft.House, v.Draft.Notes
	}
	v.Warnings = FactWarnings(text)
	for _, k := range sortedKeys(notes) {
		v.Warnings = append(v.Warnings, FactWarnings(notes[k])...)
	}
	return v, nil
}

// draftParts are the edits a draft makes: those given that differ from
// their built-in text.
func draftParts(parts map[string]string, builtIn func(string) string) map[string]string {
	var out map[string]string
	for k, v := range parts {
		if t := strings.TrimSpace(v); t != "" && t != strings.TrimSpace(builtIn(k)) {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = t
		}
	}
	return out
}

// settleParts drops the edits equal to their built-in text now: an
// upgrade that caught up with them.
func (p *AgentPrompt) settleParts(builtIn func(string) string) {
	for k, v := range p.Parts {
		if strings.TrimSpace(v) == strings.TrimSpace(builtIn(k)) {
			delete(p.Parts, k)
			delete(p.PartBase, k)
		}
	}
	if len(p.Parts) == 0 {
		p.Parts, p.PartBase = nil, nil
	}
}

func sortedKeys(m map[string]string) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	return keys
}

// editPrompt changes one agent's instructions.
func (s *Service) editPrompt(ctx context.Context, id string, edit func(p *AgentPrompt, baseline string) error) (*AgentPrompt, error) {
	if _, ok := InfoOf(id); !ok {
		return nil, ErrNotFound
	}
	defer s.forget()
	baseline := s.Baseline(id)
	ps, err := change(ctx, s.opts.Backend, PromptsKey, func(ps *Prompts) error {
		if ps.Agents == nil {
			ps.Agents = map[string]*AgentPrompt{}
		}
		p := ps.Agents[id]
		if p == nil {
			p = &AgentPrompt{}
			ps.Agents[id] = p
		}
		if err := edit(p, baseline); err != nil {
			return err
		}
		p.Settle(baseline)
		p.settleParts(s.builtIn(id))
		prune(ps)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ps.Agents[id], nil
}

// SaveDraft saves edited instructions as the agent's draft, with the
// edits of its built-in text in force; a new save starts its way to live
// again. It returns the warnings about facts.
func (s *Service) SaveDraft(ctx context.Context, id, house string, notes map[string]string, by string) ([]string, error) {
	return s.SaveDraftParts(ctx, id, house, notes, nil, false, by)
}

// builtIn gives an agent's parts' built-in text, as view changes stand.
func (s *Service) builtIn(id string) func(string) string {
	view := id == Sherlock && s.sherlockSettings().ChangesView()
	return func(part string) string { return BuiltInPart(id, part, view) }
}

// SaveDraftParts saves a draft with edits of the agent's built-in text
// too, when given (AS-01): each part given replaces its built-in text
// whole; one empty or equal to the built-in text is the built-in one.
func (s *Service) SaveDraftParts(ctx context.Context, id, house string, notes, parts map[string]string, partsGiven bool, by string) ([]string, error) {
	if partsGiven {
		for k, v := range parts {
			if !slices.Contains(PartsOf(id), k) {
				return nil, fmt.Errorf("%w: the %s has no part %q to edit", ErrInvalid, id, k)
			}
			if n := Words(v); n > MaxPartWords {
				return nil, fmt.Errorf("%w: the %s has %d words; keep it to %d", ErrInvalid, strings.ToLower(partLabel(k)), n, MaxPartWords)
			}
		}
	}
	house = strings.TrimSpace(house)
	if err := CheckText("the house instructions", house); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(notes) > 1000 {
		return nil, fmt.Errorf("%w: too many clusters' notes", ErrInvalid)
	}
	for k, n := range notes {
		if err := CheckText("the notes for "+k, n); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	builtIn := s.builtIn(id)
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		p.Revs++
		p.Draft = &Draft{Rev: p.Revs, House: house, Notes: cleanNotes(notes), Stage: StageDraft, By: by, At: s.opts.Now().UTC(), Parts: cloneParts(p.Parts)}
		if partsGiven {
			p.Draft.Parts = draftParts(parts, builtIn)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	warn := FactWarnings(house)
	for _, k := range sortedKeys(notes) {
		warn = append(warn, FactWarnings(notes[k])...)
	}
	return warn, nil
}

// DiscardDraft drops the agent's draft, and with it any pilot.
func (s *Service) DiscardDraft(ctx context.Context, id string) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		if p.Draft == nil {
			return ErrNotFound
		}
		p.Draft = nil
		return nil
	})
	return err
}

// Baseline actions, when an upgrade brought a new baseline while an edit
// is in force.
const (
	// KeepEdit keeps the edit, now over the new baseline.
	KeepEdit = "keep"
	// TakeBaseline drops the edit for the chart's baseline.
	TakeBaseline = "take"
)

// ResolveBaseline keeps the edit in force or takes the chart's baseline.
// Nothing replaces either without this choice. Merging is an edit, made
// as a draft.
func (s *Service) ResolveBaseline(ctx context.Context, id, action, by string) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, baseline string) error {
		if p.House == nil {
			return fmt.Errorf("%w: the baseline is in force already", ErrInvalid)
		}
		switch action {
		case KeepEdit:
			p.BaseHash, p.Base = hash(baseline), baseline
		case TakeBaseline:
			p.goLive(baseline, p.Notes, baseline, by, "took the chart's baseline", s.opts.Now())
		default:
			return fmt.Errorf("%w: keep or take", ErrInvalid)
		}
		return nil
	})
	return err
}

// StartPilot serves the tested draft to chosen people or clusters for a
// while: both versions' answers are logged meanwhile, and may be rated.
func (s *Service) StartPilot(ctx context.Context, id string, people, clusters []string, d time.Duration, by string) error {
	if d <= 0 || d > MaxPilot {
		return fmt.Errorf("%w: a pilot lasts between an hour and %d days", ErrInvalid, int(MaxPilot.Hours()/24))
	}
	if len(people) == 0 && len(clusters) == 0 {
		return fmt.Errorf("%w: name the people or the clusters the draft serves", ErrInvalid)
	}
	if len(people) > 50 || len(clusters) > 50 {
		return fmt.Errorf("%w: at most 50 people and 50 clusters", ErrInvalid)
	}
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		if p.Draft == nil {
			return fmt.Errorf("%w: there is no draft", ErrInvalid)
		}
		if p.Draft.Test == nil || p.Draft.Test.Rev != p.Draft.Rev {
			return fmt.Errorf("%w: test the draft first", ErrInvalid)
		}
		now := s.opts.Now().UTC()
		p.Draft.Stage = StagePilot
		p.Draft.Pilot = &Pilot{People: people, Clusters: clusters, Started: now, Until: now.Add(d), By: by}
		return nil
	})
	return err
}

// StopPilot ends a pilot; the draft stays, tested.
func (s *Service) StopPilot(ctx context.Context, id string) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		if p.Draft == nil || p.Draft.Pilot == nil {
			return ErrNotFound
		}
		p.Draft.Stage, p.Draft.Pilot.Until = StageTest, s.opts.Now().UTC()
		return nil
	})
	return err
}

// BackToDraft returns the draft to its first stage (Phase 12, wave 5,
// AS-03): a pilot ends, its test no longer counts, and it takes a new
// revision, so an approval asked for it no longer applies.
func (s *Service) BackToDraft(ctx context.Context, id string) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		if p.Draft == nil {
			return ErrNotFound
		}
		p.Revs++
		d := p.Draft
		d.Rev, d.Stage, d.Test, d.Request = p.Revs, StageDraft, nil, 0
		if d.Pilot != nil && s.opts.Now().Before(d.Pilot.Until) {
			d.Pilot.Until = s.opts.Now().UTC()
		}
		d.Pilot = nil
		return nil
	})
	return err
}

// ReadyForLive says why the draft cannot go live, or returns its revision:
// it must have been tested, unless it restores a version once live.
func (s *Service) ReadyForLive(ctx context.Context, id string) (int, error) {
	if _, ok := InfoOf(id); !ok {
		return 0, ErrNotFound
	}
	ap := s.read(ctx).prompts.Agents[id]
	if ap == nil || ap.Draft == nil {
		return 0, fmt.Errorf("%w: there is no draft", ErrInvalid)
	}
	d := ap.Draft
	if (d.Test == nil || d.Test.Rev != d.Rev) && d.Restored == 0 && d.Stage != StagePilot {
		return 0, fmt.Errorf("%w: test the draft first", ErrInvalid)
	}
	return d.Rev, nil
}

// SetRequest notes the approval the draft waits for.
func (s *Service) SetRequest(ctx context.Context, id string, rev, request int) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		if p.Draft != nil && p.Draft.Rev == rev {
			p.Draft.Request = request
		}
		return nil
	})
	return err
}

// ApplyLive puts the draft of a revision live: approved by a second
// SuperAdmin, or by the only one. A draft saved again since is refused.
func (s *Service) ApplyLive(ctx context.Context, id string, rev int, by string) (Version, error) {
	var v Version
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, baseline string) error {
		if p.Draft == nil || p.Draft.Rev != rev {
			return fmt.Errorf("%w: the draft changed since it was asked for; ask again", ErrInvalid)
		}
		why := "made live"
		if p.Draft.Restored > 0 {
			why = fmt.Sprintf("restored version %d", p.Draft.Restored)
		}
		p.setParts(p.Draft.Parts, s.builtIn(id))
		v = p.goLive(p.Draft.House, p.Draft.Notes, baseline, by, why, s.opts.Now())
		p.Draft = nil
		return nil
	})
	return v, err
}

// Restore makes a draft of a version once live, which may go live again
// without a new test.
func (s *Service) Restore(ctx context.Context, id string, n int, by string) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, _ string) error {
		i := slices.IndexFunc(p.Versions, func(v Version) bool { return v.N == n })
		if i < 0 {
			return ErrNotFound
		}
		v := p.Versions[i]
		p.Revs++
		p.Draft = &Draft{Rev: p.Revs, House: v.House, Notes: v.Notes, Stage: StageDraft, By: by, At: s.opts.Now().UTC(), Restored: n, Parts: cloneParts(v.Parts)}
		return nil
	})
	return err
}

// SetLive writes instructions in force at once, for Sherlock's settings
// endpoint from before Phase 12, which saved its instructions and notes
// without a draft. The version is kept.
func (s *Service) SetLive(ctx context.Context, id, house string, notes map[string]string, by string) error {
	_, err := s.editPrompt(ctx, id, func(p *AgentPrompt, baseline string) error {
		if strings.TrimSpace(house) == "" {
			house = baseline
		}
		p.goLive(house, notes, baseline, by, "saved in Sherlock's settings", s.opts.Now())
		return nil
	})
	return err
}

// Settle drops every edit equal to its baseline the chart now deploys.
func (s *Service) Settle(ctx context.Context) error {
	snap := s.read(ctx)
	settle := false
	for id, p := range snap.prompts.Agents {
		if p != nil && p.House != nil && strings.TrimSpace(*p.House) == s.Baseline(id) {
			settle = true
		}
	}
	if !settle {
		return nil
	}
	defer s.forget()
	_, err := change(ctx, s.opts.Backend, PromptsKey, func(ps *Prompts) error {
		for id, p := range ps.Agents {
			p.Settle(s.Baseline(id))
		}
		return nil
	})
	return err
}

// Migrate carries Sherlock's instructions and notes from before Phase 12
// into its house instructions and notes for every cluster, once. Before,
// instructions replaced the built-in ones; now they add to the core, so
// the built-in text itself is not carried.
func (s *Service) Migrate(ctx context.Context) error {
	snap := s.read(ctx)
	if snap.agents.Migrated {
		return nil
	}
	defer s.forget()
	sh := s.sherlockSettings()
	instructions, notes := strings.TrimSpace(sh.Instructions), strings.TrimSpace(sh.Notes)
	if instructions == strings.TrimSpace(sherlock.BuiltIn(true, watson.Commands())) || instructions == strings.TrimSpace(sherlock.BuiltIn(false, watson.Commands())) {
		instructions = ""
	}
	if instructions != "" || notes != "" {
		if _, err := s.editPrompt(ctx, Sherlock, func(p *AgentPrompt, baseline string) error {
			if p.House != nil || len(p.Notes) > 0 {
				return nil
			}
			house := baseline
			if instructions != "" {
				house = instructions
			}
			n := map[string]string{}
			if notes != "" {
				n[AllClusters] = notes
			}
			p.goLive(house, n, baseline, "upgrade", "carried over from Sherlock's settings", s.opts.Now())
			return nil
		}); err != nil {
			return err
		}
	}
	_, err := change(ctx, s.opts.Backend, AgentsKey, func(a *Agents) error {
		a.Migrated = true
		return nil
	})
	return err
}

// Legacy is Sherlock's instructions and notes for every cluster as its
// settings endpoint from before Phase 12 shows them.
func (s *Service) Legacy(ctx context.Context) (instructions, notes string) {
	ap := s.read(ctx).prompts.Agents[Sherlock]
	if ap == nil {
		return "", ""
	}
	if ap.House != nil {
		instructions = *ap.House
	}
	return instructions, ap.Notes[AllClusters]
}

// promptsBudget is what the instructions may take of their key, below its
// limit in the ConfigMap's layout.
const promptsBudget = 440 << 10

// prune keeps the instructions within their budget: the oldest versions
// not in force go first, then the details of old test runs.
func prune(ps *Prompts) {
	size := func() int {
		b, _ := json.Marshal(ps)
		return len(b)
	}
	for size() > promptsBudget {
		var most *AgentPrompt
		for _, p := range ps.Agents {
			if p != nil && len(p.Versions) > 1 && (most == nil || len(p.Versions) > len(most.Versions)) {
				most = p
			}
		}
		if most != nil {
			i := slices.IndexFunc(most.Versions, func(v Version) bool { return v.N != most.Live })
			most.Versions = slices.Delete(most.Versions, i, i+1)
			continue
		}
		dropped := false
		for _, p := range ps.Agents {
			if p != nil && p.BaselineCheck != nil && (p.BaselineCheck.Results != nil || p.BaselineCheck.Replays != nil) {
				p.BaselineCheck.Results, p.BaselineCheck.Replays, dropped = nil, nil, true
			}
			if p != nil && p.Draft != nil && p.Draft.Test != nil && p.Draft.Test.Replays != nil {
				p.Draft.Test.Replays, dropped = nil, true
			}
		}
		if !dropped {
			return
		}
	}
}
