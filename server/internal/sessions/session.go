package sessions

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// States of a session.
const (
	StateStarting = "starting"
	StateLive     = "live"
	StateEnded    = "ended"
)

// Why a session ended (TB-13, TB-44).
const (
	EndIdle     = "idle"
	EndLimit    = "limit"
	EndOwner    = "owner"
	EndAdmin    = "admin"
	EndFinished = "finished"
	EndGone     = "gone"
	EndRestart  = "restart"
	EndFailed   = "failed"
	EndSignOut  = "signout"
	EndNoCLI    = "no-cli"
)

// Audit actions (TB-53). The request and the approval are Watson's when a
// second person approves; a session the person opened straight from its
// dialog records its own request.
const (
	ActionRequest  = "session.request"
	ActionOpen     = "session.open"
	ActionEnd      = "session.end"
	ActionWatch    = "session.watch"
	ActionTransfer = "session.transfer"
	ActionExtend   = "session.extend"
)

// Who is a person, as sessions need them.
type Who struct {
	// SignIn is the browser's sign-in session; Person who the cluster sees.
	SignIn string
	Person kube.Person
	// User tells people apart; Name is how the console shows them.
	User  string
	Name  string
	Admin bool
	Audit audit.Who
	From  string
	// Stands says whether the person's sign-in session still stands; a
	// session ends with it (TB-13).
	Stands func() bool `json:"-"`
}

func (w Who) same(o Who) bool {
	if w.User != "" || o.User != "" {
		return w.User == o.User
	}
	return w.SignIn == o.SignIn
}

// Cluster is what sessions need to know of a connection.
type Cluster struct {
	ID     string
	Name   string
	Config *rest.Config
}

// Created names an object a session made, to remove at its end.
type Created struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ForwardStats are a port forward's figures (TB-11).
type ForwardStats struct {
	CLI      string    `json:"cli,omitempty"`
	Listen   string    `json:"listen,omitempty"`
	Open     int       `json:"open"`
	Total    int       `json:"total"`
	BytesIn  int64     `json:"bytesIn"`
	BytesOut int64     `json:"bytesOut"`
	Last     time.Time `json:"last,omitempty"`
	Conns    []Conn    `json:"conns,omitempty"`
}

// Conn is one forwarded connection.
type Conn struct {
	ID     uint32    `json:"id"`
	From   string    `json:"from,omitempty"`
	Opened time.Time `json:"opened"`
	Closed time.Time `json:"closed,omitempty"`
	Bytes  int64     `json:"bytes"`
}

// Session is one session.
type Session struct {
	ID          string
	Cluster     string
	ClusterName string
	Req         Request
	Owner       Who
	Approver    string
	// Card is the Watson card it came from.
	Card    string
	State   string
	Created time.Time
	Started time.Time
	// LastActive is the last keystroke, output or byte.
	LastActive time.Time
	// Ends is the hard limit; MaxEnds is the furthest Extend may move it
	// (TB-22): the chart's maxDuration from the start, or Ends itself
	// where a second person approved the duration.
	Ends      time.Time
	MaxEnds   time.Time
	Ended     time.Time
	EndedBy   string
	EndNote   string
	Exit      *int
	Objects   []Created
	Pod       string
	Ns        string
	Container string
	Notes     []string
	Forward   ForwardStats
	Watchers  []string
	// Recorded says a transcript is kept.
	Recorded bool
	Rev      int

	token   string
	limit   time.Duration
	backend Backend
	// podUID is the pod the session runs in, as it was: a pod made again
	// under the same name is another pod, and the session ends (TB-13).
	podUID  types.UID
	cancel  context.CancelFunc
	stdin   *io.PipeWriter
	sizes   chan Size
	ring    ring
	viewers map[*Viewer]struct{}
	fwd     Forwarder
	cast    *transcript
	lastRev time.Time
}

// Manager keeps the sessions. They live in memory on the replica that
// runs the assistants (TB-14); a restart ends them.
type Manager struct {
	opts Options
	mu   sync.Mutex
	all  map[string]*Session
	subs map[*Sub]struct{}
}

