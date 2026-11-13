package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// --- Runs that wait for approval (FU-05) ---

// keptWaitingRuns is how many decided run requests stay listed.
const keptWaitingRuns = 50

// WaitingRun is a run on a production cluster whose own values need
// approval: they are weighed as a change of the fleet's values for this
// run alone (Impact), and one that needs approval waits here until a
// second SuperAdmin approves it, which starts the run as it was asked for
// and by whom, or anyone rejects it; whoever asked may only reject it. It
// is kept in the record of the variable changes.
type WaitingRun struct {
	ID          int    `json:"id"`
	Cluster     string `json:"cluster"`
	ClusterName string `json:"clusterName,omitempty"`
	Script      string `json:"script"`
	Version     int    `json:"version"`
	Fleet       string `json:"fleet"`
	FleetName   string `json:"fleetName,omitempty"`
	// Nodes are the nodes the run named, none for the whole fleet;
	// Targets the nodes it would run on when it was asked for.
	Nodes   []string          `json:"nodes,omitempty"`
	Targets []string          `json:"targets"`
	Params  map[string]string `json:"params"`
	Rollout Rollout           `json:"rollout"`
	Config  string            `json:"config,omitempty"`
	Hooks   HookRefs          `json:"hooks"`
	RetryOf int               `json:"retryOf,omitempty"`
	// Request is the page's name for the start: asked twice, it waits
	// once.
	Request string `json:"request,omitempty"`
	// By asked for it; Person is who the cluster sees, which the run is
	// started as once it is approved.
	By     Who       `json:"by"`
	Person Person    `json:"person"`
	At     time.Time `json:"at"`
	// Changed are the run's values that differ from what a node inherits;
	// Impact what that means for the script, as for a variable change.
	Changed []string     `json:"changed"`
	Impact  []ImpactItem `json:"impact"`
	Note    string       `json:"note,omitempty"`
	State   string       `json:"state"`
	// DecidedBy, DecidedAt and Comment are the decision; Run is the run
	// the approval started.
	DecidedBy *Who       `json:"decidedBy,omitempty"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	Comment   string     `json:"comment,omitempty"`
	Run       int        `json:"run,omitempty"`
}

// What says what waits, in words.
func (w WaitingRun) What() string {
	names := make([]string, 0, len(w.Params))
	for k := range w.Params {
		names = append(names, k)
	}
	sort.Strings(names)
	fleet := w.FleetName
	if fleet == "" {
		fleet = w.Fleet
	}
	return fmt.Sprintf("%s v%d on %s, with its own values for %s", w.Script, w.Version, fleet, strings.Join(names, ", "))
}

// RequestKey is the name the run an approval starts goes by, so a second
// approval of the same request starts no second run.
func (w WaitingRun) RequestKey() string {
	return fmt.Sprintf("waiting-run-%s-%d", w.Cluster, w.ID)
}

// NewWaitingRun records a run that waits for approval.
func (st *Store) NewWaitingRun(ctx context.Context, w WaitingRun) (WaitingRun, error) {
	w.At, w.State, w.DecidedBy, w.DecidedAt, w.Comment, w.Run = st.now().UTC(), ChangeWaiting, nil, nil, "", 0
	if w.Changed == nil {
		w.Changed = []string{}
	}
	if w.Impact == nil {
		w.Impact = []ImpactItem{}
	}
	err := change(ctx, st, ChangesKey, 256<<10, func(rec *changesRecord) error {
		for _, x := range rec.Runs {
			if w.Request != "" && x.Request == w.Request && x.Cluster == w.Cluster && x.By.Same(w.By) {
				w = x
				return errNoChange
			}
		}
		rec.NextRun++
		w.ID = rec.NextRun
		rec.Runs = append(rec.Runs, w)
		decided := 0
		for i := len(rec.Runs) - 1; i >= 0; i-- {
			if rec.Runs[i].State == ChangeWaiting {
				continue
			}
			if decided++; decided > keptWaitingRuns {
				rec.Runs = append(rec.Runs[:i], rec.Runs[i+1:]...)
			}
		}
		return nil
	})
	return w, err
}

// WaitingRuns lists the run requests of a cluster, newest first; every
// cluster's when cluster is "".
func (st *Store) WaitingRuns(ctx context.Context, cluster string) ([]WaitingRun, error) {
	var rec changesRecord
	if _, err := st.load(ctx, ChangesKey, &rec); err != nil {
		return nil, err
	}
	out := []WaitingRun{}
	for i := len(rec.Runs) - 1; i >= 0; i-- {
		if cluster == "" || rec.Runs[i].Cluster == cluster {
			out = append(out, rec.Runs[i])
		}
	}
	return out, nil
}

// WaitingRunFor is one run request, when who may approve it: it waits,
// and someone else asked for it.
func (st *Store) WaitingRunFor(ctx context.Context, cluster string, id int, who Who) (WaitingRun, error) {
	list, err := st.WaitingRuns(ctx, cluster)
	if err != nil {
		return WaitingRun{}, err
	}
	for _, w := range list {
		if w.ID != id {
			continue
		}
		switch {
		case w.State != ChangeWaiting:
			return w, fmt.Errorf("%w: run request %d is %s", ErrInvalid, id, w.State)
		case who.Same(w.By):
			return w, fmt.Errorf("%w: you asked for this run; another admin approves it", ErrOwnWork)
		}
		return w, nil
	}
	return WaitingRun{}, ErrNotFound
}

// DecideWaitingRun records a decision on a waiting run: approved, with the
// run it started, or rejected, which says why. Whoever asked for it may
// reject it, never approve it.
func (st *Store) DecideWaitingRun(ctx context.Context, cluster string, id int, approve bool, comment string, who Who, run int) (WaitingRun, error) {
	comment = strings.TrimSpace(comment)
	if !approve && comment == "" {
		return WaitingRun{}, fmt.Errorf("%w: say why it is rejected", ErrInvalid)
	}
	if len(comment) > MaxComment {
		return WaitingRun{}, fmt.Errorf("%w: a comment is at most %d characters", ErrInvalid, MaxComment)
	}
	var out WaitingRun
	err := change(ctx, st, ChangesKey, 256<<10, func(rec *changesRecord) error {
		for i := range rec.Runs {
			w := &rec.Runs[i]
			if w.ID != id || w.Cluster != cluster {
				continue
			}
			if w.State != ChangeWaiting {
				return fmt.Errorf("%w: run request %d is %s", ErrInvalid, id, w.State)
			}
			if approve && who.Same(w.By) {
				return fmt.Errorf("%w: you asked for this run; another admin approves it", ErrOwnWork)
			}
			now := st.now().UTC()
			w.State, w.DecidedBy, w.DecidedAt, w.Comment = ChangeRejected, &who, &now, comment
			if approve {
				w.State, w.Run = ChangeApproved, run
			}
			out = *w
			return nil
		}
		return ErrNotFound
	})
	return out, err
}
