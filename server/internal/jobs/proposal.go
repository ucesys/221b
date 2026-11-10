package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Proposals (JB-12): any change a job's agent wants becomes a proposal,
// which the Proposer files as a Watson card that waits for a person in
// AIOps › Inbox. The shape is shared with the cards' package (the seams of
// Phase 12 wave 2); cmd/221b plugs the cards' store in. Never a secret
// value or a host key.

// Proposal is one change an agent proposes.
type Proposal struct {
	Agent     string     `json:"agent"`
	Source    Source     `json:"source"`
	Cluster   string     `json:"cluster,omitempty"`
	Title     string     `json:"title"`
	Reasoning string     `json:"reasoning,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	Actions   []Action   `json:"actions"`
	Condition Condition  `json:"condition"`
	// DedupKey makes a newer proposal for the same thing replace an older.
	DedupKey string    `json:"dedupKey,omitempty"`
	Expires  time.Time `json:"expires,omitzero"`
	// --- A proposed change names the cases it resolves (Phase 12, wave
	// 6b, CF-25 to CF-27) ---
	// Resolves are the cases ("C-12") and known issues ("KI-3") it
	// resolves: what its kind names (a case analysis its case), and what
	// the run maps from Fixes. A proposal of a run that resolves nothing
	// is not filed.
	Resolves []string `json:"resolves,omitempty"`
	// Fixes is what the agent said it fixes: a position is a finding's in
	// the run's outcome, from 1 (merge counts it on from the answer's).
	Fixes []Fix `json:"-"`
}

// Resolver says why a proposal on a cluster may not name a case or a
// known issue as what it resolves, if it may not (CF-26): the case must be
// there, open and on the proposal's cluster, the known issue accepted.
// The router (package casework) is one.
type Resolver interface {
	Resolvable(ctx context.Context, cluster, id string) error
}

// Titler gives the titles of cases and known issues, for what a run's
// proposals resolve (CF-39); the router (package casework) is one.
type Titler interface {
	Titles(ctx context.Context, ids []string) map[string]string
}

// Source is what started the proposal.
type Source struct {
	// Kind is job, alert, report, knowledge or chat.
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
	SetBy string `json:"setBy,omitempty"`
	// Job is the name of the job whose run made it (CF-92).
	Job string `json:"job,omitempty"`
}

// Evidence is one piece of what the proposal rests on.
type Evidence struct {
	// Kind is log, event, fact, page or finding.
	Kind string `json:"kind"`
	Text string `json:"text"`
	Ref  string `json:"ref,omitempty"`
	At   string `json:"at,omitempty"`
}

// Action is one thing Watson would do once a person approves.
type Action struct {
	// Kind is kubectl, helm, host.add, host.label, host.remove,
	// fleet.create, fleet.update, automation.params or record.
	Kind   string         `json:"kind"`
	Args   map[string]any `json:"args"`
	Impact string         `json:"impact,omitempty"`
	// Risk is low, medium or high.
	Risk string `json:"risk,omitempty"`
}

// Condition is what must still hold when a person approves.
type Condition struct {
	// Kind is alert-firing, resource-version or none.
	Kind string         `json:"kind"`
	Args map[string]any `json:"args,omitempty"`
}

// Proposer files a proposal and returns its id.
type Proposer interface {
	Propose(ctx context.Context, p Proposal) (id string, err error)
}

// MemoryProposer keeps proposals in memory (tests, and a console without
// the cards' store).
type MemoryProposer struct {
	mu   sync.Mutex
	list []Proposal
}

// Propose keeps the proposal; one with the dedup key of another replaces it.
func (m *MemoryProposer) Propose(_ context.Context, p Proposal) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, q := range m.list {
		if p.DedupKey != "" && q.DedupKey == p.DedupKey {
			m.list[i] = p
			return fmt.Sprintf("proposal-%d", i+1), nil
		}
	}
	m.list = append(m.list, p)
	return fmt.Sprintf("proposal-%d", len(m.list)), nil
}

// List returns the proposals kept.
func (m *MemoryProposer) List() []Proposal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Proposal(nil), m.list...)
}
