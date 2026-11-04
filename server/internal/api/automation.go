package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/watson"
)

// Automation's API (Phase 11 part 2, docs/automation.md): scripts and
// their versions, fleets' parameters, and runs on a fleet's paired nodes.
// Every route is a SuperAdmin's (AU-13), and runs on the leader.

// allNodes is the fleet of every node of a cluster, for a cluster with no
// fleets; a fleet's id never starts with an underscore.
const allNodes = "_all"

const maxAutomationBody = 64 << 10

func (s *Server) automationRoutes() {
	s.handle("GET /api/v1/automation", "automation", s.getAutomation)
	s.handle("POST /api/v1/automation/check", "automation-check", s.postAutomationCheck)
	s.handle("POST /api/v1/automation/scripts", "automation-script-create", s.postAutomationScript)
	s.handle("GET /api/v1/automation/scripts/{name}", "automation-script", s.getAutomationScript)
	s.handle("PUT /api/v1/automation/scripts/{name}", "automation-script-save", s.putAutomationScript)
	s.handle("DELETE /api/v1/automation/scripts/{name}", "automation-script-delete", s.deleteAutomationScript)
	s.handle("POST /api/v1/automation/scripts/{name}/versions/{n}/submit", "automation-submit", s.postAutomationSubmit)
	s.handle("POST /api/v1/automation/scripts/{name}/versions/{n}/approve", "automation-approve", s.postAutomationApprove)
	s.handle("POST /api/v1/automation/scripts/{name}/versions/{n}/revoke", "automation-revoke", s.postAutomationRevoke)
	s.handle("POST /api/v1/automation/scripts/{name}/versions/{n}/reject", "automation-reject", s.postAutomationReject)
	s.automationMoreRoutes()
	s.handle("POST /api/v1/automation/groups", "automation-group-create", s.postAutomationGroup)
	s.handle("PUT /api/v1/automation/groups/{name}", "automation-group-save", s.putAutomationGroup)
	s.handle("DELETE /api/v1/automation/groups/{name}", "automation-group-delete", s.deleteAutomationGroup)
	s.handle("GET /api/v1/automation/runs", "automation-runs", s.getAutomationRuns)
	s.handle("GET /api/v1/automation/runs/{id}", "automation-run", s.getAutomationRun)
	s.handle("POST /api/v1/automation/runs/{id}/cancel", "automation-cancel", s.postAutomationCancel)
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"automation/fleets", "automation-fleets", s.getAutomationFleets)
		s.handle("PUT "+base+"automation/fleets/{fleet}/variables", "automation-variables", s.putAutomationVariables)
		s.handle("POST "+base+"automation/runs", "automation-run-start", s.postAutomationRun)
	}
}

// automationReady lets a SuperAdmin through to a console with Automation.
func (s *Server) automationReady(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Automation == nil || s.opts.AutomationStore == nil {
		jsonError(w, http.StatusNotFound, "Automation is not part of this console.")
		return false
	}
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin may use Automation.")
		return false
	}
	return true
}

// automationOff says why runs are off on this console, or "".
func (s *Server) automationOff() string {
	if s.opts.SSH == nil || s.opts.Sessions == nil || !s.opts.Sessions.Config().SSH.Enabled {
		return "Automation runs over SSH, which is off on this console (toolbox.ssh.enabled)."
	}
	return ""
}

func (s *Server) automationWho(r *http.Request) automation.Who {
	w := s.watsonWho(r)
	return automation.Who{Name: w.Name, User: w.User}
}

func (s *Server) recordAutomation(r *http.Request, action, cluster, target, outcome, detail string) {
	if s.opts.Audit == nil {
		return
	}
	w := s.watsonWho(r)
	s.opts.Audit.Record(audit.Event{Action: action, Who: w.Audit, Cluster: cluster, Target: target, Outcome: outcome, Detail: detail, From: fromAddr(r)})
}

