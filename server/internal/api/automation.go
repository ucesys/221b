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

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fleets"
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
	s.handle("GET /api/v1/automation/runs", "automation-runs", s.getAutomationRuns)
	s.handle("GET /api/v1/automation/runs/{id}", "automation-run", s.getAutomationRun)
	s.handle("POST /api/v1/automation/runs/{id}/cancel", "automation-cancel", s.postAutomationCancel)
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"automation/fleets", "automation-fleets", s.getAutomationFleets)
		s.handle("PUT "+base+"automation/fleets/{fleet}/params", "automation-params", s.putAutomationParams)
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
	case errors.Is(err, automation.ErrExists):
		return http.StatusConflict
	case errors.Is(err, automation.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, automation.ErrFull):
		return http.StatusInsufficientStorage
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
	Name      string    `json:"name"`
	Group     string    `json:"group"`
	Describe  string    `json:"describe"`
	Version   int       `json:"version"`
	State     string    `json:"state"`
	Approved  int       `json:"approved"`
	Approvals int       `json:"approvals"`
	Privilege string    `json:"privilege"`
	Errors    int       `json:"errors"`
	Warnings  int       `json:"warnings"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	LastRun   *runBrief `json:"lastRun,omitempty"`
}

func briefOf(r automation.Run) *runBrief {
	return &runBrief{ID: r.ID, State: r.State, DryRun: r.DryRun, Version: r.Version, Nodes: len(r.Nodes), Counts: r.Counts(), Started: r.Started}
}

func summaryOf(sc automation.Script, runs []automation.Run) scriptSummary {
	v := sc.Latest()
	out := scriptSummary{Name: sc.Name, Group: sc.Group, Describe: v.Header.Describe, Version: v.N, State: v.State, Privilege: v.Header.Privilege, Created: sc.Created, Updated: sc.Updated}
	if a, ok := sc.Approved(); ok {
		out.Approved, out.Approvals = a.N, len(a.Approvals)
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
	answerJSON(w, r, map[string]any{"off": s.automationOff(), "me": s.automationWho(r), "scripts": scripts, "running": running,
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
	answerJSON(w, r, sc)
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
	answerJSON(w, r, sc)
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

func (s *Server) versionAction(w http.ResponseWriter, r *http.Request, approve bool) {
	if !s.automationReady(w, r) {
		return
	}
	name := r.PathValue("name")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		jsonError(w, http.StatusBadRequest, "a version is a number")
		return
	}
	do, verb := s.opts.AutomationStore.Submit, "submitted"
	if approve {
		do, verb = s.opts.AutomationStore.Approve, "approved"
	}
	sc, err := do(r.Context(), name, n, s.automationWho(r))
	if err != nil {
		s.recordAutomation(r, audit.ActionAutomationApprove, "", name, audit.OutcomeDenied, fmt.Sprintf("v%d not %s: %v", n, verb, err))
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	v, _ := sc.Version(n)
	s.recordAutomation(r, audit.ActionAutomationApprove, "", name, audit.OutcomeOK, fmt.Sprintf("v%d %s (%d approvals), sha256 %.12s", n, verb, len(v.Approvals), v.Hash))
	answerJSON(w, r, sc)
}

func (s *Server) postAutomationSubmit(w http.ResponseWriter, r *http.Request) {
	s.versionAction(w, r, false)
}
func (s *Server) postAutomationApprove(w http.ResponseWriter, r *http.Request) {
	s.versionAction(w, r, true)
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
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	Nodes  []automationNode       `json:"nodes"`
	Params automation.FleetParams `json:"params"`
}

type automationFleets struct {
	Cluster    string            `json:"cluster"`
	Off        string            `json:"off,omitempty"`
	ReadOnly   string            `json:"readOnly,omitempty"`
	Production bool              `json:"production"`
	TwoPerson  bool              `json:"twoPerson"`
	Fleets     []automationFleet `json:"fleets"`
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
	out := automationFleets{Cluster: c.ID, Off: s.automationOff(), Production: c.Env == clusters.EnvProduction, TwoPerson: s.watsonTwoPerson(c)}
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
	params, err := s.opts.AutomationStore.Params(ctx, c.ID)
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

func (s *Server) putAutomationParams(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in automation.FleetParams
	if !decodeAutomation(w, r, &in) {
		return
	}
	list, ok := s.automationFleetList(r.Context(), w, r)
	if !ok {
		return
	}
	id := r.PathValue("fleet")
	found := false
	for _, f := range list.Fleets {
		found = found || f.ID == id
	}
	if !found {
		jsonError(w, http.StatusNotFound, "no fleet "+strconv.Quote(id)+" here")
		return
	}
	if err := s.opts.AutomationStore.SetParams(r.Context(), list.Cluster, id, in); err != nil {
		s.recordAutomation(r, audit.ActionAutomationParams, list.Cluster, "fleet "+id, audit.OutcomeFailed, err.Error())
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	names := make([]string, 0, len(in.Values))
	for k := range in.Values {
		names = append(names, k)
	}
	sort.Strings(names)
	s.recordAutomation(r, audit.ActionAutomationParams, list.Cluster, "fleet "+id, audit.OutcomeOK,
		fmt.Sprintf("values %s; %d nodes with their own; ceiling %s", strings.Join(names, ", "), len(in.Nodes), in.CeilingOrDefault()))
	params, _ := s.opts.AutomationStore.Params(r.Context(), list.Cluster)
	answerJSON(w, r, params[id])
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
}

func (s *Server) postAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	if off := s.automationOff(); off != "" {
		jsonError(w, http.StatusConflict, off)
		return
	}
	var in runRequest
	if !decodeAutomation(w, r, &in) {
		return
	}
	ctx := r.Context()
	list, ok := s.automationFleetList(ctx, w, r)
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
	case in.DryRun && in.Version > 0:
		if v, found = sc.Version(in.Version); !found {
			refuse(http.StatusNotFound, fmt.Sprintf("%s has no v%d", sc.Name, in.Version))
			return
		}
	case in.DryRun:
		v = sc.Latest()
	default:
		if v, err = automation.Runnable(sc, list.TwoPerson); err != nil {
			refuse(http.StatusConflict, err.Error())
			return
		}
	}
	if automation.HasErrors(v.Findings) {
		refuse(http.StatusConflict, fmt.Sprintf("%s v%d has errors to fix first", sc.Name, v.N))
		return
	}
	var fleet *automationFleet
	for i := range list.Fleets {
		if list.Fleets[i].ID == in.Fleet {
			fleet = &list.Fleets[i]
		}
	}
	if fleet == nil {
		refuse(http.StatusNotFound, "no fleet "+strconv.Quote(in.Fleet)+" here")
		return
	}
	if !in.DryRun && !fleet.Params.Allows(v.Header.Privilege) {
		refuse(http.StatusForbidden, fmt.Sprintf("%s declares privilege %s; fleet %s allows only %s", sc.Name, v.Header.Privilege, fleet.Name, fleet.Params.CeilingOrDefault()))
		return
	}
	if err := automation.ValidateOverrides(in.Params); err != nil {
		refuse(http.StatusBadRequest, err.Error())
		return
	}
	want := map[string]bool{}
	for _, n := range in.Nodes {
		want[n] = true
	}
	var targets, unpaired, missing []string
	values := map[string]map[string]string{}
	for _, n := range fleet.Nodes {
		if len(want) > 0 && !want[n.Name] {
			continue
		}
		delete(want, n.Name)
		if !n.Paired {
			unpaired = append(unpaired, n.Name)
			continue
		}
		res := automation.Resolve(v.Header, fleet.Params, n.Name, in.Params)
		for _, m := range res.Missing {
			missing = append(missing, n.Name+": "+m)
		}
		values[n.Name] = res.Values
		targets = append(targets, n.Name)
	}
	switch {
	case len(want) > 0:
		var names []string
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		refuse(http.StatusBadRequest, "not in fleet "+fleet.Name+": "+strings.Join(names, ", "))
		return
	case len(unpaired) > 0:
		refuse(http.StatusConflict, "not paired for SSH, so nothing can run on them: "+strings.Join(unpaired, ", ")+"; pair them, or leave them out")
		return
	case len(missing) > 0:
		refuse(http.StatusBadRequest, "required parameters have no value: "+strings.Join(missing, "; "))
		return
	case len(targets) == 0:
		refuse(http.StatusBadRequest, "fleet "+fleet.Name+" has no node to run on")
		return
	}
	clusterName := list.Cluster
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(list.Cluster); ok {
			clusterName = sum.Name
		}
	}
	run, err := s.opts.Automation.Start(ctx, automation.Spec{Script: sc.Name, Version: v, DryRun: in.DryRun, Cluster: list.Cluster, ClusterName: clusterName,
		Fleet: fleet.ID, FleetName: fleet.Name, Nodes: targets, Values: values, Overrides: in.Params, Rollout: in.Rollout, Who: s.automationWho(r)})
	if err != nil {
		refuse(automationStatus(err), err.Error())
		return
	}
	kind := "run"
	if run.DryRun {
		kind = "dry run"
	}
	s.recordAutomation(r, audit.ActionAutomationRun, list.Cluster, sc.Name, audit.OutcomeOK, fmt.Sprintf("run %d started: %s of v%d (sha256 %.12s) on fleet %s, %d nodes, privilege %s", run.ID, kind, v.N, v.Hash, fleet.Name, len(targets), v.Header.Privilege))
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
		jsonError(w, http.StatusConflict, fmt.Sprintf("run %d is not running", id))
		return
	}
	s.recordAutomation(r, audit.ActionAutomationRun, "", strconv.Itoa(id), audit.OutcomeStopped, fmt.Sprintf("run %d cancelled", id))
	answerJSON(w, r, map[string]bool{"cancelled": true})
}
