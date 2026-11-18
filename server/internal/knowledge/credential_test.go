package knowledge

import (
	"errors"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Credentials (0.12.1, CR-11, CR-12) ---

// A source names a credential of Administration › Credentials, which must serve
// its kind and cover its address; it reads with it.
func TestASourceReadsWithItsCredentialCR11(t *testing.T) {
	f := newFixture(t)
	home := f.conf.AddSpace("INFRA")
	f.conf.AddPage("INFRA", home, "Shop", "<p>Shop.</p>")
	pat, err := f.creds.Create(ctx, credentials.Credential{Name: "confluence", Kind: credentials.KindToken, Endpoint: f.conf.URL}, credentials.Secret{Token: "read-token-123456"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, _ := f.creds.Create(ctx, credentials.Credential{Name: "elsewhere", Kind: credentials.KindToken, Endpoint: "https://elsewhere.example"}, credentials.Secret{Token: "x"}, "ann")
	keys, _ := f.creds.Create(ctx, credentials.Credential{Name: "keys", Kind: credentials.KindAccessKey, Endpoint: f.conf.URL}, credentials.Secret{AccessKeyID: "AKIA", SecretAccessKey: "s"}, "ann")
	src := Source{ID: "infra", Name: "INFRA", Kind: SourceConfluence, BaseURL: f.conf.URL, Flavor: "datacenter", Space: "INFRA"}
	for _, bad := range []string{elsewhere.ID, keys.ID, "cr-missing"} {
		x := src
		x.Credential = bad
		if _, err := f.s.SaveSourceWith(ctx, admin, x, CredsChange{}, true); err == nil {
			t.Fatalf("%s was taken", bad)
		}
	}
	src.Credential = pat.ID
	saved, err := f.s.SaveSourceWith(ctx, admin, src, CredsChange{}, true)
	if err != nil || !saved.CredMigrated {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	got, err := f.s.TestSource(ctx, admin, "infra")
	if err != nil || got.Items == 0 {
		t.Fatalf("read with the credential: %+v %v", got, err)
	}
	// Nothing is kept in the knowledge Secret for it.
	if c, _ := f.s.creds(ctx, sourceKey("infra")); !c.empty() {
		t.Fatalf("kept: %+v", c)
	}
	// A secret replaced is what every read sends from then on.
	if _, err := f.creds.Update(ctx, pat.ID, credentials.Credential{Name: "confluence", Kind: credentials.KindToken, Endpoint: f.conf.URL}, &credentials.Secret{Token: "wrong-token-000000"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TestSource(ctx, admin, "infra"); err == nil || strings.Contains(err.Error(), "wrong-token-000000") {
		t.Fatalf("the new secret was not used: %v", err)
	}
}

// A Confluence Cloud source that names a credential takes the email from
// it: none is asked of the source itself (CR-11).
func TestACloudSourceTakesItsEmailFromItsCredentialCR11(t *testing.T) {
	f := newFixture(t)
	cr, err := f.creds.Create(ctx, credentials.Credential{Name: "cloud", Kind: credentials.KindToken, Endpoint: "https://e2e.atlassian.net", SendAs: credentials.SendAs{Method: credentials.SendBasic, Username: "ann@example.com"}}, credentials.Secret{Token: "read-token-123456"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	src := Source{ID: "cloud", Name: "Cloud", Kind: SourceConfluence, BaseURL: "https://e2e.atlassian.net", Flavor: "cloud", Space: "E2E", Credential: cr.ID}
	saved, err := f.s.SaveSourceWith(ctx, admin, src, CredsChange{}, true)
	if err != nil || !saved.CredMigrated || saved.Email != "" {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	// One that keeps a token of its own still needs the email.
	tok := "own-token-123456"
	src.ID, src.Credential = "own", ""
	if _, err := f.s.SaveSource(ctx, admin, src, &tok, true); err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("no email asked: %v", err)
	}
}

// The upgrade (CR-12) makes one credential per distinct secret and
// endpoint, merging identical ones, switches the sources to it, leaves the
// old secret where it was, unread, and finds nothing the second time.
func TestTheUpgradeMovesTheSourcesCredentialsCR12(t *testing.T) {
	f := newFixture(t)
	f.space(t, map[string]string{"Shop": "<p>Shop.</p>"})
	tok := "ghp_same-token-for-both"
	for _, id := range []string{"runbooks", "playbooks"} {
		if _, err := f.s.SaveSource(ctx, admin, Source{ID: id, Name: id, Kind: SourceGit, URL: "https://github.com/ucesys/" + id}, &tok, true); err != nil {
			t.Fatal(err)
		}
	}
	consumer := CredentialConsumer{S: f.s}
	found, _, err := consumer.Find(ctx)
	if err != nil || len(found) != 3 {
		t.Fatalf("found: %+v %v", found, err)
	}
	created, err := f.creds.Migrate(ctx, []credentials.Consumer{consumer}, "the upgrade")
	if err != nil || len(created) != 2 {
		t.Fatalf("created: %+v %v", created, err)
	}
	var git credentials.Created
	for _, c := range created {
		if strings.HasPrefix(c.Name, "github.com/ucesys") {
			git = c
		}
	}
	if len(git.From) != 2 || git.Name != "github.com/ucesys (from source playbooks, source runbooks)" {
		t.Fatalf("the merged one: %+v", created)
	}
	set, _ := f.s.Settings(ctx)
	for _, id := range []string{"runbooks", "playbooks", "infra"} {
		x, _ := set.source(id)
		if !x.CredMigrated || x.Credential == "" || (id != "infra" && x.Credential != git.ID) {
			t.Fatalf("%s not switched: %+v", id, x)
		}
	}
	// The source reads with its credential; the old secret stays, unread.
	if got, err := f.s.TestSource(ctx, admin, "infra"); err != nil || got.Items == 0 {
		t.Fatalf("after the upgrade: %+v %v", got, err)
	}
	if c, _ := f.s.creds(ctx, sourceKey("infra")); c.Token != "read-token-123456" {
		t.Fatal("the old secret went")
	}
	// Run again: nothing more.
	if again, err := f.creds.Migrate(ctx, []credentials.Consumer{consumer}, "the upgrade"); err != nil || len(again) != 0 {
		t.Fatalf("again: %+v %v", again, err)
	}
	if list, _ := f.creds.List(ctx); len(list) != 2 {
		t.Fatalf("credentials: %+v", list)
	}
	// In use, the merged credential is not removed.
	f.creds.Register(f.s.Uses)
	if _, err := f.creds.Delete(ctx, git.ID); !errors.Is(err, credentials.ErrInUse) || !strings.Contains(err.Error(), "knowledge source runbooks") {
		t.Fatalf("removed while used: %v", err)
	}
}
