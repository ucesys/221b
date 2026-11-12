package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// The AI agents (Phase 12): Administration › Settings › AI, for
// SuperAdmins. Providers, agents, their instructions on the way from a
// draft to live, test cases and pilots. Sherlock's settings endpoints from
// before stay, over the same data.

func (s *Server) aiRoutes() {
	s.handle("GET /api/v1/ai", "ai", s.getAI)
	s.handle("PUT /api/v1/ai/limits", "ai-limits", s.putAILimits)
	s.handle("POST /api/v1/ai/providers", "ai-provider-add", s.postAIProvider)
	s.handle("PUT /api/v1/ai/providers/{id}", "ai-provider-update", s.putAIProvider)
	s.handle("DELETE /api/v1/ai/providers/{id}", "ai-provider-remove", s.deleteAIProvider)
	s.handle("POST /api/v1/ai/providers/{id}/default", "ai-provider-default", s.postAIProviderDefault)
	s.handle("PUT /api/v1/ai/providers/{id}/key", "ai-provider-key", s.putAIProviderKey)
	s.handle("DELETE /api/v1/ai/providers/{id}/key", "ai-provider-key", s.deleteAIProviderKey)
	s.handle("POST /api/v1/ai/providers/{id}/test", "ai-provider-test", s.postAIProviderTest)
	// --- AI settings, round 2 (Phase 12, wave 5) ---
	s.handle("POST /api/v1/ai/providers/try", "ai-provider-try", s.postAIProviderTry)
	s.handle("GET /api/v1/ai/providers/{id}/models", "ai-provider-models", s.getAIProviderModels)
	s.handle("POST /api/v1/ai/providers/models", "ai-provider-models", s.postAIProviderModels)
	s.handle("POST /api/v1/ai/agents/{id}/draft/back", "ai-draft", s.postAIBackToDraft)
	s.handle("PUT /api/v1/ai/agents/{id}", "ai-agent-update", s.putAIAgent)
	s.handle("GET /api/v1/ai/agents/{id}/instructions", "ai-instructions", s.getAIInstructions)
	s.handle("GET /api/v1/ai/agents/{id}/prompt", "ai-prompt", s.getAIPrompt)
	s.handle("PUT /api/v1/ai/agents/{id}/draft", "ai-draft", s.putAIDraft)
	s.handle("DELETE /api/v1/ai/agents/{id}/draft", "ai-draft", s.deleteAIDraft)
	s.handle("POST /api/v1/ai/agents/{id}/baseline", "ai-baseline", s.postAIBaseline)
	s.handle("POST /api/v1/ai/agents/{id}/test", "ai-test", s.postAITest)
	s.handle("GET /api/v1/ai/agents/{id}/tests", "ai-tests", s.getAITests)
	s.handle("POST /api/v1/ai/agents/{id}/pilot", "ai-pilot", s.postAIPilot)
	s.handle("DELETE /api/v1/ai/agents/{id}/pilot", "ai-pilot", s.deleteAIPilot)
	s.handle("GET /api/v1/ai/agents/{id}/pilot", "ai-pilot-log", s.getAIPilot)
	s.handle("POST /api/v1/ai/agents/{id}/live", "ai-live", s.postAILive)
	s.handle("POST /api/v1/ai/agents/{id}/versions/{n}/restore", "ai-restore", s.postAIRestore)
	s.handle("POST /api/v1/ai/pilot/{n}/rate", "ai-pilot-rate", s.postAIRate)
	s.handle("POST /api/v1/ai/tests", "ai-test-save", s.postAICase)
	s.handle("DELETE /api/v1/ai/tests/{id}", "ai-test-remove", s.deleteAICase)
}

// aiAdmin lets a SuperAdmin through.
func (s *Server) aiAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin may change the AI settings.")
		return false
	}
	return true
}

// aiBy names the person for the records.
func aiBy(r *http.Request) string {
	sess, _ := auth.SessionFrom(r.Context())
	return firstOf(sess.User.Email, sess.User.Username, sess.User.Name, "someone")
}

