package watson

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/repotest"
)

// --- Helm repositories (Phase 12, wave 5) ---

const repoDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-shop
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels: {app: {{ .Release.Name }}}
  template:
    metadata:
      labels: {app: {{ .Release.Name }}}
    spec:
      containers:
        - name: shop
          image: {{ .Values.image }}
`

func chartRegistry(t *testing.T) (*repotest.Registry, *apps.Repos) {
	t.Helper()
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	f := repotest.New()
	t.Cleanup(f.Close)
	f.Add("shop", "1.0.0", map[string]string{"values.yaml": "replicas: 1\nimage: nginx:1.25\n", "templates/deployment.yaml": repoDeployment})
	f.Add("shop", "1.1.0", map[string]string{"values.yaml": "replicas: 3\nimage: nginx:1.27\n", "templates/deployment.yaml": repoDeployment})
	r := apps.NewRepos(apps.RepoOptions{Client: f.Client()})
	if _, _, err := r.Save(context.Background(), "shop-charts", apps.RepositoryInput{Kind: apps.KindHelm, URL: f.URL + "/charts", Auth: apps.AuthNone}, "alice"); err != nil {
		t.Fatal(err)
	}
	return f, r
}

// HR-07: helm install and upgrade of a Helm repository's chart (REPO/CHART
// --version) and of an OCI registry's (oci://...), against the simulator;
// the record keeps the source, the version and the archive's digest.
func TestHelmInstallFromARepositoryAndARegistryAgainstTheSimulator(t *testing.T) {
	debugPanics = true
	f, repos := chartRegistry(t)
	tg, _ := simTarget(t)
	tg.Cluster = "local"
	store := apps.NewMemoryStore()
	tg.Apps, tg.Repos = store, repos
	run := func(line string) (string, int) {
		c, err := Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		var out bytes.Buffer
		code := runHelm(context.Background(), c, tg, &out, &out)
		t.Logf("$ %s  (exit %d)\n%s", line, code, out.String())
		return out.String(), code
	}
	out, code := run("helm install storefront shop-charts/shop --version 1.0.0 -n web --set replicas=2 --dry-run")
	if code != 0 || !strings.Contains(out, "DRY RUN: install storefront from shop-charts/shop @ 1.0.0 at the chart archive sha256:") || !strings.Contains(out, "replicas: 2") {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if out, code = run("helm install storefront shop-charts/shop --version 1.0.0 -n web"); code != 0 || !strings.Contains(out, "STATUS: deployed") {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	rec, _, err := store.Get(context.Background(), "local", "web", "storefront")
	if err != nil || rec.Source.Kind != apps.KindHelm || rec.Source.Repository != "shop-charts" || rec.Source.Chart != "shop" || rec.Source.Ref != "1.0.0" || !strings.HasPrefix(rec.Commit, "sha256:") || rec.ChartVersion != "1.0.0" {
		t.Fatalf("record: %+v %v", rec, err)
	}
	// Without --version, the newest; the record moves with it.
	if out, code = run("helm upgrade storefront shop-charts/shop -n web"); code != 0 || !strings.Contains(out, "REVISION: 2") {
		t.Fatalf("upgrade: exit %d\n%s", code, out)
	}
	if rec, _, _ = store.Get(context.Background(), "local", "web", "storefront"); rec.Source.Ref != "1.1.0" || rec.ChartVersion != "1.1.0" {
		t.Fatalf("after the upgrade: %+v", rec)
	}
	// --app redeploys from the record.
	if out, code = run("helm upgrade storefront -n web --app"); code != 0 || !strings.Contains(out, "REVISION: 3") {
		t.Fatalf("upgrade --app: exit %d\n%s", code, out)
	}
	if _, code = run("helm install other nowhere/shop -n web"); code == 0 {
		t.Fatal("a chart of an unknown repository was installed")
	}
	// An OCI registry's chart.
	if out, code = run("helm install reg oci://" + f.Host() + "/org/charts/shop --version 1.1.0 -n web"); code != 0 || !strings.Contains(out, "STATUS: deployed") {
		t.Fatalf("oci install: exit %d\n%s", code, out)
	}
	if rec, _, _ = store.Get(context.Background(), "local", "web", "reg"); rec.Source.Kind != apps.KindOCI || rec.Source.Repo != "oci://"+f.Host()+"/org/charts" || rec.Source.Ref != "1.1.0" {
		t.Fatalf("oci record: %+v", rec)
	}
	for _, bad := range []string{
		"helm install x shop-charts/shop --git https://github.com/o/r --ref main -n web",
		"helm install x --git https://github.com/o/r --ref main --version 1 -n web",
		"helm install x shop/a/b -n web",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
}

// HR-08: a card of a repository's chart deploys the archive it showed:
// one named without --version keeps the version it settled on, and one
// whose archive changed in the repository since is refused.
func TestARepositoryDeployRunsTheArchiveItsCardShowed(t *testing.T) {
	f, repos := chartRegistry(t)
	svc, cl, store := pinSetup(t, Options{Repos: repos})
	it := card(t, svc, cl, alice(), "helm install hello shop-charts/shop -n web")
	if it.Status != StatusPending || !strings.Contains(strings.Join(it.Plan.Impact, " "), "shop-charts/shop @ 1.1.0, archive sha256:") {
		t.Fatalf("%s %v", it.Status, it.Plan.Impact)
	}
	digest := it.Plan.pin.commit
	// A newer version appears: the card still deploys 1.1.0.
	f.Add("shop", "1.2.0", map[string]string{"values.yaml": "replicas: 9\nimage: nginx:bad\n", "templates/deployment.yaml": repoDeployment})
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, outputOf(svc, it.ID))
	}
	rec, _, err := store.Get(context.Background(), "local", "web", "hello")
	if err != nil || rec.Commit != digest || rec.Source.Ref != "1.1.0" {
		t.Fatalf("record %+v, card %q: %v", rec, digest, err)
	}
	// The archive of a version changed after the card: refused. (Saving
	// the repository again has the console read its index again.)
	again := func() {
		_, _, _ = repos.Save(context.Background(), "shop-charts", apps.RepositoryInput{Kind: apps.KindHelm, URL: f.URL + "/charts", Auth: apps.AuthNone}, "alice")
	}
	again()
	it = card(t, svc, cl, alice(), "helm upgrade hello shop-charts/shop --version 1.2.0 -n web")
	f.Add("shop", "1.2.0", map[string]string{"values.yaml": "replicas: 4\nimage: nginx:other\n", "templates/deployment.yaml": repoDeployment})
	again()
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	done = wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if out := outputOf(svc, it.ID); done.Status != StatusFailed || !strings.Contains(out, "changed in its repository") {
		t.Fatalf("%s: %s", done.Status, out)
	}
}
