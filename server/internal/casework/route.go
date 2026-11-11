package casework

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- The runs' findings into cases (Phase 12, wave 6, CF-01 to CF-18) ---
//
// Each finding of a run becomes a case or joins one, by its key (cluster
// + object + check), against the agent-found cases before it:
//
//	earlier case with the key   the finding
//	New or In progress          joins it: seen ×N, last seen
//	closed as duplicate         a repeat of its original (end of chain)
//	closed as knownIssue        recorded on the known issue: still present
//	closed as exception         dropped while the exception is active
//	closed as fix, first run    the same case reopens: the fix did not hold
//	  of its job since the fix
//	closed as fix, later        a new case, a regression of it
//	closed as noAction          a new case with the earlier explanation
//	none                        a new agent-found case
//
// A run of the job whose finding opened a case closed as fix since
// verifies the fix only on positive evidence (W-1, CF-94): the case's
// object is inside the run's scope (each cluster an audit read whole,
// the objects of the alerts a diagnosis read; nothing for other kinds),
// and the agent, given the key to re-check, says the problem is gone.
// Re-checked and still there, or found again, the fix did not hold. In
// scope and not re-checked, the case keeps waiting and the run says how
// many (CF-95). A finding left out past the run's cap never counts as
// not seen (W-3). A resolved known issue the run's job saw is checked
// inside the scope, and reopens when still seen.
// A run opens at most newCasesPerRun cases; the rest are capped. Agent
// repeats never match a case a person opened (it has no check).
//
// An open case a change was applied to (CF-29) is checked the same way by
// the next run of its job that re-checks it (CF-30): gone, it closes as
// fixed by itself below P1, and at P0-P1 is marked verified and waits for
// its RCA; still there, the fix did not hold, and it stays In progress.
//
// Since wave 6c: a finding below its job's lowest priority (from its
// severity, PriorityOf) opens no case and joins none: it stays on the run
// as ignored (CF-33). A case a run opens takes its type from its check
// (CF-37) and names the job's agent as its creator (CF-35); when the case
// analysis job is on, each new case at P0-P2 (opened, a regression, or a
// fix that did not hold reopening it) is queued for an analysis, which
// the leader runs one at a time (CF-32).
//
// Since CF-82: a finding the agent marks expected (the system working as
// intended, with why) opens no case and joins none, whatever its
// severity: it stays on the run as expected.

// keyOf is a finding's key as the issue service writes it.
func keyOf(k *jobs.Key) issues.Key {
	return issues.Key{Cluster: k.Cluster, Object: k.Object, Check: k.Check}
}

// keyString is a key as the routing compares it: its check by the rule
// it routes by, so the ids a rule replaced and the rule's own are one key
// (ADR-138).
func keyString(k issues.Key, canon func(string) string) string {
	return k.Cluster + "\x00" + strings.ToLower(k.Object) + "\x00" + canon(k.Check)
}

// covering is the active exception that covers a key, if any: keyed by
// its check, or by an id the same rule replaced or was replaced by
// (ADR-138).
func covering(list []lessons.Exception, k issues.Key, now time.Time, canon func(string) string) *lessons.Exception {
	for i := range list {
		e := &list[i]
		if e.Active(now) && e.Cluster == k.Cluster && canon(e.Check) == canon(k.Check) && strings.EqualFold(e.Resource, k.Object) {
			return e
		}
	}
	return nil
}

