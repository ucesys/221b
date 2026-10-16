package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/views"
)

// RolesOptions configure the Roles and Permissions pages and the check of
// the deployed roles against the shipped ones (RB-36, RB-41 to RB-44).
type RolesOptions struct {
	// Templates are the definitions of Admin and SuperAdmin in use; Secrets
	// says whether the rules on Secrets are in the roles (auth.secretsRead).
	Templates roles.Templates
	Secrets   bool
	// Enforce is "strict" to refuse every change on a cluster whose roles
	// allow more than the shipped ones; anything else only reports.
	Enforce string
	// Prefixes are the ones that tell where a bound name comes from.
	Prefixes roles.Prefixes
	// Monitor keeps the last comparison per cluster; nil makes one.
	Monitor *roles.Monitor
}

// RoleSource is what the Roles page asks of the sign-in side; *auth.Service
// implements it. Without it the page shows no counts.
type RoleSource interface {
	Holders() map[roles.ID]roles.Holders
	Members(group string) int
	UserExists(username string) bool
}

// managedBy is the label the chart puts on the bindings it renders (RB-49).
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "221b"
	// driftEvery is how often the roles of every cluster are compared with
	// the shipped ones (RB-36).
	driftEvery = 5 * time.Minute
)

func ruleOf(p rbacv1.PolicyRule) roles.Rule {
	return roles.Rule{APIGroups: p.APIGroups, Resources: p.Resources, ResourceNames: p.ResourceNames, NonResourceURLs: p.NonResourceURLs, Verbs: p.Verbs}
}

// deployedRoles reads the four roles and the nodes role from the cache. ok
// is false when the cache holds no ClusterRoles at all, which is not
// something a cluster does: the console has not read them (yet), and
// "missing" would be a false report.
func deployedRoles(src views.Source) (byID map[roles.ID]roles.Deployed, nodes roles.Deployed, ok bool) {
	if len(src.Objects("clusterroles", "")) == 0 {
		return nil, roles.Deployed{}, false
	}
	byID = map[roles.ID]roles.Deployed{}
	read := func(name string) roles.Deployed {
		cr, _ := src.Object("clusterroles", "", name).(*rbacv1.ClusterRole)
		if cr == nil {
			return roles.Deployed{}
		}
		d := roles.Deployed{Present: true}
		for _, p := range cr.Rules {
			d.Rules = append(d.Rules, ruleOf(p))
		}
		return d
	}
	for _, id := range roles.All {
		byID[id] = read(id.ClusterRole())
	}
	return byID, read(roles.NodesClusterRole), true
}

// isOurRole says whether a binding's role is one of the four, or the
// nodes role.
func isOurRole(name string) bool {
	if name == roles.NodesClusterRole {
		return true
	}
	for _, id := range roles.All {
		if id.ClusterRole() == name {
			return true
		}
	}
	return false
}

// bindingsOf lists the bindings to the four roles, namespaced or not.
func bindingsOf(src views.Source) []roles.Binding {
	var out []roles.Binding
	subjects := func(in []rbacv1.Subject) []roles.BoundSubject {
		var s []roles.BoundSubject
		for _, x := range in {
			s = append(s, roles.BoundSubject{Kind: x.Kind, Name: x.Name})
		}
		return s
	}
	for _, o := range src.Objects("clusterrolebindings", "") {
		if b, ok := o.(*rbacv1.ClusterRoleBinding); ok && b.RoleRef.Kind == "ClusterRole" && isOurRole(b.RoleRef.Name) {
			out = append(out, roles.Binding{Role: b.RoleRef.Name, Subjects: subjects(b.Subjects), Managed: b.Labels[managedByLabel] == managedByValue})
		}
	}
	for _, o := range src.Objects("rolebindings", "") {
		if b, ok := o.(*rbacv1.RoleBinding); ok && b.RoleRef.Kind == "ClusterRole" && isOurRole(b.RoleRef.Name) {
			out = append(out, roles.Binding{Role: b.RoleRef.Name, Namespace: b.Namespace, Subjects: subjects(b.Subjects), Managed: b.Labels[managedByLabel] == managedByValue})
		}
	}
	return out
}

func (s *Server) rolesOptions() roles.Options { return roles.Options{Secrets: s.opts.Roles.Secrets} }

