package views_test

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/views"
)

// A generic ephemeral volume's claim, <pod>-<volume>, is the pod's as a
// PersistentVolumeClaim volume's is (kube#11): its storage row shows the
// pod and its node, and its alert the pod among its users.
func TestAnEphemeralVolumesClaimShowsItsPod(t *testing.T) {
	src := fake.New("worker-1")
	p := pod("shop", "web-7f9-x2k4", "worker-1")
	p.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{}}}}}
	p.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "scratch", MountPath: "/scratch"}}
	src.Put(p)
	src.Put(pvc("shop", "web-7f9-x2k4-scratch", ptr("longhorn"), "10Gi", corev1.ClaimBound, "pvc-scratch"))
	if got := views.ClaimsOf(p); !slices.Equal(got, []string{"web-7f9-x2k4-scratch"}) {
		t.Fatalf("claims %v", got)
	}
	row, ok := views.BuildStorage(src, now)["pvc:shop/web-7f9-x2k4-scratch"]
	if !ok || row.Pair == nil {
		t.Fatal("no storage row for the claim")
	}
	if len(row.Pair.Pods) != 1 || row.Pair.Pods[0].Name != "web-7f9-x2k4" || row.Pair.Pods[0].MountPath != "/scratch" || !slices.Equal(row.Pair.Nodes, []string{"worker-1"}) {
		t.Fatalf("storage pods %+v nodes %v", row.Pair.Pods, row.Pair.Nodes)
	}
}
