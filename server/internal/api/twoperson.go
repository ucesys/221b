package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// Two people for what would let one person loosen what needs two (review
// item 14): a cluster's environment taken off production, and a local
// user made a SuperAdmin, wait for another SuperAdmin when there is one.
// They are listed and decided on Administration › Approvals.

// ActionTwoPerson records a request and its decision.
const ActionTwoPerson = "two-person"

// approvalAdmin is a request's row on Approvals.
const approvalAdmin = "admin"

// twoPersonAuth is what the sign-in side does for requests;
// *auth.Service implements it.
type twoPersonAuth interface {
	OtherSuperAdmin(auth.Session) bool
	SuperAdminHold(sess auth.Session, before auth.Account, super []roles.ID, groups []string) auth.SuperChange
	RequestSuperAdmin(ctx context.Context, r *http.Request, sess auth.Session, hold twoperson.Request) (twoperson.Request, error)
	ApplySuperAdmin(ctx context.Context, r twoperson.Request) error
}

func (s *Server) twoPersonAuth() twoPersonAuth {
	if a, ok := s.opts.Auth.(twoPersonAuth); ok && s.opts.TwoPerson != nil {
		return a
	}
	return nil
}

func (s *Server) twoPersonRoutes() {
	s.handle("POST /api/v1/admin/requests/{id}/{decision}", "admin-request-decide", s.postTwoPerson)
}

// holdEnvironment says whether taking a cluster off production must wait:
// it does when another SuperAdmin could approve. Alone says nobody could,
// so it applies at once.
func (s *Server) holdEnvironment(r *http.Request, cur clusters.Summary, to string) (hold, alone bool) {
	if cur.Env != clusters.EnvProduction || to == clusters.EnvProduction {
		return false, false
	}
	a := s.twoPersonAuth()
	sess, ok := auth.SessionFrom(r.Context())
	if a == nil || !ok {
		return false, false
	}
	if !a.OtherSuperAdmin(sess) {
		return false, true
	}
	return true, false
}

// requestEnvironment keeps a change of environment that waits.
func (s *Server) requestEnvironment(r *http.Request, cur clusters.Summary, to string) (twoperson.Request, error) {
	sess, _ := auth.SessionFrom(r.Context())
	req, err := s.opts.TwoPerson.Add(r.Context(), twoperson.Request{Kind: twoperson.KindEnvironment, Cluster: cur.ID, ClusterName: cur.Name, From: cur.Env, To: to, By: auth.WhoOf(sess)})
	outcome, detail := audit.OutcomeOK, "waits for another SuperAdmin: "+req.What()
	if err != nil {
		outcome, detail = audit.OutcomeFailed, err.Error()
	}
	s.writeAudit(r, cur.ID, outcome, ActionTwoPerson, cur.ID, detail)
	return req, err
}

// applyEnvironment makes an approved change of environment.
func (s *Server) applyEnvironment(ctx context.Context, req twoperson.Request) error {
	sum, ok := s.opts.Clusters.Summary(req.Cluster)
	if !ok {
		return fmt.Errorf("the connection %s is gone", req.Cluster)
	}
	if sum.Env != req.From {
		return fmt.Errorf("%s is %s now, not %s: reject this and ask again", sum.Name, sum.Env, req.From)
	}
	spec := sum.Spec
	spec.Env = req.To
	_, err := s.opts.Clusters.Update(ctx, req.Cluster, clusters.Draft{Spec: spec})
	return err
}

func (s *Server) postTwoPerson(w http.ResponseWriter, r *http.Request) {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin decides a request.")
		return
	}
	a := s.twoPersonAuth()
	if a == nil {
		jsonError(w, http.StatusNotFound, "This console keeps no requests.")
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
	sess, _ := auth.SessionFrom(r.Context())
	req, err := s.opts.TwoPerson.Decide(r.Context(), id, decision == "approve", in.Comment, auth.WhoOf(sess), func(req twoperson.Request) error {
		switch req.Kind {
		case twoperson.KindEnvironment:
			return s.applyEnvironment(r.Context(), req)
		case twoperson.KindSuperAdmin:
			return a.ApplySuperAdmin(r.Context(), req)
		}
		return fmt.Errorf("%w: kind %q", twoperson.ErrInvalid, req.Kind)
	})
	if err != nil {
		code := http.StatusConflict
		switch {
		case errors.Is(err, twoperson.ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, twoperson.ErrOwn):
			code = http.StatusForbidden
		case errors.Is(err, twoperson.ErrInvalid):
			code = http.StatusBadRequest
		}
		s.writeAudit(r, req.Cluster, audit.OutcomeDenied, ActionTwoPerson, strconv.Itoa(id), fmt.Sprintf("%s %s: %v", decision, req.What(), err))
		jsonError(w, code, err.Error())
		return
	}
	detail := fmt.Sprintf("request %d %s: %s", req.ID, req.State, req.What())
	if req.Comment != "" {
		detail += " · " + req.Comment
	}
	s.writeAudit(r, req.Cluster, audit.OutcomeOK, ActionTwoPerson, strconv.Itoa(req.ID), detail)
	answerJSON(w, r, req)
}

// twoPersonRows are the requests as rows of Approvals: those another
// SuperAdmin may approve, the person's own to withdraw, and the decided.
func (s *Server) twoPersonRows(ctx context.Context, sess auth.Session) (pending, decided []approvalRow, err error) {
	if s.twoPersonAuth() == nil {
		return nil, nil, nil
	}
	list, err := s.opts.TwoPerson.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	me := auth.PersonKey(sess.User)
	for _, req := range list {
		row := approvalRow{Kind: approvalAdmin, What: req.What(), Cluster: req.Cluster, ClusterName: req.ClusterName, RequestedBy: req.By.Name, RequestedAt: timeRef(req.At), ID: strconv.Itoa(req.ID)}
		mine := req.By.Key == me
		switch req.State {
		case twoperson.Waiting:
			row.State, row.Own = approvalPending, mine
			pending = append(pending, row)
		default:
			row.State, row.ApprovedAt, row.Comment = approvalApproved, req.DecidedAt, req.Comment
			if req.State == twoperson.Rejected {
				row.State = approvalRejected
			}
			if req.DecidedBy != nil {
				row.ApprovedBy = req.DecidedBy.Name
				row.Mine = mine && req.DecidedBy.Key != me
			}
			decided = append(decided, row)
		}
	}
	return pending, decided, nil
}