func (s *Server) monitor() *roles.Monitor { return s.opts.Roles.Monitor }

// checkRoles compares a cluster's roles with the shipped ones, keeps the
// result, and audits a change. ok is false when the roles have not been read.
func (s *Server) checkRoles(id string, src views.Source) (roles.Drift, map[roles.ID]roles.Deployed, roles.Deployed, bool) {
	dep, nodes, ok := deployedRoles(src)
	if !ok {
		return roles.Drift{}, nil, roles.Deployed{}, false
	}
	d := roles.Check(s.opts.Roles.Templates, s.rolesOptions(), dep, time.Now().UTC())
	if s.monitor().Set(id, d) && s.opts.Audit != nil && (d.State != "match") {
		detail := d.State
		for _, r := range d.Roles {
			detail += "; " + r.ID.Name() + ": " + roles.Summary(r.Differences, 3)
		}
		s.opts.Audit.Record(audit.Event{Action: audit.ActionRolesDrift, Cluster: id, Detail: detail, Who: audit.Who{Subject: "221b"}})
	}
	return d, dep, nodes, true
}

// RoleRefusal is why every change on a cluster is refused for its roles
// (rbac.enforce strict), or "" (RB-36).
func (s *Server) RoleRefusal(cluster string) string {
	return s.monitor().Refusal(cluster, s.opts.Roles.Enforce)
}

// RunRoleChecks compares the roles of every cluster with the shipped ones,
// now and every five minutes, until the context ends.
func (s *Server) RunRoleChecks(ctx context.Context) {
	tick := time.NewTimer(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.checkAllRoles()
			tick.Reset(driftEvery)
		}
	}
}

func (s *Server) checkAllRoles() {
	if s.opts.Clusters == nil {
		if s.opts.Cluster != nil {
			s.checkRoles(clusters.LocalID, s.opts.Cluster)
		}
		return
	}
	for _, sum := range s.opts.Clusters.List() {
		if _, client, ok := s.opts.Clusters.Lookup(sum.ID); ok {
			if cl, _ := client.(Cluster); cl != nil {
				s.checkRoles(sum.ID, cl)
			}
		}
	}
}

// superAdmin lets only a SuperAdmin through; with sign-in off everyone is
// one, as for every other administration page.
func (s *Server) superAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Auth == nil {
		return true
	}
	if sess, _ := auth.SessionFrom(r.Context()); !sess.Admin {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin sees the roles.")
		return false
	}
	return true
}

func (s *Server) getRoles(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	_, dep, _, read := s.checkRoles(clusterID(r), cl)
	if !read {
		jsonError(w, http.StatusServiceUnavailable, "The console has not read this cluster's roles yet. Try again in a moment.")
		return
	}
	in := roles.RolesInput{Templates: s.opts.Roles.Templates, Deployed: dep, Bindings: bindingsOf(cl), Prefixes: s.opts.Roles.Prefixes, Visible: true}
	if src, ok := s.opts.Auth.(RoleSource); ok {
		in.Members, in.Exists, in.Holders = src.Members, src.UserExists, src.Holders()
	}
	// The bindings are shown only to a person Kubernetes lets list them.
	if p := s.person(r); !p.Nobody() {
		ctx, cancel := context.WithTimeout(r.Context(), prepareTimeout)
		defer cancel()
		allowed, err := cl.CanI(ctx, p, "list", "rbac.authorization.k8s.io", "rolebindings", "")
		if err != nil {
			jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
			return
		}
		in.Visible = allowed
	}
	writeValue(w, r, roles.BuildRoles(in))
}

func (s *Server) getPermissions(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	d, dep, nodes, read := s.checkRoles(clusterID(r), cl)
	if !read {
		jsonError(w, http.StatusServiceUnavailable, "The console has not read this cluster's roles yet. Try again in a moment.")
		return
	}
	writeValue(w, r, roles.BuildPermissions(s.opts.Roles.Templates, dep, nodes, s.opts.Roles.Enforce, d))
}

func (s *Server) rolesRoutes() {
	s.data("admin/roles", "admin-roles", s.getRoles)
	s.data("admin/permissions", "admin-permissions", s.getPermissions)
}

// writeValue answers with a value as JSON.
func writeValue(w http.ResponseWriter, r *http.Request, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "the answer could not be written")
		return
	}
	writeJSON(w, r, b)
}
