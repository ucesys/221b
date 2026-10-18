package kube

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const planted = "PLANTED-SECRET-VALUE-9f8e7d6c5b4a"

// A Secret as kubectl apply leaves it: the whole object, values included,
// in the last-applied annotation, and a tool's own copy in another.
func plantedSecret() *corev1.Secret {
	b64 := base64.StdEncoding.EncodeToString([]byte(planted))
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "planted", Namespace: "default",
			Annotations: map[string]string{
				LastApplied:                 `{"apiVersion":"v1","kind":"Secret","stringData":{"token":"` + planted + `"}}`,
				"kapp.k14s.io/original":     `{"data":{"token":"` + b64 + `"}}`,
				"meta.helm.sh/release-name": "shop",
			},
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte(planted), "user": []byte("svc")},
	}
}

func TestYAMLHidesSecretValuesEverywhere(t *testing.T) {
	raw, err := json.Marshal(plantedSecret())
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	delete(obj["metadata"].(map[string]any), "managedFields")
	hideSecretData(obj)
	text, err := ToYAML(obj)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{planted, base64.StdEncoding.EncodeToString([]byte(planted)), "svc", "managedFields"} {
		if strings.Contains(text, leak) {
			t.Errorf("the YAML carries %q:\n%s", leak, text)
		}
	}
	for _, want := range []string{"token: <33 bytes, hidden by 221B>", "user: <3 bytes, hidden by 221B>", "meta.helm.sh/release-name: shop", "kubectl.kubernetes.io/last-applied-configuration: <", "kapp.k14s.io/original: <"} {
		if !strings.Contains(text, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, text)
		}
	}
}

func TestSecretSummaryKeepsNoValue(t *testing.T) {
	out, err := SummarizeSecret(plantedSecret())
	if err != nil {
		t.Fatal(err)
	}
	sum := out.(*SecretSummary)
	text, _ := json.Marshal(sum)
	for _, leak := range []string{planted, base64.StdEncoding.EncodeToString([]byte(planted))} {
		if strings.Contains(string(text), leak) {
			t.Errorf("the summary carries %q: %s", leak, text)
		}
	}
	if sum.Annotations["meta.helm.sh/release-name"] != "shop" || !strings.HasPrefix(sum.Annotations[LastApplied], "<") || !strings.HasPrefix(sum.Annotations["kapp.k14s.io/original"], "<") {
		t.Errorf("annotations: %v", sum.Annotations)
	}
	if len(sum.Keys) != 2 || sum.Keys[0].Key != "token" || sum.Keys[0].Bytes != len(planted) || sum.ManagedFields != nil {
		t.Errorf("summary: %+v", sum)
	}
}

// A TLS Secret keeps the public facts of its certificate (names, issuer,
// validity) and nothing of the key or of the PEM itself.
func TestTLSSecretKeepsOnlyCertificateFacts(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "shop.example.test"}, DNSNames: []string{"shop.example.test", "*.shop.example.test"}, NotBefore: until.Add(-90 * 24 * time.Hour), NotAfter: until}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	out, _ := SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": crt, "tls.key": []byte(planted)}})
	sum := out.(*SecretSummary)
	if sum.Cert == nil || sum.Cert.Error != "" || !sum.Cert.NotAfter.Equal(until) || strings.Join(sum.Cert.DNSNames, ",") != "shop.example.test,*.shop.example.test" {
		t.Fatalf("cert %+v", sum.Cert)
	}
	text, _ := json.Marshal(sum)
	b64 := base64.StdEncoding.EncodeToString(der)
	for _, leak := range []string{planted, base64.StdEncoding.EncodeToString([]byte(planted)), "BEGIN CERTIFICATE", b64[:40]} {
		if strings.Contains(string(text), leak) {
			t.Errorf("the summary carries %q: %s", leak, text)
		}
	}
	// An Opaque Secret with a tls.crt key is not parsed.
	out, _ = SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "opaque"}, Data: map[string][]byte{"tls.crt": crt}})
	if out.(*SecretSummary).Cert != nil {
		t.Error("an Opaque Secret's tls.crt was read")
	}
	// Garbage is reported, not parsed.
	out, _ = SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "bad"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": []byte("nope")}})
	if c := out.(*SecretSummary).Cert; c == nil || c.Error != "tls.crt holds no PEM certificate" {
		t.Errorf("garbage: %+v", c)
	}
}

// A name is one path segment: "..", "/" or "%" would steer the read onto
// another kind's object, a Secret's values unhidden (X-01).
func TestCheckPathNames(t *testing.T) {
	for _, c := range []struct {
		ns, name string
		ok       bool
	}{
		{"shop", "web", true},
		{"", "n1", true},
		{"shop", "web-0.v2", true},
		{"default", "../secrets/db", false},
		{"default", "..", false},
		{"default", "a/b", false},
		{"default", "a%2Fb", false},
		{"..", "web", false},
		{"shop", "", false},
	} {
		if err := CheckPathNames(c.ns, c.name); (err == nil) != c.ok {
			t.Errorf("%q/%q: %v", c.ns, c.name, err)
		}
	}
}

// Under a credential-looking key every string and number is hidden, in
// lists and maps too; other keys keep their values (P2-07).
func TestMaskValuesUnderCredentialKeys(t *testing.T) {
	vals := map[string]any{
		"replicas":  float64(2),
		"password":  float64(12345678),
		"passwords": []any{"hunter2", "swordfish"},
		"apiTokens": map[string]any{"ci": "abc", "enabled": true},
		"db":        map[string]any{"host": "db.shop", "secretName": ""},
	}
	var masked []string
	text, err := ToYAML(MaskValues(vals, "", &masked))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"12345678", "hunter2", "swordfish", "abc"} {
		if strings.Contains(text, leak) {
			t.Errorf("the values carry %q:\n%s", leak, text)
		}
	}
	for _, keep := range []string{"replicas: 2", "host: db.shop", "enabled: true"} {
		if !strings.Contains(text, keep) {
			t.Errorf("the values lost %q:\n%s", keep, text)
		}
	}
	want := map[string]bool{"password": true, "passwords[0]": true, "passwords[1]": true, "apiTokens.ci": true}
	if len(masked) != len(want) {
		t.Errorf("masked %v", masked)
	}
	for _, m := range masked {
		if !want[m] {
			t.Errorf("masked %q", m)
		}
	}
}
