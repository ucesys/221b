package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/watson"
)

// The rest of Automation: facts, configurations, parameter changes,
// Sherlock's help, and retries.

const clusterTarget = "_cluster"

func (s *Server) automationMoreRoutes() {
	s.handle("GET /api/v1/automation/run-profiles", "automation-run-profiles", s.getAutomationProfiles)
	s.handle("PUT /api/v1/automation/run-profiles/{id}", "automation-run-profile-save", s.putAutomationProfile)
	s.handle("DELETE /api/v1/automation/run-profiles/{id}", "automation-run-profile-delete", s.deleteAutomationProfile)
	s.handle("POST /api/v1/automation/sherlock/draft", "automation-sherlock-draft", s.postAutomationDraft)
	s.handle("POST /api/v1/automation/scripts/{name}/versions/{n}/review", "automation-sherlock-review", s.postAutomationReview)
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"automation/facts", "automation-facts", s.getAutomationFacts)
		s.handle("POST "+base+"automation/facts", "automation-facts-refresh", s.postAutomationFacts)
		s.handle("GET "+base+"automation/variable-changes", "automation-variable-changes", s.getVariableChanges)
		s.handle("POST "+base+"automation/variable-changes/{id}/{decision}", "automation-variable-change-decide", s.postVariableChange)
		s.handle("POST "+base+"automation/runs/{id}/retry", "automation-retry", s.postAutomationRetry)
	}
}

// ---------- configurations ----------

func (s *Server) getAutomationProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	list, err := s.opts.AutomationStore.Profiles(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs, _ := s.opts.Automation.Runs(r.Context())
	since := time.Now().Add(-7 * 24 * time.Hour)
	count := map[string]int{}
	for _, run := range runs {
		if run.Started.After(since) {
			count[run.Config]++
		}
	}
	type row struct {
		automation.RunProfile
		Runs int `json:"runs"`
	}
	out := make([]row, 0, len(list))
	for _, c := range list {
		out = append(out, row{RunProfile: c, Runs: count[c.ID]})
	}
	answerJSON(w, r, map[string]any{"profiles": out})
}

func (s *Server) putAutomationProfile(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in automation.RunProfile
	if !decodeAutomation(w, r, &in) {
		return
	}
	in.ID = r.PathValue("id")
	if err := s.opts.AutomationStore.SetProfile(r.Context(), in); err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationConfig, "", in.ID, audit.OutcomeOK, in.Kind+" run profile saved")
	answerJSON(w, r, in)
}

func (s *Server) deleteAutomationProfile(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.AutomationStore.DeleteProfile(r.Context(), id); err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationConfig, "", id, audit.OutcomeOK, "run profile deleted")
	answerJSON(w, r, map[string]bool{"deleted": true})
}

// ---------- groups ----------

func (s *Server) postAutomationGroup(w http.ResponseWriter, r *http.Request) {
	s.saveAutomationGroup(w, r, "")
}

func (s *Server) putAutomationGroup(w http.ResponseWriter, r *http.Request) {
	s.saveAutomationGroup(w, r, r.PathValue("name"))
}

func (s *Server) saveAutomationGroup(w http.ResponseWriter, r *http.Request, old string) {
	if !s.automationReady(w, r) {
		return
	}
	var in automation.Group
	if !decodeAutomation(w, r, &in) {
		return
	}
	if err := s.opts.AutomationStore.SaveGroup(r.Context(), old, in); err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationScript, "", "group "+in.Name, audit.OutcomeOK, "group saved")
	answerJSON(w, r, in)
}

func (s *Server) deleteAutomationGroup(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	name := r.PathValue("name")
	if err := s.opts.AutomationStore.DeleteGroup(r.Context(), name); err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationScript, "", "group "+name, audit.OutcomeOK, "group deleted")
	answerJSON(w, r, map[string]bool{"deleted": true})
}

// ---------- facts ----------

func (s *Server) getAutomationFacts(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	list, err := s.opts.AutomationStore.Facts(r.Context(), clusterID(r))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	answerJSON(w, r, map[string]any{"nodes": list, "keys": automation.FactKeys})
}

