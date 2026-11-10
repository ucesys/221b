package casework

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- Agent-found cases are analysed by themselves (Phase 12, wave 6c,
// CF-32) ---
//
// While the case analysis job is on, each new case a run opens at P0-P2
// (opened, a regression, or a fix that did not hold reopening it) is
// queued for one analysis (route.go marks it, AnalysisQueued). The
// leader's tick runs the queue one analysis at a time: the highest
// priority first, then the oldest. It keeps to the job: paused, or not
// covering a case's cluster, the case waits; with the day's budget spent,
// the rest wait for the next day (the run's budget holds as on any run).
// A case leaves the queue when its analysis starts, when a person asks
// for one (Analyze), and when it closes. A case a person opened is
// analysed at once, as before.

// reader is the console reading a case for its own work.
var reader = Caller{Person: Person{Name: "System"}, ID: system.ID, Read: true}

// drain starts the next queued analysis, when none from the queue runs
// here.
func (s *Service) drain(ctx context.Context) {
	if s.o.Jobs == nil {
		return
	}
	s.mu.Lock()
	busy := s.queueRun != 0
	s.mu.Unlock()
	if busy {
		return
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return
	}
	var queued []issues.Line
	for _, l := range lines {
		if l.AnalysisQueued != nil && l.Open() {
			queued = append(queued, l)
		}
	}
	if len(queued) == 0 {
		return
	}
	sort.SliceStable(queued, func(i, j int) bool {
		a, b := queued[i], queued[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.AnalysisQueued.Equal(*b.AnalysisQueued) {
			return a.AnalysisQueued.Before(*b.AnalysisQueued)
		}
		return a.Number < b.Number
	})
	under := s.analyzing(ctx)
	// Whether the job takes a cluster's case now, asked once a cluster.
	ready := map[string]error{}
	for _, l := range queued {
		if under[l.Number] || s.running(l.Number) {
			continue
		}
		err, asked := ready[l.Cluster]
		if !asked {
			err = s.o.Jobs.Ready(ctx, jobs.KindCase, jobs.Event{Kind: jobs.EventCase, Cluster: l.Cluster})
			ready[l.Cluster] = err
		}
		switch {
		case errors.Is(err, jobs.ErrBudget):
			// The day's budget is spent: the rest wait for tomorrow.
			return
		case errors.Is(err, jobs.ErrOff):
			// Paused, or not covering its cluster: it waits.
			continue
		case err != nil:
			return
		}
		v, err := s.Get(ctx, reader, caseID(l.Number))
		if err != nil || !v.Open() {
			continue
		}
		s.mu.Lock()
		if s.queueRun != 0 {
			s.mu.Unlock()
			return
		}
		s.queueRun = l.Number
		s.mu.Unlock()
		info := infoOf(v)
		s.note(ctx, l.Number, Event{At: s.now(), What: "Its turn in the queue came: the case analysis job started on it"})
		s.record(audit.Event{Action: ActionAnalyze, Who: audit.Who{Subject: "221b", Name: "case analysis"}, Cluster: l.Cluster, Target: caseID(l.Number), Outcome: audit.OutcomeOK,
			Detail: fmt.Sprintf("case %s: its analysis started from the queue (P%d, queued %s, %d waiting)", caseID(l.Number), l.Priority, l.AnalysisQueued.UTC().Format("2006-01-02 15:04"), len(queued)-1)})
		s.startAnalysis(ctx, l.Number, sys)
		s.launch(info)
		return
	}
}

// Enqueue queues an open case for an analysis, for a person who asked
// for many at once (CF-38): the queue runs it in its turn. Refused as
// Analyze is: a closed case, one naming no cluster, one being analysed,
// or the job paused.
func (s *Service) Enqueue(ctx context.Context, id string, c Caller) error {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return err
	}
	switch {
	case !v.Status.Open():
		return fmt.Errorf("%w: %s is closed", ErrState, caseID(n))
	case v.Cluster == "":
		return fmt.Errorf("%w: name the cluster %s is about first", ErrInvalid, caseID(n))
	case s.analyzing(ctx)[n] || s.running(n):
		return fmt.Errorf("%w: %s is being analysed", ErrState, caseID(n))
	case s.o.Jobs == nil || !s.o.Jobs.On(ctx, jobs.KindCase, jobs.Event{Kind: jobs.EventCase, Cluster: v.Cluster}):
		return fmt.Errorf("%w: turn the case analysis job on in AIOps › Jobs (it must cover %s)", ErrOff, v.Cluster)
	}
	_, err = s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Report) error {
		if !r.Status.Open() {
			return fmt.Errorf("%w: %s is closed", issues.ErrInvalid, caseID(n))
		}
		if r.AnalysisQueued == nil {
			at := tx.Now()
			r.AnalysisQueued = &at
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.note(ctx, n, Event{At: s.now(), What: "Queued for the case analysis job", By: c.actor()})
	s.record(audit.Event{Action: ActionAnalyze, Who: who(c.Person), Cluster: v.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: queued for its analysis", caseID(n))})
	return nil
}

// queueDone lets the queue go on once its analysis ended.
func (s *Service) queueDone(n int) {
	s.mu.Lock()
	if s.queueRun == n {
		s.queueRun = 0
	}
	s.mu.Unlock()
}

// --- end of the queue.
