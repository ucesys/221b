package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/ai/auto"
)

// Administration › AI: an agent's effort, Auto and floor are saved at once
// and audited with what they were; the classifier's provider is chosen,
// or none, and audited; the test dialog's run takes an effort and a model
// to test at (AI-43 to AI-48).
func TestEffortAutoAndTheClassifierAPI(t *testing.T) {
	srv, ops, fake, _, _, aud := aiSetupWith(t, nil)
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", `{"effort":"auto","model":"auto","floorEffort":"medium"}`, 200, &o)
	if a := agentIn(t, o, "auditor"); a.Settings.Effort != "auto" || a.Model != "auto" || a.Settings.FloorEffort != "medium" {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(aud.String(), "effort auto, model auto, floor medium/none (was effort the model's default, model the provider's default, floor none/none)") {
		t.Fatalf("audit: %s", aud.String())
	}
	for _, bad := range []string{`{"effort":"extreme"}`, `{"effort":"auto","floorEffort":"max"}`} {
		if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", bad); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, r.StatusCode, text)
		}
	}
	// The classifier: only an OpenAI-compatible provider with a model, of
	// the list: not the default provider, not an Anthropic gateway, not an
	// OpenAI-compatible one with no default model.
	for _, p := range []map[string]any{
		{"id": "gateway", "name": "Gateway", "kind": "anthropic", "endpoint": "https://gw.example", "model": "claude-opus-5-5"},
		{"id": "bare", "name": "Bare", "kind": "openai", "endpoint": "http://bare.example/v1"},
	} {
		body, _ := json.Marshal(p)
		aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(body), 200, &o)
	}
	for id, why := range map[string]string{"default": "there is no provider", "gateway": "OpenAI-compatible", "bare": "default model first", "nosuch": "there is no provider"} {
		if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/classifier", `{"provider":"`+id+`"}`); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, why) {
			t.Fatalf("the classifier on %s: %d %s", id, r.StatusCode, text)
		}
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
	// not ones, estimated when they are, at them: the Troubleshooter on
	// Opus estimated on Haiku at low costs less than as set, and the
	// estimate asks no model anything.
	for _, bad := range []string{`{"live":true,"effort":"auto"}`, `{"live":true,"model":"auto"}`} {
		if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", bad); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, r.StatusCode, text)
		}
	}
	fake.Reset()
	var plain, est map[string]any
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", `{"live":true,"estimate":true}`, 200, &plain)
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", `{"live":true,"estimate":true,"effort":"low","model":"claude-haiku-5-5"}`, 200, &est)
	if p, e := plain["estimateUSD"].(float64), est["estimateUSD"].(float64); e <= 0 || e >= p || est["cases"] != plain["cases"] {
		t.Fatalf("the estimate on Haiku at low %v, as set %v", est, plain)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("an estimate sent %d requests", n)
	}
}

// agentIn is an agent of the overview; the test fails without it.
func agentIn(t *testing.T, o aiOverview, id string) ai.AgentView {
	t.Helper()
	for _, a := range o.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent %s in the overview", id)
	return ai.AgentView{}
}

// A provider's offered models and their tiers are saved with it, shown in
// the overview and audited (A9); Auto's model on an OpenAI-compatible
// provider is taken once one has a tier, and a model it does not offer is
// refused.
func TestAProvidersOfferedModelsAPIA9(t *testing.T) {
	srv, ops, _, _, _, aud := aiSetupWith(t, nil)
	var o aiOverview
	body, _ := json.Marshal(map[string]any{"id": "a5000", "name": "A5000", "kind": "openai", "endpoint": "http://vllm.example/v1", "model": "qwen3.8-27b"})
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(body), 200, &o)
	if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", `{"provider":"a5000","model":"auto"}`); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, "none has a tier") {
		t.Fatalf("Auto with no tier: %d %s", r.StatusCode, text)
	}
	body, _ = json.Marshal(map[string]any{"name": "A5000", "kind": "openai", "endpoint": "http://vllm.example/v1", "model": "qwen3.8-27b", "offeredChosen": true,
		"offered": []map[string]string{{"model": "qwen3.8-8b", "tier": "light"}, {"model": "qwen3.8-27b", "tier": "standard"}}})
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/providers/a5000", string(body), 200, &o)
	if !strings.Contains(aud.String(), "offers qwen3.8-8b (light), qwen3.8-27b (standard)") {
		t.Fatalf("audit: %s", aud.String())
	}
	i := slices.IndexFunc(o.Providers, func(p ai.ProviderView) bool { return p.ID == "a5000" })
	if i < 0 {
		t.Fatalf("no a5000 in the view: %+v", o.Providers)
	}
	if p := o.Providers[i]; p.OffersAll || len(p.Offers) != 2 || p.Offers[0] != (auto.Offered{Model: "qwen3.8-8b", Tier: "light"}) || p.Offers[1] != (auto.Offered{Model: "qwen3.8-27b", Tier: "standard"}) {
		t.Fatalf("the view: %+v", p)
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", `{"provider":"a5000","model":"auto"}`, 200, &o)
	if a := agentIn(t, o, "auditor"); a.Model != "auto" || a.ProviderID != "a5000" || a.AutoNoTier {
		t.Fatalf("the Auditor on Auto: %+v", a)
	}
	if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/troubleshooter", `{"provider":"a5000","model":"qwen3.8-235b"}`); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, "does not offer") {
		t.Fatalf("a model not offered: %d %s", r.StatusCode, text)
	}
}
