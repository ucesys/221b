package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/appstest"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// awaySecret is the credentials' Secret, which cannot be read while away.
type awaySecret struct {
	*credentials.MemorySecret
	away bool
}

func (a *awaySecret) LoadKey(ctx context.Context, key string) ([]byte, string, bool, error) {
	if a.away {
		return nil, "", false, errors.New("the API server is away")
	}
	return a.MemorySecret.LoadKey(ctx, key)
}

// DB-60, DB-61 at a start: with a database and the secrets in a Secret,
// the list is copied into the database once and then leaves the Secret,
// whose secrets stay; a copy that fails leaves the list there for the
// next start; a list a console from before the move wrote since is
// removed and not read; and nothing is removed when the database cannot
// say the copy was made. With the secrets in memory, the list stays in
// memory with them. (A console without a database, whose list stayed in
// the Secret, is no more: DB-69.)
func TestTheCredentialsListLeavesItsSecretAtAStart(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	mem := &awaySecret{MemorySecret: &credentials.MemorySecret{}}
	keys := func() []string {
		t.Helper()
		k, err := mem.Keys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(k)
		return k
	}
	names := func(s *credentials.Service) []string {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range list {
			out = append(out, c.Name)
		}
		return out
	}
	token := func(s *credentials.Service, name string) credentials.Credential {
		t.Helper()
		c, err := s.Create(ctx, credentials.Credential{Name: name, Kind: credentials.KindToken, Endpoint: "https://github.com/ucesys"}, credentials.Secret{Token: "tok-" + name}, "ann")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// A console before 0.13 kept its list in the Secret, under "index".
	asBefore := func(s *credentials.Service) {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"credentials": list})
		_, version, _, _ := mem.LoadKey(ctx, "index")
		if err := mem.SaveKeyIf(ctx, "index", b, version); err != nil {
			t.Fatal(err)
		}
	}
	before := credentials.New(credentials.Options{Secrets: mem})
	a, b := token(before, "GitHub"), token(before, "Mirror")
	asBefore(before)
	withList := slices.Sorted(slices.Values([]string{"index", "secret-" + a.ID, "secret-" + b.ID}))
	if got := keys(); !slices.Equal(got, withList) {
		t.Fatalf("the Secret before: %v", got)
	}

	// A start that cannot read the Secret: the console starts, the list
	// stays in the Secret, and nothing says it was copied.
	mem.away = true
	failed := &wiring{database: d}
	if list := failed.credentialsList(ctx, mem); list == nil {
		t.Fatal("with a database and a Secret the list is the database's")
	}
	mem.away = false
	if copied, err := d.Imported(ctx, credentials.ListImportID); err != nil || copied {
		t.Fatalf("a copy that failed is recorded (%v): %v", copied, err)
	}
	if got := keys(); !slices.Equal(got, withList) {
		t.Fatalf("a copy that failed changed the Secret: %v", got)
	}

	// The next start copies, and the list leaves the Secret alone.
	w := &wiring{database: d}
	creds := credentials.New(credentials.Options{Secrets: mem, Repo: w.credentialsList(ctx, mem)})
	if !w.importIDs[credentials.ListImportID] || len(w.importClash) != 0 {
		t.Fatalf("the copy's name is not claimed: %v %v", w.importIDs, w.importClash)
	}
	if got := names(creds); !slices.Equal(got, []string{"GitHub", "Mirror"}) {
		t.Fatalf("the list in the database: %v", got)
	}
	secrets := slices.Sorted(slices.Values([]string{"secret-" + a.ID, "secret-" + b.ID}))
	if got := keys(); !slices.Equal(got, secrets) {
		t.Fatalf("the Secret after the copy: %v", got)
	}
	if u, err := creds.Resolve(ctx, a.ID, credentials.UseGit, "https://github.com/ucesys/221b"); err != nil || u.Token != "tok-GitHub" {
		t.Fatalf("a copied credential: %v", err)
	}

	// A console from before the move writes a list of its own; the next
	// start removes it and does not read it.
	older := credentials.New(credentials.Options{Secrets: mem})
	late := token(older, "Late")
	asBefore(older)
	again := &wiring{database: d}
	creds = credentials.New(credentials.Options{Secrets: mem, Repo: again.credentialsList(ctx, mem)})
	if got := names(creds); !slices.Equal(got, []string{"GitHub", "Mirror"}) {
		t.Fatalf("a list written since was read: %v", got)
	}
	if got := keys(); !slices.Equal(got, slices.Sorted(slices.Values(append(slices.Clone(secrets), "secret-"+late.ID)))) {
		t.Fatalf("the Secret after the later list was removed: %v", got)
	}

	// A database that cannot say whether the copy was made removes nothing.
	older = credentials.New(credentials.Options{Secrets: mem})
	token(older, "Later still")
	asBefore(older)
	gone := dbtest.Open(t)
	gone.Close()
	removed := false
	(&wiring{database: gone}).removeCopiedFromSecrets(ctx, credentials.ListImportID, "the credentials' list", func(context.Context) (int, error) {
		removed = true
		return 1, nil
	})
	(&wiring{database: gone}).credentialsList(ctx, mem)
	if got := keys(); removed || !slices.Contains(got, "index") {
		t.Fatalf("removed (%v) on a database that did not answer; the Secret: %v", removed, got)
	}
}

// CR-12 on an install like the owner's: two applications with the same
// GitHub token for one owner and a Helm repository with a password become
// two credentials; the applications and the repository name them, their
// old secrets stay where they were, unread; a second run makes nothing.
func TestTheUpgradeMovesApplicationsAndRepositories(t *testing.T) {
	ctx := context.Background()
	store := appstest.Store(t)
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
