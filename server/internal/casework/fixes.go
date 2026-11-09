package casework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- Fixes (Phase 12, wave 6g, CF-50 to CF-55) ---
//
// A case closed as Fixed with Save fix on (the default) saves its fix in
// Cases › Mycroft › Fixes: the problem (the case's title, the agents'
// check, the object), the cause and the prevention of its RCA, what was
// done and the changes run from its drawer that finished done. The case
// names the fix it was saved as; cases fixed together save one fix.

// fixPerson is a caller as package fixes names them.
func fixPerson(c Caller) fixes.Person {
	return fixes.Person{Subject: c.Subject, Email: c.Email, Name: c.Name}
}

// saveFix saves the fix of cases closed as Fixed together, the first one
// naming the problem; the cases are told the fix. A failure is logged:
// the cases are closed already.
func (s *Service) saveFix(ctx context.Context, ns []int, text string, rca *Completion, by fixes.Person) {
	if s.o.Fixes == nil || len(ns) == 0 {
		return
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		s.o.Logger.Warn("a case's fix could not be saved", "case", caseID(ns[0]), "err", err)
		return
	}
	in, ok := s.fromCases(ctx, lines, ns, text, rca)
	if !ok {
		return
	}
	saved, err := s.o.Fixes.SaveFromCase(ctx, in, by)
	if err != nil {
		s.o.Logger.Warn("a case's fix could not be saved", "case", caseID(ns[0]), "err", err)
		return
	}
	s.noteFix(ctx, ns, saved)
}

// fromCases is what a fix is saved from: the first case's problem, the
// RCA given (or the first case's), what was done and every case's changes
// that finished done.
func (s *Service) fromCases(ctx context.Context, lines []issues.Line, ns []int, text string, rca *Completion) (fixes.FromCase, bool) {
	var first *issues.Line
	for i := range lines {
		if lines[i].Number == ns[0] {
			first = &lines[i]
			break
		}
	}
	if first == nil {
		return fixes.FromCase{}, false
	}
	in := fixes.FromCase{Title: firstOf(first.Title, caseID(first.Number)), Cluster: first.Cluster, Resource: first.Object, Steps: text}
	if first.Key != nil {
		in.Key = first.Key.Check
	}
	if first.ClosedAt != nil {
		in.At = *first.ClosedAt
	}
	for _, n := range ns {
		in.Cases = append(in.Cases, caseID(n))
		w, _, err := s.loadWork(ctx, n)
		if err != nil {
			continue
		}
		for _, l := range fixLines(w.Commands) {
			if !slices.Contains(in.Commands, l) {
				in.Commands = append(in.Commands, l)
			}
		}
		if rca == nil && w.RCA != nil {
			rca = &Completion{Cause: w.RCA.Cause, Prevention: w.RCA.Prevention}
		}
		if in.Steps == "" && w.FixApplied != nil {
			in.Steps = w.FixApplied.Text
		}
		if in.Symptoms == "" && w.Analysis != nil {
			in.Symptoms = w.Analysis.Summary
		}
	}
	if rca != nil {
		in.Cause, in.Prevention = rca.Cause, rca.Prevention
	}
	return in, in.Steps != "" || len(in.Commands) > 0
}

// noteFix tells the cases the fix they were saved as.
func (s *Service) noteFix(ctx context.Context, ns []int, saved fixes.Saved) {
	what := "Its fix was saved as " + saved.Fix.ID
	switch {
	case saved.Added:
		what = "Added to " + saved.Fix.ID + ", a fix that says the same"
	case len(saved.Contradicts) > 0:
		what += ", pending: it contradicts " + strings.Join(saved.Contradicts, ", ")
	}
	now := s.now()
	for _, n := range ns {
		err := s.updateWork(ctx, n, func(w *Work) error {
			w.Fix, w.Updated = saved.Fix.ID, now
			addEvent(w, Event{At: now, What: what, By: issues.Actor{Name: "221b", Kind: issues.ActorSystem}})
			return nil
		})
		if err != nil {
			s.o.Logger.Warn("a case could not be told its fix", "case", caseID(n), "fix", saved.Fix.ID, "err", err)
		}
	}
}

// ---------- the fixes there were before ----------

// FixesImportKey marks the one import of the fixes of the cases closed as
// Fixed before wave 6g.
const FixesImportKey = "fixes-import.json"

type importMarker struct {
	At    time.Time `json:"at"`
	Cases int       `json:"cases"`
	Fixes []string  `json:"fixes"`
}

// ImportFixes saves, once, the fixes of the cases closed as Fixed before
// fixes were kept (CF-54), oldest first, the cases fixed together as one;
// a case already naming its fix is left. It returns how many cases were
// read; 0 when it ran before.
func (s *Service) ImportFixes(ctx context.Context) (int, error) {
	if s.o.Fixes == nil {
		return 0, nil
	}
	var m importMarker
	raw, _, err := s.o.Backend.LoadKey(ctx, FixesImportKey)
	switch {
	case err == nil && len(raw) > 2:
		return 0, nil
	case err != nil && !isNotFound(err):
		return 0, err
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return 0, err
	}
	var done []issues.Line
	for _, l := range lines {
		if l.Outcome == issues.OutcomeFix && !l.Open() {
			done = append(done, l)
		}
	}
	slices.SortStableFunc(done, func(a, b issues.Line) int { return closedAt(a).Compare(closedAt(b)) })
	taken := map[int]bool{}
	for _, l := range done {
		if taken[l.Number] {
			continue
		}
		w, _, err := s.loadWork(ctx, l.Number)
		if err != nil || w.Fix != "" {
			continue
		}
		ns := []int{l.Number}
		if w.RCA != nil {
			for _, id := range w.RCA.Cases {
				var n int
				if _, err := fmt.Sscanf(id, "C-%d", &n); err == nil && n != l.Number {
					ns = append(ns, n)
				}
			}
		}
		for _, n := range ns {
			taken[n] = true
		}
		by := fixes.Person{Name: "221b"}
		if w.FixApplied != nil && w.FixApplied.By != "" {
			by = fixes.Person{Name: w.FixApplied.By}
		}
		in, ok := s.fromCases(ctx, lines, ns, "", nil)
		if !ok {
			continue
		}
		saved, err := s.o.Fixes.SaveFromCase(ctx, in, by)
		if err != nil {
			s.o.Logger.Warn("an earlier case's fix could not be saved", "case", caseID(l.Number), "err", err)
			continue
		}
		s.noteFix(ctx, ns, saved)
		m.Cases += len(ns)
		if !slices.Contains(m.Fixes, saved.Fix.ID) {
			m.Fixes = append(m.Fixes, saved.Fix.ID)
		}
	}
	m.At = s.now()
	b, _ := json.Marshal(m)
	if _, err := s.o.Backend.SaveKey(ctx, FixesImportKey, b, ""); err != nil {
		return m.Cases, err
	}
	s.o.Logger.Info("the fixes of the cases closed as fixed before were saved", "cases", m.Cases, "fixes", len(m.Fixes))
	return m.Cases, nil
}

func isNotFound(err error) bool { return err != nil && errors.Is(err, kv.ErrNotFound) }
