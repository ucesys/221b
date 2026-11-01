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
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/exec"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
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
	// onCreate runs inside a create, as the API server takes its time.
	onCreate func(ctx context.Context) error
	// noisy makes a command print until it is stopped, and a while after.
	noisy bool
}

func (f *fakeBackend) creating(ctx context.Context, dry bool) error {
	f.mu.Lock()
	hook := f.onCreate
	f.mu.Unlock()
	if dry || hook == nil {
		return nil
	}
	return hook(ctx)
}

func newFake() *fakeBackend {
	f := &fakeBackend{pods: map[string]*corev1.Pod{}, ns: map[string]*corev1.Namespace{}, jobs: map[string]*batchv1.Job{}}
	f.addPod("web", "api-1", "api")
	f.ns["web"] = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "web"}}
	return f
}

func (f *fakeBackend) addPod(ns, name string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(ns + "/" + name + "#1"), Labels: map[string]string{"app": "api", "pod-template-hash": "abc"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
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
		return nil, apierrors.NewNotFound(corev1.Resource("pods"), name)
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
	noShell, code, out, noisy := f.noShell, f.exitCode, f.output, f.noisy
	f.mu.Unlock()
	if noShell {
		return errors.New(`exec: "sh": executable file not found in $PATH`)
	}
	if noisy {
		for ctx.Err() == nil {
			io.WriteString(stdout, "line of output\n")
		}
		for range 2000 {
			io.WriteString(stdout, "line of output\n")
		}
		return ctx.Err()
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

func (f *fakeBackend) CreateJob(ctx context.Context, job *batchv1.Job, dry bool) error {
	if err := f.creating(ctx, dry); err != nil {
		return err
	}
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

func (f *fakeBackend) CreatePod(ctx context.Context, pod *corev1.Pod, dry bool) error {
	if err := f.creating(ctx, dry); err != nil {
		return err
	}
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

func TestExtendMovesTheHardLimitWithinTheCharts(t *testing.T) {
	m, _, c, buf := setup(t)
	// Asked for 15 minutes on an ordinary cluster: Extend adds 10 at a
	// time, up to the chart's 60 from the start (TB-22).
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1", Minutes: 15}, alice(), "", "")
	live := waitState(t, m, s.ID, StateLive)
	if want := live.Started.Add(time.Hour); !live.MaxEnds.Equal(want) {
		t.Fatalf("max ends %v, want %v", live.MaxEnds, want)
	}
	c.add(12 * time.Minute)
	if _, err := m.Extend(s.ID, bob()); !errors.Is(err, ErrNotYours) {
		t.Fatalf("bob extended alice's session: %v", err)
	}
	got, err := m.Extend(s.ID, alice())
	if err != nil {
		t.Fatal(err)
	}
	if want := live.Started.Add(25 * time.Minute); !got.Ends.Equal(want) || !got.LastActive.Equal(c.now()) {
		t.Fatalf("ends %v (want %v), last active %v", got.Ends, want, got.LastActive)
	}
	for i := 0; i < 3; i++ {
		if _, err := m.Extend(s.ID, alice()); err != nil {
			t.Fatal(err)
		}
	}
	// 55, then the cap at 60.
	if got, _ = m.Extend(s.ID, alice()); !got.Ends.Equal(live.MaxEnds) {
		t.Fatalf("ends %v, want the chart's limit %v", got.Ends, live.MaxEnds)
	}
	if _, err := m.Extend(s.ID, alice()); !errors.Is(err, ErrLimit) {
		t.Fatalf("extended past the chart's limit: %v", err)
	}
	if !strings.Contains(buf.text(), ActionExtend) {
		t.Fatal("extending was not audited")
	}
	// A duration a second person approved is the limit.
	s2, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1", Minutes: 30}, alice(), "Bob", "card2")
	live2 := waitState(t, m, s2.ID, StateLive)
	if !live2.MaxEnds.Equal(live2.Ends) {
		t.Fatalf("an approved session may be extended: %v past %v", live2.MaxEnds, live2.Ends)
	}
	if _, err := m.Extend(s2.ID, alice()); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "Bob") {
		t.Fatalf("an approved duration was extended: %v", err)
	}
}

func TestASessionEndsWithItsPod(t *testing.T) {
	m, f, _, _ := setup(t)
	m.opts.PodCheck = 20 * time.Millisecond
	// A forward has no stream to notice by: it looks at its pod (TB-13).
	fwd, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolForward, Namespace: "web", Pod: "api-1", Port: 80}, alice(), "", "")
	waitState(t, m, fwd.ID, StateLive)
	f.mu.Lock()
	delete(f.pods, "web/api-1")
	f.mu.Unlock()
	if got := waitState(t, m, fwd.ID, StateEnded); got.EndedBy != EndGone || got.EndNote != "The pod is gone." {
		t.Fatalf("%s: %s", got.EndedBy, got.EndNote)
	}
	// A pod made again under the same name is another pod.
	f.mu.Lock()
	f.addPod("web", "api-1", "api")
	f.mu.Unlock()
	fwd2, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolForward, Namespace: "web", Pod: "api-1", Port: 80}, alice(), "", "")
	waitState(t, m, fwd2.ID, StateLive)
	f.mu.Lock()
	f.pods["web/api-1"].UID = "web/api-1#2"
	f.mu.Unlock()
	if got := waitState(t, m, fwd2.ID, StateEnded); got.EndedBy != EndGone || got.EndNote != "The pod was replaced." {
		t.Fatalf("%s: %s", got.EndedBy, got.EndNote)
	}
	// A shell whose pod goes ends as the pod's end, not the shell's.
	sh, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "")
	waitState(t, m, sh.ID, StateLive)
	v, replay, _ := m.Attach(sh.ID, alice(), false)
	if !strings.Contains(string(replay), "$ ") {
		read(t, v, "$ ")
	}
	f.mu.Lock()
	delete(f.pods, "web/api-1")
	f.mu.Unlock()
	if err := m.Input(sh.ID, v, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	if got := waitState(t, m, sh.ID, StateEnded); got.EndedBy != EndGone {
		t.Fatalf("%s: %s", got.EndedBy, got.EndNote)
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

func TestAViewerThatFallsBehindIsDroppedNotEnded(t *testing.T) {
	m, _, _, _ := setup(t)
	m.opts.ViewerWait = 20 * time.Millisecond
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "")
	waitState(t, m, s.ID, StateLive)
	slow, _, err := m.Attach(s.ID, alice(), false)
	if err != nil {
		t.Fatal(err)
	}
	quick, _, err := m.Attach(s.ID, alice(), false)
	if err != nil {
		t.Fatal(err)
	}
	// The quick browser drains as the output comes.
	var qmu sync.Mutex
	var got []byte
	go func() {
		for {
			select {
			case b := <-quick.Out:
				qmu.Lock()
				got = append(got, b...)
				qmu.Unlock()
			case <-quick.Done():
				return
			}
		}
	}()
	// Output waits briefly for a browser that is behind, so a burst slows
	// the shell rather than losing a window.
	w := &termWriter{m: m, s: m.all[s.ID]}
	start := time.Now()
	for i := 0; i < 520; i++ {
		w.Write([]byte("y"))
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("the writer did not wait for the slow viewer")
	}
	// Still behind after the wait: dropped, and told it was only this
	// window, not the session, that ended.
	select {
	case <-slow.Done():
	default:
		t.Fatal("the slow viewer was not dropped")
	}
	if !slow.Dropped() {
		t.Fatal("the dropped viewer reads as the session's end")
	}
	if cur, _ := m.Get(s.ID); cur.State != StateLive {
		t.Fatalf("the session %s", cur.State)
	}
	// The other browser kept everything.
	deadline := time.Now().Add(time.Second)
	for {
		qmu.Lock()
		n := len(got)
		qmu.Unlock()
		if n >= 520 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the quick viewer got %d bytes", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-quick.Done():
		t.Fatal("the quick viewer was dropped")
	default:
	}
	// Reattaching replays what was shown and goes on from there.
	again, replay, err := m.Attach(s.ID, alice(), false)
	if err != nil || len(replay) < 520 {
		t.Fatalf("replay %d bytes: %v", len(replay), err)
	}
	w.Write([]byte("z"))
	if b := <-again.Out; string(b) != "z" {
		t.Fatalf("after the replay: %q", b)
	}
	// The session's end, by contrast, is not a drop.
	m.End(s.ID, alice(), "")
	<-again.Done()
	if again.Dropped() {
		t.Fatal("the end reads as a drop")
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
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolDebugContainer, Namespace: "web", Pod: "api-1", Image: "network", Profile: toolbox.ProfileGeneral, MountVolumes: true, ReadOnly: true}, alice(), "", "")
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
	if *job.Spec.ActiveDeadlineSeconds != int64((m.opts.Config.MaxDuration.D()+m.opts.StartWithin)/time.Second) || job.Spec.Template.Spec.ReadinessGates[0].ConditionType != NeverReady {
		t.Fatalf("job spec = %+v", job.Spec)
	}
	// No ServiceAccount named: no token, so a shell in the pod holds no
	// credential; a named account mounts its own (TB-28, H-2 of the Phase
	// 9 review).
	if auto := job.Spec.Template.Spec.AutomountServiceAccountToken; auto == nil || *auto {
		t.Fatalf("a debug pod without a ServiceAccount mounts a token: %v", auto)
	}
	if j, _, err := DebugJob(toolbox.Default(), "abcdef0123456789", "Alice", Request{Tool: toolbox.ToolDebugPod, Namespace: "web", Image: "general", ServiceAccount: "ops"}, LevelPrivileged, nil, time.Hour); err != nil || j.Spec.Template.Spec.ServiceAccountName != "ops" || !*j.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatalf("a named ServiceAccount is not mounted: %v %+v", err, j.Spec.Template.Spec)
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
	pod := f.pods["221b-privileged/"+got.Pod]
	f.mu.Unlock()
	if pod == nil || pod.Spec.NodeName != "worker-3" || !pod.Spec.HostPID || !*pod.Spec.Containers[0].SecurityContext.Privileged || pod.Spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("node pod = %+v", pod)
	}
	m.End(s.ID, alice(), "")
	waitState(t, m, s.ID, StateEnded)
	if len(f.deleted) != 1 || !strings.HasPrefix(f.deleted[0], "pod/221b-privileged/") {
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

func TestGeneralDebugContainerIsAsCapableAsTheNamespaceAllows(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api"}}}}
	r := Request{Tool: toolbox.ToolDebugContainer, Image: "network", Profile: toolbox.ProfileGeneral}
	c := toolbox.Default()
	ec, err := EphemeralContainer(c, "abcd1234", r, pod, LevelPrivileged)
	if err != nil || ec.SecurityContext.Capabilities == nil || len(ec.SecurityContext.Capabilities.Add) != len(c.Images["network"].Caps) || ec.SecurityContext.RunAsUser != nil {
		t.Fatalf("privileged: %+v %v", ec.SecurityContext, err)
	}
	// NET_RAW and NET_ADMIN are not baseline capabilities.
	if ec, _ := EphemeralContainer(c, "abcd1234", r, pod, LevelBaseline); ec.SecurityContext.Capabilities != nil {
		t.Fatalf("baseline: %+v", ec.SecurityContext.Capabilities)
	}
	r.Profile = toolbox.ProfileRestricted
	if ec, _ := EphemeralContainer(c, "abcd1234", r, pod, LevelRestricted); *ec.SecurityContext.RunAsUser != 65532 || *ec.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("restricted: %+v", ec.SecurityContext)
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

// A pairing request may name more nodes: validated and deduplicated,
// summed up in one line, split into one request per node under one card
// (asked October 30, 2026); other node tools take one node.
func TestARequestOnSeveralNodes(t *testing.T) {
	c := toolbox.Default()
	c.SSH.Enabled = true
	r := Request{Tool: toolbox.ToolSSHPair, Node: "a", Nodes: []string{"b", " a ", "c", "b", ""}, LoginUser: "ops"}
	if err := r.Validate(c, false); err != nil {
		t.Fatal(err)
	}
	if got := r.AllNodes(); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("nodes %v", got)
	}
	if s := r.Summary(); s != "pair 3 nodes for SSH as ops: a, b, c" {
		t.Fatalf("summary %q", s)
	}
	each := r.Each()
	if len(each) != 3 || each[1].Node != "b" || each[1].Nodes != nil || each[1].LoginUser != "ops" || each[2].Port != r.Port {
		t.Fatalf("each: %+v", each)
	}
	bad := Request{Tool: toolbox.ToolSSHPair, Node: "a", Nodes: []string{"Not A Node"}, LoginUser: "ops"}
	if err := bad.Validate(c, false); err == nil {
		t.Fatal("a bad node name passed")
	}
	shell := Request{Tool: toolbox.ToolNodeShell, Node: "a", Nodes: []string{"b"}}
	if err := shell.Validate(c, false); err == nil || !strings.Contains(err.Error(), "one node at a time") {
		t.Fatalf("a node shell on two nodes: %v", err)
	}
}

// Only pairing takes several nodes: any other tool's body with nodes would
// open a session for each under a card that names one.
func TestOnlyPairingTakesSeveralNodes(t *testing.T) {
	c := toolbox.Default()
	c.SSH.Enabled = true
	for _, r := range []Request{
		{Tool: toolbox.ToolDebugPod, Namespace: "x", Image: "general", Nodes: []string{"a", "b"}},
		{Tool: toolbox.ToolShell, Namespace: "x", Pod: "p", Shell: "auto", Nodes: []string{"a"}},
		{Tool: toolbox.ToolCommand, Namespace: "x", Pod: "p", Shell: "auto", Command: "ls", Nodes: []string{"a"}},
		{Tool: toolbox.ToolSSH, Node: "a", Nodes: []string{"b"}},
	} {
		if err := r.Validate(c, false); err == nil || !strings.Contains(err.Error(), "one node at a time") {
			t.Errorf("%s with nodes: %v", r.Tool, err)
		}
	}
}

// Pairing runs a general node pod, so a chart that leaves that profile
// out refuses it as it refuses a node command.
func TestPairingRespectsTheNodeProfiles(t *testing.T) {
	c := toolbox.Default()
	c.SSH.Enabled = true
	c.NodeShell.Profiles = []string{toolbox.ProfileNetAdmin}
	for _, r := range []Request{{Tool: toolbox.ToolSSHPair, Node: "a", LoginUser: "ops"}, {Tool: toolbox.ToolSSHUnpair, Node: "a"}} {
		if err := r.Validate(c, false); err == nil || !strings.Contains(err.Error(), "node shell profiles here") {
			t.Errorf("%s: %v", r.Tool, err)
		}
	}
}

// An SSH session is root on the node: with approval always it waits for a
// second person, as a node shell does.
func TestAnSSHSessionNeedsTheSecondPersonANodeShellDoes(t *testing.T) {
	c := toolbox.Default()
	c.NodeShell.Approval = toolbox.ApprovalAlways
	if !NeedsTwo(c, toolbox.ToolSSH) || !NeedsTwo(c, toolbox.ToolNodeShell) || NeedsTwo(c, toolbox.ToolShell) {
		t.Fatal("the second person")
	}
}

// The SSH tools' cards say what they do; pairing goes through the node's
// kubelet, so a node that is not Ready refuses it, and SSH does not.
func TestTheCardsOfTheSSHTools(t *testing.T) {
	m, _, _, _ := setup(t)
	ctx := context.Background()
	who := kube.Person{User: "alice"}
	pair := Request{Tool: toolbox.ToolSSHPair, Node: "worker-3", LoginUser: "ops", Port: 22, Profile: toolbox.ProfileGeneral}
	p := m.PlanFor(ctx, Cluster{ID: "c1"}, pair, who, "Alice")
	if len(p.Targets) != 1 || p.Targets[0] != "node/worker-3" || p.Object == "" || !strings.Contains(strings.Join(p.Impact, " "), "admits ops on port 22") {
		t.Fatalf("the pairing card: %+v", p)
	}
	un := m.PlanFor(ctx, Cluster{ID: "c1"}, Request{Tool: toolbox.ToolSSHUnpair, Node: "worker-3", Profile: toolbox.ProfileGeneral}, who, "Alice")
	if len(un.Targets) != 1 || !strings.Contains(strings.Join(un.Impact, " "), "removes the console's SSH authority") {
		t.Fatalf("the unpairing card: %+v", un)
	}
	s := m.PlanFor(ctx, Cluster{ID: "c1"}, Request{Tool: toolbox.ToolSSH, Node: "worker-3"}, who, "Alice")
	if len(s.Targets) != 1 || s.Object != "" || len(s.DryRun) != 1 || !strings.Contains(s.DryRun[0], "No access review") {
		t.Fatalf("the SSH card: %+v", s)
	}
}

type fakeTerm struct{ closed atomic.Bool }

func (t *fakeTerm) Run(context.Context, io.Reader, io.Writer, <-chan Size) error { return nil }
func (t *fakeTerm) Close() error                                                 { t.closed.Store(true); return nil }

// A connection that comes up after its session ended is dropped: it is
// authenticated as root, and nothing else would ever close it.
func TestASessionEndedDuringTheDialDropsTheConnection(t *testing.T) {
	m, _, _, _ := setup(t)
	s := &Session{}
	s.State = StateEnded
	term := &fakeTerm{}
	m.sshTerminal(context.Background(), s, term, "")
	if !term.closed.Load() {
		t.Fatal("the connection was left open")
	}
}

// Who may have a transcript does not change when the session leaves memory.
func TestATranscriptFollowsTheSessionsRecord(t *testing.T) {
	m, _, _, _ := setup(t)
	s, err := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "card1")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateLive)
	admin := bob()
	admin.Admin = true
	for _, c := range []struct {
		name    string
		cluster string
		who     Who
		visible bool
	}{{"its owner", "c1", alice(), true}, {"another person", "c1", bob(), false}, {"a SuperAdmin", "c1", admin, true}, {"another cluster", "c2", admin, false}} {
		if _, got := m.Recorded(c.cluster, s.ID, c.who); got != c.visible {
			t.Errorf("live, %s: %v", c.name, got)
		}
	}
	m.opts.History.Add(Record{ID: "gone", Cluster: "c1", OwnerUser: "alice", Recorded: true})
	m.opts.History.Add(Record{ID: "nobody", Cluster: "c1", Recorded: true})
	for _, c := range []struct {
		name string
		id   string
		who  Who
		seen bool
	}{{"its owner", "gone", alice(), true}, {"another person", "gone", bob(), false}, {"a SuperAdmin", "gone", admin, true},
		{"anyone, with no owner on the record", "nobody", Who{SignIn: "s-x"}, false}, {"anyone, with no record", "unknown", alice(), false}} {
		if recorded, got := m.Recorded("c1", c.id, c.who); got != c.seen || (got && !recorded) {
			t.Errorf("gone, %s: recorded %v, visible %v", c.name, recorded, got)
		}
	}
}

