package casework

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- A proposed change names the cases it resolves (Phase 12, wave 6b,
// CF-25 to CF-31) ---
//
// A proposed change (a Watson card) exists to solve a problem, and every
// problem is a case: so every card an agent proposes names the cases and
// known issues it resolves, and a case lists every card that names it.
// The link is the card's (cards.Card.Resolves); the case keeps the ids too,
// so a card let go of still shows as gone. A person who works the case may
// link another card to it, or unlink one a person linked; an agent's link
// stays as long as its card.
//
// When a card that resolves a case is applied, the case records the fix
// (by the approver, at the time), goes In progress, and, found by an agent,
// waits for the next run of the job that found it: not seen, the case
// closes as fixed by itself below P1, or waits for its RCA at P0-P1; seen
// again, the fix did not hold. A card rejected, failed or expired leaves a
// note. The cards' store calls CardChanged when a card's state changes;
// the leader's tick catches up with the cards it missed (applied on
// another replica, or while the hook was not set): the case keeps which
// state of each card it acted on, so each acts once.

// fromCards turns the cards' errors into the cases'.
func fromCards(err error) error {
	for _, k := range []struct{ from, to error }{{cards.ErrNotFound, ErrNotFound}, {cards.ErrInvalid, ErrInvalid}, {cards.ErrState, ErrState}} {
		if errors.Is(err, k.from) {
			return fmt.Errorf("%w: %s", k.to, strings.TrimPrefix(err.Error(), k.from.Error()+": "))
		}
	}
	return err
}

// linkable says why a card may not be linked to a case now, if not: one
// that waits, is approved or applied may; one decided otherwise may not.
func linkable(c cards.Card) error {
	switch c.State {
	case cards.Queued, cards.Approved, cards.Applied:
		return nil
	}
	return fmt.Errorf("%w: %s cannot fix anything: %s", ErrState, c.ID, cards.DecidedText(c))
}

// LinkChange links a card to a case, for a person who works it (CF-28):
// the card resolves the case from now on. 404 for a case or card that is
// not there; ErrState for a card on another cluster, one linked already, a
// decided card, or a closed case. A card applied already records its fix
// at once.
func (s *Service) LinkChange(ctx context.Context, id, card string, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if s.o.Cards == nil {
		return View{}, fmt.Errorf("%w: the proposed changes are not kept on this console", ErrState)
	}
	cid := caseID(n)
	got, err := s.o.Cards.Get(ctx, strings.TrimSpace(card))
	if err != nil {
		return View{}, fromCards(err)
	}
	switch {
	case !v.Status.Open():
		return View{}, fmt.Errorf("%w: %s is closed; reopen it to link a change", ErrState, cid)
	case v.Cluster != "" && got.Cluster != v.Cluster:
		return View{}, fmt.Errorf("%w: %s is a change on %s, and %s is on %s", ErrState, got.ID, got.Cluster, cid, v.Cluster)
	case slices.Contains(got.Resolves, cid):
		return View{}, fmt.Errorf("%w: %s resolves %s already", ErrState, got.ID, cid)
	}
	if err := linkable(got); err != nil {
		return View{}, err
	}
	name := firstOf(c.Name, c.Label())
	got, err = s.o.Cards.Link(ctx, got.ID, cid, cards.Person{Key: c.key(), Name: name})
	if err != nil {
		return View{}, fromCards(err)
	}
	now := s.now()
	err = s.updateWork(ctx, n, func(w *Work) error {
		if !slices.Contains(w.Proposals, got.ID) {
			w.Proposals = addID(w.Proposals, got.ID, maxCards)
		}
		if w.LinkedBy == nil {
			w.LinkedBy = map[string]string{}
		}
		w.LinkedBy[got.ID] = name
		addEvent(w, Event{At: now, What: fmt.Sprintf("Linked the change %s: %s", got.ID, clip(got.Title, 200)), By: c.actor()})
		w.Updated = now
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("a change linked to a case could not be noted on its work", "case", cid, "card", got.ID, "err", err)
	}
	s.record(audit.Event{Action: ActionChangeLink, Who: who(c.Person), Cluster: v.Cluster, Target: cid, From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("case %s: the change %s (%s) linked by %s; it resolves %s", cid, got.ID, got.State, name, strings.Join(got.Resolves, ", "))})
	if got.State == cards.Applied {
		s.onCard(ctx, n, got)
	}
	return s.Get(ctx, c, cid)
}

// UnlinkChange takes a card a person linked off a case (CF-28). An
// agent's link stays (ErrState): a person rejects its card instead. 404
// for a case or card that is not there, or a card that does not resolve
// the case.
func (s *Service) UnlinkChange(ctx context.Context, id, card string, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if s.o.Cards == nil {
		return View{}, fmt.Errorf("%w: the proposed changes are not kept on this console", ErrState)
	}
	cid := caseID(n)
	got, err := s.o.Cards.Get(ctx, strings.TrimSpace(card))
	if err != nil {
		return View{}, fromCards(err)
	}
	if !slices.Contains(got.Resolves, cid) {
		return View{}, fmt.Errorf("%w: %s does not resolve %s", ErrNotFound, got.ID, cid)
	}
	if by := got.ResolvesBy[cid]; by == "" || by == cards.LinkedByAgent {
		return View{}, fmt.Errorf("%w: the agent proposed %s for %s, so the link stays; reject the change instead", ErrState, got.ID, cid)
	}
	name := firstOf(c.Name, c.Label())
	if got, err = s.o.Cards.Unlink(ctx, got.ID, cid, cards.Person{Key: c.key(), Name: name}); err != nil {
		return View{}, fromCards(err)
	}
	now := s.now()
	err = s.updateWork(ctx, n, func(w *Work) error {
		w.Proposals = slices.DeleteFunc(w.Proposals, func(x string) bool { return x == got.ID })
		delete(w.LinkedBy, got.ID)
		delete(w.Handled, got.ID)
		addEvent(w, Event{At: now, What: fmt.Sprintf("Unlinked the change %s: %s", got.ID, clip(got.Title, 200)), By: c.actor()})
		w.Updated = now
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("a change unlinked from a case could not be noted on its work", "case", cid, "card", got.ID, "err", err)
	}
	s.record(audit.Event{Action: ActionChangeUnlink, Who: who(c.Person), Cluster: v.Cluster, Target: cid, From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("case %s: the change %s unlinked by %s", cid, got.ID, name)})
	return s.Get(ctx, c, cid)
}

// CardChanged is the cards' store's hook (cards.Store.OnChange): a card
// whose state changed acts on each case and known issue it resolves.
func (s *Service) CardChanged(c cards.Card) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.cardChanged(ctx, c)
}

func (s *Service) cardChanged(ctx context.Context, c cards.Card) {
	for _, ref := range c.Resolves {
		if strings.HasPrefix(ref, "KI-") {
			if c.State == cards.Applied {
				s.onKnown(ctx, ref, c)
			}
			continue
		}
		if n, ok := number(ref); ok {
			s.onCard(ctx, n, c)
		}
	}
}

// approverOf is who approved a card last, the person whose approval
// applied it; Key is "" for a standing approval.
func approverOf(c cards.Card) cards.Person {
	if n := len(c.Approvals); n > 0 {
		d := c.Approvals[n-1]
		if d.Standing != "" {
			return cards.Person{Name: firstOf(d.By.Name, "a standing approval")}
		}
		return d.By
	}
	return cards.Person{Name: "Watson"}
}

// onCard acts on one case of a card: it keeps the card among the case's,
// and once for each state it ends in, applied records the fix and takes
// the case In progress, waiting for the next run of the job that found it
// (CF-29); rejected, failed and expired leave a note.
func (s *Service) onCard(ctx context.Context, n int, c cards.Card) {
	cid := caseID(n)
	final := c.State == cards.Applied || c.State == cards.Failed || c.State == cards.Rejected || c.State == cards.Expired
	approver := approverOf(c)
	at := c.AppliedAt()
	if at.IsZero() {
		at = c.Updated
	}
	text := ""
	switch c.State {
	case cards.Applied:
		text = fmt.Sprintf("Fix applied by change %s: %s", c.ID, clip(c.Title, 200))
	case cards.Failed:
		text = fmt.Sprintf("Change %s failed: %s", c.ID, clip(firstOf(c.Reason, "an action failed"), 300))
	case cards.Rejected:
		by, why := "someone", ""
		if c.Rejection != nil {
			by, why = firstOf(c.Rejection.By.Name, c.Rejection.By.Key, by), c.Rejection.Comment
		}
		text = fmt.Sprintf("Change %s was rejected by %s: %s", c.ID, by, clip(why, 300))
	case cards.Expired:
		text = fmt.Sprintf("Change %s expired unapproved", c.ID)
	}
	// Who did it (CF-35): the approver whose approval applied it, the
	// person (or the system) who rejected it; the system otherwise.
	by := sys
	if d := c.Decider(); d != nil && (c.State == cards.Applied || c.State == cards.Rejected) {
		by = issues.Actor(*d)
	}
	act := false
	now := s.now()
	err := s.updateWork(ctx, n, func(w *Work) error {
		changed := false
		act = false
		if !slices.Contains(w.Proposals, c.ID) {
			w.Proposals, changed = addID(w.Proposals, c.ID, maxCards), true
		}
		if final && w.Handled[c.ID] != c.State {
			if w.Handled == nil {
				w.Handled = map[string]string{}
			}
			w.Handled[c.ID], act, changed = c.State, true, true
			// A card that fell off the list acts no more.
			for k := range w.Handled {
				if !slices.Contains(w.Proposals, k) {
					delete(w.Handled, k)
				}
			}
			if c.State == cards.Applied {
				w.FixApplied = &Note{Text: text, By: approver.Name, At: at, Change: c.ID}
			}
			addEvent(w, Event{At: now, What: text, By: by})
		}
		if !changed {
			return errUnchanged
		}
		w.Updated = now
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.o.Logger.Warn("a change could not be noted on the case it resolves", "case", cid, "card", c.ID, "err", err)
		}
		return
	}
	if !act {
		return
	}
	var was issues.Status
	r, err := s.o.Issues.Apply(ctx, n, func(tx *issues.Tx, r *issues.Case) error {
		tx.As(by)
		was = r.Status
		if c.State == cards.Applied {
			r.AppliedBy = &issues.AppliedChange{Card: c.ID, Title: clip(c.Title, 200), By: approver.Name, At: at}
			// Who approved it is involved in the case (EM-05).
			for _, d := range c.Approvals {
				if d.Standing == "" && d.By.Kind == "" && d.By.Key != "" {
					issues.AddApprovers(r, issues.Person{ID: d.By.Key, Name: d.By.Name})
				}
			}
			if r.Status.Open() {
				if r.Owner == nil && approver.Key != "" {
					r.Owner = &issues.Person{ID: approver.Key, Name: approver.Name}
				}
				issues.Take(r, tx.Now())
				// Found by an agent, the next run of its job checks it; a
				// person's case only records the fix, and a person closes it.
				if r.AgentFound() && r.Key != nil && r.Job != "" {
					r.VerifyPending, r.Verified, r.Verification = true, nil, issues.VerifyWaiting
					r.VerifiedBy, r.Unchecked, r.UncheckedWhy = nil, 0, ""
				}
			}
			tx.Changed(r, &system.Person)
		}
		return tx.Note(r, system.Person, text+".", true)
	})
	if err != nil {
		s.o.Logger.Warn("a change could not act on the case it resolves", "case", cid, "card", c.ID, "err", err)
		return
	}
	if c.State != cards.Applied {
		return
	}
	s.tell(n, mailChange{what: fmt.Sprintf("fix applied by the change %s, now %s", c.ID, shownStatus(r)), by: approver.Key, byName: approver.Name})
	detail := fmt.Sprintf("case %s: fix applied by change %s (%s), approved by %s", cid, c.ID, clip(c.Title, 120), approver.Name)
	switch {
	case !r.Status.Open():
		detail += "; the case is closed, so only the fix is recorded"
	case r.VerifyPending:
		detail += fmt.Sprintf("; %s → %s; the next run of %s verifies it", was, r.Status, r.Job)
	default:
		detail += fmt.Sprintf("; %s → %s; a person closes it", was, r.Status)
	}
	s.record(audit.Event{Action: ActionFixApplied, Who: audit.Who{Subject: approver.Key, Name: approver.Name}, Cluster: r.Cluster, Target: cid, Outcome: audit.OutcomeOK, Detail: detail})
}

// onKnown records a change applied on a known issue it resolves, once: an
// update "Fix applied by change <id>"; it resolves it not by itself
// (CF-29). An update of before that names the card c<n> counts (CF-56).
func (s *Service) onKnown(ctx context.Context, id string, c cards.Card) {
	const fixed = "Fix applied by change "
	text := fixed + c.ID
	err := s.o.Issues.Batch(ctx, func(tx *issues.Tx) error {
		k := tx.Known(id)
		if k == nil {
			return nil
		}
		for _, u := range k.Updates {
			if was, ok := strings.CutPrefix(u.Text, fixed); ok && cardid.Same(was, c.ID) {
				return nil
			}
		}
		k.Log(issues.Update{At: tx.Now(), By: system.Person, Text: text})
		k.Updated = tx.Now()
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("a change applied could not be noted on the known issue it resolves", "known", id, "card", c.ID, "err", err)
	}
}

// catchUp acts on the cards that resolve cases and changed since this
// replica last looked, on the leader (CF-29): what the hook missed. The
// first look after it takes the lead reads them all; a card acts once
// whatever reads it.
func (s *Service) catchUp(ctx context.Context) {
	if s.o.Cards == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	since := s.caught
	s.mu.Unlock()
	list, err := s.o.Cards.List(ctx, cards.Filter{})
	if err != nil {
		s.o.Logger.Warn("the proposed changes could not be read to catch up with them", "err", err)
		return
	}
	for _, c := range list {
		// A margin for the replicas' clocks and the hook under way.
		if len(c.Resolves) == 0 || !since.IsZero() && c.Updated.Before(since.Add(-2*time.Minute)) {
			continue
		}
		s.cardChanged(ctx, c)
	}
	s.mu.Lock()
	s.caught = now
	s.mu.Unlock()
}

// Resolvable says why a proposal on a cluster may not name a case or a
// known issue as what it resolves, if it may not (CF-26, jobs.Resolver):
// a case must be there, open, and on the proposal's cluster (any, for a
// case that names none); a known issue there and accepted.
func (s *Service) Resolvable(ctx context.Context, cluster, id string) error {
	if strings.HasPrefix(id, "KI-") {
		k, err := s.o.Issues.KnownIssue(ctx, system, id)
		if err != nil || k.Draft {
			return fmt.Errorf("there is no known issue %s", id)
		}
		return nil
	}
	n, ok := number(id)
	if !ok {
		return fmt.Errorf("%q is not a case", id)
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(lines, func(l issues.Line) bool { return l.Number == n })
	switch {
	case i < 0:
		return fmt.Errorf("there is no case %s", caseID(n))
	case !lines[i].Open():
		return fmt.Errorf("%s is closed", caseID(n))
	case lines[i].Cluster != "" && cluster != "" && lines[i].Cluster != cluster:
		return fmt.Errorf("%s is on the cluster %s, the change on %s", caseID(n), lines[i].Cluster, cluster)
	}
	return nil
}

var _ jobs.Resolver = (*Service)(nil)
