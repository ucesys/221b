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

	"k8s.io/client-go/discovery"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
)

// What Watson does for the durable cards of agents' proposals (Phase 12,
// ADR-99, ADR-100): it works out a change's objects, impact and a
// fingerprint of them when an agent proposes it and again when a person
// approves it, and once the card's people have approved it, it runs the
// change as the last of them, in their Watson panel, without a card of
// its own: the durable card was that card.

// Inspection is what a change would act on now. Its targets and impact
// are read as the person a card was asked for, as an approval card's are
// (ADR-51); only an agent's own card, with no person to read as, reads
// them with the console's account. Its fingerprint is read with the
// console's account: it says only whether the objects changed since.
type Inspection struct {
	// Line is the command as Watson reads it.
	Line string `json:"line"`
	// Targets name the objects; Impact says what changes, one fact a line.
	Targets []string `json:"targets,omitempty"`
	Impact  []string `json:"impact,omitempty"`
	// Fingerprint is a digest of every part of those objects the command
	// depends on: the same guards that lapse a Watson card (ADR-51).
	Fingerprint string `json:"fingerprint"`
	// Refusal says why the change may not run, or "": it removes values
	// from the console's own release.
	Refusal string `json:"refusal,omitempty"`
}

// ErrNotChange is a line a durable card cannot carry: a read runs at
// once, without anyone's approval.
var ErrNotChange = errors.New("not a change")

// Inspect parses a change and reads what it would act on now. as is the
// person the card was asked for: its targets and impact are read as them,
// so it shows nothing their own access does not; nil reads them with the
// console's account, for an agent's own card. The fingerprint is read with
// the console's account whoever asked, so the same objects give the same
// fingerprint. An object that cannot be read for it is an error: a
// fingerprint without it would prove nothing.
func (s *Service) Inspect(ctx context.Context, cl Cluster, line string, as *kube.Person) (Inspection, error) {
	return s.inspect(ctx, cl, line, as, true)
}

// Fingerprint is Inspect without the targets and impact: what a card's
// recheck compares, read with the console's account.
func (s *Service) Fingerprint(ctx context.Context, cl Cluster, line string) (Inspection, error) {
	return s.inspect(ctx, cl, line, nil, false)
}

func (s *Service) inspect(ctx context.Context, cl Cluster, line string, as *kube.Person, describe bool) (out Inspection, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Watson failed reading what it acts on: %v", r)
		}
	}()
	c, err := Parse(line)
	if err != nil {
		return Inspection{}, err
	}
	if !c.Change || c.DryRun() {
		return Inspection{}, fmt.Errorf("%w: %s reads, and runs without an approval", ErrNotChange, c.Name())
	}
	out = Inspection{Line: c.Line}
	w, err := s.consoleCard(ctx, cl, c)
	if err != nil {
		return out, err
	}
	d, cs, guards, alert, refusal := w.disc, w.cs, w.guards, w.alert, w.refusal
	targets, impact := w.targets, w.impact
	out.Refusal = refusal
	if refusal != "" {
		// The card says why it will not run, as a Watson card does.
		impact = append(impact, refusal)
	}
	switch {
	case !describe:
	case as == nil || as.Nobody():
		out.Targets, out.Impact = targets, withAlert(alert, impact)
	default:
		// What the card shows is read again as the person: what they may
		// not see is left out, and the card says so.
		asker := Target{Config: cl.Config, Person: *as, Cluster: cl.ID, Apps: s.opts.Apps, Repos: s.opts.Repos, Credentials: s.opts.Credentials, Discovery: d, pin: &appPin{}}
		acs, err := clientsFor(ctx, asker)
		if err != nil {
			out.Impact = withAlert(alert, []string{"Watson could not read the cluster to work out the impact: " + err.Error()})
			break
		}
		var asImpact []string
		out.Targets, _, asImpact = cardFor(ctx, acs, c, asker)
		if refusal != "" {
			asImpact = append(asImpact, refusal)
		}
		out.Impact = withAlert(alert, asImpact)
	}
	if out.Fingerprint, err = fingerprintOf(ctx, cs, c.Line, guards, w.pin); err != nil {
		return out, err
	}
	return out, nil
}

