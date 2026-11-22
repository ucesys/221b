package apps

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Every field of a repository is a column and comes back, the list keeps
// its order, a repository keeps its row when it changes, and one removed
// is gone.
func TestRepositoriesInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewReposPG(d)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	git := Repository{Name: "platform-charts", Kind: "git", URL: "https://git.corp.example/platform/charts", DefaultBranch: "main", Credential: "cr-1", CredMigrated: true,
		PinnedCert: &certpin.Pin{SHA256: "ab12", Subject: "CN=git.corp.example", Issuer: "CN=git.corp.example", NotAfter: at.AddDate(1, 0, 0), By: "Ann", At: at},
		TestedAt:   at, TestOK: true, Tested: "listed 12 branches", SetBy: "Ann", SetAt: at}
	helm := Repository{Name: "bitnami", Kind: "helm", URL: "https://charts.bitnami.com/bitnami", Auth: "basic", Username: "ci", HasSecret: true, SetBy: "Bob", SetAt: at.Add(time.Hour)}
	doc := repoRecord{Repositories: []Repository{git, helm}, Migration: GitMigration{At: at, Notes: []string{"two applications shared one address"}, Dismissed: true}}
	save := func(doc repoRecord, version string) (repoRecord, string) {
		t.Helper()
		raw, _ := json.Marshal(doc)
		if _, err := b.SaveKey(ctx, RepositoriesKey, raw, version); err != nil {
			t.Fatal(err)
		}
		got, v, err := b.LoadKey(ctx, RepositoriesKey)
		if err != nil {
			t.Fatal(err)
		}
		var out repoRecord
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatal(err)
		}
		return out, v
	}
	got, v := save(doc, "0")
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("got  %+v\nwant %+v", got, doc)
	}
	id := func(name string) (out string) {
		_ = d.Do(ctx, func(q db.Querier) error {
			return q.QueryRow(ctx, "SELECT repository_id::text FROM repositories WHERE name = $1", name).Scan(&out)
		})
		return out
	}
	was := id("bitnami")
	helm.URL, helm.TestOK = "https://charts.example/bitnami", true
	doc.Repositories = []Repository{helm, git}
	if got, v = save(doc, v); !reflect.DeepEqual(got, doc) || id("bitnami") != was {
		t.Fatalf("after a change and a reorder: %+v (row %s, was %s)", got, id("bitnami"), was)
	}
	doc.Repositories = []Repository{git}
	if got, _ = save(doc, v); len(got.Repositories) != 1 || id("bitnami") != "" {
		t.Fatalf("after removing one: %+v", got)
	}
}

// A repository given a new name keeps its row, so what points at it
// follows it there (DB-62): an application's row, a source of Mycroft's.
// A name that is new beside one that is gone is the same repository only
// when nothing else about it differs.
func TestARenamedRepositoryKeepsItsRow(t *testing.T) {
	d := dbtest.Open(t)
	b := NewReposPG(d)
	ctx := context.Background()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	charts := Repository{Name: "charts", Kind: "git", URL: "https://git.corp.example/platform/charts", SetBy: "Ann", SetAt: at}
	mirror := Repository{Name: "mirror", Kind: "git", URL: "https://git.corp.example/platform/mirror", SetBy: "Ann", SetAt: at}
	version := "0"
	save := func(list ...Repository) {
		t.Helper()
		raw, _ := json.Marshal(repoRecord{Repositories: list})
		if _, err := b.SaveKey(ctx, RepositoriesKey, raw, version); err != nil {
			t.Fatal(err)
		}
		_, v, err := b.LoadKey(ctx, RepositoriesKey)
		if err != nil {
			t.Fatal(err)
		}
		version = v
	}
	id := func(name string) (out string) {
		_ = d.Do(ctx, func(q db.Querier) error {
			return q.QueryRow(ctx, "SELECT repository_id::text FROM repositories WHERE name = $1", name).Scan(&out)
		})
		return out
	}
	save(charts, mirror)
	was, other := id("charts"), id("mirror")
	renamed := charts
	renamed.Name = "platform-charts"
	save(renamed, mirror)
	if got := id("platform-charts"); got != was || id("charts") != "" || id("mirror") != other {
		t.Fatalf("the renamed repository's row is %s, was %s", got, was)
	}
	// Another repository in its place is another row.
	replaced := Repository{Name: "elsewhere", Kind: "git", URL: "https://git.corp.example/elsewhere", SetBy: "Bob", SetAt: at}
	save(replaced, mirror)
	if got := id("elsewhere"); got == "" || got == was || id("platform-charts") != "" {
		t.Fatalf("a repository in another's place has the row %s (the other's was %s)", got, was)
	}
	// Two of the same that take the place of one: only one has its row.
	twin, twin2 := replaced, replaced
	twin.Name, twin2.Name = "twin", "twin-2"
	save(twin, twin2, mirror)
	if a, b := id("twin"), id("twin-2"); a == "" || b == "" || a == b {
		t.Fatalf("two repositories share the row %s %s", a, b)
	}
}
