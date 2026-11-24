package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/kv"
)

// The findings of the code review of the AI API changes (phase-13.1-ai,
// November 24, 2026).

// Finding 8: the test panel of an agent whose provider cannot answer
// (no credential) is a conflict that says why, not a server error; an
// agent that is not there is not found.
func TestATestOfAnAgentWithAnUnusableProviderReview8(t *testing.T) {
	srv, ops, _, _, _, _ := aiSetupWith(t, nil)
	var o aiOverview
	body, _ := json.Marshal(map[string]any{"id": "gw", "name": "GW", "kind": "openai", "endpoint": "https://gw.example/v1", "model": "gpt-5"})
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(body), 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/troubleshooter", `{"provider":"gw"}`, 200, &o)
	for _, b := range []string{`{"live":true,"replays":10,"estimate":true}`, `{"live":true}`} {
		if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", b); r.StatusCode != http.StatusConflict || !strings.Contains(text, "has no credential") {
			t.Fatalf("%s: %d %s", b, r.StatusCode, text)
		}
	}
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/nosuch/test", `{"live":true}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("no such agent: %d %s", r.StatusCode, text)
	}
}

// Finding 7: asking for a script while the Automation author is off is a
// conflict that says so (AI-54), not a 502 of the provider's.
func TestAScriptWithTheAuthorOffIsAConflictReview7(t *testing.T) {
	srv, ops, _, _, _, _ := aiSetupOn(t, nil, nil, func(o *Options) {
		st := automation.NewStore(&kv.Memory{}, nil)
		o.Automation, o.AutomationStore = automation.NewEngine(automation.Options{Store: st, Exec: noExec{}}), st
	})
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock", `{"sherlockClusters":["local"]}`, 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/automationAuthor", `{"on":false,"confirmSingleProvider":true}`, 200, &o)
	r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/automation/sherlock/draft", `{"request":"install chrony"}`)
	if r.StatusCode != http.StatusConflict || !strings.Contains(text, "the Automation author is off") {
		t.Fatalf("%d %s", r.StatusCode, text)
	}
}
