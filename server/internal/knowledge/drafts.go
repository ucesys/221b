package knowledge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/ucesys/221b/server/internal/audit"
)

// Reviewed before published (ADR-95): each run's page changes wait as one
// item in AIOps › Inbox, with every page's diff. A page needs one person's
// approval, a critical one two different people's; an agent never counts.
// Approved pages are published to the store (merged in git, copied with a
// conditional write in S3); a page someone changed meanwhile is not
// overwritten and waits for a person. Agents read only approved pages.

// States of a run and of its pages.
const (
	StateWaiting   = "waiting"
	StatePartial   = "partial"
	StateDone      = "done"
	StateRejected  = "rejected"
	StatePublished = "published"
	StateConflict  = "conflict"
	// StateHeld is a change to a page people retired (KN-24): kept in
	// its run to be seen, never published.
	StateHeld = "held"
)

// heldReason says why a change to a retired page waits for a person.
const heldReason = "The page is retired, so Mycroft does not rewrite it. Make it active again (a second person approves) and sync again to have it rewritten."

// Run is one sync's page changes.
type Run struct {
	ID      string    `json:"id"`
	Started time.Time `json:"started"`
	By      string    `json:"by"`
	// StartedBy is the person who started it (Sync now, an upload), if
	// one did.
	StartedBy string      `json:"startedBy,omitempty"`
	State     string      `json:"state"`
	Store     string      `json:"store"`
	Pages     []DraftPage `json:"pages"`
	Reported  []string    `json:"reported,omitempty"`
	Decisions []Decision  `json:"decisions,omitempty"`
	Decided   time.Time   `json:"decided,omitzero"`
}

// DraftPage is one page's change in a run.
type DraftPage struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Critical bool   `json:"critical,omitempty"`
	New      bool   `json:"new,omitempty"`
	// Base is the page's version in the store when Mycroft read it.
	Base    string      `json:"base,omitempty"`
	Diff    string      `json:"diff"`
	Added   int         `json:"added"`
	Removed int         `json:"removed"`
	Clipped bool        `json:"clipped,omitempty"`
	Sources []SourceRef `json:"sources,omitempty"`
	State   string      `json:"state"`
	// Approvals are the people who approved it, in order.
	Approvals []Approval `json:"approvals,omitempty"`
	// Reason says why a page waits for a person after all.
	Reason string `json:"reason,omitempty"`
	// Contradicts are the active fixes the page says otherwise than,
	// flagged for its reviewer (KN-33).
	Contradicts []Contradiction `json:"contradicts,omitempty"`
}

// Approval is one person's approval.
type Approval struct {
	By   string    `json:"by"`
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// Decision is one person's approval or rejection of a run.
type Decision struct {
	By      string    `json:"by"`
	Name    string    `json:"name"`
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	Comment string    `json:"comment,omitempty"`
	Pages   []string  `json:"pages,omitempty"`
}

// Needed is how many people a page needs.
func (p DraftPage) Needed() int {
	if p.Critical {
		return 2
	}
	return 1
}

type draftsDoc struct {
	Runs []Run `json:"runs"`
	// Requests are the requests to retire a page or make it active
	// again (KN-24), and Corrections the corrections sent with This is
	// outdated… (KN-23).
	Requests    []PageRequest `json:"requests,omitempty"`
	Corrections []Correction  `json:"corrections,omitempty"`
}

// keptRuns is how many decided runs stay listed.
const keptRuns = 30

// maxDiff bounds one page's diff.
const maxDiff = 16 << 10

// diff is a unified diff of a page, its lines added and removed, and
// whether it was cut.
func diff(path, before, after string) (string, int, int, bool) {
	u := difflib.UnifiedDiff{A: difflib.SplitLines(before), B: difflib.SplitLines(after), FromFile: path, ToFile: path, Context: 3}
	text, _ := difflib.GetUnifiedDiffString(u)
	added, removed := 0, 0
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	cut := len(text) > maxDiff
	if cut {
		text = clip(text, maxDiff)
	}
	return text, added, removed, cut
}

func (r *Run) open() bool { return r.State == StateWaiting || r.State == StatePartial }

// fitDrafts makes room: decided runs lose their diffs first, then go,
// oldest first.
func fitDrafts(d *draftsDoc) bool {
	for i := range d.Runs {
		r := &d.Runs[i]
		if r.open() {
			continue
		}
		for j := range r.Pages {
			if r.Pages[j].Diff != "" {
				for k := range r.Pages {
					r.Pages[k].Diff = ""
				}
				return true
			}
		}
	}
	for i := range d.Runs {
		if !d.Runs[i].open() {
			d.Runs = slices.Delete(d.Runs, i, i+1)
			return true
		}
	}
	for i := range d.Requests {
		if d.Requests[i].State != RequestWaiting {
			d.Requests = slices.Delete(d.Requests, i, i+1)
			return true
		}
	}
	if len(d.Corrections) > 0 {
		d.Corrections = d.Corrections[1:]
		return true
	}
	return false
}

func (s *Service) editDrafts(ctx context.Context, fn func(*draftsDoc) error) (draftsDoc, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return update(ctx, s.kv, DraftsKey, s.limits.DraftsBytes, fitDrafts, func(d *draftsDoc) error {
		if err := fn(d); err != nil {
			return err
		}
		decided := 0
		for i := len(d.Runs) - 1; i >= 0; i-- {
			if !d.Runs[i].open() {
				if decided++; decided > keptRuns {
					d.Runs = slices.Delete(d.Runs, i, i+1)
				}
			}
		}
		decided = 0
		for i := len(d.Requests) - 1; i >= 0; i-- {
			if d.Requests[i].State != RequestWaiting {
				if decided++; decided > keptRuns {
					d.Requests = slices.Delete(d.Requests, i, i+1)
				}
			}
		}
		if len(d.Corrections) > keptCorrections {
			d.Corrections = d.Corrections[len(d.Corrections)-keptCorrections:]
		}
		return nil
	})
}

// saveRun lists a new run for review.
func (s *Service) saveRun(ctx context.Context, r Run) error {
	_, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		if slices.ContainsFunc(d.Runs, func(x Run) bool { return x.ID == r.ID }) {
			return fmt.Errorf("there is a run %s already", r.ID)
		}
		d.Runs = append(d.Runs, r)
		return nil
	})
	return err
}