// Options configure the manager.
type Options struct {
	Config toolbox.Config
	Audit  *audit.Log
	Logger *slog.Logger
	Now    func() time.Time
	// Backend returns a cluster's backend as a person; nil uses client-go.
	Backend func(cl Cluster, p kube.Person) (Backend, error)
	// History keeps the records of ended sessions; nil keeps them in
	// memory.
	History History
	// StartWithin bounds how long a session waits for its container.
	StartWithin time.Duration
	// MaxOutput bounds a command's output kept, in bytes.
	MaxOutput int
	// CLIWait is how long a port forward waits for the CLI (TB-21).
	CLIWait time.Duration
	// ViewerWait is how long output waits for a browser that has fallen
	// behind before the browser is dropped to reconnect and replay.
	ViewerWait time.Duration
	// PodCheck is how often a port forward looks at its pod (TB-13).
	PodCheck time.Duration
}

// Sub follows the changes of a cluster's sessions.
type Sub struct {
	Cluster string
	C       chan string
}

// New makes the manager.
func New(o Options) *Manager {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Backend == nil {
		o.Backend = func(cl Cluster, p kube.Person) (Backend, error) { return NewKubeBackend(cl.Config, p) }
	}
	if o.History == nil {
		o.History = &MemoryHistory{Keep: o.Config.History.Keep}
	}
	if o.StartWithin <= 0 {
		o.StartWithin = 90 * time.Second
	}
	if o.MaxOutput <= 0 {
		o.MaxOutput = 2 << 20
	}
	if o.CLIWait <= 0 {
		o.CLIWait = 5 * time.Minute
	}
	if o.ViewerWait <= 0 {
		o.ViewerWait = 250 * time.Millisecond
	}
	if o.PodCheck <= 0 {
		o.PodCheck = 5 * time.Second
	}
	return &Manager{opts: o, all: map[string]*Session{}, subs: map[*Sub]struct{}{}}
}

// Config is the toolbox values the manager runs with.
func (m *Manager) Config() toolbox.Config { return m.opts.Config }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Subscribe follows a cluster's sessions.
func (m *Manager) Subscribe(cluster string) *Sub {
	s := &Sub{Cluster: cluster, C: make(chan string, 256)}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	return s
}

// Unsubscribe ends a subscription.
func (m *Manager) Unsubscribe(s *Sub) {
	m.mu.Lock()
	delete(m.subs, s)
	m.mu.Unlock()
}

func (m *Manager) changedLocked(s *Session) {
	s.Rev++
	s.lastRev = m.opts.Now()
	for sub := range m.subs {
		if sub.Cluster != s.Cluster {
			continue
		}
		select {
		case sub.C <- s.ID:
		default:
		}
	}
}

func (m *Manager) record(action string, s *Session, who Who, outcome, detail string) {
	if m.opts.Audit == nil {
		return
	}
	d := fmt.Sprintf("session=%s tool=%s", s.ID, s.Req.Tool)
	if detail != "" {
		d += " " + detail
	}
	m.opts.Audit.Record(audit.Event{Action: action, Who: who.Audit, Cluster: s.Cluster, Target: s.Req.Summary(), Detail: d, Outcome: outcome, From: who.From})
}

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("no such session")
	ErrNotYours = errors.New("that session is someone else's")
	ErrEnded    = errors.New("the session has ended")
	ErrToken    = errors.New("the forward's token does not match")
)

