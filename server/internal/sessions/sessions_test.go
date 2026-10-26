package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/exec"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/tunnel"
)

// fakeBackend is a cluster in memory: pods that run, a shell that echoes,
// and a port that answers.
type fakeBackend struct {
	mu        sync.Mutex
	pods      map[string]*corev1.Pod
	ns        map[string]*corev1.Namespace
	jobs      map[string]*batchv1.Job
	deleted   []string
	execs     [][]string
	noShell   bool
	exitCode  int
	output    string
	sizes     []Size
	forwarded []byte
	dryRuns   int
	refuse    error
}

func newFake() *fakeBackend {
	f := &fakeBackend{pods: map[string]*corev1.Pod{}, ns: map[string]*corev1.Namespace{}, jobs: map[string]*batchv1.Job{}}
	f.addPod("web", "api-1", "api")
	f.ns["web"] = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "web"}}
	return f
}

func (f *fakeBackend) addPod(ns, name string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": "api", "pod-template-hash": "abc"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}, {Name: "cfg", MountPath: "/etc/x", SubPath: "x"}}})
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: c, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
	}
	p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
	f.pods[ns+"/"+name] = p
	return p
}

func (f *fakeBackend) Pod(_ context.Context, ns, name string) (*corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pods[ns+"/"+name]
	if p == nil {
		return nil, errors.New(`pods "` + name + `" not found`)
	}
	return p.DeepCopy(), nil
}

func (f *fakeBackend) Namespace(_ context.Context, name string) (*corev1.Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.ns[name]; n != nil {
		return n, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeBackend) Exec(ctx context.Context, ns, pod, container string, cmd []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, sizes <-chan Size) error {
	f.mu.Lock()
	f.execs = append(f.execs, append([]string{ns + "/" + pod + "/" + container}, cmd...))
	noShell, code, out := f.noShell, f.exitCode, f.output
	f.mu.Unlock()
	if noShell {
		return errors.New(`exec: "sh": executable file not found in $PATH`)
	}
	if !tty {
		io.WriteString(stdout, out)
		if code != 0 {
			return exec.CodeExitError{Err: errors.New("exit"), Code: code}
		}
		return nil
	}
	go func() {
		for s := range sizes {
			f.mu.Lock()
			f.sizes = append(f.sizes, s)
			f.mu.Unlock()
		}
	}()
	io.WriteString(stdout, "$ ")
	buf := make([]byte, 1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			line := string(buf[:n])
			if strings.TrimSpace(line) == "exit" {
				return nil
			}
			io.WriteString(stdout, "echo:"+line)
		}
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (f *fakeBackend) Forwarder(context.Context, string, string) (Forwarder, error) {
	return &fakeForwarder{f: f}, nil
}

type fakeForwarder struct{ f *fakeBackend }

func (ff *fakeForwarder) Dial(port int) (io.ReadWriteCloser, error) {
	a, b := net.Pipe()
	go func() {
		// The pod's port answers with what it was sent, upper-cased.
		buf := make([]byte, 1024)
		for {
			n, err := b.Read(buf)
			if n > 0 {
				ff.f.mu.Lock()
				ff.f.forwarded = append(ff.f.forwarded, buf[:n]...)
				ff.f.mu.Unlock()
				b.Write([]byte(strings.ToUpper(string(buf[:n]))))
			}
			if err != nil {
				b.Close()
				return
			}
		}
	}()
	return a, nil
}
func (ff *fakeForwarder) Close() error { return nil }

func (f *fakeBackend) AddEphemeral(_ context.Context, pod *corev1.Pod, ec corev1.EphemeralContainer, dry bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return f.refuse
	}
	if dry {
		f.dryRuns++
		return nil
	}
	p := f.pods[pod.Namespace+"/"+pod.Name]
	p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, ec)
	p.Status.EphemeralContainerStatuses = append(p.Status.EphemeralContainerStatuses, corev1.ContainerStatus{Name: ec.Name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
	return nil
}

func (f *fakeBackend) CreateJob(_ context.Context, job *batchv1.Job, dry bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dry {
		f.dryRuns++
		return nil
	}
	f.jobs[job.Namespace+"/"+job.Name] = job
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: job.Namespace, Name: job.Name + "-x", Labels: job.Spec.Template.Labels}, Spec: job.Spec.Template.Spec,
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "debug", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	f.pods[p.Namespace+"/"+p.Name] = p
	return nil
}

func (f *fakeBackend) CreatePod(_ context.Context, pod *corev1.Pod, dry bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dry {
		f.dryRuns++
		return nil
	}
	p := pod.DeepCopy()
	p.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "node", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
	f.pods[p.Namespace+"/"+p.Name] = p
	return nil
}

func (f *fakeBackend) JobPod(_ context.Context, ns, session string) (*corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pods {
		if p.Namespace == ns && p.Labels[LabelSession] == session {
			return p.DeepCopy(), nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeBackend) DeleteJob(_ context.Context, ns, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, "job/"+ns+"/"+name)
	return nil
}

func (f *fakeBackend) DeletePod(_ context.Context, ns, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, "pod/"+ns+"/"+name)
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type auditBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (a *auditBuf) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b.Write(p)
}

func (a *auditBuf) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(a.b.String()), "\n") {
		var e audit.Event
		if json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e.Action)
		}
	}
	return out
}