// A pod's deadline cannot be raised, so a session that Extend can move
// gets one that reaches as far as Extend does; a duration a second person
// approved stays the limit. Either way the console ends it at its limit.
func TestAPodsDeadlineReachesWhatExtendCan(t *testing.T) {
	m, f, c, _ := setup(t)
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolNodeShell, Node: "worker-3", Minutes: 30}, alice(), "", "")
	got := waitState(t, m, s.ID, StateLive)
	f.mu.Lock()
	deadline := time.Duration(*f.pods["221b-privileged/"+got.Pod].Spec.ActiveDeadlineSeconds) * time.Second
	f.mu.Unlock()
	if deadline < got.MaxEnds.Sub(got.Started) {
		t.Fatalf("the pod stops at %s, and Extend reaches %s", deadline, got.MaxEnds.Sub(got.Started))
	}
	if _, err := m.Extend(s.ID, alice()); err != nil {
		t.Fatal(err)
	}
	c.add(41 * time.Minute)
	m.sweep()
	if got := waitState(t, m, s.ID, StateEnded); got.EndedBy != EndLimit {
		t.Fatalf("ended by %s, want its limit of 40 minutes", got.EndedBy)
	}

	a, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolNodeShell, Node: "worker-3", Minutes: 30}, alice(), "Bob", "")
	got = waitState(t, m, a.ID, StateLive)
	f.mu.Lock()
	deadline = time.Duration(*f.pods["221b-privileged/"+got.Pod].Spec.ActiveDeadlineSeconds) * time.Second
	f.mu.Unlock()
	if deadline != 30*time.Minute+m.opts.StartWithin {
		t.Fatalf("an approved 30 minutes gives a deadline of %s", deadline)
	}
}

