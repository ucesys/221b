package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/scrub"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Effort and Auto (phase-13.1-ai, part A, AI-43 to AI-48): what package
// auto decides, put to work on the agents. A conversation's message is
// picked by the classifier (chooser, for the engine); a job's run by the
// rules alone (PickRun, for the jobs' runner).

// ClassifierTimeout is how long a conversation's message waits for the
// classifier before the agent's fixed defaults apply (the owner: 1.5
// seconds). The A5000 answered in 0.20 s (median) on November 22.
const ClassifierTimeout = 1500 * time.Millisecond

// autoSettings is what Auto may pick for an agent on its provider: the
// model among the provider's offered models by tier (A9); on a provider
// none of whose offered models has a tier, the model stays the one
// resolved.
func autoSettings(set AgentSettings, p Provider, model string) auto.Settings {
	s := set.auto()
	s.Ladder = ladderOf(p)
	if s.Model != auto.Auto || s.Ladder.Empty() {
		s.Model = model
	}
	return s
}

// anthropicKind says a provider speaks the Claude Messages API as
// Anthropic does: with no offered models chosen, Auto picks among Claude
// Haiku, Sonnet and Opus 5.5 on it.
func anthropicKind(p Provider) bool {
	return p.Provider.Kind == "" || p.Provider.Kind == sherlock.KindAnthropic
}

// modelOfAuto is the model an agent starts on before Auto has picked: for
// a model left to Auto, the standard tier's on its provider; on a provider
// with no tiered model, the provider's default.
func modelOfAuto(set AgentSettings, p Provider) string {
	if set.Model != auto.Auto {
		return modelOf(set, p)
	}
	if l := ladderOf(p); !l.Empty() {
		return l.Of(auto.DefaultTier)
	}
	return modelOf(AgentSettings{}, p)
}

// chooser is an agent's Auto for the engine (sherlock.Chooser).
type chooser struct {
	s        *Service
	settings auto.Settings
	provider Provider
}

// Choose picks for a conversation's message, or a task handed over.
func (c *chooser) Choose(ctx context.Context, in sherlock.ChooseInput) sherlock.Choice {
	var (
		p     auto.Pick
		moved bool
		cost  float64
		on    string
	)
	switch {
	case in.Single:
		p = auto.Single(c.settings)
	case in.Inherit != "":
		p = auto.Inherited(c.settings, in.Inherit)
	default:
		t := auto.Turn{Model: in.Model, MovedUp: in.MovedUp}
		t.Level, t.Failed, cost, on = c.s.classify(ctx, in.Text, in.Model == "")
		p, moved = auto.Conversation(c.settings, t, auto.Limits{DayShare: in.DayShare})
	}
	ch := sherlock.Choice{Model: p.Model, Effort: p.Effort, By: p.By, Why: p.Why, CostUSD: cost, Provider: on}
	if moved {
		ch.MovedUpFrom = in.Model
	}
	if ch.Model != "" {
		ch.Price = priceOf(ch.Model, c.provider)
	}
	return ch
}

// classify asks the classifier for a message's level: low, medium or
// high, or "" with why there is none (off, gone, too slow, an answer it
// may not give). It says what the call cost and on which provider.
func (s *Service) classify(ctx context.Context, text string, first bool) (level, failed string, cost float64, provider string) {
	snap := s.read(ctx)
	p, model, why := s.classifier(snap)
	if why != "" {
		return "", why, 0, ""
	}
	cctx, cancel := context.WithTimeout(ctx, ClassifierTimeout)
	defer cancel()
	text = scrub.Known(scrub.Text(text), s.allSecrets(snap))
	answer, u, err := s.Client(p.ID).ChooseOne(cctx, model, auto.ClassifierPrompt(first), text, auto.Choices)
	cost = priceOf(model, p).Cost(u)
	switch {
	case err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded):
		return "", fmt.Sprintf("the classifier did not answer in %.1f s", ClassifierTimeout.Seconds()), cost, p.ID
	case err != nil:
		s.opts.Logger.Warn("the classifier failed", "provider", p.ID, "err", err)
		return "", "the classifier failed", cost, p.ID
	}
	level = auto.Said(answer)
	if level == "" {
		return "", "the classifier answered none of low, medium or high", cost, p.ID
	}
	return level, "", cost, p.ID
}

// classifier is the provider and model the classifier asks, or why there
// is none: none chosen, gone, not OpenAI-compatible, without a model.
func (s *Service) classifier(snap snapshot) (Provider, string, string) {
	id := snap.providers.ClassifierID
	if id == "" {
		return Provider{}, "", "no classifier"
	}
	p, ok := s.provider(snap, id)
	switch {
	case !ok:
		return Provider{}, "", "the classifier's provider is gone"
	case p.Provider.Kind != sherlock.KindOpenAI:
		return Provider{}, "", "the classifier's provider is not OpenAI-compatible"
	case p.Model == "":
		return Provider{}, "", "the classifier's provider has no default model"
	case s.usable(p) != "":
		return Provider{}, "", "the classifier's provider cannot be used: " + s.usable(p)
	}
	return p, p.Model, ""
}

// SetClassifier chooses the provider the classifier asks, on its default
// model: an OpenAI-compatible provider of the list; "" turns it off.
func (s *Service) SetClassifier(ctx context.Context, id string) error {
	defer s.forget()
	if id != "" {
		snap := s.read(ctx)
		p, ok := s.provider(snap, id)
		switch {
		case !ok || id == DefaultProvider:
			return fmt.Errorf("%w: there is no provider %q to classify with", ErrInvalid, id)
		case p.Provider.Kind != sherlock.KindOpenAI:
			return fmt.Errorf("%w: the classifier runs on an OpenAI-compatible provider (vLLM's constrained choice); %s is %s", ErrInvalid, p.Name, KindLabel(p.Provider.Kind))
		case p.Model == "":
			return fmt.Errorf("%w: give %s a default model first: the classifier asks it", ErrInvalid, p.Name)
		}
	}
	_, err := change(ctx, s.opts.Backend, ProvidersKey, func(ps *Providers) error {
		ps.ClassifierID = id
		return nil
	})
	return err
}

// PickRun is the rules' pick for one step of a job's run (AI-45), put in
// force on the agent the step runs: its model and effort, and how and why
// they were picked. runModel is the model the run started on with Auto, ""
// for its first step; movedUp says it was moved up already. movedFrom is
// the model this step moved the run up from, "" when it did not. An agent
// with nothing left to Auto keeps what is set, picked by nobody.
func (s *Service) PickRun(ctx context.Context, a *sherlock.Agent, in auto.RunInput, runModel string, movedUp bool) (p auto.Pick, movedFrom string) {
	snap := s.read(ctx)
	set := s.settingsOf(snap, a.ID)
	if !set.usesAuto() {
		return auto.Pick{Model: a.Model, Effort: a.Effort}, ""
	}
	prov, ok := s.provider(snap, set.Provider)
	if !ok {
		a.Auto = nil
		return auto.Pick{Model: a.Model, Effort: a.Effort}, ""
	}
	as := autoSettings(set, prov, a.Model)
	p = auto.Rules(as, in)
	if as.AutoModel() && runModel != "" {
		if auto.MoveUp(as.Ladder, runModel, p.Model, movedUp, in.DayShare) {
			movedFrom = runModel
			p.Why += "; moved up from " + auto.Label(runModel)
		} else {
			p.Model = runModel
		}
	}
	a.Model, a.Price, a.Effort, a.Auto, a.PickBy, a.PickWhy = p.Model, priceOf(p.Model, prov), p.Effort, nil, p.By, p.Why
	return p, movedFrom
}
