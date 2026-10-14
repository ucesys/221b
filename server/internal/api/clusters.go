package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
)

// The cluster connections (P3-04 to P3-09): everyone lists them for the
// menu; administrators add, change, test and remove them. Credentials go
// in and never come back out (X-04).

// maxDraftBody bounds a connection draft: a kubeconfig with inline
// certificates is a few kilobytes; a megabyte is plenty.
const maxDraftBody = 1 << 20

func (s *Server) admin(w http.ResponseWriter, r *http.Request) bool {
	sess, ok := auth.SessionFrom(r.Context())
	if s.opts.Auth != nil && (!ok || !sess.Admin) {
		jsonError(w, http.StatusForbidden, "only an administrator can manage cluster connections")
		return false
	}
	return true
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	b, _ := json.Marshal(struct {
		Clusters []clusters.Summary `json:"clusters"`
	}{s.opts.Clusters.List()})
	writeJSON(w, r, b)
}

func (s *Server) getCluster(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.opts.Clusters.Summary(r.PathValue("cluster"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no cluster named "+fmt.Sprintf("%q", r.PathValue("cluster")))
		return
	}
	b, _ := json.Marshal(sum)
	writeJSON(w, r, b)
}

// draft reads a connection draft from the body.
func (s *Server) draft(w http.ResponseWriter, r *http.Request) (clusters.Draft, bool) {
	var d clusters.Draft
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the connection as application/json")
		return d, false
	}
	var in draftJSON
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDraftBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the connection is not valid: "+err.Error())
		return d, false
	}
	return in.draft(), true
}

// draftJSON is the editor's form as JSON: the spec's fields and the
// credential's, flat.
type draftJSON struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name"`
	Env              string `json:"env"`
	Description      string `json:"description"`
	Server           string `json:"server"`
	CA               string `json:"ca"`
	Auth             string `json:"auth"`
	Mode             string `json:"mode"`
	Token            string `json:"token,omitempty"`
	Kubeconfig       string `json:"kubeconfig,omitempty"`
	Context          string `json:"context,omitempty"`
	CertPEM          string `json:"certPem,omitempty"`
	KeyPEM           string `json:"keyPem,omitempty"`
	CAPEM            string `json:"caPem,omitempty"`
	OIDCIssuer       string `json:"issuer,omitempty"`
	OIDCClientID     string `json:"clientID,omitempty"`
	OIDCClientSecret string `json:"clientSecret,omitempty"`
	OIDCScopes       string `json:"scopes,omitempty"`
}

func (in draftJSON) draft() clusters.Draft {
	return clusters.Draft{
		Spec:       clusters.Spec{ID: in.ID, Name: in.Name, Env: in.Env, Description: in.Description, Server: in.Server, CA: in.CA, Auth: in.Auth, Mode: in.Mode},
		Credential: clusters.Credential{Token: in.Token, Kubeconfig: in.Kubeconfig, Context: in.Context, CertPEM: in.CertPEM, KeyPEM: in.KeyPEM, CAPEM: in.CAPEM, OIDCIssuer: in.OIDCIssuer, OIDCClientID: in.OIDCClientID, OIDCClientSecret: in.OIDCClientSecret, OIDCScopes: in.OIDCScopes},
	}
}

// clusterError answers a validation error with its field, or a plain one.
func clusterError(w http.ResponseWriter, err error) {
	var v *clusters.ValidationError
	switch {
	case errors.As(err, &v):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": v.Msg, "field": v.Field})
	case errors.Is(err, clusters.ErrNotFound):
		jsonError(w, http.StatusNotFound, "no such cluster connection")
	default:
		jsonError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) addCluster(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	d, ok := s.draft(w, r)
	if !ok {
		return
	}
	sum, err := s.opts.Clusters.Add(r.Context(), d)
	if err != nil {
		s.recordCluster(r, audit.ActionClusterAdd, d.Name, audit.OutcomeFailed, err.Error())
		clusterError(w, err)
		return
	}
	s.recordCluster(r, audit.ActionClusterAdd, sum.ID, audit.OutcomeOK, fmt.Sprintf("%s (%s), %s, sign-in by %s, %s", sum.Name, sum.Env, sum.Server, sum.Auth, sum.Mode))
	s.opts.Logger.Info("cluster connection added", "cluster", sum.ID, "server", sum.Server, "auth", sum.Auth)
	b, _ := json.Marshal(sum)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, r, b)
}

func (s *Server) updateCluster(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	d, ok := s.draft(w, r)
	if !ok {
		return
	}
	id := r.PathValue("cluster")
	sum, err := s.opts.Clusters.Update(r.Context(), id, d)
	if err != nil {
		s.recordCluster(r, audit.ActionClusterUpdate, id, audit.OutcomeFailed, err.Error())
		clusterError(w, err)
		return
	}
	what := "settings"
	if !d.Credential.Empty() {
		what = "credentials and settings"
	}
	s.recordCluster(r, audit.ActionClusterUpdate, sum.ID, audit.OutcomeOK, fmt.Sprintf("%s changed: %s (%s), %s, sign-in by %s, %s", what, sum.Name, sum.Env, sum.Server, sum.Auth, sum.Mode))
	s.opts.Logger.Info("cluster connection changed", "cluster", sum.ID, "credentials", !d.Credential.Empty())
	b, _ := json.Marshal(sum)
	writeJSON(w, r, b)
}

func (s *Server) removeCluster(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	id := r.PathValue("cluster")
	sum, _ := s.opts.Clusters.Summary(id)
	if err := s.opts.Clusters.Remove(r.Context(), id); err != nil {
		s.recordCluster(r, audit.ActionClusterRemove, id, audit.OutcomeFailed, err.Error())
		clusterError(w, err)
		return
	}
	s.recordCluster(r, audit.ActionClusterRemove, id, audit.OutcomeOK, fmt.Sprintf("%s (%s) removed with its credentials", sum.Name, sum.Env))
	s.opts.Logger.Info("cluster connection removed", "cluster", id)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"removed":true}` + "\n"))
}

func (s *Server) testClusterDraft(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	d, ok := s.draft(w, r)
	if !ok {
		return
	}
	res, err := s.opts.Clusters.TestDraft(r.Context(), d.ID, d)
	if err != nil {
		clusterError(w, err)
		return
	}
	s.recordCluster(r, audit.ActionClusterTest, firstOf(d.ID, d.Name), outcomeOf(res), res.Summary)
	b, _ := json.Marshal(res)
	writeJSON(w, r, b)
}

func (s *Server) testCluster(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	id := r.PathValue("cluster")
	res, err := s.opts.Clusters.TestSaved(r.Context(), id)
	if err != nil {
		clusterError(w, err)
		return
	}
	s.recordCluster(r, audit.ActionClusterTest, id, outcomeOf(res), res.Summary)
	b, _ := json.Marshal(res)
	writeJSON(w, r, b)
}

func outcomeOf(res clusters.TestResult) string {
	if res.Tone == "err" {
		return audit.OutcomeFailed
	}
	return audit.OutcomeOK
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// recordCluster writes an audit event about a connection.
func (s *Server) recordCluster(r *http.Request, action, cluster, outcome, detail string) {
	if s.opts.Audit == nil {
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	s.opts.Audit.Record(audit.Event{Action: action, Who: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}, Cluster: cluster, Target: "connection", Outcome: outcome, Detail: detail, From: fromAddr(r)})
}
