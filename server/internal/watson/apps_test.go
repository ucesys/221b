package watson

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/ucesys/221b/server/internal/apps"
)

// gitChart makes a repository with a small chart at charts/web, tagged
// v1.0.0, and returns its path. over replaces or adds files.
func gitChart(t *testing.T, over ...map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"charts/web/Chart.yaml":  "apiVersion: v2\nname: web\nversion: 1.0.0\nappVersion: \"1.25\"\n",
		"charts/web/values.yaml": "replicas: 1\nimage: nginx:1.25\n",
		"charts/web/templates/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-web
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels: {app: {{ .Release.Name }}}
  template:
    metadata:
      labels: {app: {{ .Release.Name }}}
    spec:
      containers:
        - name: web
          image: {{ .Values.image }}
`,
		"charts/web/templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-cm\ndata:\n  k: v\n",
	}
	for _, m := range over {
		for name, text := range m {
			files[name] = text
		}
	}
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	h, err := w.Commit("the chart", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateTag("v1.0.0", h, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

// HA-06 to HA-08: helm install from a git source against the simulator,
// with the application's saved values; the record then carries the
// commit; an upgrade with --app applies new parameters; the uninstall
// removes the release and the record. The lines are what Parse accepts.
func TestHelmInstallUpgradeAndUninstallFromGitAgainstTheSimulator(t *testing.T) {
	debugPanics = true
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	tg, _ := simTarget(t)
	tg.Cluster = "local"
	store := apps.NewMemoryStore()
	tg.Apps = store
	repo := gitChart(t)
	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "v1.0.0", RefKind: apps.RefTag}
	if err := store.Save(context.Background(), apps.Record{Cluster: "local", Namespace: "web", Name: "hello", Source: src, Values: "replicas: 2\n", Params: []string{"image=nginx:1.25-alpine"}}, nil); err != nil {
		t.Fatal(err)
	}
	helmLine := func(verb string, extra string) string {
		return "helm " + verb + " hello --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web --app" + extra
	}
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

	// The dry run says what it would do and changes nothing.
	out, code := run(helmLine("install", " --dry-run=server"))
	if code != 0 || !strings.Contains(out, "DRY RUN: install hello") || !strings.Contains(out, "replicas: 2") || !strings.Contains(out, "image: nginx:1.25-alpine") {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if rec, _, _ := store.Get(context.Background(), "local", "web", "hello"); rec.Commit != "" {
		t.Errorf("a dry run moved the record to commit %s", rec.Commit)
	}

	out, code = run(helmLine("install", ""))
	if code != 0 || !strings.Contains(out, "STATUS: deployed") || !strings.Contains(out, "REVISION: 1") {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	rec, _, err := store.Get(context.Background(), "local", "web", "hello")
	if err != nil || len(rec.Commit) != 40 || rec.ChartVersion != "1.0.0" || rec.CommitSubject != "the chart" {
		t.Fatalf("record after install: %+v %v", rec, err)
	}
	out, _ = run("helm history hello -n web")
	if !strings.Contains(out, "web-1.0.0") || !strings.Contains(out, "221B: ") {
		t.Errorf("history: %s", out)
	}

	// A second install is refused; an upgrade with a new parameter goes through.
	if _, code := run(helmLine("install", "")); code == 0 {
		t.Error("installing twice succeeded")
	}
	out, code = run(helmLine("upgrade", " --set replicas=3"))
	if code != 0 || !strings.Contains(out, "REVISION: 2") {
		t.Fatalf("upgrade: exit %d\n%s", code, out)
	}
	// The saved token and values go to the saved repository, never another.
	out, code = run("helm upgrade hello --git https://evil.invalid/x --ref main -n web --app --dry-run=client")
	if code == 0 || !strings.Contains(out, "cannot take its token") {
		t.Fatalf("another repository with --app: exit %d\n%s", code, out)
	}

	out, code = run("helm uninstall hello -n web")
	if code != 0 || !strings.Contains(out, `release "hello" uninstalled`) {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if _, _, err := store.Get(context.Background(), "local", "web", "hello"); err == nil {
		t.Error("the record outlived the release")
	}
	if _, code := run("helm uninstall hello -n web"); code == 0 {
		t.Error("uninstalling twice succeeded")
	}

	// Without a record, --app says so; without --git, the source is missing.
	if out, code := run("helm install other --path charts/web --ref main -n web --app"); code == 0 || !strings.Contains(out, "no application web/other") {
		t.Errorf("no record: exit %d %s", code, out)
	}
}

// appLines runs helm lines against the simulator.
func appLines(t *testing.T, store apps.Store) func(line string) (string, int) {
	t.Helper()
	debugPanics = true
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	tg, _ := simTarget(t)
	tg.Cluster, tg.Apps = "local", store
	return func(line string) (string, int) {
		t.Helper()
		c, err := Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		var out bytes.Buffer
		code := runHelm(context.Background(), c, tg, &out, &out)
		return out.String(), code
	}
}

// A deploy from a line without --app moves the record to what it
// deployed and takes nothing else from it: its values, parameters,
// commands and the rest stay for the next Redeploy, and the token stays
// with its repository.
func TestADeployWithoutAppKeepsTheRecord(t *testing.T) {
	store := apps.NewMemoryStore()
	run := appLines(t, store)
	ctx := context.Background()
	repo := gitChart(t)
	line := "helm upgrade hello --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web"
	if out, code := run("helm install hello --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web"); code != 0 {
		t.Fatalf("install: %s", out)
	}
	rec, _, err := store.Get(ctx, "local", "web", "hello")
	if err != nil {
		t.Fatal(err)
	}
	created := rec.Created
	rec.Values, rec.Params, rec.ValuesFile, rec.IgnoreDefaults = "replicas: 2\n", []string{"a=b"}, "values-prod.yaml", true
	rec.Fleet, rec.FleetLabel, rec.CreateNamespace = "gpu", "pool=gpu", true
	rec.Commands = []apps.Command{{Name: "scale", Params: []string{"replicas=5"}}}
	rec.Commit = "old"
	token := "the-saved-token"
	if err := store.Save(ctx, rec, &token); err != nil {
		t.Fatal(err)
	}
	if out, code := run(line); code != 0 {
		t.Fatalf("upgrade: %s", out)
	}
	got, tok, err := store.Get(ctx, "local", "web", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commit) != 40 || got.ChartVersion != "1.0.0" || !got.Updated.After(rec.Updated) {
		t.Fatalf("the deploy fields: %+v", got)
	}
	if got.Values != rec.Values || len(got.Params) != 1 || got.ValuesFile != rec.ValuesFile || !got.IgnoreDefaults || got.Fleet != "gpu" || got.FleetLabel != "pool=gpu" || !got.CreateNamespace || len(got.Commands) != 1 || !got.Created.Equal(created) {
		t.Fatalf("the rest of the record: %+v", got)
	}
	if tok != token {
		t.Fatal("the deploy lost the token")
	}

	// Moved to another repository, the token must not follow it there.
	other := gitChart(t)
	out, code := run("helm upgrade hello --git " + other + " --path charts/web --ref v1.0.0 --tag -n web")
	if code != 0 || !strings.Contains(out, "token saved for the old one was removed") || strings.Contains(out, token) {
		t.Fatalf("another repository: %d %s", code, out)
	}
	got, tok, _ = store.Get(ctx, "local", "web", "hello")
	if got.Source.Repo != other || tok != "" || got.Values != rec.Values {
		t.Fatalf("after moving: %+v, token %q", got, tok)
	}
}

// changedDuringDeploy is an Admin saving the Values tab while a deploy
// runs, at the worst moment: just after the deploy read the record for
// its final save, and once before that.
type changedDuringDeploy struct {
	apps.Store
	gets int
}

func (s *changedDuringDeploy) Get(ctx context.Context, cluster, namespace, name string) (apps.Record, string, error) {
	r, tok, err := s.Store.Get(ctx, cluster, namespace, name)
	s.gets++
	if err == nil && s.gets == 2 {
		other := r
		other.Values = "replicas: 7\n"
		other.Commands = []apps.Command{{Name: "added", Params: []string{"a=b"}}}
		if err := s.Store.Save(ctx, other, nil); err != nil {
			return r, tok, err
		}
	}
	return r, tok, err
}

// A save made during a long upgrade stands: the deploy changes the record
// as it is when the deploy ends, and only its own fields.
func TestASaveDuringADeployStands(t *testing.T) {
	mem := apps.NewMemoryStore()
	store := &changedDuringDeploy{Store: mem}
	run := appLines(t, store)
	ctx := context.Background()
	repo := gitChart(t)
	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "v1.0.0", RefKind: apps.RefTag}
	if err := mem.Save(ctx, apps.Record{Cluster: "local", Namespace: "web", Name: "hello", Source: src, Values: "replicas: 2\n"}, nil); err != nil {
		t.Fatal(err)
	}
	if out, code := run("helm install hello --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web --app --set image=nginx:1.26"); code != 0 {
		t.Fatalf("install: %s", out)
	}
	got, _, err := mem.Get(ctx, "local", "web", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got.Values != "replicas: 7\n" || len(got.Commands) != 1 {
		t.Fatalf("the save made meanwhile went: %+v", got)
	}
	if len(got.Commit) != 40 || got.ChartVersion != "1.0.0" {
		t.Fatalf("the deploy was not recorded: %+v", got)
	}
	if len(got.Params) != 0 {
		t.Fatalf("a --set for this deploy joined the saved parameters: %v", got.Params)
	}
}

// A dependency the chart's values turn on and the repository does not
// vendor is refused; turned off, it is no matter.
func TestADeployOfAChartMissingAnEnabledDependencyIsRefused(t *testing.T) {
	store := apps.NewMemoryStore()
	run := appLines(t, store)
	repo := gitChart(t, map[string]string{
		"charts/web/Chart.yaml":  "apiVersion: v2\nname: web\nversion: 1.0.0\ndependencies:\n  - name: postgresql\n    version: 15.x.x\n    repository: https://charts.example\n    condition: postgresql.enabled\n",
		"charts/web/values.yaml": "replicas: 1\nimage: nginx:1.25\npostgresql:\n  enabled: true\n",
	})
	line := "helm install hello --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web"
	if out, code := run(line); code == 0 || !strings.Contains(out, "does not vendor") || !strings.Contains(out, "postgresql") {
		t.Fatalf("enabled: exit %d\n%s", code, out)
	}
	if out, code := run(line + " --set postgresql.enabled=false"); code != 0 {
		t.Fatalf("disabled: exit %d\n%s", code, out)
	}
}
