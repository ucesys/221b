package watson

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
)

// The commands of every cluster and person: reads run at once, changes
// wait on an approval card (P6-04) that shows the exact command, the
// objects it touches, a server-side dry run and its impact. An approval
// lapses after ten minutes, or when those objects change; it runs as the
// person who approved it (P6-05), behind a second person on production
// clusters when an administrator asks for that (P6-07), and every step is
// audited (P6-08).

// Statuses of a command.
const (
	StatusPending   = "pending"   // waiting for its card to be approved (or a read to be run)
	StatusQueued    = "queued"    // approved, waiting for the command before it
	StatusRunning   = "running"   // running
	StatusDone      = "done"      // finished with exit code 0
	StatusFailed    = "failed"    // finished with another exit code
	StatusStopped   = "stopped"   // stopped part way
	StatusLapsed    = "lapsed"    // its approval lapsed
	StatusDiscarded = "discarded" // discarded unrun
	StatusRefused   = "refused"   // refused by a rule, never run
)

// Audit actions.
const (
	ActionCommand = "watson.command"
	ActionApprove = "watson.approve"
	ActionRefuse  = "watson.refuse"
	ActionResult  = "watson.result"
	ActionLapse   = "watson.lapse"
	ActionDiscard = "watson.discard"
)

// Cluster is what Watson needs to know of a connection.
type Cluster struct {
	ID   string
	Name string
	// Env is production, staging, development or other.
	Env string
	// ReadOnly is a connection that may not change anything (P6-05), and
	// ReadOnlyHow says where that is set.
	ReadOnly    bool
	ReadOnlyHow string
	// Locked, when set, is why every change here is refused although the
	// connection allows them: the cluster's roles allow more than the ones
	// 221B ships, and rbac.enforce is strict (RB-36).
	Locked string
	// Config is the console's connection to it.
	Config *rest.Config
}

// Who is the person acting.
type Who struct {
	// Session is the browser session.
	Session string
	// Person is who the cluster sees; zero with sign-in off.
	Person kube.Person
	// User tells people apart for the two-person rule; empty with sign-in off.
	User string
	// Name is how the console shows them.
	Name  string
	Audit audit.Who
	From  string
}

// Line is one line of output.
type Line struct {
	// K is out, err or info (Watson's own notes).
	K string `json:"k"`
	T string `json:"t"`
}

// Plan is what an approval card shows.
type Plan struct {
	// Ready is false while Watson is still working the card out.
	Ready bool `json:"ready"`
	// Targets name the objects the command acts on.
	Targets []string `json:"targets,omitempty"`
	// Impact says what the change does, one fact per line.
	Impact []string `json:"impact,omitempty"`
	// DryRun is the server's answer to the same command with
	// --dry-run=server, as the person who asked.
	DryRun     []Line `json:"dryRun,omitempty"`
	DryRunExit int    `json:"dryRunExit"`
	// DryRunNote says something about the dry run.
	DryRunNote string `json:"dryRunNote,omitempty"`
	// DryRunning is true while the dry run runs.
	DryRunning bool `json:"dryRunning,omitempty"`
	guards     []guard
}

// Item is one command.
type Item struct {
	ID      string
	Cluster string
	cmd     *Command
	Line    string
	Change  bool
	// From says where it came from: typed, Sherlock, a button.
	From string
	// Why is the note its source gave, such as Sherlock's reason.
	Why       string
	Status    string
	Requester Who
	Created   time.Time
	Expires   time.Time
	Plan      *Plan
	Approver  *Who
	Started   time.Time
	Finished  time.Time
	Exit      int
	Lines     []Line
	bytes     int
	cut       bool
	// Reason says why it lapsed, was refused or stopped.
	Reason string
	// Rule names the rule that refused it.
	Rule string
	// Rev counts its changes, for the streams.
	Rev    int
	cancel context.CancelFunc
}

