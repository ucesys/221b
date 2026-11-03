package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/watson"
)

// approvalRow is one thing that waits, or waited, for an approval: a
// script version, a variable change or a Watson card. Approved by and on
// name whoever decided it, as State says how.
type approvalRow struct {
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	What        string     `json:"what"`
	Cluster     string     `json:"cluster,omitempty"`
	ClusterName string     `json:"clusterName,omitempty"`
	RequestedBy string     `json:"requestedBy"`
	RequestedAt *time.Time `json:"requestedAt,omitempty"`
	ApprovedBy  string     `json:"approvedBy,omitempty"`
	ApprovedAt  *time.Time `json:"approvedAt,omitempty"`
	Script      string     `json:"script,omitempty"`
	Version     int        `json:"version,omitempty"`
	Fleet       string     `json:"fleet,omitempty"`
	ID          string     `json:"id,omitempty"`
	// Comment is the reason given with a rejection.
	Comment string `json:"comment,omitempty"`
	// Mine says someone else decided what this person asked for, which
	// the gear flags until they open Approvals.
	Mine bool `json:"mine,omitempty"`
	// Own says this person asked for what waits: a Watson card of theirs
	// they may approve themselves, which is no news to them.
	Own bool `json:"own,omitempty"`
}

// The kinds and states of an approval row.
const (
	approvalScript   = "script"
	approvalChange   = "change"
	approvalCommand  = "command"
	approvalPending  = "pending"
	approvalApproved = "approved"
	approvalRejected = "rejected"
	approvalRevoked  = "revoked"
	// approvalsKept is how many decided rows the page lists.
	approvalsKept = 200
)

