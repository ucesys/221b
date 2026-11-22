package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/ucesys/221b/server/internal/automation"
)

// Run History, one page of every run (AU-24, DB-37): the runs, and the
// requests for a run whose own values wait for approval, which share the
// runs' table for this: waiting, rejected, withdrawn by whoever asked, or
// expired. A request approved is the run it started, which names it
// (valuesApproved); one approved whose run is no longer kept is listed as
// approved.

// The states a request has in the history.
const (
	historyWaiting   = "waiting"
	historyRejected  = "rejected"
	historyWithdrawn = "withdrawn"
	historyExpired   = "expired"
	historyApproved  = "approved"
)

// historyEntry is one row of Run History: a run or a request, its state,
// and when it started or was asked for.
type historyEntry struct {
	Kind    string                 `json:"kind"`
	State   string                 `json:"state"`
	At      time.Time              `json:"at"`
	Run     *automation.Run        `json:"run,omitempty"`
	Request *automation.WaitingRun `json:"request,omitempty"`
}

// requestState is a request's state in the history: a rejection by the
// person who asked is a withdrawal.
func requestState(w automation.WaitingRun) string {
	if w.State == historyRejected && w.DecidedBy != nil && w.DecidedBy.Same(w.By) {
		return historyWithdrawn
	}
	return w.State
}

// getAutomationHistory lists the runs and the run requests together,
// newest first; ?state= keeps one state (a run's, or a request's:
// waiting, rejected, withdrawn, expired, approved).
func (s *Server) getAutomationHistory(w http.ResponseWriter, r *http.Request) {
	if !s.automationReady(w, r) {
		return
	}
	s.expireWaitingRuns(r)
	runs, err := s.opts.Automation.Runs(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	requests, err := s.opts.AutomationStore.WaitingRuns(r.Context(), "")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	want := r.URL.Query().Get("state")
	started := map[int]bool{}
	out := []historyEntry{}
	for i := range runs {
		run := &runs[i]
		if run.ValuesApproved != nil {
			started[run.ValuesApproved.Request] = true
		}
		// The list leaves each node's values and output to the run's drawer.
		for j := range run.Nodes {
			run.Nodes[j].Tail, run.Nodes[j].Values = "", nil
		}
		if want == "" || want == run.State {
			out = append(out, historyEntry{Kind: "run", State: run.State, At: run.Started, Run: run})
		}
	}
	for i := range requests {
		req := &requests[i]
		state := requestState(*req)
		if state == historyApproved && (started[req.ID] || runKept(runs, req.Run)) {
			continue
		}
		if want == "" || want == state {
			out = append(out, historyEntry{Kind: "request", State: state, At: req.At, Request: req})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	answerJSON(w, r, map[string]any{"entries": out})
}

// runKept says whether a run is in the list; 0 is none.
func runKept(runs []automation.Run, id int) bool {
	for _, r := range runs {
		if id != 0 && r.ID == id {
			return true
		}
	}
	return false
}