// Start opens a session from an approved card: it creates what the tool
// needs, as the person who asked (TB-37, TB-51), and returns once the
// session exists; the container is waited for after.
func (m *Manager) Start(cl Cluster, req Request, owner Who, approver, card string) (*Session, error) {
	backend, err := m.opts.Backend(cl, owner.Person)
	if err != nil {
		return nil, err
	}
	now := m.opts.Now()
	limit := m.opts.Config.Limit(time.Duration(req.Minutes) * time.Minute)
	s := &Session{
		ID: newID(), Cluster: cl.ID, ClusterName: cl.Name, Req: req, Owner: owner, Approver: approver, Card: card,
		State: StateStarting, Created: now, LastActive: now, limit: limit, backend: backend,
		viewers: map[*Viewer]struct{}{}, ring: ring{max: 256 << 10},
	}
	if req.Tool == toolbox.ToolForward {
		s.token = newToken()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	m.mu.Lock()
	m.all[s.ID] = s
	m.changedLocked(s)
	m.mu.Unlock()
	if card == "" {
		m.record(ActionRequest, s, owner, audit.OutcomeOK, fmt.Sprintf("from=%q approval=none", owner.From))
	}
	go m.run(ctx, s)
	return s, nil
}

// run brings a session up and keeps it until it ends.
func (m *Manager) run(ctx context.Context, s *Session) {
	r := s.Req
	b := s.backend
	fail := func(err error) {
		m.end(s, EndFailed, statusMessage(err), Who{})
	}
	switch r.Tool {
	case toolbox.ToolShell, toolbox.ToolCommand, toolbox.ToolForward:
		pod, err := b.Pod(ctx, r.Namespace, r.Pod)
		if err != nil {
			fail(err)
			return
		}
		container := r.Container
		if container == "" && len(pod.Spec.Containers) > 0 {
			container = pod.Spec.Containers[0].Name
		}
		if r.Tool != toolbox.ToolForward {
			if ok, why := containerRunning(pod, container); !ok {
				if why == "" {
					why = "container " + container + " is not running"
				}
				fail(errors.New(why))
				return
			}
		}
		m.located(s, pod, container)
		switch r.Tool {
		case toolbox.ToolShell:
			m.terminal(ctx, s, ShellCommand(r.Shell))
		case toolbox.ToolCommand:
			m.command(ctx, s, []string{"sh", "-c", r.Command})
		case toolbox.ToolForward:
			m.forward(ctx, s)
		}
	case toolbox.ToolDebugContainer:
		pod, err := b.Pod(ctx, r.Namespace, r.Pod)
		if err != nil {
			fail(err)
			return
		}
		level := LevelPrivileged
		if ns, err := b.Namespace(ctx, r.Namespace); err == nil {
			level = PodSecurity(ns)
		}
		ec, err := EphemeralContainer(m.opts.Config, s.ID, r, pod, level)
		if err != nil {
			fail(err)
			return
		}
		if err := b.AddEphemeral(ctx, pod, ec, false); err != nil {
			fail(err)
			return
		}
		m.note(s, fmt.Sprintf("Debug container %s attached to pod %s: it stays until the pod restarts.", ec.Name, r.Pod))
		running, err := waitRunning(ctx, func(c context.Context) (*corev1.Pod, error) { return b.Pod(c, r.Namespace, r.Pod) }, ec.Name, m.opts.StartWithin)
		if err != nil {
			fail(err)
			return
		}
		m.located(s, running, ec.Name)
		m.terminal(ctx, s, ShellCommand("auto"))
	case toolbox.ToolDebugPod:
		level := LevelPrivileged
		if ns, err := b.Namespace(ctx, r.Namespace); err == nil {
			level = PodSecurity(ns)
		}
		var copied map[string]string
		if r.LabelsFrom != "" {
			p, err := b.Pod(ctx, r.Namespace, r.LabelsFrom)
			if err != nil {
				fail(err)
				return
			}
			copied = p.Labels
		}
		job, notes, err := DebugJob(m.opts.Config, s.ID, s.Owner.Name, r, level, copied, s.limit)
		if err != nil {
			fail(err)
			return
		}
		for _, n := range notes {
			m.note(s, n)
		}
		if err := b.CreateJob(ctx, job, false); err != nil {
			fail(err)
			return
		}
		m.created(s, Created{Kind: "Job", Namespace: job.Namespace, Name: job.Name})
		pod, err := waitRunning(ctx, func(c context.Context) (*corev1.Pod, error) { return b.JobPod(c, r.Namespace, s.ID) }, "debug", m.opts.StartWithin)
		if err != nil {
			fail(err)
			return
		}
		m.located(s, pod, "debug")
		m.terminal(ctx, s, ShellCommand("auto"))
	case toolbox.ToolNodeShell, toolbox.ToolNodeCommand:
		pod, err := NodePod(m.opts.Config, s.ID, s.Owner.Name, r, s.limit)
		if err != nil {
			fail(err)
			return
		}
		if err := b.CreatePod(ctx, pod, false); err != nil {
			fail(err)
			return
		}
		m.created(s, Created{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name})
		running, err := waitRunning(ctx, func(c context.Context) (*corev1.Pod, error) { return b.Pod(c, pod.Namespace, pod.Name) }, "node", m.opts.StartWithin)
		if err != nil {
			fail(err)
			return
		}
		m.located(s, running, "node")
		if r.Tool == toolbox.ToolNodeShell {
			m.note(s, fmt.Sprintf("Pod %s in %s on node %s, %s profile: the node's filesystem is at /host (chroot /host for its own tools).", pod.Name, pod.Namespace, r.Node, r.Profile))
			m.terminal(ctx, s, ShellCommand("auto"))
		} else {
			m.command(ctx, s, NodeCommand(r.Command))
		}
	default:
		fail(fmt.Errorf("unknown tool %q", r.Tool))
	}
}

func (m *Manager) located(s *Session, pod *corev1.Pod, container string) {
	m.mu.Lock()
	s.Ns, s.Pod, s.Container, s.podUID = pod.Namespace, pod.Name, container, pod.UID
	m.changedLocked(s)
	m.mu.Unlock()
}

// podGone says why the session's pod is no longer the one it started in,
// or "" while it is (TB-13): gone, being deleted, or made again under the
// same name. A passing error says nothing.
func (m *Manager) podGone(ctx context.Context, s *Session) string {
	m.mu.Lock()
	ns, name, uid, b := s.Ns, s.Pod, s.podUID, s.backend
	m.mu.Unlock()
	if name == "" {
		return ""
	}
	p, err := b.Pod(ctx, ns, name)
	switch {
	case apierrors.IsNotFound(err):
		return "The pod is gone."
	case err != nil:
		return ""
	case p.DeletionTimestamp != nil:
		return "The pod is being deleted."
	case uid != "" && p.UID != uid:
		return "The pod was replaced."
	}
	return ""
}

func (m *Manager) created(s *Session, c Created) {
	m.mu.Lock()
	s.Objects = append(s.Objects, c)
	m.changedLocked(s)
	m.mu.Unlock()
}

func (m *Manager) note(s *Session, n string) {
	m.mu.Lock()
	s.Notes = append(s.Notes, n)
	m.changedLocked(s)
	m.mu.Unlock()
}

// live marks a session started: its hard limit counts from now.
func (m *Manager) live(s *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.State != StateStarting {
		return false
	}
	now := m.opts.Now()
	s.State, s.Started, s.LastActive, s.Ends = StateLive, now, now, now.Add(s.limit)
	s.MaxEnds = s.Ends
	if s.Approver == "" {
		s.MaxEnds = now.Add(m.opts.Config.MaxDuration.D())
	}
	m.changedLocked(s)
	if m.opts.Config.Transcripts == toolbox.TranscriptsPVC && Interactive(s.Req.Tool) {
		if t, err := newTranscript(m.opts.Config.TranscriptDir, s, now); err == nil {
			s.cast, s.Recorded = t, true
		} else {
			m.opts.Logger.Warn("transcript not kept", "session", s.ID, "err", err)
		}
	}
	m.record(ActionOpen, s, s.Owner, audit.OutcomeOK, fmt.Sprintf("approved_by=%q pod=%s/%s container=%s limit=%s", s.Approver, s.Ns, s.Pod, s.Container, s.limit))
	return true
}

// terminal runs an interactive shell until it exits or the session ends.
func (m *Manager) terminal(ctx context.Context, s *Session, cmd []string) {
	pr, pw := io.Pipe()
	sizes := make(chan Size, 4)
	m.mu.Lock()
	s.stdin, s.sizes = pw, sizes
	m.mu.Unlock()
	if !m.live(s) {
		return
	}
	out := &termWriter{m: m, s: s}
	err := s.backend.Exec(ctx, s.Ns, s.Pod, s.Container, cmd, true, pr, out, out, sizes)
	if ctx.Err() != nil {
		// Ended from outside: the reason is already set.
		return
	}
	// A shell that ends with its pod is the pod's end, not the shell's.
	gone, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	why := m.podGone(gone, s)
	cancel()
	switch {
	case why != "":
		m.end(s, EndGone, why, Who{})
	case err != nil:
		msg := statusMessage(err)
		if strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory") {
			msg += " — the image has no shell; a debug container gives you one."
		}
		m.end(s, EndFailed, msg, Who{})
	default:
		m.end(s, EndFinished, "The shell exited.", Who{})
	}
}