// Route routes a run's findings into cases (CF-01). A finding whose key
// is not valid stays invalid. When the exceptions cannot be read, or the
// cases cannot be saved, nothing is routed: the findings stay as they
// came, and the result says why, so the run is routed again later (W-7,
// W-8, CF-99); an exception unread never lets a finding it covers open a
// case.
func (s *Service) Route(ctx context.Context, in jobs.RouteInput) jobs.RouteResult {
	now := s.now()
	set := s.settings(ctx)
	exceptions, err := s.o.Lessons.Exceptions(ctx)
	if err != nil {
		s.o.Logger.Warn("a run's findings were not routed: the exceptions could not be read", "run", in.Run, "err", err)
		return jobs.RouteResult{Findings: in.Findings, NotRouted: "the exceptions could not be read: " + clip(err.Error(), 300)}
	}
	// The validation rules as they stand: a finding under an id a rule
	// replaced routes by the rule (ADR-138).
	rules := s.rules.set(ctx)
	canon := rules.Canon
	var out jobs.RouteResult
	var hits map[string]int
	// fixed are the cases a change fixed that the run verified: closed, or
	// waiting for their RCA (CF-30), for the history and the audit log.
	var fixed []autoFix
	seenKeys := map[string]bool{}
	for _, f := range in.Findings {
		if f.Key != nil {
			seenKeys[keyString(keyOf(f.Key), canon)] = true
		}
	}
	// The keys of the findings left out past the cap: neither seen nor
	// not seen (W-3).
	leftOut := map[string]bool{}
	for i := range in.LeftOut {
		leftOut[keyString(keyOf(&in.LeftOut[i]), canon)] = true
	}
	// What the agent said of the fixes it re-checked, by key (W-1).
	rechecked := map[string]jobs.Recheck{}
	for _, x := range in.Rechecked {
		if x.Key != nil && x.Still != nil {
			rechecked[keyString(keyOf(x.Key), canon)] = x
		}
	}
	// The clusters whose new cases are queued for their analysis (CF-32).
	// A re-check that finds a fix did not hold reopens a case with no
	// finding of its own, so its cluster counts too.
	queue := map[string]bool{}
	if s.o.Jobs != nil {
		on := func(c string) {
			if _, ok := queue[c]; !ok && c != "" {
				queue[c] = s.o.Jobs.On(ctx, jobs.KindCase, jobs.Event{Kind: jobs.EventCase, Cluster: c})
			}
		}
		for _, f := range in.Findings {
			if f.Key != nil {
				on(f.Cluster)
			}
		}
		for _, x := range in.Rechecked {
			if x.Key != nil && x.Still != nil && *x.Still {
				on(x.Key.Cluster)
			}
		}
	}
	agent := issues.AgentActor(in.Agent)
	var queued []int
	// notRechecked counts the fixes in scope the agent did not re-check.
	var notRechecked int
	err = s.o.Issues.Batch(ctx, func(tx *issues.Tx) error {
		// Again from the start on a retry: the counts and the findings.
		out = jobs.RouteResult{Findings: append([]jobs.Finding(nil), in.Findings...)}
		for i := range out.Findings {
			// The key holds the id its rule routes by, so the cases it opens
			// are keyed by it (package jobs gives it so already).
			if f := &out.Findings[i]; f.Key != nil && canon(f.Key.Check) != f.Key.Check {
				k := *f.Key
				k.Check = canon(k.Check)
				f.Key = &k
			}
		}
		hits, fixed, queued, notRechecked = map[string]int{}, nil, nil, 0
		tx.As(agent)
		r := router{s: s, tx: tx, in: in, now: now, cap: set.NewCasesPerRun, byKey: map[string]int{}, agent: agent, queue: queue, queued: &queued, rules: rules}
		for _, l := range tx.Lines() {
			if !l.AgentFound || l.Key == nil {
				continue
			}
			k := keyString(*l.Key, canon)
			cur, ok := r.byKey[k]
			// The open one, or the newest closed.
			if !ok || l.Open() || !tx.Line(cur).Open() && l.Number > cur {
				r.byKey[k] = l.Number
			}
		}
		for i := range out.Findings {
			f := &out.Findings[i]
			if f.Routing == jobs.RoutedInvalid || f.Key == nil {
				f.Routing = jobs.RoutedInvalid
				out.Routed.Add(f.Routing)
				continue
			}
			k := keyOf(f.Key)
			if e := covering(exceptions, k, now, canon); e != nil {
				f.Routing, f.Exception = jobs.RoutedExcepted, e.ID
				hits[e.ID]++
			} else if f.Expected {
				// The system working as intended, the agent says with its
				// evidence: no case, none joined, whatever the severity
				// (CF-82).
				f.Routing = jobs.RoutedExpected
			} else if in.MinPriority != nil && PriorityOf(f.Severity) > *in.MinPriority {
				// Below the job's lowest priority: no case, none joined
				// (CF-33).
				f.Routing = jobs.RoutedIgnored
			} else if err := r.route(f, k); err != nil {
				return err
			}
			out.Routed.Add(f.Routing)
		}
		// The fixes its job's findings opened and people or changes fixed
		// since: verified when the run re-checked them inside its scope and
		// found them gone (CF-06, CF-94).
		if in.OK && in.Scope != nil {
			for _, l := range tx.Lines() {
				if !l.VerifyPending || l.Job != in.Job || l.Key == nil || !in.Scope.Has(l.Key.Cluster, l.Key.Object) {
					continue
				}
				k := keyString(*l.Key, canon)
				// Found again: routed above (reopened, or the fix did not
				// hold). Left out past the cap: nothing is concluded.
				if seenKeys[k] || leftOut[k] {
					continue
				}
				c, err := tx.Case(l.Number)
				if err != nil {
					return err
				}
				x, ok := rechecked[k]
				if !ok {
					// Not re-checked: it keeps waiting (CF-95).
					c.Unchecked++
					notRechecked++
					continue
				}
				tx.By(c, sys)
				if *x.Still {
					if err := r.stillThere(c, x); err != nil {
						return err
					}
					continue
				}
				c.VerifiedBy = &issues.VerifiedBy{Run: in.Run, Job: in.Label, At: now, Evidence: x.Evidence}
				if c.Status.Open() && c.AppliedBy != nil {
					f, err := verifyApplied(tx, c, in, now)
					if err != nil {
						return err
					}
					fixed = append(fixed, f)
				} else {
					c.VerifyPending, c.Verified, c.Updated = false, &now, now
					if err := tx.Note(c, system.Person, fmt.Sprintf("The fix is verified: %s (%s) re-checked it%s.", in.Run, in.Label, evidenceWords(x.Evidence)), true); err != nil {
						return err
					}
				}
				out.Verified = append(out.Verified, caseID(l.Number))
				out.Routed.Verified++
			}
		}
		// A resolved known issue the job saw: still seen reopens it, for
		// its owner (CF-06).
		for _, k := range tx.KnownIssues() {
			if k.Status != issues.KnownResolved || !slices.Contains(k.VerifyJobs, in.Job) {
				continue
			}
			// One of its keys seen again (or re-checked and still there)
			// reopens it; every key inside the run's scope, none seen and
			// none left out past the cap, the job is done with it.
			still, all, read := false, true, false
			for _, x := range k.Keys {
				ks := keyString(x.Key, canon)
				if seenKeys[ks] || rechecked[ks].Still != nil && *rechecked[ks].Still {
					still = true
				}
				if !in.Scope.Has(x.Cluster, x.Object) || leftOut[ks] {
					all = false
					continue
				}
				read = true
			}
			switch {
			case still:
				k.Status, k.Resolved, k.VerifyJobs, k.Pending, k.Updated = issues.KnownInvestigating, nil, nil, true, now
				k.Log(issues.Update{At: now, By: system.Person, Status: issues.KnownInvestigating, Text: fmt.Sprintf("Still seen by %s (%s) after it was resolved: reopened.", in.Run, in.Label)})
			case in.OK && read && all:
				k.VerifyJobs = slices.DeleteFunc(k.VerifyJobs, func(j string) bool { return j == in.Job })
			}
		}
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("a run's findings could not be routed into cases", "run", in.Run, "err", err)
		return jobs.RouteResult{Findings: in.Findings, NotRouted: "the cases could not be saved: " + clip(err.Error(), 300)}
	}
	out.NotRechecked = notRechecked
	if err := s.o.Lessons.Match(ctx, hits, now); err != nil {
		s.o.Logger.Warn("the exceptions' matches could not be counted", "run", in.Run, "err", err)
	}
	out.NewChecks = s.newChecks(ctx, out.Findings)
	s.rules.noteFound(ctx, in, out.Findings, now)
	s.noteFixed(ctx, fixed, in)
	r := out.Routed
	s.record(audit.Event{Action: ActionRoute, Who: audit.Who{Subject: "221b", Name: "job " + in.Job}, Target: in.Run, Outcome: audit.OutcomeOK, Cluster: oneOf(in.Clusters),
		Detail: fmt.Sprintf("%s (%s): %d findings: %d opened, %d seen, %d known, %d excepted, %d reopened, %d regressions, %d capped, %d invalid, %d ignored (lowest priority %s), %d expected; %d fixes verified [%s]; new checks [%s]; %d queued for their analysis [%s]",
			in.Run, in.Label, len(out.Findings), r.Opened, r.Seen, r.Known, r.Excepted, r.Reopened, r.Regression, r.Capped, r.Invalid, r.Ignored, lowestWords(in.MinPriority), r.Expected, r.Verified, strings.Join(out.Verified, ", "),
			strings.Join(out.NewChecks, ", "), len(queued), strings.Join(ids(queued), ", "))})
	return out
}

func oneOf(list []string) string {
	if len(list) == 1 {
		return list[0]
	}
	return ""
}

// router routes one run's findings in a Tx.
type router struct {
	s      *Service
	tx     *issues.Tx
	in     jobs.RouteInput
	now    time.Time
	cap    int
	opened int
	// byKey is the case each key goes to: the open one, or the newest
	// closed; the cases this run opens join it.
	byKey map[string]int
	// agent is the job's agent, who the cases it opens or finds again
	// name; queue the clusters whose new cases are queued for their
	// analysis, and queued the cases queued (CF-32, CF-35).
	agent  issues.Actor
	queue  map[string]bool
	queued *[]int
	// rules are the validation rules as they stand (ADR-138).
	rules *ruleSet
}

// lowestWords is a run's lowest priority, for the audit log.
func lowestWords(p *int) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("P%d", *p)
}