// postAutomationFacts asks the fleet's paired nodes, or the ones named.
func (s *Server) postAutomationFacts(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	if off := s.automationOff(); off != "" {
		jsonError(w, http.StatusConflict, off)
		return
	}
	var in struct {
		Nodes []string `json:"nodes"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	list, ok := s.automationFleetList(r.Context(), w, r)
	if !ok {
		return
	}
	var nodes []string
	seen := map[string]bool{}
	want := map[string]bool{}
	for _, n := range in.Nodes {
		want[n] = true
	}
	for _, f := range list.Fleets {
		for _, n := range f.Nodes {
			if n.Paired && !seen[n.Name] && (len(want) == 0 || want[n.Name]) {
				seen[n.Name] = true
				nodes = append(nodes, n.Name)
			}
		}
	}
	out, err := s.opts.Automation.CollectFacts(r.Context(), list.Cluster, nodes, s.automationWho(r))
	if err != nil {
		s.recordAutomation(r, audit.ActionAutomationRun, list.Cluster, "facts", audit.OutcomeFailed, err.Error())
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	bad := 0
	for _, f := range out {
		if f.Error != "" {
			bad++
		}
	}
	s.recordAutomation(r, audit.ActionAutomationRun, list.Cluster, "facts", audit.OutcomeOK, fmt.Sprintf("Watson via the facts script: %d nodes asked, %d did not answer", len(out), bad))
	answerJSON(w, r, map[string]any{"nodes": out})
}

// ---------- parameter changes ----------

func (s *Server) getVariableChanges(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	list, err := s.opts.AutomationStore.Changes(r.Context(), clusterID(r))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []automation.VariableChange{}
	}
	answerJSON(w, r, map[string]any{"changes": list})
}

func (s *Server) postVariableChange(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	decision := r.PathValue("decision")
	if err != nil || (decision != "approve" && decision != "reject") {
		jsonError(w, http.StatusNotFound, "no such decision")
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if r.ContentLength != 0 && !decodeAutomation(w, r, &in) {
		return
	}
	cluster := clusterID(r)
	c, err := s.opts.AutomationStore.DecideChange(r.Context(), cluster, id, decision == "approve", in.Comment, s.automationWho(r))
	if err != nil {
		s.recordAutomation(r, audit.ActionAutomationParams, cluster, "fleet change", audit.OutcomeDenied, fmt.Sprintf("change %d: %v", id, err))
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationParams, cluster, "fleet "+c.Fleet, audit.OutcomeOK, fmt.Sprintf("change %d %s: %s", id, c.State, strings.Join(c.Changed, ", ")))
	answerJSON(w, r, c)
}

// variablesAnswer is what saving parameters gives back.
type variablesAnswer struct {
	Params automation.FleetVariables  `json:"params"`
	Change *automation.VariableChange `json:"change,omitempty"`
}

// weighChange lists what a change touches. The console's rule goes first;
// Sherlock may add to what needs approval, never take away.
func (s *Server) weighChange(r *http.Request, cluster string, before, after automation.FleetVariables, changed []string) ([]automation.ImpactItem, string) {
	scripts, err := s.opts.AutomationStore.Scripts(r.Context())
	if err != nil {
		return nil, "the scripts could not be read: " + err.Error()
	}
	joined := after
	joined.Sensitive = append(append([]string(nil), before.Sensitive...), after.Sensitive...)
	items := automation.Impact(scripts, joined, changed)
	safe := 0
	for _, i := range items {
		if i.Decision == automation.ImpactSafe {
			safe++
		}
	}
	if safe == 0 {
		return items, ""
	}
	if s.opts.Sherlock == nil || !s.sherlockStatusFor(r, cluster).Available {
		return items, "Sherlock did not weigh this change: it is off for this cluster."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Changed parameters: %s\n", strings.Join(changed, ", "))
	for _, i := range items {
		if i.Decision != automation.ImpactSafe {
			continue
		}
		for _, sc := range scripts {
			if v, ok := sc.Version(i.Version); ok && sc.Name == i.Script {
				fmt.Fprintf(&b, "\n--- %s v%d ---\n%s\n", i.Script, i.Version, v.Text)
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	text, err := s.opts.Sherlock.CompleteAs(ctx, s.agentID(ai.ChangePlanner), s.watsonWho(r).Audit, cluster, "weigh a parameter change", automation.ImpactPrompt, b.String())
	if err != nil {
		return items, "Sherlock could not weigh this change: " + err.Error()
	}
	var ans struct {
		Raise []struct {
			Script string `json:"script"`
			Why    string `json:"why"`
		} `json:"raise"`
	}
	if err := json.Unmarshal([]byte(jsonIn(text)), &ans); err != nil {
		return items, "Sherlock's answer about this change could not be read."
	}
	raised := map[string]string{}
	for _, x := range ans.Raise {
		raised[x.Script] = x.Why
	}
	return automation.RaiseImpact(items, raised), ""
}

func jsonIn(text string) string {
	i, j := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if i < 0 || j < i {
		return "{}"
	}
	return text[i : j+1]
}

// ---------- Sherlock on scripts ----------

var fenceRE = regexp.MustCompile("(?s)```[a-z]*\\n(.*?)```")

func (s *Server) sherlockForAutomation(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Sherlock == nil {
		jsonError(w, http.StatusConflict, "Sherlock is not part of this console.")
		return false
	}
	if st := s.sherlockStatusFor(r, clusters.LocalID); !st.Available {
		jsonError(w, http.StatusConflict, firstOf(st.Reason, "Sherlock is off: an administrator can turn it on in Settings."))
		return false
	}
	return true
}

// fixRounds bounds how often Sherlock is sent back to its own script.
const fixRounds = 3

var questionRE = regexp.MustCompile(`(?m)^QUESTION:\s*(.+)$`)

// maxNotes bounds what Sherlock says beside its script.
const maxNotes = 2000

// scriptFrom reads a script out of Sherlock's answer, what it asks, and
// what it says about the script, outside the code block.
func scriptFrom(text string) (string, []string, string) {
	var questions []string
	rest := ""
	if m := fenceRE.FindStringSubmatch(text); m != nil {
		rest = strings.Replace(text, m[0], "", 1)
		text = m[1]
	}
	for _, q := range questionRE.FindAllStringSubmatch(rest, -1) {
		questions = append(questions, strings.TrimSpace(q[1]))
	}
	notes := strings.TrimSpace(questionRE.ReplaceAllString(rest, ""))
	if len(notes) > maxNotes {
		notes = strings.ToValidUTF8(notes[:maxNotes], "") + "…"
	}
	return automation.Normalize(strings.TrimSpace(text)), questions, notes
}

// draft is what Sherlock made of a request, once the checks have had their say.
type draft struct {
	Text      string
	Header    automation.Header
	Findings  []automation.Finding
	Questions []string
	// Notes are what Sherlock said, a paragraph a round.
	Notes  []string
	Rounds int
}

func findingsText(fs []automation.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s, line %d (%s): %s\n", f.Level, f.Line, f.Code, f.Msg)
	}
	return b.String()
}

// fixDraft sends a draft back to Sherlock while the checks find something
// and it keeps changing the script. An error from the model ends it: what
// it had stands, and the person sees the findings left.
func fixDraft(answer string, ask func(string) (string, error)) draft {
	text, questions, note := scriptFrom(answer)
	var notes []string
	if note != "" {
		notes = append(notes, note)
	}
	h, fs := automation.Check(text)
	rounds := 1
	for ; rounds <= fixRounds && len(fs) > 0; rounds++ {
		next, err := ask("The console's checks found these in your script. Fix every one you can, and change nothing else. A finding you cannot fix without a person, ask about as QUESTION:.\n\n" + findingsText(fs) + "\nThe script:\n" + text)
		if err != nil {
			break
		}
		fixed, more, note := scriptFrom(next)
		if note != "" {
			notes = append(notes, note)
		}
		if fixed == text {
			questions = append(questions, more...)
			break
		}
		text, questions = fixed, more
		h, fs = automation.Check(text)
	}
	return draft{Text: text, Header: h, Findings: fs, Questions: questions, Notes: notes, Rounds: rounds}
}

// postAutomationDraft has Sherlock write or fix a script, and sends it back
// to its own work while the console's checks find something it can fix, so
// a person sees only what it could not, and what it needs them to answer.
// The draft is not saved: a person saves it.
func (s *Server) postAutomationDraft(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in struct {
		Request string `json:"request"`
		Text    string `json:"text,omitempty"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	in.Request = strings.TrimSpace(in.Request)
	if len(in.Request) > 4<<10 || (in.Request == "" && strings.TrimSpace(in.Text) == "") {
		jsonError(w, http.StatusBadRequest, "say what the script should do, in at most 4 KiB")
		return
	}
	if !s.sherlockForAutomation(w, r) {
		return
	}
	ctx := r.Context()
	who := s.watsonWho(r).Audit
	var b strings.Builder
	if in.Request != "" {
		b.WriteString("The request:\n" + in.Request + "\n")
	} else {
		b.WriteString("Fix what the checks found below, and change nothing else.\n")
	}
	if strings.TrimSpace(in.Text) != "" {
		b.WriteString("\nThe script as it stands, to change:\n" + in.Text + "\n")
		if _, fs := automation.Check(in.Text); len(fs) > 0 {
			b.WriteString("\nWhat the console's checks found in it:\n" + findingsText(fs))
		}
	}
	if facts := s.factsSummary(ctx); facts != "" {
		b.WriteString("\nThe nodes it may run on:\n" + facts)
	}
	answer, err := s.opts.Sherlock.CompleteAs(ctx, s.agentID(ai.AutomationAuthor), who, clusters.LocalID, "write a script", automation.DraftPrompt, b.String())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	d := fixDraft(answer, func(ask string) (string, error) {
		return s.opts.Sherlock.CompleteAs(ctx, s.agentID(ai.AutomationAuthor), who, clusters.LocalID, "fix a script", automation.DraftPrompt, ask)
	})
	out := map[string]any{"text": d.Text, "header": d.Header, "findings": d.Findings, "questions": d.Questions, "notes": d.Notes, "rounds": d.Rounds}
	// The Automation author's draft always goes to the Script reviewer,
	// when it is on, before anyone sees it (Phase 12).
	reviewed := ""
	if s.opts.AI != nil && s.opts.AI.Usable(ai.ScriptReviewer, "") == "" && strings.TrimSpace(d.Text) != "" {
		if text, err := s.opts.Sherlock.CompleteAs(ctx, ai.ScriptReviewer, who, clusters.LocalID, "review a draft", automation.ReviewPrompt, d.Text); err == nil {
			var rev automation.Review
			if err := json.Unmarshal([]byte(jsonIn(text)), &rev); err != nil || rev.Summary == "" {
				rev = automation.Review{Summary: strings.TrimSpace(text)}
			}
			out["review"] = rev
			reviewed = fmt.Sprintf("; the Script reviewer found %d", len(rev.Findings))
		}
	}
	s.recordAutomation(r, audit.ActionAutomationScript, "", "draft", audit.OutcomeOK, fmt.Sprintf("Sherlock drafted a script in %d rounds, %d findings left%s; nothing is saved until a person saves it", d.Rounds, len(d.Findings), reviewed))
	answerJSON(w, r, out)
}

// agentID is the agent that answers a model call (Phase 12): "" is
// Sherlock, the only one without the agents.
func (s *Server) agentID(id string) string {
	if s.opts.AI == nil {
		return ""
	}
	return id
}

// factsSummary says what kinds of node there are, for a draft to fit.
func (s *Server) factsSummary(ctx context.Context) string {
	list, err := s.opts.AutomationStore.Facts(ctx, clusters.LocalID)
	if err != nil {
		return ""
	}
	kinds := map[string]int{}
	for _, f := range list {
		if f.Error == "" {
			kinds[fmt.Sprintf("%s (%s, %s)", f.Facts["os.name"], f.Facts["os.family"], f.Facts["pkg.manager"])]++
		}
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, fmt.Sprintf("%d × %s", kinds[k], k))
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}

func (s *Server) postAutomationReview(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	name := r.PathValue("name")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		jsonError(w, http.StatusBadRequest, "a version is a number")
		return
	}
	if !s.sherlockForAutomation(w, r) {
		return
	}
	sc, found, err := s.opts.AutomationStore.Script(r.Context(), name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, ok := sc.Version(n)
	if !found || !ok {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no %s v%d", name, n))
		return
	}
	text, err := s.opts.Sherlock.CompleteAs(r.Context(), s.agentID(ai.ScriptReviewer), s.watsonWho(r).Audit, clusters.LocalID, "review a script", automation.ReviewPrompt, v.Text)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	var rev automation.Review
	if err := json.Unmarshal([]byte(jsonIn(text)), &rev); err != nil || rev.Summary == "" {
		rev = automation.Review{Summary: strings.TrimSpace(text)}
	}
	sc, err = s.opts.AutomationStore.SetReview(r.Context(), name, n, rev)
	if err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationScript, "", name, audit.OutcomeOK, fmt.Sprintf("Sherlock reviewed v%d: %d findings; a review never approves", n, len(rev.Findings)))
	answerJSON(w, r, s.scriptAnswer(r, sc))
}

