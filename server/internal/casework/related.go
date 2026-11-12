package casework

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
)

// --- What the analysis starts from (Phase 12, wave 6e, CF-43) ---
//
// A case's analysis is given the records the console holds that may bear
// on it, found the way New Case finds them (CF-12), with no agent and no
// search by the agent: the earlier cases with the same key (the cluster,
// the object and the check), however long ago within the cases kept; the
// closed cases on the same object or with a title alike, within the
// look-back; the open cases like it, which may be the same problem; and
// the known issues it matches; since wave 6g, the active fixes of its
// problem (CF-52), which stay after their cases go. Each closed case comes
// with how it was closed and its RCA, which is the case's (CF-44).

// What the analysis is given at most of each.
const (
	relatedSameKey = 5
	relatedAlike   = 5
	relatedOpen    = 3
	relatedKnown   = 3
	relatedFixes   = 3
)

// related is the records that may bear on a case, as the agent reads
// them; "" when there are none.
func (s *Service) related(ctx context.Context, c caseInfo) string {
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return ""
	}
	var self *issues.Line
	for i := range lines {
		if lines[i].Number == c.Number {
			self = &lines[i]
			break
		}
	}
	seen := map[int]bool{c.Number: true}
	var same []issues.Line
	if self != nil && self.Key != nil {
		for _, l := range lines {
			if !l.Open() && l.Key != nil && *l.Key == *self.Key && !seen[l.Number] {
				same = append(same, l)
			}
		}
		sort.SliceStable(same, func(i, j int) bool { return closedAt(same[i]).After(closedAt(same[j])) })
		if len(same) > relatedSameKey {
			same = same[:relatedSameKey]
		}
		for _, l := range same {
			seen[l.Number] = true
		}
	}
	text := strings.TrimSpace(c.Title + " " + c.Detail)
	var alike, open []issues.Line
	if scored, err := s.o.Issues.Similar(ctx, text, c.Cluster, normObject(c.Resource), c.Number); err == nil {
		back := s.now().AddDate(0, 0, -s.settings(ctx).LookBackDays)
		for _, x := range scored {
			switch {
			case seen[x.Number]:
			case x.Open() && len(open) < relatedOpen:
				open = append(open, x.Line)
			case !x.Open() && x.ClosedAt != nil && x.ClosedAt.After(back) && len(alike) < relatedAlike:
				alike = append(alike, x.Line)
			}
		}
	}
	var known []string
	if text != "" {
		if m, err := s.o.Issues.MatchText(ctx, text); err == nil {
			for _, cand := range m.Known {
				if cand.Confidence < knownScore || len(known) >= relatedKnown {
					continue
				}
				k, err := s.o.Issues.KnownIssue(ctx, system, cand.ID)
				if err != nil {
					continue
				}
				line := fmt.Sprintf("- %s (%s, match %d%%): %s", k.ID, k.Status, cand.Confidence, clip(k.Title, 200))
				if k.Workaround != "" {
					line += "\n  workaround: " + clip(k.Workaround, 300)
				}
				if k.Expected != "" {
					line += "\n  fix expected: " + clip(k.Expected, 200)
				}
				known = append(known, line)
			}
		}
	}
	var parts []string
	if len(same) > 0 {
		parts = append(parts, "Earlier cases with the same check on the same object:")
		parts = append(parts, s.closedLines(ctx, same)...)
	}
	if len(alike) > 0 {
		parts = append(parts, "Cases closed lately on the same object or with a title like it:")
		parts = append(parts, s.closedLines(ctx, alike)...)
	}
	if len(open) > 0 {
		parts = append(parts, "Open cases like it, which may be the same problem:")
		for _, l := range open {
			parts = append(parts, fmt.Sprintf("- %s (P%d, %s): %s", caseID(l.Number), l.Priority, l.Status, clip(l.Title, 200)))
		}
	}
	if len(known) > 0 {
		parts = append(parts, "Known issues it matches:")
		parts = append(parts, known...)
	}
	key := ""
	if self != nil && self.Key != nil {
		key = self.Key.Check
	}
	if fx := s.fixesFor(ctx, key, text); len(fx) > 0 {
		parts = append(parts, "Fixes of the problem, saved from earlier cases:")
		parts = append(parts, fx...)
	}
	return strings.Join(parts, "\n")
}

// fixesFor are the active fixes of a case's problem, as the agent reads
// them: those of the agents' check first, then those its text matches.
func (s *Service) fixesFor(ctx context.Context, key, text string) []string {
	if s.o.Fixes == nil {
		return nil
	}
	picked, _ := s.o.Fixes.For(ctx, key, text, relatedFixes)
	var out []string
	for _, f := range picked {
		line := fmt.Sprintf("- %s, updated %s, fixed %d case(s): %s", f.ID, f.Updated.UTC().Format("2006-01-02"), len(f.Cases), clip(f.Title, 200))
		if f.Cause != "" {
			line += "\n  cause: " + clip(f.Cause, 400)
		}
		if f.Steps != "" {
			line += "\n  what fixed it: " + clip(f.Steps, 400)
		}
		if len(f.Commands) > 0 {
			line += "\n  changes that worked: " + clip(strings.Join(f.Commands, "; "), 600)
		}
		if f.Prevention != "" {
			line += "\n  prevention: " + clip(f.Prevention, 300)
		}
		out = append(out, line)
	}
	return out
}

// closedLines are closed cases as the agent reads them: how each was
// closed, its RCA and, since wave 6f, the commands that fixed it.
func (s *Service) closedLines(ctx context.Context, list []issues.Line) []string {
	var out []string
	for _, l := range list {
		line := fmt.Sprintf("- %s, closed %s as %s: %s", caseID(l.Number), closedAt(l).UTC().Format("2006-01-02"), closedAs(l), clip(l.Title, 200))
		if l.Explanation != "" {
			label := "explanation"
			if l.Outcome == issues.OutcomeFix {
				label = "what was done"
			}
			line += "\n  " + label + ": " + clip(l.Explanation, 300)
		}
		if w, _, err := s.loadWork(ctx, l.Number); err == nil && l.Outcome == issues.OutcomeFix {
			if w.RCA != nil && w.RCA.Cause != "" {
				line += "\n  RCA cause: " + clip(w.RCA.Cause, 400)
			}
			if w.RCA != nil && w.RCA.Prevention != "" {
				line += "\n  RCA prevention: " + clip(w.RCA.Prevention, 300)
			}
			// The changes run from its drawer that finished done, which it
			// kept when it closed (CF-49).
			if fix := fixLines(w.Commands); len(fix) > 0 {
				if len(fix) > MaxCommands {
					fix = fix[:MaxCommands]
				}
				line += "\n  commands that fixed it: " + strings.Join(fix, "; ")
			}
		}
		out = append(out, line)
	}
	return out
}

// closedAs is how a closed case was closed, in words, with what it
// names.
func closedAs(l issues.Line) string {
	switch {
	case l.Outcome == issues.OutcomeDuplicate && l.DuplicateOf > 0:
		return "a duplicate of " + caseID(l.DuplicateOf)
	case l.Outcome == issues.OutcomeException && l.Exception != "":
		return "an accepted exception (" + l.Exception + ")"
	}
	return outcomeWords(l.Outcome, l.KnownIssue)
}

// closedAt is when a case was closed; its last change when that is not
// known.
func closedAt(l issues.Line) time.Time {
	if l.ClosedAt != nil {
		return *l.ClosedAt
	}
	return l.Updated
}

// --- end of what the analysis starts from.