// enqueue queues a case new to the people who work the cases for its
// analysis (CF-32): at P0-P2, while the case analysis job is on for its
// cluster.
func (r *router) enqueue(c *issues.Case) {
	if c.Priority > 2 || !r.queue[c.Cluster] || !c.Status.Open() {
		return
	}
	at := r.now
	c.AnalysisQueued = &at
	*r.queued = append(*r.queued, c.Number)
}

func ids(list []int) []string {
	out := make([]string, len(list))
	for i, n := range list {
		out[i] = caseID(n)
	}
	return out
}

// route routes one finding by the row of its key's earlier case.
func (r *router) route(f *jobs.Finding, k issues.Key) error {
	n, ok := r.byKey[keyString(k, r.rules.Canon)]
	if !ok {
		return r.open(f, k, nil)
	}
	return r.by(f, k, n, 0)
}

// by applies the table's row of case n to a finding.
func (r *router) by(f *jobs.Finding, k issues.Key, n, depth int) error {
	l := r.tx.Line(n)
	if l == nil || depth > 50 {
		return r.open(f, k, nil)
	}
	if l.Open() {
		c, err := r.tx.Case(n)
		if err != nil {
			return err
		}
		// A change was applied to fix it, and the next run of its job
		// found it again: the fix did not hold; it stays open (CF-30).
		if c.VerifyPending && c.Job == r.in.Job && c.AppliedBy != nil {
			c.VerifyPending, c.Verified, c.Verification = false, nil, issues.VerifyNotHeld
			if err := r.tx.Note(c, system.Person, fmt.Sprintf("The fix did not hold: %s (%s) found it again after change %s.", r.in.Run, r.in.Label, c.AppliedBy.Card), true); err != nil {
				return err
			}
		} else if c.Verification == issues.VerifyVerified && c.Job == r.in.Job {
			// Verified at P0-P1 and waiting for its RCA, it is found again
			// before a person closed it: it no longer reads as verified.
			c.Verified, c.VerifiedBy, c.Verification = nil, nil, issues.VerifyNotHeld
			if err := r.tx.Note(c, system.Person, fmt.Sprintf("The fix did not hold: %s (%s) found it again after it was verified.", r.in.Run, r.in.Label), true); err != nil {
				return err
			}
		}
		if c.Seen == nil {
			c.Seen = &issues.Seen{Count: 1, First: c.Created}
		}
		c.Seen.Count++
		c.Seen.Last, c.Updated = r.now, r.now
		f.Routing, f.Case = jobs.RoutedSeen, caseID(n)
		return nil
	}
	switch l.Outcome {
	case issues.OutcomeDuplicate:
		if l.DuplicateOf != 0 {
			return r.by(f, k, l.DuplicateOf, depth+1)
		}
	case issues.OutcomeKnownIssue:
		if ki := r.tx.Known(l.KnownIssue); ki != nil {
			if ki.StillPresent == nil {
				ki.StillPresent = &issues.StillPresent{}
			}
			ki.StillPresent.Count++
			ki.StillPresent.Last = r.now
			ki.AddKey(k, r.in.Job)
			f.Routing, f.KnownIssue, f.Case = jobs.RoutedKnown, ki.ID, caseID(n)
			return nil
		}
	case issues.OutcomeFix:
		c, err := r.tx.Case(n)
		if err != nil {
			return err
		}
		if c.VerifyPending && c.Job == r.in.Job {
			issues.ReopenCase(c, r.now)
			// Open again, it says why: the fix did not hold (CF-107).
			c.Verification = issues.VerifyNotHeld
			if c.Seen == nil {
				c.Seen = &issues.Seen{Count: 1, First: c.Created}
			}
			c.Seen.Count++
			c.Seen.Last = r.now
			if err := r.tx.Note(c, system.Person, fmt.Sprintf("Reopened: the fix did not hold; %s (%s) found it again.", r.in.Run, r.in.Label), true); err != nil {
				return err
			}
			r.enqueue(c)
			f.Routing, f.Case = jobs.RoutedReopened, caseID(n)
			return nil
		}
		return r.open(f, k, c)
	case issues.OutcomeNoAction:
		c, err := r.tx.Case(n)
		if err != nil {
			return err
		}
		return r.open(f, k, c)
	}
	return r.open(f, k, nil)
}

