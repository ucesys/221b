package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/views"
	"github.com/ucesys/221b/server/internal/watson"
)

// Helm applications (Phase 10, HA-01 to HA-20): the records the console
// keeps about releases it installed from git, and the reads the Helm1
// page makes about any release: its revisions, their manifests' diffs,
// the images they run, and whether the cluster still matches what Helm
// applied. Every read is made as the person asking; every change is a
// Watson command that runs as them (HA-06).

const (
	ActionAppSave   = "app.save"
	ActionAppForget = "app.forget"
	// driftFor is how long a drift check is kept before the page asks again.
	driftFor = 2 * time.Minute
)

func (s *Server) appsRoutes() {
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"apps", "apps", s.getApps)
		s.handle("POST "+base+"apps", "app-create", s.postApp)
		s.handle("POST "+base+"apps/refs", "app-repo-refs", s.postAppRefs)
		s.handle("POST "+base+"apps/inspect", "app-repo-inspect", s.postAppInspect)
		s.handle("GET "+base+"apps/{namespace}/{name}", "app", s.getApp)
		s.handle("PUT "+base+"apps/{namespace}/{name}", "app-update", s.putApp)
		s.handle("DELETE "+base+"apps/{namespace}/{name}", "app-delete", s.deleteApp)
		s.handle("POST "+base+"apps/{namespace}/{name}/upgrade", "app-upgrade", s.postAppUpgrade)
		s.handle("GET "+base+"apps/{namespace}/{name}/drift", "app-drift", s.getAppDrift)
		s.handle("GET "+base+"apps/{namespace}/{name}/refs", "app-refs", s.getAppRefs)
		s.handle("GET "+base+"apps/{namespace}/{name}/inspect", "app-inspect", s.getAppInspect)
		s.handle("GET "+base+"apps/{namespace}/{name}/revisions", "app-revisions", s.getAppRevisions)
		s.handle("GET "+base+"apps/{namespace}/{name}/diff", "app-diff", s.getAppDiff)
		s.handle("GET "+base+"apps/{namespace}/{name}/images", "app-images", s.getAppImages)
	}
}

// appView is a record as the page sees it, with its last drift check.
type appView struct {
	apps.Record
	Drift *apps.Drift `json:"drift,omitempty"`
	// DriftState sums the drift up: in sync, drifted, unknown, or "" when
	// it was not checked yet.
	DriftState string `json:"driftState,omitempty"`
}

type driftEntry struct {
	d    apps.Drift
	when time.Time
}

func (s *Server) driftKey(cluster, ns, name string, p kube.Person) string {
	return cluster + "\x00" + ns + "/" + name + "\x00" + p.Key()
}

// cachedDrift is the check kept under the key, unless it is older than
// driftFor or made before since: a check from before the record's last
// deploy says nothing about what runs now (Phase 10.1, M-01).
func (s *Server) cachedDrift(key string, since time.Time) *apps.Drift {
	if v, ok := s.drifts.Load(key); ok {
		e := v.(driftEntry)
		if s.opts.Now().Sub(e.when) < driftFor && (since.IsZero() || e.when.After(since)) {
			d := e.d
			return &d
		}
	}
	return nil
}

// appReady finds the cluster and the person, and refuses while the
// console keeps no applications.
func (s *Server) appReady(w http.ResponseWriter, r *http.Request) (watson.Cluster, watson.Who, bool) {
	if s.opts.Apps == nil {
		jsonError(w, http.StatusNotFound, "this console keeps no Helm applications")
		return watson.Cluster{}, watson.Who{}, false
	}
	return s.watsonReady(w, r)
}