// Options configure the service.
type Options struct {
	Audit  *audit.Log
	Logger *slog.Logger
	Now    func() time.Time
	// Lapse is how long an approval card stays valid (P6-04): 10 minutes.
	Lapse time.Duration
	// CheckEvery is how often pending cards check their objects.
	CheckEvery time.Duration
	// MaxOutput bounds one command's output, in bytes.
	MaxOutput int
	// Keep is how long finished commands stay.
	Keep time.Duration
	// MaxItems bounds the commands kept per session and cluster.
	MaxItems int
	// TwoPerson says whether changes on a cluster need a second person
	// (P6-07).
	TwoPerson func(c Cluster) bool
	// Run runs a command; tests replace it. Nil runs it for real.
	Run func(ctx context.Context, c *Command, t Target, out, errOut io.Writer, extra ...string) int
}

// Service keeps the commands.
type Service struct {
	opts Options
	disc Discovery

	mu     sync.Mutex
	items  map[string]*Item
	queues map[string]*runQueue
	subs   map[*Sub]struct{}
}

type runQueue struct {
	ids     []string
	running bool
}

// Sub is a stream's subscription: the IDs of items that changed.
type Sub struct {
	Cluster string
	C       chan string
}

// New starts the service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Lapse <= 0 {
		o.Lapse = 10 * time.Minute
	}
	if o.CheckEvery <= 0 {
		o.CheckEvery = 3 * time.Second
	}
	if o.MaxOutput <= 0 {
		o.MaxOutput = 2 << 20
	}
	if o.Keep <= 0 {
		o.Keep = 12 * time.Hour
	}
	if o.MaxItems <= 0 {
		o.MaxItems = 200
	}
	if o.TwoPerson == nil {
		o.TwoPerson = func(Cluster) bool { return false }
	}
	if o.Run == nil {
		o.Run = Exec
	}
	return &Service{opts: o, items: map[string]*Item{}, queues: map[string]*runQueue{}, subs: map[*Sub]struct{}{}}
}

// Exec runs a parsed command against a target: kubectl or helm.
func Exec(ctx context.Context, c *Command, t Target, out, errOut io.Writer, extra ...string) int {
	if c.Tool == "helm" {
		if len(extra) > 0 {
			cp := *c
			cp.Flags = map[string][]string{}
			for k, v := range c.Flags {
				cp.Flags[k] = v
			}
			for _, e := range extra {
				name, value, _ := strings.Cut(strings.TrimPrefix(e, "--"), "=")
				cp.Flags[name] = []string{value}
			}
			c = &cp
		}
		return runHelm(ctx, c, t, out, errOut)
	}
	return runKubectl(ctx, c, t, out, errOut, extra...)
}

// Subscribe follows the changes of a cluster's commands.
func (s *Service) Subscribe(cluster string) *Sub {
	sub := &Sub{Cluster: cluster, C: make(chan string, 256)}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub
}

// Unsubscribe ends a subscription.
func (s *Service) Unsubscribe(sub *Sub) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

// changedLocked tells the streams an item changed.
func (s *Service) changedLocked(it *Item) {
	it.Rev++
	for sub := range s.subs {
		if sub.Cluster != it.Cluster {
			continue
		}
		select {
		case sub.C <- it.ID:
		default:
		}
	}
}

func newID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) record(action string, it *Item, who Who, outcome, detail string) {
	if s.opts.Audit == nil {
		return
	}
	d := "command=" + quote(it.Line)
	if detail != "" {
		d += " " + detail
	}
	s.opts.Audit.Record(audit.Event{Action: action, Who: who.Audit, Cluster: it.Cluster, Target: it.cmdName(), Detail: d, Outcome: outcome, From: who.From})
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

func (it *Item) cmdName() string {
	if it.cmd != nil {
		return it.cmd.Name()
	}
	return "watson"
}

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("no such command")
	ErrNotYours = errors.New("that command is someone else's")
	ErrState    = errors.New("the command cannot do that now")
)

