package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/live"
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
	// Exec says the full Admin may open shells (rbac.roles.admin.exec).
	Exec bool
	// Enforce is "strict" to refuse every change on a cluster whose roles
	// allow more than the shipped ones; anything else only reports.
	Enforce string
	// Prefixes are the ones that tell where a bound name comes from.
	Prefixes roles.Prefixes
	// Monitor keeps the last comparison per cluster; nil makes one.
	Monitor *roles.Monitor
	// Bindings are the RoleBindings the chart renders, for the YAML of a
	// cluster the chart is not installed in (RB-29).
	Bindings []roles.BindingSpec
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
	// The chart marks what it renders for the roles with this label; Helm
	// sets app.kubernetes.io/managed-by itself, so that cannot say it.
	managedLabel   = "221b/managed"
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "221b"
	// driftEvery is how often the roles of every cluster are compared with
	// the shipped ones (RB-36); driftSoon how soon after a start, and after
	// a check that found nothing read yet.
	driftEvery = 5 * time.Minute
	driftSoon  = 15 * time.Second
	// rolesWatch is how often the cached roles are looked at for a change
	// since their last comparison, which is then compared at once (RB-36:
	// "when the roles change"). Looking is five reads from the cache.
	rolesWatch = 20 * time.Second
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
			s = append(s, roles.BoundSubject{Kind: x.Kind, Name: x.Name, Namespace: x.Namespace})
		}
		return s
	}
	for _, o := range src.Objects("clusterrolebindings", "") {
		if b, ok := o.(*rbacv1.ClusterRoleBinding); ok && b.RoleRef.Kind == "ClusterRole" && isOurRole(b.RoleRef.Name) {
			out = append(out, roles.Binding{Role: b.RoleRef.Name, Subjects: subjects(b.Subjects), Managed: managedBy221b(b.Labels)})
		}
	}
	for _, o := range src.Objects("rolebindings", "") {
		if b, ok := o.(*rbacv1.RoleBinding); ok && b.RoleRef.Kind == "ClusterRole" && isOurRole(b.RoleRef.Name) {
			out = append(out, roles.Binding{Role: b.RoleRef.Name, Namespace: b.Namespace, Subjects: subjects(b.Subjects), Managed: managedBy221b(b.Labels)})
		}
	}
	return out
}

// managedBy221b says whether 221B's chart (or its YAML for other clusters)
// made an object: the 221b/managed label, or managed-by 221b as earlier
// drafts wrote it.
func managedBy221b(labels map[string]string) bool {
	return labels[managedLabel] == "true" || labels[managedByLabel] == managedByValue
}

func (s *Server) rolesOptions() roles.Options {
	return roles.Options{Secrets: s.opts.Roles.Secrets, Exec: s.opts.Roles.Exec}
}

func (s *Server) monitor() *roles.Monitor { return s.opts.Roles.Monitor }

// checkRoles compares a cluster's roles with the shipped ones, keeps the
// result, and audits a change. ok is false when the roles have not been read.
// The verdict is kept with of, the fingerprint of the connection src
// belongs to, read with it (running): src may be a connection re-pointed
// since, whose verdict must not pass for the new one (RB-36).
func (s *Server) checkRoles(id, of string, src views.Source) (roles.Drift, map[roles.ID]roles.Deployed, roles.Deployed, bool) {
	dep, nodes, ok := deployedRoles(src)
	if !ok {
		return roles.Drift{}, nil, roles.Deployed{}, false
	}
	d := roles.Check(s.opts.Roles.Templates, s.rolesOptions(), dep, nodes, time.Now().UTC())
	d.Of = of
	d.Stamp = roles.Stamp(dep, nodes)
	if s.monitor().Set(id, d) && s.opts.Audit != nil && (d.State != "match") {
		detail := d.State
		for _, r := range d.Roles {
			detail += "; " + r.ID.Name() + ": " + roles.Summary(r.Differences, 3)
		}
		s.opts.Audit.Record(audit.Event{Action: audit.ActionRolesDrift, Cluster: id, Detail: detail, Who: audit.Who{Subject: "221b"}})
	}
	return d, dep, nodes, true
}

