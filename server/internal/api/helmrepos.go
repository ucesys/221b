package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
)

// --- Helm repositories (Phase 12, wave 5, HR-01 to HR-12, ADR-123 to
// ADR-125) ---
//
// The chart repositories and OCI registries the console installs charts
// from besides git: Settings › Applications lists them, with Test; a
// SuperAdmin adds, changes and removes them, and those who manage
// applications (Admin, SuperAdmin) read the list, a repository's charts
// and a chart's versions for the new application's form. A password or
// token is written, never read back: the list says whether one is kept.

// The audit log's actions of the repositories.
const (
	ActionHelmRepoSave   = "helmrepo.save"
	ActionHelmRepoRemove = "helmrepo.remove"
	ActionHelmRepoTest   = "helmrepo.test"
)

func (s *Server) helmReposRoutes() {
	s.handle("GET /api/v1/helm-repositories", "helm-repositories", s.getHelmRepos)
	s.handle("POST /api/v1/helm-repositories/test", "helm-repository-test", s.postHelmRepoTest)
	s.handle("POST /api/v1/helm-repositories/versions", "helm-chart-versions", s.postHelmChartVersions)
	s.handle("PUT /api/v1/helm-repositories/{name}", "helm-repository-save", s.putHelmRepo)
	s.handle("DELETE /api/v1/helm-repositories/{name}", "helm-repository-remove", s.deleteHelmRepo)
	s.handle("POST /api/v1/helm-repositories/{name}/test", "helm-repository-retest", s.postHelmRepoRetest)
	s.handle("GET /api/v1/helm-repositories/{name}/charts", "helm-repository-charts", s.getHelmRepoCharts)
}

// helmReposView is the list, with what the person may do.
type helmReposView struct {
	Repositories []apps.Repository `json:"repositories"`
	// MayEdit says the person may add, change and remove them: a
	// SuperAdmin.
	MayEdit bool `json:"mayEdit"`
}

// mayReadRepos lets through those who manage applications, and
// SuperAdmins.
func (s *Server) mayReadRepos(w http.ResponseWriter, r *http.Request) bool {
	if s.isSuperAdmin(r) {
		return true
	}
	return s.mayManageApps(w, r)
}

func (s *Server) mayEditRepos(w http.ResponseWriter, r *http.Request) bool {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin adds, changes or removes chart repositories.")
		return false
	}
	return true
}

func (s *Server) getHelmRepos(w http.ResponseWriter, r *http.Request) {
	if !s.mayReadRepos(w, r) {
		return
	}
	list, err := s.opts.Repos.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	b, _ := json.Marshal(helmReposView{Repositories: list, MayEdit: s.isSuperAdmin(r)})
	writeJSON(w, r, b)
}

func decodeRepo(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the repository as application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return false
	}
	return true
}

// repoBy names the person, for the list's "set by".
func repoBy(r *http.Request) string {
	sess, _ := auth.SessionFrom(r.Context())
	for _, v := range []string{sess.User.Email, sess.User.Name, sess.User.Subject} {
		if v != "" {
			return v
		}
	}
	return "someone"
}

// repoAudit is a repository as the audit log names it: never its
// password or token.
func repoAudit(repo apps.Repository) string {
	kept := ""
	if repo.HasSecret {
		kept = ", one kept"
	}
	return repo.Kind + " " + repo.URL + ", sign-in " + repo.Auth + kept
}

func (s *Server) putHelmRepo(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	var in apps.RepositoryInput
	if !decodeRepo(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if repo, err := apps.CheckRepository(name, in); err == nil {
		if err := apps.CheckSourceHost(r.Context(), repo.URL); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	repo, created, err := s.opts.Repos.Save(r.Context(), name, in, repoBy(r))
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, apps.ErrInvalidRepo) {
			code = http.StatusBadRequest
		}
		jsonError(w, code, strings.TrimPrefix(err.Error(), apps.ErrInvalidRepo.Error()+": "))
		return
	}
	verb := "changed"
	if created {
		verb = "added"
	}
	s.record(r, ActionHelmRepoSave, repo.Name, "chart repository "+verb+": "+repoAudit(repo))
	b, _ := json.Marshal(repo)
	writeJSON(w, r, b)
}