// open opens a new agent-found case of a finding: a regression of an
// earlier case closed as fixed, or with the explanation of one closed as
// needing no action (CF-90); capped past the run's newCasesPerRun.
func (r *router) open(f *jobs.Finding, k issues.Key, earlier *issues.Case) error {
	if r.opened >= r.cap {
		f.Routing = jobs.RoutedCapped
		return nil
	}
	p := PriorityOf(f.Severity)
	detail := f.Detail
	if len(f.Evidence) > 0 {
		detail = strings.TrimSpace(detail + "\n\nEvidence:\n- " + strings.Join(f.Evidence, "\n- "))
	}
	o := issues.Opening{Source: SourceFinding, Title: f.Title, Text: detail, Cluster: k.Cluster, Object: k.Object, Priority: &p, PriorityBy: "severity",
		Key: &k, Job: r.in.Job, Run: r.in.Run, Finding: f.ID, Severity: f.Severity, Type: r.rules.Type(k.Check), CreatedBy: &r.agent}
	routing := jobs.RoutedOpened
	if earlier != nil {
		by := ""
		if earlier.ClosedBy != nil {
			by = earlier.ClosedBy.Label()
		}
		at := r.now
		if earlier.Closed != nil {
			at = *earlier.Closed
		}
		o.Previous = &issues.PreviousClose{Case: earlier.Number, Outcome: earlier.Outcome, Explanation: earlier.Explanation, By: by, At: at}
		if earlier.Outcome == issues.OutcomeFix {
			o.RegressionOf, routing = earlier.Number, jobs.RoutedRegression
		}
	}
	if earlier == nil {
		o.PossibleDuplicateOf = r.tx.SimilarOpen(f.Title, k.Cluster, k.Object, 0)
	}
	c, err := r.tx.Open(system, o)
	if err != nil {
		return err
	}
	if err := r.tx.Note(c, system.Person, fmt.Sprintf("Found by %s (%s), finding %s.", r.in.Label, r.in.Run, f.ID), true); err != nil {
		return err
	}
	r.opened++
	r.byKey[keyString(k, r.rules.Canon)] = c.Number
	r.enqueue(c)
	f.Routing, f.Case = routing, caseID(c.Number)
	return nil
}