func (a *auditBuf) text() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b.String()
}

func setup(t *testing.T) (*Manager, *fakeBackend, *clock, *auditBuf) {
	t.Helper()
	f := newFake()
	c := &clock{t: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)}
	buf := &auditBuf{}
	m := New(Options{Config: toolbox.Default(), Audit: audit.New(audit.Options{Out: buf}), Now: c.now, StartWithin: 2 * time.Second, CLIWait: time.Hour,
		Backend: func(Cluster, kube.Person) (Backend, error) { return f, nil }})
	return m, f, c, buf
}

func alice() Who {
	return Who{SignIn: "s-a", User: "alice", Name: "Alice", Person: kube.Person{User: "alice"}}
}
func bob() Who { return Who{SignIn: "s-b", User: "bob", Name: "Bob", Person: kube.Person{User: "bob"}} }

func waitState(t *testing.T, m *Manager, id, state string) Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := m.Get(id)
		if s.State == state {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, _ := m.Get(id)
	t.Fatalf("session %s is %s (%s %s), want %s", id, s.State, s.EndedBy, s.EndNote, state)
	return s
}

func read(t *testing.T, v *Viewer, want string) string {
	t.Helper()
	var got strings.Builder
	deadline := time.After(3 * time.Second)
	for !strings.Contains(got.String(), want) {
		select {
		case b := <-v.Out:
			got.Write(b)
		case <-deadline:
			t.Fatalf("waited for %q, got %q", want, got.String())
		}
	}
	return got.String()
}

func TestShellRoundTripAndResize(t *testing.T) {
	m, f, _, buf := setup(t)
	s, err := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "card1")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateLive)
	v, replay, err := m.Attach(s.ID, alice(), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.ReadOnly {
		t.Fatal("the owner's view is read-only")
	}
	got := string(replay)
	if !strings.Contains(got, "$ ") {
		got += read(t, v, "$ ")
	}
	if err := m.Input(s.ID, v, []byte("id\n")); err != nil {
		t.Fatal(err)
	}
	read(t, v, "echo:id")
	m.Resize(s.ID, v, Size{Cols: 120, Rows: 40})
	// The container defaults to the first, and the shell to bash then sh.
	f.mu.Lock()
	first := strings.Join(f.execs[0], " ")
	f.mu.Unlock()
	if !strings.HasPrefix(first, "web/api-1/api sh -c command -v bash") {
		t.Fatalf("exec = %q", first)
	}
	// Someone else may not type, nor attach to type.
	if _, _, err := m.Attach(s.ID, bob(), false); !errors.Is(err, ErrNotYours) {
		t.Fatalf("bob attached: %v", err)
	}
	if err := m.Input(s.ID, v, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	ended := waitState(t, m, s.ID, StateEnded)
	if ended.EndedBy != EndFinished {
		t.Fatalf("ended by %s", ended.EndedBy)
	}
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	sizes := append([]Size(nil), f.sizes...)
	f.mu.Unlock()
	if len(sizes) == 0 || sizes[0] != (Size{120, 40}) {
		t.Fatalf("sizes = %v", sizes)
	}
	acts := strings.Join(buf.actions(), ",")
	if !strings.Contains(acts, ActionOpen) || !strings.Contains(acts, ActionEnd) {
		t.Fatalf("audit = %s", acts)
	}
	if h := m.History("c1", alice()); len(h) != 1 || h[0].EndedBy != EndFinished {
		t.Fatalf("history = %+v", h)
	}
	if h := m.History("c1", bob()); len(h) != 0 {
		t.Fatalf("bob sees alice's history: %+v", h)
	}
}

func TestShellWithoutAShellSaysSo(t *testing.T) {
	m, f, _, _ := setup(t)
	f.noShell = true
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "")
	got := waitState(t, m, s.ID, StateEnded)
	if got.EndedBy != EndFailed || !strings.Contains(got.EndNote, "debug container") {
		t.Fatalf("ended %s: %s", got.EndedBy, got.EndNote)
	}
}

