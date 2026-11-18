package knowledge

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Administration › Settings › Repositories (0.12.1, RP-03, RP-05, RP-06) ---

// A git source names a git repository of Administration › Settings › Repositories and
// keeps its branch and directory; its address and credential are the
// repository's, read from the list, so a change there applies at once. A
// name the list does not have is refused. The upgrade finds the sources
// that type their own address and switches them.
func TestAGitSourceNamesARepository(t *testing.T) {
	f := newFixture(t)
	list := map[string]GitRepo{"runbooks": {URL: "https://git.example.com/ops/runbooks"}}
	f.s.SetGitRepos(func(context.Context) (map[string]GitRepo, error) { return list, nil })
	src := Source{ID: "runbooks", Name: "Runbooks", Kind: SourceGit, Repository: "runbooks", Branch: "ops", Path: "docs"}
	saved, err := f.s.SaveSourceWith(ctx, admin, src, CredsChange{}, true)
	if err != nil || saved.URL != "https://git.example.com/ops/runbooks" || !saved.CredMigrated || saved.Branch != "ops" || saved.Path != "docs" {
		t.Fatalf("%+v %v", saved, err)
	}
	bad := src
	bad.ID, bad.Repository = "other", "nosuch"
	if _, err := f.s.SaveSourceWith(ctx, admin, bad, CredsChange{}, true); err == nil {
		t.Fatal("a repository the list does not have was taken")
	}
	cr, err := f.creds.Create(ctx, credentials.Credential{Name: "ops", Kind: credentials.KindToken, Endpoint: "https://git.example.com/ops"}, credentials.Secret{Token: "t"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	list["runbooks"] = GitRepo{URL: "https://git.example.com/ops/handbook", Credential: cr.ID}
	set, err := f.s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := set.source("runbooks")
	if x.URL != "https://git.example.com/ops/handbook" || x.Credential != cr.ID || x.Branch != "ops" {
		t.Fatalf("%+v", x)
	}
	// Its credential is the repository's: Administration › Credentials lists the
	// repository, not the source.
	if uses, _ := f.s.Uses(ctx); len(uses) != 0 {
		t.Errorf("uses: %+v", uses)
	}
	if uses, _ := f.s.RepoUses(ctx); len(uses) != 1 || uses[0].Repository != "runbooks" || uses[0].Label != "knowledge source Runbooks" {
		t.Errorf("repository uses: %+v", uses)
	}
	// A source with its own address: found by the upgrade, then switched.
	own := Source{ID: "playbooks", Name: "Playbooks", Kind: SourceGit, URL: "https://git.example.com/ops/playbooks.git", Branch: "main"}
	if _, err := f.s.SaveSourceWith(ctx, admin, own, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	users, err := f.s.GitSourceUsers(ctx)
	if err != nil || len(users) != 1 || users[0].ID != "playbooks" || users[0].Waiting || users[0].Label != "knowledge source Playbooks" {
		t.Fatalf("%+v %v", users, err)
	}
	list["playbooks"] = GitRepo{URL: "https://git.example.com/ops/playbooks"}
	if err := f.s.UseGitRepository(ctx, "playbooks", "playbooks"); err != nil {
		t.Fatal(err)
	}
	if users, _ := f.s.GitSourceUsers(ctx); len(users) != 0 {
		t.Fatalf("still found: %+v", users)
	}
	set, _ = f.s.Settings(ctx)
	if x, _ := set.source("playbooks"); x.Repository != "playbooks" || x.URL != "https://git.example.com/ops/playbooks" || x.Branch != "main" {
		t.Fatalf("%+v", x)
	}
}
