package api

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Run values on production, weighed and approved (FU-05, ADR-156) ---
//
// On production a run's own values are weighed as a change of the fleet's
// values would be, for this script and the nodes the run reaches, and for
// this run alone: the console's rule, then Sherlock, which may only ask
// for more. Values that need approval make the run a request that waits
// for a second admin (Inbox › Approvals); approving it starts the run as
// it was asked for and by whom, with every check made again.

// runWeight is what a run's own values weigh: what they change, for which
// version, and whether it needs approval. Refused says why the run would
// not start at all, for the dialog's preview.
type runWeight struct {
	NeedsApproval bool                    `json:"needsApproval"`
	Changed       []string                `json:"changed"`
	Impact        []automation.ImpactItem `json:"impact"`
	Note          string                  `json:"note,omitempty"`
	Refused       string                  `json:"refused,omitempty"`
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// weighRun weighs a run's own values on the nodes it reaches: as if each
// node's values changed from what it inherits to what the run gives it. A
// value the same as what every node inherits changes nothing; a binding
// to another variable is a change whatever the values. A value is
// sensitive when any fleet of those nodes marks it so. Sherlock is asked
// only for a real start, never for a preview.
func (s *Server) weighRun(r *http.Request, list automationFleets, sc automation.Script, v automation.Version, targets []string, run map[string]string, values map[string]map[string]string, sherlock bool) runWeight {
	before := automation.FleetVariables{Nodes: map[string]map[string]string{}}
	after := automation.FleetVariables{Nodes: map[string]map[string]string{}}
	var sensitive []string
	for _, n := range targets {
		layers := list.layersOf(n)
		b, a := map[string]string{}, map[string]string{}
		for _, ly := range layers {
			sensitive = append(sensitive, ly.Params.Sensitive...)
		}
		for name := range run {
			if _, ok := v.Header.Param(name); !ok {
				continue
			}
			if val, source, conflict := automation.Inherited(layers, n, name); conflict == nil && source != automation.SourceMissing {
				b[name] = val
			}
			if val, ok := values[n][name]; ok {
				a[name] = val
			}
		}
		before.Nodes[n], after.Nodes[n] = b, a
	}
	changed := automation.ChangedVariables(before, after)
	for name, val := range run {
		p, declared := v.Header.Param(name)
		if bound, ok := automation.Bound(val); ok && declared && !p.Secret && bound != name && !slices.Contains(changed, name) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	out := runWeight{Changed: changed, Impact: []automation.ImpactItem{}}
	if len(changed) == 0 {
		return out
	}
	one := automation.Script{Name: sc.Name, Group: sc.Group, Versions: []automation.Version{v}}
	items, note := s.weighScripts(r, list.Cluster, []automation.Script{one}, sensitive, changed, sherlock)
	if items != nil {
		out.Impact = items
	}
	out.Note = note
	for _, i := range out.Impact {
		out.NeedsApproval = out.NeedsApproval || i.Decision == automation.ImpactApproval
	}
	return out
}

// waitForApproval keeps a run whose values need approval, and says so:
// 202, with the request.
func (s *Server) waitForApproval(w http.ResponseWriter, r *http.Request, l launch, wt runWeight, targets []string, fleetName string) {
	in := l.in
	wr := automation.WaitingRun{Cluster: l.list.Cluster, ClusterName: l.list.ClusterName(), Script: l.sc.Name, Version: l.v.N, Fleet: l.fleet.ID, FleetName: fleetName,
		Nodes: in.Nodes, Targets: targets, Params: in.Params, Rollout: in.Rollout, Config: l.config.ID, Hooks: in.Hooks, RetryOf: l.retryOf, Request: in.Request,
		By: automation.Who{Name: l.who.Name, User: l.who.User}, Person: automation.Person{User: l.who.Person.User, Groups: l.who.Person.Groups},
		Changed: wt.Changed, Impact: wt.Impact, Note: wt.Note}
	saved, err := s.opts.AutomationStore.NewWaitingRun(r.Context(), wr)
	if err != nil {
		l.refuse(automationStatus(err), err.Error())
		return
	}
	var why []string
	for _, i := range wt.Impact {
		if i.Decision == automation.ImpactApproval {
			why = append(why, i.Why)
		}
	}
	s.recordAutomation(r, audit.ActionAutomationRun, l.list.Cluster, l.sc.Name, audit.OutcomeOK,
		fmt.Sprintf("run request %d waits for approval: v%d on %s with its own values for %s (%s)", saved.ID, l.v.N, fleetName, strings.Join(wt.Changed, ", "), strings.Join(why, "; ")))
	w.WriteHeader(http.StatusAccepted)
	answerJSON(w, r, map[string]any{"waiting": saved})
}

// postAutomationRunWeigh previews a run: whether its own values would wait
// for approval, and what would stop it, without starting or recording
// anything. Sherlock is not asked, so a start may still wait where the
// preview said none is needed; the note says when.
func (s *Server) postAutomationRunWeigh(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	var in runRequest
	if !decodeAutomation(w, r, &in) {
		return
	}
	s.startRun(w, r, in, 0, runMode{who: s.watsonWho(r), preview: true})
}

func (s *Server) getWaitingRuns(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	list, err := s.opts.AutomationStore.WaitingRuns(r.Context(), clusterID(r))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	answerJSON(w, r, map[string]any{"waiting": list})
}

// waitingRunMu orders approvals of waiting runs from the check to the
// record, as the twoperson store does: writes are the leader's (ADR-70).
// The run's request name stops a second start in any case.
var waitingRunMu sync.Mutex

// postWaitingRun approves or rejects a waiting run. Approving starts it as
// the person who asked for it, with the values they gave and every check
// made again; when it cannot start, it keeps waiting and the answer says
// why. Whoever asked may reject (withdraw) it, never approve it.
func (s *Server) postWaitingRun(w http.ResponseWriter, r *http.Request) {
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
	ctx, cluster, me := r.Context(), clusterID(r), s.automationWho(r)
	st := s.opts.AutomationStore
	target := fmt.Sprintf("run request %d", id)
	if decision == "reject" {
		wr, err := st.DecideWaitingRun(ctx, cluster, id, false, in.Comment, me, 0)
		if err != nil {
			s.recordAutomation(r, audit.ActionAutomationRun, cluster, target, audit.OutcomeDenied, fmt.Sprintf("not rejected: %v", err))
			jsonError(w, automationStatus(err), err.Error())
			return
		}
		s.recordAutomation(r, audit.ActionAutomationRun, cluster, wr.Script, audit.OutcomeOK, fmt.Sprintf("run request %d rejected: %s · %s", id, wr.What(), wr.Comment))
		answerJSON(w, r, map[string]any{"waiting": wr})
		return
	}
	waitingRunMu.Lock()
	defer waitingRunMu.Unlock()
	wr, err := st.WaitingRunFor(ctx, cluster, id, me)
	if err != nil {
		s.recordAutomation(r, audit.ActionAutomationRun, cluster, target, audit.OutcomeDenied, fmt.Sprintf("not approved: %v", err))
		jsonError(w, automationStatus(err), err.Error())
		return
	}
	asker := watson.Who{Name: wr.By.Name, User: wr.By.User, Person: kube.Person{User: wr.Person.User, Groups: wr.Person.Groups}}
	req := runRequest{Script: wr.Script, Version: wr.Version, Fleet: wr.Fleet, Nodes: wr.Nodes, Params: wr.Params, Rollout: wr.Rollout, Config: wr.Config, Hooks: wr.Hooks, Request: wr.RequestKey()}
	s.startRun(w, r, req, wr.RetryOf, runMode{who: asker, approved: &wr, approver: me, onStarted: func(run automation.Run) {
		decided, err := st.DecideWaitingRun(ctx, cluster, id, true, in.Comment, me, run.ID)
		if err != nil {
			// The run started; approving again finds it by its request name.
			jsonError(w, automationStatus(err), fmt.Sprintf("run %d started, but run request %d could not be marked approved: %v", run.ID, id, err))
			return
		}
		s.recordAutomation(r, audit.ActionAutomationRun, cluster, wr.Script, audit.OutcomeOK, fmt.Sprintf("run request %d approved: run %d started as %s", id, run.ID, wr.By.Name))
		answerJSON(w, r, map[string]any{"waiting": decided, "run": run})
	}})
}
