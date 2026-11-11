package casework

import (
	"context"
	"slices"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
)

// A case's stage is what it waits for now, in one word beside its status
// (CF-107): New, In progress and Closed say little of a case an agent
// found, whose flow runs from its analysis over a change's approval to
// the validation run that re-checks the fix. A case has at most one
// stage; none is a case a person simply works on, or one closed with
// nothing left to wait for.
const (
	// StageAnalysisQueued waits for the case analysis job.
	StageAnalysisQueued = "analysisQueued"
	// StageAnalyzing has its analysis running.
	StageAnalyzing = "analyzing"
	// StageFixSuggested has an analysis that suggested a fix, and no
	// proposed change waiting: a person decides what to do.
	StageFixSuggested = "fixSuggested"
	// StageApproval has a proposed change waiting for a person's
	// approval; StageSecondApproval one of the two it needs given.
	StageApproval       = "approval"
	StageSecondApproval = "secondApproval"
	// StageApplying has an approved change running in Watson.
	StageApplying = "applying"
	// StageChangeFailed is a case whose last change run failed.
	StageChangeFailed = "changeFailed"
	// StageValidation waits for a run of the job that found it to
	// re-check the fix: a change applied to an open case, or a case a
	// person closed as fixed.
	StageValidation = "validation"
	// StageClosure has its fix in and waits for a person to close it:
	// verified by a run at P0-P1 (with the RCA), or applied by a change
	// to a case no run re-checks (a person's).
	StageClosure = "closure"
	// StageNotHeld is an open case a run found again after its fix.
	StageNotHeld = "notHeld"
	// StageVerified is a case closed as fixed whose fix a run verified.
	StageVerified = "verified"
)

// stageOf is a case's stage: what it waits for now, the step that needs a
// person before the one that needs a run. change is where its proposed
// changes stand (changeStage).
func stageOf(r issues.Case, analyzing bool, change string) string {
	if !r.Status.Open() {
		switch {
		case r.Outcome.Canonical() != issues.OutcomeFix:
			return ""
		case r.VerifyPending:
			return StageValidation
		case r.Verified != nil:
			return StageVerified
		}
		return ""
	}
	switch {
	case analyzing:
		return StageAnalyzing
	case change == StageApplying, change == StageApproval, change == StageSecondApproval:
		return change
	case r.Verification == issues.VerifyWaiting:
		return StageValidation
	case r.AnalysisQueued != nil:
		return StageAnalysisQueued
	case r.Verification == issues.VerifyVerified:
		return StageClosure
	case r.Verification == issues.VerifyNotHeld:
		return StageNotHeld
	case change == StageChangeFailed:
		return StageChangeFailed
	case r.AppliedBy != nil:
		// Applied, and no run re-checks it: a person closes it. Reopened
		// since, the change it names is the one of before, and the case
		// says nothing.
		if r.Reopened > 0 {
			return ""
		}
		return StageClosure
	case r.FixProposed:
		return StageFixSuggested
	}
	return ""
}

// changeStage is where a case's proposed changes stand, as a stage: one
// running, one waiting for an approval (the second of two, when one was
// given), or the one that ran last failed; "" when none says anything.
func changeStage(list []cards.Card) string {
	out := ""
	var last cards.Card
	for _, c := range list {
		switch c.State {
		case cards.Approved:
			return StageApplying
		case cards.Queued:
			if c.Needed > 1 && len(c.Approvals) > 0 {
				if out == "" {
					out = StageSecondApproval
				}
			} else {
				out = StageApproval
			}
		case cards.Applied, cards.Failed:
			if c.Updated.After(last.Updated) {
				last = c
			}
		}
	}
	if out == "" && last.State == cards.Failed {
		return StageChangeFailed
	}
	return out
}

// changeStages is every case's changeStage, by the case's id ("C-12"),
// from one read of the proposed changes; empty when they cannot be read.
func (s *Service) changeStages(ctx context.Context) map[string]string {
	out := map[string]string{}
	if s.o.Cards == nil {
		return out
	}
	all, err := s.o.Cards.List(ctx, cards.Filter{})
	if err != nil {
		return out
	}
	by := map[string][]cards.Card{}
	for _, c := range all {
		for _, id := range c.Resolves {
			if !slices.Contains(cardIDs(by[id]), c.ID) {
				by[id] = append(by[id], c)
			}
		}
	}
	for id, list := range by {
		if st := changeStage(list); st != "" {
			out[id] = st
		}
	}
	return out
}

func cardIDs(list []cards.Card) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.ID
	}
	return out
}
