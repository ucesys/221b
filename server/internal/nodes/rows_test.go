package nodes

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var now = time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)

func node(name string, mods ...func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour)), Labels: map[string]string{
			corev1.LabelTopologyZone: "zone-a",
		}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3800m"),
				corev1.ResourceMemory: resource.MustParse("15Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "KubeletReady", LastTransitionTime: metav1.NewTime(now.Add(-71 * time.Hour))},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
			},
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeHostName, Address: name}, {Type: corev1.NodeInternalIP, Address: "10.0.0.7"}},
			NodeInfo:  corev1.NodeSystemInfo{KubeletVersion: "v1.37.1", OSImage: "Ubuntu 24.04.3 LTS", KernelVersion: "6.8.0", ContainerRuntimeVersion: "containerd://2.1.4", Architecture: "amd64"},
		},
	}
	for _, m := range mods {
		m(n)
	}
	return n
}

func setCond(t corev1.NodeConditionType, st corev1.ConditionStatus, since time.Time) func(*corev1.Node) {
	return func(n *corev1.Node) {
		for i := range n.Status.Conditions {
			if n.Status.Conditions[i].Type == t {
				n.Status.Conditions[i].Status = st
				n.Status.Conditions[i].LastTransitionTime = metav1.NewTime(since)
				n.Status.Conditions[i].Message = "message for " + string(t)
				return
			}
		}
		n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: t, Status: st, LastTransitionTime: metav1.NewTime(since)})
	}
}

func cordoned(n *corev1.Node) { n.Spec.Unschedulable = true }

func pod(name, cpu, mem string, mods ...func(*corev1.Pod)) *corev1.Pod {
	started := metav1.NewTime(now.Add(-time.Hour))
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: started},
		Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "main", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)},
		}}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			StartTime:         &started,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: started}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	for _, m := range mods {
		m(p)
	}
	return p
}

func phase(ph corev1.PodPhase) func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.Status.Phase = ph }
}

func notReadySince(at time.Time) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(at)}}
		p.Status.ContainerStatuses[0].Ready = false
	}
}

func waiting(reason string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].Ready = false
		p.Status.ContainerStatuses[0].RestartCount = 7
		p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "back-off"}}
		p.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Second))}}
	}
}

func titles(r Row) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Title+"/"+p.Severity)
	}
	return out
}

func TestStatus(t *testing.T) {
	cases := []struct {
		name       string
		node       *corev1.Node
		status     string
		tone       string
		tipContain string
	}{
		{"ready", node("n1"), "Ready", "ok", "Ready since"},
		{"cordoned", node("n1", cordoned), "Cordoned", "warn", "SchedulingDisabled"},
		{"not ready", node("n1", setCond(corev1.NodeReady, corev1.ConditionFalse, now.Add(-5*time.Minute))), "Not ready", "err", "Not ready since"},
		{"not ready and cordoned", node("n1", cordoned, setCond(corev1.NodeReady, corev1.ConditionFalse, now)), "Not ready", "err", "also cordoned"},
		{"unknown", node("n1", setCond(corev1.NodeReady, corev1.ConditionUnknown, now.Add(-47*time.Minute))), "Unknown", "err", "47m ago"},
		{"no ready condition", node("n1", func(n *corev1.Node) { n.Status.Conditions = nil }), "Unknown", "err", "no Ready condition"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := BuildRow(c.node, nil, nil, now)
			if r.Status != c.status || r.Tone != c.tone {
				t.Fatalf("status = %q/%q, want %q/%q", r.Status, r.Tone, c.status, c.tone)
			}
			if !strings.Contains(r.StatusTip, c.tipContain) {
				t.Fatalf("tip %q does not contain %q", r.StatusTip, c.tipContain)
			}
		})
	}
}

func TestRolesAndFacts(t *testing.T) {
	r := BuildRow(node("cp-1", func(n *corev1.Node) {
		n.Labels["node-role.kubernetes.io/control-plane"] = ""
		n.Labels["node-role.kubernetes.io/etcd"] = ""
		n.Spec.Taints = []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}}
	}), nil, nil, now)
	if r.Role != "control-plane" || strings.Join(r.Roles, ",") != "control-plane,etcd" {
		t.Fatalf("role %q roles %v", r.Role, r.Roles)
	}
	if len(r.Taints) != 1 || r.Taints[0].Effect != "NoSchedule" {
		t.Fatalf("taints %+v", r.Taints)
	}
	if r.Zone != "zone-a" || r.InternalIP != "10.0.0.7" || r.Kubelet != "v1.37.1" || r.Runtime != "containerd://2.1.4" {
		t.Fatalf("facts %+v", r)
	}
	if r.CPU.Allocatable != 3800 || r.CPU.Capacity != 4000 || r.Memory.Allocatable != 15<<30 || r.Pods.Capacity != 110 {
		t.Fatalf("resources cpu %+v mem %+v pods %+v", r.CPU, r.Memory, r.Pods)
	}
	if w := BuildRow(node("w"), nil, nil, now); w.Role != "worker" || len(w.Roles) != 0 {
		t.Fatalf("worker role %q %v", w.Role, w.Roles)
	}
	legacy := BuildRow(node("m", func(n *corev1.Node) { n.Labels["kubernetes.io/role"] = "master" }), nil, nil, now)
	if legacy.Role != "control-plane" {
		t.Fatalf("legacy master label gave role %q", legacy.Role)
	}
}