// command runs one command and keeps its output (TB-16, TB-33).
func (m *Manager) command(ctx context.Context, s *Session, cmd []string) {
	if !m.live(s) {
		return
	}
	w := &limitWriter{m: m, s: s, max: m.opts.MaxOutput}
	err := s.backend.Exec(ctx, s.Ns, s.Pod, s.Container, cmd, false, nil, w, w, nil)
	code := 0
	note := "finished"
	if err != nil {
		var ce interface{ ExitStatus() int }
		if errors.As(err, &ce) {
			code = ce.ExitStatus()
		} else if ctx.Err() != nil {
			return
		} else {
			code = -1
			note = statusMessage(err)
		}
	}
	if w.cut {
		m.note(s, fmt.Sprintf("Output cut at %d MB.", m.opts.MaxOutput>>20))
	}
	m.mu.Lock()
	s.Exit = &code
	m.mu.Unlock()
	if code == 0 {
		m.end(s, EndFinished, "exit 0", Who{})
	} else if code == -1 {
		m.end(s, EndFailed, note, Who{})
	} else {
		m.end(s, EndFinished, fmt.Sprintf("exit %d", code), Who{})
	}
}

// forward waits for the CLI; the tunnel does the rest (TB-19 to TB-22).
// It also watches the pod, which a forward has no stream to notice by: a
// forward to a pod that is gone, or made again, ends (TB-13).
func (m *Manager) forward(ctx context.Context, s *Session) {
	if !m.live(s) {
		return
	}
	cli := time.NewTimer(m.opts.CLIWait)
	defer cli.Stop()
	pod := time.NewTicker(m.opts.PodCheck)
	defer pod.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cli.C:
			m.mu.Lock()
			connected := s.Forward.CLI != ""
			m.mu.Unlock()
			if !connected {
				m.end(s, EndNoCLI, fmt.Sprintf("No CLI connected within %s.", short(m.opts.CLIWait)), Who{})
				return
			}
		case <-pod.C:
			if why := m.podGone(ctx, s); why != "" {
				m.end(s, EndGone, why, Who{})
				return
			}
		}
	}
}

