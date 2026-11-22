// Package auto picks an agent's effort and model when an administrator
// leaves them to the console (phase-13.1-ai, part A, AI-43 to AI-48).
//
// The owner's decisions (November 22, 2026): Auto picks among Claude
// Haiku 5.5, Sonnet 5.5 and Opus 5.5 on Anthropic; a job's run is picked
// by rules alone, from what started it; a conversation (Sherlock, an agent
// a person talks to) by a classifier that reads the message and answers
// low, medium or high. The effort is picked for every message, the model
// when the conversation or the run starts, moved up once at most and never
// down. Every pick says why.
//
// The package is pure: no request leaves it. The classifier's request is
// sherlock.Client.ChooseOne, and package ai puts the two together.
package auto

import (
	"fmt"
	"slices"
	"strings"
)

// The effort levels, lowest first, as the Messages API names them.
const (
	Low    = "low"
	Medium = "medium"
	High   = "high"
	XHigh  = "xhigh"
	Max    = "max"
)

// Levels are the effort levels, lowest first.
var Levels = []string{Low, Medium, High, XHigh, Max}

// Auto is the value of an agent's effort or model left to the console.
const Auto = "auto"

// Rank is an effort's place among Levels, from 0; -1 for none.
func Rank(e string) int { return slices.Index(Levels, e) }

// ValidEffort says whether e is an effort an agent may be set to: ""
// (the model's own default), a level, or Auto.
func ValidEffort(e string) bool { return e == "" || e == Auto || Rank(e) >= 0 }

// The models Auto picks among, cheapest first (the owner, November 22).
const (
	Haiku  = "claude-haiku-5-5"
	Sonnet = "claude-sonnet-5-5"
	Opus   = "claude-opus-5-5"
)

// Models are the models Auto picks among, cheapest first.
var Models = []string{Haiku, Sonnet, Opus}

// Tier is a model's place among Models, from 0; -1 for one Auto does not
// pick.
func Tier(model string) int { return slices.Index(Models, model) }

// Label names a model for people: "Claude Opus 5.5".
func Label(model string) string {
	switch model {
	case Haiku:
		return "Claude Haiku 5.5"
	case Sonnet:
		return "Claude Sonnet 5.5"
	case Opus:
		return "Claude Opus 5.5"
	}
	return model
}

// How a pick was made.
const (
	// ByRules: a job's run, from what started it.
	ByRules = "rules"
	// ByClassifier: a conversation, from the classifier's word.
	ByClassifier = "classifier"
	// ByFixed: the agent's fixed defaults, as when the classifier is off or
	// did not answer in time.
	ByFixed = "fixed"
)

// Settings are what an agent leaves to Auto and what it fixes.
type Settings struct {
	// Effort is the agent's effort: "" (the model's default), a level, or
	// Auto. Model likewise: a model, or Auto.
	Effort string
	Model  string
	// FloorEffort and FloorModel are the lowest effort and model Auto may
	// pick; "" is none (AI-47). Item 5's measurement fills them.
	FloorEffort string
	FloorModel  string
}

// AutoEffort and AutoModel say what is left to Auto.
func (s Settings) AutoEffort() bool { return s.Effort == Auto }
func (s Settings) AutoModel() bool  { return s.Model == Auto }

// Pick is what Auto chose, and why.
type Pick struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	By     string `json:"by,omitempty"`
	Why    string `json:"why,omitempty"`
}

// BudgetShare is the share of the day's budget spent above which Auto
// steps the effort down one level and moves no model up (AI-46).
const BudgetShare = 0.8

// Limits are what Auto never crosses (AI-46).
type Limits struct {
	// Urgent says an open P0 or P1 case is what the work is about: only
	// then may Auto pick xhigh or max.
	Urgent bool
	// DayShare is the share of the day's budget spent: the run's job's
	// for a run, the most spent of the agent's own, its provider's and the
	// total for a conversation. 0 when none is set.
	DayShare float64
}

