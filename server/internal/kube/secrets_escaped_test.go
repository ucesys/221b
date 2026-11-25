package kube

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeKey is an obviously fake private key: multi-line, as PEM is.
const fakeKey = "-----BEGIN PRIVATE KEY-----\nFAKEFAKEFAKEnotakey0001\nFAKEFAKEFAKEnotakey0002\n-----END PRIVATE KEY-----\n"

// fakeCert is as fake a certificate.
const fakeCert = "-----BEGIN CERTIFICATE-----\nfake-cert\n-----END CERTIFICATE-----\n"

// A tool that keeps the applied object in an annotation keeps it as JSON,
// where a value is escaped: kapp's original, or any other one (kube#2). No
// form of a value reaches the cache or the YAML view.
func TestAnnotationsHoldingAValueEscapedAreHidden(t *testing.T) {
	applied := func(escapeHTML bool) string {
		obj := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
			"metadata":   map[string]any{"name": "web-tls", "namespace": "shop"},
			"stringData": map[string]string{"tls.crt": fakeCert, "tls.key": fakeKey, "note": `fake<&>"pass"`}}
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(escapeHTML)
		_ = enc.Encode(obj)
		return strings.TrimSpace(b.String())
	}
	keyJSON, _ := json.Marshal(fakeKey)
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-tls", Namespace: "shop", Annotations: map[string]string{
			"kapp.k14s.io/original":           applied(true),
			"objectset.rio.cattle.io/applied": "H4sIAAAAAAAA/fake",
			"example.com/applied":             applied(false),
			// A part of a JSON text, which does not parse.
			"example.com/key-part": `{"tls.key":` + string(keyJSON),
			"example.com/note":     `"note":"fake\u003c\u0026\u003e\"pass\""`,
			"example.com/team":     "payments",
		}},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte(fakeCert), "tls.key": []byte(fakeKey), "note": []byte(`fake<&>"pass"`)},
	}
	out, err := SummarizeSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	sum := out.(*SecretSummary)
	for k, v := range sum.Annotations {
		if k == "example.com/team" {
			if v != "payments" {
				t.Errorf("an annotation with no value was hidden: %q", v)
			}
			continue
		}
		if !strings.HasPrefix(v, "<") || !strings.HasSuffix(v, "hidden by 221B>") {
			t.Errorf("annotation %s kept: %q", k, v)
		}
	}

	// The YAML view hides the same.
	raw, _ := json.Marshal(s)
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	hideSecretData(obj)
	text, _ := ToYAML(obj)
	for _, leak := range []string{"FAKEFAKEFAKE", "fake-cert", "pass"} {
		if strings.Contains(text, leak) {
			t.Errorf("the YAML carries %q:\n%s", leak, text)
		}
	}
	if !strings.Contains(text, "example.com/team: payments") {
		t.Errorf("the YAML lost an ordinary annotation:\n%s", text)
	}
}
