package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/clusters"
)

const plantedValue = "PLANTED-SECRET-VALUE-9f8e7d6c5b4a"

func seedSecret(src *source) {
	src.putSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "web", ResourceVersion: "7"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"password": []byte(plantedValue), "blob": {0, 1, 2, 255}}})
}

// NS-01: a namespace made from the console, as the person.
func TestNamespaceIsCreatedAsThePerson(t *testing.T) {
	srv, ops, _, aud, src, _ := multiSetupWith(t, "ops@corp.example", nil)
	if r, body := send(t, ops, http.MethodPost, srv.URL+"/api/v1/namespaces", `{"name":"made-here","labels":{"team":"a"}}`); r.StatusCode != http.StatusCreated || src.Object("namespaces", "", "made-here") == nil {
		t.Fatalf("create: %d %s", r.StatusCode, body)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/namespaces", `{"name":"made-here"}`); r.StatusCode != http.StatusConflict {
		t.Errorf("twice: %d", r.StatusCode)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/namespaces", `{"name":"Bad_Name"}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad name: %d", r.StatusCode)
	}
	if !strings.Contains(aud.String(), "namespace.create") {
		t.Errorf("audit: %s", aud.String())
	}
}

// SE-02 to SE-04: an Admin edits a Secret blind by default; only with
// secrets.reveal on does one key's value reach the browser, audited; a
// Viewer may do neither; keys-only views still carry no value.
func TestSecretValuesAreWrittenBlindAndRevealedOnlyWhenTheChartSaysSo(t *testing.T) {
	srv, ops, _, aud, src, _ := multiSetupWith(t, "ops@corp.example", nil)
	seedSecret(src)
	r, body := send(t, ops, http.MethodGet, srv.URL+"/api/v1/secrets/config", "")
	if r.StatusCode != 200 || !strings.Contains(body, `"read":true`) || !strings.Contains(body, `"reveal":false`) || !strings.Contains(body, `"edit":true`) {
		t.Fatalf("config %d %s", r.StatusCode, body)
	}
	// Revealing is off: the value stays on the server, and the refusal is audited.
	r, body = send(t, ops, http.MethodGet, srv.URL+"/api/v1/secrets/web/db/value?key=password", "")
	if r.StatusCode != http.StatusForbidden || strings.Contains(body, plantedValue) || !strings.Contains(body, "secrets.reveal") {
		t.Fatalf("reveal off: %d %s", r.StatusCode, body)
	}
	// A blind edit: replace one key, add one, remove one; the audit log names keys only.
	r, body = send(t, ops, http.MethodPut, srv.URL+"/api/v1/secrets/web/db", `{"set":{"password":"new-one","token":"dG9r"},"base64":["token"],"remove":["blob"],"resourceVersion":"7"}`)
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("edit %d %s", r.StatusCode, body)
	}
	got := src.full["web/db"]
	if string(got.Data["password"]) != "new-one" || string(got.Data["token"]) != "tok" || got.Data["blob"] != nil {
		t.Errorf("after the edit: %q", got.Data)
	}
	if a := aud.String(); !strings.Contains(a, "secret.update") || !strings.Contains(a, "password,token") || !strings.Contains(a, "removed=") || !strings.Contains(a, "blob") || strings.Contains(a, "new-one") || strings.Contains(a, plantedValue) {
		t.Errorf("audit: %s", a)
	}
	// A stale edit is refused, not applied over someone else's.
	if r, body := send(t, ops, http.MethodPut, srv.URL+"/api/v1/secrets/web/db", `{"set":{"password":"older"},"resourceVersion":"7"}`); r.StatusCode != http.StatusConflict {
		t.Errorf("stale edit: %d %s", r.StatusCode, body)
	}
	// A new Secret.
	if r, body := send(t, ops, http.MethodPost, srv.URL+"/api/v1/secrets/web", `{"name":"made-here","set":{"a":"1"}}`); r.StatusCode != http.StatusCreated || src.full["web/made-here"] == nil || src.full["web/made-here"].Type != corev1.SecretTypeOpaque {
		t.Errorf("create: %d %s", r.StatusCode, body)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/secrets/web", `{"name":"made-here","set":{"a":"1"}}`); r.StatusCode != http.StatusConflict {
		t.Errorf("create twice: %d", r.StatusCode)
	}
	if r, body := send(t, ops, http.MethodPost, srv.URL+"/api/v1/secrets/web", `{"name":"Bad Name","set":{"a":"1"}}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad name: %d %s", r.StatusCode, body)
	}
	// The keys-only views still say nothing of the values.
	for _, path := range []string{"/api/v1/objects/secrets/web/db", "/api/v1/yaml/secrets/web/db", "/api/v1/snapshot?views=namespaces"} {
		if _, body := send(t, ops, http.MethodGet, srv.URL+path, ""); strings.Contains(body, "new-one") || strings.Contains(body, plantedValue) {
			t.Errorf("%s shows a value: %s", path, body)
		}
	}

	// A Viewer may neither see nor change.
	srv2, viewer, _, _, src2, _ := multiSetupWith(t, "viewer@corp.example", nil)
	seedSecret(src2)
	if r, _ := send(t, viewer, http.MethodPut, srv2.URL+"/api/v1/secrets/web/db", `{"set":{"password":"x"}}`); r.StatusCode != http.StatusForbidden {
		t.Errorf("viewer edit: %d", r.StatusCode)
	}
	if r, body := send(t, viewer, http.MethodGet, srv2.URL+"/api/v1/secrets/config", ""); r.StatusCode != 200 || !strings.Contains(body, `"edit":false`) {
		t.Errorf("viewer config: %d %s", r.StatusCode, body)
	}

	// With secrets.reveal on, one key at a time, as text or base64, audited.
	srv3, ops3, _, aud3, src3, _ := multiSetupWith(t, "ops@corp.example", map[string]bool{"secrets/kube-system": true}, func(o *Options) { o.SecretsReveal = true })
	seedSecret(src3)
	r, body = send(t, ops3, http.MethodGet, srv3.URL+"/api/v1/secrets/web/db/value?key=password", "")
	if r.StatusCode != 200 || !strings.Contains(body, `"value":"`+plantedValue+`"`) || strings.Contains(body, `"base64":true`) || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %s %s", r.StatusCode, body, r.Header.Get("Cache-Control"))
	}
	r, body = send(t, ops3, http.MethodGet, srv3.URL+"/api/v1/secrets/web/db/value?key=blob", "")
	if r.StatusCode != 200 || !strings.Contains(body, `"base64":true`) || !strings.Contains(body, `"value":"AAEC/w=="`) {
		t.Errorf("binary value: %d %s", r.StatusCode, body)
	}
	if r, _ := send(t, ops3, http.MethodGet, srv3.URL+"/api/v1/secrets/web/db/value?key=nope", ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("missing key: %d", r.StatusCode)
	}
	if a := aud3.String(); !strings.Contains(a, "secret.reveal") || !strings.Contains(a, "password") || strings.Contains(a, plantedValue) {
		t.Errorf("reveal audit: %s", a)
	}
	// Kubernetes decides: a namespace the person may not read is refused.
	src3.putSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sys", Namespace: "kube-system", ResourceVersion: "1"}, Data: map[string][]byte{"k": []byte("v")}})
	if r, body := send(t, ops3, http.MethodGet, srv3.URL+"/api/v1/secrets/kube-system/sys/value?key=k", ""); r.StatusCode != http.StatusForbidden || strings.Contains(body, `"v"`) {
		t.Errorf("denied namespace: %d %s", r.StatusCode, body)
	}
}

// auditEvents reads the audit log's records back.
func auditEvents(t *testing.T, out string) []audit.Event {
	t.Helper()
	var list []audit.Event
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var e audit.Event
		if err := json.Unmarshal([]byte(line), &e); err == nil && e.Action != "" {
			list = append(list, e)
		}
	}
	return list
}

// An audit record says which cluster it concerns and how the attempt ended,
// in the fields a filter or a SIEM reads, not only in the words of Detail.
func TestAuditRecordsNameTheClusterAndHowTheAttemptEnded(t *testing.T) {
	srv, ops, _, aud, src, _ := multiSetupWith(t, "ops@corp.example", map[string]bool{"secrets/kube-system": true}, func(o *Options) { o.SecretsReveal = true })
	seedSecret(src)
	src.putSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sys", Namespace: "kube-system", ResourceVersion: "1"}, Data: map[string][]byte{"k": []byte("v")}})
	send(t, ops, http.MethodGet, srv.URL+"/api/v1/secrets/web/db/value?key=password", "")
	send(t, ops, http.MethodGet, srv.URL+"/api/v1/secrets/kube-system/sys/value?key=k", "")
	send(t, ops, http.MethodGet, srv.URL+"/api/v1/secrets/web/absent/value?key=k", "")
	send(t, ops, http.MethodPost, srv.URL+"/api/v1/namespaces", `{"name":"web"}`)

	srv2, ops2, _, aud2, _, _ := multiSetupWith(t, "ops@corp.example", nil)
	send(t, ops2, http.MethodGet, srv2.URL+"/api/v1/secrets/web/db/value?key=password", "")

	type rec struct{ action, target, outcome string }
	var got []rec
	for _, e := range append(auditEvents(t, aud.String()), auditEvents(t, aud2.String())...) {
		if strings.HasPrefix(e.Action, "secret.") || strings.HasPrefix(e.Action, "namespace.") {
			if e.Cluster != clusters.LocalID {
				t.Errorf("%s has cluster %q", e.Action, e.Cluster)
			}
			if strings.Contains(e.Detail, "outcome=") {
				t.Errorf("%s still says its outcome in the detail: %s", e.Action, e.Detail)
			}
			got = append(got, rec{e.Action, e.Target, e.Outcome})
		}
	}
	want := []rec{
		{"secret.reveal", "web/db", audit.OutcomeOK},
		{"secret.reveal", "kube-system/sys", audit.OutcomeDenied},
		{"secret.reveal", "web/absent", audit.OutcomeFailed},
		{"namespace.create", "web", audit.OutcomeFailed},
		{"secret.reveal", "web/db", audit.OutcomeDenied},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("records %+v, want %+v", got, want)
	}

	// What concerns the console itself names no cluster.
	send(t, ops, http.MethodPut, srv.URL+"/api/v1/settings", `{"refreshSeconds":7}`)
	for _, e := range auditEvents(t, aud.String()) {
		if e.Action == audit.ActionSettings && (e.Cluster != "" || e.Outcome != audit.OutcomeOK) {
			t.Fatalf("settings record %+v", e)
		}
	}
}
