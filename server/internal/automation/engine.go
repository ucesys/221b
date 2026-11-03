package automation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Executor runs one command on a node over SSH (AU-08). asNobody drops to
// the nobody user before the command starts. The console's SSH service
// implements it with Phase 11's pairings.
type Executor interface {
	Exec(ctx context.Context, cluster, node, keyID, command string, asNobody bool, stdin io.Reader, out io.Writer) (int, error)
}

// Options configure the engine.
type Options struct {
	Store *Store
	Exec  Executor
	// Unreachable says whether an Exec error means the node could not be
	// reached, as opposed to a command that failed.
	Unreachable func(error) bool
	Logger      *slog.Logger
	Now         func() time.Time
	// OutputDir, when set, keeps each run's output in a file there.
	OutputDir string
	// OnEnd is called once a run has ended, for the audit log.
	OnEnd func(Run)
	// OnCommand is called for every command a run gives, in Watson's
	// words, for the audit log. Outcome is ok, failed or stopped.
	OnCommand func(r Run, node, what, outcome string)
	// Kube runs the kubectl lines of Kubernetes scripts and hooks.
	Kube KubeRunner
	// RebootWait bounds the wait for a rebooted node; RebootPoll is how
	// often it is tried; BeatEvery how often a running run says it lives.
	RebootWait, RebootPoll, BeatEvery time.Duration
	// MaxParallel bounds the nodes a batch runs at once.
	MaxParallel int
}

// Engine runs scripts on nodes, on the leader.
type Engine struct {
	opts Options
	mu   sync.Mutex
	// active are the runs this console is running; outputs the full
	// output of the newest runs, by run and node.
	active  map[int]*activeRun
	outputs map[int]map[string]string
	order   []int
}

type activeRun struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	run    Run
	text   string
	values map[string]map[string]string
	out    map[string]*tail
	// secrets are the values of secret parameters, in memory for the run.
	secrets map[string]map[string]string
	header  Header
	before  []Hook
	after   []Hook
	person  Person
}

// NewEngine makes an engine.
func NewEngine(o Options) *Engine {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Unreachable == nil {
		o.Unreachable = func(error) bool { return false }
	}
	if o.RebootWait <= 0 {
		o.RebootWait = defaultRebootWait
	}
	if o.RebootPoll <= 0 {
		o.RebootPoll = defaultRebootPoll
	}
	if o.BeatEvery <= 0 {
		o.BeatEvery = defaultBeatEvery
	}
	if o.MaxParallel <= 0 || o.MaxParallel > MaxParallel {
		o.MaxParallel = MaxParallel
	}
	return &Engine{opts: o, active: map[int]*activeRun{}, outputs: map[int]map[string]string{}}
}

// Spec is a run to start: the version, the targets with each node's
// values, and how to go through them.
type Spec struct {
	Script      string
	Version     Version
	DryRun      bool
	Cluster     string
	ClusterName string
	Fleet       string
	FleetName   string
	// Nodes are the targets in order; Values each one's parameters.
	Nodes     []string
	Values    map[string]map[string]string
	Overrides map[string]string
	Rollout   Rollout
	Who       Who
	// Secrets are each node's secret values, never recorded: Values holds
	// their references.
	Secrets map[string]map[string]string
	// Config names the configuration; Identity is the service account
	// Kubernetes commands run as, and Person who started the run.
	Config   string
	Identity string
	Person   Person
	// Basis says why this version may run.
	Basis string
	// Before and After run around each node.
	Before, After []Hook
	HookRefs      HookRefs
	RetryOf       int
}

