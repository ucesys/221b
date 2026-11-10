// Package cards keeps the agents' proposals as durable Watson cards
// (Phase 12, ADR-99 to ADR-102). Any change an agent wants, asked in a
// chat or on its own at night, becomes a card that waits in AIOps › Inbox
// and the cluster's Watson panel until people approve or reject it, it
// expires, or a newer proposal for the same thing replaces it. A card
// says what started it, which agent proposed it and why, the evidence,
// and exactly what it will do; Watson checks it again when it is
// approved, and an agent never counts as a person, so on production a
// card an agent started needs two. Approved, its actions run through
// Watson: kubectl and helm, and the console's own records (hosts, fleets,
// Automation parameters). Cards live in the <release>-cases ConfigMap,
// with the standing approvals and the notification settings; the
// notification channels' addresses are secrets, kept in a Secret.
package cards

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The keys of the cases ConfigMap.
const (
	CardsKey    = "cards.json"
	StandingKey = "cards-standing.json"
	NotifyKey   = "cards-notify.json"
)

// The most each key holds by default; the console passes its layout's.
const (
	DefaultCardsBytes    = 640 << 10
	DefaultStandingBytes = 64 << 10
	DefaultNotifyBytes   = 16 << 10
)

// States of a card: queued until decided; approved while its actions
// run; applied or failed after. Rejected, expired and replaced cards never
// ran.
const (
	Queued   = "queued"
	Approved = "approved"
	Rejected = "rejected"
	Expired  = "expired"
	Replaced = "replaced"
	Applied  = "applied"
	Failed   = "failed"
)

// Kinds of action. kubectl and helm run through Watson's runner; the rest
// are Watson's console actions (ADR-101).
const (
	KindKubectl          = "kubectl"
	KindHelm             = "helm"
	KindHostAdd          = "host.add"
	KindHostLabel        = "host.label"
	KindHostRemove       = "host.remove"
	KindFleetCreate      = "fleet.create"
	KindFleetUpdate      = "fleet.update"
	KindAutomationParams = "automation.params"
	// KindRecord is another console record, one type at a time, each with
	// its own card shape (Store.RegisterRecord).
	KindRecord = "record"
)

// Kinds of source: what started the agent.
const (
	SourceJob       = "job"
	SourceAlert     = "alert"
	SourceReport    = "report"
	SourceKnowledge = "knowledge"
	SourceChat      = "chat"
	// SourceCase is a case's analysis (Phase 12, case work): ID names the
	// case, "C-12".
	SourceCase = "case"
)

// Kinds of condition: what must still hold when the card is approved.
const (
	ConditionNone    = "none"
	ConditionAlert   = "alert-firing"
	ConditionVersion = "resource-version"
)

// Limits of one proposal; longer text is cut, more items refused.
const (
	MaxTitle     = 200
	MaxReasoning = 4000
	MaxEvidence  = 20
	MaxText      = 1000
	MaxActions   = 50
	MaxArgs      = 8 << 10
	MaxImpact    = 500
	MaxComment   = 500
	// MaxResolves is how many cases and known issues one card resolves
	// (CF-25).
	MaxResolves = 20
	// MaxOpen is how many cards may wait at once.
	MaxOpen = 300
	// Lifetime is the longest a card waits: 24 hours (design: Expires).
	Lifetime = 24 * time.Hour
	// keepFinished is how many decided cards the history keeps, as many as
	// fit; maxHistory the events kept on one card.
	keepFinished = 500
	maxHistory   = 30
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("no such proposal")
	ErrInvalid  = errors.New("not valid")
	ErrState    = errors.New("the proposal cannot do that now")
	// ErrOwn is an approval its requester may not give.
	ErrOwn = errors.New("someone else must approve it")
	// ErrForbidden is an approval this person may not give.
	ErrForbidden = errors.New("not yours to approve")
	// ErrStale is a card that does not hold as it stands: Recheck says why.
	ErrStale = errors.New("cannot be approved as it stands")
	ErrFull  = errors.New("too many proposals wait")
)

// Source is what started the agent: a job, an alert, a report, a change
// in the knowledge sources, or a person in a chat, and who set it up.
type Source struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Label string `json:"label"`
	// SetBy names who set the trigger, or asked in the chat.
	SetBy string `json:"setBy,omitempty"`
	// Job names the job whose run made it ("Nightly audit"), so the
	// drawer says "Nightly audit, run-12" (CF-92); empty for none.
	Job string `json:"job,omitempty"`
}

