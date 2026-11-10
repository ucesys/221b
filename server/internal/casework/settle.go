package casework

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
)

// --- Closing a case settles its waiting changes (Phase 12, wave 6c,
// CF-34) ---
//
// A change that still waits for its approval (a queued card) exists to
// fix a case. When the case closes, with any outcome, each waiting change
// that resolves it and nothing else still open is rejected, by whoever
// closed the case (the system for a close of its own), saying why: "Case
// C-12 was closed as no action needed". A change that also resolves another
// open case, or a known issue not yet resolved, keeps waiting, with this
// case still listed. A case closed as a duplicate passes its waiting
// changes on instead: they resolve its original too, linked by the
// system. A change applied already is not touched: the case's own
// verification closes a case a change fixed (CF-30).

// outcomeWords are a close's outcome as the reason of a rejection says
// it: "Case C-12 was closed as <words>".
func outcomeWords(o issues.Outcome, known string) string {
	switch o.Canonical() {
	case issues.OutcomeFix:
		return "fixed"
	case issues.OutcomeNoAction:
		return "no action needed"
	case issues.OutcomeKnownIssue:
		if known != "" {
			return "the known issue " + known
		}
		return "a known issue"
	case issues.OutcomeException:
		return "an accepted exception"
	case issues.OutcomeDuplicate:
		return "a duplicate"
	}
	return string(o)
}

// systemCard is the console as the person a card's decision names.
var systemCard = cards.SystemPerson

// closed does what follows a case's close, by any path: its
// troubleshooting ends (its conversation deleted, its command log kept to
// the changes that finished done: CF-47, CF-48), and its waiting changes
// are settled (CF-34).
func (s *Service) closed(ctx context.Context, n int, by cards.Person) {
	s.endTrouble(ctx, n)
	s.settle(ctx, n, by)
}

// settle settles the waiting changes of a case just closed (CF-34): each
// one that resolves only this case among the open ones is rejected by
// by; a duplicate's are linked to its original instead. It reads the case
// as it stands, so a case open again by now settles nothing.
func (s *Service) settle(ctx context.Context, n int, by cards.Person) {
	if s.o.Cards == nil {
		return
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		s.o.Logger.Warn("a closed case's waiting changes could not be settled", "case", caseID(n), "err", err)
		return
	}
	open := map[int]bool{}
	var self *issues.Line
	for i := range lines {
		l := &lines[i]
		open[l.Number] = l.Open()
		if l.Number == n {
			self = l
		}
	}
	if self == nil || self.Open() {
		return
	}
	waiting, err := s.o.Cards.List(ctx, cards.Filter{State: cards.Queued})
	if err != nil {
		s.o.Logger.Warn("a closed case's waiting changes could not be read", "case", caseID(n), "err", err)
		return
	}
	cid := caseID(n)
	var known map[string]bool
	for _, c := range waiting {
		if !slices.Contains(c.Resolves, cid) {
			continue
		}
		if self.Outcome == issues.OutcomeDuplicate && self.DuplicateOf != 0 {
			s.passOn(ctx, c, n, self.DuplicateOf)
			continue
		}
		if known == nil {
			known = s.openKnown(ctx)
		}
		if keeps(c, cid, open, known) {
			continue
		}
		reason := fmt.Sprintf("Case %s was closed as %s", cid, outcomeWords(self.Outcome, self.KnownIssue))
		if _, err := s.o.Cards.Reject(ctx, c.ID, by, reason); err != nil {
			s.o.Logger.Warn("a closed case's waiting change could not be rejected", "case", cid, "card", c.ID, "err", err)
		}
	}
}

// keeps says whether a waiting change still has an open case or known
// issue to resolve besides cid.
func keeps(c cards.Card, cid string, open map[int]bool, known map[string]bool) bool {
	for _, ref := range c.Resolves {
		if ref == cid {
			continue
		}
		if strings.HasPrefix(ref, "KI-") {
			if known[ref] {
				return true
			}
			continue
		}
		if m, ok := number(ref); ok && open[m] {
			return true
		}
	}
	return false
}

// openKnown are the known issues not resolved.
func (s *Service) openKnown(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	list, err := s.o.Issues.Known(ctx, system, true)
	if err != nil {
		return out
	}
	for _, k := range list {
		if k.Status != issues.KnownResolved {
			out[k.ID] = true
		}
	}
	return out
}

// passOn makes a duplicate's waiting change resolve its original, linked
// by the system, and says so on both cases' history.
func (s *Service) passOn(ctx context.Context, c cards.Card, n, original int) {
	oid := caseID(original)
	if slices.Contains(c.Resolves, oid) {
		return
	}
	if _, err := s.o.Cards.Link(ctx, c.ID, oid, systemCard); err != nil {
		s.o.Logger.Warn("a duplicate's waiting change could not be passed to its original", "case", caseID(n), "card", c.ID, "err", err)
		return
	}
	now := s.now()
	s.note(ctx, original, Event{At: now, What: fmt.Sprintf("The change %s now resolves this case too: %s was closed as its duplicate", c.ID, caseID(n))})
	s.note(ctx, n, Event{At: now, What: fmt.Sprintf("The change %s waits on for %s, the case this one repeats", c.ID, oid)})
}

// settleLater settles a case's waiting changes in the background: what
// the issue service closed itself (a timer, its own API).
func (s *Service) settleLater(n int, by cards.Person) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), 30*time.Second)
		defer cancel()
		s.closed(ctx, n, by)
	}()
}

// answeredLater notes in a case's history that its reporter answered
// the question asked: it no longer waits for them (CF-67).
func (s *Service) answeredLater(n int, r issues.Report, by *issues.Person) {
	actor := issues.Actor{Name: "the reporter", Kind: issues.ActorPerson}
	if by != nil {
		actor.Name = firstOf(by.Name, by.ID, actor.Name)
	}
	at := s.now()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), 30*time.Second)
		defer cancel()
		s.note(ctx, n, Event{At: at, What: "The reporter answered, now " + shownStatus(r), By: actor})
	}()
}

// issueChanged hears the issue service's changes: a case it closed
// itself (a question unanswered, its API) settles its waiting changes
// too. The console's own closes settle where they close.
func (s *Service) issueChanged(e issues.Event) {
	if e.Answered && e.Report != nil && e.Report.Status.Open() {
		s.answeredLater(e.Report.Number, *e.Report, e.By)
		return
	}
	if e.Report == nil || e.Report.Status != issues.StatusClosed || (e.Kind != issues.EventReportStatus && e.Kind != issues.EventReportLinked) {
		return
	}
	by := systemCard
	if e.By != nil && !e.By.System {
		by = cards.Person{Key: e.By.ID, Name: e.By.Label()}
	}
	s.settleLater(e.Report.Number, by)
}

// --- end of settling.