// Submit adds commands: a block may hold several, one per line, as
// Sherlock writes them. A read runs at once when run is set; a change
// gets an approval card.
func (s *Service) Submit(ctx context.Context, cl Cluster, who Who, text, from, why string, run bool) []*Item {
	lines := SplitCommands(text)
	if len(lines) == 0 {
		lines = []string{strings.TrimSpace(text)}
	}
	var out []*Item
	for _, line := range lines {
		out = append(out, s.submitOne(cl, who, line, from, why, run))
	}
	return out
}

func (s *Service) submitOne(cl Cluster, who Who, line, from, why string, run bool) *Item {
	now := s.opts.Now()
	it := &Item{ID: newID(), Cluster: cl.ID, Line: strings.TrimSpace(line), From: from, Why: why, Requester: who, Created: now, Exit: -1}
	c, err := Parse(line)
	if err != nil {
		var r *Refusal
		it.Status, it.Reason, it.Rule = StatusRefused, err.Error(), "invalid"
		if errors.As(err, &r) {
			it.Rule = r.Rule
		}
		s.add(it)
		s.record(ActionRefuse, it, who, audit.OutcomeDenied, "rule="+quote(it.Rule)+" reason="+quote(it.Reason))
		return it
	}
	it.cmd = c
	it.Line = c.Line
	// A change asked as a dry run changes nothing: it runs like a read.
	it.Change = c.Change && !c.DryRun()
	s.record(ActionCommand, it, who, audit.OutcomeOK, map[bool]string{true: "kind=change", false: "kind=read"}[it.Change]+" from="+quote(from))
	if it.Change {
		if r := s.refusalFor(cl, who, nil); r != nil {
			it.Status, it.Reason, it.Rule = StatusRefused, r.Message, r.Rule
			s.add(it)
			s.record(ActionRefuse, it, who, audit.OutcomeDenied, "rule="+quote(r.Rule)+" reason="+quote(r.Message))
			return it
		}
		it.Status = StatusPending
		it.Expires = now.Add(s.opts.Lapse)
		it.Plan = &Plan{}
		s.add(it)
		go s.plan(cl, it.ID)
		return it
	}
	it.Status = StatusPending
	s.add(it)
	if run {
		s.mu.Lock()
		s.enqueueLocked(cl, it, who)
		s.mu.Unlock()
	}
	return it
}

// refusalFor checks the rules a change must pass (P6-05, P6-07); with an
// approver, whether that person may approve it.
func (s *Service) refusalFor(cl Cluster, requester Who, approver *Who) *Refusal {
	if cl.Locked != "" {
		return refuse("roles", "Refused: %s", cl.Locked)
	}
	if cl.ReadOnly {
		how := cl.ReadOnlyHow
		if how == "" {
			how = "An administrator can allow changes in the connection's settings."
		}
		return refuse("read-only cluster", "Refused: %s is connected read-only, so Watson changes nothing there. %s", cl.Name, how)
	}
	if s.opts.TwoPerson(cl) {
		if requester.User == "" {
			return refuse("two people", "Refused: changes to %s need a second person's approval, and with sign-in off the console cannot tell people apart. Turn sign-in on, or an administrator can turn the two-person rule off in Settings.", cl.Name)
		}
		if approver != nil && approver.User == requester.User {
			return refuse("two people", "Someone other than %s must approve this change: %s is a production cluster, where an administrator requires a second person.", requester.Name, cl.Name)
		}
	}
	return nil
}

func (s *Service) add(it *Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[it.ID] = it
	s.trimLocked(it.Requester.Session, it.Cluster)
	s.changedLocked(it)
}

// trimLocked drops the oldest finished commands past the limit.
func (s *Service) trimLocked(session, cluster string) {
	var mine []*Item
	cutoff := s.opts.Now().Add(-s.opts.Keep)
	for _, it := range s.items {
		if it.Requester.Session == session && it.Cluster == cluster {
			mine = append(mine, it)
		}
		if finished(it.Status) && !it.Finished.IsZero() && it.Finished.Before(cutoff) {
			delete(s.items, it.ID)
		}
	}
	if len(mine) <= s.opts.MaxItems {
		return
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Created.Before(mine[j].Created) })
	for _, it := range mine[:len(mine)-s.opts.MaxItems] {
		if finished(it.Status) {
			delete(s.items, it.ID)
		}
	}
}

