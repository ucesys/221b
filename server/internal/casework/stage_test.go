package casework

import (
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
)

// A case's stage is what it waits for now (CF-107): one word, the step
// that needs a person before the one that needs a run.
func TestStageOf(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	open := func(f func(*issues.Case)) issues.Case {
		c := issues.Case{Status: issues.StatusInProgress}
		if f != nil {
			f(&c)
		}
		return c
	}
	closed := func(o issues.Outcome, f func(*issues.Case)) issues.Case {
		c := issues.Case{Status: issues.StatusClosed, Outcome: o}
		if f != nil {
			f(&c)
		}
		return c
	}
	for _, tc := range []struct {
		name      string
		c         issues.Case
		analyzing bool
		change    string
		want      string
	}{
		{"a case a person works on has none", open(nil), false, "", ""},
		{"queued for analysis", open(func(c *issues.Case) { c.AnalysisQueued = &at }), false, "", StageAnalysisQueued},
		{"its analysis runs, though still marked queued", open(func(c *issues.Case) { c.AnalysisQueued = &at }), true, "", StageAnalyzing},
		{"a fix suggested, no change waiting", open(func(c *issues.Case) { c.FixProposed = true }), false, "", StageFixSuggested},
		{"a change waits for approval", open(func(c *issues.Case) { c.FixProposed = true }), false, StageApproval, StageApproval},
		{"a change waits for its second approval", open(func(c *issues.Case) { c.FixProposed = true }), false, StageSecondApproval, StageSecondApproval},
		{"an approved change runs", open(func(c *issues.Case) { c.FixProposed = true }), false, StageApplying, StageApplying},
		{"the last change failed", open(func(c *issues.Case) { c.FixProposed = true }), false, StageChangeFailed, StageChangeFailed},
		{"a fix applied waits for its validation run", open(func(c *issues.Case) {
			c.VerifyPending, c.Verification, c.FixProposed = true, issues.VerifyWaiting, true
		}), false, "", StageValidation},
		{"another change waiting comes before the validation run", open(func(c *issues.Case) { c.VerifyPending, c.Verification = true, issues.VerifyWaiting }), false, StageApproval, StageApproval},
		{"verified at P0-P1, it waits for its closure", open(func(c *issues.Case) { c.Verification, c.Verified, c.FixProposed = issues.VerifyVerified, &at, true }), false, "", StageClosure},
		{"a change applied the fix of a person's case: a person closes it", open(func(c *issues.Case) { c.AppliedBy, c.FixProposed = &issues.AppliedChange{Card: "PC-1"}, true }), false, "", StageClosure},
		{"reopened by a person since a change was applied, it says nothing", open(func(c *issues.Case) {
			c.AppliedBy, c.Reopened, c.FixProposed = &issues.AppliedChange{Card: "PC-1"}, 1, true
		}), false, "", ""},
		{"a later change failed after one was applied", open(func(c *issues.Case) { c.AppliedBy = &issues.AppliedChange{Card: "PC-1"} }), false, StageChangeFailed, StageChangeFailed},
		{"the fix did not hold", open(func(c *issues.Case) { c.Verification, c.FixProposed = issues.VerifyNotHeld, true }), false, "", StageNotHeld},
		{"the fix did not hold, and it is queued for analysis again", open(func(c *issues.Case) { c.Verification, c.AnalysisQueued = issues.VerifyNotHeld, &at }), false, "", StageAnalysisQueued},
		{"the fix did not hold, and a new change waits", open(func(c *issues.Case) { c.Verification = issues.VerifyNotHeld }), false, StageApproval, StageApproval},
		{"an earlier failed change says nothing once a fix did not hold", open(func(c *issues.Case) { c.Verification = issues.VerifyNotHeld }), false, StageChangeFailed, StageNotHeld},
		{"closed as fixed, it waits for its validation run", closed(issues.OutcomeFix, func(c *issues.Case) { c.VerifyPending = true }), false, "", StageValidation},
		{"closed as fixed and verified", closed(issues.OutcomeFix, func(c *issues.Case) { c.Verified = &at }), false, "", StageVerified},
		{"closed as fixed by a person, a person's case", closed(issues.OutcomeFix, nil), false, "", ""},
		{"closed with another outcome", closed(issues.OutcomeNoAction, func(c *issues.Case) { c.FixProposed = true }), false, StageApproval, ""},
	} {
		if got := stageOf(tc.c, tc.analyzing, tc.change); got != tc.want {
			t.Errorf("%s: stage %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Where a case's proposed changes stand: a running one first, then one
// waiting for an approval, then a failure of the one that ran last.
func TestChangeStage(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	card := func(state string, minutes int, f func(*cards.Card)) cards.Card {
		c := cards.Card{State: state, Needed: 1, Updated: at.Add(time.Duration(minutes) * time.Minute)}
		if f != nil {
			f(&c)
		}
		return c
	}
	one := func(c *cards.Card) { c.Needed, c.Approvals = 2, []cards.Decision{{}} }
	two := func(c *cards.Card) { c.Needed = 2 }
	for _, tc := range []struct {
		name string
		list []cards.Card
		want string
	}{
		{"no changes", nil, ""},
		{"rejected, expired and replaced ones say nothing", []cards.Card{card(cards.Rejected, 0, nil), card(cards.Expired, 1, nil), card(cards.Replaced, 2, nil)}, ""},
		{"one waiting", []cards.Card{card(cards.Queued, 0, nil)}, StageApproval},
		{"one waiting on production, no approval yet", []cards.Card{card(cards.Queued, 0, two)}, StageApproval},
		{"one of two approvals given", []cards.Card{card(cards.Queued, 0, one)}, StageSecondApproval},
		{"a first approval still to give comes before a second", []cards.Card{card(cards.Queued, 0, one), card(cards.Queued, 1, nil)}, StageApproval},
		{"one running comes first", []cards.Card{card(cards.Queued, 0, nil), card(cards.Approved, 1, nil)}, StageApplying},
		{"applied", []cards.Card{card(cards.Applied, 0, nil)}, ""},
		{"the last one run failed", []cards.Card{card(cards.Applied, 0, nil), card(cards.Failed, 5, nil)}, StageChangeFailed},
		{"a failure before a later applied one", []cards.Card{card(cards.Failed, 0, nil), card(cards.Applied, 5, nil)}, ""},
		{"a failure, and a new one waiting", []cards.Card{card(cards.Failed, 0, nil), card(cards.Queued, 5, nil)}, StageApproval},
	} {
		if got := changeStage(tc.list); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
