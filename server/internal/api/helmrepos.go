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
// from besides git: Administration › Settings › Repositories lists them, with Test; a
// SuperAdmin adds, changes and removes them, and those who manage
// applications (Admin, SuperAdmin) read the list, a repository's charts
// and a chart's versions for the new application's form. Each signs in
// with a credential of Administration › Credentials, named by id (0.12.1,
// CR-11). Since 0.12.1 (RP-01 to RP-08) the list holds the git
// repositories applications' Setup and Mycroft's git sources name too,
// each with what uses it; one in use is not removed.

// The audit log's actions of the repositories.
const (
	ActionHelmRepoSave   = "helmrepo.save"
	ActionHelmRepoRemove = "helmrepo.remove"
	ActionHelmRepoTest   = "helmrepo.test"
	// ActionRepoMigration is the upgrade's list of git repositories
	// dismissed (RP-05).
	ActionRepoMigration = "helmrepo.migration"
)

func (s *Server) helmReposRoutes() {
	s.handle("GET /api/v1/helm-repositories", "helm-repositories", s.getHelmRepos)
	s.handle("POST /api/v1/helm-repositories/test", "helm-repository-test", s.postHelmRepoTest)
	s.handle("POST /api/v1/helm-repositories/versions", "helm-chart-versions", s.postHelmChartVersions)
	s.handle("PUT /api/v1/helm-repositories/{name}", "helm-repository-save", s.putHelmRepo)
	s.handle("DELETE /api/v1/helm-repositories/{name}", "helm-repository-remove", s.deleteHelmRepo)
	s.handle("POST /api/v1/helm-repositories/{name}/test", "helm-repository-retest", s.postHelmRepoRetest)
	s.handle("GET /api/v1/helm-repositories/{name}/charts", "helm-repository-charts", s.getHelmRepoCharts)
	s.handle("POST /api/v1/helm-repositories/migration/dismiss", "repository-migration-dismiss", s.postRepoMigrationDismiss)
	s.handle("POST /api/v1/helm-repositories/{name}/rename", "repository-rename", s.postRepoRename)
}

// postRepoRename gives a repository a new name; what names it follows
// (RP-05).
func (s *Server) postRepoRename(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decodeRepo(w, r, &in) {
		return
	}
	from := r.PathValue("name")
	repo, err := s.opts.Repos.Rename(r.Context(), from, in.Name)
	switch {
	case errors.Is(err, apps.ErrNoRepository):
		jsonError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, apps.ErrInvalidRepo):
		jsonError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), apps.ErrInvalidRepo.Error()+": "))
		return
	case err != nil && repo.Name == "":
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	detail := repoWord(repo.Kind) + " renamed from " + from
	if err != nil {
		detail += "; " + err.Error()
	}
	s.record(r, ActionHelmRepoSave, repo.Name, detail)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	b, _ := json.Marshal(repo)
	writeJSON(w, r, b)
}

// helmReposView is the list, with what the person may do.
type helmReposView struct {
	Repositories []repoView `json:"repositories"`
	// MayEdit says the person may add, change and remove them: a
	// SuperAdmin.
	MayEdit bool `json:"mayEdit"`
	// Migration is what the upgrade made of the git addresses typed
	// before 0.12.1, until a SuperAdmin dismisses it (RP-05).
	Migration *apps.GitMigration `json:"migration,omitempty"`
}

// repoView is a repository with what names it (RP-02, RP-06).
type repoView struct {
	apps.Repository
	UsedBy []string `json:"usedBy"`
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
	users, err := s.opts.Repos.Users(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	view := helmReposView{Repositories: make([]repoView, 0, len(list)), MayEdit: s.isSuperAdmin(r)}
	for _, x := range list {
		view.Repositories = append(view.Repositories, repoView{Repository: x, UsedBy: append([]string{}, users[x.Name]...)})
	}
	if view.MayEdit {
		if m, err := s.opts.Repos.Migration(r.Context()); err == nil && !m.Dismissed && (len(m.Created) > 0 || len(m.Notes) > 0) {
			view.Migration = &m
		}
	}
	b, _ := json.Marshal(view)
	writeJSON(w, r, b)
}

// postRepoMigrationDismiss hides the list of what the upgrade made.
func (s *Server) postRepoMigrationDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	if err := s.opts.Repos.DismissMigration(r.Context()); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionRepoMigration, "repositories", "the list of git repositories made at the upgrade dismissed")
	w.WriteHeader(http.StatusNoContent)
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

// repoAudit is a repository as the audit log names it: its credential
// by name, never a secret.
func (s *Server) repoAudit(r *http.Request, repo apps.Repository) string {
	if !repo.CredMigrated {
		kept := ""
		if repo.HasSecret {
			kept = ", one kept"
		}
		return repo.Kind + " " + repo.URL + ", sign-in " + repo.Auth + kept
	}
	cred := "none"
	if repo.Credential != "" && s.opts.Credentials != nil {
		cred = s.credentialLabel(r.Context(), repo.Credential)
	}
	out := repo.Kind + " " + repo.URL + ", credential " + cred
	if repo.DefaultBranch != "" {
		out += ", default branch " + repo.DefaultBranch
	}
	return out
}

// repoWord names a repository's kind in the audit log: "chart
// repository" as before, "git repository".
func repoWord(kind string) string {
	if kind == apps.KindGit {
		return "git repository"
	}
	return "chart repository"
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
	s.record(r, ActionHelmRepoSave, repo.Name, repoWord(repo.Kind)+" "+verb+": "+s.repoAudit(r, repo))
	b, _ := json.Marshal(repo)
	writeJSON(w, r, b)
}

func (s *Server) deleteHelmRepo(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditRepos(w, r) {
		return
	}
	name := r.PathValue("name")
	old, err := s.opts.Repos.Raw(r.Context(), name)
	if errors.Is(err, apps.ErrNoRepository) {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if err == nil {
		err = s.opts.Repos.Delete(r.Context(), name)
	}
	if errors.Is(err, apps.ErrInUse) {
		s.recordOutcome(r, ActionHelmRepoRemove, name, repoWord(old.Kind)+" not removed: "+strings.TrimPrefix(err.Error(), apps.ErrInUse.Error()+": "), audit.OutcomeDenied)
		jsonError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), apps.ErrInUse.Error()+": "))
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionHelmRepoRemove, name, repoWord(old.Kind)+" removed: "+s.repoAudit(r, old))
	w.WriteHeader(http.StatusNoContent)
}

// repoTestInput is a repository as typed, tested before it is saved with
// the credential chosen (CR-11).
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
	// The credential goes only to an address it covers (CR-04).
	repo, secret, err := s.opts.Repos.Signed(r.Context(), repo)
	if err != nil {
		jsonError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), apps.ErrInvalidRepo.Error()+": "))
		return
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
