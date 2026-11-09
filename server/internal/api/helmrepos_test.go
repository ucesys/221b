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
	"github.com/ucesys/221b/server/internal/apps/repotest"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Helm repositories (Phase 12, wave 5) ---

// HR-02 to HR-09: a SuperAdmin keeps the chart repositories with Test;
// the password is written and never read back, nor audited; those who
// manage applications read the charts and versions; a new application
// from a repository's chart becomes a Watson card naming repo/chart and
// its version.
func TestHelmRepositoriesAndAnApplicationFromOne(t *testing.T) {
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	f := repotest.New()
	t.Cleanup(f.Close)
	f.User, f.Password = "reader", "hunter2-secret"
	f.Add("shop", "1.0.0", map[string]string{"values.yaml": "color: blue\n", "templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-cm\ndata:\n  color: {{ .Values.color | quote }}\n"})
	f.Add("shop", "1.1.0", map[string]string{"values.yaml": "color: green\n", "templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-cm\n"})
	repos := apps.NewRepos(apps.RepoOptions{Client: f.Client()})
	store := apps.NewMemoryStore()
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, auditOut, local, liveStore := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps, o.Repos = store, repos
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond, Apps: store, Repos: repos})
	})
	ctx := context.Background()
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/v1/helm-repositories"
	repo := `{"kind":"helm","url":"` + f.URL + `/charts","auth":"basic","username":"reader","secret":"hunter2-secret"}`

	// Test before saving: the wrong password, then the right one.
	if resp, body := send(t, c, http.MethodPost, base+"/test", `{"kind":"helm","url":"`+f.URL+`/charts","auth":"basic","username":"reader","secret":"nope"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "refused the credentials") {
		t.Fatalf("a test with the wrong password: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPost, base+"/test", repo); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"charts":1`) {
		t.Fatalf("a test: %s %s", resp.Status, body)
	}
	resp, body := send(t, c, http.MethodPut, base+"/shop-charts", repo)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"hasSecret":true`) || strings.Contains(body, "hunter2") {
		t.Fatalf("save: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPut, base+"/Bad%20Name", repo); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad name: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPost, base+"/shop-charts/test", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("retest: %s %s", resp.Status, body)
	}
	resp, body = send(t, c, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"shop-charts"`) || !strings.Contains(body, `"testOk":true`) || !strings.Contains(body, `"mayEdit":true`) || strings.Contains(body, "hunter2") {
		t.Fatalf("list: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodGet, base+"/shop-charts/charts", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"shop"`) || !strings.Contains(body, `"latest":"1.1.0"`) {
		t.Fatalf("charts: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPost, base+"/versions", `{"kind":"helm","repository":"shop-charts","chart":"shop"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"versions":["1.1.0","1.0.0"]`) || !strings.Contains(body, `"newest":"1.1.0"`) {
		t.Fatalf("versions: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps/inspect", `{"kind":"helm","repository":"shop-charts","chart":"shop","ref":"1.0.0"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ref":"1.0.0"`) || !strings.Contains(body, `"commit":"sha256:`) {
		t.Fatalf("inspect: %s %s", resp.Status, body)
	}

	// A new application from the chart: a Watson card, and the record.
	resp, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/apps", `{"namespace":"web","name":"storefront","source":{"kind":"helm","repository":"shop-charts","chart":"shop","ref":"1.0.0"},"values":"color: red\n"}`)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(body, "helm install storefront shop-charts/shop --version 1.0.0 -n web --app") {
		t.Fatalf("a new application: %s %s", resp.Status, body)
	}
	rec, _, err := store.Get(ctx, clusters.LocalID, "web", "storefront")
	if err != nil || rec.Source.Kind != apps.KindHelm || rec.Source.Repo != f.URL+"/charts" || rec.HasToken {
		t.Fatalf("record: %+v %v", rec, err)
	}
	if resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps", `{"namespace":"web","name":"x","source":{"kind":"helm","repository":"shop-charts","chart":"shop","ref":"1.0.0"},"token":"abc"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a token with a repository chart: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps", `{"namespace":"web","name":"x","source":{"kind":"helm","repository":"nowhere","chart":"shop"}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown repository: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/apps/web/storefront/refs", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"tags":["1.1.0","1.0.0"]`) || !strings.Contains(body, `"newerTags":["1.1.0"]`) {
		t.Fatalf("refs: %s %s", resp.Status, body)
	}

	// A viewer reads none of it; an Admin who is not a SuperAdmin changes none.
	v := signIn(t, srv, "viewer@corp.example")
	if resp, _ := send(t, v, http.MethodGet, base, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer lists the repositories: %s", resp.Status)
	}
	if resp, _ := send(t, v, http.MethodPut, base+"/x", repo); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer saves a repository: %s", resp.Status)
	}
	if resp, _ := send(t, c, http.MethodDelete, base+"/shop-charts", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %s", resp.Status)
	}
	if resp, _ := send(t, c, http.MethodDelete, base+"/shop-charts", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("remove again: %s", resp.Status)
	}
	got := auditOut.String()
	for _, want := range []string{`"action":"helmrepo.save"`, `"action":"helmrepo.test"`, `"action":"helmrepo.remove"`, "sign-in basic, one kept"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the audit log lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "hunter2") {
		t.Fatal("the audit log holds the password")
	}
}
