package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"

	"github.com/ucesys/221b/server/internal/kube"
)

func resources(cpu, mem string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
}

// Size is a terminal's size.
type Size struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Backend is what sessions need from a cluster, as a person. The
// implementation below uses client-go; tests use a fake.
type Backend interface {
	// Pod reads a pod.
	Pod(ctx context.Context, ns, name string) (*corev1.Pod, error)
	// Namespace reads a namespace, for its Pod Security level.
	Namespace(ctx context.Context, name string) (*corev1.Namespace, error)
	// Exec runs a command in a container; with tty, sizes resize it.
	Exec(ctx context.Context, ns, pod, container string, cmd []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, sizes <-chan Size) error
	// Forwarder opens the port-forward connection to a pod, for its ports.
	Forwarder(ctx context.Context, ns, pod string) (Forwarder, error)
	// AddEphemeral adds a debug container to a pod; dryRun only asks.
	AddEphemeral(ctx context.Context, pod *corev1.Pod, ec corev1.EphemeralContainer, dryRun bool) error
	// CreateJob and CreatePod create what a session runs in; dryRun only
	// asks.
	CreateJob(ctx context.Context, job *batchv1.Job, dryRun bool) error
	CreatePod(ctx context.Context, pod *corev1.Pod, dryRun bool) error
	// JobPod finds the pod of a debug pod's Job.
	JobPod(ctx context.Context, ns, session string) (*corev1.Pod, error)
	// Delete removes what a session created.
	DeleteJob(ctx context.Context, ns, name string) error
	DeletePod(ctx context.Context, ns, name string) error
}

// Forwarder carries a session's port-forward connections: one stream pair
// per TCP connection, over one connection to the kubelet.
type Forwarder interface {
	// Dial opens a connection to a port of the pod.
	Dial(port int) (io.ReadWriteCloser, error)
	Close() error
}

// KubeBackend is the Backend of a cluster, as a person: every request is
// made with the person impersonated (TB-51), so Kubernetes decides and
// its refusal is what the person sees.
type KubeBackend struct {
	cfg *rest.Config
	cs  kubernetes.Interface
}

// NewKubeBackend is the backend of a cluster's console connection, acting
// as the person (the console itself for nobody, with sign-in off).
func NewKubeBackend(console *rest.Config, p kube.Person) (*KubeBackend, error) {
	cfg := rest.CopyConfig(console)
	if !p.Nobody() {
		cfg.Impersonate = rest.ImpersonationConfig{UserName: p.User, Groups: p.ImpersonationGroups()}
	}
	// Streams last as long as the session: no client timeout.
	cfg.Timeout = 0
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &KubeBackend{cfg: cfg, cs: cs}, nil
}

func (b *KubeBackend) Pod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	return b.cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
}

func (b *KubeBackend) Namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	return b.cs.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

type sizeQueue struct {
	ctx context.Context
	c   <-chan Size
}

func (q sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s, ok := <-q.c:
		if !ok {
			return nil
		}
		return &remotecommand.TerminalSize{Width: s.Cols, Height: s.Rows}
	case <-q.ctx.Done():
		return nil
	}
}

// Exec opens the exec stream with the WebSocket protocol, falling back to
// SPDY on API servers without it (TB-15).
func (b *KubeBackend) Exec(ctx context.Context, ns, pod, container string, cmd []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, sizes <-chan Size) error {
	req := b.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: cmd, Stdin: stdin != nil, Stdout: true, Stderr: !tty, TTY: tty}, scheme.ParameterCodec)
	ws, err := remotecommand.NewWebSocketExecutor(b.cfg, "GET", req.URL().String())
	if err != nil {
		return err
	}
	sp, err := remotecommand.NewSPDYExecutor(b.cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, sp, func(err error) bool { return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err) })
	if err != nil {
		return err
	}
	opts := remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: tty}
	if !tty {
		opts.Stderr = stderr
	}
	if tty && sizes != nil {
		opts.TerminalSizeQueue = sizeQueue{ctx: ctx, c: sizes}
	}
	return ex.StreamWithContext(ctx, opts)
}

// Forwarder dials the kubelet's port-forward through the API server, with
// SPDY over WebSocket where the API server offers it and plain SPDY
// otherwise.
func (b *KubeBackend) Forwarder(ctx context.Context, ns, pod string) (Forwarder, error) {
	req := b.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(b.cfg)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	ws, err := portforward.NewSPDYOverWebsocketDialer(req.URL(), b.cfg)
	if err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool { return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err) })
	}
	conn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("opening the port forward: %w", err)
	}
	return &kubeForwarder{conn: conn}, nil
}

type kubeForwarder struct {
	conn httpstream.Connection
	mu   sync.Mutex
	next int
}

// Dial opens the error and data streams of one connection, as kubectl
// port-forward does for each connection it accepts.
func (f *kubeForwarder) Dial(port int) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	f.next++
	id := f.next
	f.mu.Unlock()
	h := http.Header{}
	h.Set(corev1.StreamType, corev1.StreamTypeError)
	h.Set(corev1.PortHeader, strconv.Itoa(port))
	h.Set(corev1.PortForwardRequestIDHeader, strconv.Itoa(id))
	errStream, err := f.conn.CreateStream(h)
	if err != nil {
		return nil, fmt.Errorf("creating the error stream: %w", err)
	}
	errStream.Close()
	h.Set(corev1.StreamType, corev1.StreamTypeData)
	data, err := f.conn.CreateStream(h)
	if err != nil {
		return nil, fmt.Errorf("creating the data stream: %w", err)
	}
	s := &forwardStream{data: data, errc: make(chan error, 1)}
	go func() {
		b, err := io.ReadAll(errStream)
		switch {
		case err != nil:
			s.errc <- err
		case len(b) > 0:
			s.errc <- errors.New(string(b))
		default:
			s.errc <- nil
		}
	}()
	return s, nil
}

