package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/sherlocktools"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// aiSetup is the multi-cluster test console with the agents over a fake
// Claude API, the default provider's key saved, and two SuperAdmins.
func aiSetup(t *testing.T) (*httptest.Server, *http.Client, *fakeclaude.Fake, *ai.Service, *sherlock.Service) {
	t.Helper()
	fake := fakeclaude.New()
	fake.Delay = 0
	api := httptest.NewServer(fake)
	t.Cleanup(api.Close)
	keys := sherlock.NewKeys(nil, "", nil)
	if err := keys.Set(context.Background(), "sk-ant-test-key-0123456789", "test"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var aiSvc *ai.Service
	var svc *sherlock.Service
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		aiSvc = ai.New(ai.Options{Settings: o.Settings.(ai.SettingsStore), Keys: keys, DefaultURL: api.URL, ToolDefs: sherlocktools.Defs, Logger: logger})
		svc = sherlock.New(sherlock.Options{
			Agent: aiSvc.Agent, OnAnswer: aiSvc.OnAnswer, Client: &sherlock.Client{BaseURL: api.URL, Key: keys.Key},
			Settings: func() sherlock.Settings {
				sh := o.Settings.Current().SherlockOrDefault()
				return sherlock.Settings{Model: sh.Model, Caps: sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}}
			},
			Audit: o.Audit, Logger: logger,
		})
		aiSvc.SetEngine(svc)
		o.Sherlock, o.SherlockKeys, o.AI, o.SherlockDefaultURL = svc, keys, aiSvc, api.URL
		o.TwoPerson = twoperson.New(nil, nil)
	})
	t.Cleanup(svc.Wait)
	return srv, c, fake, aiSvc, svc
}

func aiJSON(t *testing.T, c *http.Client, method, url, body string, want int, v any) {
	t.Helper()
	r, text := send(t, c, method, url, body)
	if r.StatusCode != want {
		t.Fatalf("%s %s: %d %s", method, url, r.StatusCode, text)
	}
	if v != nil {
		if err := json.Unmarshal([]byte(text), v); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
}

// Settings › AI: providers with a credential that is never shown, a test
// of each, agents moved between them, and only for a SuperAdmin.
func TestAIProvidersAndAgentsAPI(t *testing.T) {
	srv, ops, fake, _, _ := aiSetup(t)
	viewer := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/ai", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer read the AI settings: %d", r.StatusCode)
	}
	var o aiOverview
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai", "", 200, &o)
	if len(o.Agents) != len(ai.Catalog) || len(o.Providers) != 1 || !o.Providers[0].Key.Set || len(o.Clusters) == 0 || o.Knowledge == "" {
		t.Fatalf("%+v", o)
	}
	// A gateway in front of the same fake, at another address.
	gw := httptest.NewServer(fake)
	t.Cleanup(gw.Close)
	body, _ := json.Marshal(map[string]any{"id": "second", "name": "Second", "kind": "anthropic", "endpoint": gw.URL + "/gateway", "model": "claude-sonnet-5-5"})
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(body), 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/providers/second/key", `{"key":"second-key-0123456789"}`, 200, &o)
	_, text := send(t, ops, http.MethodGet, srv.URL+"/api/v1/ai", "")
	if strings.Contains(text, "second-key-0123456789") || strings.Contains(text, "sk-ant-test-key") {
		t.Fatal("a credential reached the browser")
	}
	var test map[string]any
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers/default/test", "", 200, &test)
	if test["ok"] != true || test["model"] != "claude-opus-5-5" {
		t.Fatalf("test: %v", test)
	}
	// The Script reviewer on the author's provider needs the confirmation.
	if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/scriptReviewer", `{"model":"claude-haiku-5-5"}`); r.StatusCode != http.StatusConflict || !strings.Contains(text, "independent second opinion") {
		t.Fatalf("%d %s", r.StatusCode, text)
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/scriptReviewer", `{"provider":"second"}`, 200, &o)
	for _, a := range o.Agents {
		if a.ID == ai.ScriptReviewer && (a.ProviderID != "second" || a.Model != "claude-sonnet-5-5") {
			t.Fatalf("%+v", a)
		}
	}
	if r, _ := send(t, ops, http.MethodDelete, srv.URL+"/api/v1/ai/providers/second", ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a provider in use was removed: %d", r.StatusCode)
	}
	// Sherlock's clusters and limits are the settings of before.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock", `{"sherlockClusters":["local","nowhere"]}`, 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/limits", `{"dailyUSD":60,"monthlyUSD":600}`, 200, &o)
	var cfg sherlockConfig
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock/config", "", 200, &cfg)
	if !cfg.Settings.Clusters["local"] || cfg.Settings.Clusters["nowhere"] || cfg.Settings.DailyUSD != 60 {
		t.Fatalf("%+v", cfg.Settings)
	}
}