// Start records a run and starts it; it goes on after the request ends.
func (e *Engine) Start(ctx context.Context, sp Spec) (Run, error) {
	if len(sp.Nodes) == 0 {
		return Run{}, fmt.Errorf("%w: no node to run on", ErrInvalid)
	}
	if len(sp.Nodes) > MaxNodes {
		return Run{}, fmt.Errorf("%w: at most %d nodes in a run", ErrInvalid, MaxNodes)
	}
	if err := sp.Rollout.Validate(); err != nil {
		return Run{}, err
	}
	now := e.opts.Now().UTC()
	r := Run{Script: sp.Script, Version: sp.Version.N, Hash: sp.Version.Hash, DryRun: sp.DryRun, Privilege: sp.Version.Header.Privilege,
		Cluster: sp.Cluster, ClusterName: sp.ClusterName, Fleet: sp.Fleet, FleetName: sp.FleetName, Overrides: sp.Overrides, Rollout: sp.Rollout,
		By: sp.Who, State: RunRunning, Started: now, Beat: now, Target: sp.Version.Header.Target, Config: sp.Config, Identity: sp.Identity, Basis: sp.Basis, HookRefs: sp.HookRefs, RetryOf: sp.RetryOf}
	for _, h := range sp.Before {
		r.Hooks = append(r.Hooks, "before: "+h.Label)
	}
	for _, h := range sp.After {
		r.Hooks = append(r.Hooks, "after: "+h.Label)
	}
	for bi, batch := range Batches(sp.Nodes, sp.Rollout) {
		for _, n := range batch {
			r.Nodes = append(r.Nodes, NodeResult{Node: n, Batch: bi + 1, State: NodeWaiting, Values: sp.Values[n]})
		}
	}
	r, err := e.opts.Store.NewRun(ctx, r)
	if err != nil {
		return Run{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	answer := r.Copy()
	a := &activeRun{cancel: cancel, run: r.Copy(), text: sp.Version.Text, values: sp.Values, out: map[string]*tail{}, secrets: sp.Secrets, header: sp.Version.Header,
		before: sp.Before, after: sp.After, person: sp.Person}
	e.mu.Lock()
	e.active[r.ID] = a
	e.mu.Unlock()
	go e.execute(runCtx, a)
	return answer, nil
}

// SetKube gives the engine what runs kubectl lines; the API server has it.
func (e *Engine) SetKube(k KubeRunner) { e.opts.Kube = k }

// Cancel stops a running run: no new node starts, running ones end.
func (e *Engine) Cancel(id int) bool {
	e.mu.Lock()
	a, ok := e.active[id]
	e.mu.Unlock()
	if ok {
		a.cancel()
	}
	return ok
}

// Running says whether this console runs a run now.
func (e *Engine) Running(id int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.active[id]
	return ok
}

// Runs lists the kept runs, newest first, the ones running here as they
// are now.
func (e *Engine) Runs(ctx context.Context) ([]Run, error) {
	list, err := e.opts.Store.Runs(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[int]bool{}
	for i := range list {
		if a, ok := e.active[list[i].ID]; ok {
			a.mu.Lock()
			list[i] = a.run.Copy()
			a.mu.Unlock()
		}
		seen[list[i].ID] = true
	}
	for id, a := range e.active {
		if !seen[id] {
			a.mu.Lock()
			list = append(list, a.run.Copy())
			a.mu.Unlock()
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return list, nil
}

// Get returns one run and the output of each of its nodes this console
// still has.
func (e *Engine) Get(ctx context.Context, id int) (Run, map[string]string, bool, error) {
	e.mu.Lock()
	a, ok := e.active[id]
	kept := e.outputs[id]
	e.mu.Unlock()
	if ok {
		a.mu.Lock()
		defer a.mu.Unlock()
		out := map[string]string{}
		for n, t := range a.out {
			out[n] = t.String()
		}
		return a.run.Copy(), out, true, nil
	}
	list, err := e.opts.Store.Runs(ctx)
	if err != nil {
		return Run{}, nil, false, err
	}
	for _, r := range list {
		if r.ID == id {
			out := map[string]string{}
			for n, s := range kept {
				out[n] = s
			}
			if len(out) == 0 && e.opts.OutputDir != "" {
				out = readOutputs(e.opts.OutputDir, id)
			}
			return r, out, true, nil
		}
	}
	return Run{}, nil, false, nil
}

// save writes the run's state; a failure is logged, the run goes on.
func (e *Engine) save(a *activeRun) {
	a.mu.Lock()
	r := a.run.Copy()
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := e.opts.Store.PutRun(ctx, r); err != nil {
		e.opts.Logger.Warn("saving an automation run failed", "run", r.ID, "err", err)
	}
}

func (e *Engine) setNode(a *activeRun, node string, f func(*NodeResult)) {
	a.mu.Lock()
	for i := range a.run.Nodes {
		if a.run.Nodes[i].Node == node {
			f(&a.run.Nodes[i])
		}
	}
	a.mu.Unlock()
	e.save(a)
}

// execute goes through the batches (AU-10).
func (e *Engine) execute(ctx context.Context, a *activeRun) {
	defer a.cancel()
	beat := time.NewTicker(e.opts.BeatEvery)
	stopBeat := make(chan struct{})
	go func() {
		for {
			select {
			case <-beat.C:
				a.mu.Lock()
				a.run.Beat = e.opts.Now().UTC()
				a.mu.Unlock()
				e.save(a)
			case <-stopBeat:
				return
			}
		}
	}()
	a.mu.Lock()
	ro := a.run.Rollout
	batches := map[int][]string{}
	last := 0
	for _, n := range a.run.Nodes {
		batches[n.Batch] = append(batches[n.Batch], n.Node)
		last = max(last, n.Batch)
	}
	a.mu.Unlock()
	note := ""
	failures := func() int {
		a.mu.Lock()
		defer a.mu.Unlock()
		c := 0
		for _, n := range a.run.Nodes {
			if n.State == NodeFailed || n.State == NodeUnreachable {
				c++
			}
		}
		return c
	}
	for b := 1; b <= last && note == "" && ctx.Err() == nil; b++ {
		sem := make(chan struct{}, e.opts.MaxParallel)
		var wg sync.WaitGroup
		for _, node := range batches[b] {
			if ctx.Err() != nil || (ro.StopAfter > 0 && failures() >= ro.StopAfter) {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(node string) {
				defer wg.Done()
				defer func() { <-sem }()
				e.node(ctx, a, node, ro)
			}(node)
		}
		wg.Wait()
		switch f := failures(); {
		case ro.StopAfter > 0 && f >= ro.StopAfter:
			note = fmt.Sprintf("stopped after %d failed or unreachable nodes, the limit", f)
		case b == 1 && last > 1 && f > 0:
			note = "stopped: the first batch had a failure"
		}
	}
	beat.Stop()
	close(stopBeat)
	now := e.opts.Now().UTC()
	a.mu.Lock()
	cancelled := ctx.Err() != nil
	for i := range a.run.Nodes {
		if !a.run.Nodes[i].Done() {
			if cancelled {
				a.run.Nodes[i].State = NodeCancelled
			} else {
				a.run.Nodes[i].State = NodeSkipped
			}
		}
	}
	c := a.run.Counts()
	switch {
	case cancelled:
		a.run.State = RunCancelled
	case note != "":
		a.run.State, a.run.Note = RunStopped, note
	case c[NodeFailed]+c[NodeUnreachable] > 0:
		a.run.State = RunFailed
	default:
		a.run.State = RunSucceeded
	}
	a.run.Ended, a.run.Beat = &now, now
	r := a.run.Copy()
	outs := map[string]string{}
	for n, t := range a.out {
		outs[n] = t.String()
	}
	a.mu.Unlock()
	e.save(a)
	e.mu.Lock()
	delete(e.active, r.ID)
	e.outputs[r.ID] = outs
	e.order = append(e.order, r.ID)
	for len(e.order) > defaultKeepOutputs {
		delete(e.outputs, e.order[0])
		e.order = e.order[1:]
	}
	e.mu.Unlock()
	if e.opts.OutputDir != "" {
		writeOutputs(e.opts.OutputDir, r.ID, outs, e.opts.Logger)
	}
	if e.opts.OnEnd != nil {
		e.opts.OnEnd(r)
	}
}

// Env is what every node's script gets besides its parameters (AU-08).
func Env(r Run, node string, values map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range values {
		env[k] = v
	}
	env["DRY_RUN"] = "0"
	if r.DryRun {
		env["DRY_RUN"] = "1"
	}
	env["NODE"], env["CLUSTER"], env["FLEET"], env["RUN_ID"] = node, r.Cluster, r.Fleet, strconv.Itoa(r.ID)
	return env
}

// Command is the command a node runs: the environment, then a shell that
// reads the script on its standard input.
func Command(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("env")
	for _, k := range keys {
		b.WriteString(" " + k + "=" + quote(env[k]))
	}
	b.WriteString(" sh -s")
	return b.String()
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// KeyID is the SSH certificate's key ID for a node's connection: sshd's
// log on the node names the person and the run (AU-13).
func KeyID(r Run) string {
	who := r.By.User
	if who == "" {
		who = r.By.Name
	}
	if who == "" {
		who = "console"
	}
	return fmt.Sprintf("%s via 221B automation run %d", who, r.ID)
}

// command records one command in Watson's words.
func (e *Engine) command(r Run, node, what, outcome string) {
	if e.opts.OnCommand != nil {
		e.opts.OnCommand(r, node, what, outcome)
	}
}

func outcomeOf(failed bool) string {
	if failed {
		return "failed"
	}
	return "ok"
}

// kube runs kubectl lines, each checked against what the header declares,
// and stops at the first that fails. It says whether any line ran that can
// change something.
func (e *Engine) kube(ctx context.Context, r Run, a *activeRun, node, identity string, h Header, lines []string, env map[string]string, out io.Writer) (bool, error) {
	if e.opts.Kube == nil {
		return false, errors.New("this console cannot run Kubernetes commands")
	}
	changed := false
	for _, raw := range lines {
		line, err := ExpandKube(raw, env)
		if err == nil {
			var action KubeAction
			if action, err = KubeActionOf(line); err == nil && !h.Allowed(action) {
				err = fmt.Errorf("Watson refused %q: the script does not declare %s %s", line, action.Verb, action.Resource)
			} else if err == nil && !readOnlyVerb(action.Verb) {
				changed = true
			}
		}
		if err != nil {
			fmt.Fprintf(out, "[221B] %v\n", err)
			e.command(r, node, raw, "denied")
			return changed, err
		}
		fmt.Fprintf(out, "[221B] %s: %s\n", r.Watson(), line)
		code, err := e.opts.Kube.RunLine(ctx, r.Cluster, a.person, identity, line, r.DryRun, out)
		if err == nil && code != 0 {
			err = fmt.Errorf("%q exited with %d", line, code)
		}
		e.command(r, node, line, outcomeOf(err != nil))
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func readOnlyVerb(v string) bool {
	switch v {
	case "get", "describe", "logs", "top", "events", "rollout status", "rollout history":
		return true
	}
	return false
}

func (e *Engine) hooks(ctx context.Context, r Run, a *activeRun, node string, list []Hook, env map[string]string, out io.Writer) error {
	for _, h := range list {
		fmt.Fprintf(out, "[221B] %s on %s\n", h.Label, node)
		identity := h.Identity
		if identity == "" {
			identity = r.Identity
		}
		if _, err := e.kube(ctx, r, a, node, identity, h.Header, h.Lines, env, out); err != nil {
			return fmt.Errorf("%s: %w", h.Label, err)
		}
	}
	return nil
}

// node runs the script on one node (AU-08, AU-09), or a Kubernetes script
// on the cluster, with the run's hooks around it (AT33).
func (e *Engine) node(ctx context.Context, a *activeRun, node string, ro Rollout) {
	started := e.opts.Now().UTC()
	e.setNode(a, node, func(n *NodeResult) { n.State, n.Started = NodeRunning, &started })
	a.mu.Lock()
	r := a.run.Copy()
	store := newTail(OutputBytes)
	a.out[node] = store
	values := a.values[node]
	secrets := a.secrets[node]
	text := a.text
	header := a.header
	a.mu.Unlock()
	var mask []string
	env := Env(r, node, values)
	var stdin strings.Builder
	for _, k := range sortedKeys(secrets) {
		mask = append(mask, secrets[k])
		delete(env, k)
		fmt.Fprintf(&stdin, "%s=%s; export %s\n", k, quote(secrets[k]), k)
	}
	out := newMask(store, mask)
	done := func() {
		out.Close()
	}
	who := r.By.Name
	if who == "" {
		who = r.By.User
	}
	fmt.Fprintf(out, "[221B] %s, for %s: %s\n", r.Watson(), who, r.Basis)
	res := NodeResult{}
	hookErr := e.hooks(ctx, r, a, node, a.before, env, out)
	var result Result
	hasResult := false
	if hookErr != nil {
		res.State, res.Error = NodeFailed, "before the script: "+hookErr.Error()
	} else if header.Target == TargetKubernetes {
		var lines []string
		for _, l := range KubeLines(text) {
			if !strings.HasPrefix(l.Text, "#!") {
				lines = append(lines, l.Text)
			}
		}
		tctx, cancel := context.WithTimeout(ctx, time.Duration(ro.TimeoutMinutes)*time.Minute)
		changed, err := e.kube(tctx, r, a, node, r.Identity, header, lines, env, out)
		cancel()
		switch {
		case ctx.Err() != nil:
			res.State, res.Error = NodeCancelled, "cancelled"
		case err != nil:
			res.State, res.Error = NodeFailed, err.Error()
		case changed && !r.DryRun:
			res.State = NodeChanged
		default:
			res.State = NodeNoChange
		}
	} else {
		asNobody := r.DryRun || r.Privilege == PrivilegeNobody
		if len(header.Facts) > 0 {
			f := e.askFacts(ctx, r.Cluster, node, KeyID(r))
			_ = e.opts.Store.saveFacts(context.WithoutCancel(ctx), []NodeFacts{f})
			e.command(r, node, "facts", outcomeOf(f.Error != ""))
			for _, k := range header.Facts {
				if v, ok := f.Facts[k]; ok {
					env[FactEnv(k)] = v
				}
			}
		}
		stdin.WriteString(text)
		tctx, cancel := context.WithTimeout(ctx, time.Duration(ro.TimeoutMinutes)*time.Minute)
		exit, err := e.opts.Exec.Exec(tctx, r.Cluster, node, KeyID(r), Command(env), asNobody, strings.NewReader(stdin.String()), out)
		timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case ctx.Err() != nil:
			res.State, res.Error = NodeCancelled, "cancelled"
		case err != nil && e.opts.Unreachable(err):
			res.State, res.Error = NodeUnreachable, err.Error()
		case timedOut:
			res.State, res.Error = NodeFailed, fmt.Sprintf("timed out after %d minutes", ro.TimeoutMinutes)
		case err != nil:
			res.State, res.Error = NodeFailed, err.Error()
		case exit != 0:
			res.State, res.Error = NodeFailed, fmt.Sprintf("exited with %d", exit)
		default:
			res.State = NodeNoChange
		}
		if err == nil {
			x := exit
			res.Exit = &x
		}
		e.command(r, node, fmt.Sprintf("script sha256 %.12s", r.Hash), outcomeOf(res.State == NodeFailed || res.State == NodeUnreachable))
		out.Flush()
		result, hasResult = ParseResult(store.String())
		if hasResult {
			res.Msg = result.Msg
			if res.State == NodeNoChange && result.Changed {
				res.State = NodeChanged
			}
		}
		if res.State == NodeChanged && hasResult && result.Reboot && !r.DryRun {
			e.setNode(a, node, func(n *NodeResult) { n.State, n.Msg = NodeRebooting, result.Msg })
			if rerr := e.reboot(ctx, r, node, out); rerr != nil {
				res.State, res.Error = NodeFailed, rerr.Error()
				if ctx.Err() != nil {
					res.State, res.Error = NodeCancelled, "cancelled while the node rebooted"
				}
			}
		}
	}
	healthy := res.State == NodeChanged || res.State == NodeNoChange
	switch {
	case healthy:
		if err := e.hooks(ctx, r, a, node, a.after, env, out); err != nil {
			res.State, res.Error = NodeFailed, "after the script: "+err.Error()
		}
	case hookErr == nil && len(a.before) > 0 && len(a.after) > 0 && res.State != NodeCancelled:
		res.Error += "; the node stays cordoned: nothing ran after the script"
	}
	done()
	ended := e.opts.Now().UTC()
	tailText := last(store.String(), tailBytes)
	e.setNode(a, node, func(n *NodeResult) {
		n.State, n.Exit, n.Msg, n.Error, n.Ended, n.Tail = res.State, res.Exit, res.Msg, res.Error, &ended, tailText
	})
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mask writes through, with each secret value blanked, holding back enough
// that a value split across two writes is still caught. Nothing but the
// masked text reaches what it wraps.
type mask struct {
	w      io.Writer
	values []string
	buf    []byte
}

func newMask(w io.Writer, values []string) *mask {
	m := &mask{w: w}
	for _, v := range values {
		if v != "" {
			m.values = append(m.values, v)
		}
	}
	return m
}

const maskText = "••••••"

func (m *mask) Write(p []byte) (int, error) {
	if len(m.values) == 0 {
		return m.w.Write(p)
	}
	m.buf = append(m.buf, p...)
	return len(p), m.drain(false)
}

// drain writes all it can: what could still turn out to be the start of a
// value stays held, unless this is the end.
func (m *mask) drain(final bool) error {
	s := string(m.buf)
	for _, v := range m.values {
		s = strings.ReplaceAll(s, v, maskText)
	}
	n := len(s)
	if !final {
		for _, v := range m.values {
			for l := min(len(v)-1, len(s)); l > 0; l-- {
				if strings.HasSuffix(s, v[:l]) {
					n = min(n, len(s)-l)
					break
				}
			}
		}
	}
	if n > 0 {
		if _, err := io.WriteString(m.w, s[:n]); err != nil {
			return err
		}
	}
	m.buf = append(m.buf[:0], s[n:]...)
	return nil
}

// Flush writes what is held.
func (m *mask) Flush() { _ = m.drain(true) }

// Close writes what is held.
func (m *mask) Close() { m.Flush() }

// reboot reboots a node and waits for SSH to answer again (AU-09).
func (e *Engine) reboot(ctx context.Context, r Run, node string, out io.Writer) error {
	fmt.Fprintf(out, "\n[221B] rebooting %s and waiting up to %s for it to come back\n", node, e.opts.RebootWait)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = e.opts.Exec.Exec(rctx, r.Cluster, node, KeyID(r), "systemctl reboot || reboot", false, strings.NewReader(""), io.Discard)
	cancel()
	deadline := time.Now().Add(e.opts.RebootWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.opts.RebootPoll):
		}
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		code, err := e.opts.Exec.Exec(pctx, r.Cluster, node, KeyID(r), "true", false, strings.NewReader(""), io.Discard)
		cancel()
		if err == nil && code == 0 {
			fmt.Fprintf(out, "[221B] %s is back\n", node)
			return nil
		}
	}
	return fmt.Errorf("the node did not come back within %s of its reboot", e.opts.RebootWait)
}

func outputPath(dir string, id int) string {
	return filepath.Join(dir, "automation", strconv.Itoa(id))
}

func writeOutputs(dir string, id int, outs map[string]string, log *slog.Logger) {
	p := outputPath(dir, id)
	if err := os.MkdirAll(p, 0o750); err != nil {
		log.Warn("keeping an automation run's output failed", "run", id, "err", err)
		return
	}
	for node, s := range outs {
		if err := os.WriteFile(filepath.Join(p, filepath.Base(node)+".log"), []byte(s), 0o640); err != nil {
			log.Warn("keeping an automation run's output failed", "run", id, "node", node, "err", err)
		}
	}
}

func readOutputs(dir string, id int) map[string]string {
	out := map[string]string{}
	p := outputPath(dir, id)
	entries, err := os.ReadDir(p)
	if err != nil {
		return out
	}
	for _, en := range entries {
		name, ok := strings.CutSuffix(en.Name(), ".log")
		if !ok || en.IsDir() {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(p, en.Name())); err == nil {
			out[name] = string(b)
		}
	}
	return out
}
