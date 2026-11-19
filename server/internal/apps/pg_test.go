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