func finished(status string) bool {
	switch status {
	case StatusDone, StatusFailed, StatusStopped, StatusLapsed, StatusDiscarded, StatusRefused:
		return true
	}
	return false
}

// plan works out an approval card: the objects, what changes, and the
// server-side dry run as the person who asked.
func (s *Service) plan(cl Cluster, id string) {
	s.mu.Lock()
	it := s.items[id]
	s.mu.Unlock()
	if it == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p := &Plan{}
	console := Target{Config: cl.Config}
	if d, err := s.disc.For(cl.ID, cl.Config); err == nil {
		console.Discovery = d
	}
	if console.Discovery != nil {
		cs, err := clientsFor(ctx, console)
		if err == nil {
			p.Targets, p.guards, p.Impact = cardFor(ctx, cs, it.cmd)
		} else {
			p.Impact = []string{"Watson could not read the cluster to work out the impact: " + err.Error()}
		}
	}
	// The impact shows while the dry run runs; approval waits for both.
	s.mu.Lock()
	if it.Status == StatusPending {
		early := *p
		early.DryRunning = true
		it.Plan = &early
		s.changedLocked(it)
	}
	s.mu.Unlock()
	// The same command with --dry-run=server, as the person who asked.
	asker := console
	asker.Person = it.Requester.Person
	var buf lineBuffer
	buf.max = 64 << 10
	extra := []string{"--dry-run=server"}
	if it.cmd.Name() == "kubectl drain" && !it.cmd.Has("timeout") {
		// A budget that allows no eviction would hold the dry run, as it
		// holds the drain: it gives up after a few retries.
		extra = append(extra, "--timeout=12s")
		p.DryRunNote = "The dry run gives up after 12 seconds; the drain itself waits as long as --timeout says."
	}
	p.DryRunExit = s.opts.Run(ctx, it.cmd, asker, &buf.out, &buf.err, extra...)
	p.DryRun = buf.lines()
	p.Ready = true
	s.mu.Lock()
	defer s.mu.Unlock()
	if it.Status == StatusPending {
		it.Plan = p
		s.changedLocked(it)
	}
}

// view finds an item a person may see.
func (s *Service) itemLocked(id string) (*Item, error) {
	it := s.items[id]
	if it == nil {
		return nil, ErrNotFound
	}
	return it, nil
}

// Visible says whether a person sees a command on a cluster: their own,
// and, where two people are needed, everyone's changes waiting for
// approval. A command is only ever seen, approved, stopped or discarded on
// the cluster it was planned for, so a card checked on one cluster never
// runs on another.
func (s *Service) Visible(it *Item, who Who, cl Cluster) bool {
	if it.Cluster != cl.ID {
		return false
	}
	if it.Requester.Session == who.Session {
		return true
	}
	if who.User != "" && it.Requester.User == who.User {
		return true
	}
	if it.Approver != nil && it.Approver.Session == who.Session {
		return true
	}
	return it.Change && it.Status == StatusPending && who.User != "" && s.opts.TwoPerson(cl)
}

