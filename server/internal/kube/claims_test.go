package kube

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A generic ephemeral volume's claim is named <pod>-<volume> (kube#11):
// the pod's changes mark its alert and storage rows, and its node's
// kubelet is read for its usage.
func TestAPodsClaimsTakeItsEphemeralVolumes(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-7f9-x2k4", Namespace: "shop"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{
		{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-0"}}},
		{Name: "scratch", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}}}
	if got := podClaims(p); !slices.Equal(got, []string{"data-0", "web-7f9-x2k4-scratch"}) {
		t.Fatalf("claims %v", got)
	}
}
