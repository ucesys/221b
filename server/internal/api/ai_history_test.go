package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// setLocalEnv sets the environment of the console's own cluster.
func setLocalEnv(t *testing.T, srv *httptest.Server, c *http.Client, env string) {
	t.Helper()
	body := `{"name":"Here","env":"` + env + `","description":"","server":"https://10.0.0.1:6443","ca":"system","auth":"incluster","mode":"read"}`
	if r, text := send(t, c, http.MethodPut, srv.URL+"/api/v1/clusters/local", body); r.StatusCode != http.StatusOK {
		t.Fatalf("the local cluster to %s: %d %s", env, r.StatusCode, text)
	}
}

// The drawer's History (AI-32): every draft saved since the last went
// live, whole, beside the versions once live; a discarded draft stays,
// marked, and a draft that goes live drops them all but itself, now its
// version. Only a SuperAdmin reads it.
func TestAIHistoryAPI(t *testing.T) {
	srv, ops, _, _, _, _ := aiSetupWith(t, nil)
	viewer := signIn(t, srv, "viewer@corp.example")
	base := srv.URL + "/api/v1/ai/agents/auditor/history"
	var h struct {
		Entries []ai.HistoryEntry `json:"entries"`
	}
	h.Entries = nil
	aiJSON(t, ops, http.MethodGet, base, "", 200, &h)
	if len(h.Entries) != 1 || h.Entries[0].Kind != ai.HistoryLive || h.Entries[0].N != 0 || !h.Entries[0].InForce {
		t.Fatalf("before any draft: %+v", h.Entries)
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI first.","notes":{"local":"Sundays off."}}`, 200, nil)
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI first, then SOC 2."}`, 200, nil)
	h.Entries = nil
	aiJSON(t, ops, http.MethodGet, base, "", 200, &h)
	if len(h.Entries) != 3 || h.Entries[0].Rev != 2 || !h.Entries[0].Current || h.Entries[1].Rev != 1 || h.Entries[1].Kind != ai.HistoryDraft || h.Entries[1].Current {
		t.Fatalf("two drafts: %+v", h.Entries)
	}
	var one ai.HistoryText
	aiJSON(t, ops, http.MethodGet, base+"/draft/1", "", 200, &one)
	if one.House != "Weigh PCI first." || one.Notes["local"] != "Sundays off." || len(one.Parts) == 0 || one.Parts[0].Text == "" {
		t.Fatalf("draft 1: %+v", one)
	}
	if r, _ := send(t, ops, http.MethodGet, base+"/draft/9", ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a draft never saved: %d", r.StatusCode)
	}
	if r, _ := send(t, ops, http.MethodGet, base+"/sideways/1", ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a kind that is none: %d", r.StatusCode)
	}
	if r, _ := send(t, viewer, http.MethodGet, base, ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer read the history: %d", r.StatusCode)
	}
	if r, _ := send(t, viewer, http.MethodGet, base+"/draft/1", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer read a draft: %d", r.StatusCode)
	}
	// Discarded, the drafts stay to compare, marked.
	aiJSON(t, ops, http.MethodDelete, srv.URL+"/api/v1/ai/agents/auditor/draft", "", 200, nil)
	h.Entries = nil
	aiJSON(t, ops, http.MethodGet, base, "", 200, &h)
	if len(h.Entries) != 3 || h.Entries[0].Kind != ai.HistoryDiscarded || h.Entries[1].Kind != ai.HistoryDiscarded || h.Entries[0].Current {
		t.Fatalf("discarded: %+v", h.Entries)
	}
	// Live: the drafts go, the one that went live stays as its version.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh SOC 2 first."}`, 200, nil)
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/live", `{"untested":true,"reason":"hotfix"}`, 200, nil)
	h.Entries = nil
	aiJSON(t, ops, http.MethodGet, base, "", 200, &h)
	if len(h.Entries) != 1 || h.Entries[0].Kind != ai.HistoryLive || h.Entries[0].N != 1 || h.Entries[0].Rev != 3 || !h.Entries[0].InForce || !strings.HasPrefix(h.Entries[0].Note, "made live untested: hotfix") {
		t.Fatalf("after going live: %+v", h.Entries)
	}
	one = ai.HistoryText{}
	aiJSON(t, ops, http.MethodGet, base+"/draft/3", "", 200, &one)
	if one.Kind != ai.HistoryLive || one.N != 1 || one.House != "Weigh SOC 2 first." {
		t.Fatalf("draft 3 is version 1: %+v", one)
	}
}

// Untested on a console whose own cluster is not production, a draft goes
// live at once, with no second approval: its version and the audit log
// say why (AI-34). On production, or with the environment unknown, it
// waits as before; a tested draft waits wherever the console runs.
func TestAIUntestedOffProductionNeedsNoSecondApproval(t *testing.T) {
	srv, ops, _, aiSvc, _, aud := aiSetupWith(t, nil)
	_ = signIn(t, srv, "ops2@corp.example")
	live := srv.URL + "/api/v1/ai/agents/auditor/live"
	for _, env := range []string{"staging", "development", "other"} {
		setLocalEnv(t, srv, ops, env)
		aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI first on `+env+`."}`, 200, nil)
		var out struct {
			Live    *ai.Version `json:"live"`
			Waiting bool        `json:"waiting"`
		}
		aiJSON(t, ops, http.MethodPost, live, `{"untested":true,"reason":"hotfix"}`, 200, &out)
		if out.Waiting || out.Live == nil || !strings.Contains(out.Live.Why, "made live untested: hotfix; no second approval") {
			t.Fatalf("%s: %+v", env, out)
		}
	}
	if !strings.Contains(aud.String(), "no second approval needed: the console's own cluster is not production") {
		t.Fatalf("not recorded:\n%s", aud.String())
	}
	// A tested draft still waits off production.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI first, tested."}`, 200, nil)
	if _, err := aiSvc.RunTests(t.Context(), ai.Auditor, ai.TestOptions{Draft: true}); err != nil {
		t.Fatal(err)
	}
	aiJSON(t, ops, http.MethodPost, live, "", http.StatusAccepted, nil)
	// On production, untested waits too.
	srv2, ops3, _, _, _, _ := aiSetupWith(t, nil)
	_ = signIn(t, srv2, "ops2@corp.example")
	setLocalEnv(t, srv2, ops3, "production")
	aiJSON(t, ops3, http.MethodPut, srv2.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI first."}`, 200, nil)
	aiJSON(t, ops3, http.MethodPost, srv2.URL+"/api/v1/ai/agents/auditor/live", `{"untested":true,"reason":"hotfix"}`, http.StatusAccepted, nil)
}

// A go-live request on Approvals carries what it asks for, for its drawer
// to show (AI-35): the agent, the draft and why it goes untested.
func TestAIApprovalRowCarriesTheRequest(t *testing.T) {
	srv, ops, _, _, _, _ := aiSetupWith(t, nil)
	ops2 := signIn(t, srv, "ops2@corp.example")
	setLocalEnv(t, srv, ops, "production")
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI first."}`, 200, nil)
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/live", `{"untested":true,"reason":"hotfix"}`, http.StatusAccepted, nil)
	var list struct {
		Items []approvalRow `json:"items"`
	}
	aiJSON(t, ops2, http.MethodGet, srv.URL+"/api/v1/admin/approvals", "", 200, &list)
	var row *approvalRow
	for i := range list.Items {
		if list.Items[i].Kind == approvalAdmin {
			row = &list.Items[i]
		}
	}
	if row == nil || row.Request == nil || row.Request.Kind != twoperson.KindPrompt || row.Request.Agent != ai.Auditor || row.Request.Rev != 1 || row.Request.Untested != "hotfix" {
		t.Fatalf("the row: %+v", row)
	}
}

// The pilot's ratings are gone (AI-29): their route answers nothing.
func TestAIPilotRatingIsGone(t *testing.T) {
	srv, ops, _, _, _, _ := aiSetupWith(t, nil)
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/pilot/1/rate", `{"rating":1}`); r.StatusCode == http.StatusOK {
		t.Fatalf("rated: %d", r.StatusCode)
	}
}