// Approve approves a change (or runs a read): it checks the card still
// holds, then queues it to run as the approver.
func (s *Service) Approve(ctx context.Context, cl Cluster, who Who, id string) (*Item, error) {
	s.mu.Lock()
	it, err := s.itemLocked(id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !s.Visible(it, who, cl) {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if it.Status != StatusPending {
		s.mu.Unlock()
		return it, fmt.Errorf("%w: it is %s", ErrState, it.Status)
	}
	if !it.Change {
		if it.Requester.Session != who.Session {
			s.mu.Unlock()
			return it, ErrNotYours
		}
		s.enqueueLocked(cl, it, who)
		s.mu.Unlock()
		return it, nil
	}
	if it.Plan == nil || !it.Plan.Ready {
		s.mu.Unlock()
		return it, fmt.Errorf("%w: Watson is still working out what it changes", ErrState)
	}
	if r := s.refusalFor(cl, it.Requester, &who); r != nil {
		s.mu.Unlock()
		s.record(ActionRefuse, it, who, audit.OutcomeDenied, "rule="+quote(r.Rule)+" reason="+quote(r.Message))
		return it, r
	}
	if s.opts.Now().After(it.Expires) {
		s.lapseLocked(it, fmt.Sprintf("The approval lapsed: cards are good for %s. Send the command again for a fresh card.", minutes(s.opts.Lapse)))
		s.mu.Unlock()
		return it, fmt.Errorf("%w: %s", ErrState, it.Reason)
	}
	guards := it.Plan.guards
	s.mu.Unlock()
	// The objects must be as the card showed them (P6-04).
	if why := checkGuards(ctx, s.consoleClients(ctx, cl), guards); why != "" {
		s.mu.Lock()
		s.lapseLocked(it, why)
		s.mu.Unlock()
		return it, fmt.Errorf("%w: %s", ErrState, why)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if it.Status != StatusPending {
		return it, fmt.Errorf("%w: it is %s", ErrState, it.Status)
	}
	w := who
	it.Approver = &w
	s.record(ActionApprove, it, who, audit.OutcomeOK, "requested_by="+quote(it.Requester.Name))
	s.enqueueLocked(cl, it, who)
	return it, nil
}

func minutes(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}

func (s *Service) lapseLocked(it *Item, why string) {
	if it.Status != StatusPending {
		return
	}
	it.Status, it.Reason, it.Finished = StatusLapsed, why, s.opts.Now()
	s.changedLocked(it)
	go s.record(ActionLapse, it, it.Requester, audit.OutcomeDenied, "reason="+quote(why))
}

func (s *Service) consoleClients(ctx context.Context, cl Cluster) *clients {
	t := Target{Config: cl.Config}
	d, err := s.disc.For(cl.ID, cl.Config)
	if err != nil {
		return nil
	}
	t.Discovery = d
	cs, err := clientsFor(ctx, t)
	if err != nil {
		return nil
	}
	return cs
}

// Discard drops a command that has not run.
func (s *Service) Discard(cl Cluster, who Who, id string) (*Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, err := s.itemLocked(id)
	if err != nil || !s.Visible(it, who, cl) {
		return nil, ErrNotFound
	}
	if it.Requester.Session != who.Session && (who.User == "" || it.Requester.User != who.User) {
		return it, ErrNotYours
	}
	if it.Status != StatusPending {
		return it, fmt.Errorf("%w: it is %s", ErrState, it.Status)
	}
	it.Status, it.Finished = StatusDiscarded, s.opts.Now()
	s.changedLocked(it)
	go s.record(ActionDiscard, it, who, audit.OutcomeOK, "")
	return it, nil
}

// Edit replaces a pending command's text: the old card goes, a new one
// takes its place.
func (s *Service) Edit(ctx context.Context, cl Cluster, who Who, id, line string) ([]*Item, error) {
	it, err := s.Discard(cl, who, id)
	if err != nil {
		return nil, err
	}
	return s.Submit(ctx, cl, who, line, it.From, it.Why, false), nil
}

// Stop stops a running command, or a queued one before it starts.
func (s *Service) Stop(cl Cluster, who Who, id string) (*Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, err := s.itemLocked(id)
	if err != nil || !s.Visible(it, who, cl) {
		return nil, ErrNotFound
	}
	switch it.Status {
	case StatusRunning:
		if it.cancel != nil {
			it.cancel()
		}
	case StatusQueued:
		it.Status, it.Finished, it.Reason = StatusStopped, s.opts.Now(), "Stopped before it started."
		s.changedLocked(it)
	default:
		return it, fmt.Errorf("%w: it is %s", ErrState, it.Status)
	}
	return it, nil
}

// Clear removes a person's finished commands on a cluster.
func (s *Service) Clear(cl Cluster, who Who) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, it := range s.items {
		mine := it.Requester.Session == who.Session || (who.User != "" && it.Requester.User == who.User)
		if it.Cluster == cl.ID && mine && finished(it.Status) {
			delete(s.items, id)
			for sub := range s.subs {
				if sub.Cluster == cl.ID {
					select {
					case sub.C <- id:
					default:
					}
				}
			}
		}
	}
}

// Items lists what a person sees on a cluster, oldest first.
func (s *Service) Items(cl Cluster, who Who) []*Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Item
	for _, it := range s.items {
		if it.Cluster == cl.ID && s.Visible(it, who, cl) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Get returns one item, or nil when it is gone.
func (s *Service) Get(id string) *Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[id]
}

// Snapshot copies an item under the lock, for a stream: lines from the
// given one on.
func (s *Service) Snapshot(id string, from int) (Item, []Line, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return Item{}, nil, false
	}
	cp := *it
	if it.Plan != nil {
		p := *it.Plan
		cp.Plan = &p
	}
	if from < 0 || from > len(it.Lines) {
		from = len(it.Lines)
	}
	lines := append([]Line(nil), it.Lines[from:]...)
	cp.Lines = nil
	return cp, lines, true
}

// LineCount is how many lines an item has.
func (s *Service) LineCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.items[id]; it != nil {
		return len(it.Lines)
	}
	return 0
}