// End ends a session: by its owner, or by an administrator with a reason
// the owner sees (TB-12).
func (m *Manager) End(id string, who Who, reason string) (*Session, error) {
	m.mu.Lock()
	s := m.all[id]
	if s == nil {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if s.State == StateEnded {
		m.mu.Unlock()
		return s, ErrEnded
	}
	owner := s.Owner.same(who)
	m.mu.Unlock()
	switch {
	case owner:
		m.end(s, EndOwner, "", who)
	case who.Admin:
		note := "Ended by " + who.Name
		if r := strings.TrimSpace(reason); r != "" {
			note += ": " + r
		}
		m.end(s, EndAdmin, note, who)
	default:
		return s, ErrNotYours
	}
	return s, nil
}

// end ends a session once: it stops its streams, removes what it made,
// and keeps the record.
func (m *Manager) end(s *Session, by, note string, who Who) {
	m.mu.Lock()
	if s.State == StateEnded {
		m.mu.Unlock()
		return
	}
	s.State, s.Ended, s.EndedBy, s.EndNote = StateEnded, m.opts.Now(), by, note
	if s.cancel != nil {
		s.cancel()
	}
	if s.stdin != nil {
		s.stdin.Close()
	}
	fwd := s.fwd
	s.fwd = nil
	for v := range s.viewers {
		v.close()
	}
	s.viewers = map[*Viewer]struct{}{}
	objects := append([]Created(nil), s.Objects...)
	cast := s.cast
	s.cast = nil
	m.changedLocked(s)
	m.mu.Unlock()
	if fwd != nil {
		fwd.Close()
	}
	if cast != nil {
		cast.close()
	}
	// What the session made goes as the person who made it (TB-29, TB-31).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, o := range objects {
		var err error
		switch o.Kind {
		case "Job":
			err = s.backend.DeleteJob(ctx, o.Namespace, o.Name)
		case "Pod":
			err = s.backend.DeletePod(ctx, o.Namespace, o.Name)
		}
		if err != nil {
			m.opts.Logger.Warn("could not remove what a session made; its deadline will", "session", s.ID, "kind", o.Kind, "name", o.Namespace+"/"+o.Name, "err", err)
		}
	}
	actor := s.Owner
	if who.Name != "" {
		actor = who
	}
	outcome := audit.OutcomeOK
	if by == EndFailed {
		outcome = audit.OutcomeFailed
	}
	detail := fmt.Sprintf("ended_by=%s duration=%s", by, s.Ended.Sub(s.Created).Round(time.Second))
	if note != "" {
		detail += fmt.Sprintf(" note=%q", note)
	}
	if s.Exit != nil {
		detail += fmt.Sprintf(" exit=%d", *s.Exit)
	}
	if s.Req.Tool == toolbox.ToolCommand || s.Req.Tool == toolbox.ToolNodeCommand {
		out := s.ring.String()
		if len(out) > 16<<10 {
			out = out[:16<<10] + "…"
		}
		detail += fmt.Sprintf(" command=%q output=%q", s.Req.Command, out)
	}
	if s.Req.Tool == toolbox.ToolForward {
		detail += fmt.Sprintf(" connections=%d bytes_in=%d bytes_out=%d", s.Forward.Total, s.Forward.BytesIn, s.Forward.BytesOut)
	}
	m.record(ActionEnd, s, actor, outcome, detail)
	m.opts.History.Add(recordOf(s))
}