// mayManageApps says whether the person holds a role that changes
// applications: Admin or SuperAdmin (HA-02); Kubernetes decides the rest.
func (s *Server) mayManageApps(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Auth == nil {
		return true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if ok && (sess.Admin || slices.Contains(sess.Roles, roles.Admin) || slices.Contains(sess.Roles, roles.SuperAdmin)) {
		return true
	}
	jsonError(w, http.StatusForbidden, "only Admins and SuperAdmins install, upgrade or remove applications")
	return false
}

func appNames(w http.ResponseWriter, ns, name string) bool {
	if len(validation.IsDNS1123Label(ns)) > 0 || len(validation.IsDNS1123Subdomain(name)) > 0 || len(name) > 53 {
		jsonError(w, http.StatusBadRequest, "name a release and its namespace")
		return false
	}
	return true
}

func (s *Server) getApps(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.appReady(w, r)
	if !ok {
		return
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	recs, err := s.opts.Apps.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := []appView{}
	for _, rec := range recs {
		if rec.Cluster != c.ID {
			continue
		}
		if may, err := cl.CanI(r.Context(), who.Person, "list", "", "secrets", rec.Namespace); err != nil || !may {
			continue
		}
		v := appView{Record: rec}
		if d := s.cachedDrift(s.driftKey(c.ID, rec.Namespace, rec.Name, who.Person), rec.Updated); d != nil {
			v.Drift, v.DriftState = d, d.State()
		}
		out = append(out, v)
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.appReady(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	_, cl, ok := s.lookup(w, r)
	if !ok || !appNames(w, ns, name) || !s.mayList(w, r, cl, "secrets", ns) {
		return
	}
	rec, _, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
	if errors.Is(err, apps.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "the console keeps no source for this release")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	v := appView{Record: rec}
	if d := s.cachedDrift(s.driftKey(c.ID, ns, name, who.Person), rec.Updated); d != nil {
		v.Drift, v.DriftState = d, d.State()
	}
	b, _ := json.Marshal(v)
	writeJSON(w, r, b)
}

// appInput is what the page sends to create or change an application.
type appInput struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Source    *apps.Source `json:"source,omitempty"`
	// Token is a credential for a private repository; "" with ClearToken
	// removes the saved one.
	Token           string   `json:"token,omitempty"`
	ClearToken      bool     `json:"clearToken,omitempty"`
	Values          *string  `json:"values,omitempty"`
	Params          []string `json:"params,omitempty"`
	CreateNamespace *bool    `json:"createNamespace,omitempty"`
	// ValuesFile is a second values file in the chart's directory, "" for
	// none (R2-08); IgnoreDefaults leaves the chart's values.yaml out
	// (R2-09); Fleet is the fleet to deploy to, by id, "" for none (R2-11).
	ValuesFile     *string `json:"valuesFile,omitempty"`
	IgnoreDefaults *bool   `json:"ignoreDefaults,omitempty"`
	Fleet          *string `json:"fleet,omitempty"`
	// Upgrade asks for the change to be deployed as well as saved.
	Upgrade bool `json:"upgrade,omitempty"`
	// Install adds --install to an upgrade.
	Install bool `json:"install,omitempty"`
	// Ref and RefKind, on an upgrade, move the application to another
	// branch or tag first (HA-10).
	Ref     string `json:"ref,omitempty"`
	RefKind string `json:"refKind,omitempty"`
}

func (s *Server) readApp(w http.ResponseWriter, r *http.Request) (appInput, bool) {
	var in appInput
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the application as application/json")
		return in, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the application is not valid: "+err.Error())
		return in, false
	}
	if in.Source != nil {
		src, err := apps.Normalize(*in.Source)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return in, false
		}
		in.Source = &src
	}
	for i, p := range in.Params {
		p = strings.TrimSpace(p)
		if k, _, ok := strings.Cut(p, "="); !ok || strings.TrimSpace(k) == "" {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("parameter %d is not key=value", i+1))
			return in, false
		}
		in.Params[i] = p
	}
	if in.Values != nil {
		if _, err := apps.Values(*in.Values, in.Params); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return in, false
		}
	}
	if in.ValuesFile != nil {
		f := strings.TrimSpace(*in.ValuesFile)
		if f != "" && (strings.HasPrefix(f, "/") || strings.Contains(f, "..") || !(strings.HasSuffix(f, ".yaml") || strings.HasSuffix(f, ".yml"))) {
			jsonError(w, http.StatusBadRequest, "the values file is a YAML file in the chart's directory, such as values-prod.yaml")
			return in, false
		}
		in.ValuesFile = &f
	}
	return in, true
}

