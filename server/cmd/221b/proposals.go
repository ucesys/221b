package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/knowledge"
)

// cardsProposer hands what the agents on their own propose to the cards
// store (WC-01), so it waits in AIOps › Inbox and survives a restart. The
// producers keep a proposal of their own with the cards' JSON shape, so a
// proposal crosses as JSON.
type cardsProposer struct{ store *cards.Store }

func (c cardsProposer) propose(ctx context.Context, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var p cards.Proposal
	if err := json.Unmarshal(b, &p); err != nil {
		return "", fmt.Errorf("the proposal does not fit a card: %w", err)
	}
	return c.store.Propose(ctx, p)
}

// jobsProposer is the jobs' Proposer (JB-15).
type jobsProposer struct{ cardsProposer }

func (j jobsProposer) Propose(ctx context.Context, p jobs.Proposal) (string, error) {
	return j.propose(ctx, p)
}

// knowledgeProposer is Mycroft's Proposer (KN-19). The cards store opens
// after the knowledge, so it is set once it has; until then a proposal is
// refused, and the next drift check makes it again.
type knowledgeProposer struct{ cards atomic.Pointer[cardsProposer] }

func (k *knowledgeProposer) Propose(ctx context.Context, p knowledge.Proposal) (string, error) {
	c := k.cards.Load()
	if c == nil {
		return "", errors.New("the store of proposals is not open yet")
	}
	return c.propose(ctx, p)
}

// registerKnowledgeJobs shows Mycroft's jobs in AIOps › Jobs (KN-07,
// JB-03): each source's Wiki sync job (KN-28), made from the sources and
// changed with them, and the drift check (KN-19), which a SuperAdmin
// turns on and sets when.
func registerKnowledgeJobs(ctx context.Context, svc *knowledge.Service, js *jobs.Service) {
	if svc == nil {
		return
	}
	k := knowledge.JobDrift
	spec := k.Default
	jobs.Register(jobs.Kind{ID: k.ID, Label: k.Label, Agent: k.Agent, AgentName: "Mycroft", Order: 62, Does: "Compares the documents with what the console sees, and proposes a card for each difference",
		Default: jobs.When{Schedule: &spec}, Schedulable: true, NoFindings: true,
		Run: func(ctx context.Context, env *jobs.RunEnv) (jobs.Outcome, error) {
			res, err := svc.Drift(ctx)
			return jobs.Outcome{Report: res.Text}, err
		}})
	svc.SetJobs(ctx, js)
}
