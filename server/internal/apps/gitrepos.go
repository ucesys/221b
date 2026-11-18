package apps

// --- Administration › Settings › Repositories (0.12.1, RP-01 to RP-08) ---
//
// Settings › Applications became Administration › Settings › Repositories: beside the Helm
// repositories and OCI registries it lists the git repositories that
// applications' Setup and Mycroft's git sources name, each with its
// address, the credential of Administration › Credentials it is fetched with
// and an optional default branch. A user names the repository and keeps
// its own branch, tag and path: an address or a credential changes once,
// on the repository, and every user reads it from there (Resolve,
// WithRepositories). A repository in use is not removed. The upgrade
// makes one of every distinct address applications and git sources typed
// before (MigrateGit).

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/credentials"
)

// ErrInUse is a repository that something names, which stays.
var ErrInUse = errors.New("in use")

// KindWord names a kind of repository for people.
func KindWord(kind string) string {
	switch kind {
	case KindGit:
		return "git repository"
	case KindOCI:
		return "OCI registry"
	}
	return "Helm repository"
}

// credentialUseOf is what a repository's credential serves.
func credentialUseOf(kind string) string {
	if kind == KindGit {
		return credentials.UseGit
	}
	return credentials.UseHelm
}

// RepoUser is something that names a repository: an application's
// Setup, a Mycroft source.
type RepoUser struct {
	Repository string
	// Label names it for people: "application 221b/k8s-console".
	Label string
}

// UsersFunc lists the users of the repositories.
type UsersFunc func(ctx context.Context) ([]RepoUser, error)

// RenameFunc has the users of a repository name it by its new name.
type RenameFunc func(ctx context.Context, from, to string) error

// users are the lists registered with AddUsers, and what renames them.
type repoUsers struct {
	mu      sync.Mutex
	list    []UsersFunc
	renames []RenameFunc
}

// AddRenamer registers what has a repository's users follow it to a new
// name: the applications, Mycroft's sources.
func (r *Repos) AddRenamer(f RenameFunc) {
	if r == nil {
		return
	}
	r.users.mu.Lock()
	defer r.users.mu.Unlock()
	r.users.renames = append(r.users.renames, f)
}

// Rename gives a repository a new name, and has what names it follow
// (RP-05: the upgrade's names are the addresses' last segments, to be
// renamed as SuperAdmins like). A repository with a password or token of
// before 0.12.1 that has not moved to a credential keeps its name.
func (r *Repos) Rename(ctx context.Context, from, to string) (Repository, error) {
	to = strings.TrimSpace(to)
	if !repoName.MatchString(to) {
		return Repository{}, fmt.Errorf("%w: a repository's name is lowercase letters, digits and dashes, up to 63", ErrInvalidRepo)
	}
	var out Repository
	err := r.update(ctx, func(rec *repoRecord) error {
		i := slices.IndexFunc(rec.Repositories, func(x Repository) bool { return x.Name == from })
		if i < 0 {
			return fmt.Errorf("%w %q", ErrNoRepository, from)
		}
		if from == to {
			out = rec.Repositories[i]
			return nil
		}
		if slices.ContainsFunc(rec.Repositories, func(x Repository) bool { return x.Name == to }) {
			return fmt.Errorf("%w: there is a repository %s already", ErrInvalidRepo, to)
		}
		if !rec.Repositories[i].CredMigrated && rec.Repositories[i].HasSecret {
			return fmt.Errorf("%w: %s keeps a password or token of before 0.12.1; choose its credential first", ErrInvalidRepo, from)
		}
		rec.Repositories[i].Name = to
		for j := range rec.Migration.Created {
			if rec.Migration.Created[j].Name == from {
				rec.Migration.Created[j].Name = to
			}
		}
		out = rec.Repositories[i]
		return nil
	})
	if err != nil || from == to {
		return out, err
	}
	r.forget(from)
	r.users.mu.Lock()
	renames := slices.Clone(r.users.renames)
	r.users.mu.Unlock()
	var failed []string
	for _, f := range renames {
		if err := f(ctx, from, to); err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("%s was renamed %s, but some of what names it still names %s, and reads its address as kept: %s", from, to, from, strings.Join(failed, "; "))
	}
	return out, nil
}

