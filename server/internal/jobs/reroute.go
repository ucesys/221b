package jobs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// --- Routing that did not happen is tried again (phase 12 review, W-8,
// I-3, CF-99) ---
//
// A run whose findings could not be routed into cases (the exceptions
// could not be read, or the cases saved) keeps them as they came, says
// why on the run, and is routed again on the leader's next look, the
// oldest first. A late routing opens and joins cases as the run would
// have, and verifies no fix: what a run did not see counts only when it
// ends. A run is let go, and says so, when its job is gone, when a later
// run of its job has been routed since (its findings are older than what
// the cases know now), or a day after it ended.

// rerouteFor is how long a run's routing is tried again.
const rerouteFor = 24 * time.Hour

// reroute routes again the runs whose routing did not happen.
func (s *Service) reroute(ctx context.Context, jr jobsRecord) {
	if s.o.Router == nil {
		return
	}
	// At most once a minute, as the sweep.
	now := s.o.Now()
	s.mu.Lock()
	if now.Sub(s.rerouted) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.rerouted = now
	s.mu.Unlock()
	rec, err := s.loadRuns(ctx)
	if err != nil {
		s.o.Logger.Warn("the runs cannot be read to route them again", "err", err)
		return
	}
	var todo []Run
	for _, r := range rec.Runs {
		if r.Reroute && r.Status != StatusRunning {
			todo = append(todo, r)
		}
	}
	// The runs are kept newest first; the oldest is routed first.
	slices.Reverse(todo)
	for _, r := range todo {
		k, kind := s.o.Kinds.Kind(r.Kind)
		i := slices.IndexFunc(s.jobsOf(jr), func(j Job) bool { return j.ID == r.Job })
		why := ""
		switch {
		case i < 0 || !kind:
			why = "its job is gone"
		case routedAfter(rec.Runs, r):
			why = "a later run of its job was routed since"
		case now.Sub(r.Ended) > rerouteFor:
			why = "it was tried again for a day"
		}
		var res RouteResult
		if why == "" {
			j := s.jobsOf(jr)[i]
			res = s.o.Router.Route(ctx, RouteInput{Run: r.ID, Job: r.Job, Kind: r.Kind, Label: r.Label, Clusters: r.Clusters, OK: false, Findings: r.Findings,
				Agent: firstNonEmpty(k.AgentName, k.Agent), MinPriority: ptrInt(j.Lowest())})
			if res.NotRouted != "" {
				// Still not: the next look tries again.
				continue
			}
		}
		done := false
		err := s.updateRuns(ctx, func(rec *runsRecord) error {
			done = false
			for i := range rec.Runs {
				x := &rec.Runs[i]
				if x.ID != r.ID {
					continue
				}
				if !x.Reroute {
					return errUnchanged
				}
				x.Reroute, done = false, true
				if why != "" {
					x.Note = clip(strings.TrimPrefix(x.Note+"; ", "; ")+"its findings were let go unrouted: "+why, 1000)
					return nil
				}
				x.Findings, x.Routed, x.NewChecks, x.NotRouted = res.Findings, res.Routed, res.NewChecks, ""
				x.Note = clip(strings.TrimPrefix(x.Note+"; ", "; ")+fmt.Sprintf("its findings were routed into cases on a later look, at %s", now.UTC().Format("2006-01-02 15:04 UTC")), 1000)
				return nil
			}
			return errUnchanged
		})
		if err != nil {
			s.o.Logger.Warn("a run routed again could not be kept", "run", r.ID, "err", err)
			continue
		}
		if done && why != "" {
			s.record(audit.Event{Action: ActionRun, Who: audit.Who{Subject: "221b", Name: "job " + r.Job}, Target: r.Job, Outcome: audit.OutcomeFailed,
				Detail: fmt.Sprintf("%s %s: its findings were let go unrouted: %s (%s)", r.ID, r.Label, why, r.NotRouted)})
		}
	}
}

// routedAfter says whether a run of r's job that started after it has
// been routed: one that ran to an end and is not waiting to be routed.
func routedAfter(runs []Run, r Run) bool {
	for _, x := range runs {
		if x.Job != r.Job || !x.Started.After(r.Started) || x.Reroute || x.NotRouted != "" {
			continue
		}
		switch x.Status {
		case StatusRunning, StatusSkipped, StatusMissed:
			continue
		}
		return true
	}
	return false
}
