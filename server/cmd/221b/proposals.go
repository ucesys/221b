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
// store (WC-01), so it waits in Cases › Inbox and survives a restart. The
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

// registerKnowledgeJobs shows Mycroft's syncs and drift check in Cases ›
// Jobs (KN-07, JB-03), where a SuperAdmin turns them on and sets when.
func registerKnowledgeJobs(svc *knowledge.Service) {
	if svc == nil {
		return
	}
	kind := func(k knowledge.JobKind, order int, does string, run func(ctx context.Context) (string, error)) jobs.Kind {
		spec := k.Default
		return jobs.Kind{ID: k.ID, Label: k.Label, Agent: k.Agent, AgentName: "Mycroft", Order: order, Does: does,
			Default: jobs.When{Schedule: &spec}, Schedulable: true,
			Run: func(ctx context.Context, env *jobs.RunEnv) (jobs.Outcome, error) {
				text, err := run(ctx)
				return jobs.Outcome{Report: text}, err
			}}
	}
	sync := func(full bool) func(ctx context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			res, err := svc.Sync(ctx, full)
			return res.Text, err
		}
	}
	jobs.Register(kind(knowledge.JobSync, 60, "Reads every source again and proposes the wiki's pages", sync(true)))
	jobs.Register(kind(knowledge.JobSyncChanges, 61, "Reads the source items changed since the last read", sync(false)))
	jobs.Register(kind(knowledge.JobDrift, 62, "Compares the documents with what the console sees, and proposes a card for each difference", func(ctx context.Context) (string, error) {
		res, err := svc.Drift(ctx)
		return res.Text, err
	}))
}
