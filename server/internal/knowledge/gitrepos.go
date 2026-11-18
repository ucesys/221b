package knowledge

// --- Administration › Settings › Repositories (0.12.1, RP-03, RP-05, RP-06) ---
//
// A git source names a git repository of Administration › Settings › Repositories and
// keeps its own branch and directory: the address and the credential are
// the repository's, read from the list whenever the settings are read, so
// a change made there applies at once.

import (
	"context"
	"slices"
	"sync"

	"github.com/ucesys/221b/server/internal/certpin"
)

// GitRepo is a git repository of Administration › Settings › Repositories as a source
// reads it: its address and the credential it is fetched with ("" none).
type GitRepo struct {
	URL        string
	Credential string
	// PinnedCert is the certificate pinned with the repository (CR-16).
	PinnedCert *certpin.Pin
}

// GitReposFunc reads the git repositories of Administration › Settings › Repositories, by
// name.
type GitReposFunc func(ctx context.Context) (map[string]GitRepo, error)

// gitRepos is the list, set once the repositories are open.
type gitRepos struct {
	mu sync.Mutex
	f  GitReposFunc
}

// SetGitRepos has the sources read their git repositories from Administration › Settings ›
// Repositories.
func (s *Service) SetGitRepos(f GitReposFunc) {
	s.gitRepos.mu.Lock()
	defer s.gitRepos.mu.Unlock()
	s.gitRepos.f = f
}

func (s *Service) gitReposNow(ctx context.Context) (map[string]GitRepo, error) {
	s.gitRepos.mu.Lock()
	f := s.gitRepos.f
	s.gitRepos.mu.Unlock()
	if f == nil {
		return nil, nil
	}
	return f(ctx)
}

// namesRepo says a source is a git source naming a repository of the list.
func (x Source) namesRepo() bool { return x.Kind == SourceGit && x.Repository != "" }

// withRepos fills in the address and credential of the git sources that
// name a repository; one no longer listed reads as it was kept.
func (s *Service) withRepos(ctx context.Context, set Settings) (Settings, error) {
	if !slices.ContainsFunc(set.Sources, Source.namesRepo) {
		return set, nil
	}
	m, err := s.gitReposNow(ctx)
	if err != nil {
		return set, err
	}
	set.Sources = slices.Clone(set.Sources)
	for i, x := range set.Sources {
		if r, ok := m[x.Repository]; ok && x.namesRepo() {
			set.Sources[i].URL, set.Sources[i].Credential, set.Sources[i].CredMigrated = r.URL, r.Credential, true
			set.Sources[i].repoPin = r.PinnedCert
		}
	}
	return set, nil
}

// fillRepo is a git source saved naming a repository, with its address and
// credential.
func (s *Service) fillRepo(ctx context.Context, x Source) (Source, error) {
	if x.Kind != SourceGit {
		x.Repository = ""
		return x, nil
	}
	if x.Repository == "" {
		return x, nil
	}
	m, err := s.gitReposNow(ctx)
	if err != nil {
		return x, err
	}
	r, ok := m[x.Repository]
	if !ok {
		return x, invalid("Administration › Settings › Repositories lists no git repository %q", x.Repository)
	}
	x.URL, x.Credential, x.CredMigrated = r.URL, r.Credential, true
	x.repoPin = r.PinnedCert
	return x, nil
}

// RepoUse is a source that names a repository, for Administration › Settings ›
// Repositories' Used by.
type RepoUse struct {
	Repository string
	Label      string
}

// RepoUses lists the sources that name a git repository.
func (s *Service) RepoUses(ctx context.Context) ([]RepoUse, error) {
	set, err := s.rawSettings(ctx)
	if err != nil {
		return nil, err
	}
	var out []RepoUse
	for _, x := range set.Sources {
		if x.namesRepo() {
			out = append(out, RepoUse{Repository: x.Repository, Label: "knowledge source " + x.Name})
		}
	}
	return out, nil
}

// GitSourceUser is a git source with an address of its own, as the
// upgrade finds it (RP-05).
type GitSourceUser struct {
	ID, Label  string
	URL        string
	Credential string
	// Waiting says its own credential of before 0.12.1 has not moved to
	// Administration › Credentials yet.
	Waiting bool
}

// GitSourceUsers are the git sources that name no repository.
func (s *Service) GitSourceUsers(ctx context.Context) ([]GitSourceUser, error) {
	set, err := s.rawSettings(ctx)
	if err != nil {
		return nil, err
	}
	var out []GitSourceUser
	for _, x := range set.Sources {
		if x.Kind != SourceGit || x.Repository != "" || x.URL == "" {
			continue
		}
		u := GitSourceUser{ID: x.ID, Label: "knowledge source " + x.Name, URL: x.URL, Credential: x.Credential}
		if !x.CredMigrated && x.Credential == "" {
			c, err := s.creds(ctx, sourceKey(x.ID))
			if err != nil {
				return nil, err
			}
			u.Waiting = !c.empty()
		}
		out = append(out, u)
	}
	return out, nil
}

// UseGitRepository has a git source name a repository; its address stays
// in the source as well.
func (s *Service) UseGitRepository(ctx context.Context, id, name string) error {
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		if set.Sources[i].Kind == SourceGit && set.Sources[i].Repository == "" {
			set.Sources[i].Repository, set.Sources[i].CredMigrated = name, true
		}
		return nil
	})
	return err
}

// RenameGitRepository has the sources naming a repository follow it to
// its new name.
func (s *Service) RenameGitRepository(ctx context.Context, from, to string) error {
	_, err := s.editSettings(ctx, func(set *Settings) error {
		for i := range set.Sources {
			if set.Sources[i].namesRepo() && set.Sources[i].Repository == from {
				set.Sources[i].Repository = to
			}
		}
		return nil
	})
	return err
}