// autoFix is an open case a change fixed that a run verified (CF-30):
// Closed when it closed as fixed by itself, the text it closed with.
type autoFix struct {
	Number  int
	Cluster string
	Closed  bool
	Text    string
}

// verifyApplied verifies the fix a change applied to an open case: below
// P1 the case closes as fixed by the console, with no RCA, its fix
// verified; at P0-P1 it is marked verified and stays In progress until a
// person gives the fix with its RCA (CF-30).
func verifyApplied(tx *issues.Tx, c *issues.Case, in jobs.RouteInput, now time.Time) (autoFix, error) {
	a := c.AppliedBy
	f := autoFix{Number: c.Number, Cluster: c.Cluster}
	if c.RCARequired() {
		c.VerifyPending, c.Verified, c.Verification, c.Updated = false, &now, issues.VerifyVerified, now
		f.Text = fmt.Sprintf("The fix is verified: %s (%s) re-checked it after change %s%s; add the RCA to close it.", in.Run, in.Label, a.Card, evidenceWords(verifiedEvidence(c)))
		return f, tx.Note(c, system.Person, f.Text, true)
	}
	f.Closed = true
	f.Text = fmt.Sprintf("Fixed by change %s (%s), applied by %s on %s; verified by %s (%s)", a.Card, a.Title, firstOf(a.By, "Watson"), a.At.UTC().Format("2006-01-02"), in.Run, in.Label)
	issues.CloseCase(c, system.Person, issues.OutcomeFix, f.Text, now)
	c.VerifyPending, c.Verified = false, &now
	return f, tx.Note(c, system.Person, f.Text+".", true)
}