func TestRequestsLimitsAndCounts(t *testing.T) {
	withInit := pod("init", "100m", "64Mi", func(p *corev1.Pod) {
		// A big init container: the pod requests the larger of it and the
		// sum of its regular containers, as the scheduler counts it.
		p.Spec.InitContainers = []corev1.Container{{Name: "setup", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("32Mi")}}}}
	})
	always := corev1.ContainerRestartPolicyAlways
	withSidecar := pod("sidecar", "250m", "128Mi", func(p *corev1.Pod) {
		p.Spec.InitContainers = []corev1.Container{{Name: "proxy", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi")}}}}
		p.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")}
		p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi")}
	})
	done := pod("job", "4", "8Gi", phase(corev1.PodSucceeded))
	failed := pod("evicted", "4", "8Gi", phase(corev1.PodFailed))
	used := Usage{CPUMilli: 1234, MemoryBytes: 5 << 30}
	r := BuildRow(node("n1"), []*corev1.Pod{withInit, withSidecar, done, failed}, &used, now)
	// init pod: max(2000m, 100m) = 2000m; sidecar pod: 250 + 50 + 10 overhead = 310m.
	if r.CPU.Requested != 2310 {
		t.Fatalf("cpu requested %d, want 2310", r.CPU.Requested)
	}
	if want := int64(64<<20 + 128<<20 + 32<<20); r.Memory.Requested != want {
		t.Fatalf("memory requested %d, want %d", r.Memory.Requested, want)
	}
	if r.CPU.Limits != 1010 {
		t.Fatalf("cpu limits %d, want 1010 (limit plus overhead)", r.CPU.Limits)
	}
	if r.Pods.Running != 2 || r.Pods.Completed != 2 {
		t.Fatalf("pods %+v", r.Pods)
	}
	if r.CPU.Used == nil || *r.CPU.Used != 1234 || *r.Memory.Used != 5<<30 {
		t.Fatalf("usage not copied: %+v %+v", r.CPU, r.Memory)
	}
	if r2 := BuildRow(node("n1"), nil, nil, now); r2.CPU.Used != nil || r2.Memory.Used != nil {
		t.Fatal("usage should be nil without metrics")
	}
}

func TestPodProblems(t *testing.T) {
	healthy := pod("ok", "100m", "64Mi")
	crash := pod("crash", "100m", "64Mi", waiting("CrashLoopBackOff"))
	slowStart := pod("slow", "100m", "64Mi", phase(corev1.PodPending), notReadySince(now.Add(-30*time.Second)))
	stuck := pod("stuck", "100m", "64Mi", phase(corev1.PodPending), notReadySince(now.Add(-10*time.Minute)), func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}
	})
	unready := pod("unready", "100m", "64Mi", notReadySince(now.Add(-5*time.Minute)))
	deadline := metav1.NewTime(now.Add(-10 * time.Minute))
	terminating := pod("terminating", "100m", "64Mi", func(p *corev1.Pod) { p.DeletionTimestamp = &deadline })

	r := BuildRow(node("n1"), []*corev1.Pod{healthy, crash, slowStart, stuck, unready, terminating}, nil, now)
	if r.Pods.Failing != 1 || r.Pods.Starting != 1 || r.Pods.Unready != 2 || r.Pods.NotReady != 4 {
		t.Fatalf("counts %+v", r.Pods)
	}
	if len(r.Problems) != 1 {
		t.Fatalf("problems %v", titles(r))
	}
	p := r.Problems[0]
	if p.Title != "4 pods not ready" || p.Severity != SeverityError {
		t.Fatalf("problem %q %q", p.Title, p.Severity)
	}
	if p.Summary != "1 failing, 1 still starting, 2 failing readiness checks." {
		t.Fatalf("summary %q", p.Summary)
	}
	if p.Entries[0].Reason != "CrashLoopBackOff" || p.Entries[0].Object != "pod default/crash" {
		t.Fatalf("first entry should be the failing pod: %+v", p.Entries[0])
	}
	if !strings.Contains(p.Entries[0].Message, "restarted 7 times") || !strings.Contains(p.Entries[0].Message, "137 (OOMKilled)") {
		t.Fatalf("crash message %q", p.Entries[0].Message)
	}
	reasons := map[string]bool{}
	for _, e := range p.Entries {
		reasons[e.Reason] = true
	}
	for _, want := range []string{"ContainerCreating", "NotReady", "Terminating"} {
		if !reasons[want] {
			t.Fatalf("missing entry %s in %+v", want, p.Entries)
		}
	}
	if r.Errors != 1 || r.Warnings != 0 {
		t.Fatalf("errors %d warnings %d", r.Errors, r.Warnings)
	}

	// Without a failing pod the problem is a warning.
	r = BuildRow(node("n1"), []*corev1.Pod{unready}, nil, now)
	if r.Problems[0].Severity != SeverityWarning || r.Problems[0].Title != "1 pod not ready" {
		t.Fatalf("got %v", titles(r))
	}
}