func (s *Server) deleteHelmRepo(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	name := r.PathValue("name")
	old, _, err := s.opts.Repos.Get(r.Context(), name)
	if errors.Is(err, apps.ErrNoRepository) {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if err == nil {
		err = s.opts.Repos.Delete(r.Context(), name)
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionHelmRepoRemove, name, "chart repository removed: "+repoAudit(old))
	w.WriteHeader(http.StatusNoContent)
}

// repoTestInput is a repository as typed, tested before it is saved; a
// name of a saved one with no secret given tests with the one kept.
type repoTestInput struct {
	Name string `json:"name,omitempty"`
	apps.RepositoryInput
}

// postHelmRepoTest tests a repository as the dialog holds it (HR-03),
// before or after it is saved.
func (s *Server) postHelmRepoTest(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	var in repoTestInput
	if !decodeRepo(w, r, &in) {
		return
	}
	name := in.Name
	if name == "" {
		name = "test"
	}
	repo, err := apps.CheckRepository(name, in.RepositoryInput)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	secret := ""
	if in.Secret != nil {
		secret = strings.TrimSpace(*in.Secret)
	} else if in.Name != "" {
		// The kept secret goes only to the address it was kept for.
		if old, kept, err := s.opts.Repos.Get(r.Context(), in.Name); err == nil && old.URL == repo.URL {
			secret = kept
		}
	}
	if repo.Auth == apps.AuthNone {
		secret = ""
	}
	s.answerRepoTest(w, r, repo, secret, false)
}

// postHelmRepoRetest tests a saved repository and keeps what it found.
func (s *Server) postHelmRepoRetest(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	repo, secret, err := s.opts.Repos.Get(r.Context(), r.PathValue("name"))
	if errors.Is(err, apps.ErrNoRepository) {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.answerRepoTest(w, r, repo, secret, true)
}

func (s *Server) answerRepoTest(w http.ResponseWriter, r *http.Request, repo apps.Repository, secret string, keep bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res := s.opts.Repos.Test(ctx, repo, secret)
	if keep {
		if err := s.opts.Repos.NoteTest(r.Context(), repo.Name, res); err != nil {
			s.opts.Logger.Warn("a chart repository's test could not be kept", "repository", repo.Name, "err", err)
		}
	}
	outcome := audit.OutcomeOK
	if !res.OK {
		outcome = audit.OutcomeFailed
	}
	s.recordOutcome(r, ActionHelmRepoTest, repo.Name, repo.Kind+" "+repo.URL+": "+res.Text, outcome)
	b, _ := json.Marshal(res)
	writeJSON(w, r, b)
}

func (s *Server) getHelmRepoCharts(w http.ResponseWriter, r *http.Request) {
	if !s.mayReadRepos(w, r) {
		return
	}
	charts, err := s.opts.Repos.Charts(r.Context(), r.PathValue("name"))
	switch {
	case errors.Is(err, apps.ErrNoRepository):
		jsonError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, apps.ErrInvalidRepo):
		jsonError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), apps.ErrInvalidRepo.Error()+": "))
		return
	case err != nil:
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	b, _ := json.Marshal(map[string]any{"charts": charts})
	writeJSON(w, r, b)
}

// postHelmChartVersions lists a repository chart's versions (HR-05): a
// Helm repository's by its name, an OCI registry's by its address.
func (s *Server) postHelmChartVersions(w http.ResponseWriter, r *http.Request) {
	if !s.mayReadRepos(w, r) {
		return
	}
	var in apps.Source
	if !decodeRepo(w, r, &in) {
		return
	}
	src, err := apps.Normalize(in)
	if err == nil && !src.FromRepository() {
		err = errors.New("name a Helm repository's or an OCI registry's chart")
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	versions, err := s.opts.Repos.Versions(r.Context(), src)
	if errors.Is(err, apps.ErrNoRepository) {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	b, _ := json.Marshal(map[string]any{"versions": versions, "newest": apps.Newest(versions)})
	writeJSON(w, r, b)
}
