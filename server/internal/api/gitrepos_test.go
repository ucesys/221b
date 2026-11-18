package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Settings › Repositories (0.12.1, RP-01 to RP-08) ---

// A SuperAdmin lists a git repository beside the chart repositories; an
// application's Setup names it, by name or by its address, and reads its
// address from it; the list says what uses it, and it is not removed
// while used, which the audit log keeps; a Viewer neither reads nor
// changes the list.
func TestGitRepositoriesInSettingsRepositories(t *testing.T) {
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	raw := apps.NewMemoryStore()
	creds := credentials.New(credentials.Options{})
	repos := apps.NewRepos(apps.RepoOptions{Credentials: creds})
	store := apps.WithRepositories(raw, repos)
	repos.AddUsers(apps.AppRepoUses(store))
	repos.AddRenamer(apps.AppRepoRenamer(store))
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, auditOut, local, liveStore := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps, o.Repos, o.Credentials = store, repos, creds
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond, Apps: store, Repos: repos, Credentials: creds})
	})
	ctx := context.Background()
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	rec := apps.Record{Cluster: clusters.LocalID, Namespace: "team-b", Name: "pay", Source: apps.Source{Repo: "https://git.invalid/other/x", Ref: "main", RefKind: apps.RefBranch}, CredMigrated: true}
	if err := raw.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/v1/helm-repositories"
	resp, body := send(t, c, http.MethodPut, base+"/pay", `{"kind":"git","url":"https://Git.invalid/team-b/pay.git/","defaultBranch":"main"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"url":"https://git.invalid/team-b/pay"`) || !strings.Contains(body, `"defaultBranch":"main"`) {
		t.Fatalf("save: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPut, base+"/pay", `{"kind":"git","url":"http://git.invalid/team-b/pay"}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "https://") {
		t.Fatalf("not https: %s %s", resp.Status, body)
	}
	// The address typed in Setup is the listed repository's: recorded as it.
	app := srv.URL + "/api/v1/apps/team-b/pay"
	if resp, body := send(t, c, http.MethodPut, app, `{"source":{"repo":"https://git.invalid/team-b/pay","path":"chart","ref":"main"}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup by address: %s %s", resp.Status, body)
	}
	got, _, _ := raw.Get(ctx, clusters.LocalID, "team-b", "pay")
	if got.Source.Repository != "pay" || got.Source.Path != "chart" {
		t.Fatalf("%+v", got.Source)
	}
	// By name: the address is the repository's.
	if resp, body := send(t, c, http.MethodPut, app, `{"source":{"repo":"","repository":"pay","path":"chart","ref":"v2","refKind":"tag"}}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"repository":"pay"`) || !strings.Contains(body, `"repo":"https://git.invalid/team-b/pay"`) {
		t.Fatalf("setup by name: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPut, app, `{"source":{"repo":"","repository":"nosuch","ref":"main"}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown repository: %s %s", resp.Status, body)
	}
	other, _ := creds.Create(ctx, credentials.Credential{Name: "team-b", Kind: credentials.KindToken, Endpoint: "https://git.invalid/team-b"}, credentials.Secret{Token: "x"}, "test")
	if resp, body := send(t, c, http.MethodPut, app, `{"source":{"repo":"","repository":"pay","ref":"main"},"credential":"`+other.ID+`"}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "its own credential") {
		t.Fatalf("a credential beside a listed repository: %s %s", resp.Status, body)
	}
	// The address changes once, on the repository: the application reads it.
	if resp, body := send(t, c, http.MethodPut, base+"/pay", `{"kind":"git","url":"https://git.invalid/team-b/payments","credential":"`+other.ID+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("change: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodGet, app, ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"repo":"https://git.invalid/team-b/payments"`) || !strings.Contains(body, `"credential":"`+other.ID+`"`) {
		t.Fatalf("the application: %s %s", resp.Status, body)
	}
	// Used by, and not removed while used.
	resp, body = send(t, c, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"usedBy":["application team-b/pay"]`) || !strings.Contains(body, `"kind":"git"`) {
		t.Fatalf("list: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodDelete, base+"/pay", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "application team-b/pay") {
		t.Fatalf("remove in use: %s %s", resp.Status, body)
	}
	// Renamed: the application follows.
	if resp, body := send(t, c, http.MethodPost, base+"/pay/rename", `{"name":"payments"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"payments"`) {
		t.Fatalf("rename: %s %s", resp.Status, body)
	}
	if got, _, _ := raw.Get(ctx, clusters.LocalID, "team-b", "pay"); got.Source.Repository != "payments" {
		t.Fatalf("the application did not follow: %+v", got.Source)
	}
	if resp, body := send(t, c, http.MethodPost, base+"/payments/rename", `{"name":"Bad Name"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad name: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPost, base+"/migration/dismiss", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("dismiss: %s %s", resp.Status, body)
	}
	// A Viewer reads none of it and changes none of it.
	v := signIn(t, srv, "viewer@corp.example")
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "", ""},
		{http.MethodPut, "/x", `{"kind":"git","url":"https://git.invalid/x/y"}`},
		{http.MethodDelete, "/payments", ""},
		{http.MethodPost, "/payments/rename", `{"name":"x"}`},
		{http.MethodPost, "/migration/dismiss", ""},
	} {
		if resp, body := send(t, v, req.method, base+req.path, req.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a viewer %s %s: %s %s", req.method, req.path, resp.Status, body)
		}
	}
	// Once nothing names it, it goes.
	if err := raw.Delete(ctx, clusters.LocalID, "team-b", "pay"); err != nil {
		t.Fatal(err)
	}
	if resp, body := send(t, c, http.MethodDelete, base+"/payments", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %s %s", resp.Status, body)
	}
	log := auditOut.String()
	for _, want := range []string{`git repository added`, `git repository changed`, `git repository not removed`, `"outcome":"denied"`, `git repository removed`, `renamed from pay`, `"action":"helmrepo.migration"`} {
		if !strings.Contains(log, want) {
			t.Errorf("the audit log lacks %s", want)
		}
	}
}
