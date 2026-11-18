package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// appsSetup is a console with Watson on the simulator, and a record of
// team-b/pay with a saved token. A person is denied team-b unless they
// are named in allow.
func appsSetup(t *testing.T, deny map[string]bool) (*httptest.Server, *http.Client, *apps.MemoryStore, *source) {
	t.Helper()
	srv, c, store, local, _ := appsSetupCreds(t, deny)
	return srv, c, store, local
}

// appsSetupCreds is appsSetup with Administration › Credentials (CR-11).
func appsSetupCreds(t *testing.T, deny map[string]bool) (*httptest.Server, *http.Client, *apps.MemoryStore, *source, *credentials.Service) {
	t.Helper()
	store := apps.NewMemoryStore()
	creds := credentials.New(credentials.Options{})
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, _, local, liveStore := multiSetupWith(t, "ops@corp.example", deny, func(o *Options) {
		o.Apps, o.Credentials = store, creds
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond, Credentials: creds})
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
	return srv, c, store, local, creds
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

// An application names its credential (CR-11): one that covers its new
// repository goes with it, one that does not is let go and said so, and
// one that does not cover the repository named is refused.
func TestMovingToAnotherRepositoryLetsItsCredentialGo(t *testing.T) {
	srv, c, store, _, creds := appsSetupCreds(t, nil)
	url := srv.URL + "/api/v1/apps/team-b/pay"
	ctx := context.Background()
	// The same repository keeps the token of before 0.12.1, and says nothing.
	resp, body := send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/pay","ref":"v2","refKind":"tag"}}`)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "credentialRemoved") || !strings.Contains(body, `"hasToken":true`) {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if _, tok, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay"); tok != "saved-token" {
		t.Fatalf("the same repository lost its token: %q", tok)
	}
	team, err := creds.Create(ctx, credentials.Credential{Name: "team-b", Kind: credentials.KindToken, Endpoint: "https://git.invalid/team-b", SendAs: credentials.SendAs{Method: credentials.SendBasic, Username: "x-access-token"}}, credentials.Secret{Token: "team-token"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := creds.Create(ctx, credentials.Credential{Name: "other", Kind: credentials.KindToken, Endpoint: "https://elsewhere.example"}, credentials.Secret{Token: "x"}, "test")
	// A credential that does not cover the repository is refused.
	resp, body = send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/moved","ref":"main"},"credential":"`+other.ID+`"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "is for https://elsewhere.example") {
		t.Fatalf("%s %s", resp.Status, body)
	}
	// Another repository with a credential that covers it takes that.
	resp, body = send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/moved","ref":"main"},"credential":"`+team.ID+`"}`)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "credentialRemoved") || strings.Contains(body, "team-token") {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if rec, _, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay"); rec.Credential != team.ID || !rec.CredMigrated {
		t.Fatalf("%+v", rec)
	}
	// One its credential does not cover lets it go, and says so.
	resp, body = send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/other/third","ref":"main"}}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"credentialRemoved":true`) {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if rec, _, _ := store.Get(ctx, clusters.LocalID, "team-b", "pay"); rec.Credential != "" {
		t.Fatalf("the credential stayed: %+v", rec)
	}
	// Its Used by names the application while it does.
	resp, body = send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/team-b/pay","ref":"main"},"credential":"`+team.ID+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s", resp.Status, body)
	}
	used, _ := creds.UsedBy(ctx)
	if len(used[team.ID]) != 1 || used[team.ID][0].What != "app team-b/pay" {
		t.Fatalf("used by: %+v", used)
	}
	if _, err := creds.Delete(ctx, team.ID); !errors.Is(err, credentials.ErrInUse) {
		t.Fatalf("deleted while used: %v", err)
	}
}

func TestAFailedInstallReplacedWithAnotherRepositoryLeavesTheTokenBehind(t *testing.T) {
	srv, c, store, _ := appsSetup(t, nil)
	resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps", `{"namespace":"team-b","name":"pay","source":{"repo":"https://git.invalid/other/place","ref":"main"}}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%s %s", resp.Status, body)
	}
	// The record names no credential: the token kept before is not read.
	rec, tok, _ := store.Get(context.Background(), clusters.LocalID, "team-b", "pay")
	if auth, err := apps.AuthFor(context.Background(), nil, rec, tok); err != nil || !auth.None() || !rec.CredMigrated {
		t.Fatalf("the token stayed in use: %+v %v", rec, err)
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

// A person sees an application in a namespace they reach, or one an admin
// with the same namespace access created, without what it is set to.
func TestAnApplicationIsSeenInReachOrThroughTheCreatorsAccess(t *testing.T) {
	srv, c, store, _ := appsSetup(t, map[string]bool{"secrets/team-b": true, "secrets/team-c": true})
	ctx := context.Background()
	mine := []string{"default", "kube-system", "web"}
	shared := apps.Record{Cluster: clusters.LocalID, Namespace: "team-b", Name: "shared", Source: apps.Source{Repo: "https://git.example/a/b", Ref: "main", Path: "."}, Values: "password: hidden", Params: []string{"a=b"}, CreatorNamespaces: mine}
	other := shared
	other.Namespace, other.Name, other.CreatorNamespaces = "team-c", "other", []string{"team-c"}
	for _, rec := range []apps.Record{shared, other} {
		if err := store.Save(ctx, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	r, err := c.Get(srv.URL + "/api/v1/apps")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	var list []apps.Record
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	names := map[string]apps.Record{}
	for _, x := range list {
		names[x.Name] = x
	}
	if _, ok := names["pay"]; ok {
		t.Fatal("a record from before creators were kept showed in a namespace out of reach")
	}
	if _, ok := names["other"]; ok {
		t.Fatal("a record by someone with other access showed")
	}
	got, ok := names["shared"]
	if !ok {
		t.Fatalf("a record by someone with the same access is missing: %s", body)
	}
	if got.Values != "" || len(got.Params) != 0 {
		t.Fatalf("its values showed: %+v", got)
	}
	r2, err := c.Get(srv.URL + "/api/v1/apps/team-c/other")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("the other record answered %d", r2.StatusCode)
	}
}

// The source's side of a diff and the source's refs use the saved token
// and values: a person who may list Secrets in the namespace, and not
// create them, does not manage the application there and is refused.
func TestTheSourceSideOfADiffIsForThoseWhoManageTheApplication(t *testing.T) {
	srv, c, _, _ := appsSetup(t, map[string]bool{"create secrets/team-b": true})
	base := srv.URL + "/api/v1/apps/team-b/pay"
	for _, path := range []string{"/diff?to=source", "/diff?from=source&to=1", "/refs"} {
		resp, body := send(t, c, http.MethodGet, base+path, "")
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "does not let you manage applications in team-b") || strings.Contains(body, "git.invalid") {
			t.Errorf("%s: %s %s", path, resp.Status, body)
		}
	}
}
