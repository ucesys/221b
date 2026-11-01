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
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	// Any Secret that holds both keys serves TLS, whatever its type, and
	// gives away no more.
	out, _ = SummarizeSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "generic"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"tls.crt": crt, "tls.key": []byte(planted)}})
	generic := out.(*SecretSummary)
	if generic.Cert == nil || generic.Cert.Error != "" || !generic.Cert.NotAfter.Equal(until) {
		t.Fatalf("a generic Secret's cert %+v", generic.Cert)
	}
	if text, _ := json.Marshal(generic); strings.Contains(string(text), planted) || strings.Contains(string(text), "BEGIN CERTIFICATE") {
		t.Errorf("the summary carries a value: %s", text)
	}
	// An Opaque Secret with a tls.crt key alone is not parsed.
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

// A ServiceAccount token's namespace key says what the Secret's metadata
// says: it hides nothing, so it does not hide the ServiceAccount's name,
// and the Access tab finds its long-lived tokens (P4-03).
func TestATokenSecretKeepsTheNameOfItsServiceAccount(t *testing.T) {
	const name = "kubernetes.io/service-account.name"
	token := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-server-token", Annotations: map[string]string{name: "argocd-server", "other": "argocd"}},
			Type:       corev1.SecretTypeServiceAccountToken,
			Data:       map[string][]byte{"namespace": []byte("argocd"), "token": []byte(planted), "ca.crt": []byte("the cluster's CA")},
		}
	}
	summarized := func(s *corev1.Secret) map[string]string {
		out, err := SummarizeSecret(s)
		if err != nil {
			t.Fatal(err)
		}
		return out.(*SecretSummary).Annotations
	}
	// The YAML view is the other way out of the server.
	yamlOf := func(s *corev1.Secret) map[string]string {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
		if err != nil {
			t.Fatal(err)
		}
		hideSecretData(raw)
		out := map[string]string{}
		for k, v := range raw["metadata"].(map[string]any)["annotations"].(map[string]any) {
			out[k], _ = v.(string)
		}
		return out
	}
	for way, read := range map[string]func(*corev1.Secret) map[string]string{"summary": summarized, "YAML": yamlOf} {
		if a := read(token()); a[name] != "argocd-server" || a["other"] != "argocd" {
			t.Errorf("%s: the name is hidden: %v", way, a)
		}

		// Everything else stays hidden: the token, raw or base64...
		s := token()
		s.Annotations["copy"] = "x " + planted
		s.Annotations["copy64"] = base64.StdEncoding.EncodeToString([]byte(planted))
		if a := read(s); !strings.HasPrefix(a["copy"], "<") || !strings.HasPrefix(a["copy64"], "<") {
			t.Errorf("%s: the token shows: %v", way, a)
		}
		// ...a namespace key that does not say the Secret's namespace...
		s = token()
		s.Data["namespace"] = []byte("hunter2")
		s.Annotations["copy"] = "hunter2"
		if a := read(s); !strings.HasPrefix(a["copy"], "<") {
			t.Errorf("%s: a forged namespace shows: %v", way, a)
		}
		// ...the same value under another key...
		s = token()
		s.Data["extra"] = []byte("argocd")
		if a := read(s); !strings.HasPrefix(a[name], "<") {
			t.Errorf("%s: a value under another key shows: %v", way, a)
		}
		// ...and a namespace key in any other type of Secret.
		s = token()
		s.Type = corev1.SecretTypeOpaque
		if a := read(s); !strings.HasPrefix(a[name], "<") {
			t.Errorf("%s: an Opaque Secret's value shows: %v", way, a)
		}
	}
}

// A secret env var in the list form charts take is masked as under a key,
// and so are the other names and any value that carries a credential.
func TestMaskValuesCoversListFormsNamesAndURLs(t *testing.T) {
	vals := map[string]any{
		"env": []any{
			map[string]any{"name": "DB_PASSWORD", "value": "hunter2one"},
			map[string]any{"name": "MODE", "value": "prod"},
		},
		"smtp":     map[string]any{"pass": "hunter2two", "pwd": "hunter2three", "host": "mail"},
		"database": map[string]any{"dsn": "hunter2four", "connectionString": "hunter2five", "url": "postgres://app:hunter2six@db/x"},
		"homepage": "https://example.com/docs",
	}
	var masked []string
	text, err := ToYAML(MaskValues(vals, "", &masked))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "hunter2") {
		t.Errorf("the values carry a credential:\n%s", text)
	}
	for _, keep := range []string{"name: DB_PASSWORD", "value: prod", "host: mail", "homepage: https://example.com/docs"} {
		if !strings.Contains(text, keep) {
			t.Errorf("the values lost %q:\n%s", keep, text)
		}
	}
	if !slices.Contains(masked, "env.DB_PASSWORD") || !slices.Contains(masked, "database.url") {
		t.Errorf("masked %v", masked)
	}
}

// A release's notes are masked before anything keeps them: what the values,
// the chart's defaults and the manifest's Secrets hold goes under any name.
func TestReleaseNotesAreMaskedBeforeTheyAreKept(t *testing.T) {
	var rec HelmRecord
	rec.Name, rec.Namespace, rec.Version = "shop", "web", 1
	rec.Config = map[string]any{"auth": map[string]any{"password": "hunter2typed"}, "pin": float64(987654)}
	rec.Chart.Values = map[string]any{"adminPass": "hunter2default"}
	rec.Manifest = "---\nkind: Secret\nmetadata:\n  name: made\nstringData:\n  key: hunter2string\ndata:\n  other: " + base64.StdEncoding.EncodeToString([]byte("hunter2data")) + "\n"
	rec.Info.Notes = "Log in as admin / hunter2typed\nor hunter2default, hunter2string, hunter2data or " + base64.StdEncoding.EncodeToString([]byte("hunter2typed")) + "\npassword: hunter2named\nSee the docs."
	r, err := DecodeHelmRelease(EncodeHelmRelease(rec))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Notes, "hunter2") || strings.Contains(r.Notes, base64.StdEncoding.EncodeToString([]byte("hunter2typed"))) {
		t.Errorf("the notes keep a credential: %s", r.Notes)
	}
	if !strings.Contains(r.Notes, "See the docs.") || !strings.Contains(r.Notes, "Log in as admin") {
		t.Errorf("the notes lost their text: %s", r.Notes)
	}
}

// A value too short to replace without damage takes the notes with it.
func TestNotesThatPrintATooShortValueAreDropped(t *testing.T) {
	config := map[string]any{"password": "x1"}
	if got := MaskNotes("Sign in with x1 now.", config, nil, ""); got != NotesHidden {
		t.Errorf("notes: %q", got)
	}
	if got := MaskNotes("Run bx12 now.", config, nil, ""); got != "Run bx12 now." {
		t.Errorf("notes that do not print it changed: %q", got)
	}
}
