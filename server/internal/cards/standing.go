package cards

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// Standing approvals (ADR-102): a SuperAdmin approves a narrow class of
// proposals ahead, outside production only, such as "restart a
// crash-looping pod in development, at most 3 an hour". A card that
// matches one is approved by it as it arrives, within its rate, after the
// same re-check any approval makes, and the audit log says so. Its
// changes run as the SuperAdmin who set it, as Kubernetes knew them then.

// RunAs is who a standing approval's changes run as: the user and groups
// the clusters knew the SuperAdmin who set it by.
type RunAs struct {
	User   string   `json:"user,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// Standing is one standing approval.
type Standing struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	On   bool   `json:"on"`
	// Agent limits it to one agent's proposals; empty is any agent's.
	Agent string `json:"agent,omitempty"`
	// Clusters are the clusters it covers, never a production one.
	Clusters []string `json:"clusters"`
	// Actions are what each of a card's actions must be: a console
	// action's kind ("host.label"), or the start of a kubectl or helm
	// command of at least two words ("kubectl rollout restart").
	Actions []string `json:"actions"`
	// Namespaces limits kubectl and helm commands to these; empty is any.
	Namespaces []string `json:"namespaces,omitempty"`
	// Condition, when set, is the condition a card must carry, such as
	// alert-firing for "a crash-looping pod".
	Condition string `json:"condition,omitempty"`
	// PerHour is the most cards it approves in an hour.
	PerHour int       `json:"perHour"`
	By      Person    `json:"by"`
	RunAs   RunAs     `json:"runAs"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Applied are when it approved a card, in the last hour.
	Applied []time.Time `json:"applied,omitempty"`
}

// Limits of the standing approvals.
const (
	MaxStanding    = 50
	MaxPerHour     = 60
	maxStandingFor = 20
)

type standingRecord struct {
	Standing []Standing `json:"standing"`
}

func (s *Store) changeStanding(ctx context.Context, edit func(*standingRecord) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return changeJSON(ctx, s.opts.Backend, StandingKey, edit)
}

// Standings lists the standing approvals.
func (s *Store) Standings(ctx context.Context) ([]Standing, error) {
	rec, _, err := loadJSON[standingRecord](ctx, s.opts.Backend, StandingKey)
	if err != nil {
		return nil, err
	}
	out := rec.Standing
	if out == nil {
		out = []Standing{}
	}
	return out, nil
}

func newStandingID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "s" + hex.EncodeToString(b)
}

// checkStanding validates a standing approval as a SuperAdmin sets it.
func (s *Store) checkStanding(st *Standing) error {
	st.Name = strings.TrimSpace(st.Name)
	st.Agent = strings.TrimSpace(st.Agent)
	switch {
	case st.Name == "" || len(st.Name) > 80:
		return fmt.Errorf("%w: give it a name of at most 80 characters", ErrInvalid)
	case len(st.Clusters) == 0 || len(st.Clusters) > maxStandingFor:
		return fmt.Errorf("%w: name 1 to %d clusters it covers", ErrInvalid, maxStandingFor)
	case len(st.Actions) == 0 || len(st.Actions) > maxStandingFor:
		return fmt.Errorf("%w: name 1 to %d kinds of action it approves", ErrInvalid, maxStandingFor)
	case st.PerHour < 1 || st.PerHour > MaxPerHour:
		return fmt.Errorf("%w: it approves 1 to %d cards an hour", ErrInvalid, MaxPerHour)
	case len(st.Namespaces) > maxStandingFor:
		return fmt.Errorf("%w: at most %d namespaces", ErrInvalid, maxStandingFor)
	}
	switch st.Condition {
	case "", ConditionAlert, ConditionVersion:
	default:
		return fmt.Errorf("%w: a condition is alert-firing or resource-version, or none", ErrInvalid)
	}
	for _, id := range st.Clusters {
		info, ok := s.opts.Cluster(id)
		switch {
		case !ok:
			return fmt.Errorf("%w: no cluster %q is connected", ErrInvalid, id)
		case info.Production:
			return fmt.Errorf("%w: %s is a production cluster, where a standing approval is never allowed", ErrInvalid, info.Name)
		}
	}
	for i, a := range st.Actions {
		a = strings.Join(strings.Fields(a), " ")
		st.Actions[i] = a
		tool, _, _ := strings.Cut(a, " ")
		switch {
		case tool == KindKubectl || tool == KindHelm:
			f := strings.Fields(a)
			if len(f) < 2 || strings.HasPrefix(f[1], "-") {
				return fmt.Errorf("%w: %q: name the command, such as \"kubectl rollout restart\", not the whole of %s", ErrInvalid, a, tool)
			}
		case a == KindRecord || s.executor(a) == nil:
			return fmt.Errorf("%w: %q is not an action Watson runs here", ErrInvalid, a)
		}
	}
	return nil
}

