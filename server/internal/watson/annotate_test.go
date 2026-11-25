package watson

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// kubectl annotate never shows a Secret's annotations, which can hold its
// data: --list is refused on Secrets, and setting one needs --overwrite,
// without which kubectl quotes the value it has (review of November 24,
// 2026, finding 5).
func TestAnnotateNeverListsOrQuotesASecretsAnnotations(t *testing.T) {
	for _, line := range []string{
		"kubectl annotate secret db -n web --list",
		"kubectl annotate secrets -n web -l app=db --list note=x",
		"kubectl annotate secret db -n web kubectl.kubernetes.io/last-applied-configuration=x",
		"kubectl annotate secret,configmap -l app=db -n web note=x",
	} {
		_, err := Parse(line)
		r, ok := err.(*Refusal)
		if !ok || r.Rule != "secret values" {
			t.Errorf("%s: %v", line, err)
		}
	}
	for _, line := range []string{
		"kubectl annotate secret db -n web note=x --overwrite",
		"kubectl annotate secret db -n web kubectl.kubernetes.io/last-applied-configuration-",
		"kubectl annotate configmap web -n web --list",
		"kubectl annotate deploy api -n web note=x",
		"kubectl label secret db -n web --list",
	} {
		if _, err := Parse(line); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
}

// plantedSecret makes Secret web/db, labelled app=db, as kubectl apply
// leaves it: its last-applied annotation holds its data.
func plantedSecret(t *testing.T, cl Cluster) {
	t.Helper()
	applied := `{"apiVersion":"v1","data":{"password":"aHVudGVyMi1hcHBsaWVk"},"kind":"Secret"}`
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "web", Labels: map[string]string{"app": "db"},
		Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": applied, "copy": "x hunter2-applied x", "note": "plain"}},
		Data: map[string][]byte{"password": []byte("hunter2-applied")}}
	if _, err := simClient(t, cl).CoreV1().Secrets("web").Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// An annotate card that picks Secrets with -l shows none of their values:
// the annotations that hold one are hidden as by name (finding 6).
func TestAnAnnotateCardOfSecretsBySelectorShowsNoValue(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	plantedSecret(t, cl)
	for _, line := range []string{
		"kubectl annotate secret -n web -l app=db kubectl.kubernetes.io/last-applied-configuration-",
		"kubectl annotate secret -n web -l app=db copy=y --overwrite",
		"kubectl annotate secret db -n web kubectl.kubernetes.io/last-applied-configuration-",
	} {
		got := card(t, svc, cl, alice(), line)
		all := strings.Join(got.Plan.Impact, "\n")
		for _, l := range got.Plan.DryRun {
			all += "\n" + l.T
		}
		if strings.Contains(all, "hunter2") || strings.Contains(all, "aHVudGVy") || !strings.Contains(all, "hidden by 221B") {
			t.Errorf("%s: %s", line, all)
		}
	}
}

// Annotating a Secret by name that carries the last-applied annotation can
// be approved: its guard compares the Secret as the card read it, values
// hidden, not the hidden card against the live Secret (review of November
// 24, 2026, cut by the cap: impact.go:589).
func TestAnAnnotateCardOfASecretWithLastAppliedCanBeApproved(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	plantedSecret(t, cl)
	got := card(t, svc, cl, alice(), "kubectl annotate secret db -n web kubectl.kubernetes.io/last-applied-configuration-")
	if _, err := svc.Approve(context.Background(), cl, alice(), got.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, got.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, done.Reason)
	}
	sec, err := simClient(t, cl).CoreV1().Secrets("web").Get(context.Background(), "db", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sec.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Fatal("the annotation is still there")
	}
}