// enqueueLocked queues a command behind the person's others on the
// cluster, so they run in order.
func (s *Service) enqueueLocked(cl Cluster, it *Item, runner Who) {
	key := it.Requester.Session + "\x00" + it.Cluster
	if it.Approver != nil {
		key = it.Approver.Session + "\x00" + it.Cluster
	}
	q := s.queues[key]
	if q == nil {
		q = &runQueue{}
		s.queues[key] = q
	}
	it.Status = StatusQueued
	if it.Approver == nil {
		w := runner
		it.Approver = &w
	}
	s.changedLocked(it)
	q.ids = append(q.ids, it.ID)
	if !q.running {
		q.running = true
		go s.work(cl, key, q)
	}
}

func (s *Service) work(cl Cluster, key string, q *runQueue) {
	for {
		s.mu.Lock()
		if len(q.ids) == 0 {
			q.running = false
			delete(s.queues, key)
			s.mu.Unlock()
			return
		}
		id := q.ids[0]
		q.ids = q.ids[1:]
		it := s.items[id]
		if it == nil || it.Status != StatusQueued {
			s.mu.Unlock()
			continue
		}
		var guards []guard
		if it.Change && it.Plan != nil {
			guards = it.Plan.guards
		}
		if len(guards) > 0 {
			// A change that waited behind another, a drain held up by a
			// budget, say, must still find its objects as the card showed
			// them when it starts (P6-04).
			s.mu.Unlock()
			why := checkGuards(context.Background(), s.consoleClients(context.Background(), cl), guards)
			s.mu.Lock()
			if it.Status != StatusQueued {
				s.mu.Unlock()
				continue
			}
			if why != "" {
				it.Status, it.Reason, it.Finished = StatusLapsed, why, s.opts.Now()
				s.changedLocked(it)
				go s.record(ActionLapse, it, *it.Approver, audit.OutcomeDenied, "reason="+quote(why))
				s.mu.Unlock()
				continue
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		it.cancel = cancel
		it.Status, it.Started = StatusRunning, s.opts.Now()
		runner := *it.Approver
		s.changedLocked(it)
		s.mu.Unlock()
		s.execute(ctx, cl, it, runner)
		cancel()
	}
}

// execute runs one command as the person who approved it.
func (s *Service) execute(ctx context.Context, cl Cluster, it *Item, runner Who) {
	t := Target{Config: cl.Config, Person: runner.Person}
	d, err := s.disc.For(cl.ID, cl.Config)
	code := 1
	out := &itemWriter{s: s, it: it, k: "out"}
	errOut := &itemWriter{s: s, it: it, k: "err"}
	if err != nil {
		fmt.Fprintf(errOut, "watson: cannot reach the cluster: %v\n", err)
	} else {
		t.Discovery = d
		code = s.opts.Run(ctx, it.cmd, t, out, errOut)
	}
	out.flush()
	errOut.flush()
	s.mu.Lock()
	it.Exit, it.Finished, it.cancel = code, s.opts.Now(), nil
	outcome := audit.OutcomeOK
	switch {
	case ctx.Err() != nil:
		it.Status, it.Reason, outcome = StatusStopped, "Stopped by "+runner.Name+".", audit.OutcomeStopped
	case code == 0:
		it.Status = StatusDone
	default:
		it.Status, outcome = StatusFailed, audit.OutcomeFailed
	}
	if it.cut {
		it.Lines = append(it.Lines, Line{K: "info", T: fmt.Sprintf("# watson: output cut at %d MB", s.opts.MaxOutput>>20)})
	}
	s.changedLocked(it)
	s.mu.Unlock()
	s.record(ActionResult, it, runner, outcome, fmt.Sprintf("exit=%d duration=%s", code, it.Finished.Sub(it.Started).Round(time.Millisecond)))
}

// itemWriter appends a command's output to it line by line.
type itemWriter struct {
	s       *Service
	it      *Item
	k       string
	partial []byte
}

func (w *itemWriter) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	var lines []Line
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, Line{K: w.k, T: strings.TrimRight(string(w.partial[:i]), "\r")})
		w.partial = w.partial[i+1:]
	}
	if len(lines) > 0 {
		w.push(lines)
	}
	return len(p), nil
}