func automationStatus(err error) int {
	switch {
	case errors.Is(err, automation.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, automation.ErrExists), errors.Is(err, automation.ErrInUse), errors.Is(err, automation.ErrBusy):
		return http.StatusConflict
	case errors.Is(err, automation.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, automation.ErrFull):
		return http.StatusInsufficientStorage
	case errors.Is(err, automation.ErrOwnWork):
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// decodeAutomation reads a JSON body.
func decodeAutomation(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send it as application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAutomationBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return false
	}
	return true
}

func answerJSON(w http.ResponseWriter, r *http.Request, v any) {
	b, _ := json.Marshal(v)
	writeJSON(w, r, b)
}

// ---------- scripts ----------

type runBrief struct {
	ID      int            `json:"id"`
	State   string         `json:"state"`
	DryRun  bool           `json:"dryRun"`
	Version int            `json:"version"`
	Nodes   int            `json:"nodes"`
	Counts  map[string]int `json:"counts"`
	Started time.Time      `json:"started"`
}

type scriptSummary struct {
	// Target is kubernetes for a script that acts on the cluster.
	Target   string `json:"target,omitempty"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Describe string `json:"describe"`
	Version  int    `json:"version"`
	State    string `json:"state"`
	// InReview is the version waiting for review, or 0.
	InReview  int    `json:"inReview"`
	Approved  int    `json:"approved"`
	Approvals int    `json:"approvals"`
	Privilege string `json:"privilege"`
	DryRun    bool   `json:"dryRun"`
	// Params are what the latest and the approved version read.
	Params   []automation.Param `json:"params"`
	Errors   int                `json:"errors"`
	Warnings int                `json:"warnings"`
	Created  time.Time          `json:"created"`
	Updated  time.Time          `json:"updated"`
	LastRun  *runBrief          `json:"lastRun,omitempty"`
}

func briefOf(r automation.Run) *runBrief {
	return &runBrief{ID: r.ID, State: r.State, DryRun: r.DryRun, Version: r.Version, Nodes: len(r.Nodes), Counts: r.Counts(), Started: r.Started}
}

func summaryOf(sc automation.Script, runs []automation.Run) scriptSummary {
	v := sc.Latest()
	out := scriptSummary{Name: sc.Name, Group: sc.Group, Describe: v.Header.Describe, Version: v.N, State: v.State, Privilege: v.Header.Privilege, DryRun: v.Header.DryRun, Created: sc.Created, Updated: sc.Updated}
	for _, x := range sc.Versions {
		if x.State == automation.StateReview {
			out.InReview = x.N
		}
	}
	out.Target = v.Header.Target
	out.Params = append([]automation.Param{}, v.Header.Params...)
	if a, ok := sc.Approved(); ok {
		out.Approved, out.Approvals = a.N, len(a.Approvals)
		for _, p := range a.Header.Params {
			if !slices.ContainsFunc(out.Params, func(x automation.Param) bool { return x.Name == p.Name }) {
				out.Params = append(out.Params, p)
			}
		}
		out.Target = a.Header.Target
		if out.Describe == "" {
			out.Describe = a.Header.Describe
		}
	}
	for _, f := range v.Findings {
		if f.Level == automation.LevelError {
			out.Errors++
		} else {
			out.Warnings++
		}
	}
	for _, r := range runs {
		if r.Script == sc.Name {
			out.LastRun = briefOf(r)
			break
		}
	}
	return out
}

func (s *Server) getAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	list, err := s.opts.AutomationStore.Scripts(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs, err := s.opts.Automation.Runs(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	scripts := make([]scriptSummary, 0, len(list))
	for _, sc := range list {
		scripts = append(scripts, summaryOf(sc, runs))
	}
	running := 0
	for _, run := range runs {
		if run.State == automation.RunRunning {
			running++
		}
	}
	groups, err := s.opts.AutomationStore.Groups(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	answerJSON(w, r, map[string]any{"off": s.automationOff(), "me": s.automationWho(r), "scripts": scripts, "groups": groups, "running": running,
		"limits": map[string]int{"text": automation.MaxText, "timeout": automation.MaxTimeout, "parallel": automation.MaxParallel, "nodes": automation.MaxNodes}})
}

func (s *Server) postAutomationCheck(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	h, fs := automation.Check(automation.Normalize(in.Text))
	answerJSON(w, r, map[string]any{"header": h, "findings": fs})
}

type scriptBody struct {
	Name  string `json:"name,omitempty"`
	Group string `json:"group"`
	Text  string `json:"text"`
}

func (s *Server) saveAutomationScript(w http.ResponseWriter, r *http.Request, name string, in scriptBody, create bool) {
	sc, err := s.opts.AutomationStore.Save(r.Context(), name, in.Group, in.Text, s.automationWho(r), create)
	if err != nil {
		s.recordAutomation(r, audit.ActionAutomationScript, "", name, audit.OutcomeFailed, err.Error())
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	v := sc.Latest()
	s.recordAutomation(r, audit.ActionAutomationScript, "", name, audit.OutcomeOK, fmt.Sprintf("v%d %s, sha256 %.12s", v.N, v.State, v.Hash))
	answerJSON(w, r, s.scriptAnswer(r, sc))
}

func (s *Server) postAutomationScript(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in scriptBody
	if !decodeAutomation(w, r, &in) {
		return
	}
	s.saveAutomationScript(w, r, in.Name, in, true)
}

func (s *Server) putAutomationScript(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in scriptBody
	if !decodeAutomation(w, r, &in) {
		return
	}
	s.saveAutomationScript(w, r, r.PathValue("name"), in, false)
}

// scriptAnswer is a script with what the person asking may do with each
// version, so the page shows the console's rules rather than its own copy.
type scriptAnswer struct {
	automation.Script
	Versions []versionAnswer `json:"versions"`
}

type versionAnswer struct {
	automation.Version
	May automation.VersionMay `json:"may"`
}

func (s *Server) scriptAnswer(r *http.Request, sc automation.Script) scriptAnswer {
	who := s.automationWho(r)
	out := scriptAnswer{Script: sc, Versions: make([]versionAnswer, 0, len(sc.Versions))}
	for _, v := range sc.Versions {
		out.Versions = append(out.Versions, versionAnswer{Version: v, May: automation.May(sc, v, who)})
	}
	return out
}

func (s *Server) getAutomationScript(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	sc, ok, err := s.opts.AutomationStore.Script(r.Context(), r.PathValue("name"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		jsonError(w, http.StatusNotFound, "no script "+strconv.Quote(r.PathValue("name")))
		return
	}
	answerJSON(w, r, s.scriptAnswer(r, sc))
}

func (s *Server) deleteAutomationScript(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	name := r.PathValue("name")
	if err := s.opts.AutomationStore.Delete(r.Context(), name); err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	s.recordAutomation(r, audit.ActionAutomationScript, "", name, audit.OutcomeOK, "deleted")
	answerJSON(w, r, map[string]bool{"deleted": true})
}

func (s *Server) versionAction(w http.ResponseWriter, r *http.Request, do func(context.Context, string, int, automation.Who) (automation.Script, error), verb string) {
	if !s.automationReady(w, r) {
		return
	}
	name := r.PathValue("name")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		jsonError(w, http.StatusBadRequest, "a version is a number")
		return
	}
	sc, err := do(r.Context(), name, n, s.automationWho(r))
	if err != nil {
		s.recordAutomation(r, audit.ActionAutomationApprove, "", name, audit.OutcomeDenied, fmt.Sprintf("v%d not %s: %v", n, verb, err))
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	v, _ := sc.Version(n)
	s.recordAutomation(r, audit.ActionAutomationApprove, "", name, audit.OutcomeOK, fmt.Sprintf("v%d %s (%d approvals), sha256 %.12s", n, verb, len(v.Approvals), v.Hash))
	answerJSON(w, r, s.scriptAnswer(r, sc))
}

func (s *Server) postAutomationSubmit(w http.ResponseWriter, r *http.Request) {
	s.versionAction(w, r, s.opts.AutomationStore.Submit, "submitted")
}
func (s *Server) postAutomationApprove(w http.ResponseWriter, r *http.Request) {
	s.versionAction(w, r, s.opts.AutomationStore.Approve, "approved")
}
func (s *Server) postAutomationRevoke(w http.ResponseWriter, r *http.Request) {
	s.versionAction(w, r, s.opts.AutomationStore.Revoke, "revoked")
}
func (s *Server) postAutomationReject(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	s.versionAction(w, r, func(ctx context.Context, name string, n int, who automation.Who) (automation.Script, error) {
		return s.opts.AutomationStore.Reject(ctx, name, n, who, in.Comment)
	}, "rejected")
}

// ---------- fleets and parameters ----------

type automationNode struct {
	Name      string `json:"name"`
	Paired    bool   `json:"paired"`
	LoginUser string `json:"loginUser,omitempty"`
	// Why says why the node cannot be run on, when it cannot.
	Why string `json:"why,omitempty"`
}

type automationFleet struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	Nodes  []automationNode          `json:"nodes"`
	Params automation.FleetVariables `json:"params"`
}

type automationFleets struct {
	Cluster    string            `json:"cluster"`
	Off        string            `json:"off,omitempty"`
	ReadOnly   string            `json:"readOnly,omitempty"`
	Production bool              `json:"production"`
	Fleets     []automationFleet `json:"fleets"`
	// Conflicts are parameters a node gets from more than one fleet.
	Conflicts []automation.Conflict `json:"conflicts"`
	name      string
}

// layersOf are the fleets a node belongs to, then All nodes.
func (f automationFleets) layersOf(node string) []automation.Layer {
	var out []automation.Layer
	for _, x := range f.Fleets {
		member := x.ID == allNodes
		for _, n := range x.Nodes {
			member = member || n.Name == node
		}
		if member {
			out = append(out, automation.Layer{ID: x.ID, Name: x.Name, Params: x.Params, Default: x.ID == allNodes})
		}
	}
	return out
}

// ClusterName is the cluster's name for people.
func (f automationFleets) ClusterName() string {
	if f.name != "" {
		return f.name
	}
	return f.Cluster
}

// automationFleetList is a cluster's fleets with their nodes, whether each
// is paired for SSH, and their parameters; the last fleet is every node.
func (s *Server) automationFleetList(ctx context.Context, w http.ResponseWriter, r *http.Request) (automationFleets, bool) {
	c, ok := s.watsonCluster(w, r)
	if !ok {
		return automationFleets{}, false
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return automationFleets{}, false
	}
	out := automationFleets{Cluster: c.ID, Off: s.automationOff(), Production: c.Env == clusters.EnvProduction, name: c.Name}
	switch {
	case c.Locked != "":
		out.ReadOnly = "Changes are refused here: " + c.Locked
	case c.ReadOnly:
		out.ReadOnly = "This connection is read-only, so it runs nothing on its nodes."
	}
	var nodes []fleets.Node
	for _, o := range cl.Objects("nodes", "") {
		if n, ok := o.(*corev1.Node); ok {
			nodes = append(nodes, fleets.Node{Name: n.Name, Labels: n.Labels, Taints: n.Spec.Taints})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	paired := map[string]string{}
	stale := map[string]bool{}
	if s.opts.SSH != nil && out.Off == "" {
		fp := s.opts.SSH.Fingerprint()
		if list, err := s.opts.SSH.List(ctx, c.ID); err == nil {
			for _, p := range list {
				paired[p.Node] = p.LoginUser
				stale[p.Node] = p.CAFinger != "" && fp != "" && p.CAFinger != fp
			}
		} else {
			jsonError(w, http.StatusServiceUnavailable, "the SSH pairings could not be read: "+err.Error())
			return automationFleets{}, false
		}
	}
	params, err := s.opts.AutomationStore.Variables(ctx, c.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return automationFleets{}, false
	}
	member := func(n fleets.Node) automationNode {
		an := automationNode{Name: n.Name}
		user, ok := paired[n.Name]
		switch {
		case !ok:
			an.Why = "not paired for SSH: pair it from its Tools menu"
		case stale[n.Name]:
			an.Why = "paired with another authority: pair it again"
		default:
			an.Paired, an.LoginUser = true, user
		}
		return an
	}
	var defs []fleets.Fleet
	if s.opts.Settings != nil {
		defs = s.opts.Settings.Current().Fleets[c.ID]
	}
	for _, f := range defs {
		af := automationFleet{ID: f.ID, Name: f.Name, Nodes: []automationNode{}, Params: params[f.ID]}
		for _, n := range nodes {
			if f.Takes(n) {
				af.Nodes = append(af.Nodes, member(n))
			}
		}
		out.Fleets = append(out.Fleets, af)
	}
	all := automationFleet{ID: allNodes, Name: "All nodes", Nodes: []automationNode{}, Params: params[allNodes]}
	for _, n := range nodes {
		all.Nodes = append(all.Nodes, member(n))
	}
	out.Fleets = append(out.Fleets, all)
	layers := map[string][]automation.Layer{}
	for _, n := range nodes {
		layers[n.Name] = out.layersOf(n.Name)
	}
	out.Conflicts = automation.VariableConflicts(layers)
	return out, true
}

func (s *Server) getAutomationFleets(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	out, ok := s.automationFleetList(r.Context(), w, r)
	if !ok {
		return
	}
	answerJSON(w, r, out)
}

func (s *Server) putAutomationVariables(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in struct {
		automation.FleetVariables
		Reason string `json:"reason,omitempty"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	list, ok := s.automationFleetList(r.Context(), w, r)
	if !ok {
		return
	}
	id := r.PathValue("fleet")
	fleetName := ""
	found := false
	for _, f := range list.Fleets {
		if f.ID == id {
			found, fleetName = true, f.Name
		}
	}
	if !found {
		jsonError(w, http.StatusNotFound, "no fleet "+strconv.Quote(id)+" here")
		return
	}
	next := in.FleetVariables
	if err := automation.ValidateVariables(next); err != nil {
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	all, err := s.opts.AutomationStore.Variables(r.Context(), list.Cluster)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	before := all[id]
	changed := automation.ChangedVariables(before, next)
	impact, note := s.weighChange(r, list.Cluster, before, next, changed)
	if automation.RebootChanged(before, next) {
		changed = append(changed, "reboot timeout")
	}
	if !before.Allows(automation.PrivilegeElevated) && next.Allows(automation.PrivilegeElevated) {
		changed = append(changed, "privilege ceiling")
		impact = append(impact, automation.ImpactItem{Script: "every script of this fleet", Decision: automation.ImpactApproval, Why: "the ceiling rises to full", By: "rule"})
	}
	// Marked sensitive stays so until a second admin says otherwise, on any
	// cluster: otherwise one person could unmark it and change it alone.
	unmarked := false
	for _, name := range before.Sensitive {
		if !slices.Contains(next.Sensitive, name) {
			unmarked = true
			changed = append(changed, name+" stops being sensitive")
			impact = append(impact, automation.ImpactItem{Script: name, Decision: automation.ImpactApproval, Why: "it stops being sensitive", By: "rule"})
		}
	}
	ch := automation.VariableChange{Cluster: list.Cluster, Fleet: id, FleetName: fleetName, By: s.automationWho(r), Reason: in.Reason, Changed: changed, Before: before, After: next, Impact: impact, Note: note, State: automation.ChangeApplied}
	if ch.Changed == nil {
		ch.Changed = []string{}
	}
	// Off production a change takes effect at once, as a run there needs no
	// approval; the list still says what it touches.
	if unmarked || (list.Production && ch.NeedsApproval()) {
		ch.State = automation.ChangeWaiting
		saved, err := s.opts.AutomationStore.NewChange(r.Context(), ch)
		if err != nil {
			jsonError(w, automationStatus(err), err.Error())
			return
		}
		s.recordAutomation(r, audit.ActionAutomationParams, list.Cluster, "fleet "+id, audit.OutcomeOK, fmt.Sprintf("change %d waits for approval: %s", saved.ID, strings.Join(changed, ", ")))
		w.WriteHeader(http.StatusAccepted)
		answerJSON(w, r, variablesAnswer{Params: before, Change: &saved})
		return
	}
	if err := s.opts.AutomationStore.SetVariables(r.Context(), list.Cluster, id, next); err != nil {
		s.recordAutomation(r, audit.ActionAutomationParams, list.Cluster, "fleet "+id, audit.OutcomeFailed, err.Error())
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	var kept *automation.VariableChange
	if len(changed) > 0 {
		if saved, err := s.opts.AutomationStore.NewChange(r.Context(), ch); err == nil {
			kept = &saved
		}
	}
	names := make([]string, 0, len(next.Values))
	for k := range next.Values {
		names = append(names, k)
	}
	sort.Strings(names)
	s.recordAutomation(r, audit.ActionAutomationParams, list.Cluster, "fleet "+id, audit.OutcomeOK,
		fmt.Sprintf("values %s; %d nodes with their own; ceiling %s", strings.Join(names, ", "), len(next.Nodes), next.CeilingOrDefault()))
	params, _ := s.opts.AutomationStore.Variables(r.Context(), list.Cluster)
	answerJSON(w, r, variablesAnswer{Params: params[id], Change: kept})
}

// ---------- runs ----------

type runRequest struct {
	Script  string             `json:"script"`
	Version int                `json:"version,omitempty"`
	DryRun  bool               `json:"dryRun"`
	Fleet   string             `json:"fleet"`
	Nodes   []string           `json:"nodes,omitempty"`
	Params  map[string]string  `json:"params,omitempty"`
	Rollout automation.Rollout `json:"rollout"`
	// Config names the configuration; Hooks run around each node.
	Config string              `json:"config,omitempty"`
	Hooks  automation.HookRefs `json:"hooks"`
	// Request is the page's name for this start: sent twice, it starts
	// one run.
	Request string `json:"request,omitempty"`
}

// pickNodes is the fleet's nodes a run names, in the fleet's order, and the
// names the fleet does not have. Naming none takes the whole fleet.
func pickNodes(fleet []automationNode, names []string) (picked []automationNode, unknown []string) {
	if len(names) == 0 {
		return fleet, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, n := range fleet {
		if want[n.Name] {
			picked = append(picked, n)
			delete(want, n.Name)
		}
	}
	for n := range want {
		unknown = append(unknown, n)
	}
	sort.Strings(unknown)
	return picked, unknown
}

func (s *Server) postAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in runRequest
	if !decodeAutomation(w, r, &in) {
		return
	}
	s.startAutomationRun(w, r, in, 0)
}

// startAutomationRun checks everything a run needs and starts it: a retry
// goes through the same checks, as the world may have changed since.
func (s *Server) startAutomationRun(w http.ResponseWriter, r *http.Request, in runRequest, retryOf int) {
	ctx := r.Context()
	list, ok := s.automationFleetList(ctx, w, r)
	if !ok {
		return
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	refuse := func(code int, msg string) {
		s.recordAutomation(r, audit.ActionAutomationRun, list.Cluster, in.Script, audit.OutcomeDenied, msg)
		jsonError(w, code, msg)
	}
	if list.ReadOnly != "" {
		refuse(http.StatusConflict, list.ReadOnly)
		return
	}
	sc, found, err := s.opts.AutomationStore.Script(ctx, in.Script)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		refuse(http.StatusNotFound, "no script "+strconv.Quote(in.Script))
		return
	}
	var v automation.Version
	switch {
	case in.DryRun:
		if v, err = automation.DryRunnable(sc, in.Version); err != nil {
			refuse(automationStatus(err), err.Error())
			return
		}
	default:
		if v, err = automation.RunVersion(sc, list.Production, in.Version); err != nil {
			code := automationStatus(err)
			if code == http.StatusInternalServerError {
				code = http.StatusConflict
			}
			refuse(code, err.Error())
			return
		}
	}
	if automation.HasErrors(v.Findings) {
		refuse(http.StatusConflict, fmt.Sprintf("%s v%d has errors to fix first", sc.Name, v.N))
		return
	}
	kube8s := v.Header.Target == automation.TargetKubernetes
	// SSH reaches nodes; a script on the cluster needs none.
	if off := s.automationOff(); off != "" && (!kube8s || len(in.Hooks.Before)+len(in.Hooks.After) > 0) {
		refuse(http.StatusConflict, off)
		return
	}
	var fleet *automationFleet
	if kube8s {
		in.Fleet = clusterTarget
		fleet = &automationFleet{ID: clusterTarget, Name: "the cluster", Nodes: []automationNode{{Name: list.ClusterName(), Paired: true}}}
	} else {
		for i := range list.Fleets {
			if list.Fleets[i].ID == in.Fleet {
				fleet = &list.Fleets[i]
			}
		}
	}
	if fleet == nil {
		refuse(http.StatusNotFound, "no fleet "+strconv.Quote(in.Fleet)+" here")
		return
	}
	if !in.DryRun && list.Production && len(in.Params) > 0 {
		names := make([]string, 0, len(in.Params))
		for k := range in.Params {
			names = append(names, k)
		}
		sort.Strings(names)
		refuse(http.StatusConflict, fmt.Sprintf("On production a run takes its values from the fleets: values given for the run (%s) would skip the approval a parameter change needs here. Change them on the fleet instead.", strings.Join(names, ", ")))
		return
	}
	if !in.DryRun && !kube8s && !fleet.Params.Allows(v.Header.Privilege) {
		refuse(http.StatusForbidden, fmt.Sprintf("%s declares privilege %s; the ceiling of fleet %s is %s", sc.Name, automation.PrivilegeName(v.Header.Privilege), fleet.Name, automation.PrivilegeName(fleet.Params.CeilingOrDefault())))
		return
	}
	if !in.DryRun && list.Production {
		changes, err := s.opts.AutomationStore.Changes(ctx, list.Cluster)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if id := automation.Suspended(changes, fleet.ID, sc.Name, v.N); id != 0 {
			refuse(http.StatusConflict, fmt.Sprintf("%s v%d is suspended on %s: parameter change %d waits for approval", sc.Name, v.N, fleet.Name, id))
			return
		}
	}
	configs, err := s.opts.AutomationStore.Profiles(ctx)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	config, err := automation.ForRun(configs, in.Config, v.Header, in.DryRun, fleet.ID, fleet.Params)
	if err != nil {
		refuse(automationStatus(err), err.Error())
		return
	}
	who := s.watsonWho(r)
	identity := ""
	if kube8s || len(in.Hooks.Before)+len(in.Hooks.After) > 0 {
		identity = config.Identity()
		if identity == "" {
			identity = v.Header.RunsAs
		}
		if identity != "" && !who.Person.Nobody() {
			ns, name, _ := strings.Cut(identity, "/")
			if may, err := cl.CanIName(ctx, who.Person, "impersonate", "", "serviceaccounts", ns, name); err != nil || !may {
				refuse(http.StatusForbidden, "Kubernetes does not let you impersonate "+identity+".")
				return
			}
		}
	}
	before, err := s.resolveHooks(ctx, in.Hooks.Before)
	if err == nil {
		var after []automation.Hook
		if after, err = s.resolveHooks(ctx, in.Hooks.After); err == nil {
			// A hook may name its own identity, which takes the same right.
			for _, h := range append(append([]automation.Hook(nil), before...), after...) {
				if h.Identity == "" || who.Person.Nobody() {
					continue
				}
				ns, name, _ := strings.Cut(h.Identity, "/")
				if may, err := cl.CanIName(ctx, who.Person, "impersonate", "", "serviceaccounts", ns, name); err != nil || !may {
					refuse(http.StatusForbidden, "Kubernetes does not let you impersonate "+h.Identity+", which "+h.Label+" runs as.")
					return
				}
			}
			if err = automation.ValidateOverrides(in.Params); err == nil {
				s.launchRun(w, r, launch{in: in, sc: sc, v: v, list: list, fleet: fleet, cl: cl, who: who, identity: identity, config: config, before: before, after: after, retryOf: retryOf, refuse: refuse})
				return
			}
		}
	}
	refuse(automationStatus(err), err.Error())
}

// launch is a run that passed its checks of who and what.
type launch struct {
	in            runRequest
	sc            automation.Script
	v             automation.Version
	list          automationFleets
	fleet         *automationFleet
	cl            Cluster
	who           watson.Who
	identity      string
	config        automation.RunProfile
	before, after []automation.Hook
	retryOf       int
	refuse        func(int, string)
}

func (s *Server) launchRun(w http.ResponseWriter, r *http.Request, l launch) {
	in, v, list, fleet, refuse := l.in, l.v, l.list, l.fleet, l.refuse
	ctx := r.Context()
	picked, unknown := pickNodes(fleet.Nodes, in.Nodes)
	var targets, unpaired, missing, clash, capped, held []string
	values := map[string]map[string]string{}
	reboot := map[string]int{}
	kube8s := v.Header.Target == automation.TargetKubernetes
	// Each node answers to all its fleets, not only the one picked: the
	// strictest ceiling holds, and a change waiting on any of them holds
	// the version (on production).
	var changes []automation.VariableChange
	if !in.DryRun && !kube8s && list.Production {
		var err error
		if changes, err = s.opts.AutomationStore.Changes(ctx, list.Cluster); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	fleetName, paired := fleet.Name, 0
	for _, n := range fleet.Nodes {
		if n.Paired {
			paired++
		}
	}
	for _, n := range picked {
		if !n.Paired {
			unpaired = append(unpaired, n.Name)
			continue
		}
		layers := list.layersOf(n.Name)
		if !in.DryRun && !kube8s {
			if ceiling, from := automation.NodeCeiling(layers); !(automation.FleetVariables{Ceiling: ceiling}).Allows(v.Header.Privilege) {
				capped = append(capped, fmt.Sprintf("%s (ceiling %s, fleet %s)", n.Name, automation.PrivilegeName(ceiling), from))
			}
			for _, ly := range layers {
				if id := automation.Suspended(changes, ly.ID, l.sc.Name, v.N); id != 0 {
					held = append(held, fmt.Sprintf("%s (fleet %s: parameter change %d waits for approval)", n.Name, ly.Name, id))
					break
				}
			}
		}
		reboot[n.Name] = automation.RebootMinutes(layers, n.Name)
		res := automation.ResolveLayers(v.Header, layers, n.Name, in.Params)
		for _, m := range res.Missing {
			missing = append(missing, n.Name+": "+m)
		}
		for _, c := range res.Conflicts {
			clash = append(clash, fmt.Sprintf("%s on %s comes from fleets %s", c.Param, c.Node, strings.Join(c.Fleets, " and ")))
		}
		values[n.Name] = res.Values
		targets = append(targets, n.Name)
	}
	switch {
	case len(unknown) > 0:
		refuse(http.StatusBadRequest, "not in fleet "+fleet.Name+": "+strings.Join(unknown, ", "))
		return
	case len(unpaired) > 0:
		refuse(http.StatusConflict, "not paired for SSH, so nothing can run on them: "+strings.Join(unpaired, ", ")+"; pair them, or leave them out")
		return
	case len(capped) > 0:
		refuse(http.StatusForbidden, fmt.Sprintf("%s declares privilege %s, over the ceiling of a fleet these nodes are in: %s. Raise those fleets' ceilings, or leave the nodes out", l.sc.Name, automation.PrivilegeName(v.Header.Privilege), strings.Join(capped, "; ")))
		return
	case len(held) > 0:
		refuse(http.StatusConflict, fmt.Sprintf("%s v%d is suspended on these nodes: %s. Decide the change, or leave the nodes out", l.sc.Name, v.N, strings.Join(held, "; ")))
		return
	case len(clash) > 0:
		refuse(http.StatusConflict, "defined on more than one fleet: "+strings.Join(clash, "; ")+". Keep it on one fleet, or set it on the node or the run")
		return
	case len(missing) > 0:
		refuse(http.StatusBadRequest, "required parameters have no value: "+strings.Join(missing, "; "))
		return
	case len(targets) == 0:
		refuse(http.StatusBadRequest, "fleet "+fleet.Name+" has no node to run on")
		return
	}
	secrets, err := readSecrets(ctx, l.cl, l.who.Person, v.Header, values)
	if err != nil {
		refuse(automationStatus(err), err.Error())
		return
	}
	// Picked by hand, they are no fleet's: the dialog says so, and so does the run.
	if fleet.ID == allNodes && len(targets) < paired {
		fleetName = "custom selection"
	}
	clusterName := list.ClusterName()
	basis := "approved code, no per-run approval"
	switch {
	case in.DryRun:
		basis = "a dry run, as nobody: read-only"
	case v.State != automation.StateApproved:
		basis = fmt.Sprintf("v%d is %s: this cluster is not production, so it may run", v.N, v.State)
	}
	run, err := s.opts.Automation.Start(ctx, automation.Spec{Script: l.sc.Name, Version: v, DryRun: in.DryRun, Cluster: list.Cluster, ClusterName: clusterName,
		Fleet: fleet.ID, FleetName: fleetName, Nodes: targets, Values: values, Overrides: in.Params, Rollout: in.Rollout, Who: s.automationWho(r),
		Secrets: secrets, Config: l.config.ID, Identity: l.identity, Person: automation.Person{User: l.who.Person.User, Groups: l.who.Person.Groups}, Basis: basis,
		Before: l.before, After: l.after, HookRefs: in.Hooks, RetryOf: l.retryOf, RebootMinutes: reboot, Request: in.Request,
		Recheck: !in.DryRun && list.Production})
	if errors.Is(err, automation.ErrSameRequest) {
		// Asked twice, by a retry or a second click: the first start answers.
		answerJSON(w, r, run)
		return
	}
	if err != nil {
		refuse(automationStatus(err), err.Error())
		return
	}
	kind := "run"
	if run.DryRun {
		kind = "dry run"
	}
	detail := fmt.Sprintf("%s, %s of v%d, %s (sha256 %.12s) on %s, %d nodes, privilege %s, run profile %s", run.Watson(), kind, v.N, v.State, v.Hash, fleetName, len(targets), automation.PrivilegeName(v.Header.Privilege), l.config.Name)
	if l.identity != "" {
		detail += ", as " + l.identity
	}
	if l.retryOf != 0 {
		detail += fmt.Sprintf(", retrying run %d", l.retryOf)
	}
	s.recordAutomation(r, audit.ActionAutomationRun, list.Cluster, l.sc.Name, audit.OutcomeOK, detail)
	answerJSON(w, r, run)
}

func (s *Server) getAutomationRuns(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	runs, err := s.opts.Automation.Runs(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The list leaves each node's values and output to the run's drawer.
	for i := range runs {
		for j := range runs[i].Nodes {
			runs[i].Nodes[j].Tail, runs[i].Nodes[j].Values = "", nil
		}
	}
	answerJSON(w, r, map[string]any{"runs": runs})
}

func (s *Server) runID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		jsonError(w, http.StatusBadRequest, "a run is a number")
		return 0, false
	}
	return id, true
}

func (s *Server) getAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	run, out, found, err := s.opts.Automation.Get(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no run %d", id))
		return
	}
	answerJSON(w, r, map[string]any{"run": run, "output": out, "live": s.opts.Automation.Running(id)})
}

func (s *Server) postAutomationCancel(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	if !s.opts.Automation.Cancel(id) {
		// Run by a console that no longer leads: the record carries the
		// cancel to it, at its next beat.
		asked, err := s.opts.AutomationStore.AskCancel(r.Context(), id)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !asked {
			jsonError(w, http.StatusConflict, fmt.Sprintf("run %d is not running", id))
			return
		}
	}
	s.recordAutomation(r, audit.ActionAutomationRun, "", strconv.Itoa(id), audit.OutcomeStopped, fmt.Sprintf("run %d cancelled", id))
	answerJSON(w, r, map[string]bool{"cancelled": true})
}
