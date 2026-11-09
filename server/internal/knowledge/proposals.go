package knowledge

import (
	"context"
	"sync"
	"time"
)

// Mycroft's inventory and drift proposals become Watson cards a person
// approves (package cards, wired in at the merge). This package names
// them in the cards' shape and hands them to a Proposer; a proposal never
// holds a credential or a host key.

// Proposal is one change Mycroft wants, as a card shows it.
type Proposal struct {
	Agent     string           `json:"agent"`
	Source    ProposalSource   `json:"source"`
	Cluster   string           `json:"cluster"`
	Title     string           `json:"title"`
	Reasoning string           `json:"reasoning"`
	Evidence  []Evidence       `json:"evidence"`
	Actions   []ProposalAction `json:"actions"`
	Condition Condition        `json:"condition"`
	DedupKey  string           `json:"dedupKey"`
	Expires   time.Time        `json:"expires"`
}

// ProposalSource is what started a proposal.
type ProposalSource struct {
	// Kind is "knowledge" for Mycroft's.
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
	SetBy string `json:"setBy,omitempty"`
}

// Evidence is one thing a proposal rests on.
type Evidence struct {
	// Kind is "page" (a source's page or document) or "fact".
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	Ref  string    `json:"ref,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// ProposalAction is one thing Watson would do.
type ProposalAction struct {
	// Kind is "host.add" or "record".
	Kind   string         `json:"kind"`
	Args   map[string]any `json:"args"`
	Impact string         `json:"impact"`
	Risk   string         `json:"risk"`
}

// Condition is what must still hold when the card is approved.
type Condition struct {
	Kind string         `json:"kind"`
	Args map[string]any `json:"args"`
}

// Proposer takes proposals; it returns the card's id.
type Proposer interface {
	Propose(ctx context.Context, p Proposal) (id string, err error)
}

// NopProposer drops every proposal.
type NopProposer struct{}

func (NopProposer) Propose(context.Context, Proposal) (string, error) { return "", nil }

// MemoryProposer keeps the proposals it is given, for tests.
type MemoryProposer struct {
	mu   sync.Mutex
	List []Proposal
}

func (m *MemoryProposer) Propose(_ context.Context, p Proposal) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.List = append(m.List, p)
	return p.DedupKey, nil
}

// Proposals returns a copy of what was proposed.
func (m *MemoryProposer) Proposals() []Proposal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Proposal(nil), m.List...)
}
