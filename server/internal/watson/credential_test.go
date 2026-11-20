package watson

import (
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/appstest"
	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Credentials (0.12.1, CR-11) ---

// A typed helm line's git source is fetched with the credential
// --credential names; else the application's, when a record of it is
// kept for that repository; else the one credential that covers the
// repository. Several are refused: Watson never guesses.
func TestAGitSourcesCredentialIsChosenNotGuessed(t *testing.T) {
	ctx := context.Background()
	creds := credentials.New(credentials.Options{})
	store := appstest.Store(t)
	tg := Target{Cluster: "local", Apps: store, Credentials: creds}
	repo := "https://github.com/ucesys/221b"
	choose := func(line string) (*appChart, apps.GitAuth, error) {
		t.Helper()
		c, err := Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		src, _, err := appSource(ctx, c, tg)
		if err != nil {
			t.Fatal(err)
		}
		a := &appChart{src: src}
		auth, err := a.gitAuth(ctx, c, tg, "web", "shop", "")
		return a, auth, err
	}
	line := "helm upgrade shop --git " + repo + " --ref main -n web"

	// None covers it: anonymously.
	if a, auth, err := choose(line); err != nil || !auth.None() || !a.credSet || a.cred != "" {
		t.Fatalf("none: %+v %+v %v", a, auth, err)
	}
	// One covers it: that one.
	org, err := creds.Create(ctx, credentials.Credential{Name: "org", Kind: credentials.KindToken, Endpoint: "https://github.com/ucesys", SendAs: credentials.SendAs{Method: credentials.SendBasic, Username: "x-access-token"}}, credentials.Secret{Token: "ghp_org"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Create(ctx, credentials.Credential{Name: "elsewhere", Kind: credentials.KindToken, Endpoint: "https://gitlab.com"}, credentials.Secret{Token: "glpat"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if a, auth, err := choose(line); err != nil || auth.Password != "ghp_org" || auth.Username != "x-access-token" || a.cred != org.ID {
		t.Fatalf("one: %+v %+v %v", a, auth, err)
	}
	// Several cover it: refused, naming them.
	one, _ := creds.Create(ctx, credentials.Credential{Name: "the 221b repo", Kind: credentials.KindToken, Endpoint: "https://github.com/ucesys/221b"}, credentials.Secret{Token: "ghp_repo"}, "ann")
	if _, _, err := choose(line); err == nil || !strings.Contains(err.Error(), "2 credentials cover") || !strings.Contains(err.Error(), "'the 221b repo'") || !strings.Contains(err.Error(), "--credential NAME") {
		t.Fatalf("several: %v", err)
	}
	// --credential chooses, by name.
	if a, auth, err := choose(line + " --credential 'the 221b repo'"); err != nil || auth.Bearer != "ghp_repo" || a.cred != one.ID {
		t.Fatalf("chosen: %+v %+v %v", a, auth, err)
	}
	if _, _, err := choose(line + " --credential elsewhere"); err == nil || !strings.Contains(err.Error(), "is for https://gitlab.com") {
		t.Fatalf("one that does not cover it: %v", err)
	}
	// The application's own credential, when a record is kept for the
	// repository, even without --app.
	rec := apps.Record{Cluster: "local", Namespace: "web", Name: "shop", Source: apps.Source{Repo: repo, Ref: "main", RefKind: apps.RefBranch}, Credential: org.ID, CredMigrated: true}
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	if a, auth, err := choose(line); err != nil || auth.Password != "ghp_org" || a.cred != org.ID {
		t.Fatalf("the application's: %+v %+v %v", a, auth, err)
	}
}