// ---------- terminal plumbing ----------

// Viewer is one browser attached to a terminal: the owner, or a
// SuperAdmin watching (TB-55).
type Viewer struct {
	Out      chan []byte
	ReadOnly bool
	Name     string
	once     sync.Once
	done     chan struct{}
	// dropped says the viewer fell behind the output and was let go while
	// the session went on: it reconnects and replays. Done closes either
	// way; this tells the two apart.
	dropped atomic.Bool
}

// Done is closed when the viewer is dropped, or the session ends.
func (v *Viewer) Done() <-chan struct{} { return v.done }

// Dropped says Done closed because the viewer fell behind, not because the
// session ended: the browser reconnects and replays (TB-14).
func (v *Viewer) Dropped() bool { return v.dropped.Load() }

func (v *Viewer) close() { v.once.Do(func() { close(v.done) }) }

// termWriter takes a shell's output to the viewers, the replay buffer and
// the transcript.
type termWriter struct {
	m *Manager
	s *Session
}

func (w *termWriter) Write(p []byte) (int, error) {
	m, s := w.m, w.s
	b := append([]byte(nil), p...)
	m.mu.Lock()
	s.ring.Write(b)
	s.LastActive = m.opts.Now()
	cast := s.cast
	viewers := make([]*Viewer, 0, len(s.viewers))
	for v := range s.viewers {
		viewers = append(viewers, v)
	}
	if m.opts.Now().Sub(s.lastRev) > 5*time.Second {
		m.changedLocked(s)
	}
	m.mu.Unlock()
	if cast != nil {
		cast.event("o", b)
	}
	for _, v := range viewers {
		select {
		case v.Out <- b:
			continue
		case <-v.done:
			continue
		default:
		}
		// The browser is behind. Wait for it a moment, as a terminal waits
		// for its screen, so a burst of output slows the shell rather than
		// losing a window; a browser still behind after that is dropped,
		// and reconnects to replay what it missed.
		select {
		case v.Out <- b:
		case <-v.done:
		case <-time.After(m.opts.ViewerWait):
			m.drop(s, v)
		}
	}
	return len(p), nil
}

// drop lets a viewer go that cannot keep up: it is told so, and reconnects.
func (m *Manager) drop(s *Session, v *Viewer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := s.viewers[v]; !ok {
		return
	}
	delete(s.viewers, v)
	if v.ReadOnly {
		s.Watchers = without(s.Watchers, v.Name)
		m.changedLocked(s)
	}
	v.dropped.Store(true)
	v.close()
}

// without removes one occurrence of a name.
func without(names []string, name string) []string {
	for i, n := range names {
		if n == name {
			return append(names[:i:i], names[i+1:]...)
		}
	}
	return names
}

// limitWriter keeps a command's output up to a limit.
type limitWriter struct {
	m   *Manager
	s   *Session
	max int
	n   int
	cut bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.n+len(p) > w.max {
		w.cut = true
		p = p[:max(0, w.max-w.n)]
	}
	w.n += len(p)
	if len(p) > 0 {
		(&termWriter{m: w.m, s: w.s}).Write(p)
	}
	return len(p), nil
}