// Evidence is one thing the agent read: a log line, an event, a fact, a
// wiki page, a finding, with when it was true.
type Evidence struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	Ref  string    `json:"ref,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// Action is one thing the card does, with the agent's word on its impact
// and risk (low, medium, high).
type Action struct {
	Kind   string         `json:"kind"`
	Args   map[string]any `json:"args"`
	Impact string         `json:"impact,omitempty"`
	Risk   string         `json:"risk,omitempty"`
}

// Condition is what must still hold when the card is approved: an alert
// still firing, an object still at the version the agent read.
type Condition struct {
	Kind string         `json:"kind"`
	Args map[string]any `json:"args,omitempty"`
}

// Proposal is what an agent proposes (the shape of the seams between the
// packages that produce them). Never a secret value or a host key.
type Proposal struct {
	Agent     string     `json:"agent"`
	Source    Source     `json:"source"`
	Cluster   string     `json:"cluster"`
	Title     string     `json:"title"`
	Reasoning string     `json:"reasoning,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	Actions   []Action   `json:"actions"`
	Condition Condition  `json:"condition"`
	// DedupKey names what the proposal is about: a newer proposal with the
	// same key replaces the older one.
	DedupKey string `json:"dedupKey,omitempty"`
	// Expires is when it stops waiting: at most 24 hours on.
	Expires time.Time `json:"expires,omitzero"`
	// --- A proposed change names the cases it resolves (Phase 12, wave
	// 6b, CF-25 to CF-28) ---
	// Resolves are the cases ("C-12") and known issues ("KI-3") the change
	// resolves; ResolvesBy says who linked each: LinkedByAgent, or the
	// person's name.
	Resolves   []string          `json:"resolves,omitempty"`
	ResolvesBy map[string]string `json:"resolvesBy,omitempty"`
}

// LinkedByAgent is who linked a case to a change the agent proposed for
// it: its link stays as long as the card (CF-28).
const LinkedByAgent = "agent"

// Person is someone who decided a card: Key tells people apart (the user
// name), Name is shown.
type Person struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Kind is ActorSystem for the console deciding on its own (a case
	// closed rejects its waiting changes, CF-34); "" is a person.
	Kind string `json:"kind,omitempty"`
}

// --- Who did it, everywhere (Phase 12, wave 6c, CF-35) ---

// Actor is who made a change to a card: a person, an agent or the system.
type Actor struct {
	Name string `json:"name"`
	// Kind is ActorPerson, ActorAgent or ActorSystem.
	Kind string `json:"kind"`
}

// The kinds of Actor.
const (
	ActorPerson = "person"
	ActorAgent  = "agent"
	ActorSystem = "system"
)

// SystemActor is the console deciding on its own: a card expired,
// replaced, or rejected because its case closed.
var SystemActor = Actor{Name: "System", Kind: ActorSystem}

// SystemPerson is the console as the person who decides a card on its
// own (CF-34).
var SystemPerson = Person{Key: "system", Name: "System", Kind: ActorSystem}

// ActorOf is a person who decided a card as an actor: a standing approval
// or the console is the system.
func ActorOf(p Person) Actor {
	if p.Kind == ActorSystem || p.Key == SystemPerson.Key || strings.HasPrefix(p.Key, "standing:") {
		return Actor{Name: firstNonEmpty(p.Name, SystemActor.Name), Kind: ActorSystem}
	}
	return Actor{Name: firstNonEmpty(p.Name, p.Key, "someone"), Kind: ActorPerson}
}

// Proposer is who proposed the card: the person who asked in a chat, or
// the agent.
func (c Card) Proposer() Actor {
	if c.ProposedBy != nil {
		return *c.ProposedBy
	}
	if c.Requester != nil {
		return ActorOf(*c.Requester)
	}
	return Actor{Name: firstNonEmpty(c.Agent, "an agent"), Kind: ActorAgent}
}

// Decider is who decided the card, once decided: the approver whose
// approval made it run, the person who rejected it, or the system for a
// card that expired, was replaced or failed while nobody ran it.
func (c Card) Decider() *Actor {
	var a Actor
	switch {
	case c.State == Queued:
		return nil
	case c.State == Rejected && c.Rejection != nil:
		a = ActorOf(c.Rejection.By)
	case (c.State == Approved || c.State == Applied || c.State == Failed) && len(c.Approvals) > 0:
		a = ActorOf(c.Approvals[len(c.Approvals)-1].By)
	default:
		a = SystemActor
	}
	return &a
}

// --- end of who did it.

// Decision is an approval or a rejection, with its comment.
type Decision struct {
	By      Person    `json:"by"`
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
	// Standing names the standing approval that gave it.
	Standing string `json:"standing,omitempty"`
}