func TestIdleAndLimitAndKeep(t *testing.T) {
	m, _, c, _ := setup(t)
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1", Minutes: 15}, alice(), "", "")
	live := waitState(t, m, s.ID, StateLive)
	if want := live.Started.Add(15 * time.Minute); !live.Ends.Equal(want) {
		t.Fatalf("ends %v, want %v", live.Ends, want)
	}
	c.add(9 * time.Minute)
	if _, err := m.Keep(s.ID, alice()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Keep(s.ID, bob()); !errors.Is(err, ErrNotYours) {
		t.Fatalf("bob kept alice's session: %v", err)
	}
	c.add(9 * time.Minute)
	m.sweep()
	// 18 minutes in, 9 since the keep: the 15-minute limit ends it, not idleness.
	got := waitState(t, m, s.ID, StateEnded)
	if got.EndedBy != EndLimit {
		t.Fatalf("ended by %s", got.EndedBy)
	}

	s2, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "")
	waitState(t, m, s2.ID, StateLive)
	c.add(11 * time.Minute)
	m.sweep()
	if got := waitState(t, m, s2.ID, StateEnded); got.EndedBy != EndIdle {
		t.Fatalf("ended by %s", got.EndedBy)
	}
}

func TestEndsWithTheSignIn(t *testing.T) {
	m, _, _, _ := setup(t)
	signedIn := true
	var mu sync.Mutex
	who := alice()
	who.Stands = func() bool { mu.Lock(); defer mu.Unlock(); return signedIn }
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, who, "", "")
	waitState(t, m, s.ID, StateLive)
	m.sweep()
	if got, _ := m.Get(s.ID); got.State != StateLive {
		t.Fatal("ended while signed in")
	}
	mu.Lock()
	signedIn = false
	mu.Unlock()
	m.sweep()
	if got := waitState(t, m, s.ID, StateEnded); got.EndedBy != EndSignOut {
		t.Fatalf("ended by %s", got.EndedBy)
	}
}

func TestAdminEndsAndWatches(t *testing.T) {
	m, _, _, buf := setup(t)
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "Bob", "")
	waitState(t, m, s.ID, StateLive)
	if _, err := m.End(s.ID, bob(), "no"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("bob, no admin, ended it: %v", err)
	}
	admin := bob()
	admin.Admin = true
	w, _, err := m.Attach(s.ID, admin, true)
	if err != nil {
		t.Fatal(err)
	}
	if !w.ReadOnly {
		t.Fatal("a watcher may type")
	}
	if err := m.Input(s.ID, w, []byte("rm -rf /\n")); !errors.Is(err, ErrNotYours) {
		t.Fatalf("the watcher typed: %v", err)
	}
	if got, _ := m.Get(s.ID); len(got.Watchers) != 1 || got.Watchers[0] != "Bob" {
		t.Fatalf("watchers = %v", got.Watchers)
	}
	if _, err := m.End(s.ID, admin, "migration window over"); err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, s.ID, StateEnded)
	if got.EndedBy != EndAdmin || got.EndNote != "Ended by Bob: migration window over" {
		t.Fatalf("%s: %s", got.EndedBy, got.EndNote)
	}
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("the watcher was not dropped")
	}
	if !strings.Contains(buf.text(), ActionWatch) {
		t.Fatalf("watching was not audited: %s", buf.text())
	}
}