// conn is a cluster's running connection as one read: its client, the
// configuration it connects with, and its fingerprint (what it connects
// to and with, *clusters.Manager's Conn). A role verdict is kept with the
// fingerprint of the client it was read through, and a change is decided
// by the fingerprint of the configuration it runs with, so a connection
// re-pointed between two reads never pairs one cluster's verdict with
// another's API server (RB-36). The fingerprint is "" without one.
type conn struct {
	cl  Cluster
	cfg *rest.Config
	of  string
}

func (s *Server) running(id string) conn {
	var c conn
	if s.opts.Clusters == nil {
		if id == clusters.LocalID && s.opts.Cluster != nil {
			c.cl = s.opts.Cluster
			if rc, ok := s.opts.Cluster.(interface{ RestConfig() *rest.Config }); ok {
				c.cfg = rc.RestConfig()
			}
		}
		return c
	}
	var client clusters.Client
	var ok bool
	if cn, has := s.opts.Clusters.(interface {
		Conn(string) (*live.Store, clusters.Client, string, bool)
	}); has {
		_, client, c.of, ok = cn.Conn(id)
	} else {
		_, client, ok = s.opts.Clusters.Lookup(id)
	}
	if !ok || client == nil {
		return conn{}
	}
	c.cl, _ = client.(Cluster)
	c.cfg = client.RestConfig()
	return c
}

// clusterOf is the running connection of a cluster, or nil.
func (s *Server) clusterOf(id string) Cluster { return s.running(id).cl }

// RoleRefusal is why every change on a cluster is refused for its roles
// (rbac.enforce strict), or "" (RB-36). A cluster with no current
// comparison (none yet, one of the connection before it was re-pointed,
// or an old one) is compared now, from the cache, on any replica; one
// whose roles cannot be read stays refused.
func (s *Server) RoleRefusal(cluster string) string {
	return s.refusalOf(cluster, s.running(cluster))
}

// refusalOf is RoleRefusal for the connection c, read once: what a change
// that runs with c's configuration is decided by.
func (s *Server) refusalOf(cluster string, c conn) string {
	if s.opts.Roles.Enforce != "strict" {
		return ""
	}
	if _, ok := s.monitor().Current(cluster, c.of); !ok && c.cl != nil {
		s.checkRoles(cluster, c.of, c.cl)
	}
	return s.monitor().Refusal(cluster, c.of, s.opts.Roles.Enforce)
}

// RunRoleChecks compares the roles of every cluster with the shipped ones,
// soon and every five minutes, until the context ends; only while this
// replica reads the clusters (ADR-70), so the audit log has one record per
// drift rather than one per replica.
func (s *Server) RunRoleChecks(ctx context.Context) {
	tick := time.NewTimer(driftSoon)
	defer tick.Stop()
	watch := time.NewTicker(rolesWatch)
	defer watch.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch.C:
			if s.opts.Active == nil || s.opts.Active() {
				s.checkChangedRoles()
			}
			continue
		case <-tick.C:
		case <-s.roleCheck:
			if !tick.Stop() {
				select {
				case <-tick.C:
				default:
				}
			}
		}
		tick.Reset(s.roleCheckNow())
	}
}

// roleCheckNow runs one check, and says when the next is due. A replica
// that has just become active has often not listed anything yet: its check
// reads nothing, and the first comparison must not wait five minutes.
func (s *Server) roleCheckNow() time.Duration {
	if (s.opts.Active == nil || s.opts.Active()) && !s.checkAllRoles() {
		return driftSoon
	}
	return driftEvery
}

// CheckRolesSoon asks RunRoleChecks for a check now: a replica that
// becomes the leader compares the roles without waiting for the timer.
func (s *Server) CheckRolesSoon() {
	select {
	case s.roleCheck <- struct{}{}:
	default:
	}
}

// checkChangedRoles compares the roles of each cluster whose cached roles
// are not the ones last compared: an edit to a 221B ClusterRole is seen
// within rolesWatch rather than at the next five-minute round (RB-36).
func (s *Server) checkChangedRoles() {
	check := func(id string, c conn) {
		if c.cl == nil {
			return
		}
		dep, nodes, ok := deployedRoles(c.cl)
		if !ok {
			return
		}
		if d, had := s.monitor().Get(id); had && d.Stamp == roles.Stamp(dep, nodes) && d.Of == c.of {
			return
		}
		s.checkRoles(id, c.of, c.cl)
	}
	if s.opts.Clusters == nil {
		check(clusters.LocalID, s.running(clusters.LocalID))
		return
	}
	for _, sum := range s.opts.Clusters.List() {
		check(sum.ID, s.running(sum.ID))
	}
}

