package apps

import (
	"context"
	"testing"
)

// hookSources is fakeSources that does something once a user is switched.
type hookSources struct {
	fakeSources
	after func(key string)
}

func (h *hookSources) UseRepository(ctx context.Context, key, name string) error {
	_ = h.fakeSources.UseRepository(ctx, key, name)
	if h.after != nil {
		h.after(key)
	}
	return nil
}

// A repository another replica made while the migration ran is never
// overwritten: a name taken meanwhile gets another, and one of the same
// address and credential is used as it is (RP-05).
func TestGitMigrationKeepsRepositoriesMadeMeanwhile(t *testing.T) {
	ctx := context.Background()
	r := gitRepos(t)
	h := &hookSources{fakeSources: fakeSources{users: []GitUser{
		{Key: "a", Label: "source A", URL: "https://github.com/ucesys/221b"},
		{Key: "b", Label: "source B", URL: "https://github.com/org/shop"},
		{Key: "c", Label: "source C", URL: "https://github.com/org/docs"},
	}}}
	h.after = func(key string) {
		if key != "a" {
			return
		}
		// Made elsewhere after the migration read the list.
		if _, _, err := r.Save(ctx, "shop", RepositoryInput{Kind: KindHelm, URL: "https://charts.example.com"}, "bob"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.Save(ctx, "team-docs", RepositoryInput{Kind: KindGit, URL: "https://github.com/org/docs"}, "bob"); err != nil {
			t.Fatal(err)
		}
	}
	created, err := r.MigrateGit(ctx, []GitConsumer{h}, "the upgrade to 0.12.1")
	if err != nil {
		t.Fatal(err)
	}
	shop, err := r.Raw(ctx, "shop")
	if err != nil || shop.Kind != KindHelm || shop.URL != "https://charts.example.com" {
		t.Fatalf("overwritten: %+v %v", shop, err)
	}
	if h.named["b"] != "shop-2" || h.named["c"] != "team-docs" {
		t.Fatalf("named: %v", h.named)
	}
	if len(created) != 2 || created[1].Name != "shop-2" {
		t.Fatalf("created: %+v", created)
	}
	if list, _ := r.List(ctx); len(list) != 4 {
		t.Fatalf("listed: %+v", list)
	}
}
