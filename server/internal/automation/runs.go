package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Run states (AU-10, AU-12).
const (
	RunRunning     = "running"
	RunSucceeded   = "succeeded"
	RunFailed      = "failed"
	RunStopped     = "stopped"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted"
)

// A node's states in a run (AU-08).
const (
	NodeWaiting     = "waiting"
	NodeRunning     = "running"
	NodeRebooting   = "rebooting"
	NodeNoChange    = "nochange"
	NodeChanged     = "changed"
	NodeFailed      = "failed"
	NodeUnreachable = "unreachable"
	NodeSkipped     = "skipped"
	NodeCancelled   = "cancelled"
)

// Rollout modes (AU-10).
const (
	RolloutAll    = "all"
	RolloutSize   = "size"
	RolloutCanary = "canary"
)

// Limits of runs (AU-07, AU-12).
const (
	MaxRuns            = 200
	maxRunsBytes       = 512 << 10
	MaxNodes           = 500
	MaxParallel        = 20
	DefaultTimeout     = 10
	MaxTimeout         = 60
	tailBytes          = 2 << 10
	OutputBytes        = 64 << 10
	staleAfter         = 2 * time.Minute
	defaultBeatEvery   = 30 * time.Second
	defaultRebootWait  = 10 * time.Minute
	defaultRebootPoll  = 15 * time.Second
	defaultKeepOutputs = 50
)