// checkAllRoles compares every cluster's roles and says whether it read any.
func (s *Server) checkAllRoles() bool {
	read := false
	ids := []string{clusters.LocalID}
	if s.opts.Clusters != nil {
		ids = ids[:0]
		for _, sum := range s.opts.Clusters.List() {
			ids = append(ids, sum.ID)
		}
	}
	for _, id := range ids {
		if c := s.running(id); c.cl != nil {
			if _, _, _, ok := s.checkRoles(id, c.of, c.cl); ok {
				read = true
			}
		}
	}
	return read
}

// superAdmin lets only a SuperAdmin through; with sign-in off everyone is
// one, as for every other administration page.
func (s *Server) superAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin sees the roles.")
		return false
	}
	return true
}

func (s *Server) isSuperAdmin(r *http.Request) bool {
	if s.opts.Auth == nil {
		return true
	}
	sess, _ := auth.SessionFrom(r.Context())
	return sess.Admin
}

// getRoles is the Roles page for everyone who signs in; it follows
// Kubernetes, so the bindings are shown only to a person who may list them,
// and the super-role holders only to a SuperAdmin (RB-41, RB-42).
func (s *Server) getRoles(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	// The roles, and the bindings below, of one connection read once.
	c := s.running(clusterID(r))
	if c.cl == nil {
		jsonError(w, http.StatusServiceUnavailable, "The connection to this cluster is not running; see its cluster page.")
		return
	}
	cl := c.cl
	_, dep, _, read := s.checkRoles(clusterID(r), c.of, cl)
	if !read {
		jsonError(w, http.StatusServiceUnavailable, "The console has not read this cluster's roles yet. Try again in a moment.")
		return
	}
	in := roles.RolesInput{Templates: s.opts.Roles.Templates, Options: s.rolesOptions(), Deployed: dep, Bindings: bindingsOf(cl), Prefixes: s.opts.Roles.Prefixes, Visible: true}
	if src, ok := s.opts.Auth.(RoleSource); ok {
		in.Members, in.Exists = src.Members, src.UserExists
		if s.isSuperAdmin(r) {
			in.Holders = src.Holders()
		}
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
		// ClusterRoleBindings are shown only to a person who may list them.
		if allowed {
			wide, err := cl.CanI(ctx, p, "list", "rbac.authorization.k8s.io", "clusterrolebindings", "")
			if err != nil {
				jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
				return
			}
			if !wide {
				in.Bindings = slices.DeleteFunc(in.Bindings, func(b roles.Binding) bool { return b.Namespace == "" })
			}
		}
	}
	writeValue(w, r, roles.BuildRoles(in))
}

// getPermissions is the Permissions page, read-only for everyone who signs
// in (RB-44).
func (s *Server) getPermissions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	c := s.running(clusterID(r))
	if c.cl == nil {
		jsonError(w, http.StatusServiceUnavailable, "The connection to this cluster is not running; see its cluster page.")
		return
	}
	d, dep, nodes, read := s.checkRoles(clusterID(r), c.of, c.cl)
	if !read {
		jsonError(w, http.StatusServiceUnavailable, "The console has not read this cluster's roles yet. Try again in a moment.")
		return
	}
	writeValue(w, r, roles.BuildPermissions(s.opts.Roles.Templates, dep, nodes, s.opts.Roles.Enforce, d))
}

// getRolesManifest is the YAML of the four roles and the bindings of the
// groups in values, in the definitions in use, for a cluster the chart is
// not installed in (RB-29). Nothing writes it: a person who may create roles
// applies it.
func (s *Server) getRolesManifest(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	body := roles.Manifest(roles.ManifestInput{Templates: s.opts.Roles.Templates, Secrets: s.opts.Roles.Secrets, Exec: s.opts.Roles.Exec, Bindings: s.opts.Roles.Bindings})
	h := w.Header()
	h.Set("Content-Type", "application/yaml; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="221b-roles.yaml"`)
	h.Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, body)
}

func (s *Server) rolesRoutes() {
	s.handle("GET /api/v1/admin/roles/manifest", "admin-roles-manifest", s.getRolesManifest)
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