func timeRef(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// watsonClusters are the clusters Watson can reach, for what spans them all.
func (s *Server) watsonClusters() []watson.Cluster {
	if s.opts.Clusters == nil {
		return nil
	}
	var out []watson.Cluster
	for _, sum := range s.opts.Clusters.List() {
		c := watson.Cluster{ID: sum.ID, Name: sum.Name, Env: sum.Env, ReadOnly: sum.Mode != clusters.ModeWrite, Locked: s.RoleRefusal(sum.ID)}
		if _, client, ok := s.opts.Clusters.Lookup(sum.ID); ok && client != nil {
			c.Config = client.RestConfig()
		}
		out = append(out, c)
	}
	return out
}

// getApprovals lists everything waiting for this person to approve, across
// clusters: script versions, variable changes and Watson's cards; what
// they asked for themselves stays out, where a second person is needed.
// Below them, newest first, what was approved, rejected or revoked.
func (s *Server) getApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin sees what waits for approval.")
		return
	}
	me := s.automationWho(r)
	var pending, decided []approvalRow
	names := map[string]string{}
	wc := s.watsonClusters()
	for _, c := range wc {
		names[c.ID] = c.Name
	}
	if st := s.opts.AutomationStore; st != nil {
		mayApprove := map[string]bool{}
		waiting, err := st.ToApprove(r.Context(), me)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, p := range waiting {
			mayApprove[fmt.Sprintf("%s/%d", p.Script, p.Version)] = true
		}
		scripts, err := st.Scripts(r.Context())
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, sc := range scripts {
			for _, v := range sc.Versions {
				row := approvalRow{Kind: approvalScript, What: fmt.Sprintf("%s v%d", sc.Name, v.N), RequestedBy: v.Author.Name, Script: sc.Name, Version: v.N}
				asked := v.Author
				if v.Submitted != nil {
					row.RequestedBy, row.RequestedAt = v.Submitted.By.Name, timeRef(v.Submitted.At)
					asked = v.Submitted.By
				}
				// Whoever approves or rejects never wrote it; a revoke may be one's own.
				askedMe := me.Same(asked)
				switch {
				case v.State == automation.StateReview:
					if mayApprove[fmt.Sprintf("%s/%d", sc.Name, v.N)] {
						row.State = approvalPending
						pending = append(pending, row)
					}
				case v.State == automation.StateRejected && v.Rejected != nil:
					row.State, row.ApprovedBy, row.ApprovedAt, row.Comment = approvalRejected, v.Rejected.By.Name, timeRef(v.Rejected.At), v.Rejected.Comment
					row.Mine = askedMe
					decided = append(decided, row)
				case len(v.Approvals) > 0:
					by := make([]string, len(v.Approvals))
					for i, a := range v.Approvals {
						by[i] = a.By.Name
					}
					row.State, row.ApprovedBy, row.ApprovedAt = approvalApproved, strings.Join(by, ", "), timeRef(v.Approvals[0].At)
					row.Mine = askedMe
					decided = append(decided, row)
				case len(v.Revoked) > 0:
					last := v.Revoked[len(v.Revoked)-1]
					row.State, row.ApprovedBy, row.ApprovedAt = approvalRevoked, last.By.Name, timeRef(last.At)
					row.Mine = askedMe && !me.Same(last.By)
					decided = append(decided, row)
				}
			}
		}
		changes, err := st.AllChanges(r.Context())
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, c := range changes {
			fleet := c.FleetName
			if fleet == "" {
				fleet = c.Fleet
			}
			what := fleet + ": " + strings.Join(c.Changed, ", ")
			if c.Reason != "" {
				what += " · " + c.Reason
			}
			row := approvalRow{Kind: approvalChange, What: what, Cluster: c.Cluster, ClusterName: names[c.Cluster], RequestedBy: c.By.Name, RequestedAt: timeRef(c.At), Fleet: c.Fleet, ID: strconv.Itoa(c.ID)}
			if row.ClusterName == "" {
				row.ClusterName = c.Cluster
			}
			switch c.State {
			case automation.ChangeWaiting:
				if !me.Same(c.By) {
					row.State = approvalPending
					pending = append(pending, row)
				}
			case automation.ChangeApproved, automation.ChangeRejected:
				row.State = approvalApproved
				if c.State == automation.ChangeRejected {
					row.State = approvalRejected
				}
				if c.DecidedBy != nil {
					row.ApprovedBy = c.DecidedBy.Name
					row.Mine = me.Same(c.By) && !me.Same(*c.DecidedBy)
				}
				row.ApprovedAt, row.Comment = c.DecidedAt, c.Comment
				decided = append(decided, row)
			}
		}
	}
	if s.opts.Watson != nil {
		who := s.watsonWho(r)
		for _, c := range wc {
			for _, it := range s.opts.Watson.Items(c, who) {
				if !it.Change {
					continue
				}
				row := approvalRow{Kind: approvalCommand, What: it.Line, Cluster: c.ID, ClusterName: c.Name, RequestedBy: it.Requester.Name, RequestedAt: timeRef(it.Created), ID: it.ID}
				mine := who.User != "" && it.Requester.User == who.User
				switch {
				case it.Status == watson.StatusPending:
					if !mine || !s.opts.Watson.NeedsTwo(c, it) {
						row.State, row.Own = approvalPending, mine
						pending = append(pending, row)
					}
				case it.Approver != nil:
					row.State, row.ApprovedBy, row.ApprovedAt = approvalApproved, it.Approver.Name, timeRef(it.Approved)
					row.Mine = mine && it.Approver.User != who.User
					decided = append(decided, row)
				}
			}
		}
	}
	at := func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	}
	sort.SliceStable(pending, func(i, j int) bool { return at(pending[i].RequestedAt).Before(at(pending[j].RequestedAt)) })
	sort.SliceStable(decided, func(i, j int) bool { return at(decided[i].ApprovedAt).After(at(decided[j].ApprovedAt)) })
	if len(decided) > approvalsKept {
		decided = decided[:approvalsKept]
	}
	answerJSON(w, r, struct {
		Items []approvalRow `json:"items"`
	}{Items: append(append([]approvalRow{}, pending...), decided...)})
}