// Between back-offs a crash-looping container is terminated, not waiting;
// it is failing all the same.
func TestCrashingContainerBetweenBackoffs(t *testing.T) {
	exited := func(p *corev1.Pod) {
		cs := &p.Status.ContainerStatuses[0]
		cs.Ready, cs.RestartCount = false, 3
		cs.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Second))}}
	}
	r := BuildRow(node("n1"), []*corev1.Pod{pod("crash", "10m", "16Mi", exited)}, nil, now)
	if r.Errors != 1 || r.Pods.Failing != 1 {
		t.Fatalf("errors %d, pods %+v", r.Errors, r.Pods)
	}
	e := r.Problems[0].Entries[0]
	if e.Reason != "CrashLoopBackOff" || !strings.Contains(e.Message, "exited with code 1 (Error)") {
		t.Fatalf("entry %+v", e)
	}

	// Under restartPolicy Never nothing restarts it: the pod is finishing, not crash-looping.
	never := pod("job", "10m", "16Mi", exited, func(p *corev1.Pod) { p.Spec.RestartPolicy = corev1.RestartPolicyNever })
	if r := BuildRow(node("n1"), []*corev1.Pod{never}, nil, now); r.Pods.Failing != 0 {
		t.Fatalf("pods %+v", r.Pods)
	}
}

func TestPodEntriesAreCapped(t *testing.T) {
	var pods []*corev1.Pod
	for i := 0; i < 14; i++ {
		pods = append(pods, pod("p"+string(rune('a'+i)), "10m", "1Mi", waiting("ImagePullBackOff")))
	}
	r := BuildRow(node("n1"), pods, nil, now)
	if got := len(r.Problems[0].Entries); got != maxPodEntries {
		t.Fatalf("entries %d, want %d", got, maxPodEntries)
	}
	if !strings.Contains(r.Problems[0].Summary, "first 10 are listed") {
		t.Fatalf("summary %q", r.Problems[0].Summary)
	}
}

func TestNodeProblems(t *testing.T) {
	r := BuildRow(node("n1",
		setCond(corev1.NodeReady, corev1.ConditionUnknown, now.Add(-3*time.Hour)),
		setCond(corev1.NodeDiskPressure, corev1.ConditionTrue, now.Add(-time.Minute)),
		setCond(corev1.NodeNetworkUnavailable, corev1.ConditionTrue, now.Add(-time.Minute)),
		cordoned,
	), []*corev1.Pod{pod("a", "1", "1Gi"), pod("b", "1", "1Gi")}, nil, now)
	got := strings.Join(titles(r), ", ")
	want := "Kubelet stopped reporting/error, Disk pressure/warning, Network unavailable/error, Cordoned/warning"
	if got != want {
		t.Fatalf("problems\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(r.Problems[0].Summary, "for 3h") {
		t.Fatalf("summary %q", r.Problems[0].Summary)
	}
	if !strings.Contains(r.Problems[3].Summary, "2 pods are still running") {
		t.Fatalf("cordon summary %q", r.Problems[3].Summary)
	}
	if r.Errors != 2 || r.Warnings != 2 {
		t.Fatalf("errors %d warnings %d", r.Errors, r.Warnings)
	}
}

func TestEmptySlicesEncodeAsArrays(t *testing.T) {
	r := BuildRow(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "bare"}}, nil, nil, now)
	if r.Problems == nil || r.Taints == nil || r.Conditions == nil || r.Labels == nil || r.Roles == nil {
		t.Fatalf("nil collections would encode as null: %+v", r)
	}
}

func TestDur(t *testing.T) {
	cases := map[time.Duration]string{
		0:                             "1s",
		45 * time.Second:              "45s",
		12 * time.Minute:              "12m",
		3*time.Hour + 5*time.Minute:   "3h 5m",
		2 * time.Hour:                 "2h",
		50*time.Hour + 10*time.Minute: "2d 2h",
		30 * 24 * time.Hour:           "30d",
	}
	for d, want := range cases {
		if got := dur(d); got != want {
			t.Errorf("dur(%v) = %q, want %q", d, got, want)
		}
	}
}