// What a session's end could not know of is still removed: the object
// that appears after the end took its list, whether the create answered
// or was cut short by the end though the API server had taken it.
func TestAnObjectCreatedWhileTheSessionEndsIsRemoved(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		req        Request
		answer     error
	}{
		{"pod, created", "pod/221b-privileged/", Request{Tool: toolbox.ToolNodeShell, Node: "worker-3"}, nil},
		{"pod, cut short", "pod/221b-privileged/", Request{Tool: toolbox.ToolNodeShell, Node: "worker-3"}, context.Canceled},
		{"job, created", "job/web/", Request{Tool: toolbox.ToolDebugPod, Namespace: "web", Image: "network"}, nil},
		{"job, cut short", "job/web/", Request{Tool: toolbox.ToolDebugPod, Namespace: "web", Image: "network"}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f, _, _ := setup(t)
			creating := make(chan struct{})
			f.onCreate = func(ctx context.Context) error {
				close(creating)
				<-ctx.Done()
				return tc.answer
			}
			s, err := m.Start(Cluster{ID: "c1"}, tc.req, alice(), "", "")
			if err != nil {
				t.Fatal(err)
			}
			<-creating
			if _, err := m.End(s.ID, alice(), ""); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				f.mu.Lock()
				deleted := append([]string(nil), f.deleted...)
				f.mu.Unlock()
				if len(deleted) == 1 && strings.HasPrefix(deleted[0], tc.want) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("deleted %v, want one %s", deleted, tc.want)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got := waitState(t, m, s.ID, StateEnded); got.EndedBy != EndOwner || len(got.Objects) != 0 {
				t.Fatalf("ended by %s, objects %v", got.EndedBy, got.Objects)
			}
		})
	}
}

