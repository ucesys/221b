package watson

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// helmRecord writes a release record as Helm's secrets driver keeps it:
// the release's JSON, base64, in data.release.
func helmRecord(t *testing.T, cl Cluster, ns, name string, version int, releaseJSON string) {
	t.Helper()
	k := simClient(t, cl)
	v := strconv.Itoa(version)
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1." + name + ".v" + v, Namespace: ns, Labels: map[string]string{"owner": "helm", "name": name, "version": v, "status": "deployed"}},
		Type: "helm.sh/release.v1", Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString([]byte(releaseJSON)))}}
	if _, err := k.CoreV1().Secrets(ns).Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// simClient is a client of the simulator, which speaks JSON only.
func simClient(t *testing.T, cl Cluster) kubernetes.Interface {
	t.Helper()
	cfg := rest.CopyConfig(cl.Config)
	cfg.ContentType = "application/json"
	k, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A release record without its info or chart, which anyone who may create
// Secrets in the namespace can write, gives a card that cannot be approved,
// not a crash of the console (review of November 24, 2026, finding 4).
func TestADamagedReleaseRecordGivesACardThatCannotBeApproved(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	helmRecord(t, cl, "web", "x", 1, `{"name":"x","version":1}`)
	for _, line := range []string{"helm rollback x -n web", "helm uninstall x -n web", "helm upgrade x -n web --git https://git.example/x --ref main"} {
		w := alice()
		w.ManageApps = true
		got := card(t, svc, cl, w, line)
		if got.Status != StatusPending || got.Plan == nil || !got.Plan.Ready {
			t.Fatalf("%s: %s %+v", line, got.Status, got.Plan)
		}
		if !strings.Contains(strings.Join(got.Plan.Impact, " "), "has no info") {
			t.Errorf("%s: impact %v", line, got.Plan.Impact)
		}
		if _, err := svc.Approve(context.Background(), cl, w, got.ID); err == nil {
			t.Errorf("%s: approved", line)
		}
	}
}
