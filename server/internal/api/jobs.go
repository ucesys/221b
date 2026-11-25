package api

import (
	"context"
	"errors"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Jobs (Phase 12, JB-15 to JB-18): AIOps › Jobs and Runs and the Inbox's
// findings, served by package jobs under /api/v1/jobs, /api/v1/runs and
// /api/v1/findings; and the read-only toolbox agents on their own read
// the clusters with (JB-08).

func (s *Server) jobsRoutes() {
	for _, rt := range s.opts.Jobs.Routes(s.jobsCaller) {
		s.handle(rt.Pattern, rt.Name, rt.Handler)
	}
}

// jobsCaller is who a request is from, as the jobs need them: a
// SuperAdmin, and the roles they hold. The super roles read across every
// namespace, as agents on their own may, so they see the runs and
// findings; who may change jobs is the jobs' setting.
func (s *Server) jobsCaller(r *http.Request) (jobs.Caller, bool) {
	if s.opts.Auth == nil {
		return jobs.Caller{Person: jobs.Person{Name: "anonymous", From: fromAddr(r)}, Admin: true, Read: true, Approve: true}, true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return jobs.Caller{}, false
	}
	c := jobs.Caller{Person: jobs.Person{Subject: sess.User.Subject, Email: sess.User.Email, Name: firstOf(sess.User.Name, sess.User.Username), From: fromAddr(r)}, Admin: sess.Admin}
	// Those who may approve proposed changes see the findings too, and
	// make cases of them or ignore them (FX-06).
	c.Approve = s.mayApproveCards(r)
	for _, id := range sess.Roles {
		c.Roles = append(c.Roles, string(id))
		c.Read = c.Read || id.Super()
	}
	return c, true
}

// AgentReader is how an agent on its own reads a cluster: the toolbox of
// read-only tools, as the reader identity, and what the prompt says of
// the cluster.
type AgentReader struct {
	Tools    sherlock.Toolbox
	Name     string
	Version  string
	Platform string
}

// ErrNoCluster is a cluster that is not there or not running.
var ErrNoCluster = errors.New("the cluster is not connected")

// ReaderFor is the toolbox an agent on its own reads a cluster with
// (JB-08): the console's own read API, called in-process as sess, the
// reader identity, exactly as Sherlock's tools are for a person; nil is
// the console itself, with sign-in off. None of the tools writes, and none
// shows a Secret's values.
func (s *Server) ReaderFor(ctx context.Context, cluster string, sess *auth.Session) (AgentReader, error) {
	cl, err := s.readerCluster(cluster)
	if err != nil {
		return AgentReader{}, err
	}
	var as auth.Session
	hasSess := false
	if sess != nil && s.opts.Auth != nil {
		as, hasSess = *sess, true
		ctx = auth.WithSession(ctx, *sess)
	}
	out := AgentReader{Tools: s.newToolbox(cluster, cl, as, hasSess), Name: cluster}
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(cluster); ok {
			out.Name, out.Version = sum.Name, sum.Version
		}
	}
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://221b.internal/api/v1/", nil); err == nil {
		if cluster != clusters.LocalID {
			req.SetPathValue("cluster", cluster)
		}
		out.Platform = s.platformPrompt(req, cl, out.Name, out.Version)
	}
	return out, nil
}

// readerCluster is a cluster an agent on its own may read.
func (s *Server) readerCluster(cluster string) (Cluster, error) {
	if s.opts.Clusters == nil {
		if cluster != clusters.LocalID || s.opts.Cluster == nil {
			return nil, ErrNoCluster
		}
		return s.opts.Cluster, nil
	}
	st, client, ok := s.opts.Clusters.Lookup(cluster)
	if !ok {
		return nil, ErrNoCluster
	}
	cl, _ := client.(Cluster)
	if st == nil || cl == nil {
		return nil, ErrNoCluster
	}
	return cl, nil
}

// readerReads are what an audit reads in the namespaces, each listed
// across the cluster: the reader may list one of them, and it reads
// something. Nodes and namespaces do not count: a Viewer reader lists
// them out of the box, and an audit of them alone finds nothing.
var readerReads = [][2]string{{"", "pods"}, {"apps", "deployments"}, {"", "events"}}

// maxReaderNamespaces bounds the namespaces asked one by one.
const maxReaderNamespaces = 100

// ReaderBlind says whether the reader identity sess can read no
// namespace of a cluster (JB-23): it may list none of readerReads across
// the cluster,
// nor the pods of any of its namespaces (the first 100), as Kubernetes
// answers SubjectAccessReviews made by the console. The console itself
// (sign-in off, or nil) reads everything. An error is no answer.
func (s *Server) ReaderBlind(ctx context.Context, cluster string, sess *auth.Session) (bool, error) {
	cl, err := s.readerCluster(cluster)
	if err != nil {
		return false, err
	}
	if sess == nil {
		return false, nil
	}
	p := s.toolPerson(*sess, true)
	if p.Nobody() {
		return false, nil
	}
	for _, rd := range readerReads {
		ok, err := cl.CanI(ctx, p, "list", rd[0], rd[1], "")
		if err != nil || ok {
			return false, err
		}
	}
	for i, o := range cl.Objects("namespaces", "") {
		if i == maxReaderNamespaces {
			break
		}
		m, ok := o.(metav1.Object)
		if !ok {
			continue
		}
		may, err := cl.CanI(ctx, p, "list", "", "pods", m.GetName())
		if err != nil || may {
			return false, err
		}
	}
	return true, nil
}