func TestCommandKeepsItsOutput(t *testing.T) {
	m, f, _, buf := setup(t)
	f.output, f.exitCode = "hello\n", 3
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolCommand, Namespace: "web", Pod: "api-1", Command: "echo hello; exit 3"}, alice(), "", "")
	got := waitState(t, m, s.ID, StateEnded)
	if got.Exit == nil || *got.Exit != 3 || got.EndNote != "exit 3" {
		t.Fatalf("exit %v %q", got.Exit, got.EndNote)
	}
	if string(m.Output(s.ID)) != "hello\n" {
		t.Fatalf("output %q", m.Output(s.ID))
	}
	if !strings.Contains(buf.text(), `output=\"hello\\n\"`) {
		t.Fatalf("the audit record has no output: %s", buf.text())
	}
}

func TestDebugContainerMountsVolumesAtTheTargetsPaths(t *testing.T) {
	m, f, _, _ := setup(t)
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolDebugContainer, Namespace: "web", Pod: "api-1", Image: "network", Profile: toolbox.ProfileBaseline, MountVolumes: true, ReadOnly: true}, alice(), "", "")
	got := waitState(t, m, s.ID, StateLive)
	f.mu.Lock()
	pod := f.pods["web/api-1"]
	ec := pod.Spec.EphemeralContainers[0]
	f.mu.Unlock()
	if ec.TargetContainerName != "api" || ec.Image != "nicolaka/netshoot:v0.14" || got.Container != ec.Name {
		t.Fatalf("ec = %+v", ec.EphemeralContainerCommon)
	}
	// The subPath mount is left out; the claim is read-only.
	if len(ec.VolumeMounts) != 1 || ec.VolumeMounts[0].MountPath != "/data" || !ec.VolumeMounts[0].ReadOnly {
		t.Fatalf("mounts = %+v", ec.VolumeMounts)
	}
	m.End(s.ID, alice(), "")
	waitState(t, m, s.ID, StateEnded)
	// A debug container cannot be removed: nothing is deleted.
	if len(f.deleted) != 0 {
		t.Fatalf("deleted %v", f.deleted)
	}
}

func TestDebugPodCopiesLabelsAndIsRemoved(t *testing.T) {
	m, f, _, _ := setup(t)
	f.ns["web"].Labels = map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolDebugPod, Namespace: "web", Image: "network", LabelsFrom: "api-1", Minutes: 30}, alice(), "", "")
	got := waitState(t, m, s.ID, StateLive)
	f.mu.Lock()
	var job *batchv1.Job
	for _, j := range f.jobs {
		job = j
	}
	f.mu.Unlock()
	labels := job.Spec.Template.Labels
	if labels["app"] != "api" || labels["pod-template-hash"] != "" || labels[LabelSession] != s.ID {
		t.Fatalf("labels = %v", labels)
	}
	if *job.Spec.ActiveDeadlineSeconds != 1800 || job.Spec.Template.Spec.ReadinessGates[0].ConditionType != NeverReady {
		t.Fatalf("job spec = %+v", job.Spec)
	}
	// netshoot's NET_RAW and NET_ADMIN are not baseline capabilities.
	if add := job.Spec.Template.Spec.Containers[0].SecurityContext.Capabilities.Add; len(add) != 0 {
		t.Fatalf("added %v in a baseline namespace", add)
	}
	if len(got.Notes) != 2 {
		t.Fatalf("notes = %v", got.Notes)
	}
	m.End(s.ID, alice(), "")
	waitState(t, m, s.ID, StateEnded)
	if len(f.deleted) != 1 || f.deleted[0] != "job/web/"+job.Name {
		t.Fatalf("deleted %v", f.deleted)
	}
}

func TestNodeShellAndNodeCommand(t *testing.T) {
	m, f, _, _ := setup(t)
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolNodeShell, Node: "worker-3", Profile: toolbox.ProfileSysAdmin}, alice(), "Bob", "")
	got := waitState(t, m, s.ID, StateLive)
	f.mu.Lock()
	pod := f.pods["221b-toolbox/"+got.Pod]
	f.mu.Unlock()
	if pod == nil || pod.Spec.NodeName != "worker-3" || !pod.Spec.HostPID || !*pod.Spec.Containers[0].SecurityContext.Privileged || pod.Spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("node pod = %+v", pod)
	}
	m.End(s.ID, alice(), "")
	waitState(t, m, s.ID, StateEnded)
	if len(f.deleted) != 1 || !strings.HasPrefix(f.deleted[0], "pod/221b-toolbox/") {
		t.Fatalf("deleted %v", f.deleted)
	}

	f.output = "Filesystem 84%\n"
	c, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolNodeCommand, Node: "worker-3", Command: "df -h"}, alice(), "Bob", "")
	done := waitState(t, m, c.ID, StateEnded)
	if done.EndedBy != EndFinished || string(m.Output(c.ID)) != "Filesystem 84%\n" {
		t.Fatalf("%s %q", done.EndedBy, m.Output(c.ID))
	}
	f.mu.Lock()
	last := f.execs[len(f.execs)-1]
	f.mu.Unlock()
	if strings.Join(last[1:], " ") != "chroot /host sh -c df -h" {
		t.Fatalf("exec = %v", last)
	}
}

