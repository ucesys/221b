package watson

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// What Watson does for the durable cards of agents' proposals (Phase 12,
// ADR-99, ADR-100): it works out a change's objects, impact and a
// fingerprint of them when an agent proposes it and again when a person
// approves it, and once the card's people have approved it, it runs the
// change as the last of them, in their Watson panel, without a card of
// its own: the durable card was that card.

// Inspection is what a change would act on now, read with the console's
// own account: an agent that proposes it has no person to read as.
type Inspection struct {
	// Line is the command as Watson reads it.
	Line string `json:"line"`
	// Targets name the objects; Impact says what changes, one fact a line.
	Targets []string `json:"targets,omitempty"`
	Impact  []string `json:"impact,omitempty"`
	// Fingerprint is a digest of every part of those objects the command
	// depends on: the same guards that lapse a Watson card (ADR-51).
	Fingerprint string `json:"fingerprint"`
}

// ErrNotChange is a line a durable card cannot carry: a read runs at
// once, without anyone's approval.
var ErrNotChange = errors.New("not a change")

// Inspect parses a change and reads what it would act on now. An object
// that cannot be read is an error: a fingerprint without it would prove
// nothing.
func (s *Service) Inspect(ctx context.Context, cl Cluster, line string) (Inspection, error) {
	c, err := Parse(line)
	if err != nil {
		return Inspection{}, err
	}
	if !c.Change || c.DryRun() {
		return Inspection{}, fmt.Errorf("%w: %s reads, and runs without an approval", ErrNotChange, c.Name())
	}
	out := Inspection{Line: c.Line}
	if cl.Config == nil {
		return out, errors.New("the cluster cannot be reached")
	}
	t := Target{Config: cl.Config, Cluster: cl.ID, Apps: s.opts.Apps, Repos: s.opts.Repos, pin: &appPin{}}
	d, err := s.disc.For(cl.ID, cl.Config)
	if err != nil {
		return out, fmt.Errorf("cannot reach the cluster: %w", err)
	}
	t.Discovery = d
	cs, err := clientsFor(ctx, t)
	if err != nil {
		return out, fmt.Errorf("cannot reach the cluster: %w", err)
	}
	targets, guards, impact := cardFor(ctx, cs, c, t)
	out.Targets, out.Impact = targets, impact
	parts := make([]string, 0, len(guards))
	for _, g := range guards {
		got, err := g.read(ctx, cs)
		if err != nil {
			return out, fmt.Errorf("Watson could not read %s: %w", g.what, err)
		}
		parts = append(parts, g.what+"\x00"+got)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(c.Line + "\x00" + strings.Join(parts, "\x01")))
	out.Fingerprint = hex.EncodeToString(sum[:16])
	return out, nil
}

// RunApproved runs a change whose durable card its people approved: the
// cluster's rules still hold (read-only, locked), the change runs as the
// approver given, the last to approve, and its output shows in their
// Watson panel like any command of theirs. from and why say where it came
// from: "Agent proposal" and the card's title.
func (s *Service) RunApproved(cl Cluster, approver Who, line, from, why string, inspected *Inspection) (*Item, error) {
	c, err := Parse(line)
	if err != nil {
		return nil, err
	}
	if !c.Change || c.DryRun() {
		return nil, fmt.Errorf("%w: %s", ErrNotChange, c.Name())
	}
	now := s.opts.Now()
	it := &Item{ID: newID(), Cluster: cl.ID, cmd: c, Line: c.Line, Change: true, From: from, Why: why, Requester: approver, Created: now, Exit: -1}
	s.record(ActionCommand, it, approver, audit.OutcomeOK, "kind=change from="+quote(from))
	if r := s.refusalFor(cl, Who{}, nil, false, false); r != nil && r.Rule != "two people" {
		it.Status, it.Reason, it.Rule = StatusRefused, r.Message, r.Rule
		s.add(it)
		s.record(ActionRefuse, it, approver, audit.OutcomeDenied, "rule="+quote(r.Rule)+" reason="+quote(r.Message))
		return it, r
	}
	p := &Plan{Ready: true}
	if inspected != nil {
		p.Targets, p.Impact = inspected.Targets, inspected.Impact
	}
	it.Status, it.Plan, it.Expires = StatusPending, p, now.Add(s.opts.Lapse)
	s.add(it)
	s.mu.Lock()
	w := approver
	it.Approver, it.Approved = &w, now
	s.record(ActionApprove, it, approver, audit.OutcomeOK, "approval=\"a durable card's\"")
	s.enqueueLocked(cl, it, approver)
	s.mu.Unlock()
	return it, nil
}

// WaitDone waits until a command has finished, or ctx ends, and returns
// it as it is then.
func (s *Service) WaitDone(ctx context.Context, id string) (Item, bool) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		it, _, ok := s.Snapshot(id, -1)
		if !ok {
			return Item{}, false
		}
		if finished(it.Status) {
			return it, true
		}
		select {
		case <-ctx.Done():
			return it, false
		case <-t.C:
		}
	}
}

// Supersede discards a pending card that something else took the place
// of, such as a durable card approved for the same command.
func (s *Service) Supersede(id, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil || it.Status != StatusPending {
		return
	}
	it.Status, it.Reason, it.Finished = StatusDiscarded, why, s.opts.Now()
	s.changedLocked(it)
	go s.record(ActionDiscard, it, it.Requester, audit.OutcomeOK, "reason="+quote(why))
}

// SetOnEnd sets what hears of every change that ends: run, failed,
// stopped, lapsed or discarded. The API sets it once it is built, before
// any request.
func (s *Service) SetOnEnd(f func(Item)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEnd = f
}

// AddOnFinish adds what hears of every command that ends, read or change,
// whatever its end: a case logs how the commands run from it ended (Phase
// 12, wave 6f, CF-48). The API adds it once it is built, before any
// request.
func (s *Service) AddOnFinish(f func(Item)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onFinish = append(s.onFinish, f)
}

// endedLocked tells the hooks a command ended.
func (s *Service) endedLocked(it *Item) {
	if !finished(it.Status) {
		return
	}
	cp := *it
	cp.Lines, cp.Plan = nil, nil
	for _, f := range s.onFinish {
		go f(cp)
	}
	if s.onEnd == nil || !it.Change {
		return
	}
	go s.onEnd(cp)
}