// fingerprintOf is the digest of everything a change depends on: what its
// guards read, with the console's account, and for a deploy the commit
// and the values its card settled on, so a branch that moved or values
// saved since give another fingerprint (HA-07).
func fingerprintOf(ctx context.Context, cs *clients, line string, guards []guard, pin *appPin) (string, error) {
	parts := make([]string, 0, len(guards)+1)
	for _, g := range guards {
		got, err := g.read(ctx, cs)
		if err != nil {
			return "", fmt.Errorf("Watson could not read %s: %w", g.what, err)
		}
		parts = append(parts, g.what+"\x00"+got)
	}
	if pin != nil && pin.commit != "" {
		parts = append(parts, "deploy\x00"+pin.src.String()+"\x00"+pin.commit+"\x00"+pin.values)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(line + "\x00" + strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:16]), nil
}

// consoleView is a change's card worked out with the console's account:
// what a durable card's fingerprint, refusal, pin and guards come from.
type consoleView struct {
	disc    discovery.CachedDiscoveryInterface
	cs      *clients
	targets []string
	guards  []guard
	impact  []string
	pin     *appPin
	tone    string
	alert   string
	refusal string
}

// consoleCard works out a change's card with the console's account.
func (s *Service) consoleCard(ctx context.Context, cl Cluster, c *Command) (*consoleView, error) {
	if cl.Config == nil {
		return nil, errors.New("the cluster cannot be reached")
	}
	w := &consoleView{pin: &appPin{}}
	t := Target{Config: cl.Config, Cluster: cl.ID, Apps: s.opts.Apps, Repos: s.opts.Repos, Credentials: s.opts.Credentials, pin: w.pin}
	d, err := s.disc.For(cl.ID, cl.Config)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the cluster: %w", err)
	}
	t.Discovery, w.disc = d, d
	if w.cs, err = clientsFor(ctx, t); err != nil {
		return nil, fmt.Errorf("cannot reach the cluster: %w", err)
	}
	w.targets, w.guards, w.impact = cardFor(ctx, w.cs, c, t)
	w.tone, w.alert, w.refusal = s.cardAlert(cl.ID, c, w.pin)
	return w, nil
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
	// The card is worked out again here, whatever the caller says of it:
	// a refusal it holds is Watson's to find (asked November 24, 2026,
	// after a durable card upgraded the console's own release past it).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	w, werr := s.consoleCard(ctx, cl, c)
	if werr != nil {
		r := refuse("unchecked", "Refused: Watson could not work out what this change does now, so it ran nothing: %v", werr)
		it.Status, it.Reason, it.Rule = StatusRefused, r.Message, r.Rule
		s.add(it)
		s.record(ActionRefuse, it, approver, audit.OutcomeDenied, "rule="+quote(r.Rule)+" reason="+quote(r.Message))
		return it, r
	}
	if w.refusal != "" {
		r := refuse("own release", "%s", w.refusal)
		it.Status, it.Reason, it.Rule = StatusRefused, r.Message, r.Rule
		s.add(it)
		s.record(ActionRefuse, it, approver, audit.OutcomeDenied, "rule="+quote(r.Rule)+" reason="+quote(r.Message))
		return it, r
	}
	// What was approved is what runs: the objects as the card's last check
	// read them, and for a deploy the commit and values it showed. The
	// guards hold the objects to that when the change starts, and the pin
	// holds the deploy to it (P6-04, HA-07).
	if inspected != nil && inspected.Fingerprint != "" {
		fp, err := fingerprintOf(ctx, w.cs, c.Line, w.guards, w.pin)
		if err != nil || fp != inspected.Fingerprint {
			why := "what it acts on changed since its card was checked"
			if err != nil {
				why = err.Error()
			}
			r := refuse("changed", "Refused: %s, so it ran nothing. A fresh proposed change shows the state as it is.", why)
			it.Status, it.Reason, it.Rule = StatusRefused, r.Message, r.Rule
			s.add(it)
			s.record(ActionRefuse, it, approver, audit.OutcomeDenied, "rule="+quote(r.Rule)+" reason="+quote(r.Message))
			return it, r
		}
	}
	p := &Plan{Ready: true, Targets: w.targets, Impact: withAlert(w.alert, w.impact), Tone: w.tone, Alert: w.alert, guards: w.guards, pin: w.pin}
	if inspected != nil {
		p.Targets, p.Impact = inspected.Targets, inspected.Impact
	}
	it.Status, it.Plan, it.Expires = StatusPending, p, now.Add(s.opts.Lapse)
	s.add(it)
	s.mu.Lock()
	by := approver
	it.Approver, it.Approved = &by, now
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
