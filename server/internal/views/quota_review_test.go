package views_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/views"
)

// Pod-level requests decide what quota admission counts (on by default
// since Kubernetes 1.34): the cached template keeps them, and a quota of
// pods that are not BestEffort takes such a pod (kube#8).
func TestARolloutCountsPodLevelRequests(t *testing.T) {
	src := fake.New("worker-1")
	src.Put(&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "q"},
		Spec:   corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"requests.cpu": resource.MustParse("9")}, Scopes: []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeNotBestEffort}},
		Status: corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{"requests.cpu": resource.MustParse("9")}, Used: corev1.ResourceList{"requests.cpu": resource.MustParse("8")}}})
	four := int32(4)
	surge, none := intstr.FromInt32(1), intstr.FromInt32(0)
	src.Put(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}, Spec: appsv1.DeploymentSpec{Replicas: &four,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &surge, MaxUnavailable: &none}},
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
			Containers: []corev1.Container{{Name: "c", Image: "c:1", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}}}},
		}}}})
	r, ok := views.BuildPolicy(src, "rollout:shop/api", now)
	if !ok {
		t.Fatal("no rollout row")
	}
	ro := r.(views.RolloutRow)
	if !ro.Stalls || len(ro.Short) != 1 || ro.Short[0].Need != "2" {
		t.Fatalf("rollout %+v", ro)
	}
	// Only pod-level requests: the pod is not BestEffort either.
	src.Put(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "lean"}, Spec: appsv1.DeploymentSpec{Replicas: &four,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &surge, MaxUnavailable: &none}},
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
			Containers: []corev1.Container{{Name: "c", Image: "c:1"}},
		}}}})
	r, _ = views.BuildPolicy(src, "rollout:shop/lean", now)
	if ro := r.(views.RolloutRow); !ro.Stalls {
		t.Fatalf("a pod with pod-level requests only was taken as BestEffort: %+v", ro)
	}
}