// Runs lists the runs, those waiting first, newest first; only the open
// ones unless all.
func (s *Service) Runs(ctx context.Context, all bool) ([]Run, error) {
	var d draftsDoc
	if _, err := load(ctx, s.kv, DraftsKey, &d); err != nil {
		return nil, err
	}
	var open, decided []Run
	for i := len(d.Runs) - 1; i >= 0; i-- {
		if d.Runs[i].open() {
			open = append(open, d.Runs[i])
		} else if all {
			decided = append(decided, d.Runs[i])
		}
	}
	return append(open, decided...), nil
}

// RunByID is one run.
func (s *Service) RunByID(ctx context.Context, id string) (Run, error) {
	runs, err := s.Runs(ctx, true)
	if err != nil {
		return Run{}, err
	}
	for _, r := range runs {
		if r.ID == id {
			return r, nil
		}
	}
	return Run{}, wrapf(ErrNotFound, "no run %s", id)
}

// CanApprove says whether a person has something left to approve in a
// run: a page waiting that they have not approved.
func (r Run) CanApprove(person string) bool {
	if !r.open() {
		return false
	}
	for _, p := range r.Pages {
		if p.State == StateWaiting && !slices.ContainsFunc(p.Approvals, func(a Approval) bool { return a.By == person }) {
			return true
		}
	}
	return false
}

// Waiting counts the runs a reviewer can act on, for the Inbox's badge.
func (s *Service) Waiting(ctx context.Context, c Caller) (int, error) {
	if !c.Reviewer {
		return 0, nil
	}
	runs, err := s.Runs(ctx, false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.CanApprove(c.ID) {
			n++
		}
	}
	// And the requests to retire a page or make it active again that
	// someone else asked for (KN-24).
	reqs, err := s.PageRequests(ctx, false)
	if err != nil {
		return 0, err
	}
	for _, r := range reqs {
		if r.CanApprove(c.ID) {
			n++
		}
	}
	return n, nil
}

