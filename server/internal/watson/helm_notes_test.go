package watson

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	release "helm.sh/helm/v4/pkg/release/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// helm status shows a release's notes as the Helm tab does, masked, in
// every format.
func TestHelmStatusMasksTheNotes(t *testing.T) {
	rel := &release.Release{Name: "shop", Info: &release.Info{Notes: "Log in as admin / hunter2typed. See the docs."}, Config: map[string]any{"adminPass": "hunter2typed"}}
	var table, doc bytes.Buffer
	if err := (statusPrinter{rel: rel}).WriteTable(&table); err != nil {
		t.Fatal(err)
	}
	if err := (statusPrinter{rel: rel}).WriteJSON(&doc); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(doc.Bytes()) {
		t.Fatalf("not JSON: %s", doc.String())
	}
	for _, out := range []string{table.String(), doc.String()} {
		if strings.Contains(out, "hunter2") || !strings.Contains(out, "See the docs.") {
			t.Errorf("status: %s", out)
		}
	}
	if !strings.Contains(rel.Info.Notes, "hunter2typed") {
		t.Error("the release itself changed")
	}
}

// helm status -o json and -o yaml never print the release's objects:
// Helm loads them whole, Secrets with their data (review of November 24,
// 2026, finding 3).
func TestHelmStatusPrintsNoObjectOfTheRelease(t *testing.T) {
	secret := &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "shop-db", Namespace: "web"},
		Data: map[string][]byte{"password": []byte("hunter2-in-the-secret")}}
	rel := &release.Release{Name: "shop", Namespace: "web", Info: &release.Info{Resources: map[string][]runtime.Object{"v1/Secret": {secret}}}}
	for _, write := range []func(statusPrinter, *bytes.Buffer) error{
		func(p statusPrinter, b *bytes.Buffer) error { return p.WriteJSON(b) },
		func(p statusPrinter, b *bytes.Buffer) error { return p.WriteYAML(b) },
	} {
		var out bytes.Buffer
		if err := write(statusPrinter{rel: rel}, &out); err != nil {
			t.Fatal(err)
		}
		if enc := base64.StdEncoding.EncodeToString([]byte("hunter2-in-the-secret")); strings.Contains(out.String(), enc) || strings.Contains(out.String(), "hunter2") || strings.Contains(out.String(), "shop-db") {
			t.Errorf("status: %s", out.String())
		}
	}
	if len(rel.Info.Resources) != 1 {
		t.Error("the release itself changed")
	}
}
