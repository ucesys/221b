package views_test

import (
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ucesys/221b/server/internal/views"
)

func warning(uid, kind, ns, name, reason, msg string, count int32, first, last time.Duration) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name + "." + strings.ToLower(reason), Namespace: ns, UID: types.UID("ev-" + name + reason)},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: name, UID: types.UID(uid)},
		Type:           corev1.EventTypeWarning, Reason: reason, Message: msg, Count: count,
		FirstTimestamp: ts(first), LastTimestamp: ts(last), Source: corev1.EventSource{Component: "kubelet"},
	}
}

func alerts(t *testing.T, src views.Source) map[string]views.AlertRow {
	t.Helper()
	out := map[string]views.AlertRow{}
	for _, k := range views.AlertKeys(src) {
		if a, ok := views.BuildAlert(src, k, now); ok {
			out[k] = a
		}
	}
	return out
}

func TestAlertsForTheThreeFaults(t *testing.T) {
	src := source()
	// An image that cannot be pulled.
	pull := pod("shop", "web-abc-4", "worker-1")
	pull.Status.Conditions[0].Status = corev1.ConditionFalse
	pull.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: `Back-off pulling image "registry.example/nope:1"`}}}}
	src.Put(pull)
	src.Put(warning("uid-web-abc-4", "Pod", "shop", "web-abc-4", "Failed", `Failed to pull image "registry.example/nope:1": not found`, 3, 5*time.Minute, time.Minute))
	// A pod killed for running out of memory, running again.
	oom := pod("shop", "web-abc-5", "worker-1")
	oom.Status.ContainerStatuses[0].RestartCount = 1
	oom.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled", FinishedAt: ts(2 * time.Minute)}}
	src.Put(oom)
	// One that ran out of memory long ago is no alert.
	old := pod("shop", "web-abc-6", "worker-1")
	old.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled", FinishedAt: ts(3 * time.Hour)}}
	src.Put(old)
	src.Put(warning("uid-web-abc-3", "Pod", "shop", "web-abc-3", "FailedScheduling", "0/3 nodes are available: 3 Insufficient cpu.", 58, 59*time.Minute, 40*time.Second))

	got := alerts(t, src)
	cases := []struct{ key, sev, status string }{
		{"pod:shop/web-abc-2", views.SevFailed, "CrashLoopBackOff"},
		{"pod:shop/web-abc-3", views.SevPending, "Pending"},
		{"pod:shop/web-abc-4", views.SevFailed, "ImagePullBackOff"},
		{"pod:shop/web-abc-5", views.SevRecovered, "Running"},
	}
	for _, c := range cases {
		a, ok := got[c.key]
		if !ok || a.Severity != c.sev || a.Status != c.status {
			t.Errorf("%s: %+v (present %v), want %s %s", c.key, a, ok, c.sev, c.status)
		}
	}
	for _, k := range []string{"pod:shop/web-abc-1", "pod:shop/web-abc-6", "pod:shop/loader-xyz", "node:worker-1"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s has an alert: %+v", k, got[k])
		}
	}
	if a := got["pod:shop/web-abc-4"]; a.Waiting != "ImagePullBackOff" || !strings.Contains(a.WaitingMessage, "nope:1") || len(a.Events) != 1 || a.Events[0].Count != 3 {
		t.Errorf("image pull: %+v", a)
	}
	if a := got["pod:shop/web-abc-5"]; !a.OOM || a.LastReason != "OOMKilled" || a.LastExit == nil || *a.LastExit != 137 {
		t.Errorf("oom: %+v", a)
	}
	if a := got["pod:shop/web-abc-3"]; a.Node != "" || a.Why != "0/3 nodes are available: 3 Insufficient cpu." || a.Events[0].Reason != "FailedScheduling" {
		t.Errorf("pending: %+v", a)
	}
	if a := got["pod:shop/web-abc-2"]; a.LastReason != "Error" || *a.LastExit != 1 || a.Restarts != 6 {
		t.Errorf("crash: %+v", a)
	}
}