// verifiedEvidence is what the run that verified a case's fix read.
func verifiedEvidence(c *issues.Case) string {
	if c.VerifiedBy == nil {
		return ""
	}
	return c.VerifiedBy.Evidence
}

// evidenceWords ends a note with what a run read, when it said.
func evidenceWords(evidence string) string {
	if evidence = strings.TrimSpace(evidence); evidence == "" {
		return ""
	}
	return ": " + strings.TrimRight(evidence, ".")
}

// stillThere is a waiting fix the run re-checked and found still there,
// with no finding for it (W-1): as when it is found again, a case closed
// as fixed reopens, and an open one a change was applied to keeps going,
// the fix did not hold (CF-06, CF-30).
func (r *router) stillThere(c *issues.Case, x jobs.Recheck) error {
	if c.Status.Open() {
		c.VerifyPending, c.Verified, c.Verification, c.Updated = false, nil, issues.VerifyNotHeld, r.now
		change := "it was fixed"
		if c.AppliedBy != nil {
			change = "change " + c.AppliedBy.Card
		}
		return r.tx.Note(c, system.Person, fmt.Sprintf("The fix did not hold: %s (%s) re-checked it after %s%s.", r.in.Run, r.in.Label, change, evidenceWords(x.Evidence)), true)
	}
	issues.ReopenCase(c, r.now)
	c.Verification = issues.VerifyNotHeld
	if err := r.tx.Note(c, system.Person, fmt.Sprintf("Reopened: the fix did not hold; %s (%s) re-checked it%s.", r.in.Run, r.in.Label, evidenceWords(x.Evidence)), true); err != nil {
		return err
	}
	r.enqueue(c)
	return nil
}

// noteFixed puts the cases a run verified after a change in their history
// and the audit log (CF-30): a case closed by itself as fixed by the
// system person, with its fix as the work's.
func (s *Service) noteFixed(ctx context.Context, fixed []autoFix, in jobs.RouteInput) {
	for _, f := range fixed {
		at := s.now()
		err := s.updateWork(ctx, f.Number, func(w *Work) error {
			what := f.Text
			if f.Closed {
				what = "Closed as fixed: " + f.Text
			}
			addEvent(w, Event{At: at, What: what, By: sys})
			w.Updated = at
			return nil
		})
		if err != nil {
			s.o.Logger.Warn("a verified fix could not be noted on its case's work", "case", caseID(f.Number), "err", err)
		}
		if !f.Closed {
			continue
		}
		s.closed(ctx, f.Number, systemCard)
		s.record(audit.Event{Action: ActionFix, Who: audit.Who{Subject: "221b", Name: "system"}, Cluster: f.Cluster, Target: caseID(f.Number), Outcome: audit.OutcomeOK,
			Detail: fmt.Sprintf("case %s closed as fixed by the system, no RCA, verified by %s (%s): %s", caseID(f.Number), in.Run, in.Label, f.Text)})
	}
}

