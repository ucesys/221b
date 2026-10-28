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
// v1.0.0, and returns its path.
func gitChart(t *testing.T) string {
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
	for name, text := range map[string]string{
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
	} {
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
