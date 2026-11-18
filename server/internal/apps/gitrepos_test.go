package apps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Settings › Repositories (0.12.1, RP-01 to RP-08) ---

func gitRepos(t *testing.T) *Repos {
	t.Helper()
	return NewRepos(RepoOptions{Credentials: credentials.New(credentials.Options{})})
}

func appRec(ns, name, repo, cred string) Record {
	return Record{Cluster: "local", Namespace: ns, Name: name, Source: Source{Repo: repo, Path: "deploy/helm/221b", Ref: "main", RefKind: RefBranch}, Credential: cred, CredMigrated: true}
}

// A git repository is kept with its address cleaned, an optional default
// branch and its credential; it keeps its kind (RP-02).
func TestGitRepositoriesAreKept(t *testing.T) {
	ctx := context.Background()
	r := gitRepos(t)
	repo, created, err := r.Save(ctx, "221b", RepositoryInput{Kind: KindGit, URL: "https://GitHub.com/ucesys/221b.git/", DefaultBranch: "main"}, "ann")
	if err != nil || !created || repo.URL != "https://github.com/ucesys/221b" || repo.DefaultBranch != "main" || repo.Kind != KindGit {
		t.Fatalf("%+v %v %v", repo, created, err)
	}
	for _, in := range []RepositoryInput{
		{Kind: KindGit, URL: "http://github.com/ucesys/221b"},
		{Kind: KindGit, URL: "https://github.com"},
		{Kind: KindGit, URL: "https://github.com/ucesys/221b", DefaultBranch: "a..b"},
		{Kind: KindHelm, URL: "https://charts.example.com", DefaultBranch: "main"},
		{Kind: "svn", URL: "https://github.com/ucesys/221b"},
	} {
		if _, _, err := r.Save(ctx, "bad", in, "ann"); !errors.Is(err, ErrInvalidRepo) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if _, _, err := r.Save(ctx, "221b", RepositoryInput{Kind: KindHelm, URL: "https://charts.example.com"}, "ann"); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Errorf("a kind changed: %v", err)
	}
	if _, err := r.Charts(ctx, "221b"); !errors.Is(err, ErrInvalidRepo) {
		t.Errorf("a git repository's charts: %v", err)
	}
	// A credential must serve git and cover the address.
	creds := r.o.Credentials
	helm, err := creds.Create(ctx, credentials.Credential{Name: "elsewhere", Kind: credentials.KindToken, Endpoint: "https://gitlab.com"}, credentials.Secret{Token: "glpat"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Save(ctx, "221b", RepositoryInput{Kind: KindGit, URL: "https://github.com/ucesys/221b", Credential: helm.ID}, "ann"); !errors.Is(err, ErrInvalidRepo) {
		t.Errorf("a credential that does not cover the address: %v", err)
	}
	// A helm line names a Helm repository: a git one is refused.
	if _, err := r.Resolve(ctx, Source{Kind: KindHelm, Repository: "221b", Chart: "x"}); err == nil || !strings.Contains(err.Error(), "--git 221b") {
		t.Errorf("a git repository as a chart repository: %v", err)
	}
}

// The applications naming a git repository read its address and
// credential from the list: a change made there applies to all (RP-03).
func TestApplicationsReadTheirGitRepository(t *testing.T) {
	ctx := context.Background()
	r := gitRepos(t)
	if _, _, err := r.Save(ctx, "221b", RepositoryInput{Kind: KindGit, URL: "https://github.com/ucesys/221b"}, "ann"); err != nil {
		t.Fatal(err)
	}
	raw := NewMemoryStore()
	store := WithRepositories(raw, r)
	rec := appRec("221b", "k8s-console", "https://github.com/ucesys/221b", "")
	rec.Source.Repository = "221b"
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	other := appRec("web", "shop", "https://github.com/org/shop", "")
	if err := store.Save(ctx, other, nil); err != nil {
		t.Fatal(err)
	}
	cred, err := r.o.Credentials.Create(ctx, credentials.Credential{Name: "mirror", Kind: credentials.KindToken, Endpoint: "https://git.example.com/mirror"}, credentials.Secret{Token: "t"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Save(ctx, "221b", RepositoryInput{Kind: KindGit, URL: "https://git.example.com/mirror/221b", Credential: cred.ID}, "ann"); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Get(ctx, "local", "221b", "k8s-console")
	if err != nil || got.Source.Repo != "https://git.example.com/mirror/221b" || got.Credential != cred.ID || !got.CredMigrated || got.Source.Path != "deploy/helm/221b" {
		t.Fatalf("%+v %v", got, err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	for _, x := range list {
		want := map[string]string{"k8s-console": "https://git.example.com/mirror/221b", "shop": "https://github.com/org/shop"}[x.Name]
		if x.Source.Repo != want {
			t.Errorf("%s reads %s", x.Name, x.Source.Repo)
		}
	}
	// A fetch of a source naming it takes its address too.
	src, err := r.gitSource(ctx, Source{Repo: "https://stale.example.com/x", Repository: "221b", Ref: "main"})
	if err != nil || src.Repo != "https://git.example.com/mirror/221b" {
		t.Errorf("%+v %v", src, err)
	}
	// An address typed that the list has is found by it.
	m, err := r.MatchGit(ctx, "git.example.com/mirror/221b.git")
	if err != nil || len(m) != 1 || m[0].Name != "221b" {
		t.Errorf("%+v %v", m, err)
	}
}

// fakeSources stands for Mycroft's git sources in the upgrade.
type fakeSources struct {
	users []GitUser
	named map[string]string
}

func (f *fakeSources) GitUsers(context.Context) ([]GitUser, error) {
	var out []GitUser
	for _, u := range f.users {
		if f.named[u.Key] == "" {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeSources) UseRepository(_ context.Context, key, name string) error {
	if f.named == nil {
		f.named = map[string]string{}
	}
	f.named[key] = name
	return nil
}

// The upgrade makes one git repository of each address typed, named after
// it, carrying the credential its users named; switches them to it; runs
// again without doing anything; and a repository in use is not removed
// (RP-05, RP-06). The live case: 221b/k8s-console and
// 221b-github/221b-github, both from https://github.com/ucesys/221b
// without a credential.
func TestGitMigrationMakesOneRepositoryPerAddress(t *testing.T) {
	ctx := context.Background()
	r := gitRepos(t)
	raw := NewMemoryStore()
	store := WithRepositories(raw, r)
	r.AddUsers(AppRepoUses(store))
	for _, rec := range []Record{
		appRec("221b", "k8s-console", "https://github.com/ucesys/221b", ""),
		appRec("221b-github", "221b-github", "https://github.com/ucesys/221b", ""),
	} {
		if err := store.Save(ctx, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	kn := &fakeSources{users: []GitUser{{Key: "runbooks", Label: "knowledge source Runbooks", URL: "https://GitHub.com/ucesys/221b.git"}}}
	r.AddUsers(func(context.Context) ([]RepoUser, error) {
		var out []RepoUser
		for k, n := range kn.named {
			out = append(out, RepoUser{Repository: n, Label: "knowledge source " + k})
		}
		return out, nil
	})
	consumers := []GitConsumer{AppGitConsumer{Store: store}, kn}
	created, err := r.MigrateGit(ctx, consumers, "the upgrade to 0.12.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].Name != "221b" || created[0].URL != "https://github.com/ucesys/221b" || created[0].Credential != "" ||
		!slices.Equal(created[0].From, []string{"application 221b-github/221b-github", "application 221b/k8s-console", "knowledge source Runbooks"}) {
		t.Fatalf("%+v", created)
	}
	repo, err := r.Raw(ctx, "221b")
	if err != nil || repo.Kind != KindGit || repo.Credential != "" || !repo.CredMigrated {
		t.Fatalf("%+v %v", repo, err)
	}
	for _, key := range [][2]string{{"221b", "k8s-console"}, {"221b-github", "221b-github"}} {
		rec, _, err := raw.Get(ctx, "local", key[0], key[1])
		if err != nil || rec.Source.Repository != "221b" || rec.Source.Repo != "https://github.com/ucesys/221b" || rec.Source.Path != "deploy/helm/221b" || rec.Source.Ref != "main" {
			t.Errorf("%v: %+v %v", key, rec.Source, err)
		}
	}
	if kn.named["runbooks"] != "221b" {
		t.Errorf("the source names %q", kn.named["runbooks"])
	}
	m, err := r.Migration(ctx)
	if err != nil || len(m.Created) != 1 || m.Dismissed || m.At.IsZero() {
		t.Fatalf("%+v %v", m, err)
	}
	// Again: nothing to do, nothing new to say.
	if again, err := r.MigrateGit(ctx, consumers, "the upgrade to 0.12.1"); err != nil || len(again) != 0 {
		t.Fatalf("again: %+v %v", again, err)
	}
	if list, _ := r.List(ctx); len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	// Used by all three; not removed while it is, saying by whom.
	users, err := r.UsersOf(ctx, "221b")
	if err != nil || len(users) != 3 {
		t.Fatalf("%v %v", users, err)
	}
	err = r.Delete(ctx, "221b")
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "application 221b/k8s-console") || !strings.Contains(err.Error(), "knowledge source runbooks") {
		t.Fatalf("delete in use: %v", err)
	}
	if err := r.DismissMigration(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.Migration(ctx); !m.Dismissed {
		t.Error("not dismissed")
	}
	// Once nothing names it, it goes.
	for _, key := range [][2]string{{"221b", "k8s-console"}, {"221b-github", "221b-github"}} {
		if err := raw.Delete(ctx, "local", key[0], key[1]); err != nil {
			t.Fatal(err)
		}
	}
	kn.named = nil
	kn.users = nil
	if err := r.Delete(ctx, "221b"); err != nil {
		t.Fatal(err)
	}
}

// Users of one address that named different credentials get one
// repository each, said so; a name taken gets -2; a user still keeping
// a token of before 0.12.1 waits for its credential (RP-05).
func TestGitMigrationKeepsCredentialsApart(t *testing.T) {
	ctx := context.Background()
	r := gitRepos(t)
	creds := r.o.Credentials
	cred, err := creds.Create(ctx, credentials.Credential{Name: "org", Kind: credentials.KindToken, Endpoint: "https://github.com/ucesys"}, credentials.Secret{Token: "t"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Save(ctx, "shop", RepositoryInput{Kind: KindHelm, URL: "https://charts.example.com"}, "ann"); err != nil {
		t.Fatal(err)
	}
	raw := NewMemoryStore()
	waiting := appRec("web", "old", "https://github.com/org/legacy", "")
	waiting.CredMigrated, waiting.HasToken = false, true
	for _, rec := range []Record{
		appRec("221b", "public", "https://github.com/ucesys/221b", ""),
		appRec("221b", "private", "https://github.com/ucesys/221b/", cred.ID),
		appRec("web", "shop", "https://github.com/org/shop.git", ""),
	} {
		if err := raw.Save(ctx, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Save(ctx, waiting, str("ghp_old")); err != nil {
		t.Fatal(err)
	}
	created, err := r.MigrateGit(ctx, []GitConsumer{AppGitConsumer{Store: raw}}, "the upgrade to 0.12.1")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]GitMigrated{}
	for _, c := range created {
		names[c.Name] = c
	}
	// One for each credential named at the address, in the order found.
	if len(created) != 3 || names["221b"].Credential == names["221b-2"].Credential || names["221b"].URL != names["221b-2"].URL || names["shop-2"].URL != "https://github.com/org/shop" {
		t.Fatalf("%+v", created)
	}
	private := "221b"
	if names["221b-2"].Credential == cred.ID {
		private = "221b-2"
	}
	m, _ := r.Migration(ctx)
	if !slices.ContainsFunc(m.Notes, func(n string) bool { return strings.Contains(n, "different credentials") }) {
		t.Errorf("notes: %v", m.Notes)
	}
	if rec, _, _ := raw.Get(ctx, "local", "web", "old"); rec.Source.Repository != "" {
		t.Errorf("a record keeping its token was switched: %+v", rec.Source)
	}
	if rec, _, _ := raw.Get(ctx, "local", "221b", "private"); rec.Source.Repository != private {
		t.Errorf("%+v", rec.Source)
	}
	// Its token moved to a credential: the next run takes it.
	rec, _, _ := raw.Get(ctx, "local", "web", "old")
	rec.CredMigrated = true
	if err := raw.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	if again, err := r.MigrateGit(ctx, []GitConsumer{AppGitConsumer{Store: raw}}, "the upgrade to 0.12.1"); err != nil || len(again) != 1 || again[0].Name != "legacy" {
		t.Fatalf("%+v %v", again, err)
	}
}

// A repository's name comes from its address's last segment.
func TestMigratedRepoName(t *testing.T) {
	taken := map[string]bool{"app": true}
	for addr, want := range map[string]string{
		"https://github.com/ucesys/221b": "221b",
		"https://gitlab.com/Org/My_App":  "my-app",
		"https://git.example.com/x/app":  "app-2",
		"https://git.example.com/x/___":  "repository",
	} {
		if got := migratedRepoName(addr, taken); got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
}
