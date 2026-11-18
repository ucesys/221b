package main

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/credentials"
)

// CR-12 on an install like the owner's: two applications with the same
// GitHub token for one owner and a Helm repository with a password become
// two credentials; the applications and the repository name them, their
// old secrets stay where they were, unread; a second run makes nothing.
func TestTheUpgradeMovesApplicationsAndRepositories(t *testing.T) {
	ctx := context.Background()
	store := apps.NewMemoryStore()
	tok := "ghp_shared-token-for-both"
	for _, a := range [][2]string{{"221b", "k8s-console"}, {"221b-github", "221b-github"}} {
		rec := apps.Record{Cluster: "local", Namespace: a[0], Name: a[1], Source: apps.Source{Repo: "https://github.com/ucesys/" + a[1], Ref: "main", RefKind: apps.RefBranch}}
		if err := store.Save(ctx, rec, &tok); err != nil {
			t.Fatal(err)
		}
	}
	// One without a token is left alone.
	if err := store.Save(ctx, apps.Record{Cluster: "local", Namespace: "web", Name: "public", Source: apps.Source{Repo: "https://github.com/other/public", Ref: "main", RefKind: apps.RefBranch}}, nil); err != nil {
		t.Fatal(err)
	}
	creds := credentials.New(credentials.Options{})
	repos := apps.NewRepos(apps.RepoOptions{Credentials: creds})
	pw := "repo-password"
	if _, _, err := repos.RestoreLegacy(ctx, "private", apps.RepositoryInput{Kind: apps.KindHelm, URL: "https://charts.example.com/private", Auth: apps.AuthBasic, Username: "reader", Secret: &pw}, "alice"); err != nil {
		t.Fatal(err)
	}
	consumers := []credentials.Consumer{appCredentials{store}, repoCredentials{repos}}
	created, err := creds.Migrate(ctx, consumers, "the upgrade to 0.12.1")
	if err != nil || len(created) != 2 {
		t.Fatalf("created: %+v %v", created, err)
	}
	var gh credentials.Created
	for _, c := range created {
		if c.Name == "github.com/ucesys (from app 221b-github/221b-github, app 221b/k8s-console)" {
			gh = c
		}
	}
	if gh.ID == "" {
		t.Fatalf("names: %+v", created)
	}
	for _, a := range [][2]string{{"221b", "k8s-console"}, {"221b-github", "221b-github"}} {
		rec, kept, _ := store.Get(ctx, "local", a[0], a[1])
		if rec.Credential != gh.ID || !rec.CredMigrated || kept != tok {
			t.Fatalf("%s/%s: %+v, kept %q", a[0], a[1], rec, kept)
		}
		auth, err := apps.AuthFor(ctx, creds, rec, "")
		if err != nil || auth.Password != tok || auth.Username != "x-access-token" {
			t.Fatalf("fetched with: %+v %v", auth, err)
		}
	}
	if rec, _, _ := store.Get(ctx, "local", "web", "public"); rec.CredMigrated || rec.Credential != "" {
		t.Fatalf("a public one changed: %+v", rec)
	}
	got, secret, err := repos.Get(ctx, "private")
	if err != nil || got.Credential == "" || secret != pw || got.Username != "reader" || got.Auth != apps.AuthBasic {
		t.Fatalf("the repository: %+v %q %v", got, secret, err)
	}
	if again, err := creds.Migrate(ctx, consumers, "the upgrade to 0.12.1"); err != nil || len(again) != 0 {
		t.Fatalf("again: %+v %v", again, err)
	}
	if list, _ := creds.List(ctx); len(list) != 2 {
		t.Fatalf("credentials: %+v", list)
	}
}
