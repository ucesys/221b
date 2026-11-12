package casework

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/jobs"
)

// ---------- on the leader: the analyses left behind, the retention ----------

// Run looks for analyses a replica left behind, and runs the day's
// retention, until ctx ends; then it stops the analyses under way.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.o.Every)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			s.Stop()
			return
		case <-t.C:
		}
	}
}

// Tick runs once, on the leader: the retention when a day has passed
// since it last ran, the changes that resolve cases it has not acted on
// (CF-29), the queue of analyses (CF-32), and it lets go of the analyses
// no replica runs any more: one
// whose run ended, or never started, while it was not running here
// (CW-04).
func (s *Service) Tick(ctx context.Context) {
	if !s.leads() {
		s.mu.Lock()
		s.caught = time.Time{}
		s.mu.Unlock()
		return
	}
	s.retain(ctx)
	s.catchUp(ctx)
	// What the runs kept found is counted once as it leads (CF-85).
	s.rules.recountOnce(ctx)
	if s.o.Jobs == nil {
		return
	}
	// The next queued analysis, when none runs (CF-32).
	defer s.drain(ctx)
	rec, err := s.loadRecord(ctx)
	if err != nil {
		return
	}
	now := s.now()
	var gone []int
	for key, since := range rec.Analyzing {
		n, err := strconv.Atoi(key)
		if err != nil || s.running(n) {
			continue
		}
		w, _, err := s.loadWork(ctx, n)
		if errors.Is(err, ErrNotFound) {
			gone = append(gone, n)
			continue
		}
		if err != nil {
			continue
		}
		if len(w.Runs) == 0 || w.Analysis != nil && w.Analysis.Run == w.Runs[0] {
			// Not started, or ended and noted: let go once it has waited
			// long enough to have started on any replica.
			if now.Sub(since) > 2*time.Minute {
				gone = append(gone, n)
			}
			continue
		}
		if r, err := s.o.Jobs.GetRun(ctx, w.Runs[0]); errors.Is(err, jobs.ErrNotFound) || err == nil && r.Status != jobs.StatusRunning {
			gone = append(gone, n)
		}
	}
	for _, n := range gone {
		_ = s.updateRecord(ctx, func(rec *record) error {
			if _, ok := rec.Analyzing[strconv.Itoa(n)]; !ok || s.running(n) {
				return errUnchanged
			}
			delete(rec.Analyzing, strconv.Itoa(n))
			return nil
		})
		s.note(ctx, n, Event{At: now, What: "The analysis stopped: the replica running it went down or lost the lead; analyze it again"})
		s.record(audit.Event{Action: ActionAnalyze, Who: audit.Who{Subject: "221b", Name: "case analysis"}, Target: caseID(n), Outcome: audit.OutcomeFailed, Detail: fmt.Sprintf("case %s: its analysis stopped with the replica running it", caseID(n))})
	}
}

func (s *Service) running(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[n]
}

// retain deletes the agent-found cases closed more than
// keepAgentClosedDays ago (CF-21), once a day: their lines and objects. A
// case a person opened keeps the issue service's retention of its text.
// The day is claimed first, over the version read, so two leaders do not
// both run it.
func (s *Service) retain(ctx context.Context) {
	now := s.now()
	claimed := false
	var keep int
	err := s.updateRecord(ctx, func(rec *record) error {
		if now.Sub(rec.Retained) < 24*time.Hour {
			return errUnchanged
		}
		rec.Retained, claimed, keep = now, true, rec.Settings.KeepAgentClosedDays
		return nil
	})
	if err != nil || !claimed {
		return
	}
	gone, err := s.o.Issues.DropClosedAgentCases(ctx, now.AddDate(0, 0, -keep), system.Person)
	if err != nil {
		s.o.Logger.Warn("the closed agent-found cases could not be let go", "err", err)
		s.record(audit.Event{Action: ActionRetention, Who: audit.Who{Subject: "221b", Name: "cases"}, Target: "cases", Outcome: audit.OutcomeFailed, Detail: "the closed agent-found cases could not be let go: " + err.Error()})
		return
	}
	if len(gone) == 0 {
		return
	}
	ids := make([]string, len(gone))
	for i, n := range gone {
		ids[i] = caseID(n)
	}
	s.record(audit.Event{Action: ActionRetention, Who: audit.Who{Subject: "221b", Name: "cases"}, Target: "cases", Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("%d agent-found cases closed more than %d days ago deleted: %s", len(gone), keep, strings.Join(ids, ", "))})
}