// Attach attaches a browser to a terminal: the owner to type, a SuperAdmin
// to watch. It returns what the terminal showed so far.
func (m *Manager) Attach(id string, who Who, watch bool) (*Viewer, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.all[id]
	if s == nil {
		return nil, nil, ErrNotFound
	}
	owner := s.Owner.same(who)
	if !owner && !(watch && who.Admin) {
		return nil, nil, ErrNotYours
	}
	v := &Viewer{Out: make(chan []byte, 512), ReadOnly: !owner || watch, Name: who.Name, done: make(chan struct{})}
	replay := s.ring.Bytes()
	if s.State == StateEnded {
		v.close()
		return v, replay, nil
	}
	s.viewers[v] = struct{}{}
	if v.ReadOnly {
		s.Watchers = append(s.Watchers, who.Name)
		m.changedLocked(s)
		m.record(ActionWatch, s, who, audit.OutcomeOK, "")
	}
	return v, replay, nil
}

// Detach drops a viewer.
func (m *Manager) Detach(id string, v *Viewer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.all[id]
	if s == nil {
		return
	}
	if _, ok := s.viewers[v]; ok {
		delete(s.viewers, v)
		if v.ReadOnly {
			s.Watchers = without(s.Watchers, v.Name)
			m.changedLocked(s)
		}
	}
	v.close()
}

// Input types into a terminal; only its owner may.
func (m *Manager) Input(id string, v *Viewer, p []byte) error {
	if v.ReadOnly {
		return ErrNotYours
	}
	m.mu.Lock()
	s := m.all[id]
	if s == nil || s.State != StateLive || s.stdin == nil {
		m.mu.Unlock()
		return ErrEnded
	}
	s.LastActive = m.opts.Now()
	w, cast := s.stdin, s.cast
	m.mu.Unlock()
	if cast != nil {
		cast.event("i", p)
	}
	_, err := w.Write(p)
	return err
}

// Resize sets a terminal's size.
func (m *Manager) Resize(id string, v *Viewer, sz Size) {
	if v.ReadOnly || sz.Cols == 0 || sz.Rows == 0 {
		return
	}
	m.mu.Lock()
	s := m.all[id]
	var c chan Size
	var cast *transcript
	if s != nil && s.State == StateLive {
		c, cast = s.sizes, s.cast
	}
	m.mu.Unlock()
	if c != nil {
		select {
		case c <- sz:
		default:
		}
	}
	if cast != nil {
		cast.resize(sz)
	}
}

// Keep resets the idle timer (TB-10's Keep button).
func (m *Manager) Keep(id string, who Who) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.all[id]
	if s == nil {
		return nil, ErrNotFound
	}
	if !s.Owner.same(who) {
		return s, ErrNotYours
	}
	if s.State == StateEnded {
		return s, ErrEnded
	}
	s.LastActive = m.opts.Now()
	m.changedLocked(s)
	return s, nil
}

// ExtendBy is what one Extend adds to a session's hard limit.
const ExtendBy = 10 * time.Minute

// ErrLimit says a session cannot be extended: it is at the chart's
// limit, or a second person approved its duration.
var ErrLimit = errors.New("the session cannot be extended")

// Extend adds ten minutes to a session's hard limit, up to the chart's
// maxDuration from its start (TB-22); where a second person approved the
// duration, that is the limit. It counts as activity.
func (m *Manager) Extend(id string, who Who) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.all[id]
	if s == nil {
		return nil, ErrNotFound
	}
	if !s.Owner.same(who) {
		return s, ErrNotYours
	}
	if s.State != StateLive {
		return s, ErrEnded
	}
	if !s.Ends.Before(s.MaxEnds) {
		if s.Approver != "" {
			return s, fmt.Errorf("%w: %s approved it for %s", ErrLimit, s.Approver, short(s.limit))
		}
		return s, fmt.Errorf("%w: sessions here last %s at most", ErrLimit, short(m.opts.Config.MaxDuration.D()))
	}
	ends := s.Ends.Add(ExtendBy)
	if ends.After(s.MaxEnds) {
		ends = s.MaxEnds
	}
	s.Ends, s.LastActive = ends, m.opts.Now()
	m.changedLocked(s)
	m.record(ActionExtend, s, who, audit.OutcomeOK, "ends="+ends.UTC().Format(time.RFC3339))
	return s, nil
}

