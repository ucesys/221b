package sessions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// The real backend against the simulator: exec over the WebSocket, an
// ephemeral container, a debug Job's pod and a node shell's pod.
func simSetup(t *testing.T) (*Manager, Cluster, *kubernetes.Clientset) {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 4, Pods: 30, Seed: 7})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	cs := kubernetes.NewForConfigOrDie(cfg)
	m := New(Options{Config: toolbox.Default(), Audit: audit.New(audit.Options{Out: io.Discard}), StartWithin: 10 * time.Second})
	return m, Cluster{ID: "local", Name: "sim", Config: cfg}, cs
}

func aPod(t *testing.T, cs *kubernetes.Clientset) (string, string) {
	t.Helper()
	pods, err := cs.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pods.Items {
		if p.Status.Phase == "Running" && len(p.Status.ContainerStatuses) > 0 && p.Status.ContainerStatuses[0].State.Running != nil {
			return p.Namespace, p.Name
		}
	}
	t.Fatal("no running pod")
	return "", ""
}

func TestExecAgainstTheSimulator(t *testing.T) {
	m, cl, cs := simSetup(t)
	ns, pod := aPod(t, cs)
	b, err := NewKubeBackend(cl.Config, kube.Person{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := b.Exec(context.Background(), ns, pod, "", []string{"sh", "-c", "hostname"}, false, nil, &out, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != pod {
		t.Fatalf("hostname = %q", out.String())
	}
	err = b.Exec(context.Background(), ns, pod, "", []string{"sh", "-c", "false"}, false, nil, io.Discard, io.Discard, nil)
	var ce interface{ ExitStatus() int }
	if !errors.As(err, &ce) || ce.ExitStatus() != 1 {
		t.Fatalf("false: %v", err)
	}

	s, _ := m.Start(cl, Request{Tool: toolbox.ToolShell, Namespace: ns, Pod: pod}, alice(), "", "")
	waitState(t, m, s.ID, StateLive)
	v, replay, err := m.Attach(s.ID, alice(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(replay), "root@"+pod) {
		read(t, v, "root@"+pod)
	}
	_ = m.Input(s.ID, v, []byte("id\r"))
	read(t, v, "uid=0(root)")
	_ = m.Input(s.ID, v, []byte("exit\r"))
	if got := waitState(t, m, s.ID, StateEnded); got.EndedBy != EndFinished {
		t.Fatalf("ended by %s: %s", got.EndedBy, got.EndNote)
	}
}

func TestDebugToolsAgainstTheSimulator(t *testing.T) {
	m, cl, cs := simSetup(t)
	ns, pod := aPod(t, cs)
	dc, _ := m.Start(cl, Request{Tool: toolbox.ToolDebugContainer, Namespace: ns, Pod: pod, Image: "general", Profile: toolbox.ProfileGeneral}, alice(), "", "")
	got := waitState(t, m, dc.ID, StateLive)
	p, _ := cs.CoreV1().Pods(ns).Get(context.Background(), pod, metav1.GetOptions{})
	if len(p.Spec.EphemeralContainers) != 1 || got.Container != p.Spec.EphemeralContainers[0].Name {
		t.Fatalf("ephemeral %+v, container %s", p.Spec.EphemeralContainers, got.Container)
	}
	m.End(dc.ID, alice(), "")

	dp, _ := m.Start(cl, Request{Tool: toolbox.ToolDebugPod, Namespace: ns, Image: "network", LabelsFrom: pod}, alice(), "", "")
	live := waitState(t, m, dp.ID, StateLive)
	if live.Pod == "" {
		t.Fatal("no pod for the debug Job")
	}
	m.End(dp.ID, alice(), "")
	waitState(t, m, dp.ID, StateEnded)
	jobs, _ := cs.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{LabelSelector: LabelSession + "=" + dp.ID})
	if len(jobs.Items) != 0 {
		t.Fatalf("the Job outlived its session: %v", jobs.Items)
	}

	nodes, _ := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	node := nodes.Items[0].Name
	nc, _ := m.Start(cl, Request{Tool: toolbox.ToolNodeCommand, Node: node, Command: "df -h"}, alice(), "Bob", "")
	if done := waitState(t, m, nc.ID, StateEnded); done.EndedBy != EndFinished || !strings.Contains(string(m.Output(nc.ID)), "/var/lib/containerd") {
		t.Fatalf("%s %s %q", done.EndedBy, done.EndNote, m.Output(nc.ID))
	}
}
