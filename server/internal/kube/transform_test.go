package kube

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/nodes"
)

// The cache holds stripped pods, so a row built from them must be the row
// the full pods would give.
func TestStrippedPodsGiveTheSameRow(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	started := metav1.NewTime(now.Add(-time.Hour))
	res := func(cpu, mem string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}}
	}
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}}
	exited := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}
	always := corev1.ContainerRestartPolicyAlways
	pod := func(name string, policy corev1.RestartPolicy, containers []corev1.Container, statuses []corev1.ContainerStatus) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", CreationTimestamp: started, Annotations: map[string]string{"a": "b"}},
			Spec:       corev1.PodSpec{NodeName: "n1", RestartPolicy: policy, Containers: containers},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning, StartTime: &started, ContainerStatuses: statuses,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Second))}},
			},
		}
	}
	// A Job's main container failed under restartPolicy Never while its
	// proxy keeps running: finishing, not crash-looping.
	job := pod("job", corev1.RestartPolicyNever,
		[]corev1.Container{{Name: "main", Image: "job:1", Resources: res("100m", "64Mi")}, {Name: "proxy", Image: "proxy:1", Resources: res("50m", "32Mi")}},
		[]corev1.ContainerStatus{{Name: "main", State: exited}, {Name: "proxy", Ready: true, State: running}})
	crash := pod("crash", corev1.RestartPolicyAlways,
		[]corev1.Container{{Name: "app", Image: "app:1", Env: []corev1.EnvVar{{Name: "TOKEN", Value: "literal"}}, Resources: res("200m", "128Mi")}},
		[]corev1.ContainerStatus{{Name: "app", RestartCount: 4, State: exited}})
	// Being resized in place: 500m asked for, 1 core still allocated.
	resized := pod("resized", corev1.RestartPolicyAlways,
		[]corev1.Container{{Name: "app", Image: "app:1", Resources: res("500m", "256Mi")}},
		[]corev1.ContainerStatus{{Name: "app", Ready: true, State: running, AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi")}}})
	sidecar := pod("sidecar", corev1.RestartPolicyAlways,
		[]corev1.Container{{Name: "app", Image: "app:1", Resources: res("100m", "64Mi")}},
		[]corev1.ContainerStatus{{Name: "app", Ready: true, State: running}})
	sidecar.Spec.InitContainers = []corev1.Container{{Name: "mesh", Image: "mesh:1", RestartPolicy: &always, Resources: res("10m", "16Mi")}}
	sidecar.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m")}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", CreationTimestamp: started},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi"), corev1.ResourcePods: resource.MustParse("110")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: started}},
		},
	}
	pods := []*corev1.Pod{job, crash, resized, sidecar}
	want := nodes.BuildRow(node, pods, nil, nil, now)
	var stripped []*corev1.Pod
	for _, p := range pods {
		s, err := StripPod(p.DeepCopy())
		if err != nil {
			t.Fatal(err)
		}
		stripped = append(stripped, s.(*corev1.Pod))
	}
	got := nodes.BuildRow(node, stripped, nil, nil, now)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("rows differ after stripping:\n full    %+v\n stripped %+v", want, got)
	}
	if got.Pods.Failing != 1 {
		t.Fatalf("only the crash-looping pod is failing: %+v", got.Pods)
	}
	// 100+50 (job) + 200 (crash) + 1000 (resized, allocated) + 100+10+5 (sidecar).
	if got.CPU.Requested != 1465 {
		t.Fatalf("CPU requested %d", got.CPU.Requested)
	}
	for _, p := range stripped {
		if len(p.Annotations) > 0 || p.Spec.Containers[0].Image != "" || p.Spec.Containers[0].Env != nil {
			t.Fatalf("pod %s kept fields the console never shows", p.Name)
		}
	}
}