// applyAppInput copies what a request sets onto the record; the fleet
// is looked up in the cluster's fleets for its deploy label (R2-11).
func (s *Server) applyAppInput(rec *apps.Record, in appInput, cluster string) error {
	if in.Values != nil {
		rec.Values = *in.Values
	}
	if in.Params != nil {
		rec.Params = in.Params
	}
	if in.CreateNamespace != nil {
		rec.CreateNamespace = *in.CreateNamespace
	}
	if in.ValuesFile != nil {
		rec.ValuesFile = *in.ValuesFile
	}
	if in.IgnoreDefaults != nil {
		rec.IgnoreDefaults = *in.IgnoreDefaults
	}
	if in.Fleet != nil {
		rec.Fleet, rec.FleetLabel = "", ""
		if id := strings.TrimSpace(*in.Fleet); id != "" {
			var list []fleets.Fleet
			if s.opts.Settings != nil {
				list = s.opts.Settings.Current().Fleets[cluster]
			}
			i := slices.IndexFunc(list, func(f fleets.Fleet) bool { return f.ID == id })
			if i < 0 {
				return fmt.Errorf("the cluster has no fleet %q", id)
			}
			if list[i].Deploy == "" {
				return fmt.Errorf("the fleet %s has no deploy label: give it one in its editor", list[i].Name)
			}
			rec.Fleet, rec.FleetLabel = id, list[i].Deploy
		}
	} else if rec.Fleet != "" && s.opts.Settings != nil {
		// The label follows the fleet's editor.
		for _, f := range s.opts.Settings.Current().Fleets[cluster] {
			if f.ID == rec.Fleet && f.Deploy != "" {
				rec.FleetLabel = f.Deploy
			}
		}
	}
	return nil
}

// appLine is the Watson command for an application's change: the source
// is spelled out so the card and the audit log say where the chart comes
// from, and --app takes the saved values (HA-06).
func appLine(verb, ns, name string, src apps.Source, extra ...string) string {
	parts := []string{"helm", verb, name, "--git", src.Repo}
	if src.Path != "" {
		parts = append(parts, "--path", src.Path)
	}
	parts = append(parts, "--ref", src.Ref)
	if src.RefKind == apps.RefTag {
		parts = append(parts, "--tag")
	}
	parts = append(parts, "-n", ns, "--app")
	return strings.Join(append(parts, extra...), " ")
}

// submitApp hands the line to Watson as a card the person approves there
// (asked at the October 27, 2026 review: a deploy is read before it
// runs), and answers with the card as the Watson pane shows it.
func (s *Server) submitApp(w http.ResponseWriter, r *http.Request, c watson.Cluster, who watson.Who, line string) {
	items := s.opts.Watson.Submit(r.Context(), c, who, line, "Helm1", "", false)
	if len(items) == 0 {
		jsonError(w, http.StatusInternalServerError, "Watson took no command")
		return
	}
	it := items[0]
	snap, lines, _ := s.opts.Watson.Snapshot(it.ID, 0)
	b, _ := json.Marshal(s.watsonView(snap, who, c, lines, 0))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(b)
}

// postApp creates an application: its record, then its install (HA-05).
func (s *Server) postApp(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.appReady(w, r)
	if !ok || !s.mayManageApps(w, r) {
		return
	}
	in, ok := s.readApp(w, r)
	if !ok {
		return
	}
	if !appNames(w, in.Namespace, in.Name) {
		return
	}
	if in.Source == nil {
		jsonError(w, http.StatusBadRequest, "name the chart's repository, path and branch or tag")
		return
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if _, _, err := s.opts.Apps.Get(r.Context(), c.ID, in.Namespace, in.Name); err == nil {
		// A record whose install failed is replaced; one with a release
		// is upgraded instead.
		if _, deployed := views.BuildReleaseDetails(cl, in.Namespace, in.Name, s.opts.Now().UTC()); deployed {
			jsonError(w, http.StatusConflict, "release "+in.Namespace+"/"+in.Name+" exists: upgrade it from its panel, or uninstall it first")
			return
		}
	}
	now := s.opts.Now().UTC()
	rec := apps.Record{Cluster: c.ID, Namespace: in.Namespace, Name: in.Name, Source: *in.Source, Created: now, CreatedBy: who.User, Updated: now, UpdatedBy: who.User}
	if err := s.applyAppInput(&rec, in, c.ID); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var token *string
	if in.Token != "" {
		token = &in.Token
	}
	if err := s.opts.Apps.Save(r.Context(), rec, token); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionAppSave, in.Namespace+"/"+in.Name, "source="+quoteDetail(rec.Source.String())+" token="+strconv.FormatBool(token != nil)+" params="+strconv.Itoa(len(rec.Params)))
	var extra []string
	if rec.CreateNamespace {
		extra = append(extra, "--create-namespace")
	}
	s.submitApp(w, r, c, who, appLine("install", in.Namespace, in.Name, *in.Source, extra...))
}

