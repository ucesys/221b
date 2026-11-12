package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/backup"
)

// --- Helm repositories (Phase 12, wave 5) ---

// HR-10: a backup holds the repositories with each password or token as
// the placeholder; a restore adds the missing ones, without their
// credentials, and says to give them again.
func TestHelmRepositoriesBackup(t *testing.T) {
	ctx := context.Background()
	repos := apps.NewRepos(apps.RepoOptions{})
	pw := "s3cret-value"
	if _, _, err := repos.Save(ctx, "private", apps.RepositoryInput{Kind: apps.KindHelm, URL: "https://charts.example.com/private", Auth: apps.AuthBasic, Username: "reader", Secret: &pw}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repos.Save(ctx, "public", apps.RepositoryInput{Kind: apps.KindOCI, URL: "oci://registry.example.com/charts"}, "alice"); err != nil {
		t.Fatal(err)
	}
	reg := backup.NewRegistry()
	registerHelmReposBackup(reg, repos)
	f, err := reg.Export(ctx, &backup.Env{By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if strings.Contains(string(b), pw) || !strings.Contains(string(b), `"secret":"\u003csecret: not in the backup\u003e"`) || !strings.Contains(string(b), "oci://registry.example.com/charts") {
		t.Fatalf("the backup: %s", b)
	}
	into := apps.NewRepos(apps.RepoOptions{})
	reg2 := backup.NewRegistry()
	registerHelmReposBackup(reg2, into)
	rep := reg2.Restore(ctx, f, map[string]bool{"helmRepositories": true}, &backup.Env{By: "bob"})
	if rep.Restored["helmRepositories"] != 2 || len(rep.Secrets) != 1 || rep.Secrets[0].Name != "the password of the Helm repository private" || rep.Secrets[0].Where != "Settings › Applications › Repositories › private" {
		t.Fatalf("restored: %+v", rep)
	}
	got, _, err := into.Get(ctx, "private")
	if err != nil || got.HasSecret || got.Auth != apps.AuthNone || got.URL != "https://charts.example.com/private" {
		t.Fatalf("private: %+v %v", got, err)
	}
}
