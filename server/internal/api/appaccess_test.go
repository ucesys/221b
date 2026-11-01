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
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// appsSetup is a console with Watson on the simulator, and a record of
// team-b/pay with a saved token. A person is denied team-b unless they
// are named in allow.
func appsSetup(t *testing.T, deny map[string]bool) (*httptest.Server, *http.Client, *apps.MemoryStore, *source) {
	t.Helper()
	store := apps.NewMemoryStore()
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, _, local, liveStore := multiSetupWith(t, "ops@corp.example", deny, func(o *Options) {
		o.Apps = store
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond})
	})
	ctx := context.Background()
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	token := "saved-token"
	rec := apps.Record{Cluster: clusters.LocalID, Namespace: "team-b", Name: "pay", Source: apps.Source{Repo: "https://git.invalid/team-b/pay", Ref: "main", RefKind: apps.RefBranch}, Values: "password: hidden\n"}
	if err := store.Save(ctx, rec, &token); err != nil {
		t.Fatal(err)
	}
	return srv, c, store, local
}

// A person with a role somewhere, and no rights in team-b, gets the same
// 403 from everything that changes or inspects an application there,
// whether a record is kept or not, and the record stays as it was.
func TestAnAdminOfOneNamespaceTouchesNoApplicationOfAnother(t *testing.T) {
	srv, c, store, _ := appsSetup(t, map[string]bool{"secrets/team-b": true})
	base := srv.URL + "/api/v1/apps/team-b/"
	ctx := context.Background()
	before, tok, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay")
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "%s/inspect", ""},
		{http.MethodPut, "%s", `{}`},
		{http.MethodPut, "%s", `{"source":{"repo":"https://other.example/x/y","ref":"main"}}`},
		{http.MethodPut, "%s", `not json`},
		{http.MethodDelete, "%s?record=1", ""},
		{http.MethodDelete, "%s", ""},
		{http.MethodPost, "%s/upgrade", `{}`},
		{http.MethodPut, "%s/commands/x", `{"params":["a=b"]}`},
		{http.MethodDelete, "%s/commands/x", ""},
		{http.MethodPost, "%s/commands/x/run", ""},
	} {
		var answers []string
		for _, name := range []string{"pay", "nothing-kept"} {
			resp, body := send(t, c, req.method, base+strings.Replace(req.path, "%s", name, 1), req.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s: %s %s", req.method, name, resp.Status, body)
			}
			answers = append(answers, body)
		}
		if answers[0] != answers[1] || strings.Contains(answers[0], "hidden") || strings.Contains(answers[0], "git.invalid") {
			t.Errorf("%s %s: %q and %q", req.method, req.path, answers[0], answers[1])
		}
	}
	resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps", `{"namespace":"team-b","name":"pay","source":{"repo":"https://other.example/x/y","ref":"main"}}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("creating: %s %s", resp.Status, body)
	}
	after, tokAfter, err := store.Get(ctx, clusters.LocalID, "team-b", "pay")
	if err != nil || after.Source != before.Source || after.Values != before.Values || after.Updated != before.Updated || tokAfter != tok {
		t.Fatalf("the record changed: %+v %q %v", after, tokAfter, err)
	}
}

func TestAViewerCannotInspectAnApplication(t *testing.T) {
	srv, _, _, _ := appsSetup(t, nil)
	v := signIn(t, srv, "viewer@corp.example")
	if resp, body := send(t, v, http.MethodGet, srv.URL+"/api/v1/apps/team-b/pay/inspect", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("%s %s", resp.Status, body)
	}
}

func TestWatsonsAppFlagAsksKubernetesForTheNamespace(t *testing.T) {
	srv, c, _, _ := appsSetup(t, map[string]bool{"secrets/team-b": true})
	resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands", `{"command":"helm upgrade pay -n team-b --app --dry-run=client"}`)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "does not let you manage applications in team-b") || strings.Contains(body, "git.invalid") {
		t.Fatalf("%s %s", resp.Status, body)
	}
}

func TestMovingToAnotherRepositoryRemovesTheSavedToken(t *testing.T) {
	srv, c, store, _ := appsSetup(t, nil)
	url := srv.URL + "/api/v1/apps/team-b/pay"
	ctx := context.Background()
	// The same repository keeps it, and says nothing.
	resp, body := send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/pay","ref":"v2","refKind":"tag"}}`)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "tokenRemoved") || !strings.Contains(body, `"hasToken":true`) {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if _, tok, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay"); tok != "saved-token" {
		t.Fatalf("the same repository lost its token: %q", tok)
	}
	// Another one with a token of its own takes that.
	resp, body = send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/moved","ref":"main"},"token":"new-token"}`)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "tokenRemoved") {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if _, tok, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay"); tok != "new-token" {
		t.Fatalf("%q", tok)
	}
	// Another one without a token drops the saved one, and says so.
	resp, body = send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/third","ref":"main"}}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"tokenRemoved":true`) || strings.Contains(body, `"hasToken"`) {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if rec, tok, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay"); tok != "" || rec.HasToken {
		t.Fatalf("the token stayed: %q", tok)
	}
}

func TestAFailedInstallReplacedWithAnotherRepositoryLeavesTheTokenBehind(t *testing.T) {
	srv, c, store, _ := appsSetup(t, nil)
	resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps", `{"namespace":"team-b","name":"pay","source":{"repo":"https://git.invalid/other/place","ref":"main"}}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if _, tok, _ := store.Get(context.Background(), clusters.LocalID, "team-b", "pay"); tok != "" {
		t.Fatalf("the token stayed: %q", tok)
	}
}

func TestAnApplicationsCommandQuotesItsChartPath(t *testing.T) {
	line, err := appLine("upgrade", "web", "shop", apps.Source{Repo: "https://git.invalid/a/b", Path: "charts/shop", Ref: "main", RefKind: apps.RefBranch})
	if err != nil || !strings.Contains(line, "--path 'charts/shop' --ref main") {
		t.Fatalf("%q %v", line, err)
	}
	if parts := watson.SplitCommands(line); len(parts) != 1 {
		t.Fatalf("%q", parts)
	}
}

func TestARecordWithAPathTheChecksRefuseDeploysNothing(t *testing.T) {
	srv, c, store, _ := appsSetup(t, nil)
	ctx := context.Background()
	rec, _, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay")
	rec.Source.Path = "charts/x\nmore"
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps/team-b/pay/upgrade", `{}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "chart path") {
		t.Fatalf("%s %s", resp.Status, body)
	}
}

func TestASavedSourceIsCheckedForItsPathAndHost(t *testing.T) {
	srv, c, _, _ := appsSetup(t, nil)
	for _, in := range []string{
		`{"source":{"repo":"https://git.invalid/a/b","path":"x --no-hooks","ref":"main"}}`,
		`{"source":{"repo":"https://git.invalid/a/b","path":"x\nhelm uninstall y","ref":"main"}}`,
		`{"source":{"repo":"https://localhost/a/b","ref":"main"}}`,
		`{"source":{"repo":"https://169.254.169.254/a/b","ref":"main"}}`,
	} {
		if resp, body := send(t, c, http.MethodPut, srv.URL+"/api/v1/apps/team-b/pay", in); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s %s", in, resp.Status, body)
		}
	}
}