// putApp changes what the console keeps: the source (which adopts a
// release the console did not install, HA-11), the values, the
// parameters or the token; with upgrade, it deploys the change too.
func (s *Server) putApp(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.appReady(w, r)
	if !ok || !s.mayManageApps(w, r) {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !appNames(w, ns, name) {
		return
	}
	in, ok := s.readApp(w, r)
	if !ok {
		return
	}
	now := s.opts.Now().UTC()
	rec, _, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
	switch {
	case errors.Is(err, apps.ErrNotFound):
		if in.Source == nil {
			jsonError(w, http.StatusBadRequest, "the console keeps no source for this release: give one")
			return
		}
		rec = apps.Record{Cluster: c.ID, Namespace: ns, Name: name, Created: now, CreatedBy: who.User}
	case err != nil:
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	if in.Source != nil {
		rec.Source = *in.Source
	}
	if err := s.applyAppInput(&rec, in, c.ID); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Ref != "" {
		src := rec.Source
		src.Ref, src.RefKind = in.Ref, in.RefKind
		if src, err = apps.Normalize(src); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		rec.Source = src
	}
	rec.Updated, rec.UpdatedBy = now, who.User
	var token *string
	if in.ClearToken {
		empty := ""
		token = &empty
	} else if in.Token != "" {
		token = &in.Token
	}
	if err := s.opts.Apps.Save(r.Context(), rec, token); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionAppSave, ns+"/"+name, "source="+quoteDetail(rec.Source.String())+" token="+strconv.FormatBool(token != nil && *token != "")+" params="+strconv.Itoa(len(rec.Params))+" upgrade="+strconv.FormatBool(in.Upgrade))
	if !in.Upgrade {
		b, _ := json.Marshal(appView{Record: rec})
		writeJSON(w, r, b)
		return
	}
	s.submitApp(w, r, c, who, appLine("upgrade", ns, name, rec.Source, installFlags(rec, in.Install)...))
}

// installFlags are --install and, when the record asks for it,
// --create-namespace.
func installFlags(rec apps.Record, install bool) []string {
	if !install {
		return nil
	}
	if rec.CreateNamespace {
		return []string{"--install", "--create-namespace"}
	}
	return []string{"--install"}
}

// postAppUpgrade deploys the application again from its source, moved
// to another branch or tag first when the request names one (HA-10).
func (s *Server) postAppUpgrade(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.appReady(w, r)
	if !ok || !s.mayManageApps(w, r) {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !appNames(w, ns, name) {
		return
	}
	in, ok := s.readApp(w, r)
	if !ok {
		return
	}
	rec, _, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
	if errors.Is(err, apps.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "the console keeps no source for this release: set one first")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	if in.Ref != "" {
		src := rec.Source
		src.Ref, src.RefKind = in.Ref, in.RefKind
		if src, err = apps.Normalize(src); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		rec.Source, rec.Updated, rec.UpdatedBy = src, s.opts.Now().UTC(), who.User
		if err := s.opts.Apps.Save(r.Context(), rec, nil); err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		s.record(r, ActionAppSave, ns+"/"+name, "source="+quoteDetail(rec.Source.String()))
	}
	s.submitApp(w, r, c, who, appLine("upgrade", ns, name, rec.Source, installFlags(rec, in.Install)...))
}

// deleteApp uninstalls the release (Watson removes the record when it is
// gone); ?record=1 only forgets the source and keeps the release.
func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.appReady(w, r)
	if !ok || !s.mayManageApps(w, r) {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !appNames(w, ns, name) {
		return
	}
	if r.URL.Query().Get("record") == "1" {
		if err := s.opts.Apps.Delete(r.Context(), c.ID, ns, name); err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		s.record(r, ActionAppForget, ns+"/"+name, "the release is kept")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	line := "helm uninstall " + name + " -n " + ns
	if r.URL.Query().Get("keepHistory") == "1" {
		line += " --keep-history"
	}
	s.submitApp(w, r, c, who, line)
}

// postAppRefs lists a repository's branches and tags for the dialog
// (HA-05), with the token typed there, which is not kept.
func (s *Server) postAppRefs(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.appReady(w, r); !ok || !s.mayManageApps(w, r) {
		return
	}
	var in struct {
		Repo  string `json:"repo"`
		Token string `json:"token,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return
	}
	src, err := apps.Normalize(apps.Source{Repo: in.Repo, Ref: "x"})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	refs, err := apps.ListRefs(r.Context(), src.Repo, in.Token)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	refs.Heads = nil
	b, _ := json.Marshal(refs)
	writeJSON(w, r, b)
}

// postAppInspect fetches a repository at a ref (its default branch when
// none is given) and says what charts it holds and what its README's
// install example sets, for the new-application form to start from
// (M-07). The token, when given, is used for the fetch and kept nowhere.
// getAppInspect looks a kept application's repository over at its ref
// (M-07), with the saved token: the charts and their values files, for
// the Source card's choice of a second values file (R2-08).
func (s *Server) getAppInspect(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.appReady(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !appNames(w, ns, name) {
		return
	}
	rec, token, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
	if errors.Is(err, apps.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "the console keeps no source for this release")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	co, err := apps.Fetch(r.Context(), rec.Source, token)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	insp := apps.Inspect(co)
	b, _ := json.Marshal(struct {
		apps.Inspection
		Ref     string `json:"ref"`
		RefKind string `json:"refKind"`
		Commit  string `json:"commit"`
	}{insp, rec.Source.Ref, rec.Source.RefKind, co.Commit})
	writeJSON(w, r, b)
}

func (s *Server) postAppInspect(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.appReady(w, r); !ok || !s.mayManageApps(w, r) {
		return
	}
	var in struct {
		Repo    string `json:"repo"`
		Ref     string `json:"ref,omitempty"`
		RefKind string `json:"refKind,omitempty"`
		Token   string `json:"token,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return
	}
	src, err := apps.Normalize(apps.Source{Repo: in.Repo, Ref: "x"})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	src.Ref, src.RefKind = strings.TrimSpace(in.Ref), in.RefKind
	if src.Ref == "" {
		refs, err := apps.ListRefs(r.Context(), src.Repo, in.Token)
		if err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		if refs.Default == "" {
			jsonError(w, http.StatusBadGateway, "the repository has no default branch; name a branch or a tag")
			return
		}
		src.Ref, src.RefKind = refs.Default, apps.RefBranch
	}
	if src.RefKind == "" {
		src.RefKind = apps.RefBranch
	}
	co, err := apps.Fetch(r.Context(), src, in.Token)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := struct {
		apps.Inspection
		Ref     string `json:"ref"`
		RefKind string `json:"refKind"`
		Commit  string `json:"commit"`
	}{apps.Inspect(co), src.Ref, src.RefKind, co.Commit}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// appRefs is what the page learns about an application's repository:
// its refs, the newer tags, and whether its branch moved on (HA-16).
type appRefs struct {
	apps.Refs
	NewerTags []string `json:"newerTags"`
	// Moved says the deployed branch has commits after the deployed one.
	Moved bool `json:"moved"`
	// Head is the deployed ref's commit now.
	Head string `json:"head,omitempty"`
}

func (s *Server) getAppRefs(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.appReady(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	_, cl, ok := s.lookup(w, r)
	if !ok || !appNames(w, ns, name) || !s.mayList(w, r, cl, "secrets", ns) {
		return
	}
	rec, token, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
	if errors.Is(err, apps.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "the console keeps no source for this release")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	refs, err := apps.ListRefs(r.Context(), rec.Source.Repo, token)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := appRefs{Refs: refs, NewerTags: apps.NewerTags(rec.Source.Ref, refs.Tags), Head: refs.Heads[rec.Source.Ref]}
	if out.NewerTags == nil {
		out.NewerTags = []string{}
	}
	out.Moved = rec.Source.RefKind == apps.RefBranch && out.Head != "" && rec.Commit != "" && out.Head != rec.Commit
	out.Heads = nil
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// personClients are clients that read as the person.
type personClients struct {
	cs     *kubernetes.Clientset
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

func clientsAs(cfg *rest.Config, p kube.Person) (*personClients, error) {
	c := rest.CopyConfig(cfg)
	if !p.Nobody() {
		c.Impersonate = rest.ImpersonationConfig{UserName: p.User, Groups: p.ImpersonationGroups()}
	}
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(c)
	if err != nil {
		return nil, err
	}
	// Discovery is read as the console, as Watson does: what the cluster
	// serves is no secret, and a person's rights do not change it.
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &personClients{cs: cs, dyn: dyn, mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disco))}, nil
}

// releases reads a release's revisions as the person, newest first.
func (pc *personClients) releases(ns, name string) ([]*releaseV1, error) {
	d := driver.NewSecrets(pc.cs.CoreV1().Secrets(ns))
	res, err := d.Query(map[string]string{"name": name, "owner": "helm"})
	if err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]*releaseV1, 0, len(res))
	for _, r := range res {
		rel, err := apps.V1(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// appClients prepares the person's clients for an application read.
func (s *Server) appClients(w http.ResponseWriter, r *http.Request) (watson.Cluster, watson.Who, *personClients, bool) {
	c, who, ok := s.appReady(w, r)
	if !ok {
		return c, who, nil, false
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	_, cl, ok := s.lookup(w, r)
	if !ok || !appNames(w, ns, name) || !s.mayList(w, r, cl, "secrets", ns) {
		return c, who, nil, false
	}
	if c.Config == nil {
		jsonError(w, http.StatusServiceUnavailable, "the cluster's connection is not ready")
		return c, who, nil, false
	}
	pc, err := clientsAs(c.Config, who.Person)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return c, who, nil, false
	}
	return c, who, pc, true
}

// getAppDrift compares the cluster with the release's manifest, as the
// person (HA-09); the answer is kept two minutes unless ?refresh=1.
func (s *Server) getAppDrift(w http.ResponseWriter, r *http.Request) {
	c, who, pc, ok := s.appClients(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	key := s.driftKey(c.ID, ns, name, who.Person)
	if r.URL.Query().Get("refresh") != "1" {
		var since time.Time
		if rec, _, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name); err == nil {
			since = rec.Updated
		}
		if d := s.cachedDrift(key, since); d != nil {
			b, _ := json.Marshal(d)
			writeJSON(w, r, b)
			return
		}
	}
	rels, err := pc.releases(ns, name)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "reading the release: "+err.Error())
		return
	}
	if len(rels) == 0 {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no Helm release %s in %s", name, ns))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	d := apps.CheckDrift(ctx, pc.dyn, pc.mapper, rels[0].Manifest, ns, s.opts.Now().UTC())
	s.drifts.Store(key, driftEntry{d: d, when: s.opts.Now()})
	b, _ := json.Marshal(d)
	writeJSON(w, r, b)
}

// revisionView is one revision as the History tab lists it.
type revisionView struct {
	Revision     int       `json:"revision"`
	Updated      time.Time `json:"updated"`
	Status       string    `json:"status"`
	Chart        string    `json:"chart"`
	ChartVersion string    `json:"chartVersion"`
	AppVersion   string    `json:"appVersion"`
	Description  string    `json:"description"`
	Objects      int       `json:"objects"`
}

func (s *Server) getAppRevisions(w http.ResponseWriter, r *http.Request) {
	_, _, pc, ok := s.appClients(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	rels, err := pc.releases(ns, name)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "reading the release: "+err.Error())
		return
	}
	out := make([]revisionView, 0, len(rels))
	for _, rel := range rels {
		v := revisionView{Revision: rel.Version, Updated: rel.Info.LastDeployed, Status: rel.Info.Status.String(), Description: rel.Info.Description, Objects: len(apps.Compare("", rel.Manifest, "", "").Added)}
		if rel.Chart != nil && rel.Chart.Metadata != nil {
			v.Chart, v.ChartVersion, v.AppVersion = rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, rel.Chart.Metadata.AppVersion
		}
		out = append(out, v)
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// getAppImages lists the images a revision's manifest runs (HA-15).
func (s *Server) getAppImages(w http.ResponseWriter, r *http.Request) {
	_, _, pc, ok := s.appClients(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	rel, err := s.revision(pc, ns, name, r.URL.Query().Get("revision"))
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	out := apps.Images(rel.Manifest)
	if out == nil {
		out = []apps.Image{}
	}
	b, _ := json.Marshal(struct {
		Revision int          `json:"revision"`
		Images   []apps.Image `json:"images"`
	}{rel.Version, out})
	writeJSON(w, r, b)
}

// revision finds one revision: the newest for "".
func (s *Server) revision(pc *personClients, ns, name, rev string) (*releaseV1, error) {
	rels, err := pc.releases(ns, name)
	if err != nil {
		return nil, fmt.Errorf("reading the release: %w", err)
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("no Helm release %s in %s", name, ns)
	}
	if rev == "" {
		return rels[0], nil
	}
	n, err := strconv.Atoi(rev)
	if err != nil {
		return nil, fmt.Errorf("revision %q is not a number", rev)
	}
	for _, rel := range rels {
		if rel.Version == n {
			return rel, nil
		}
	}
	return nil, fmt.Errorf("release %s/%s has no revision %d", ns, name, n)
}

// getAppDiff compares two revisions (?from=N&to=M), or what is deployed
// with what the source renders now (?to=source, at the saved ref or at
// ?ref=&refKind=) (HA-17).
func (s *Server) getAppDiff(w http.ResponseWriter, r *http.Request) {
	c, who, pc, ok := s.appClients(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	q := r.URL.Query()
	from, err := s.revision(pc, ns, name, q.Get("from"))
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	fromLabel := fmt.Sprintf("revision %d", from.Version)
	var toManifest, toLabel string
	if q.Get("to") == "source" {
		rec, token, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
		if errors.Is(err, apps.ErrNotFound) {
			jsonError(w, http.StatusNotFound, "the console keeps no source for this release")
			return
		}
		if err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		src := rec.Source
		if ref := q.Get("ref"); ref != "" {
			src.Ref, src.RefKind = ref, q.Get("refKind")
			if src, err = apps.Normalize(src); err != nil {
				jsonError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		co, err := apps.Fetch(r.Context(), src, token)
		if err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		ch, vals, err := apps.Compose(co, src, &rec, nil)
		if err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		toLabel = fmt.Sprintf("%s (%s)", src, short12(co.Commit))
		if m, err := s.opts.Watson.RenderUpgrade(r.Context(), c, who, ns, name, ch, vals); err == nil {
			toManifest = m
		} else {
			// Without a server dry run (no right to upgrade, or no release
			// yet), the chart renders on the console: lookups and the
			// cluster's capabilities are not the real ones.
			version := ""
			if _, client, ok := s.opts.Clusters.Lookup(c.ID); ok && client != nil {
				version = client.Info().Version
			}
			rendered, err2 := apps.Render(r.Context(), ch, vals, name, ns, version)
			if err2 != nil {
				jsonError(w, http.StatusBadGateway, err2.Error())
				return
			}
			toManifest, toLabel = rendered.Manifest, toLabel+", rendered on the console: "+strings.TrimPrefix(err.Error(), "Error: ")
		}
	} else {
		to, err := s.revision(pc, ns, name, q.Get("to"))
		if err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		toManifest, toLabel = to.Manifest, fmt.Sprintf("revision %d", to.Version)
	}
	d := apps.Compare(from.Manifest, toManifest, fromLabel, toLabel)
	b, _ := json.Marshal(struct {
		apps.Diff
		From string `json:"from"`
		To   string `json:"to"`
	}{d, fromLabel, toLabel})
	writeJSON(w, r, b)
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func quoteDetail(s string) string { return strconv.Quote(s) }

// releaseV1 is a release in the SDK's v1 format.
type releaseV1 = release.Release
