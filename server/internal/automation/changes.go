package automation

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ChangesKey holds parameter changes (AT29).
const ChangesKey = "changes.json"

// A change's states, and what a script's use of its values means.
const (
	ChangeWaiting  = "waiting"
	ChangeApproved = "approved"
	ChangeRejected = "rejected"
	ChangeApplied  = "applied"
	ImpactSafe     = "safe"
	ImpactApproval = "approval"
	maxChanges     = 100
)

// ImpactItem is one approved version a change touches.
type ImpactItem struct {
	Script   string `json:"script"`
	Version  int    `json:"version"`
	Decision string `json:"decision"`
	Why      string `json:"why"`
	// By is rule, for what the console worked out, or sherlock.
	By string `json:"by"`
}

// VariableChange is a change to a fleet's parameters. On a production cluster
// one that needs approval waits: the current values stay in effect, and
// the versions it touches are suspended on that fleet until it is decided.
type VariableChange struct {
	ID        int            `json:"id"`
	Cluster   string         `json:"cluster"`
	Fleet     string         `json:"fleet"`
	FleetName string         `json:"fleetName,omitempty"`
	By        Who            `json:"by"`
	At        time.Time      `json:"at"`
	Reason    string         `json:"reason,omitempty"`
	Changed   []string       `json:"changed"`
	Before    FleetVariables `json:"before"`
	After     FleetVariables `json:"after"`
	Impact    []ImpactItem   `json:"impact"`
	// Note says what could not be weighed: Sherlock was off, or failed.
	Note      string     `json:"note,omitempty"`
	State     string     `json:"state"`
	DecidedBy *Who       `json:"decidedBy,omitempty"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	// Comment is the reason given with a rejection.
	Comment string `json:"comment,omitempty"`
}

// NeedsApproval says whether any script needs the change approved.
func (c VariableChange) NeedsApproval() bool {
	for _, i := range c.Impact {
		if i.Decision == ImpactApproval {
			return true
		}
	}
	return false
}

// ChangedVariables names what differs between two sets of values, the
// fleet's and every node's. The ceiling counts: raising it changes what
// scripts may do.
func ChangedVariables(a, b FleetVariables) []string {
	seen := map[string]bool{}
	diff := func(x, y map[string]string) {
		for k, v := range x {
			if w, ok := y[k]; !ok || w != v {
				seen[k] = true
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				seen[k] = true
			}
		}
	}
	diff(a.Values, b.Values)
	nodes := map[string]bool{}
	for n := range a.Nodes {
		nodes[n] = true
	}
	for n := range b.Nodes {
		nodes[n] = true
	}
	for n := range nodes {
		diff(a.Nodes[n], b.Nodes[n])
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Impact lists the approved versions that read a changed value, and what
// the console alone can tell: a sensitive value always needs approval, and
// so does one a condition reads, since it changes what the script does.
// Sherlock may move one to needs approval, never back.
func Impact(scripts []Script, fp FleetVariables, changed []string) []ImpactItem {
	sensitive := map[string]bool{}
	for _, s := range fp.Sensitive {
		sensitive[s] = true
	}
	out := []ImpactItem{}
	for _, s := range scripts {
		for _, v := range s.Versions {
			if v.State != StateApproved {
				continue
			}
			var uses []string
			decision, why := ImpactSafe, "the value is passed through"
			for _, name := range changed {
				if _, ok := v.Header.Param(name); !ok {
					continue
				}
				uses = append(uses, name)
				cond := regexp.MustCompile(`(?m)^\s*(if|elif|while|case|\[|test)\b.*\$\{?` + regexp.QuoteMeta(name) + `\b`)
				switch {
				case sensitive[name]:
					decision, why = ImpactApproval, name+" is marked sensitive"
				case cond.MatchString(v.Text) && decision == ImpactSafe:
					decision, why = ImpactApproval, "a condition reads "+name
				}
			}
			if len(uses) > 0 {
				out = append(out, ImpactItem{Script: s.Name, Version: v.N, Decision: decision, Why: why, By: "rule"})
			}
		}
	}
	return out
}

// RaiseImpact applies Sherlock's opinion: it can only make an item
// stricter.
func RaiseImpact(items []ImpactItem, raised map[string]string) []ImpactItem {
	out := append([]ImpactItem(nil), items...)
	for i := range out {
		why, ok := raised[fmt.Sprintf("%s v%d", out[i].Script, out[i].Version)]
		if ok && out[i].Decision == ImpactSafe {
			out[i].Decision, out[i].Why, out[i].By = ImpactApproval, why, "sherlock"
		}
	}
	return out
}

type changesRecord struct {
	Next    int              `json:"next"`
	Changes []VariableChange `json:"changes"`
}

// Changes lists a cluster's parameter changes, newest first.
func (st *Store) Changes(ctx context.Context, cluster string) ([]VariableChange, error) {
	var rec changesRecord
	if _, err := st.load(ctx, ChangesKey, &rec); err != nil {
		return nil, err
	}
	out := []VariableChange{}
	for i := len(rec.Changes) - 1; i >= 0; i-- {
		if rec.Changes[i].Cluster == cluster {
			out = append(out, rec.Changes[i])
		}
	}
	return out, nil
}

// AllChanges lists every change kept, on every cluster, oldest first.
func (st *Store) AllChanges(ctx context.Context) ([]VariableChange, error) {
	var rec changesRecord
	if _, err := st.load(ctx, ChangesKey, &rec); err != nil {
		return nil, err
	}
	return append([]VariableChange{}, rec.Changes...), nil
}

// WaitingChanges lists what waits for a decision, on every cluster.
func (st *Store) WaitingChanges(ctx context.Context) ([]VariableChange, error) {
	var rec changesRecord
	if _, err := st.load(ctx, ChangesKey, &rec); err != nil {
		return nil, err
	}
	out := []VariableChange{}
	for _, c := range rec.Changes {
		if c.State == ChangeWaiting {
			out = append(out, c)
		}
	}
	return out, nil
}

// NewChange records a change.
func (st *Store) NewChange(ctx context.Context, c VariableChange) (VariableChange, error) {
	c.At = st.now().UTC()
	err := change(ctx, st, ChangesKey, 256<<10, func(rec *changesRecord) error {
		rec.Next++
		c.ID = rec.Next
		rec.Changes = append(rec.Changes, c)
		if n := len(rec.Changes) - maxChanges; n > 0 {
			kept := rec.Changes[:0:0]
			for _, x := range rec.Changes {
				if n > 0 && x.State != ChangeWaiting {
					n--
					continue
				}
				kept = append(kept, x)
			}
			rec.Changes = kept
		}
		return nil
	})
	return c, err
}

// DecideChange approves or rejects a waiting change; whoever asked for it
// cannot approve it, and a rejection says why. The caller applies an
// approved change's values.
func (st *Store) DecideChange(ctx context.Context, cluster string, id int, approve bool, comment string, who Who) (VariableChange, error) {
	comment = strings.TrimSpace(comment)
	if !approve && comment == "" {
		return VariableChange{}, fmt.Errorf("%w: say why it is rejected", ErrInvalid)
	}
	if len(comment) > MaxComment {
		return VariableChange{}, fmt.Errorf("%w: a comment is at most %d characters", ErrInvalid, MaxComment)
	}
	var out VariableChange
	err := change(ctx, st, ChangesKey, 256<<10, func(rec *changesRecord) error {
		for i := range rec.Changes {
			c := &rec.Changes[i]
			if c.ID != id || c.Cluster != cluster {
				continue
			}
			if c.State != ChangeWaiting {
				return fmt.Errorf("%w: change %d is %s", ErrInvalid, id, c.State)
			}
			if approve && who.Same(c.By) {
				return fmt.Errorf("%w: you asked for this change", ErrOwnWork)
			}
			now := st.now().UTC()
			c.State, c.DecidedBy, c.DecidedAt, c.Comment = ChangeRejected, &who, &now, comment
			if approve {
				c.State = ChangeApproved
			}
			out = *c
			return nil
		}
		return ErrNotFound
	})
	return out, err
}

// Suspended names the waiting change that holds a version on a fleet, or 0.
func Suspended(changes []VariableChange, fleet, script string, version int) int {
	for _, c := range changes {
		if c.State != ChangeWaiting || c.Fleet != fleet {
			continue
		}
		for _, i := range c.Impact {
			if i.Script == script && i.Version == version && i.Decision == ImpactApproval {
				return c.ID
			}
		}
	}
	return 0
}