// Instructions go from a draft to live through a test and a second
// SuperAdmin's approval; Sherlock's settings endpoint of before reads and
// writes the same instructions.
func TestAIInstructionsLifecycleAPI(t *testing.T) {
	srv, ops, _, _, _ := aiSetup(t)
	ops2 := signIn(t, srv, "ops2@corp.example")
	var v ai.InstructionsView
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI findings first.\nThe vault is at 10.1.2.3.","notes":{"local":"Audit weekly."}}`, 200, &v)
	if v.Draft == nil || v.Draft.Stage != ai.StageDraft || len(v.Warnings) != 1 || v.ValuesKey != "agents.auditor.instructions" {
		t.Fatalf("%+v", v)
	}
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/live", ""); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, "test the draft first") {
		t.Fatalf("untested to live: %d %s", r.StatusCode, text)
	}
	var est map[string]any
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/test", `{"estimate":true}`, 200, &est)
	if est["estimateUSD"].(float64) <= 0 {
		t.Fatalf("%v", est)
	}
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/test", `{}`, http.StatusAccepted, nil)
	for i := 0; i < 200; i++ {
		aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/auditor/instructions", "", 200, &v)
		if v.Draft.Test != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v.Draft.Test == nil || v.Draft.Stage != ai.StageTest {
		t.Fatalf("the test did not land: %+v", v.Draft)
	}
	// Another SuperAdmin exists: live waits for their approval.
	var waiting struct {
		Waiting bool              `json:"waiting"`
		Request twoperson.Request `json:"request"`
	}
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/live", "", http.StatusAccepted, &waiting)
	if !waiting.Waiting || waiting.Request.Kind != twoperson.KindPrompt {
		t.Fatalf("%+v", waiting)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/admin/requests/"+itoa(waiting.Request.ID)+"/approve", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("approved their own: %d", r.StatusCode)
	}
	aiJSON(t, ops2, http.MethodPost, srv.URL+"/api/v1/admin/requests/"+itoa(waiting.Request.ID)+"/approve", "", 200, nil)
	v = ai.InstructionsView{}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/auditor/instructions", "", 200, &v)
	if v.State != ai.Edited || v.House != "Weigh PCI findings first.\nThe vault is at 10.1.2.3." || v.Live != 1 || v.Draft != nil || v.Notes["local"] != "Audit weekly." {
		t.Fatalf("%+v", v)
	}
	var prompt map[string]any
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/auditor/prompt", "", 200, &prompt)
	if !strings.Contains(prompt["instructions"].(string), "Weigh PCI findings first.") || !strings.Contains(prompt["cluster"].(string), "Audit weekly.") {
		t.Fatalf("%v", prompt)
	}
	// Sherlock's settings of before write its house instructions and the
	// notes for every cluster, live at once, and read them back.
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200,"instructions":"Be terse.","notes":"We run RKE2."}`)
	v = ai.InstructionsView{}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/sherlock/instructions", "", 200, &v)
	if v.House != "Be terse." || v.Notes[ai.AllClusters] != "We run RKE2." {
		t.Fatalf("%+v", v)
	}
	var cfg sherlockConfig
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock/config", "", 200, &cfg)
	if cfg.Settings.Instructions != "Be terse." || cfg.Settings.Notes != "We run RKE2." {
		t.Fatalf("%+v", cfg.Settings)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// An answer shows its hand-overs in the conversation; one may be saved as
// a test case, its tool results scrubbed.
func TestAIHandOverInTheConversationAndSaveAsTestCase(t *testing.T) {
	srv, ops, _, aiSvc, svc := aiSetup(t)
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`)
	m := askAndRead(t, srv, ops, svc, "[[chain troubleshooter]]")
	if m.Status != "done" || len(m.Hands) != 1 || m.Hands[0].ToName != "Troubleshooter" || m.Hands[0].Status != "done" {
		t.Fatalf("%+v", m)
	}
	m = askAndRead(t, srv, ops, svc, "Tell me about pod web/shop")
	var c ai.TestCase
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/tests", `{"cluster":"local","message":`+itoa(m.ID)+`,"name":"shop","points":["Running"]}`, 200, &c)
	if c.Agent != ai.Sherlock || len(c.Recorded) != 1 || c.Recorded[0].Tool != "get_pod" || c.Expect.Tools[0] != "get_pod" {
		t.Fatalf("%+v", c)
	}
	cases, _ := aiSvc.TestsOf(context.Background(), ai.Sherlock)
	if cases[len(cases)-1].ID != c.ID {
		t.Fatal("the case was not kept")
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/tests", `{"cluster":"local","message":9999}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a case from nothing: %d", r.StatusCode)
	}
}