func TestNodeAndVolumeAlerts(t *testing.T) {
	src := source()
	src.Put(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-2", UID: "uid-worker-2", Labels: map[string]string{"node-role.kubernetes.io/worker": "", "workload": "general", "topology.kubernetes.io/zone": "rack-b"}},
		Spec:       corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: ts(48 * time.Hour)},
			{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasDiskPressure", Message: "kubelet has disk pressure", LastTransitionTime: ts(20 * time.Minute)},
		}, Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.20.2.33"}}},
	})
	// The kubelet names the node by its name in the event's UID.
	src.Put(warning("worker-2", "Node", "", "worker-2", "ImageGCFailed", "failed to garbage collect required amount of images", 11, 50*time.Minute, 4*time.Minute))
	// A claim whose pod cannot mount it.
	sc := "longhorn"
	src.Put(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "shop", UID: "uid-pvc-grafana"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-1234", StorageClassName: &sc, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
	})
	user := pod("shop", "grafana-0", "worker-1")
	user.Spec.Volumes = []corev1.Volume{{Name: "storage", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "grafana"}}}}
	user.Status.Phase = corev1.PodPending
	user.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}
	src.Put(user)
	src.Put(warning("uid-grafana-0", "Pod", "shop", "grafana-0", "FailedAttachVolume", `Multi-Attach error for volume "pvc-1234" Volume is already exclusively attached to one node`, 1, 14*time.Minute, 14*time.Minute))
	// Green objects with only past warnings are resolved (6.5): a ready
	// node, and a bound claim whose pod runs now.
	src.Put(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-3", UID: "uid-worker-3", Labels: map[string]string{"node-role.kubernetes.io/worker": ""}},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: ts(48 * time.Hour)}}},
	})
	src.Put(warning("worker-3", "Node", "", "worker-3", "ImageGCFailed", "failed to garbage collect required amount of images", 2, 50*time.Minute, 30*time.Minute))
	src.Put(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "shop", UID: "uid-pvc-data"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-5678", StorageClassName: &sc},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	})
	db := pod("shop", "db-0", "worker-1")
	db.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
	src.Put(db)
	src.Put(warning("uid-db-0", "Pod", "shop", "db-0", "FailedMount", `MountVolume.SetUp failed for volume "pvc-5678": timed out`, 1, 40*time.Minute, 40*time.Minute))

	got := alerts(t, src)
	for _, k := range []string{"node:worker-3", "pvc:shop/data"} {
		if a, ok := got[k]; !ok || a.Severity != views.SevRecovered || a.StatusTone != "ok" {
			t.Errorf("%s is not resolved: %+v", k, a)
		}
	}
	n, ok := got["node:worker-2"]
	if !ok || n.Severity != views.SevWarning || n.Role != "worker" || n.Pool != "general" || n.IP != "10.20.2.33" || n.Status != "Cordoned" {
		t.Fatalf("node: %+v", n)
	}
	var reasons []string
	for _, e := range n.Events {
		reasons = append(reasons, e.Source+":"+e.Reason)
	}
	sort.Strings(reasons)
	if strings.Join(reasons, ",") != "condition:KubeletHasDiskPressure,event:ImageGCFailed,state:Unschedulable" {
		t.Errorf("node entries: %v", reasons)
	}
	// The pod is pending, and its claim shows what was reported for it.
	if p := got["pod:shop/grafana-0"]; p.Severity != views.SevPending || p.Status != "ContainerCreating" {
		t.Errorf("pod: %+v", p)
	}
	c, ok := got["pvc:shop/grafana"]
	if !ok || c.Severity != views.SevWarning || len(c.Events) != 1 || c.Events[0].Pod != "shop/grafana-0" || c.Volume != "pvc-1234" || len(c.Users) != 1 || c.Users[0].Status != "ContainerCreating" {
		t.Errorf("claim: %+v", c)
	}
}