// aiFail answers an error of package ai.
func (s *Server) aiFail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var capErr *sherlock.ErrCap
	switch {
	case errors.Is(err, ai.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ai.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, ai.ErrSameProvider):
		code = http.StatusConflict
	case errors.As(err, &capErr):
		code = http.StatusTooManyRequests
	case errors.Is(err, sherlock.ErrFixed):
		code = http.StatusConflict
	default:
		s.opts.Logger.Warn("an AI change failed", "err", err)
	}
	jsonError(w, code, err.Error())
}

// aiOverview is Settings › AI: the overview and the clusters to pick.
type aiOverview struct {
	ai.Overview
	Clusters []sherlockCluster `json:"clusters"`
	// Running counts Sherlock's answers in progress.
	Running int `json:"running"`
	// Knowledge says where Mycroft's sources stand.
	Knowledge string `json:"knowledge"`
}

func (s *Server) getAI(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	o := aiOverview{Overview: s.opts.AI.View(r.Context()), Knowledge: s.knowledgeSummary()}
	if s.opts.Sherlock != nil {
		o.Running = s.opts.Sherlock.Running()
	}
	if s.opts.Clusters != nil {
		for _, c := range s.opts.Clusters.List() {
			o.Clusters = append(o.Clusters, sherlockCluster{ID: c.ID, Name: c.Name, Env: string(c.Env)})
		}
	} else {
		o.Clusters = []sherlockCluster{{ID: clusters.LocalID, Name: clusters.LocalID}}
	}
	sort.SliceStable(o.Clusters, func(i, j int) bool {
		return o.Clusters[i].ID == clusters.LocalID && o.Clusters[j].ID != clusters.LocalID
	})
	answerJSON(w, r, o)
}

