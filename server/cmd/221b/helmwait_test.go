package main

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube/kubetest"
)

// The ConfigMaps of before wait while the newest revision of the console's
// Helm release is pending, as helm upgrade --wait leaves it until every
// object it rendered is there (the 0.13.0 release check: Helm 4 failed
// the upgrade from 0.12.1 when the console deleted them during its wait).
func TestTheConfigMapsOfBeforeWaitForHelm(t *testing.T) {
	ctx := context.Background()
	cs := kubetest.Cluster()
	rev := func(n int, status string) {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.console.v" + strconv.Itoa(n), Namespace: "ns",
			Labels: map[string]string{"owner": "helm", "name": "console", "version": strconv.Itoa(n), "status": status}}, Type: "helm.sh/release.v1"}
		if _, err := cs.CoreV1().Secrets("ns").Create(ctx, s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := helmPending(ctx, cs, "ns", "console"); err != nil || p {
		t.Fatalf("no release: pending %v (%v)", p, err)
	}
	rev(9, "superseded")
	rev(10, "pending-upgrade")
	if p, _ := helmPending(ctx, cs, "ns", "console"); !p {
		t.Fatal("the newest revision is pending-upgrade, and the deletion does not wait")
	}
	if p, _ := helmPending(ctx, cs, "ns", "other"); p {
		t.Fatal("another release's revisions hold this one")
	}
	done := make(chan struct{})
	go func() {
		waitForHelm(ctx, cs, "ns/console", slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the deletion went ahead during the upgrade")
	case <-time.After(300 * time.Millisecond):
	}
	s, _ := cs.CoreV1().Secrets("ns").Get(ctx, "sh.helm.release.v1.console.v10", metav1.GetOptions{})
	s.Labels["status"] = "deployed"
	if _, err := cs.CoreV1().Secrets("ns").Update(ctx, s, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(helmPendingEvery + 5*time.Second):
		t.Fatal("the deletion still waits once the release is deployed")
	}
}