// newChecks marks the findings whose check was never seen on their
// cluster, keeps them as seen, and returns them.
func (s *Service) newChecks(ctx context.Context, findings []jobs.Finding) []string {
	var out []string
	err := update(ctx, s.o.Backend, ChecksKey, func(rec *checksRecord) error {
		out = nil
		if rec.Clusters == nil {
			rec.Clusters = map[string][]string{}
		}
		changed := false
		for i := range findings {
			f := &findings[i]
			f.NewCheck = false
			if f.Key == nil {
				continue
			}
			seen := rec.Clusters[f.Key.Cluster]
			if slices.Contains(seen, f.Key.Check) {
				continue
			}
			rec.Clusters[f.Key.Cluster] = append(seen, f.Key.Check)
			f.NewCheck, changed = true, true
			if !slices.Contains(out, f.Key.Check) {
				out = append(out, f.Key.Check)
			}
		}
		if !changed {
			return errUnchanged
		}
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("the checks seen could not be kept", "err", err)
	}
	for i := range findings {
		if findings[i].Key != nil && slices.Contains(out, findings[i].Key.Check) {
			findings[i].NewCheck = true
		}
	}
	return out
}

// ChecksSeen are the check ids (validation rules) seen on each cluster,
// which Mycroft › Exceptions' New exception offers (build 14); none when
// they cannot be read.
func (s *Service) ChecksSeen(ctx context.Context) map[string][]string {
	var rec checksRecord
	if _, err := loadJSON(ctx, s.o.Backend, ChecksKey, &rec); err != nil {
		s.o.Logger.Warn("the checks seen could not be read", "err", err)
		return nil
	}
	return rec.Clusters
}

// KeysInUse are the keys on a cluster an agent's task lists (CF-10): the
// open agent-found cases, the cases whose fix waits for verification
// (closed as fixed, or a change applied), said as such so the job that
// found them re-checks them (W-4, CF-96), the known issues still present,
// and the active exceptions up to exceptionsPerAgent, the most recently
// matched first.
//
// A key under an id a rule replaced is listed under the rule (ADR-138),
// so the agent names it by the rule from then on.
//
// A case, a known issue or an exception that cannot be read is an error
// (W-7, R2-12): the caller says so, and does not take the list for all
// there is.
func (s *Service) KeysInUse(ctx context.Context, cluster string) ([]jobs.KeyInUse, error) {
	canon := s.rules.set(ctx).Canon
	var out []jobs.KeyInUse
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return nil, fmt.Errorf("the cases could not be read: %w", err)
	}
	for _, l := range lines {
		if !l.AgentFound || l.Key == nil || l.Key.Cluster != cluster || !l.Open() && !l.VerifyPending {
			continue
		}
		k := jobs.KeyInUse{Key: jobs.Key{Cluster: cluster, Object: l.Key.Object, Check: canon(l.Key.Check)}, What: "case " + caseID(l.Number), Title: l.Title, Job: l.Job}
		if l.VerifyPending {
			k.What, k.Recheck = "fix applied, re-check: "+caseID(l.Number), true
		}
		out = append(out, k)
	}
	known, err := s.o.Issues.Known(ctx, system, true)
	if err != nil {
		return nil, fmt.Errorf("the known issues could not be read: %w", err)
	}
	for _, k := range known {
		if k.StillPresent == nil || k.Draft {
			continue
		}
		for _, x := range k.Keys {
			if x.Cluster == cluster {
				out = append(out, jobs.KeyInUse{Key: jobs.Key{Cluster: cluster, Object: x.Object, Check: canon(x.Check)}, What: "known issue " + k.ID, Title: k.Title})
			}
		}
	}
	list, err := s.o.Lessons.Exceptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("the exceptions could not be read: %w", err)
	}
	now := s.now()
	var active []lessons.Exception
	for _, e := range list {
		if e.Cluster == cluster && e.Active(now) {
			active = append(active, e)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].LastMatched.After(active[j].LastMatched) })
	if max := s.settings(ctx).ExceptionsPerAgent; len(active) > max {
		active = active[:max]
	}
	for _, e := range active {
		out = append(out, jobs.KeyInUse{Key: jobs.Key{Cluster: cluster, Object: e.Resource, Check: canon(e.Check)}, What: "exception " + e.ID, Title: e.Title})
	}
	return out, nil
}

var _ jobs.Router = (*Service)(nil)