// Ending a command that is still printing reads its output under the
// lock the stream writes under: go test -race says so.
func TestEndingACommandThatPrintsIsNoRace(t *testing.T) {
	m, f, _, buf := setup(t)
	f.noisy = true
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolCommand, Namespace: "web", Pod: "api-1", Command: "yes"}, alice(), "", "")
	waitState(t, m, s.ID, StateLive)
	for len(m.Output(s.ID)) == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := m.End(s.ID, alice(), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateEnded)
	if !strings.Contains(buf.text(), "line of output") {
		t.Fatalf("the audit record has no output: %s", buf.text())
	}
}

// Asking whether a sign-in stands can take a directory's seconds: the
// manager's lock, which every terminal needs, is free meanwhile.
func TestTheSweepAsksAboutSignInsOutsideTheLock(t *testing.T) {
	m, _, _, _ := setup(t)
	var blocked atomic.Bool
	who := alice()
	who.Stands = func() bool {
		free := make(chan struct{})
		go func() {
			m.mu.Lock()
			m.mu.Unlock()
			close(free)
		}()
		select {
		case <-free:
		case <-time.After(2 * time.Second):
			blocked.Store(true)
		}
		return true
	}
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, who, "", "")
	waitState(t, m, s.ID, StateLive)
	m.sweep()
	if blocked.Load() {
		t.Fatal("the sweep held the lock while it asked")
	}
}