// ---------- the limits ----------

// Run ends sessions at their limits until ctx ends, then ends them all:
// a console that stops leaves no session behind (TB-14).
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			var all []*Session
			for _, s := range m.all {
				all = append(all, s)
			}
			m.mu.Unlock()
			for _, s := range all {
				m.end(s, EndRestart, "The console stopped.", Who{})
			}
			return
		case <-t.C:
		}
		m.sweep()
	}
}

func (m *Manager) sweep() {
	now := m.opts.Now()
	idle := m.opts.Config.IdleTimeout.D()
	type ending struct {
		s       *Session
		by, why string
	}
	var ends []ending
	m.mu.Lock()
	for id, s := range m.all {
		if s.State == StateEnded {
			if now.Sub(s.Ended) > time.Hour {
				delete(m.all, id)
			}
			continue
		}
		if s.State != StateLive {
			continue
		}
		switch {
		case !s.Ends.IsZero() && now.After(s.Ends):
			ends = append(ends, ending{s, EndLimit, "Time limit reached."})
		case now.Sub(s.LastActive) > idle:
			ends = append(ends, ending{s, EndIdle, fmt.Sprintf("No activity for %s.", short(idle))})
		case s.Owner.Stands != nil && !s.Owner.Stands():
			ends = append(ends, ending{s, EndSignOut, "The person's sign-in session ended."})
		}
	}
	m.mu.Unlock()
	for _, e := range ends {
		m.end(e.s, e.by, e.why, Who{})
	}
}

// ---------- reading them ----------

// Get returns a copy of a session.
func (m *Manager) Get(id string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.all[id]
	if s == nil {
		return Session{}, false
	}
	return s.copy(), true
}

func (s *Session) copy() Session {
	c := *s
	c.Objects = append([]Created(nil), s.Objects...)
	c.Notes = append([]string(nil), s.Notes...)
	c.Watchers = append([]string(nil), s.Watchers...)
	c.Forward.Conns = append([]Conn(nil), s.Forward.Conns...)
	c.viewers, c.backend, c.cancel, c.stdin, c.sizes, c.fwd, c.cast = nil, nil, nil, nil, nil, nil, nil
	c.ring = ring{}
	return c
}

// Output is what a command printed, or the end of a terminal.
func (m *Manager) Output(id string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.all[id]; s != nil {
		return s.ring.Bytes()
	}
	return nil
}

// Visible says whether a person sees a session: their own; every one for
// a SuperAdmin (TB-12).
func Visible(s Session, who Who) bool {
	return s.Owner.same(who) || who.Admin
}

// List returns the sessions of a cluster a person sees, newest first:
// live ones and those ended in the last hour.
func (m *Manager) List(cluster string, who Who) []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Session
	for _, s := range m.all {
		if s.Cluster == cluster && Visible(*s, who) {
			out = append(out, s.copy())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// History lists ended sessions' records of a cluster a person sees.
func (m *Manager) History(cluster string, who Who) []Record {
	var out []Record
	for _, r := range m.opts.History.List() {
		if r.Cluster != cluster {
			continue
		}
		if who.Admin || (r.OwnerUser != "" && r.OwnerUser == who.User) || (r.OwnerUser == "" && who.User == "") {
			out = append(out, r)
		}
	}
	return out
}

// Token checks a port forward's token, in constant time.
func (m *Manager) checkToken(s *Session, token string) bool {
	return s.token != "" && subtle.ConstantTimeCompare([]byte(s.token), []byte(token)) == 1
}

// Token returns a forward's token, for its owner's pane.
func (m *Manager) Token(id string, who Who) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.all[id]
	if s == nil {
		return "", ErrNotFound
	}
	if !s.Owner.same(who) {
		return "", ErrNotYours
	}
	return s.token, nil
}

// ---------- the replay buffer ----------

// ring keeps the last bytes a terminal showed.
type ring struct {
	buf []byte
	max int
}

func (r *ring) Write(p []byte) {
	r.buf = append(r.buf, p...)
	if r.max > 0 && len(r.buf) > r.max {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.max:]...)
	}
}

func (r *ring) Bytes() []byte  { return append([]byte(nil), r.buf...) }
func (r *ring) String() string { return string(r.buf) }
