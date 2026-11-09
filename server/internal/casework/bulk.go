package casework

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
)

// --- Many cases at once (Phase 12, wave 6c, CF-38) ---
//
// A person who works the cases selects cases in the Inbox and applies one
// action to all of them: assign, priority, type, incident number,
// analyse, or close with one outcome. Each case goes through the same
// code as its single action, with the same checks (an outcome a case
// does not allow, a duplicate refused), and the answer says, per case,
// whether it was done or why not. Each case is audited as usual, and the
// whole as cases.bulk.

// MaxBulk is how many cases one bulk action takes.
const MaxBulk = 100

// The bulk actions.
const (
	BulkAssign    = "assign"
	BulkPriority  = "priority"
	BulkType      = "type"
	BulkIncident  = "incident"
	BulkAnalyze   = "analyze"
	BulkClose     = "close"
	BulkFix       = "fix"
	BulkKnown     = "known"
	BulkDuplicate = "duplicate"
	BulkException = "exception"
)

// BulkInput is one action for many cases, with the single action's body:
// assignee (assign), priority, type, incident; outcome, explanation and
// original (close, duplicate); text and rca (fix); id or create (known:
// a known issue made once, then linked to the rest); reason and expires
// (exception).
type BulkInput struct {
	Cases       []string       `json:"cases"`
	Action      string         `json:"action"`
	Assignee    *issues.Person `json:"assignee,omitempty"`
	Priority    *int           `json:"priority,omitempty"`
	Type        *string        `json:"type,omitempty"`
	Incident    *string        `json:"incident,omitempty"`
	Outcome     issues.Outcome `json:"outcome,omitempty"`
	Explanation string         `json:"explanation,omitempty"`
	Original    string         `json:"original,omitempty"`
	Text        string         `json:"text,omitempty"`
	RCA         *Completion    `json:"rca,omitempty"`
	ID          string         `json:"id,omitempty"`
	Create      *NewKnown      `json:"create,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Expires     string         `json:"expires,omitempty"`
	// SaveFix saves one fix for every case fixed (CF-50); absent is on.
	SaveFix *bool `json:"saveFix,omitempty"`
}

// BulkResult is how the action went on one case: done, or refused with
// why; a duplicate refused says what fits instead.
type BulkResult struct {
	ID     string         `json:"id"`
	OK     bool           `json:"ok"`
	Error  string         `json:"error,omitempty"`
	Offers []issues.Offer `json:"offers,omitempty"`
}

// errText is an error as a person reads it, without the kind it is.
func errText(err error) string {
	msg := err.Error()
	for _, e := range []error{ErrInvalid, ErrNotFound, ErrDenied, ErrState, ErrOff, ErrFull, issues.ErrInvalid, issues.ErrNotFound, issues.ErrDenied} {
		msg = strings.TrimPrefix(msg, e.Error()+": ")
	}
	return upper(msg)
}

// Bulk applies one action to up to MaxBulk cases, each as its single
// action would (Analyze queues each for the case analysis job, which
// runs them one at a time), and says how it went on each.
func (s *Service) Bulk(ctx context.Context, in BulkInput, c Caller) ([]BulkResult, error) {
	var list []string
	seen := map[string]bool{}
	for _, id := range in.Cases {
		n, ok := number(id)
		key := strings.TrimSpace(id)
		if ok {
			key = caseID(n)
		}
		if !seen[key] {
			seen[key] = true
			list = append(list, key)
		}
	}
	switch {
	case len(list) == 0:
		return nil, fmt.Errorf("%w: select the cases to change", ErrInvalid)
	case len(list) > MaxBulk:
		return nil, fmt.Errorf("%w: at most %d cases at once; %d were selected", ErrInvalid, MaxBulk, len(list))
	}
	do, err := s.bulkAction(in, c)
	if err != nil {
		return nil, err
	}
	out := make([]BulkResult, 0, len(list))
	done := 0
	var fixed []string
	for _, id := range list {
		res := BulkResult{ID: id}
		if err := do(ctx, id); err != nil {
			res.Error = errText(err)
			var d *DuplicateError
			if errors.As(err, &d) {
				res.Error, res.Offers = "Not a duplicate: "+d.Check.Reason+".", d.Check.Offers
			}
		} else {
			res.OK = true
			done++
			fixed = append(fixed, id)
		}
		out = append(out, res)
	}
	if in.Action == BulkFix && in.RCA != nil && !in.RCA.Empty() && len(fixed) > 1 {
		s.closedTogether(ctx, fixed)
	}
	if in.Action == BulkFix && saving(in.SaveFix) && len(fixed) > 0 {
		var ns []int
		for _, id := range fixed {
			if n, ok := number(id); ok {
				ns = append(ns, n)
			}
		}
		s.saveFix(ctx, ns, clip(in.Text, MaxDetail), in.RCA, fixPerson(c))
	}
	s.record(audit.Event{Action: ActionBulk, Who: who(c.Person), Target: "cases", From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("%s on %d cases: %d done, %d refused [%s]", in.Action, len(list), done, len(list)-done, strings.Join(list, ", "))})
	return out, nil
}

// bulkAction is the single action a bulk action applies to each case.
func (s *Service) bulkAction(in BulkInput, c Caller) (func(ctx context.Context, id string) error, error) {
	change := func(ch Change) func(ctx context.Context, id string) error {
		return func(ctx context.Context, id string) error {
			_, err := s.Update(ctx, id, ch, c)
			return err
		}
	}
	switch in.Action {
	case BulkAssign:
		if in.Assignee == nil {
			return nil, fmt.Errorf("%w: say who to assign them to: {\"assignee\": {\"id\": ...}}, or an empty id for nobody", ErrInvalid)
		}
		return change(Change{Assignee: in.Assignee}), nil
	case BulkPriority:
		if in.Priority == nil || *in.Priority < issues.MinPriority || *in.Priority > issues.MaxPriority {
			return nil, fmt.Errorf("%w: a priority is 0 to 4", ErrInvalid)
		}
		return change(Change{Priority: in.Priority}), nil
	case BulkType:
		if in.Type == nil || !issues.ValidType(*in.Type) {
			return nil, fmt.Errorf("%w: a case's type is one of %s", ErrInvalid, strings.Join(issues.CaseTypes, ", "))
		}
		return change(Change{Type: in.Type}), nil
	case BulkIncident:
		if in.Incident == nil {
			return nil, fmt.Errorf("%w: give the incident number, or \"\" to clear it", ErrInvalid)
		}
		if _, err := issues.CleanIncident(*in.Incident); err != nil {
			return nil, s.fromIssues(err)
		}
		return change(Change{Incident: in.Incident}), nil
	case BulkAnalyze:
		// Queued, not started at once: the queue runs them one at a time
		// within the job's budgets (CF-32), where many at once would
		// overrun the job's turn.
		return func(ctx context.Context, id string) error {
			return s.Enqueue(ctx, id, c)
		}, nil
	case BulkClose, BulkDuplicate:
		ci := CloseInput{Outcome: in.Outcome, Explanation: in.Explanation, Original: in.Original}
		if in.Action == BulkDuplicate {
			ci.Outcome = issues.OutcomeDuplicate
		}
		return func(ctx context.Context, id string) error {
			_, err := s.Close(ctx, id, ci, c)
			return err
		}, nil
	case BulkFix:
		// One root cause, one RCA: each case fixed keeps it as its own
		// (CF-44), naming the others closed with it.
		// Their one fix is saved once all are fixed, not each case's.
		off := false
		fi := FixInput{Text: in.Text, RCA: in.RCA, SaveFix: &off}
		return func(ctx context.Context, id string) error {
			_, err := s.Fix(ctx, id, fi, c)
			return err
		}, nil
	case BulkKnown:
		link := KnownLink{ID: in.ID, Create: in.Create}
		return func(ctx context.Context, id string) error {
			v, err := s.Known(ctx, id, link, c)
			if err == nil && link.Create != nil {
				// The known issue is made once; the rest link it.
				link = KnownLink{ID: v.KnownIssue}
			}
			return err
		}, nil
	case BulkException:
		ei := ExceptionInput{Reason: in.Reason, Expires: in.Expires}
		return func(ctx context.Context, id string) error {
			_, err := s.Exception(ctx, id, ei, c)
			return err
		}, nil
	}
	return nil, fmt.Errorf("%w: the action is one of assign, priority, type, incident, analyze, close, fix, known, duplicate or exception", ErrInvalid)
}

// closedTogether names, on the RCA of each case fixed at once with one
// RCA, the other cases closed with it (CF-44).
func (s *Service) closedTogether(ctx context.Context, ids []string) {
	for _, id := range ids {
		n, ok := number(id)
		if !ok {
			continue
		}
		others := make([]string, 0, len(ids)-1)
		for _, o := range ids {
			if o != id {
				others = append(others, o)
			}
		}
		if err := s.updateWork(ctx, n, func(w *Work) error {
			if w.RCA != nil {
				w.RCA.Cases = others
			}
			return nil
		}); err != nil {
			s.o.Logger.Warn("the cases closed with one RCA could not be named on it", "case", id, "err", err)
		}
	}
}

// --- Resolves shows titles (wave 6c, CF-39) ---

// Titles are the titles of cases ("C-12") and known issues ("KI-3"), for
// what a proposed change resolves; an id not there is left out.
func (s *Service) Titles(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	want := map[int]string{}
	ki := false
	for _, id := range ids {
		if strings.HasPrefix(id, "KI-") {
			ki = true
		} else if n, ok := number(id); ok {
			want[n] = id
		}
	}
	if len(want) > 0 {
		if lines, err := s.o.Issues.Index(ctx); err == nil {
			for _, l := range lines {
				if id, ok := want[l.Number]; ok {
					out[id] = l.Title
				}
			}
		}
	}
	if ki {
		if list, err := s.o.Issues.Known(ctx, system, true); err == nil {
			for _, k := range list {
				for _, id := range ids {
					if id == k.ID {
						out[id] = k.Title
					}
				}
			}
		}
	}
	return out
}

// --- end of many cases at once.