// A backend whose first connection to the kubelet is dead: the pod is
// fine, every stream on that connection fails.
type dyingBackend struct {
	*fakeBackend
	dials  atomic.Int32
	closed atomic.Bool
}

type deadForwarder struct{ closed *atomic.Bool }

func (d deadForwarder) Dial(int) (io.ReadWriteCloser, error) {
	return nil, errors.New("creating the error stream: connection lost")
}
func (d deadForwarder) Close() error { d.closed.Store(true); return nil }

func (b *dyingBackend) Forwarder(ctx context.Context, ns, pod string) (Forwarder, error) {
	if b.dials.Add(1) == 1 {
		return deadForwarder{&b.closed}, nil
	}
	return b.fakeBackend.Forwarder(ctx, ns, pod)
}

// startTunnel opens a forward's tunnel and returns its frames.
func startTunnel(t *testing.T, m *Manager) (*pipeFrames, tunnel.InfoBody) {
	t.Helper()
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolForward, Namespace: "web", Pod: "api-1", Port: 5432}, alice(), "", "")
	waitState(t, m, s.ID, StateLive)
	token, err := m.Token(s.ID, alice())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeFrames{in: make(chan tunnel.Frame, 16), out: make(chan tunnel.Frame, 16)}
	p.in <- tunnel.Frame{Type: tunnel.Hello, Payload: []byte(`{"version":"0.8.2","host":"laptop"}`)}
	go m.Tunnel(context.Background(), s.ID, token, p)
	t.Cleanup(func() { close(p.in) })
	first := nextFrame(t, p)
	var info tunnel.InfoBody
	if first.Type != tunnel.Info || json.Unmarshal(first.Payload, &info) != nil {
		t.Fatalf("first frame %d %q", first.Type, first.Payload)
	}
	return p, info
}