func (s *Server) putAILimits(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	var in struct {
		DailyUSD   float64 `json:"dailyUSD"`
		MonthlyUSD float64 `json:"monthlyUSD"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	before := s.opts.AI.View(r.Context())
	if err := s.opts.AI.SetLimits(r.Context(), in.DailyUSD, in.MonthlyUSD); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAISettings, "limits", fmt.Sprintf("overall limits $%.2f/$%.2f → $%.2f/$%.2f a day/month", before.DailyUSD, before.MonthlyUSD, in.DailyUSD, in.MonthlyUSD))
	s.getAI(w, r)
}

// ---------- providers ----------

func (s *Server) postAIProvider(w http.ResponseWriter, r *http.Request) { s.saveAIProvider(w, r, true) }

func (s *Server) putAIProvider(w http.ResponseWriter, r *http.Request) { s.saveAIProvider(w, r, false) }

func (s *Server) saveAIProvider(w http.ResponseWriter, r *http.Request, adding bool) {
	if !s.aiAdmin(w, r) {
		return
	}
	var p ai.Provider
	if !decodeAutomation(w, r, &p) {
		return
	}
	if !adding {
		p.ID = r.PathValue("id")
	}
	if err := s.opts.AI.SaveProvider(r.Context(), p, adding); err != nil {
		s.aiFail(w, err)
		return
	}
	verb := "changed"
	if adding {
		verb = "added"
	}
	s.record(r, audit.ActionAISettings, "provider "+p.ID, fmt.Sprintf("provider %s %s: %s", p.ID, verb, providerText(&p.Provider)))
	s.getAI(w, r)
}

func (s *Server) deleteAIProvider(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AI.DeleteProvider(r.Context(), id); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAISettings, "provider "+id, "provider "+id+" removed, with its credential")
	s.getAI(w, r)
}

// postAIProviderDefault makes a provider the one agents that name none
// use (KI-2).
func (s *Server) postAIProviderDefault(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AI.MakeDefault(r.Context(), id); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAISettings, "provider "+id, "provider "+id+" made the default: agents that name no provider use it")
	s.getAI(w, r)
}

func (s *Server) putAIProviderKey(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	p, ok := s.opts.AI.ProviderOf(r.Context(), id)
	if !ok {
		jsonError(w, http.StatusNotFound, "There is no such provider.")
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	authKind := p.Provider.Normalized(s.opts.SherlockDefaultURL).Auth
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.opts.AI.KeysFor(id).SetFor(ctx, authKind, in.Key, aiBy(r)); err != nil {
		s.recordOutcome(r, audit.ActionAIKey, "provider "+id, "the credential was not saved: "+err.Error(), audit.OutcomeFailed)
		if errors.Is(err, sherlock.ErrFixed) || strings.Contains(err.Error(), "does not look like") || strings.Contains(err.Error(), "paste") || strings.Contains(err.Error(), "service account") {
			s.aiFail(w, fmt.Errorf("%w: %v", ai.ErrInvalid, err))
			return
		}
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIKey, "provider "+id, "a new credential was saved for the "+authKind+" authentication")
	s.getAI(w, r)
}

func (s *Server) deleteAIProviderKey(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.opts.AI.ProviderOf(r.Context(), id); !ok {
		jsonError(w, http.StatusNotFound, "There is no such provider.")
		return
	}
	if err := s.opts.AI.KeysFor(id).Delete(r.Context()); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIKey, "provider "+id, "the credential was removed")
	s.getAI(w, r)
}

// postAIProviderTest sends a provider one short request with its model, or
// the one given, and says how it answered: the connection test of
// Sherlock's settings, for any provider. It counts against the overall
// limits.
func (s *Server) postAIProviderTest(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	p, ok := s.opts.AI.ProviderOf(r.Context(), id)
	if !ok {
		jsonError(w, http.StatusNotFound, "There is no such provider.")
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if r.ContentLength != 0 && !decodeAutomation(w, r, &in) {
		return
	}
	model := firstOf(in.Model, p.Model, sherlock.DefaultModel)
	out, err := s.providerTest(r.Context(), s.opts.AI.Client(id), model, p)
	// The result is kept: a provider that answered without a credential
	// may then be used without one (AS-04).
	if rerr := s.opts.AI.RecordTest(context.Background(), id, model, err); rerr != nil {
		s.opts.Logger.Warn("keeping a provider's test failed", "provider", id, "err", rerr)
	}
	out["keyless"] = !s.opts.AI.KeysFor(id).Info().Set
	if err != nil {
		s.recordOutcome(r, audit.ActionAISettings, "provider "+id, "connection test with "+model+" failed: "+err.Error(), audit.OutcomeFailed)
	} else {
		s.record(r, audit.ActionAISettings, "provider "+id, "connection test with "+model+" answered")
	}
	answerJSON(w, r, out)
}

// providerTest sends a provider one short request and says how it
// answered. It counts against the overall limits.
func (s *Server) providerTest(ctx context.Context, c *sherlock.Client, model string, p ai.Provider) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	req := sherlock.Request{Model: model, MaxTokens: 256, System: []sherlock.Block{{Type: "text", Text: "This is a connection test from 221B. Answer in one short sentence."}},
		Messages: []sherlock.Message{{Role: "user", Content: []sherlock.Block{{Type: "text", Text: "Say that you can hear the console."}}}}}
	resp, err := c.Stream(ctx, req, func(sherlock.Delta) {})
	if s.opts.Sherlock != nil {
		price := sherlock.Settings{Model: model, PriceIn: p.PriceIn, PriceOut: p.PriceOut}.Price()
		s.opts.Sherlock.Ledger().Add(context.Background(), price.Cost(resp.Usage), false)
		_ = s.opts.Sherlock.Ledger().Save(context.Background())
	}
	out := map[string]any{"model": model, "ms": time.Since(start).Milliseconds(), "ok": err == nil}
	if err != nil {
		out["error"] = sherlock.Explain(err)
		return out, err
	}
	var b strings.Builder
	for _, bl := range resp.Content {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	out["answer"] = truncateText(b.String(), 300)
	return out, nil
}

// aiTry is provider settings from the provider dialog, not saved yet:
// the provider, the saved one it edits (whose credential is sent only to
// its own endpoint), and the model to test.
type aiTry struct {
	Provider ai.Provider `json:"provider"`
	ID       string      `json:"id"`
	Model    string      `json:"model"`
}

// tryClient is the client of the dialog's settings, and whether the saved
// credential goes with them.
func (s *Server) tryClient(w http.ResponseWriter, r *http.Request, in aiTry) (*sherlock.Client, bool, bool) {
	p := in.Provider.Provider
	if err := p.Validate(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return nil, false, false
	}
	keyOf := ""
	if in.ID != "" {
		if _, ok := s.opts.AI.ProviderOf(r.Context(), in.ID); !ok {
			jsonError(w, http.StatusNotFound, "There is no such provider.")
			return nil, false, false
		}
		if s.opts.AI.SameEndpoint(r.Context(), in.ID, p) && s.opts.AI.KeysFor(in.ID).Info().Set {
			keyOf = in.ID
		}
	}
	return s.opts.AI.TryClient(p, keyOf), keyOf != "", true
}

// postAIProviderTry tests the provider dialog's settings before they are
// saved (AS-04). Nothing is kept but the audit record; the saved
// credential goes only to the saved endpoint.
func (s *Server) postAIProviderTry(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	var in aiTry
	if !decodeAutomation(w, r, &in) {
		return
	}
	c, withKey, ok := s.tryClient(w, r, in)
	if !ok {
		return
	}
	model := firstOf(in.Model, in.Provider.Model, sherlock.DefaultModel)
	out, err := s.providerTest(r.Context(), c, model, in.Provider)
	out["keyless"] = !withKey
	target := "provider " + firstOf(in.ID, "(not saved)")
	if err != nil {
		s.recordOutcome(r, audit.ActionAISettings, target, "connection test of unsaved settings with "+model+" failed: "+err.Error(), audit.OutcomeFailed)
	} else {
		s.record(r, audit.ActionAISettings, target, "connection test of unsaved settings with "+model+" answered")
	}
	answerJSON(w, r, out)
}

// getAIProviderModels lists the models a saved provider's endpoint offers
// (AS-05).
func (s *Server) getAIProviderModels(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := s.opts.AI.ListModels(ctx, r.PathValue("id"))
	s.answerModels(w, r, list, err)
}

// postAIProviderModels lists the models of the provider dialog's
// settings before they are saved.
func (s *Server) postAIProviderModels(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	var in aiTry
	if !decodeAutomation(w, r, &in) {
		return
	}
	c, _, ok := s.tryClient(w, r, in)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := c.Models(ctx)
	s.answerModels(w, r, list, err)
}

// answerModels answers a models list; a provider that could not list
// them is said in error, with the list empty, since the model can still
// be typed.
func (s *Server) answerModels(w http.ResponseWriter, r *http.Request, list []sherlock.ModelInfo, err error) {
	if errors.Is(err, ai.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "There is no such provider.")
		return
	}
	out := map[string]any{"models": list}
	if list == nil {
		out["models"] = []sherlock.ModelInfo{}
	}
	if err != nil {
		out["error"] = modelsError(err)
	}
	answerJSON(w, r, out)
}

// modelsError says why the models could not be listed.
func modelsError(err error) string {
	var ae *sherlock.APIError
	switch {
	case errors.Is(err, sherlock.ErrNoModelList):
		return err.Error() + "."
	case errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden):
		return "The endpoint refused to list its models without a credential it accepts: save the credential, or type the model's name."
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		return "The endpoint lists no models at that address: type the model's name."
	case errors.As(err, &ae):
		return "Listing the models failed: " + ae.Error()
	}
	return "The endpoint could not be reached to list its models: " + err.Error()
}

// answerJSONBody writes v once the status is written.
func answerJSONBody(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	w.Write(b)
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- agents ----------

func (s *Server) putAIAgent(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	var in ai.AgentChange
	if !decodeAutomation(w, r, &in) {
		return
	}
	if id == ai.Sherlock && in.SherlockClusters != nil && s.opts.Clusters != nil {
		var keep []string
		for _, c := range in.SherlockClusters {
			if _, ok := s.opts.Clusters.Summary(c); ok {
				keep = append(keep, c)
			}
		}
		in.SherlockClusters = append([]string{}, keep...)
	}
	if err := s.opts.AI.SaveAgent(r.Context(), id, in); err != nil {
		s.aiFail(w, err)
		return
	}
	d := fmt.Sprintf("agent %s: provider %s, model %s, caps $%.2f/$%.2f, clusters [%s], tools [%s]", id, firstOf(in.Provider, ai.DefaultProvider), firstOf(in.Model, "the provider's default"), in.DailyUSD, in.MonthlyUSD, strings.Join(in.Clusters, ", "), strings.Join(in.Tools, ", "))
	if in.On != nil {
		d += fmt.Sprintf(", on %v", *in.On)
	}
	if in.ConfirmSingleProvider {
		d += "; confirmed a single provider"
	}
	s.record(r, audit.ActionAISettings, "agent "+id, d)
	s.getAI(w, r)
}

func (s *Server) getAIInstructions(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	v, err := s.opts.AI.Instructions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	answerJSON(w, r, v)
}

// getAIPrompt shows the whole prompt an agent gets for a cluster
// (?cluster=, the local one without), with the draft (?draft=1) or the
// instructions in force.
func (s *Server) getAIPrompt(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	cluster := firstOf(r.URL.Query().Get("cluster"), clusters.LocalID)
	r.SetPathValue("cluster", cluster)
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var a sherlock.Agent
	var err error
	if r.URL.Query().Get("draft") == "1" {
		a, err = s.opts.AI.PreviewDraft(r.Context(), id, cluster)
	} else {
		a, err = s.opts.AI.Preview(r.Context(), id, cluster, nil, nil)
	}
	if err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	name, version := cluster, ""
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(cluster); ok {
			name, version = sum.Name, sum.Version
		}
	}
	core, block := sherlock.AgentPromptText(a, sherlock.Question{Key: sherlock.Key{Cluster: cluster}, Cluster: name, Version: version, Platform: s.platformPrompt(r, cl, name, version)}, s.opts.Now())
	answerJSON(w, r, map[string]any{"instructions": core, "cluster": block, "model": a.Model, "tools": a.Tools, "compact": a.Compact})
}

func (s *Server) putAIDraft(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	var in struct {
		House string            `json:"house"`
		Notes map[string]string `json:"notes"`
		// Parts, when given, are the edits of the built-in text (Phase
		// 12, wave 5): absent keeps those in force.
		Parts *map[string]string `json:"parts"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	var parts map[string]string
	if in.Parts != nil {
		parts = *in.Parts
	}
	warnings, err := s.opts.AI.SaveDraftParts(r.Context(), id, in.House, in.Notes, parts, in.Parts != nil, aiBy(r))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	v, _ := s.opts.AI.Instructions(r.Context(), id)
	edited := []string{}
	for _, p := range v.Parts {
		if p.Draft != nil && strings.TrimSpace(*p.Draft) != strings.TrimSpace(p.BuiltIn) {
			edited = append(edited, p.Name)
		}
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, fmt.Sprintf("draft saved: %d words of house instructions, notes for %d clusters, built-in text edited [%s]", ai.Words(in.House), len(in.Notes), strings.Join(edited, ", ")))
	v.Warnings = warnings
	answerJSON(w, r, v)
}

func (s *Server) deleteAIDraft(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AI.DiscardDraft(r.Context(), id); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, "draft discarded")
	s.getAIInstructions(w, r)
}

