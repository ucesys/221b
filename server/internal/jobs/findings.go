package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// --- Cancelling a run (Phase 12, FX-12) ---
//
// A run under way may be cancelled. Its findings are routed into cases
// (package casework, CF-01): they have no state of their own.

// ---------- cancelling ----------

// activeRun is a run under way on this replica: how to cancel it, and
// when it has ended.
type activeRun struct {
	stop func(error)
	done chan struct{}
}

// cancelled is the cause a run's context ends with when a person cancels
// it.
type cancelled struct{ by Person }

func (c *cancelled) Error() string { return "cancelled by " + c.by.Label() }

// Cancel cancels a run under way (FX-12): its agent's call is cut short,
// what it spent so far is counted, and it ends "cancelled" with who
// cancelled it. It waits a little for the run to end and returns it. A
// run marked running that no replica runs any more (its replica went
// down) is marked cancelled at once.
func (s *Service) Cancel(ctx context.Context, id string, by Person) (RunBrief, error) {
	r, err := s.GetRun(ctx, id)
	if err != nil {
		return RunBrief{}, err
	}
	if r.Status != StatusRunning {
		return brief(r), fmt.Errorf("%w: the run is not running (%s)", ErrInvalid, r.Status)
	}
	s.mu.Lock()
	act := s.active[id]
	s.mu.Unlock()
	if act != nil {
		act.stop(&cancelled{by: by})
		select {
		case <-act.done:
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
		}
	} else {
		now := s.o.Now().UTC()
		err := s.updateRuns(ctx, func(rec *runsRecord) error {
			for i := range rec.Runs {
				x := &rec.Runs[i]
				if x.ID == id && x.Status == StatusRunning {
					x.Status, x.Ended = StatusCancelled, now
					x.Note = clip(strings.TrimPrefix(x.Note+"; ", "; ")+"cancelled by "+by.Label()+"; no console was running it any more", 1000)
					return nil
				}
			}
			return errUnchanged
		})
		if err != nil {
			return RunBrief{}, err
		}
	}
	s.record(audit.Event{Action: ActionCancel, Who: who(by), Target: r.Job, From: by.From, Outcome: audit.OutcomeOK, Cluster: oneCluster(r.Clusters),
		Detail: fmt.Sprintf("%s %s cancelled while it ran", r.ID, r.Label)})
	out, err := s.GetRun(ctx, id)
	if err != nil {
		return RunBrief{}, err
	}
	return brief(out), nil
}

func oneCluster(list []string) string {
	if len(list) == 1 {
		return list[0]
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