// HookRefs name what runs around each node: drain, uncordon, or
// script:<name> for an approved Kubernetes script.
type HookRefs struct {
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// Rollout says how a run goes through its nodes.
type Rollout struct {
	// Mode is all (one batch), size (batches of Size) or canary (one node,
	// then batches of Size, or all the rest when Size is 0).
	Mode string `json:"mode"`
	Size int    `json:"size,omitempty"`
	// StopAfter stops starting nodes once this many failed or were
	// unreachable; 0 never stops. The first of several batches stops on
	// any failure.
	StopAfter int `json:"stopAfter"`
	// TimeoutMinutes bounds one node's script.
	TimeoutMinutes int `json:"timeoutMinutes"`
}

// Validate checks a rollout and fills in its defaults.
func (r *Rollout) Validate() error {
	if r.Mode == "" {
		r.Mode = RolloutAll
	}
	switch r.Mode {
	case RolloutAll, RolloutCanary:
	case RolloutSize:
		if r.Size < 1 {
			return fmt.Errorf("%w: a batch size is at least 1", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: the rollout is all, size or canary", ErrInvalid)
	}
	if r.Size < 0 || r.StopAfter < 0 {
		return fmt.Errorf("%w: sizes and limits are not negative", ErrInvalid)
	}
	if r.TimeoutMinutes == 0 {
		r.TimeoutMinutes = DefaultTimeout
	}
	if r.TimeoutMinutes < 1 || r.TimeoutMinutes > MaxTimeout {
		return fmt.Errorf("%w: a node's timeout is 1 to %d minutes", ErrInvalid, MaxTimeout)
	}
	return nil
}

// Batches splits the nodes as the rollout says, in order.
func Batches(nodes []string, r Rollout) [][]string {
	if len(nodes) == 0 {
		return nil
	}
	chunks := func(ns []string, size int) [][]string {
		if size <= 0 || size >= len(ns) {
			return [][]string{ns}
		}
		var out [][]string
		for len(ns) > 0 {
			n := min(size, len(ns))
			out = append(out, ns[:n:n])
			ns = ns[n:]
		}
		return out
	}
	switch r.Mode {
	case RolloutSize:
		return chunks(nodes, r.Size)
	case RolloutCanary:
		out := [][]string{nodes[:1:1]}
		if len(nodes) > 1 {
			out = append(out, chunks(nodes[1:], r.Size)...)
		}
		return out
	}
	return [][]string{nodes}
}

// NodeResult is one node's part of a run.
type NodeResult struct {
	Node    string     `json:"node"`
	Batch   int        `json:"batch"`
	State   string     `json:"state"`
	Exit    *int       `json:"exit,omitempty"`
	Msg     string     `json:"msg,omitempty"`
	Error   string     `json:"error,omitempty"`
	Started *time.Time `json:"started,omitempty"`
	Ended   *time.Time `json:"ended,omitempty"`
	// Tail is the last lines of the output, kept with the record.
	Tail string `json:"tail,omitempty"`
	// Values are the parameters the node got.
	Values map[string]string `json:"values,omitempty"`
	// RebootMinutes is how long the node could take to come back from a
	// reboot, from its fleets; 0 is the console's default.
	RebootMinutes int `json:"rebootMinutes,omitempty"`
	// Cordoned says the run drained the node and nothing has uncordoned
	// it since.
	Cordoned bool `json:"cordoned,omitempty"`
}

// Done says whether the node's part is over.
func (n NodeResult) Done() bool {
	switch n.State {
	case NodeWaiting, NodeRunning, NodeRebooting:
		return false
	}
	return true
}

// Run is one execution of a script version on a set of nodes.
type Run struct {
	ID          int               `json:"id"`
	Script      string            `json:"script"`
	Version     int               `json:"version"`
	Hash        string            `json:"hash"`
	DryRun      bool              `json:"dryRun"`
	Privilege   string            `json:"privilege"`
	Cluster     string            `json:"cluster"`
	ClusterName string            `json:"clusterName,omitempty"`
	Fleet       string            `json:"fleet"`
	FleetName   string            `json:"fleetName,omitempty"`
	Overrides   map[string]string `json:"overrides,omitempty"`
	Rollout     Rollout           `json:"rollout"`
	By          Who               `json:"by"`
	State       string            `json:"state"`
	// Target is kubernetes for a script on the cluster, Config the
	// configuration that supplied the privilege, and Identity the service
	// account Kubernetes commands ran as, "" for the person.
	Target   string `json:"target,omitempty"`
	Config   string `json:"config,omitempty"`
	Identity string `json:"identity,omitempty"`
	// Basis says why this version could run: approved code, or not
	// approved, off production.
	Basis string `json:"basis,omitempty"`
	// Hooks name what ran around each node; RetryOf is the run this one
	// tries again.
	Hooks    []string `json:"hooks,omitempty"`
	HookRefs HookRefs `json:"hookRefs"`
	RetryOf  int      `json:"retryOf,omitempty"`
	// Note says why a run stopped early or failed to start a node.
	Note string `json:"note,omitempty"`
	// Request is the client's name for the start, so asking twice starts
	// one run; CancelAsked is a cancel that reached the record rather than
	// the console running it.
	Request     string `json:"request,omitempty"`
	CancelAsked bool   `json:"cancelAsked,omitempty"`
	// Approvers are who had approved the version when the run started.
	Approvers []Who        `json:"approvers,omitempty"`
	Started   time.Time    `json:"started"`
	Ended     *time.Time   `json:"ended,omitempty"`
	Beat      time.Time    `json:"beat"`
	Nodes     []NodeResult `json:"nodes"`
}

// Watson is how every command of this run reads in the audit log.
func (r Run) Watson() string {
	return fmt.Sprintf("Watson via %s v%d, run #%d", r.Script, r.Version, r.ID)
}

// Counts counts the nodes by state.
func (r Run) Counts() map[string]int {
	c := map[string]int{}
	for _, n := range r.Nodes {
		c[n.State]++
	}
	return c
}

// LeftCordoned are the nodes the run drained and did not uncordon.
func (r Run) LeftCordoned() []string {
	var out []string
	for _, n := range r.Nodes {
		if n.Cordoned {
			out = append(out, n.Node)
		}
	}
	return out
}

// live says whether a run still holds its nodes: running, and its console
// still beating.
func (r Run) live(now time.Time) bool {
	return r.State == RunRunning && now.Sub(r.Beat) < staleAfter
}

// Copy is a deep enough copy to hand out while the run goes on.
func (r Run) Copy() Run {
	out := r
	out.Nodes = append([]NodeResult(nil), r.Nodes...)
	return out
}

type runsRecord struct {
	Next int   `json:"next"`
	Runs []Run `json:"runs"`
}

// Runs lists the kept runs, newest first. A run still marked running whose
// leader stopped beating is shown interrupted (AU-12).
func (st *Store) Runs(ctx context.Context) ([]Run, error) {
	var rec runsRecord
	if _, err := st.load(ctx, RunsKey, &rec); err != nil {
		return nil, err
	}
	now := st.now()
	for i := range rec.Runs {
		stale(&rec.Runs[i], now)
	}
	sort.Slice(rec.Runs, func(i, j int) bool { return rec.Runs[i].ID > rec.Runs[j].ID })
	return rec.Runs, nil
}

func stale(r *Run, now time.Time) {
	if r.State != RunRunning || now.Sub(r.Beat) < staleAfter {
		return
	}
	r.State = RunInterrupted
	if r.Note == "" {
		r.Note = "the console that ran it stopped before it ended; nothing was retried"
	}
	if c := r.LeftCordoned(); len(c) > 0 {
		r.Note += "; left cordoned: " + strings.Join(c, ", ")
	}
	for i := range r.Nodes {
		if !r.Nodes[i].Done() {
			r.Nodes[i].State = NodeCancelled
			r.Nodes[i].Error = "interrupted"
		}
	}
}

// NewRun records a run and gives it its number. A node is in one run at a
// time: a run on nodes another live run has is refused, dry runs and
// scripts on the cluster aside. A request asked for again answers the run
// it started, with ErrSameRequest.
func (st *Store) NewRun(ctx context.Context, r Run) (Run, error) {
	var same Run
	err := change(ctx, st, RunsKey, maxRunsBytes, func(rec *runsRecord) error {
		now := st.now()
		for _, old := range rec.Runs {
			if r.Request != "" && old.Request == r.Request && old.By.Same(r.By) {
				same = old
				return ErrSameRequest
			}
		}
		if !r.DryRun && r.Target != TargetKubernetes {
			mine := map[string]bool{}
			for _, n := range r.Nodes {
				mine[n.Node] = true
			}
			for _, old := range rec.Runs {
				if old.Cluster != r.Cluster || old.DryRun || old.Target == TargetKubernetes || !old.live(now) {
					continue
				}
				for _, n := range old.Nodes {
					if mine[n.Node] {
						return busyError(fmt.Sprintf("%s is in run #%d (%s v%d, by %s), which is still running: wait for it to end, cancel it, or leave the node out.", n.Node, old.ID, old.Script, old.Version, old.By.Name))
					}
				}
			}
		}
		if rec.Next < 1 {
			rec.Next = 1
			for _, old := range rec.Runs {
				rec.Next = max(rec.Next, old.ID+1)
			}
		}
		r.ID = rec.Next
		rec.Next++
		rec.Runs = append(rec.Runs, r)
		pruneRuns(rec)
		return nil
	})
	if errors.Is(err, ErrSameRequest) {
		return same, err
	}
	return r, err
}

// PutRun saves a run's latest state. A run that ended stays as it ended,
// whatever a late save says; it answers whether someone asked through the
// record for the run to be cancelled.
func (st *Store) PutRun(ctx context.Context, r Run) (cancel bool, err error) {
	err = change(ctx, st, RunsKey, maxRunsBytes, func(rec *runsRecord) error {
		for i := range rec.Runs {
			if rec.Runs[i].ID != r.ID {
				continue
			}
			old := rec.Runs[i]
			if old.State != RunRunning && r.State == RunRunning {
				return errNoChange
			}
			r.CancelAsked = r.CancelAsked || old.CancelAsked
			cancel = r.CancelAsked
			rec.Runs[i] = r
			pruneRuns(rec)
			return nil
		}
		rec.Runs = append(rec.Runs, r)
		pruneRuns(rec)
		return nil
	})
	return cancel, err
}

// AskCancel marks a running run for cancelling, for the console running it
// to see at its next beat; it says whether the run was running.
func (st *Store) AskCancel(ctx context.Context, id int) (bool, error) {
	found := false
	err := change(ctx, st, RunsKey, maxRunsBytes, func(rec *runsRecord) error {
		for i := range rec.Runs {
			if rec.Runs[i].ID == id && rec.Runs[i].live(st.now()) {
				rec.Runs[i].CancelAsked, found = true, true
				return nil
			}
		}
		return errNoChange
	})
	return found, err
}

// pruneRuns keeps the newest MaxRuns, then drops tails, oldest first,
// then whole runs, until the record fits.
func pruneRuns(rec *runsRecord) {
	sort.Slice(rec.Runs, func(i, j int) bool { return rec.Runs[i].ID < rec.Runs[j].ID })
	if len(rec.Runs) > MaxRuns {
		rec.Runs = append([]Run(nil), rec.Runs[len(rec.Runs)-MaxRuns:]...)
	}
	size := func() int {
		b, _ := json.Marshal(rec)
		return len(b)
	}
	for i := 0; i < len(rec.Runs)-1 && size() > maxRunsBytes-(16<<10); i++ {
		for j := range rec.Runs[i].Nodes {
			rec.Runs[i].Nodes[j].Tail = ""
		}
	}
	for len(rec.Runs) > 1 && size() > maxRunsBytes-(16<<10) {
		rec.Runs = rec.Runs[1:]
	}
	// The newest alone too big: its tails go, then its nodes' values, which
	// its output keeps.
	if n := len(rec.Runs); n == 1 && size() > maxRunsBytes-(16<<10) {
		r := &rec.Runs[0]
		for j := range r.Nodes {
			r.Nodes[j].Tail = ""
		}
		if size() > maxRunsBytes-(16<<10) {
			for j := range r.Nodes {
				r.Nodes[j].Values = nil
			}
			if !strings.Contains(r.Note, "values not kept") {
				r.Note = strings.TrimPrefix(r.Note+"; each node's values not kept: the run is too big for its record", "; ")
			}
		}
	}
}

// Result is what a script may print as its last line.
type Result struct {
	Changed bool   `json:"changed"`
	Msg     string `json:"msg"`
	Reboot  bool   `json:"reboot"`
}

// ParseResult reads the last non-empty line of a script's output as its
// result, when it is a JSON object.
func ParseResult(out string) (Result, bool) {
	lines := strings.Split(strings.TrimRight(out, "\r\n\t "), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(last, "{") {
		return Result{}, false
	}
	var r Result
	if err := json.Unmarshal([]byte(last), &r); err != nil {
		return Result{}, false
	}
	if len(r.Msg) > 500 {
		r.Msg = strings.ToValidUTF8(r.Msg[:500], "")
	}
	return r, true
}

// tail keeps the newest bytes written to it.
type tail struct {
	mu   sync.Mutex
	max  int
	b    []byte
	lost bool
}

func newTail(max int) *tail { return &tail{max: max} }

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
		t.lost = true
	}
	return len(p), nil
}

// String is what is kept, marked when the start was dropped.
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.ToValidUTF8(string(t.b), "")
	if t.lost {
		return "[earlier output not kept]\n" + s
	}
	return s
}

// last returns the end of s, at most n bytes, on a line boundary when one
// is near.
func last(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < n/4 {
		s = s[i+1:]
	}
	return strings.ToValidUTF8(s, "")
}