// pipeFrames is one end of a tunnel in memory.
type pipeFrames struct {
	in  chan tunnel.Frame
	out chan tunnel.Frame
}

func (p *pipeFrames) ReadFrame() (tunnel.Frame, error) {
	f, ok := <-p.in
	if !ok {
		return tunnel.Frame{}, io.EOF
	}
	return f, nil
}

func (p *pipeFrames) WriteFrame(f tunnel.Frame) error {
	defer func() { recover() }()
	p.out <- f
	return nil
}

func TestTunnelForwardsConnections(t *testing.T) {
	m, f, _, _ := setup(t)
	_ = f
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolForward, Namespace: "web", Pod: "api-1", Port: 5432}, alice(), "", "")
	waitState(t, m, s.ID, StateLive)
	token, err := m.Token(s.ID, alice())
	if err != nil || token == "" {
		t.Fatal(err)
	}
	if _, err := m.Token(s.ID, bob()); !errors.Is(err, ErrNotYours) {
		t.Fatal("bob read alice's token")
	}
	bad := &pipeFrames{in: make(chan tunnel.Frame, 4), out: make(chan tunnel.Frame, 4)}
	if err := m.Tunnel(context.Background(), s.ID, "wrong", bad); !errors.Is(err, ErrToken) {
		t.Fatalf("a wrong token: %v", err)
	}
	old := &pipeFrames{in: make(chan tunnel.Frame, 4), out: make(chan tunnel.Frame, 4)}
	old.in <- tunnel.Frame{Type: tunnel.Hello, Payload: []byte(`{"version":"0.7.0","host":"laptop"}`)}
	if err := m.Tunnel(context.Background(), s.ID, token, old); !errors.Is(err, ErrCLI) {
		t.Fatalf("an old CLI: %v", err)
	}

	p := &pipeFrames{in: make(chan tunnel.Frame, 16), out: make(chan tunnel.Frame, 16)}
	p.in <- tunnel.Frame{Type: tunnel.Hello, Payload: []byte(`{"version":"0.8.2","host":"laptop","listen":"127.0.0.1:15432"}`)}
	errc := make(chan error, 1)
	go func() { errc <- m.Tunnel(context.Background(), s.ID, token, p) }()
	if fr := <-p.out; fr.Type != tunnel.Info {
		t.Fatalf("first frame %d", fr.Type)
	}
	p.in <- tunnel.Frame{Type: tunnel.Open, Conn: 7, Payload: []byte("127.0.0.1:50000")}
	p.in <- tunnel.Frame{Type: tunnel.Data, Conn: 7, Payload: []byte("select 1")}
	var reply tunnel.Frame
	select {
	case reply = <-p.out:
	case <-time.After(3 * time.Second):
		t.Fatal("no answer through the tunnel")
	}
	if reply.Type != tunnel.Data || reply.Conn != 7 || string(reply.Payload) != "SELECT 1" {
		t.Fatalf("reply %d %d %q", reply.Type, reply.Conn, reply.Payload)
	}
	got, _ := m.Get(s.ID)
	if got.Forward.CLI != "laptop" || got.Forward.Total != 1 || got.Forward.BytesIn != 8 {
		t.Fatalf("forward = %+v", got.Forward)
	}
	m.End(s.ID, alice(), "")
	// The CLI is told the session ended.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case fr := <-p.out:
			if fr.Type == tunnel.End {
				close(p.in)
				<-errc
				return
			}
		case <-deadline:
			t.Fatal("the CLI was not told the session ended")
		}
	}
}