// Approve is one person's approval of a run's waiting pages: each page
// they have not approved gets it, and those with as many as they need are
// published. A critical page approved once waits for a second person.
func (s *Service) Approve(ctx context.Context, c Caller, id string) (Run, error) {
	if !c.Reviewer {
		return Run{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin approves the wiki's changes")
	}
	if err := s.leads(); err != nil {
		return Run{}, err
	}
	store, _, err := s.currentStore(ctx)
	if err != nil {
		return Run{}, err
	}
	now := s.o.Now().UTC()
	var ready []Change
	var readyPaths []string
	var run Run
	_, err = s.editDrafts(ctx, func(d *draftsDoc) error {
		ready, readyPaths = nil, nil
		i := slices.IndexFunc(d.Runs, func(r Run) bool { return r.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no run %s", id)
		}
		r := &d.Runs[i]
		if !r.open() {
			return invalid("run %s was decided already", id)
		}
		if r.Store != store.Kind() {
			return invalid("run %s was staged in the %s store, not the one the wiki is kept in now; reject it and sync again", id, r.Store)
		}
		gained, heldNow := false, false
		s.mu.RLock()
		retired := s.manifest.Retired
		s.mu.RUnlock()
		for j := range r.Pages {
			p := &r.Pages[j]
			if p.State != StateWaiting {
				continue
			}
			if _, gone := retired[p.Path]; gone {
				// Retired since Mycroft wrote it (KN-24): held, not
				// published.
				p.State, p.Reason = StateHeld, heldReason
				heldNow = true
				continue
			}
			if !slices.ContainsFunc(p.Approvals, func(a Approval) bool { return a.By == c.ID }) {
				p.Approvals = append(p.Approvals, Approval{By: c.ID, Name: c.label(), At: now})
				gained = true
			}
			// Approved enough: published now, or again after a publish
			// that failed.
			if len(p.Approvals) >= p.Needed() {
				ready = append(ready, Change{Path: p.Path, Base: p.Base})
				readyPaths = append(readyPaths, p.Path)
			}
		}
		if !gained && len(ready) == 0 && !heldNow {
			return wrapf(ErrDenied, "you approved this already: a critical page needs a second person")
		}
		r.Decisions = append(r.Decisions, Decision{By: c.ID, Name: c.label(), At: now, Action: "approve", Pages: readyPaths})
		run = *r
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	var conflicts []string
	if len(ready) > 0 {
		conflicts, err = store.Publish(ctx, id, ready, fmt.Sprintf("Mycroft's run %s: %d pages approved by %s", id, len(ready), c.label()))
		if err != nil {
			// The approvals stay; the pages wait to be published at the
			// next approval or a retry.
			s.audit(audit.Event{Action: ActionReview, Who: c.who(), Target: id, Outcome: audit.OutcomeFailed, Detail: "publishing the approved pages failed: " + err.Error(), From: c.From})
			return run, fmt.Errorf("the approval is kept, but publishing failed: %w", err)
		}
	}
	run, err = s.settle(ctx, id, readyPaths, conflicts)
	if err != nil {
		return run, err
	}
	if !run.open() {
		if err := store.Discard(ctx, id); err != nil {
			s.o.Logger.Warn("a decided run's staged pages stay in the store", "run", id, "err", err)
		}
	}
	if err := s.Refresh(ctx); err != nil {
		s.o.Logger.Warn("reading the wiki after an approval failed", "err", err)
	}
	if s.o.Publisher != nil && len(readyPaths) > len(conflicts) {
		s.o.Publisher.EnqueueWiki(published(readyPaths, conflicts)...)
	}
	detail := fmt.Sprintf("run %s approved by %s: %d pages published", id, c.label(), len(readyPaths)-len(conflicts))
	if len(conflicts) > 0 {
		detail += fmt.Sprintf(", %d changed in the store meanwhile and wait for a person (%s)", len(conflicts), strings.Join(conflicts, ", "))
	}
	if run.open() {
		detail += "; critical pages wait for a second person"
	}
	s.audit(audit.Event{Action: ActionReview, Who: c.who(), Target: id, Outcome: audit.OutcomeOK, Detail: detail, From: c.From})
	return run, nil
}

func published(paths, conflicts []string) []string {
	var out []string
	for _, p := range paths {
		if !slices.Contains(conflicts, p) {
			out = append(out, p)
		}
	}
	return out
}

// settle records what publishing did: pages published, or in conflict.
func (s *Service) settle(ctx context.Context, id string, paths, conflicts []string) (Run, error) {
	var run Run
	_, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		i := slices.IndexFunc(d.Runs, func(r Run) bool { return r.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no run %s", id)
		}
		r := &d.Runs[i]
		waiting := false
		for j := range r.Pages {
			p := &r.Pages[j]
			switch {
			case slices.Contains(conflicts, p.Path):
				p.State, p.Reason = StateConflict, "The page changed in the store after Mycroft read it, so it was not overwritten. Compare the two, edit the page where it is kept, and sync again."
			case slices.Contains(paths, p.Path):
				p.State = StatePublished
			case p.State == StateWaiting:
				waiting = true
			}
		}
		if waiting {
			r.State = StatePartial
		} else {
			r.State, r.Decided = StateDone, s.o.Now().UTC()
		}
		run = *r
		return nil
	})
	return run, err
}

// Reject rejects a run's waiting pages: none is published, and its staged
// changes are discarded.
func (s *Service) Reject(ctx context.Context, c Caller, id, comment string) (Run, error) {
	if !c.Reviewer {
		return Run{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin rejects the wiki's changes")
	}
	if err := s.leads(); err != nil {
		return Run{}, err
	}
	comment = strings.TrimSpace(comment)
	if len(comment) > 500 {
		return Run{}, invalid("a reason is at most 500 characters")
	}
	now := s.o.Now().UTC()
	var run Run
	_, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		i := slices.IndexFunc(d.Runs, func(r Run) bool { return r.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no run %s", id)
		}
		r := &d.Runs[i]
		if !r.open() {
			return invalid("run %s was decided already", id)
		}
		for j := range r.Pages {
			if r.Pages[j].State == StateWaiting {
				r.Pages[j].State = StateRejected
			}
		}
		r.State, r.Decided = StateRejected, now
		r.Decisions = append(r.Decisions, Decision{By: c.ID, Name: c.label(), At: now, Action: "reject", Comment: comment})
		run = *r
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	if store, _, err := s.currentStore(ctx); err == nil {
		if err := store.Discard(ctx, id); err != nil {
			s.o.Logger.Warn("a rejected run's staged pages stay in the store", "run", id, "err", err)
		}
	}
	s.audit(audit.Event{Action: ActionReview, Who: c.who(), Target: id, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("run %s rejected by %s: %d pages not published", id, c.label(), len(run.Pages)), From: c.From})
	return run, nil
}
