package watson

import (
	"bytes"
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
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

// An upgrade card after a failed upgrade compares against the last
// deployed revision, which Helm upgrades from, not the failed one (review
// of November 24, 2026, finding 14).
func TestAnUpgradeCardComparesAgainstTheLastDeployedRevision(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{})
	repo := gitChart(t)
	helmRecord(t, cl, "web", "shop3", 3, `{"name":"shop3","namespace":"web","version":3,"info":{"status":"deployed"},"chart":{"metadata":{"name":"web","version":"1.0.0"}},"config":{"replicas":3,"cache":{"enabled":true}},"manifest":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: shop-cache\n"}`)
	helmRecord(t, cl, "web", "shop3", 4, `{"name":"shop3","namespace":"web","version":4,"info":{"status":"failed"},"chart":{"metadata":{"name":"web","version":"1.0.0"}},"config":{"replicas":3},"manifest":""}`)
	got := card(t, svc, cl, manager(alice()), "helm upgrade shop3 --git "+repo+" --path charts/web --ref master -n web --set replicas=3")
	text := strings.Join(got.Plan.Impact, "\n")
	if !strings.Contains(text, "ConfigMap shop-cache") || !strings.Contains(text, "Removes 1 value the release has now (helm get values): cache.") || !strings.Contains(text, "revision 3, deployed") || !strings.Contains(text, "revision 5") {
		t.Fatalf("impact:\n%s", text)
	}
	if got.Plan.pin == nil || got.Plan.pin.removed == 0 {
		t.Fatalf("pin %+v", got.Plan.pin)
	}
}

// An upgrade given no values keeps the release's, as Helm does: its card
// removes none, and on the console's own release it is not refused for it
// (review of November 24, 2026, cut by the cap: apps.go:716).
func TestAnUpgradeWithNoValuesKeepsTheReleasesValues(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{Self: apps.SelfRelease{Cluster: "local", Namespace: "web", Name: "shop4"}})
	repo := gitChart(t)
	helmRecord(t, cl, "web", "shop4", 1, `{"name":"shop4","namespace":"web","version":1,"info":{"status":"deployed"},"chart":{"metadata":{"name":"web","version":"1.0.0"}},"config":{"replicas":3,"extra":"kept"},"manifest":""}`)
	got := card(t, svc, cl, manager(alice()), "helm upgrade shop4 --git "+repo+" --path charts/web --ref master -n web")
	text := strings.Join(got.Plan.Impact, "\n")
	if strings.Contains(text, "Removes") || !strings.Contains(text, "Helm keeps the values the release has") || got.Plan.refusal != "" {
		t.Fatalf("impact:\n%s\nrefusal %q", text, got.Plan.refusal)
	}
}

// helm status masks the notes with the chart's own values too, whose
// defaults can hold a credential (review of November 24, 2026, cut by the
// cap: helm.go:166).
func TestHelmStatusMasksNotesWithTheChartsDefaults(t *testing.T) {
	_, cl, _, _, _ := setup(t, Options{})
	helmRecord(t, cl, "web", "notes", 1, `{"name":"notes","namespace":"web","version":1,"info":{"status":"deployed","notes":"Sign in with admin / chart-default-pw7."},"chart":{"metadata":{"name":"web","version":"1.0.0"},"values":{"adminPassword":"chart-default-pw7"}},"config":{},"manifest":""}`)
	var d Discovery
	disc, err := d.For(cl.ID, cl.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"helm status notes -n web", "helm status notes -n web -o json"} {
		var out bytes.Buffer
		code := runHelm(context.Background(), mustParse(t, line), Target{Config: cl.Config, Discovery: disc}, &out, &out)
		if code != 0 || strings.Contains(out.String(), "chart-default-pw7") || !strings.Contains(out.String(), "Sign in with admin") {
			t.Errorf("%s: %d %s", line, code, out.String())
		}
	}
}

// helm upgrade --install after an uninstall --keep-history installs, as
// the helm CLI does, and its card says so (review of November 24, 2026,
// cut by the cap: apps.go:517).
func TestUpgradeInstallAfterAnUninstallThatKeptItsHistoryInstalls(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{})
	repo := gitChart(t)
	helmRecord(t, cl, "web", "kept", 1, `{"name":"kept","namespace":"web","version":1,"info":{"status":"uninstalled"},"chart":{"metadata":{"name":"web","version":"1.0.0"}},"config":{},"manifest":""}`)
	got := card(t, svc, cl, manager(alice()), "helm upgrade kept --install --git "+repo+" --path charts/web --ref master -n web")
	if text := strings.Join(got.Plan.Impact, "\n"); !strings.Contains(text, "uninstalled with its history kept") {
		t.Fatalf("impact:\n%s", text)
	}
	if _, err := svc.Approve(context.Background(), cl, manager(alice()), got.ID); err != nil {
		t.Fatal(err)
	}
	if done := wait(t, svc, got.ID, func(i Item) bool { return finished(i.Status) }); done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, outputOf(svc, got.ID))
	}
}

// A rollback to a negative revision is said to be refused, as Helm
// refuses it, not described as a rollback to the previous one (review of
// November 24, 2026, sweep).
func TestARollbackToANegativeRevisionIsRefusedOnItsCard(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	helmRecord(t, cl, "web", "rb", 1, `{"name":"rb","namespace":"web","version":1,"info":{"status":"superseded"},"chart":{"metadata":{"name":"web","version":"1.0.0"}},"manifest":""}`)
	helmRecord(t, cl, "web", "rb", 2, `{"name":"rb","namespace":"web","version":2,"info":{"status":"deployed"},"chart":{"metadata":{"name":"web","version":"1.0.0"}},"manifest":""}`)
	got := card(t, svc, cl, alice(), "helm rollback rb -1 -n web")
	if text := strings.Join(got.Plan.Impact, " "); !strings.Contains(text, "helm will refuse") || strings.Contains(text, "Rolls release") {
		t.Fatalf("impact %v", got.Plan.Impact)
	}
}
