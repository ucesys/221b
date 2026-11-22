package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Administration › AI: an agent's effort, Auto and floor are saved at once
// and audited with what they were; the classifier's provider is chosen,
// or none, and audited; the test dialog's run takes an effort and a model
// to test at (AI-43 to AI-48).
func TestEffortAutoAndTheClassifierAPI(t *testing.T) {
	srv, ops, fake, _, _, aud := aiSetupWith(t, nil)
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", `{"effort":"auto","model":"auto","floorEffort":"medium"}`, 200, &o)
	for _, a := range o.Agents {
		if a.ID == "auditor" && (a.Settings.Effort != "auto" || a.Model != "auto" || a.Settings.FloorEffort != "medium") {
			t.Fatalf("%+v", a)
		}
	}
	if !strings.Contains(aud.String(), "effort auto, model auto, floor medium/none (was effort the model's default, model the provider's default, floor none/none)") {
		t.Fatalf("audit: %s", aud.String())
	}
	for _, bad := range []string{`{"effort":"extreme"}`, `{"effort":"auto","floorEffort":"max"}`} {
		if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", bad); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, r.StatusCode, text)
		}
	}
	// The classifier: only an OpenAI-compatible provider with a model.
	if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/classifier", `{"provider":"default"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("the default provider: %d %s", r.StatusCode, text)
	}
	body, _ := json.Marshal(map[string]any{"id": "a5000", "name": "A5000", "kind": "openai", "endpoint": "http://vllm.example/v1", "model": "qwen3.8-27b"})
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(body), 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/classifier", `{"provider":"a5000"}`, 200, &o)
	if o.Classifier != "a5000" || !strings.Contains(aud.String(), "Auto's classifier: off → a5000") {
		t.Fatalf("%q, audit %s", o.Classifier, aud.String())
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/classifier", `{"provider":""}`, 200, &o)
	if o.Classifier != "" || !strings.Contains(aud.String(), "Auto's classifier: a5000 → off") {
		t.Fatalf("off: %q", o.Classifier)
	}
	viewer := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, viewer, http.MethodPut, srv.URL+"/api/v1/ai/classifier", `{"provider":""}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer: %d", r.StatusCode)
	}
	// The test dialog's run at an effort and a model: refused when they are
	// not ones, estimated when they are.
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", `{"live":true,"effort":"auto"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("effort auto: %d %s", r.StatusCode, text)
	}
	var est map[string]any
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", `{"live":true,"estimate":true,"effort":"low","model":"claude-haiku-5-5"}`, 200, &est)
	if est["estimateUSD"].(float64) <= 0 {
		t.Fatalf("estimate %v", est)
	}
	_ = fake
}
