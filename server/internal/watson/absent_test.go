package watson

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// An object a card found absent voids the card when it appears before the
// approval: the change would act on something the card never showed
// (review of November 24, 2026, cut by the cap: impact.go:257).
func TestAnObjectMadeAfterItsCardVoidsTheCard(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	got := card(t, svc, cl, alice(), "kubectl delete pod later-0 -n web")
	if !strings.Contains(strings.Join(got.Plan.Impact, " "), "There is no pod web/later-0") {
		t.Fatalf("impact %v", got.Plan.Impact)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "later-0", Namespace: "web"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}}}
	if _, err := simClient(t, cl).CoreV1().Pods("web").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), got.ID); err == nil || !strings.Contains(err.Error(), "changed after this card appeared") {
		t.Fatalf("approved: %v", err)
	}
}