func TestValidate(t *testing.T) {
	c := toolbox.Default()
	ok := Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}
	if err := ok.Validate(c, false); err != nil {
		t.Fatal(err)
	}
	if err := ok.Validate(c, true); err == nil || !strings.Contains(err.Error(), "say why") {
		t.Fatalf("production without a reason: %v", err)
	}
	cases := []Request{
		{Tool: "nope"},
		{Tool: toolbox.ToolShell, Namespace: "Web!", Pod: "x"},
		{Tool: toolbox.ToolShell, Namespace: "web", Pod: "x", Shell: "python; rm"},
		{Tool: toolbox.ToolCommand, Namespace: "web", Pod: "x"},
		{Tool: toolbox.ToolForward, Namespace: "web", Pod: "x", Port: 70000},
		{Tool: toolbox.ToolDebugContainer, Namespace: "web", Pod: "x", Image: "evil:latest"},
		{Tool: toolbox.ToolDebugPod, Namespace: "web", Image: "network", Labels: map[string]string{"bad key!": "v"}},
		{Tool: toolbox.ToolNodeShell, Node: "n1", Profile: "root"},
		{Tool: toolbox.ToolShell, Namespace: "web", Pod: "x", Minutes: 600},
	}
	for _, r := range cases {
		if err := r.Validate(c, false); err == nil {
			t.Errorf("%+v passed", r)
		}
	}
	off := c
	off.PortForward.Enabled = false
	if err := (&Request{Tool: toolbox.ToolForward, Namespace: "web", Pod: "x", Port: 80}).Validate(off, false); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("a tool turned off: %v", err)
	}
}

func TestProfilesFollowPodSecurity(t *testing.T) {
	if got := ProfilesAllowed(LevelRestricted); len(got) != 1 || got[0] != toolbox.ProfileRestricted {
		t.Fatal(got)
	}
	if got := ProfilesAllowed(LevelBaseline); len(got) != 2 {
		t.Fatal(got)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}}}
	if PodSecurity(ns) != LevelRestricted || PodSecurity(&corev1.Namespace{}) != LevelPrivileged {
		t.Fatal("levels")
	}
}

func TestPlanUsesTheDryRun(t *testing.T) {
	m, f, _, _ := setup(t)
	p := m.PlanFor(context.Background(), Cluster{ID: "c1"}, Request{Tool: toolbox.ToolDebugContainer, Namespace: "web", Pod: "api-1", Image: "general", Profile: toolbox.ProfileGeneral}, kube.Person{User: "alice"}, "Alice")
	if p.DryRunExit != 0 || f.dryRuns != 1 || !strings.Contains(p.Object, "targetContainerName: api") {
		t.Fatalf("plan = %+v", p)
	}
	f.refuse = errors.New(`pods "api-1" is forbidden: violates PodSecurity "baseline:latest"`)
	p = m.PlanFor(context.Background(), Cluster{ID: "c1"}, Request{Tool: toolbox.ToolDebugContainer, Namespace: "web", Pod: "api-1", Image: "general", Profile: toolbox.ProfileGeneral}, kube.Person{User: "alice"}, "Alice")
	if p.DryRunExit == 0 || !strings.Contains(p.DryRun[0], "PodSecurity") {
		t.Fatalf("refused plan = %+v", p)
	}
	n := m.PlanFor(context.Background(), Cluster{ID: "c1"}, Request{Tool: toolbox.ToolNodeShell, Node: "worker-3", Profile: toolbox.ProfileGeneral}, kube.Person{User: "alice"}, "Alice")
	if !strings.Contains(strings.Join(n.Impact, " "), "root over every pod") || !strings.Contains(n.Object, "hostPID: true") {
		t.Fatalf("node plan = %+v", n)
	}
}

func TestTunnelVersions(t *testing.T) {
	if !tunnel.Older("0.7.9", "0.8.0") || tunnel.Older("0.8.0", "0.8.0") || tunnel.Older("v0.10.1", "0.8.0") || !tunnel.Older("dev", "0.8.0") {
		t.Fatal("version order")
	}
	f := tunnel.Frame{Type: tunnel.Data, Conn: 42, Payload: []byte("x")}
	got, err := tunnel.Decode(tunnel.Encode(f))
	if err != nil || got.Conn != 42 || string(got.Payload) != "x" {
		t.Fatal(got, err)
	}
}