func nextFrame(t *testing.T, p *pipeFrames) tunnel.Frame {
	t.Helper()
	select {
	case f := <-p.out:
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame through the tunnel")
		return tunnel.Frame{}
	}
}

// The API server restarts during a forward: the pod runs on, the
// connection to it does not. The connection that fails says so, and the
// next one dials again.
func TestAForwardDialsAgainAfterItsConnectionDies(t *testing.T) {
	m, f, _, _ := setup(t)
	b := &dyingBackend{fakeBackend: f}
	m.opts.Backend = func(Cluster, kube.Person) (Backend, error) { return b, nil }
	p, _ := startTunnel(t, m)

	p.in <- tunnel.Frame{Type: tunnel.Open, Conn: 1, Payload: []byte("127.0.0.1:50000")}
	if fr := nextFrame(t, p); fr.Type != tunnel.Close || fr.Conn != 1 || !strings.Contains(string(fr.Payload), "connection lost") {
		t.Fatalf("the dead connection: %d %d %q", fr.Type, fr.Conn, fr.Payload)
	}
	if !b.closed.Load() {
		t.Fatal("the dead connection was not closed")
	}
	p.in <- tunnel.Frame{Type: tunnel.Open, Conn: 2, Payload: []byte("127.0.0.1:50001")}
	p.in <- tunnel.Frame{Type: tunnel.Data, Conn: 2, Payload: []byte("select 1")}
	if fr := nextFrame(t, p); fr.Type != tunnel.Data || fr.Conn != 2 || string(fr.Payload) != "SELECT 1" {
		t.Fatalf("after dialling again: %d %d %q", fr.Type, fr.Conn, fr.Payload)
	}
	if b.dials.Load() != 2 {
		t.Fatalf("dialled %d times", b.dials.Load())
	}
}