// ---------- runs, continued ----------

func (s *Server) postAutomationRetry(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	run, _, found, err := s.opts.Automation.Get(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found || run.Cluster != clusterID(r) {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no run %d here", id))
		return
	}
	if s.opts.Automation.Running(id) {
		jsonError(w, http.StatusConflict, fmt.Sprintf("run %d is still running", id))
		return
	}
	var nodes []string
	for _, n := range run.Nodes {
		if n.State == automation.NodeFailed || n.State == automation.NodeUnreachable {
			nodes = append(nodes, n.Node)
		}
	}
	if len(nodes) == 0 {
		jsonError(w, http.StatusConflict, fmt.Sprintf("run %d has no failed node to try again", id))
		return
	}
	s.startAutomationRun(w, r, runRequest{Script: run.Script, Version: run.Version, DryRun: run.DryRun, Fleet: run.Fleet, Nodes: nodes, Params: run.Overrides,
		Rollout: run.Rollout, Config: run.Config, Hooks: run.HookRefs}, id)
}

// resolveHooks turns a run's hook names into what the engine runs.
func (s *Server) resolveHooks(ctx context.Context, refs []string) ([]automation.Hook, error) {
	var out []automation.Hook
	for _, ref := range refs {
		switch {
		case ref == "drain":
			out = append(out, automation.DrainHook())
		case ref == "uncordon":
			out = append(out, automation.UncordonHook())
		case strings.HasPrefix(ref, "script:"):
			name := strings.TrimPrefix(ref, "script:")
			sc, found, err := s.opts.AutomationStore.Script(ctx, name)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("%w: no script %s", automation.ErrInvalid, name)
			}
			v, err := automation.Runnable(sc)
			if err != nil {
				return nil, fmt.Errorf("%w: a hook runs approved code: %v", automation.ErrInvalid, err)
			}
			if v.Header.Target != automation.TargetKubernetes {
				return nil, fmt.Errorf("%w: %s targets nodes; a hook is a Kubernetes script", automation.ErrInvalid, name)
			}
			h := automation.HookOf(name, v)
			h.Identity = v.Header.RunsAs
			out = append(out, h)
		default:
			return nil, fmt.Errorf("%w: no hook %q", automation.ErrInvalid, ref)
		}
	}
	return out, nil
}

