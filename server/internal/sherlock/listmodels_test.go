package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The model field's dropdown is filled from the provider's endpoint
// (Phase 12, wave 5): vLLM's /v1/models with each model's context window,
// Ollama's /api/tags where /v1/models is missing, the Claude API's list
// with its key, and no list for Vertex AI or a Bedrock gateway.
func TestModelsAreListedFromTheEndpoint(t *testing.T) {
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vllm/v1/models", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "vllm auth="+r.Header.Get("Authorization")+" team="+r.Header.Get("X-Team")+" q="+r.URL.RawQuery)
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
			{"id": "qwen3.8-27b", "object": "model", "owned_by": "vllm", "max_model_len": 16384},
			{"id": "embed-small", "object": "model", "owned_by": "vllm"},
		}})
	})
	mux.HandleFunc("GET /ollama/api/tags", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "llama3.3:70b", "model": "llama3.3:70b"}, {"name": "qwen3:8b"}}})
	})
	mux.HandleFunc("GET /claude/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-key" || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "claude-sonnet-5-5", "type": "model", "max_input_tokens": 200000}, {"id": "claude-haiku-5-5", "type": "model"}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	list := func(p Provider, key string) ([]ModelInfo, error) {
		c := &Client{Key: func() string { return key }, Provider: func() Provider { return p }}
		return c.Models(ctx)
	}

	got, err := list(Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/vllm/v1", Headers: map[string]string{"X-Team": "sre"}, Query: "v=1"}, "")
	if err != nil || len(got) != 2 || got[1] != (ModelInfo{ID: "qwen3.8-27b", ContextWindow: 16384, Owner: "vllm"}) || got[0].ContextWindow != 0 {
		t.Fatalf("vLLM: %+v %v", got, err)
	}
	if seen[0] != "vllm auth= team=sre q=v=1" {
		t.Fatalf("the request: %s", seen[0])
	}
	if _, err := list(Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/vllm/v1/chat/completions"}, "tok-123"); err != nil || seen[1] != "vllm auth=Bearer tok-123 team= q=" {
		t.Fatalf("with a key, from a completions address: %v %v", seen, err)
	}
	got, err = list(Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/ollama/v1"}, "")
	if err != nil || len(got) != 2 || got[0].ID != "llama3.3:70b" || got[1].ID != "qwen3:8b" {
		t.Fatalf("Ollama: %+v %v", got, err)
	}
	got, err = list(Provider{Kind: KindAnthropic, Endpoint: srv.URL + "/claude"}, "sk-ant-key")
	if err != nil || len(got) != 2 || got[1].ID != "claude-sonnet-5-5" || got[1].ContextWindow != 200000 {
		t.Fatalf("the Claude API: %+v %v", got, err)
	}
	var ae *APIError
	if _, err := list(Provider{Kind: KindAnthropic, Endpoint: srv.URL + "/claude"}, ""); !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Fatalf("without the key: %v", err)
	}
	if _, err := list(Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/nothing/v1"}, ""); !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("no list anywhere: %v", err)
	}
	for _, p := range []Provider{{Kind: KindVertex, Region: "europe-west1", Project: "p"}, {Kind: KindBedrock, Endpoint: srv.URL + "/gateway", Auth: AuthHeader}} {
		if _, err := list(p, "k"); !errors.Is(err, ErrNoModelList) {
			t.Fatalf("%s: %v", p.Kind, err)
		}
	}
}

// The estimate of a request's size errs a little over the real count,
// never far under, and a compact agent is offered no view action.
func TestTheFootprint(t *testing.T) {
	if n := EstimateTokens(strings.Repeat("abcd ", 84)); n != 100 {
		t.Fatalf("420 characters: %d", n)
	}
	defs := []ToolDef{{Name: "list_pods", Description: "Pods.", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	a := Agent{ID: SherlockID, Instructions: "You are Sherlock.", Tools: []string{"list_pods"}}
	whole := Measure(a, defs, true)
	if whole.ToolCount != 1+len(uiDefs(true)) || whole.Instructions <= EstimateTokens(a.Instructions) {
		t.Fatalf("whole: %+v", whole)
	}
	a.Compact = true
	if c := Measure(a, defs, true); c.ToolCount != 1 || c.Tools >= whole.Tools {
		t.Fatalf("compact: %+v", c)
	}
}