// SaveStanding adds (no ID) or changes a standing approval, set by a
// SuperAdmin whose changes it runs as.
func (s *Store) SaveStanding(ctx context.Context, st Standing, by Person, runAs RunAs) (Standing, error) {
	if err := s.checkStanding(&st); err != nil {
		return st, err
	}
	now := s.now()
	st.By, st.RunAs, st.Updated, st.Applied = by, runAs, now, nil
	adding := st.ID == ""
	err := s.changeStanding(ctx, func(rec *standingRecord) error {
		if adding {
			if len(rec.Standing) >= MaxStanding {
				return fmt.Errorf("%w: at most %d standing approvals", ErrInvalid, MaxStanding)
			}
			st.ID, st.Created = newStandingID(), now
			rec.Standing = append(rec.Standing, st)
			return nil
		}
		i := slices.IndexFunc(rec.Standing, func(x Standing) bool { return x.ID == st.ID })
		if i < 0 {
			return ErrNotFound
		}
		st.Created, st.Applied = rec.Standing[i].Created, rec.Standing[i].Applied
		rec.Standing[i] = st
		return nil
	})
	if err != nil {
		return st, err
	}
	what := "changed"
	if adding {
		what = "set"
	}
	s.recordStanding(st, by, audit.OutcomeOK, fmt.Sprintf("%s: %s; on %s; at most %d an hour; on=%v", what, strings.Join(st.Actions, ", "), strings.Join(st.Clusters, ", "), st.PerHour, st.On))
	return st, nil
}

