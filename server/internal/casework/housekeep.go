package casework

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/schedule"
)

// --- Housekeeping (Phase 12, wave 6g, CF-53) ---
//
// The daily job Housekeeping keeps the console's records from repeating
// and contradicting each other: a fix that says what another of the same
// problem says is made inactive (the one that fixed more cases kept, with
// every case); two that say otherwise are both pending until a person
// accepts one; an open known issue that repeats an older one is made
// inactive; an active exception that covers what an older one covers is
// removed. Each is named after what it repeats, audited, and undone by a
// person; the run's report lists them.
//
// Since ADR-138 it also looks at the ids the agents made up (other.<slug>):
// an exact repeat found often enough, with no variant, is promoted to a
// validation rule as it is (undone by retiring it); the other candidates,
// with their likely variants, are suggested in AIOps › Mycroft ›
// Validation rules for a person to promote, edit or dismiss.

// KindHousekeeping is the housekeeping job's kind.
const KindHousekeeping = "housekeeping"

// HousekeepingKind is the housekeeping job: no agent, daily at 03:30, on
// until a SuperAdmin turns it off.
func (s *Service) HousekeepingKind() jobs.Kind {
	return jobs.Kind{ID: KindHousekeeping, Label: "Housekeeping", Order: 90, On: true,
		Does:        "Makes the fixes, known issues and exceptions that repeat another inactive, marks the fixes that contradict each other pending, offers Mycroft the fixes proven in cases for a runbook, and suggests validation rules from the ids the agents made up",
		Default:     jobs.When{Schedule: &schedule.Spec{Kind: schedule.Daily, At: "03:30"}},
		Schedulable: true,
		Run: func(ctx context.Context, env *jobs.RunEnv) (jobs.Outcome, error) {
			text, err := s.Housekeep(ctx)
			return jobs.Outcome{Report: text}, err
		}}
}

// Housekeep does one round of housekeeping and gives its report.
func (s *Service) Housekeep(ctx context.Context) (string, error) {
	var b strings.Builder
	var errs []error
	n := 0
	b.WriteString("## Housekeeping\n\n")
	if s.o.Fixes != nil {
		done, err := s.o.Fixes.Housekeep(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("the fixes: %w", err))
		}
		var dup, pend []string
		for _, d := range done {
			if d.What == fixes.ReasonDuplicate {
				dup = append(dup, fmt.Sprintf("- %s made inactive: a duplicate of %s, which keeps its cases", d.ID, d.Of))
			} else {
				pend = append(pend, fmt.Sprintf("- %s and %s pending: they fix the same problem differently; accept the right one in AIOps › Mycroft › Fixes", d.ID, d.Of))
			}
		}
		n += section(&b, "Fixes made inactive as duplicates", dup)
		n += section(&b, "Fixes pending, contradicting each other", pend)
	}
	// A fix proven in cases goes to Mycroft for a runbook (KN-34), once,
	// and again when it changes; after the duplicates are made inactive.
	if s.o.Runbooks != nil {
		offered, started, err := s.o.Runbooks.OfferFixes(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("the fixes for Mycroft's runbooks: %w", err))
		}
		next := "its job Wiki sync: Fixes drafts a runbook from it, which waits for review in AIOps › Inbox"
		if !started {
			next = "not read yet: the job Wiki sync: Fixes is paused or not running; it reads it at its next run"
		}
		var lines []string
		for _, o := range offered {
			lines = append(lines, "- "+o+": "+next)
		}
		n += section(&b, "Fixes offered to Mycroft for a runbook", lines)
	}
	if s.o.Issues != nil {
		done, err := s.o.Issues.HousekeepKnown(ctx, issues.Person{ID: "system:housekeeping", Name: "Housekeeping", System: true})
		if err != nil {
			errs = append(errs, fmt.Errorf("the known issues: %w", err))
		}
		var lines []string
		for _, d := range done {
			lines = append(lines, fmt.Sprintf("- %s made inactive: a duplicate of %s; no case is matched to it until an operator makes it active again", d.ID, d.Of))
		}
		n += section(&b, "Known issues made inactive as duplicates", lines)
	}
	if s.o.Lessons != nil {
		done, err := s.o.Lessons.RemoveDuplicates(ctx, lessons.Person{Subject: "221b", Name: "Housekeeping"})
		if err != nil {
			errs = append(errs, fmt.Errorf("the exceptions: %w", err))
		}
		var lines []string
		for _, d := range done {
			lines = append(lines, fmt.Sprintf("- %s removed: it covers what %s covers", d.ID, d.Of))
		}
		n += section(&b, "Exceptions removed as duplicates", lines)
	}
	{
		done, err := s.rules.Housekeep(ctx, fixes.Likeness)
		if err != nil {
			errs = append(errs, fmt.Errorf("the validation rules: %w", err))
		}
		var auto, sugg []string
		for _, r := range done.Promoted {
			auto = append(auto, fmt.Sprintf("- %s promoted to a validation rule as it is: an exact repeat with no variant; it means “%s”, case type %s. Undo: retire it in AIOps › Mycroft › Validation rules", r.ID, r.Says, r.Type))
		}
		for _, x := range done.Suggested {
			sugg = append(sugg, fmt.Sprintf("- %s suggested, from %s: found in %d runs on %d clusters; promote, edit or dismiss it in AIOps › Mycroft › Validation rules or the Inbox", x.ID, strings.Join(x.From, ", "), x.Runs, x.Clusters))
		}
		n += section(&b, "Validation rules promoted (exact repeats)", auto)
		n += section(&b, "Validation rules suggested", sugg)
	}
	if n == 0 {
		b.WriteString("Nothing to do: no fix, known issue or exception repeats or contradicts another, no fix to offer Mycroft for a runbook, and no validation rule to suggest.\n")
	}
	if len(errs) > 0 {
		b.WriteString("\n### Not done\n\n")
		for _, e := range errs {
			b.WriteString("- " + e.Error() + "\n")
		}
	}
	return b.String(), errors.Join(errs...)
}

func section(b *strings.Builder, title string, lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	b.WriteString("### " + title + "\n\n" + strings.Join(lines, "\n") + "\n\n")
	return len(lines)
}