// postAIBackToDraft returns the draft to its first stage (Phase 12, wave
// 5): its pilot ends and it is tested again.
func (s *Server) postAIBackToDraft(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AI.BackToDraft(r.Context(), id); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, "draft back to its first stage: its pilot ended, its test no longer counts")
	s.getAIInstructions(w, r)
}

func (s *Server) postAIBaseline(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	var in struct {
		Action string `json:"action"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	if err := s.opts.AI.ResolveBaseline(r.Context(), id, in.Action, aiBy(r)); err != nil {
		s.aiFail(w, err)
		return
	}
	what := map[string]string{ai.KeepEdit: "kept the edit over the new baseline", ai.TakeBaseline: "took the chart's baseline"}[in.Action]
	s.record(r, audit.ActionAIPrompt, "agent "+id, what)
	s.getAIInstructions(w, r)
}

// postAITest says what a test of the draft (or of the instructions in
// force) would cost, with estimate, or starts it; it runs in the
// background and its result lands on the draft.
func (s *Server) postAITest(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	var in struct {
		Replays  int  `json:"replays"`
		Estimate bool `json:"estimate"`
		Live     bool `json:"live"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	o := ai.TestOptions{Draft: !in.Live, Replays: min(max(in.Replays, 0), ai.MaxReplays), By: aiBy(r), Who: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}}
	usd, cases, replays, err := s.opts.AI.Estimate(r.Context(), id, o)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	out := map[string]any{"estimateUSD": usd, "cases": cases, "replays": replays}
	if in.Estimate {
		answerJSON(w, r, out)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		run, err := s.opts.AI.RunTests(ctx, id, o)
		detail := fmt.Sprintf("tested: %d of %d cases passed, %d replays (%d the same), $%.4f", run.Passed, run.Cases, len(run.Replays), run.Same, run.CostUSD)
		outcome := audit.OutcomeOK
		if err != nil {
			detail, outcome = "the test failed: "+err.Error(), audit.OutcomeFailed
		}
		if s.opts.Audit != nil {
			s.opts.Audit.Record(audit.Event{Action: audit.ActionAITest, Who: o.Who, Target: "agent " + id, Detail: detail, Outcome: outcome})
		}
	}()
	out["started"] = true
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	answerJSONBody(w, out)
}