// DeleteStanding removes a standing approval.
func (s *Store) DeleteStanding(ctx context.Context, id string, by Person) error {
	var gone Standing
	err := s.changeStanding(ctx, func(rec *standingRecord) error {
		i := slices.IndexFunc(rec.Standing, func(x Standing) bool { return x.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		gone = rec.Standing[i]
		rec.Standing = slices.Delete(rec.Standing, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	s.recordStanding(gone, by, audit.OutcomeOK, "removed")
	return nil
}

func (s *Store) recordStanding(st Standing, by Person, outcome, detail string) {
	if s.opts.Audit == nil {
		return
	}
	s.opts.Audit.Record(audit.Event{Time: s.now(), Action: audit.ActionCardStanding, Who: audit.Who{Subject: by.Key, Name: by.Name}, Target: "standing approval " + st.Name, Outcome: outcome, Detail: detail})
}

// namespaceOf is the namespace a kubectl or helm command line names, or
// "default".
func namespaceOf(fields []string) string {
	for i, f := range fields {
		switch {
		case (f == "-n" || f == "--namespace") && i+1 < len(fields):
			return fields[i+1]
		case strings.HasPrefix(f, "--namespace="):
			return strings.TrimPrefix(f, "--namespace=")
		case strings.HasPrefix(f, "-n") && len(f) > 2:
			return f[2:]
		}
	}
	return "default"
}

// Matches says whether a standing approval covers a card, and why not.
func (st Standing) Matches(c Card, info ClusterInfo) (bool, string) {
	switch {
	case !st.On:
		return false, "it is off"
	case info.Production:
		return false, "the cluster is production"
	case !slices.Contains(st.Clusters, c.Cluster):
		return false, "it does not cover the cluster"
	case st.Agent != "" && st.Agent != c.Agent:
		return false, "it covers another agent's proposed changes"
	case st.Condition != "" && st.Condition != c.Condition.Kind:
		return false, "the card does not carry its condition"
	case c.Requester != nil:
		return false, "a person asked for it"
	}
	for i, a := range c.Actions {
		if !st.covers(a) {
			return false, fmt.Sprintf("it does not cover action %d (%s)", i+1, a.Kind)
		}
	}
	return true, ""
}

// covers says whether one action is of a class the approval names.
func (st Standing) covers(a Action) bool {
	if a.Kind != KindKubectl && a.Kind != KindHelm {
		return slices.Contains(st.Actions, a.Kind)
	}
	line, _ := a.Args["command"].(string)
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != a.Kind {
		return false
	}
	if len(st.Namespaces) > 0 && !slices.Contains(st.Namespaces, namespaceOf(fields)) {
		return false
	}
	// The command's words, flags left out, must start with the class's.
	var words []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "-n" || f == "--namespace" {
			i++
			continue
		}
		if !strings.HasPrefix(f, "-") {
			words = append(words, f)
		}
	}
	for _, class := range st.Actions {
		want := strings.Fields(class)
		if len(want) >= 2 && len(words) >= len(want) && slices.Equal(words[:len(want)], want) {
			return true
		}
	}
	return false
}

// tryStanding approves a new card by the first standing approval that
// covers it and has room in its hour.
func (s *Store) tryStanding(ctx context.Context, c Card) {
	list, err := s.Standings(ctx)
	if err != nil || len(list) == 0 {
		return
	}
	info, ok := s.opts.Cluster(c.Cluster)
	if !ok || info.Production {
		return
	}
	for _, st := range list {
		if ok, _ := st.Matches(c, info); !ok {
			continue
		}
		now := s.now()
		took := false
		var kept Standing
		err := s.changeStanding(ctx, func(rec *standingRecord) error {
			took = false
			i := slices.IndexFunc(rec.Standing, func(x Standing) bool { return x.ID == st.ID })
			if i < 0 {
				return ErrNotFound
			}
			x := &rec.Standing[i]
			x.Applied = slices.DeleteFunc(x.Applied, func(t time.Time) bool { return now.Sub(t) >= time.Hour })
			if len(x.Applied) >= x.PerHour {
				return nil
			}
			x.Applied = append(x.Applied, now)
			took, kept = true, *x
			return nil
		})
		if err != nil {
			continue
		}
		if !took {
			s.recordStanding(st, Person{Name: "Watson"}, audit.OutcomeDenied, fmt.Sprintf("not applied to proposed change %s: it approved %d this hour already, its most; the card waits for a person", c.ID, st.PerHour))
			_, _ = s.edit(ctx, c.ID, func(x *Card) error {
				addEvent(x, now, fmt.Sprintf("the standing approval “%s” approved %d this hour already; it waits for a person", st.Name, st.PerHour), "", SystemActor)
				return nil
			})
			continue
		}
		by := Approver{Person: Person{Key: "standing:" + kept.ID, Name: fmt.Sprintf("standing approval “%s” (set by %s)", kept.Name, kept.By.Name)}, Admin: true, As: kept}
		if _, err := s.approve(ctx, c.ID, by, "", kept.ID); err != nil {
			// Not used after all: the slot goes back.
			_ = s.changeStanding(ctx, func(rec *standingRecord) error {
				if i := slices.IndexFunc(rec.Standing, func(x Standing) bool { return x.ID == kept.ID }); i >= 0 {
					x := &rec.Standing[i]
					if j := slices.Index(x.Applied, now); j >= 0 {
						x.Applied = slices.Delete(x.Applied, j, j+1)
					}
				}
				return nil
			})
			_, _ = s.edit(ctx, c.ID, func(x *Card) error {
				addEvent(x, s.now(), fmt.Sprintf("the standing approval “%s” did not approve it: %v", kept.Name, err), "", SystemActor)
				return nil
			})
			continue
		}
		return
	}
}
