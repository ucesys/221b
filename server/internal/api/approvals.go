package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/watson"
)

// approvalRow is one thing that waits, or waited, for an approval: a
// script version, a variable change, a run with its own values or a
// Watson card. Approved by and on
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
	// they may approve themselves, a request of theirs to withdraw, or
	// what they cannot decide at all (WaitingFor), which is no news to them.
	Own bool `json:"own,omitempty"`
	// WaitingFor says, on a row of the person's own that they can neither
	// approve nor reject, who decides it: "another SuperAdmin", "a second
	// person". Such a row is there to be read, and counts among what is
	// pending in their Inbox like any other row (IN-11).
	WaitingFor string `json:"waitingFor,omitempty"`
	// Overtaken says why a waiting change can only be rejected now.
	Overtaken string `json:"overtaken,omitempty"`
	// Run is the run an approved run request started.
	Run int `json:"run,omitempty"`
	// Request is what a console change asks for, for its drawer to show
	// (AI-35).
	Request *adminRequest `json:"request,omitempty"`
}

// adminRequest is what a console change that waits for a second
// SuperAdmin asks for (AI-35): an environment's from and to, the user made
// a SuperAdmin and how, or the agent and the draft that goes live, tested
// or not.
type adminRequest struct {
	Kind      string   `json:"kind"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	User      string   `json:"user,omitempty"`
	Super     bool     `json:"super,omitempty"`
	Groups    []string `json:"groups,omitempty"`
	Agent     string   `json:"agent,omitempty"`
	AgentName string   `json:"agentName,omitempty"`
	Rev       int      `json:"rev,omitempty"`
	Untested  string   `json:"untested,omitempty"`
	Withdrawn bool     `json:"withdrawn,omitempty"`
}

// The kinds and states of an approval row.
const (
	approvalScript  = "script"
	approvalChange  = "change"
	approvalCommand = "command"
	// approvalRun is a run whose own values wait on production (FU-05).
	approvalRun      = "run"
	approvalPending  = "pending"
	approvalApproved = "approved"
	approvalRejected = "rejected"
	approvalRevoked  = "revoked"
	// approvalExpired is a run request nobody decided in time (FU-05a).
	approvalExpired = "expired"
	// approvalsKept is how many decided rows the page lists.
	approvalsKept = 200
	// Who decides what a person asked for and cannot decide themselves:
	// Automation is a SuperAdmin's alone, so a script's version and a
	// variable change wait for another one, as a session card does (TB-07);
	// a Watson command under the two-person rule waits for anyone else who
	// may see the cluster.
	waitsSuperAdmin = "another SuperAdmin"
	waitsSecond     = "a second person"
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
		// The configuration and the role verdict of one connection, read
		// once (RB-36).
		run := s.running(sum.ID)
		c := watson.Cluster{ID: sum.ID, Name: sum.Name, Env: sum.Env, ReadOnly: sum.Mode != clusters.ModeWrite, Locked: s.refusalOf(sum.ID, run), Config: run.cfg}
		out = append(out, c)
	}
	return out
}

// getApprovals lists everything waiting for this person to approve, across
// clusters: script versions, variable changes and Watson's cards. What
// they asked for themselves, where a second person is needed, is listed
// too, to them alone, as their own (Own) with who decides it (WaitingFor):
// a row to read, with nothing to decide. Below them, newest first, what
// was approved, rejected or revoked.
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
					switch {
					case mayApprove[fmt.Sprintf("%s/%d", sc.Name, v.N)]:
						row.State = approvalPending
						pending = append(pending, row)
					case v.Involved(me):
						// Theirs: written, edited or submitted by them, so
						// it waits for someone else.
						row.State, row.Own, row.WaitingFor = approvalPending, true, waitsSuperAdmin
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
			row := approvalRow{Kind: approvalChange, What: what, Cluster: c.Cluster, ClusterName: names[c.Cluster], RequestedBy: c.By.Name, RequestedAt: timeRef(c.At), Fleet: c.Fleet, ID: strconv.Itoa(c.ID), Overtaken: c.Overtaken}
			if row.ClusterName == "" {
				row.ClusterName = c.Cluster
			}
			switch c.State {
			case automation.ChangeWaiting:
				row.State = approvalPending
				if me.Same(c.By) {
					// Whoever asked cannot approve it; it is rejected, or
					// taken back, on the fleet's Variables.
					row.Own, row.WaitingFor = true, waitsSuperAdmin
				}
				pending = append(pending, row)
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
		// Runs whose own values wait (FU-05): whoever asked sees theirs, to
		// withdraw it; another admin approves it.
		s.expireWaitingRuns(r)
		runs, err := st.WaitingRuns(r.Context(), "")
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, q := range runs {
			row := approvalRow{Kind: approvalRun, What: q.What(), Cluster: q.Cluster, ClusterName: names[q.Cluster], RequestedBy: q.By.Name, RequestedAt: timeRef(q.At), Script: q.Script, Version: q.Version, Fleet: q.Fleet, ID: strconv.Itoa(q.ID), Run: q.Run}
			if row.ClusterName == "" {
				row.ClusterName = q.Cluster
			}
			switch q.State {
			case automation.ChangeWaiting:
				row.State, row.Own = approvalPending, me.Same(q.By)
				pending = append(pending, row)
			default:
				row.State = approvalApproved
				switch q.State {
				case automation.ChangeRejected:
					row.State = approvalRejected
				case automation.ChangeExpired:
					row.State = approvalExpired
				}
				if q.DecidedBy != nil {
					row.ApprovedBy = q.DecidedBy.Name
					row.Mine = me.Same(q.By) && !me.Same(*q.DecidedBy)
				}
				row.ApprovedAt, row.Comment = q.DecidedAt, q.Comment
				decided = append(decided, row)
			}
		}
	}
	if s.opts.Watson != nil {
		who := s.watsonWho(r)
		for _, c := range wc {
			listed := s.opts.Watson.Listed(c, who)
			for i := range listed {
				it := &listed[i]
				if !it.Change {
					continue
				}
				row := approvalRow{Kind: approvalCommand, What: it.Line, Cluster: c.ID, ClusterName: c.Name, RequestedBy: it.Requester.Name, RequestedAt: timeRef(it.Created), ID: it.ID}
				mine := who.User != "" && it.Requester.User == who.User
				switch {
				case it.Status == watson.StatusPending:
					row.State, row.Own = approvalPending, mine
					if mine && s.opts.Watson.NeedsTwo(c, it) {
						// A session card is a SuperAdmin's to approve
						// (TB-07); any other change anyone else's.
						row.WaitingFor = waitsSecond
						if it.Session != nil {
							row.WaitingFor = waitsSuperAdmin
						}
					}
					pending = append(pending, row)
				case it.Approver != nil:
					row.State, row.ApprovedBy, row.ApprovedAt = approvalApproved, it.Approver.Name, timeRef(it.Approved)
					row.Mine = mine && it.Approver.User != who.User
					decided = append(decided, row)
				}
			}
		}
	}
	sess, _ := auth.SessionFrom(r.Context())
	reqPending, reqDecided, err := s.twoPersonRows(r.Context(), sess)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pending, decided = append(pending, reqPending...), append(decided, reqDecided...)
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