// Plan is what Watson read of one action when it was proposed: what it
// does, the objects, and the fingerprint the re-check compares.
type Plan struct {
	Summary     string   `json:"summary"`
	Targets     []string `json:"targets,omitempty"`
	Impact      []string `json:"impact,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	// Unread says why Watson could not read the target then.
	Unread string `json:"unread,omitempty"`
	// Warning is what approving it would do that is likely not meant,
	// though allowed: a fleet with no node now (KN-30). Check again says
	// it too.
	Warning string `json:"warning,omitempty"`
}

// Recheck is the last check of a card: its condition still holds and its
// targets have not changed since it was proposed.
type Recheck struct {
	At time.Time `json:"at"`
	OK bool      `json:"ok"`
	// Why says what failed; Checked what held.
	Why     string   `json:"why,omitempty"`
	Checked []string `json:"checked,omitempty"`
	// Warnings are the actions' warnings now, each with its action, when
	// it holds (Plan.Warning).
	Warnings []string `json:"warnings,omitempty"`
}

// Result is how one action went. Never its output: a kubectl command's is
// in the approver's Watson panel, Ref naming its card there.
type Result struct {
	Action  int       `json:"action"`
	OK      bool      `json:"ok"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail,omitempty"`
	Ref     string    `json:"ref,omitempty"`
	At      time.Time `json:"at"`
}

// Event is one line of a card's history: who did what, when; Actor names
// who, with their kind (CF-35).
type Event struct {
	At    time.Time `json:"at"`
	What  string    `json:"what"`
	By    string    `json:"by,omitempty"`
	Actor *Actor    `json:"actor,omitempty"`
}

// Card is a proposal as the console keeps it.
type Card struct {
	ID string `json:"id"`
	Proposal
	// Model is the agent's model when it proposed.
	Model   string    `json:"model,omitempty"`
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Requester is the person who asked for it in a chat: they count as
	// one of the people a change needs. nil for an agent on its own.
	Requester *Person `json:"requester,omitempty"`
	// Production and Needed are the cluster's environment and the
	// approvals it needs, as last worked out.
	Production bool       `json:"production,omitempty"`
	Needed     int        `json:"needed"`
	Approvals  []Decision `json:"approvals,omitempty"`
	Rejection  *Decision  `json:"rejection,omitempty"`
	Recheck    *Recheck   `json:"recheck,omitempty"`
	// Plans are Watson's reading of each action when it was proposed.
	Plans   []Plan   `json:"plans"`
	Results []Result `json:"results,omitempty"`
	// Reason says why it expired or failed.
	Reason     string `json:"reason,omitempty"`
	ReplacedBy string `json:"replacedBy,omitempty"`
	Replaces   string `json:"replaces,omitempty"`
	// Standing is the standing approval that applied it.
	Standing string `json:"standing,omitempty"`
	// WatsonItem is a chat card's command in the Watson panel, which the
	// person may approve there instead (it lives only in memory).
	WatsonItem string  `json:"watsonItem,omitempty"`
	History    []Event `json:"history,omitempty"`
	// ProposedBy is the agent that proposed it, or the person who asked
	// for it in a chat; LastBy and LastAt who changed it last, and when
	// (CF-35).
	ProposedBy *Actor     `json:"proposedBy,omitempty"`
	LastBy     *Actor     `json:"lastBy,omitempty"`
	LastAt     *time.Time `json:"lastAt,omitempty"`
	// Commands are the commands run from the proposal's drawer and its own
	// actions once approved (CF-61): every one while it waits, the changes
	// that finished done once decided. Never their output.
	Commands []Command `json:"commands,omitempty"`
}

// Open says whether the card still waits for a decision.
func (c Card) Open() bool { return c.State == Queued }

// Finished says whether the card is decided and done with.
func (c Card) Finished() bool { return c.State != Queued && c.State != Approved }

// AppliedAt is when the card was applied: its last "applied" event, the
// time of its last result otherwise; zero for one not applied.
func (c Card) AppliedAt() time.Time {
	if c.State != Applied {
		return time.Time{}
	}
	for i := len(c.History) - 1; i >= 0; i-- {
		if strings.HasPrefix(c.History[i].What, Applied) {
			return c.History[i].At
		}
	}
	if n := len(c.Results); n > 0 {
		return c.Results[n-1].At
	}
	return c.Updated
}

// Kinds lists the kinds of its actions.
func (c Card) Kinds() []string {
	out := make([]string, len(c.Actions))
	for i, a := range c.Actions {
		out[i] = a.Kind
	}
	return out
}

// Took says whether a person decided the card, or asked for it.
func (c Card) Took(key string) bool {
	if key == "" {
		return false
	}
	if c.Requester != nil && c.Requester.Key == key {
		return true
	}
	for _, d := range c.Approvals {
		if d.By.Key == key {
			return true
		}
	}
	return c.Rejection != nil && c.Rejection.By.Key == key
}

// decodeArgs reads an action's arguments into v, refusing any field it
// does not know, so nothing rides on a card unseen.
func decodeArgs(args map[string]any, v any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