// secretRE is a secret parameter's value: where to read it, never it.
var secretRE = regexp.MustCompile(`^secret:([a-z0-9][-a-z0-9.]*)/([a-z0-9][-a-z0-9.]*)/([-._a-zA-Z0-9]+)$`)

// readSecrets reads a run's secret parameters as the person who starts it,
// so Kubernetes decides. The values stay in memory for the run. An error
// never holds a value.
func readSecrets(ctx context.Context, cl Cluster, p kube.Person, h automation.Header, values map[string]map[string]string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	cache := map[string]string{}
	for _, param := range h.Params {
		if !param.Secret {
			continue
		}
		for node, vals := range values {
			ref, ok := vals[param.Name]
			if !ok {
				continue
			}
			m := secretRE.FindStringSubmatch(ref)
			if m == nil {
				return nil, fmt.Errorf("%w: %s takes a Secret, as secret:namespace/name/key", automation.ErrInvalid, param.Name)
			}
			val, ok := cache[ref]
			if !ok {
				sec, err := cl.GetSecret(ctx, p, m[1], m[2])
				if err != nil {
					return nil, fmt.Errorf("%s: the Secret %s/%s could not be read as you", param.Name, m[1], m[2])
				}
				b, found := sec.Data[m[3]]
				if !found {
					return nil, fmt.Errorf("%w: %s: the Secret %s/%s has no key %s", automation.ErrInvalid, param.Name, m[1], m[2], m[3])
				}
				val = string(b)
				cache[ref] = val
			}
			if out[node] == nil {
				out[node] = map[string]string{}
			}
			out[node][param.Name] = val
		}
	}
	return out, nil
}

// automationKube runs Kubernetes scripts and hooks through Watson.
type automationKube struct{ s *Server }

func (k automationKube) RunLine(ctx context.Context, cluster string, p automation.Person, identity, line string, dryRun bool, out io.Writer) (int, error) {
	s := k.s
	if s.opts.Watson == nil {
		return 1, errors.New("Watson is not part of this console")
	}
	c := watson.Cluster{ID: cluster, Name: cluster}
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(cluster); ok {
			c.Name, c.Env, c.ReadOnly = sum.Name, sum.Env, sum.Mode != clusters.ModeWrite
		}
		if _, client, ok := s.opts.Clusters.Lookup(cluster); ok && client != nil {
			c.Config = client.RestConfig()
		}
	}
	if c.Config == nil {
		return 1, fmt.Errorf("the connection to %s is not running", cluster)
	}
	c.Locked = s.RoleRefusal(cluster)
	person := kube.Person{User: p.User, Groups: p.Groups}
	if identity != "" {
		ns, name, _ := strings.Cut(identity, "/")
		person = kube.Person{User: "system:serviceaccount:" + ns + ":" + name}
	}
	return s.opts.Watson.RunLine(ctx, c, person, line, dryRun, out)
}
