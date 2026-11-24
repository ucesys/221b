package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/ai/auto"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
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

// interposed runs between once, before the first save of the agents'
// settings: another save that comes between a save's read and its write.
type interposed struct {
	kv.Backend
	once    sync.Once
	between func()
}

func (b *interposed) SaveKey(ctx context.Context, key string, raw []byte, version string) (string, error) {
	if key == ai.AgentsKey && b.between != nil {
		b.once.Do(b.between)
	}
	return b.Backend.SaveKey(ctx, key, raw, version)
}

// Finding 14: when another save of the agent comes between, the audit
// record's "was" is what the save replaced, not what was read before it.
func TestTheAuditSaysWhatTheSaveReplacedReview14(t *testing.T) {
	inner := &kv.Memory{}
	store := &interposed{Backend: inner}
	var other *ai.Service
	srv, ops, _, _, _, aud := aiSetupOn(t, nil, nil, func(o *Options) {
		opts := ai.Options{Settings: o.Settings.(ai.SettingsStore), Keys: sherlock.NewKeys(nil, "", nil), DefaultURL: o.SherlockDefaultURL, Logger: quietLog()}
		opts.Backend = store
		o.AI = ai.New(opts)
		opts.Backend = inner
		other = ai.New(opts)
	})
	store.between = func() {
		if err := other.SaveAgent(context.Background(), ai.Auditor, ai.AgentChange{AgentSettings: ai.AgentSettings{Effort: auto.Auto}}); err != nil {
			t.Error(err)
		}
	}
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", `{"effort":"high"}`, 200, &o)
	if !strings.Contains(aud.String(), "effort high, model the provider's default, floor none/none (was effort auto,") {
		t.Fatalf("audit: %s", aud.String())
	}
}

// Finding 6: turning Sherlock off or on for clusters, and its view
// control, are in the audit record, as SaveAgent saves them.
func TestSherlocksClustersAreAuditedReview6(t *testing.T) {
	srv, ops, _, _, _, aud := aiSetupWith(t, nil)
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock", `{"sherlockClusters":[],"viewControl":false,"on":true}`, 200, &o)
	if !strings.Contains(aud.String(), "caps $0.00/$0.00, off on every cluster, view control false, tools []") || strings.Contains(aud.String(), ", on true") {
		t.Fatalf("off: %s", aud.String())
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock", `{"sherlockClusters":["local","nosuch"],"viewControl":true}`, 200, &o)
	if !strings.Contains(aud.String(), "on clusters [local], view control true, tools []") {
		t.Fatalf("on: %s", aud.String())
	}
}

// testRecord waits for the ai.test audit record that holds want; "" when
// none comes.
func testRecord(t *testing.T, aud *lockedBuffer, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, line := range strings.Split(aud.String(), "\n") {
			if strings.Contains(line, `"action":"ai.test"`) && strings.Contains(line, want) {
				return line
			}
		}
		if time.Now().After(deadline) {
			t.Errorf("no ai.test record with %q: %s", want, aud.String())
			return ""
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Findings 9 to 12: the audit records say what was done. A run at an
// effort or on a model of its own names what it ran at, the half not
// given as it ran (Auto off), and a run that failed at a cap names it
// too; a change between two fixed models says the model before; removing
// the classifier's provider says the classifier went off.
func TestTheAuditRecordsSayWhatWasDoneReview9to12(t *testing.T) {
	srv, ops, _, _, eng, aud := aiSetupWith(t, nil)
	var o aiOverview
	// 10: the Auditor on effort and model Auto, tested at low alone, runs
	// on the standard tier's model, Claude Sonnet 5.5.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor", `{"effort":"auto","model":"auto"}`, 200, &o)
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/test", `{"live":true,"effort":"low"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", r.StatusCode, text)
	}
	testRecord(t, aud, "(at effort low on model claude-sonnet-5-5)")
	// ... and tested on Haiku alone, at the model's own effort.
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/test", `{"live":true,"model":"claude-haiku-5-5"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", r.StatusCode, text)
	}
	testRecord(t, aud, "(at effort the model's default on model claude-haiku-5-5)")
	// 9: a run that fails at a cap still names the override.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/troubleshooter", `{"dailyUSD":0.000001}`, 200, &o)
	eng.Ledger().AddFor(context.Background(), "troubleshooter", "default", 0.01, false)
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/troubleshooter/test", `{"live":true,"effort":"high","model":"claude-haiku-5-5"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", r.StatusCode, text)
	}
	if line := testRecord(t, aud, "(at effort high on model claude-haiku-5-5)"); line != "" && !strings.Contains(line, "the test failed: ") {
		t.Errorf("the capped run: %s", line)
	}
	// 11: from one fixed model to another.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/summarizer", `{"model":"claude-sonnet-5-5","effort":"high"}`, 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/summarizer", `{"model":"claude-opus-5-5","effort":"high"}`, 200, &o)
	if !strings.Contains(aud.String(), "effort high, model claude-opus-5-5, floor none/none (was effort high, model claude-sonnet-5-5, floor none/none)") {
		t.Errorf("the model before: %s", aud.String())
	}
	// 12: removing the classifier's provider.
	body, _ := json.Marshal(map[string]any{"id": "a5000", "name": "A5000", "kind": "openai", "endpoint": "http://vllm.example/v1", "model": "qwen3.8-27b"})
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/providers", string(body), 200, &o)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/classifier", `{"provider":"a5000"}`, 200, &o)
	aiJSON(t, ops, http.MethodDelete, srv.URL+"/api/v1/ai/providers/a5000", "", 200, &o)
	if o.Classifier != "" || !strings.Contains(aud.String(), "provider a5000 removed, with its credential; Auto's classifier: a5000 → off") {
		t.Errorf("the classifier's provider removed: %q %s", o.Classifier, aud.String())
	}
}
