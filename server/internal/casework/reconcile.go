package casework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
)

// --- Housekeeping puts a case and its work right (phase 12 review, W-9,
// CF-100) ---
//
// A case is kept in two writes: the issue service's record, and 221B's
// work beside it. A failure of the second is only logged, so Housekeeping
// looks for what it left behind and does it:
//
//   - A closed case whose work is still open: its conversation, the
//     commands other than the changes that finished done, or a waiting
//     change that resolves only it. They are let go of, and the change
//     rejected (or passed to the original of a duplicate), as its close
//     does (CF-34, CF-48).
//   - An applied change whose case does not name it as its fix: the case
//     records it as the change's applying does (CF-29), from the change.
//
// Each is noted in the case's history and audited (case.reconcile). The
// closes and changes of the last reconcileDays are looked at; a case
// closes once, and a change waits a day at most.

// reconcileDays is how far back Housekeeping looks.
const reconcileDays = 7

// housekeeper is who the reconciling is by.
var housekeeper = cards.Person{Name: "Housekeeping", Kind: cards.ActorSystem}

// reconcile puts right what a failed second write left behind, and says
// what it did, one line each.
func (s *Service) reconcile(ctx context.Context) ([]string, error) {
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return nil, err
	}
	since := s.now().AddDate(0, 0, -reconcileDays)
	var done []string
	var errs []error
	open := map[int]bool{}
	for _, l := range lines {
		open[l.Number] = l.Open()
	}
	// A closed case's conversation and commands.
	for _, l := range lines {
		if l.Open() || l.ClosedAt == nil || l.ClosedAt.Before(since) {
			continue
		}
		chat, commands, err := s.leftOpen(ctx, l.Number)
		if err != nil || !chat && commands == 0 {
			continue
		}
		s.endTrouble(ctx, l.Number)
		if chat, commands, err = s.leftOpen(ctx, l.Number); err != nil || chat || commands > 0 {
			errs = append(errs, fmt.Errorf("%s is closed, and its conversation and commands could not be let go of", caseID(l.Number)))
			continue
		}
		done = append(done, s.reconciled(ctx, l.Number, "the case is closed: its conversation and the commands other than the changes that finished done were let go of, as its close does"))
	}
	if s.o.Cards == nil {
		return done, errors.Join(errs...)
	}
	// A closed case's waiting changes.
	waiting, err := s.o.Cards.List(ctx, cards.Filter{State: cards.Queued})
	if err != nil {
		errs = append(errs, err)
	}
	var closed []int
	for _, c := range waiting {
		for _, ref := range c.Resolves {
			if n, ok := number(ref); ok && strings.HasPrefix(ref, "C-") && !open[n] && slices.ContainsFunc(lines, func(l issues.Line) bool { return l.Number == n }) && !slices.Contains(closed, n) {
				closed = append(closed, n)
			}
		}
	}
	for _, n := range closed {
		s.settle(ctx, n, housekeeper)
	}
	if len(closed) > 0 {
		still, err := s.o.Cards.List(ctx, cards.Filter{State: cards.Queued})
		if err != nil {
			errs = append(errs, err)
		}
		for _, n := range closed {
			cid := caseID(n)
			var settled []string
			for _, c := range waiting {
				if slices.Contains(c.Resolves, cid) && !slices.ContainsFunc(still, func(x cards.Card) bool { return x.ID == c.ID && slices.Contains(x.Resolves, cid) }) {
					settled = append(settled, c.ID)
				}
			}
			if len(settled) > 0 {
				done = append(done, s.reconciled(ctx, n, fmt.Sprintf("the case is closed: its waiting changes %s were settled, as its close does", strings.Join(settled, ", "))))
			}
		}
	}
	// An applied change its case does not name.
	applied, err := s.o.Cards.List(ctx, cards.Filter{State: cards.Applied})
	if err != nil {
		errs = append(errs, err)
	}
	for _, c := range applied {
		at := c.AppliedAt()
		if at.Before(since) {
			continue
		}
		for _, ref := range c.Resolves {
			n, ok := number(ref)
			if !ok || !strings.HasPrefix(ref, "C-") {
				continue
			}
			named, err := s.namesFix(ctx, n, c)
			if err != nil || named {
				continue
			}
			// It acts again: its case forgets it acted on it.
			if err := s.updateWork(ctx, n, func(w *Work) error {
				if _, ok := w.Handled[c.ID]; !ok {
					return errUnchanged
				}
				delete(w.Handled, c.ID)
				return nil
			}); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, fmt.Errorf("%s: %w", caseID(n), err))
				continue
			}
			s.onCard(ctx, n, c)
			if named, err := s.namesFix(ctx, n, c); err != nil || !named {
				errs = append(errs, fmt.Errorf("%s does not record the change %s applied, and could not be made to", caseID(n), c.ID))
				continue
			}
			done = append(done, s.reconciled(ctx, n, fmt.Sprintf("the change %s was applied, and the case did not record it: it does now", c.ID)))
		}
	}
	return done, errors.Join(errs...)
}

// leftOpen says what a case's work keeps that only an open case keeps:
// its conversation, and how many commands are not changes that finished
// done.
func (s *Service) leftOpen(ctx context.Context, n int) (bool, int, error) {
	keys, _, err := s.cases().Load(ctx, strconv.Itoa(n))
	if err != nil {
		return false, 0, err
	}
	b := keys[ChatKey]
	chat := len(b) > 0 && string(b) != "{}"
	var w Work
	if b, ok := keys[issues.WorkKey]; ok {
		if err := json.Unmarshal(b, &w); err != nil {
			return false, 0, err
		}
	}
	commands := 0
	for _, c := range w.Commands {
		if !c.fixed() {
			commands++
		}
	}
	return chat, commands, nil
}

// namesFix says whether a case names an applied change as its fix: the
// change, or one applied after it.
func (s *Service) namesFix(ctx context.Context, n int, c cards.Card) (bool, error) {
	keys, _, err := s.cases().Load(ctx, strconv.Itoa(n))
	if err != nil {
		return false, err
	}
	var r struct {
		AppliedBy *issues.AppliedChange `json:"appliedBy"`
	}
	if b, ok := keys[issues.CaseKey]; !ok || json.Unmarshal(b, &r) != nil {
		return false, fmt.Errorf("%w: there is no case %s", ErrNotFound, caseID(n))
	}
	a := r.AppliedBy
	return a != nil && (cardid.Same(a.Card, c.ID) || !a.At.Before(c.AppliedAt())), nil
}

// reconciled notes what Housekeeping put right on a case, in its history
// and the audit log, and gives its report's line.
func (s *Service) reconciled(ctx context.Context, n int, what string) string {
	now := s.now()
	if err := s.updateWork(ctx, n, func(w *Work) error {
		addEvent(w, Event{At: now, What: "Housekeeping: " + what, By: sys})
		w.Updated = now
		return nil
	}); err != nil {
		s.o.Logger.Warn("what Housekeeping put right could not be noted on the case", "case", caseID(n), "err", err)
	}
	s.record(audit.Event{Action: ActionReconcile, Who: audit.Who{Subject: "221b", Name: "Housekeeping"}, Target: caseID(n), Outcome: audit.OutcomeOK, Detail: caseID(n) + ": " + what})
	return fmt.Sprintf("- %s: %s", caseID(n), what)
}