// RunInput is what started a job's run and what one step of it is about,
// for the rules.
type RunInput struct {
	// Trigger is the run's: schedule, event or manual (jobs.Trigger*).
	Trigger string `json:"trigger"`
	// Event is the event's kind: alert, report, source or case.
	Event string `json:"event,omitempty"`
	// CasePriority is the case the run is about, 0 for P0 to 4 for P4;
	// -1 for none.
	CasePriority int `json:"casePriority"`
	// Production says the step's cluster is marked production.
	Production bool `json:"production,omitempty"`
	// Kind is the job's kind (nightly-audit, alert-diagnosis, …).
	Kind string `json:"kind,omitempty"`
	// DayShare is the share of the job's day budget spent.
	DayShare float64 `json:"dayShare,omitempty"`
}

// The job kinds the rules know by name (package jobs' ids).
const (
	kindDigest = "daily-digest"
	kindAudit  = "nightly-audit"
)

// Rules is the pick for one step of a job's run, from what started it and
// what the step is about (AI-45), with the limits and the floor applied.
// A setting that is fixed stays as it is set.
func Rules(s Settings, in RunInput) Pick {
	effort, model, why := rule(in)
	p := Pick{Effort: effort, Model: model, By: ByRules, Why: why}
	return finish(s, p, Limits{Urgent: in.CasePriority == 0 || in.CasePriority == 1, DayShare: in.DayShare})
}

// rule is the table of the rules: the effort, the model and why.
func rule(in RunInput) (string, string, string) {
	switch {
	case in.CasePriority == 0:
		return XHigh, Opus, "a P0 case"
	case in.CasePriority == 1:
		return High, Opus, "a P1 case"
	case in.Event == "alert" && in.Production:
		return High, Sonnet, "an alert on a production cluster"
	case in.Event == "alert":
		return Medium, Sonnet, "an alert"
	case in.Event == "case" && in.CasePriority > 1:
		return Medium, Sonnet, fmt.Sprintf("a P%d case", in.CasePriority)
	case in.Event == "case" || in.Event == "report":
		return Medium, Sonnet, "a problem report"
	case in.Event == "source":
		return Low, Haiku, "a knowledge source changed"
	}
	what := "a schedule"
	if in.Trigger == "manual" {
		what = "Run now"
	}
	switch {
	case in.Kind == kindDigest && in.Production:
		return Low, Sonnet, what + ", a summary, on a production cluster"
	case in.Kind == kindDigest:
		return Low, Haiku, what + ", a summary"
	case in.Kind == kindAudit && in.Production:
		return Medium, Sonnet, what + ", an audit of a production cluster"
	case in.Kind == kindAudit:
		return Medium, Sonnet, what + ", an audit"
	case in.Production:
		return Medium, Sonnet, what + " on a production cluster"
	}
	return Low, Sonnet, what
}

// FromLevel is the model a classifier's level starts a conversation on.
func FromLevel(level string) string {
	switch level {
	case Low:
		return Haiku
	case High, XHigh, Max:
		return Opus
	}
	return Sonnet
}

// Defaults are the effort and model Auto falls back to when the classifier
// is off or did not answer: medium on Claude Sonnet 5.5 (AI-45).
const (
	DefaultEffort = Medium
	DefaultModel  = Sonnet
)

// Turn is a message of a conversation, for Conversation.
type Turn struct {
	// Level is the classifier's word; "" when it gave none (off, too
	// slow, or an answer it may not give), and Failed says why.
	Level  string
	Failed string
	// Model is the model the conversation started on with Auto; "" on its
	// first message. MovedUp says it was moved up already.
	Model   string
	MovedUp bool
}

// Conversation is the pick for one message of a conversation (AI-45): the
// effort from the classifier's level; the model from the first message's
// level, moved up to Claude Opus 5.5 once when a later message is high,
// never down. moved says this message moved it. The limits and the floor
// apply; a setting that is fixed stays as it is set.
func Conversation(s Settings, t Turn, l Limits) (p Pick, moved bool) {
	p = Pick{By: ByClassifier, Effort: t.Level, Why: "the classifier said " + t.Level}
	if t.Level == "" || Rank(t.Level) < 0 || Rank(t.Level) > Rank(High) {
		p = Pick{By: ByFixed, Effort: DefaultEffort, Why: firstOf(t.Failed, "no classifier") + ": the defaults"}
	}
	switch {
	case t.Model == "":
		p.Model = FromLevel(p.Effort)
		if p.By == ByFixed {
			p.Model = DefaultModel
		}
	case p.By == ByClassifier && t.Level == High && Tier(t.Model) >= 0 && Tier(t.Model) < Tier(Opus) && !t.MovedUp && l.DayShare < BudgetShare && s.AutoModel():
		p.Model, moved = Opus, true
		p.Why += "; moved up from " + Label(t.Model)
	default:
		p.Model = t.Model
	}
	// A conversation is never urgent enough for xhigh or max: the
	// classifier answers up to high.
	l.Urgent = false
	return finish(s, p, l), moved
}