func (f *kubeForwarder) Close() error { return f.conn.Close() }

type forwardStream struct {
	data httpstream.Stream
	errc chan error
	once sync.Once
}

func (s *forwardStream) Read(p []byte) (int, error) {
	n, err := s.data.Read(p)
	if err == io.EOF {
		select {
		case e := <-s.errc:
			if e != nil {
				return n, e
			}
		default:
		}
	}
	return n, err
}
func (s *forwardStream) Write(p []byte) (int, error) { return s.data.Write(p) }
func (s *forwardStream) Close() error {
	var err error
	s.once.Do(func() { err = s.data.Close() })
	return err
}

func dryRun(on bool) []string {
	if on {
		return []string{metav1.DryRunAll}
	}
	return nil
}

func (b *KubeBackend) AddEphemeral(ctx context.Context, pod *corev1.Pod, ec corev1.EphemeralContainer, dry bool) error {
	cp := pod.DeepCopy()
	cp.Spec.EphemeralContainers = append(cp.Spec.EphemeralContainers, ec)
	_, err := b.cs.CoreV1().Pods(pod.Namespace).UpdateEphemeralContainers(ctx, pod.Name, cp, metav1.UpdateOptions{DryRun: dryRun(dry)})
	return err
}

func (b *KubeBackend) CreateJob(ctx context.Context, job *batchv1.Job, dry bool) error {
	_, err := b.cs.BatchV1().Jobs(job.Namespace).Create(ctx, job, metav1.CreateOptions{DryRun: dryRun(dry)})
	return err
}

func (b *KubeBackend) CreatePod(ctx context.Context, pod *corev1.Pod, dry bool) error {
	_, err := b.cs.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{DryRun: dryRun(dry)})
	return err
}

func (b *KubeBackend) JobPod(ctx context.Context, ns, session string) (*corev1.Pod, error) {
	l, err := b.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: LabelSession + "=" + session})
	if err != nil {
		return nil, err
	}
	if len(l.Items) == 0 {
		return nil, apierrors.NewNotFound(corev1.Resource("pods"), "debug pod of session "+session)
	}
	return &l.Items[0], nil
}

func (b *KubeBackend) DeleteJob(ctx context.Context, ns, name string) error {
	err := b.cs.BatchV1().Jobs(ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground), GracePeriodSeconds: ptr(int64(1))})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (b *KubeBackend) DeletePod(ctx context.Context, ns, name string) error {
	err := b.cs.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(1))})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ---------- waiting for a container ----------

// containerRunning says whether a container of a pod runs, or why it
// cannot.
func containerRunning(p *corev1.Pod, name string) (bool, string) {
	if p.DeletionTimestamp != nil {
		return false, "the pod is being deleted"
	}
	switch p.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return false, "the pod has " + map[corev1.PodPhase]string{corev1.PodSucceeded: "finished", corev1.PodFailed: "failed"}[p.Status.Phase]
	}
	all := append(append([]corev1.ContainerStatus{}, p.Status.ContainerStatuses...), p.Status.InitContainerStatuses...)
	all = append(all, p.Status.EphemeralContainerStatuses...)
	for _, s := range all {
		if s.Name != name {
			continue
		}
		if s.State.Running != nil {
			return true, ""
		}
		if w := s.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				return false, fmt.Sprintf("container %s cannot start: %s %s", name, w.Reason, w.Message)
			}
		}
		if t := s.State.Terminated; t != nil {
			return false, fmt.Sprintf("container %s has ended: %s, exit %d", name, t.Reason, t.ExitCode)
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return false, "the pod cannot be scheduled: " + c.Message
		}
	}
	return false, ""
}

// waitRunning waits until a container runs, a reason says it never will,
// or the time is up.
func waitRunning(ctx context.Context, get func(context.Context) (*corev1.Pod, error), container string, within time.Duration) (*corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	last := ""
	for {
		p, err := get(ctx)
		if err == nil {
			ok, why := containerRunning(p, container)
			if ok {
				return p, nil
			}
			if why != "" && !isTransient(why) {
				return nil, errors.New(why)
			}
			last = why
		} else if !apierrors.IsNotFound(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			if last == "" {
				last = "it did not start"
			}
			return nil, fmt.Errorf("container %s is not running after %s: %s", container, within, last)
		case <-t.C:
		}
	}
}

func isTransient(why string) bool {
	// An image being pulled again after a failure can still succeed, but
	// a session should not wait on it; scheduling may succeed later too.
	return len(why) > 0 && why[:min(len(why), 22)] == "the pod cannot be sche"
}

// statusMessage is Kubernetes' own message for an error, as the person
// should read it (TB-51).
func statusMessage(err error) string {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return st.Status().Message
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "the cluster answered something that is not JSON"
	}
	return err.Error()
}