func (s *Server) getAITests(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	cases, err := s.opts.AI.TestsOf(r.Context(), r.PathValue("id"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	if cases == nil {
		cases = []ai.TestCase{}
	}
	answerJSON(w, r, map[string]any{"cases": cases})
}

func (s *Server) postAIPilot(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	var in struct {
		People   []string `json:"people"`
		Clusters []string `json:"clusters"`
		Hours    int      `json:"hours"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	if err := s.opts.AI.StartPilot(r.Context(), id, in.People, in.Clusters, time.Duration(in.Hours)*time.Hour, aiBy(r)); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, fmt.Sprintf("pilot started for %d hours: people [%s], clusters [%s]", in.Hours, strings.Join(in.People, ", "), strings.Join(in.Clusters, ", ")))
	s.getAIInstructions(w, r)
}

func (s *Server) deleteAIPilot(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AI.StopPilot(r.Context(), id); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, "pilot stopped")
	s.getAIInstructions(w, r)
}

func (s *Server) getAIPilot(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	list, err := s.opts.AI.Pilot(r.Context(), r.PathValue("id"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	answerJSON(w, r, map[string]any{"entries": list})
}

func (s *Server) postAIRate(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "no such answer")
		return
	}
	var in struct {
		Rating int `json:"rating"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	if err := s.opts.AI.Rate(r.Context(), n, in.Rating, aiBy(r)); err != nil {
		s.aiFail(w, err)
		return
	}
	answerJSON(w, r, map[string]bool{"rated": true})
}

// postAILive puts the draft live: at once when no other SuperAdmin could
// approve it, else as a request on Administration › Approvals that
// another SuperAdmin decides (twoperson.KindPrompt).
func (s *Server) postAILive(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	info, ok := ai.InfoOf(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "There is no such agent.")
		return
	}
	rev, err := s.opts.AI.ReadyForLive(r.Context(), id)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	if a := s.twoPersonAuth(); a != nil && a.OtherSuperAdmin(sess) {
		req, err := s.opts.TwoPerson.Add(r.Context(), twoperson.Request{Kind: twoperson.KindPrompt, Agent: id, AgentName: info.Name, Rev: rev, By: auth.WhoOf(sess)})
		if err != nil {
			code := http.StatusConflict
			if errors.Is(err, twoperson.ErrInvalid) {
				code = http.StatusBadRequest
			}
			jsonError(w, code, err.Error())
			return
		}
		_ = s.opts.AI.SetRequest(r.Context(), id, rev, req.ID)
		s.writeAudit(r, "", audit.OutcomeOK, ActionTwoPerson, strconv.Itoa(req.ID), "waits for another SuperAdmin: "+req.What())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		answerJSONBody(w, map[string]any{"waiting": true, "request": req})
		return
	}
	v, err := s.opts.AI.ApplyLive(r.Context(), id, rev, aiBy(r))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, fmt.Sprintf("draft %d made live as version %d; no other SuperAdmin could approve it", rev, v.N))
	answerJSON(w, r, map[string]any{"live": v})
}

// applyPrompt puts approved instructions live (Administration › Approvals).
func (s *Server) applyPrompt(ctx context.Context, req twoperson.Request, by string) error {
	if s.opts.AI == nil {
		return errors.New("this console has no agents")
	}
	_, err := s.opts.AI.ApplyLive(ctx, req.Agent, req.Rev, by)
	return err
}

func (s *Server) postAIRestore(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "no such version")
		return
	}
	if err := s.opts.AI.Restore(r.Context(), id, n, aiBy(r)); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAIPrompt, "agent "+id, fmt.Sprintf("version %d restored as the draft", n))
	s.postAILive(w, r)
}