// finish applies the limits Auto never crosses, then the floor, then the
// settings that are fixed.
func finish(s Settings, p Pick, l Limits) Pick {
	if r := Rank(p.Effort); r > Rank(High) && !l.Urgent {
		p.Effort = High
		p.Why += "; no xhigh or max without an open P0 or P1 case"
	}
	if l.DayShare >= BudgetShare && Rank(p.Effort) > 0 {
		p.Effort = Levels[Rank(p.Effort)-1]
		p.Why += fmt.Sprintf("; the day's budget is %d%% spent: one level lower", int(l.DayShare*100))
	}
	if f := Rank(s.FloorEffort); f >= 0 && Rank(p.Effort) < f {
		p.Effort = s.FloorEffort
		p.Why += "; raised to the agent's floor"
	}
	if f := Tier(s.FloorModel); f >= 0 && Tier(p.Model) >= 0 && Tier(p.Model) < f {
		p.Model = s.FloorModel
		p.Why += "; raised to the agent's floor model"
	}
	if !s.AutoEffort() {
		p.Effort = s.Effort
	}
	if !s.AutoModel() {
		p.Model = s.Model
	}
	return p
}

// Inherited is the pick for a task handed to an agent in a job's run: the
// effort the run's rules gave the agent that handed it, on the model that
// effort starts a conversation on, never the classifier (the owner: a
// job's run is picked by rules alone). The day's budget was weighed when
// the run's step was picked.
func Inherited(s Settings, effort string) Pick {
	p := Pick{Effort: effort, Model: FromLevel(effort), By: ByRules, Why: "handed work in a job's run at " + effort}
	if Rank(effort) < 0 {
		p = Pick{Effort: DefaultEffort, Model: DefaultModel, By: ByRules, Why: "handed work in a job's run: the defaults"}
	}
	return finish(s, p, Limits{Urgent: Rank(effort) > Rank(High)})
}

// Single is the pick for one request outside a conversation and a run
// (writing a script, weighing a change, a Wiki sync's call to Mycroft):
// no classifier reads a request that long, so the defaults, with the
// floor.
func Single(s Settings) Pick {
	return finish(s, Pick{Effort: DefaultEffort, Model: DefaultModel, By: ByFixed, Why: "one request outside a conversation: the defaults"}, Limits{})
}

// MoveUp says whether a run's later step moves its model up: the step
// wants a stronger model than the run started on, the run has not moved
// up, and the day's budget is below BudgetShare.
func MoveUp(current, wanted string, movedUp bool, dayShare float64) bool {
	return !movedUp && dayShare < BudgetShare && Tier(current) >= 0 && Tier(wanted) > Tier(current)
}

// Said reads a classifier's answer: one of low, medium or high, or "".
func Said(answer string) string {
	a := strings.ToLower(strings.TrimSpace(answer))
	a = strings.Trim(a, ".\"' \n")
	switch a {
	case Low, Medium, High:
		return a
	}
	return ""
}

// Choices are the classifier's words.
var Choices = []string{Low, Medium, High}

// ClassifierPrompt is the classifier's instructions, as measured on the
// A5000 on November 22 (~/p13-qwen/bench.py: 52 of 54 first messages as
// labelled, never lower). first says the message opens the conversation;
// a later one is asked about the same way.
func ClassifierPrompt(first bool) string {
	which := "first message in a conversation"
	if !first {
		which = "latest message in a conversation"
	}
	return "You choose how much effort a Kubernetes operations assistant should spend on a person's " + which + `. Answer with exactly one word: low, medium or high.
low: a lookup, a definition, a how-to, one object's status, small talk. One or two quick reads answer it.
medium: diagnosing one workload or one component, a bounded comparison or review, explaining an alert or a report. Several reads and some reasoning.
high: an outage or incident, a root cause across several components or nodes, anything on production that is down or degraded, security compromise or cluster-wide access review, data loss, or planning a risky change (upgrade, migration, rollout). Deep investigation and careful reasoning.`
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