func (w *itemWriter) flush() {
	if len(w.partial) > 0 {
		w.push([]Line{{K: w.k, T: string(w.partial)}})
		w.partial = nil
	}
}

func (w *itemWriter) push(lines []Line) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	for _, l := range lines {
		if w.it.bytes+len(l.T) > w.s.opts.MaxOutput {
			w.it.cut = true
			return
		}
		w.it.bytes += len(l.T) + 1
		w.it.Lines = append(w.it.Lines, l)
	}
	w.s.changedLocked(w.it)
}

// lineBuffer collects output for a card's dry run.
type lineBuffer struct {
	out, err bytes.Buffer
	max      int
}

func (b *lineBuffer) lines() []Line {
	var out []Line
	add := func(k string, buf *bytes.Buffer) {
		for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if l != "" {
				out = append(out, Line{K: k, T: l})
			}
		}
	}
	add("out", &b.out)
	add("err", &b.err)
	if len(out) > 200 {
		n := len(out)
		out = append(out[:200], Line{K: "info", T: fmt.Sprintf("# … and %d more lines", n-200)})
	}
	return out
}

// Watch lapses pending cards whose time is up or whose objects changed,
// until ctx ends.
func (s *Service) Watch(ctx context.Context, clusterOf func(id string) (Cluster, bool)) {
	t := time.NewTicker(s.opts.CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		now := s.opts.Now()
		type check struct {
			it     *Item
			guards []guard
		}
		var checks []check
		for _, it := range s.items {
			if it.Status != StatusPending || !it.Change {
				continue
			}
			if now.After(it.Expires) {
				s.lapseLocked(it, fmt.Sprintf("The approval lapsed: cards are good for %s. Send the command again for a fresh card.", minutes(s.opts.Lapse)))
				continue
			}
			if it.Plan != nil && it.Plan.Ready && len(it.Plan.guards) > 0 {
				checks = append(checks, check{it, it.Plan.guards})
			}
		}
		s.mu.Unlock()
		for _, c := range checks {
			cl, ok := clusterOf(c.it.Cluster)
			if !ok {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			why := checkGuards(cctx, s.consoleClients(cctx, cl), c.guards)
			cancel()
			if why != "" {
				s.mu.Lock()
				s.lapseLocked(c.it, why)
				s.mu.Unlock()
			}
		}
	}
}