// postAICase saves a test case: one given whole, or one made from an
// answer of the person's own conversation (cluster and message), its tool
// results scrubbed again.
func (s *Server) postAICase(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	var in struct {
		Cluster string       `json:"cluster"`
		Message int          `json:"message"`
		Agent   string       `json:"agent"`
		Name    string       `json:"name"`
		Points  []string     `json:"points"`
		Case    *ai.TestCase `json:"case"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	var c ai.TestCase
	switch {
	case in.Case != nil:
		c = *in.Case
	case s.opts.Sherlock != nil && in.Message > 0:
		sess, _ := auth.SessionFrom(r.Context())
		t, ok := s.opts.Sherlock.TraceOf(sherlock.Key{Session: sess.ID, Cluster: firstOf(in.Cluster, clusters.LocalID)}, in.Message)
		if !ok {
			jsonError(w, http.StatusNotFound, "That answer is not in your conversation any more.")
			return
		}
		c = ai.FromTrace(t, in.Name, in.Points)
		if in.Agent != "" {
			c.Agent = in.Agent
		}
	default:
		jsonError(w, http.StatusBadRequest, "Name an answer of your conversation, or give the case.")
		return
	}
	saved, err := s.opts.AI.SaveTest(r.Context(), c, aiBy(r))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAITest, "agent "+saved.Agent, fmt.Sprintf("test case %s saved: %q, %d recorded results", saved.ID, saved.Name, len(saved.Recorded)))
	answerJSON(w, r, saved)
}

func (s *Server) deleteAICase(w http.ResponseWriter, r *http.Request) {
	if !s.aiAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AI.DeleteTest(r.Context(), id); err != nil {
		s.aiFail(w, err)
		return
	}
	s.record(r, audit.ActionAITest, "test "+id, "test case deleted")
	answerJSON(w, r, map[string]bool{"deleted": true})
}
