package watson

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Administration › Settings › Repositories (0.12.1, RP-04) ---

// --git NAME installs from a git repository of Administration › Settings › Repositories,
// and the record names it; an address the list has is recorded as that
// repository; a name the list does not have is refused, and so is a
// --credential other than the repository's.
func TestHelmGitTakesARepositoryByName(t *testing.T) {
	debugPanics = true
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	ctx := context.Background()
	tg, _ := simTarget(t)
	creds := credentials.New(credentials.Options{})
	repos := apps.NewRepos(apps.RepoOptions{Credentials: creds})
	raw := apps.NewMemoryStore()
	store := apps.WithRepositories(raw, repos)
	tg.Cluster, tg.Apps, tg.Repos, tg.Credentials = "local", store, repos, creds
	repos.AddUsers(apps.AppRepoUses(store))
	repo := gitChart(t)
	if _, _, err := repos.Save(ctx, "charts", apps.RepositoryInput{Kind: apps.KindGit, URL: repo}, "ann"); err != nil {
		t.Fatal(err)
	}
	run := func(line string) (string, int) {
		t.Helper()
		c, err := Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		var out bytes.Buffer
		code := runHelm(ctx, c, tg, &out, &out)
		return out.String(), code
	}
	if out, code := run("helm install hello --git charts --path charts/web --ref v1.0.0 --tag -n web"); code != 0 || !strings.Contains(out, "STATUS: deployed") {
		t.Fatalf("install by name: exit %d\n%s", code, out)
	}
	rec, _, err := raw.Get(ctx, "local", "web", "hello")
	if err != nil || rec.Source.Repository != "charts" || rec.Source.Repo != repo || rec.Source.Path != "charts/web" || rec.Source.Ref != "v1.0.0" || rec.Credential != "" {
		t.Fatalf("%+v %v", rec, err)
	}
	// The address the list has: recorded as the repository.
	if out, code := run("helm install other --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web"); code != 0 {
		t.Fatalf("install by address: exit %d\n%s", code, out)
	}
	if rec, _, _ := raw.Get(ctx, "local", "web", "other"); rec.Source.Repository != "charts" {
		t.Errorf("by address: %+v", rec.Source)
	}
	// --app takes the record, which reads its repository.
	if out, code := run("helm upgrade hello -n web --app --set replicas=3"); code != 0 || !strings.Contains(out, "REVISION: 2") {
		t.Fatalf("upgrade --app: exit %d\n%s", code, out)
	}
	if out, code := run("helm install third --git nosuch --path charts/web --ref v1.0.0 --tag -n web"); code == 0 || !strings.Contains(out, "--git nosuch") || !strings.Contains(out, "Administration › Settings › Repositories") {
		t.Errorf("an unknown name: exit %d\n%s", code, out)
	}
	if _, err := creds.Create(ctx, credentials.Credential{Name: "elsewhere", Kind: credentials.KindToken, Endpoint: "https://gitlab.com"}, credentials.Secret{Token: "glpat"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if out, code := run("helm upgrade hello --git charts --path charts/web --ref v1.0.0 --tag -n web --credential elsewhere --dry-run=client"); code == 0 || !strings.Contains(out, "its own credential") {
		t.Errorf("--credential with a listed repository: exit %d\n%s", code, out)
	}
	// Not removed while the applications name it.
	if err := repos.Delete(ctx, "charts"); err == nil {
		t.Error("a repository in use was removed")
	}
}