// A port whose program answers once the client has finished sending.
type halfConn struct {
	net.Conn
	finished chan struct{}
}

func (c *halfConn) CloseWrite() error { close(c.finished); return nil }

type halfForwarder struct{}

func (halfForwarder) Dial(int) (io.ReadWriteCloser, error) {
	a, b := net.Pipe()
	c := &halfConn{Conn: a, finished: make(chan struct{})}
	go func() {
		<-c.finished
		b.Write([]byte("the answer"))
		b.Close()
	}()
	return c, nil
}
func (halfForwarder) Close() error { return nil }

type halfBackend struct{ *fakeBackend }

func (halfBackend) Forwarder(context.Context, string, string) (Forwarder, error) {
	return halfForwarder{}, nil
}

// A program that has finished sending tells the pod so and keeps the
// connection, for the answer.
func TestACloseWriteTellsThePodAndKeepsTheAnswer(t *testing.T) {
	m, f, _, _ := setup(t)
	m.opts.Backend = func(Cluster, kube.Person) (Backend, error) { return halfBackend{f}, nil }
	p, info := startTunnel(t, m)
	if !info.CloseWrite {
		t.Fatal("the console does not say it takes CloseWrite")
	}
	p.in <- tunnel.Frame{Type: tunnel.Open, Conn: 3}
	p.in <- tunnel.Frame{Type: tunnel.CloseWrite, Conn: 3}
	if fr := nextFrame(t, p); fr.Type != tunnel.Data || string(fr.Payload) != "the answer" {
		t.Fatalf("after CloseWrite: %d %q", fr.Type, fr.Payload)
	}
	if fr := nextFrame(t, p); fr.Type != tunnel.Close || fr.Conn != 3 {
		t.Fatalf("then: %d %d", fr.Type, fr.Conn)
	}
}

// A console without SSH: the tools fail with a message, in the session
// (Phase 11), with no check of their own at each use.
func TestAConsoleWithoutSSHFailsTheSSHToolsWithAMessage(t *testing.T) {
	m, _, _, _ := setup(t)
	for _, req := range []Request{{Tool: toolbox.ToolSSH, Node: "worker-1"}, {Tool: toolbox.ToolSSHPair, Node: "worker-1", LoginUser: "ops"}} {
		s, err := m.Start(Cluster{ID: "c1"}, req, alice(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := waitState(t, m, s.ID, StateEnded); !strings.Contains(got.EndNote, "SSH is not part of this console") {
			t.Fatalf("%s: %q", req.Tool, got.EndNote)
		}
	}
}

// Each session of a card says so in the audit log, the second node's as
// much as the first's: the card's own record names only the first.
func TestASessionsAuditRecordsNameItsCard(t *testing.T) {
	m, f, _, buf := setup(t)
	f.addPod("web", "api-1", "app")
	s, err := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "card-7")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateLive)
	if !strings.Contains(buf.text(), "session="+s.ID+" tool=shell card=card-7") {
		t.Fatalf("audit %s", buf.text())
	}
}

func TestThePredicateForBeingRootOnANode(t *testing.T) {
	for tool, want := range map[string]bool{toolbox.ToolNodeShell: true, toolbox.ToolNodeCommand: true, toolbox.ToolSSHPair: true, toolbox.ToolSSHUnpair: true, toolbox.ToolSSH: true, toolbox.ToolShell: false, toolbox.ToolForward: false} {
		if OnNodeOrSSH(tool) != want {
			t.Errorf("%s: %v", tool, !want)
		}
	}
}