// AppRepoRenamer has the applications naming a repository follow it to
// its new name.
func AppRepoRenamer(store Store) RenameFunc {
	return func(ctx context.Context, from, to string) error {
		recs, err := store.List(ctx)
		if err != nil {
			return err
		}
		var failed []string
		for _, x := range recs {
			if x.Source.Repository != from {
				continue
			}
			for range 3 {
				rec, _, err := store.Get(ctx, x.Cluster, x.Namespace, x.Name)
				if err == nil && rec.Source.Repository == from {
					rec.Source.Repository = to
					err = store.Save(ctx, rec, nil)
				}
				if !errors.Is(err, ErrConflict) {
					if err != nil {
						failed = append(failed, AppLabel(x)+": "+err.Error())
					}
					break
				}
			}
		}
		if len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	}
}

// AddUsers registers a list of the repositories' users: the applications,
// Mycroft's sources (RP-06).
func (r *Repos) AddUsers(f UsersFunc) {
	if r == nil {
		return
	}
	r.users.mu.Lock()
	defer r.users.mu.Unlock()
	r.users.list = append(r.users.list, f)
}

// Users are the labels of what names each repository, sorted.
func (r *Repos) Users(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	if r == nil {
		return out, nil
	}
	r.users.mu.Lock()
	list := slices.Clone(r.users.list)
	r.users.mu.Unlock()
	for _, f := range list {
		got, err := f(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range got {
			if !slices.Contains(out[u.Repository], u.Label) {
				out[u.Repository] = append(out[u.Repository], u.Label)
			}
		}
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out, nil
}

// UsersOf are what names one repository.
func (r *Repos) UsersOf(ctx context.Context, name string) ([]string, error) {
	m, err := r.Users(ctx)
	if err != nil {
		return nil, err
	}
	return m[name], nil
}

// AppLabel names an application for Administration › Settings › Repositories' Used by:
// "application team/shop", with its cluster when it is not the console's
// own.
func AppLabel(rec Record) string {
	l := "application " + rec.Namespace + "/" + rec.Name
	if rec.Cluster != "local" {
		l += " on " + rec.Cluster
	}
	return l
}

// AppRepoUses lists the applications that name a repository: a git
// repository picked in Setup, a Helm repository, the OCI registry that
// covers a chart.
func AppRepoUses(store Store) UsersFunc {
	return func(ctx context.Context) ([]RepoUser, error) {
		recs, err := store.List(ctx)
		if err != nil {
			return nil, err
		}
		var out []RepoUser
		for _, rec := range recs {
			if rec.Source.Repository != "" {
				out = append(out, RepoUser{Repository: rec.Source.Repository, Label: AppLabel(rec)})
			}
		}
		return out, nil
	}
}

// gitFor is how long the git repositories read are used before they are
// read again: a change made on another replica shows within it.
const gitFor = 2 * time.Second

type gitCache struct {
	mu sync.Mutex
	at time.Time
	m  map[string]Repository
}

func (c *gitCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}

// GitRepos are the git repositories of the list, by name, as read a
// moment ago at most.
func (r *Repos) GitRepos(ctx context.Context) (map[string]Repository, error) {
	if r == nil {
		return map[string]Repository{}, nil
	}
	r.git.mu.Lock()
	if r.git.m != nil && r.o.Now().Sub(r.git.at) < gitFor {
		m := r.git.m
		r.git.mu.Unlock()
		return m, nil
	}
	r.git.mu.Unlock()
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	m := map[string]Repository{}
	for _, x := range list {
		if x.Kind == KindGit {
			m[x.Name] = x
		}
	}
	r.git.mu.Lock()
	r.git.m, r.git.at = m, r.o.Now()
	r.git.mu.Unlock()
	return m, nil
}

// isGit says a source is a git repository's.
func isGit(src Source) bool { return src.Kind == "" || src.Kind == KindGit }

// gitSource is a git source with the address of the repository it names,
// as the list has it now; one naming none, or one no longer listed, is as
// it was kept.
func (r *Repos) gitSource(ctx context.Context, src Source) (Source, error) {
	if r == nil || !isGit(src) || src.Repository == "" {
		return src, nil
	}
	repo, err := r.Raw(ctx, src.Repository)
	if errors.Is(err, ErrNoRepository) {
		return src, nil
	}
	if err != nil {
		return src, err
	}
	if repo.Kind == KindGit {
		src.Repo = repo.URL
	}
	return src, nil
}

// gitTrust is token with the pinned certificate of the repository a git
// source names, when its credential has none (CR-16).
func (r *Repos) gitTrust(ctx context.Context, src Source, token GitAuth) GitAuth {
	if r == nil || !isGit(src) || src.Repository == "" {
		return token
	}
	if repo, err := r.Raw(ctx, src.Repository); err == nil && repo.Kind == KindGit && repo.URL == src.Repo {
		token.Trust = token.Trust.Or(repo.OwnTrust())
	}
	return token
}

// ResolveGit fills in a git source naming a repository of Administration › Settings ›
// Repositories (RP-03): its address, from the list; the repository is
// returned for its credential and default branch.
func (r *Repos) ResolveGit(ctx context.Context, src Source) (Source, Repository, error) {
	if r == nil {
		return src, Repository{}, fmt.Errorf("%w %q: this console keeps no repositories", ErrNoRepository, src.Repository)
	}
	repo, err := r.Raw(ctx, src.Repository)
	if err != nil {
		return src, Repository{}, err
	}
	if repo.Kind != KindGit {
		return src, Repository{}, fmt.Errorf("%w: %s is a %s, not a git repository", ErrInvalidRepo, repo.Name, KindWord(repo.Kind))
	}
	src.Kind, src.Repo = "", repo.URL
	return src, repo, nil
}

// MatchGit are the git repositories listed at an address, as GitAddress
// writes it: one, usually; several when users of one address named
// different credentials.
func (r *Repos) MatchGit(ctx context.Context, address string) ([]Repository, error) {
	if r == nil {
		return nil, nil
	}
	addr, err := GitAddress(address)
	if err != nil {
		return nil, nil
	}
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Repository
	for _, x := range list {
		if x.Kind == KindGit && x.URL == addr {
			out = append(out, x)
		}
	}
	return out, nil
}

// testGit lists a git repository's branches and tags with its credential.
func (r *Repos) testGit(ctx context.Context, repo Repository) TestResult {
	var auth GitAuth
	if repo.Credential != "" {
		if r.o.Credentials == nil {
			return TestResult{Text: "this console keeps no credentials"}
		}
		a, err := CredentialAuth(ctx, r.o.Credentials, repo.Credential, repo.URL)
		if err != nil {
			return TestResult{Text: fmt.Sprintf("%s's credential: %v", repo.Name, err)}
		}
		auth = a
	}
	auth.Trust = auth.Trust.Or(repo.OwnTrust())
	refs, err := ListRefs(ctx, repo.URL, auth)
	if err != nil {
		return TestResult{Text: err.Error()}
	}
	text := fmt.Sprintf("The repository answered: %d %s and %d %s", len(refs.Branches), plural(len(refs.Branches), "branch", "branches"), len(refs.Tags), plural(len(refs.Tags), "tag", "tags"))
	if refs.Default != "" {
		text += ", its default branch " + refs.Default
	}
	if repo.DefaultBranch != "" && !slices.Contains(refs.Branches, repo.DefaultBranch) {
		return TestResult{Text: text + "; it has no branch " + repo.DefaultBranch + "."}
	}
	return TestResult{OK: true, Text: text + "."}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// resolvingStore is a store whose records read their git repository's
// address and credential from Administration › Settings › Repositories.
type resolvingStore struct {
	Store
	repos *Repos
}

// WithRepositories is a store whose records that name a git repository of
// Administration › Settings › Repositories read its address and credential from the list
// (RP-03): what changes there applies to every application at once. A
// record naming one no longer listed reads as it was kept.
func WithRepositories(s Store, r *Repos) Store {
	if r == nil {
		return s
	}
	return resolvingStore{Store: s, repos: r}
}

func (s resolvingStore) gitRepos(ctx context.Context) (map[string]Repository, error) {
	return s.repos.GitRepos(ctx)
}

func applyRepo(rec *Record, m map[string]Repository) {
	if !isGit(rec.Source) || rec.Source.Repository == "" {
		return
	}
	repo, ok := m[rec.Source.Repository]
	if !ok {
		return
	}
	rec.Source.Repo, rec.Credential, rec.CredMigrated = repo.URL, repo.Credential, true
}

func (s resolvingStore) List(ctx context.Context) ([]Record, error) {
	recs, err := s.Store.List(ctx)
	if err != nil || !slices.ContainsFunc(recs, func(r Record) bool { return isGit(r.Source) && r.Source.Repository != "" }) {
		return recs, err
	}
	m, err := s.gitRepos(ctx)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		applyRepo(&recs[i], m)
	}
	return recs, nil
}

func (s resolvingStore) Get(ctx context.Context, cluster, namespace, name string) (Record, string, error) {
	rec, tok, err := s.Store.Get(ctx, cluster, namespace, name)
	if err != nil || !isGit(rec.Source) || rec.Source.Repository == "" {
		return rec, tok, err
	}
	m, err := s.gitRepos(ctx)
	if err != nil {
		return Record{}, "", err
	}
	applyRepo(&rec, m)
	return rec, tok, nil
}

// --- The upgrade (RP-05) ---

// GitMigration is what the upgrade made of the git addresses typed before
// 0.12.1, as Administration › Settings › Repositories shows it until dismissed.
type GitMigration struct {
	At        time.Time     `json:"at,omitzero"`
	Created   []GitMigrated `json:"created,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
	Dismissed bool          `json:"dismissed,omitempty"`
}

// GitMigrated is a git repository the upgrade made, and from what.
type GitMigrated struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Credential string   `json:"credential,omitempty"`
	From       []string `json:"from"`
}

// GitUser is an application or a Mycroft source that typed a git address
// itself, as the upgrade finds it.
type GitUser struct {
	// Key names it for UseRepository; Label for people.
	Key, Label string
	URL        string
	// Credential is the credential it is fetched with, by id; "" none.
	Credential string
	// Waiting says its own secret of before 0.12.1 has not become a
	// credential yet (CR-12): it is taken at a later run.
	Waiting bool
}

// GitConsumer is a part of the console whose git addresses the upgrade
// makes repositories of.
type GitConsumer interface {
	// GitUsers lists the users that name no repository yet.
	GitUsers(ctx context.Context) ([]GitUser, error)
	// UseRepository has a user name the repository.
	UseRepository(ctx context.Context, key, name string) error
}

// Migration is what the upgrade made, for Administration › Settings › Repositories.
func (r *Repos) Migration(ctx context.Context) (GitMigration, error) {
	if r == nil {
		return GitMigration{}, nil
	}
	rec, _, err := r.load(ctx)
	return rec.Migration, err
}

// DismissMigration hides the list of what the upgrade made.
func (r *Repos) DismissMigration(ctx context.Context) error {
	return r.update(ctx, func(rec *repoRecord) error {
		rec.Migration.Dismissed = true
		return nil
	})
}

// nameChars are what a repository's name may not hold.
var nameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// migratedRepoName names a repository after its address's last segment:
// https://github.com/ucesys/221b is 221b; taken names get -2, -3.
func migratedRepoName(address string, taken map[string]bool) string {
	base := nameChars.ReplaceAllString(strings.ToLower(path.Base(strings.TrimRight(address, "/"))), "-")
	base = strings.Trim(base, "-")
	if len(base) > 56 {
		base = strings.Trim(base[:56], "-")
	}
	if base == "" || base == "." {
		base = "repository"
	}
	name := base
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	return name
}

// MigrateGit makes a git repository of every distinct address the
// consumers' users typed (RP-05), one per credential they named at it,
// named after the address, and has each user name its repository. It runs
// on the leader at start and again later; a user switched names a
// repository and is not taken again, so it can run any number of times.
// An address already listed with the same credential is used as it is.
// Nothing is removed: the users keep their address as well.
func (r *Repos) MigrateGit(ctx context.Context, consumers []GitConsumer, by string) ([]GitMigrated, error) {
	if r == nil {
		return nil, nil
	}
	type found struct {
		u     GitUser
		owner GitConsumer
	}
	var all []found
	var notes []string
	for _, c := range consumers {
		got, err := c.GitUsers(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range got {
			if u.Waiting {
				continue
			}
			addr, err := GitAddress(u.URL)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s keeps its address %s: %v", u.Label, u.URL, err))
				continue
			}
			u.URL = addr
			all = append(all, found{u, c})
		}
	}
	if len(all) == 0 && len(notes) == 0 {
		return nil, nil
	}
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	have := map[[2]string]string{}
	for _, x := range list {
		taken[x.Name] = true
		if x.Kind == KindGit {
			if _, ok := have[[2]string{x.URL, x.Credential}]; !ok {
				have[[2]string{x.URL, x.Credential}] = x.Name
			}
		}
	}
	// The groups: one address with one credential, in the order found.
	type group struct {
		url, cred string
		users     []found
	}
	var groups []*group
	byKey := map[[2]string]*group{}
	creds := map[string]map[string]bool{}
	for _, f := range all {
		k := [2]string{f.u.URL, f.u.Credential}
		g := byKey[k]
		if g == nil {
			g = &group{url: f.u.URL, cred: f.u.Credential}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.users = append(g.users, f)
		if creds[f.u.URL] == nil {
			creds[f.u.URL] = map[string]bool{}
		}
		creds[f.u.URL][f.u.Credential] = true
	}
	var created []GitMigrated
	now := r.o.Now().UTC()
	for _, g := range groups {
		labels := make([]string, 0, len(g.users))
		for _, f := range g.users {
			labels = append(labels, f.u.Label)
		}
		sort.Strings(labels)
		name, ok := have[[2]string{g.url, g.cred}]
		if !ok {
			repo := Repository{Name: migratedRepoName(g.url, taken), Kind: KindGit, URL: g.url, Credential: g.cred, Auth: AuthNone, CredMigrated: true, SetBy: by, SetAt: now}
			made, err := r.addMigrated(ctx, &repo)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s keep the address %s: no repository could be made of it (%v)", strings.Join(labels, ", "), g.url, strings.TrimPrefix(err.Error(), ErrInvalidRepo.Error()+": ")))
				continue
			}
			name = repo.Name
			taken[name] = true
			have[[2]string{g.url, g.cred}] = name
			if made {
				created = append(created, GitMigrated{Name: name, URL: g.url, Credential: g.cred, From: labels})
				if n := len(creds[g.url]); n > 1 {
					notes = append(notes, fmt.Sprintf("%s is one of %d repositories made for %s: its users named different credentials", name, n, g.url))
				}
			}
		}
		for _, f := range g.users {
			if err := f.owner.UseRepository(ctx, f.u.Key, name); err != nil {
				notes = append(notes, fmt.Sprintf("%s keeps its own address for now: it could not be switched to the repository %s (%v)", f.u.Label, name, err))
			}
		}
	}
	m, err := r.Migration(ctx)
	if err != nil {
		return created, err
	}
	fresh := len(created) > 0
	for _, n := range notes {
		fresh = fresh || !slices.Contains(m.Notes, n)
	}
	if !fresh {
		return created, nil
	}
	err = r.update(ctx, func(rec *repoRecord) error {
		mg := &rec.Migration
		mg.At = now
		mg.Created = append(mg.Created, created...)
		for _, n := range notes {
			if !slices.Contains(mg.Notes, n) {
				mg.Notes = append(mg.Notes, n)
			}
		}
		if len(created) > 0 {
			mg.Dismissed = false
		}
		return nil
	})
	return created, err
}

// addMigrated adds a repository the migration made, checked against the
// list as it is when written, not as it was read: another replica that led
// meanwhile may have made one. One of the same address and credential is
// used instead, and a name taken meanwhile is never overwritten but another
// picked. repo's name is the one used; made says it was added.
func (r *Repos) addMigrated(ctx context.Context, repo *Repository) (bool, error) {
	want := repo.Name
	made := false
	err := r.update(ctx, func(rec *repoRecord) error {
		repo.Name, made = want, false
		taken := map[string]bool{}
		for _, x := range rec.Repositories {
			if x.Kind == KindGit && x.URL == repo.URL && x.Credential == repo.Credential {
				repo.Name = x.Name
				return nil
			}
			taken[x.Name] = true
		}
		if taken[repo.Name] {
			repo.Name = migratedRepoName(repo.URL, taken)
		}
		if len(rec.Repositories) >= MaxRepositories {
			return fmt.Errorf("%w: at most %d repositories; remove one first", ErrInvalidRepo, MaxRepositories)
		}
		rec.Repositories = append(rec.Repositories, *repo)
		made = true
		return nil
	})
	r.forget(repo.Name)
	return made, err
}

// AppGitConsumer makes repositories of the applications' git addresses.
type AppGitConsumer struct{ Store Store }

// GitUsers are the applications with a git source naming no repository.
func (a AppGitConsumer) GitUsers(ctx context.Context) ([]GitUser, error) {
	recs, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []GitUser
	for _, rec := range recs {
		if !isGit(rec.Source) || rec.Source.Repository != "" || rec.Source.Repo == "" {
			continue
		}
		out = append(out, GitUser{
			Key: rec.Cluster + "/" + rec.Namespace + "/" + rec.Name, Label: AppLabel(rec), URL: rec.Source.Repo, Credential: rec.Credential,
			Waiting: !rec.CredMigrated && rec.Credential == "" && rec.HasToken,
		})
	}
	return out, nil
}

// UseRepository has an application name its git repository; its address
// stays in the record as well.
func (a AppGitConsumer) UseRepository(ctx context.Context, key, name string) error {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return fmt.Errorf("no application %q", key)
	}
	var err error
	for range 3 {
		var rec Record
		if rec, _, err = a.Store.Get(ctx, parts[0], parts[1], parts[2]); err != nil {
			return err
		}
		if !isGit(rec.Source) || rec.Source.Repository != "" {
			return nil
		}
		rec.Source.Repository, rec.CredMigrated = name, true
		if err = a.Store.Save(ctx, rec, nil); !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return err
}
