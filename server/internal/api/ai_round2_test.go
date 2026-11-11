package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/sherlocktools"
)

// AI settings, round 2 (Phase 12, wave 5).

// The provider dialog tests its settings before they are saved and lists
// the endpoint's models; an in-house endpoint without a credential (the
// owner's A5000) is usable once its saved settings passed a test, and
// Sherlock on its small model answers in the compact form, with no view
// action in what it sends (AS-04, AS-05, AS-07).
func TestAIProviderDialogAndAnInHouseModel(t *testing.T) {
	srv, ops, fake, _, svc := aiSetup(t)
	house := httptest.NewServer(fake)
	t.Cleanup(house.Close)
	open := house.URL + "/open/v1"
	try := func(body map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		var out map[string]any
		aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers/try", string(b), 200, &out)
		return out
	}
	a5000 := map[string]any{"id": "a5000", "name": "A5000", "kind": "openai", "endpoint": open, "model": "qwen3.8-27b"}
	if out := try(map[string]any{"provider": a5000}); out["ok"] != true || out["keyless"] != true || out["model"] != "qwen3.8-27b" {
		t.Fatalf("the unsaved test: %v", out)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers/try", `{"provider":{"kind":"openai","endpoint":"ftp://x"}}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad endpoint: %d", r.StatusCode)
	}
	// The saved key goes only to the saved endpoint: tried against another
	// address, the default provider's key stays home and the fake refuses.
	if out := try(map[string]any{"id": "default", "provider": map[string]any{"kind": "anthropic", "endpoint": house.URL}}); out["ok"] != false || out["keyless"] != true {
		t.Fatalf("the saved key went to another endpoint: %v", out)
	}
	var models struct {
		Models []sherlock.ModelInfo `json:"models"`
		Error  string               `json:"error"`
	}
	b, _ := json.Marshal(map[string]any{"provider": a5000})
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers/models", string(b), 200, &models)
	if len(models.Models) != len(fakeclaude.OpenModels) || models.Models[0].ID != "llama-4-scout-17b" || models.Models[1].ContextWindow != 16384 || models.Error != "" {
		t.Fatalf("the models: %+v", models)
	}
	b, _ = json.Marshal(map[string]any{"id": "a5000", "name": "A5000", "kind": "openai", "endpoint": open, "model": "qwen3.8-27b", "contextWindow": 16384, "tested": map[string]any{"ok": true, "keyless": true}})
	var o aiOverview
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(b), 200, &o)
	pv := func() ai.ProviderView {
		for _, p := range o.Providers {
			if p.ID == "a5000" {
				return p
			}
		}
		t.Fatal("no A5000")
		return ai.ProviderView{}
	}
	if p := pv(); p.KeylessOK || p.Tested != nil || p.ContextWindow != 16384 {
		t.Fatalf("a test taken from the request: %+v", p)
	}
	models.Models, models.Error = nil, ""
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/providers/a5000/models", "", 200, &models)
	if len(models.Models) != 2 {
		t.Fatalf("the saved provider's models: %+v", models)
	}
	models.Models, models.Error = nil, ""
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/providers/default/models", "", 200, &models)
	if len(models.Models) != 3 || models.Models[0].ID != "claude-haiku-5-5" {
		t.Fatalf("the Claude API's models with its key: %+v", models)
	}
	// Sherlock on it is refused until its saved settings pass a test.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock", `{"provider":"a5000","sherlockClusters":["local"]}`, 200, &o)
	for _, a := range o.Agents {
		if a.ID == ai.Sherlock && !strings.Contains(a.Unusable, "has no credential") {
			t.Fatalf("usable untested: %+v", a)
		}
	}
	var test map[string]any
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers/a5000/test", "", 200, &test)
	if test["ok"] != true || test["keyless"] != true {
		t.Fatalf("the saved test: %v", test)
	}
	o = aiOverview{}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai", "", 200, &o)
	if p := pv(); !p.KeylessOK || p.Tested == nil || !p.Tested.OK {
		t.Fatalf("after the test: %+v", p)
	}
	for _, a := range o.Agents {
		if a.ID != ai.Sherlock {
			continue
		}
		if a.Unusable != "" || a.Footprint == nil || !a.Footprint.Compact || a.Footprint.Window != 16384 || a.Footprint.Whole.Total() <= 8192 || a.Footprint.Total() >= 4000 {
			t.Fatalf("Sherlock on the A5000: %q %+v", a.Unusable, a.Footprint)
		}
	}
	fake.Reset()
	m := askAndRead(t, srv, ops, svc, "Tell me about pod web/shop")
	if m.Status != "done" {
		t.Fatalf("the answer: %+v", m)
	}
	var sent struct {
		MaxTokens int `json:"max_tokens"`
		Tools     []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	reqs := fake.Requests()
	if len(reqs) == 0 || json.Unmarshal(reqs[0], &sent) != nil {
		t.Fatalf("nothing sent: %d", len(reqs))
	}
	if sent.MaxTokens != 4096 || len(sent.Tools) > len(ai.CompactTools) || !strings.Contains(sent.Messages[0].Content, "Rules you keep") {
		t.Fatalf("what was sent: max %d, %d tools, %.80q", sent.MaxTokens, len(sent.Tools), sent.Messages[0].Content)
	}
	for _, tl := range sent.Tools {
		if sherlock.IsUITool(tl.Function.Name) || tl.Function.Name == sherlock.ToolHandOver {
			t.Fatalf("the compact form offered %s", tl.Function.Name)
		}
	}
	// The drawer's built-in text: pre-filled, edited as a draft.
	var v ai.InstructionsView
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/sherlock/instructions", "", 200, &v)
	if len(v.Parts) != 2 || !strings.HasPrefix(v.Parts[0].Text, "You are Sherlock") || v.Parts[1].Text != ai.CompactCore() {
		t.Fatalf("the parts: %+v", v.Parts)
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock/draft", `{"house":"","parts":{"compact":"You are Sherlock, in brief."}}`, 200, &v)
	if v.Parts[1].Draft == nil || *v.Parts[1].Draft != "You are Sherlock, in brief." || v.Parts[1].Edited {
		t.Fatalf("the draft's part: %+v", v.Parts[1])
	}
	var prompt map[string]any
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/sherlock/prompt?draft=1", "", 200, &prompt)
	if !strings.HasPrefix(prompt["instructions"].(string), "You are Sherlock, in brief.") || prompt["compact"] != true {
		t.Fatalf("the draft's prompt: %v", prompt)
	}
}

// Sherlock's real request, before any question, measured with the
// toolbox's definitions: its whole form is about 10,000 tokens, its
// compact form well under 8,000 (AS-07; the figures in the CHANGELOG are
// Qwen 3's tokenizer's count of the same text).
func TestSherlocksRequestSize(t *testing.T) {
	_, _, _, aiSvc, _ := aiSetup(t)
	a, err := aiSvc.Preview(t.Context(), ai.Sherlock, "local", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	whole := sherlock.Measure(a, sherlocktools.Defs(), true)
	c := a
	c.Instructions, c.Tools, c.Roster, c.Compact = ai.Assemble(ai.CompactCore(), nil, ""), ai.CompactTools, nil, true
	compact := sherlock.Measure(c, sherlocktools.Defs(), true)
	t.Logf("whole %+v = %d, compact %+v = %d", whole, whole.Total(), compact, compact.Total())
	if whole.Total() < 8000 || whole.Total() > 14000 || compact.Total() > 4000 || compact.ToolCount != len(ai.CompactTools) {
		t.Fatalf("whole %+v, compact %+v", whole, compact)
	}
}