// A restore merges by id, keeps what the toolbox keeps, newest first, and
// a second one adds nothing.
func TestARestoreMergesRecordsUpToWhatIsKept(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	h := &KVHistory{Backend: &kv.Memory{}, Key: HistoryKey, Keep: 3}
	h.Add(Record{ID: "kept", Ended: at})
	h.Add(Record{ID: "old", Ended: at.Add(-time.Hour)})
	add := []Record{{ID: "kept", Ended: at, Summary: "from the file"}, {ID: "newer", Ended: at.Add(time.Hour)}, {ID: "newest", Ended: at.Add(2 * time.Hour)}}
	n, err := h.Merge(ctx, add)
	if err != nil || n != 2 {
		t.Fatalf("merged %d: %v", n, err)
	}
	got := h.List()
	if len(got) != 3 || got[0].ID != "newest" || got[1].ID != "newer" || got[2].ID != "kept" || got[2].Summary != "" {
		t.Fatalf("%+v", got)
	}
	if n, err := h.Merge(ctx, add[:2]); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}

// conflictOnce is a store where another replica saves between a read and
// a write.
type conflictOnce struct {
	kv.Memory
	hit bool
}

func (c *conflictOnce) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if !c.hit {
		c.hit = true
		return "", kv.ErrConflict
	}
	return c.Memory.SaveKey(ctx, key, b, version)
}

func TestARestoreTriesAgainAfterAConflict(t *testing.T) {
	h := &KVHistory{Backend: &conflictOnce{}, Key: HistoryKey}
	if n, err := h.Merge(context.Background(), []Record{{ID: "a"}}); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

// A node's host keys reach the pairing record and never the output anyone
// sees, whatever chunks they arrive in.
func TestHostKeysStayOffTheOutput(t *testing.T) {
	text := "pairing\n" + HostKeyMark + "ssh-ed25519 AAAA\ndone\n" + HostKeyMark + "ssh-rsa BBBB\n221B-PAIRED ops"
	for _, size := range []int{1, 3, 7, len(text)} {
		var shown bytes.Buffer
		h := &hidingWriter{w: &shown, mark: []byte(HostKeyMark)}
		for rest := text; len(rest) > 0; {
			n := min(size, len(rest))
			h.Write([]byte(rest[:n]))
			rest = rest[n:]
		}
		h.flush()
		if got := shown.String(); got != "pairing\ndone\n221B-PAIRED ops" {
			t.Fatalf("chunks of %d: shown %q", size, got)
		}
		if got := h.hidden.String(); got != HostKeyMark+"ssh-ed25519 AAAA\n"+HostKeyMark+"ssh-rsa BBBB\n" {
			t.Fatalf("chunks of %d: hidden %q", size, got)
		}
	}
}

// Reading another person's live output is watching it: the owner sees who,
// and the audit trail has it (TB-55). The owner's own read is neither, and
// an ended session's output stays for those who may see it.
func TestReadingSomeoneElsesLiveOutputIsWatchingIt(t *testing.T) {
	m, _, _, buf := setup(t)
	m.opts.ReadShown = 30 * time.Millisecond
	s, _ := m.Start(Cluster{ID: "c1"}, Request{Tool: toolbox.ToolShell, Namespace: "web", Pod: "api-1"}, alice(), "", "")
	waitState(t, m, s.ID, StateLive)
	m.ReadOutput(s.ID, alice())
	if got, _ := m.Get(s.ID); len(got.Watchers) != 0 || strings.Contains(buf.text(), ActionWatch) {
		t.Fatalf("the owner's read counted as watching: %v %s", got.Watchers, buf.text())
	}
	admin := bob()
	admin.Admin = true
	m.ReadOutput(s.ID, admin)
	m.ReadOutput(s.ID, admin)
	if got, _ := m.Get(s.ID); len(got.Watchers) != 1 || got.Watchers[0] != "Bob" {
		t.Fatalf("watchers = %v", got.Watchers)
	}
	if n := strings.Count(buf.text(), ActionWatch); n != 2 {
		t.Fatalf("%d records of two reads: %s", n, buf.text())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := m.Get(s.ID)
		if len(got.Watchers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still shown: %v", got.Watchers)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := m.End(s.ID, alice(), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateEnded)
	before := strings.Count(buf.text(), ActionWatch)
	m.ReadOutput(s.ID, admin)
	if strings.Count(buf.text(), ActionWatch) != before+1 {
		t.Fatalf("reading an ended session was not audited: %s", buf.text())
	}
}
