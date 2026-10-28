package apps_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/ucesys/221b/server/internal/apps"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
)

func TestNormalizeCompletesAndRefusesSources(t *testing.T) {
	s, err := apps.Normalize(apps.Source{Repo: "github.com/ucesys/221b.git/", Path: "/deploy/helm/221b/", Ref: "v0.9.1", RefKind: apps.RefTag})
	if err != nil || s.Repo != "https://github.com/ucesys/221b" || s.Path != "deploy/helm/221b" || s.String() != "github.com/ucesys/221b/deploy/helm/221b @ v0.9.1" {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err := apps.Normalize(apps.Source{Repo: "https://github.com/ucesys/221b", Ref: "main"}); err != nil || s.RefKind != apps.RefBranch || s.Path != "" {
		t.Errorf("default kind: %+v %v", s, err)
	}
	for _, bad := range []apps.Source{
		{Repo: "", Ref: "main"},
		{Repo: "http://github.com/x/y", Ref: "main"},
		{Repo: "https://user:pw@github.com/x/y", Ref: "main"},
		{Repo: "https://github.com", Ref: "main"},
		{Repo: "https://github.com/x/y", Path: "../etc", Ref: "main"},
		{Repo: "https://github.com/x/y", Ref: ""},
		{Repo: "https://github.com/x/y", Ref: "a..b"},
		{Repo: "https://github.com/x/y", Ref: "-x"},
		{Repo: "https://github.com/x/y", Ref: "main", RefKind: "commit"},
	} {
		if _, err := apps.Normalize(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// chartRepo makes a repository with a small chart: v1.0.0 runs nginx:1.25
// and v1.1.0 nginx:1.26 with a second replica. It returns the path.
func chartRepo(t *testing.T) string {
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
	write := func(name, text string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		if _, err := w.Add("."); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}}); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "not a chart\n")
	write("charts/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 1.0.0\nappVersion: \"1.25\"\n")
	write("charts/web/values.yaml", "replicas: 1\nimage: nginx:1.25\n")
	write("charts/web/.helmignore", "notes.txt\n")
	write("charts/web/notes.txt", "ignored\n")
	write("charts/web/templates/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-web
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels: {app: web}
  template:
    metadata:
      labels: {app: web}
    spec:
      initContainers:
        - name: init
          image: busybox:1.37
      containers:
        - name: web
          image: {{ .Values.image }}
`)
	commit("the chart")
	head, _ := r.Head()
	if _, err := r.CreateTag("v1.0.0", head.Hash(), nil); err != nil {
		t.Fatal(err)
	}
	write("charts/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 1.1.0\nappVersion: \"1.26\"\n")
	write("charts/web/values.yaml", "replicas: 2\nimage: nginx:1.26\n")
	commit("nginx 1.26, two replicas")
	head, _ = r.Head()
	if _, err := r.CreateTag("v1.1.0", head.Hash(), nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFetchRenderImagesAndCompare(t *testing.T) {
	repo := chartRepo(t)
	ctx := context.Background()
	refs, err := apps.ListRefs(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(refs.Tags, " ") != "v1.1.0 v1.0.0" || len(refs.Branches) != 1 || refs.Default != refs.Branches[0] {
		t.Errorf("refs %+v", refs)
	}
	if got := apps.NewerTags("v1.0.0", refs.Tags); strings.Join(got, " ") != "v1.1.0" {
		t.Errorf("newer than v1.0.0: %v", got)
	}
	if got := apps.NewerTags("main", refs.Tags); got != nil {
		t.Errorf("a branch has no newer tags: %v", got)
	}

	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "v1.0.0", RefKind: apps.RefTag}
	co, err := apps.Fetch(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if co.Commit == "" || co.CommitSubject != "the chart" || co.CommitDate.Year() != 2026 {
		t.Errorf("checkout %+v", co)
	}
	ch, err := apps.LoadChart(co, "charts/web")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Metadata.Version != "1.0.0" {
		t.Errorf("chart %s", ch.Metadata.Version)
	}
	for _, f := range ch.Files {
		if f.Name == "notes.txt" {
			t.Error(".helmignore was not honoured")
		}
	}
	vals, err := apps.Values("replicas: 3\n", []string{"image=nginx:1.25-alpine"})
	if err != nil {
		t.Fatal(err)
	}
	one, err := apps.Render(ctx, ch, vals, "shop", "web", "v1.37.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.Manifest, "replicas: 3") || !strings.Contains(one.Manifest, "image: nginx:1.25-alpine") {
		t.Errorf("values and parameters: %s", one.Manifest)
	}
	images := apps.Images(one.Manifest)
	if len(images) != 2 || images[0].Image != "busybox:1.37" || !images[0].Init || images[1].Repository != "nginx" || images[1].Tag != "1.25-alpine" || images[1].Kind != "Deployment" || images[1].Name != "shop-web" || images[1].Container != "web" {
		t.Errorf("images %+v", images)
	}

	// Missing dirs, missing Chart.yaml and missing refs are said plainly.
	if _, err := apps.LoadChart(co, "charts/nope"); err == nil || !strings.Contains(err.Error(), "no directory") {
		t.Errorf("missing dir: %v", err)
	}
	if _, err := apps.LoadChart(co, ""); err == nil || !strings.Contains(err.Error(), "Chart.yaml") {
		t.Errorf("no chart: %v", err)
	}
	if _, err := apps.Fetch(ctx, apps.Source{Repo: repo, Ref: "v9.9.9", RefKind: apps.RefTag}, ""); err == nil || !strings.Contains(err.Error(), "no tag v9.9.9") {
		t.Errorf("missing tag: %v", err)
	}

	// The newer tag changes the deployment; the diff says which object.
	two, err := apps.Fetch(ctx, apps.Source{Repo: repo, Path: "charts/web", Ref: "v1.1.0", RefKind: apps.RefTag}, "")
	if err != nil {
		t.Fatal(err)
	}
	ch2, err := apps.LoadChart(two, "charts/web")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := apps.Render(ctx, ch2, nil, "shop", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	d := apps.Compare(one.Manifest, r2.Manifest, "deployed", "v1.1.0")
	if d.Same || strings.Join(d.Changed, ",") != "Deployment shop-web" || len(d.Added)+len(d.Removed) != 0 || !strings.Contains(d.Text, "-          image: nginx:1.25-alpine") || !strings.Contains(d.Text, "+          image: nginx:1.26") {
		t.Errorf("diff %+v", d)
	}
	if d := apps.Compare(one.Manifest, one.Manifest, "a", "b"); !d.Same || d.Text != "" {
		t.Errorf("same manifests differ: %+v", d)
	}
	// A Secret's values never reach a diff: a change shows as its size.
	a := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  pw: PLANTED-ONE\n"
	b := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  pw: PLANTED-TWO-LONGER\n"
	if d := apps.Compare(a, b, "a", "b"); d.Same || strings.Contains(d.Text, "PLANTED") || !strings.Contains(d.Text, "bytes, hidden by 221B") {
		t.Errorf("secret diff: %+v", d)
	}
	if d := apps.Compare(a, a, "a", "b"); !d.Same {
		t.Errorf("same secret differs: %+v", d)
	}
}

func TestParsedImagesAndParams(t *testing.T) {
	m := "kind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n    - name: a\n      image: registry.example.com:5000/team/app@sha256:abc\n    - name: b\n      image: ghcr.io/ucesys/221b:0.9.1\n"
	im := apps.Images(m)
	if len(im) != 2 || im[0].Registry != "ghcr.io" || im[0].Repository != "ucesys/221b" || im[0].Tag != "0.9.1" || im[1].Registry != "registry.example.com:5000" || im[1].Repository != "team/app" || im[1].Digest != "sha256:abc" || im[1].Tag != "" {
		t.Errorf("%+v", im)
	}
	ps, err := apps.ParseParams("a.b=1\n\n# note\nimage.tag=v2\n")
	if err != nil || strings.Join(ps, " ") != "a.b=1 image.tag=v2" {
		t.Errorf("%v %v", ps, err)
	}
	if _, err := apps.ParseParams("novalue"); err == nil {
		t.Error("a parameter without = was accepted")
	}
	if _, err := apps.Values("a: [", nil); err == nil {
		t.Error("bad YAML was accepted")
	}
}

func TestDriftComparesWhatTheManifestSets(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(gvk, meta.RESTScopeNamespace)
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "shop-web", "namespace": "web", "labels": map[string]any{"app.kubernetes.io/managed-by": "Helm"}, "uid": "x"},
		"spec":     map[string]any{"replicas": int64(5), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": "nginx:1.25", "imagePullPolicy": "IfNotPresent"}}}}},
		"status":   map[string]any{"replicas": int64(5)},
	}}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList"}, live)
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: shop-web
  creationTimestamp: null
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: web
          image: nginx:1.25
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gone
spec: {}
---
apiVersion: v1
kind: Secret
metadata:
  name: unknown-kind
stringData:
  pw: x
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: texty
  annotations:
    note: |
      two lines,
      ending with a newline
`
	// A text value that ends the manifest keeps its closing newline.
	dyn = dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList"}, live, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "texty", "namespace": "web", "annotations": map[string]any{"note": "two lines,\nending with a newline\n"}},
	}})
	d := apps.CheckDrift(context.Background(), dyn, mapper, manifest, "web", time.Now())
	if d.Changed != 1 || d.Missing != 1 || d.Unknown != 1 || d.Same != 1 || d.State() != "drifted" {
		t.Fatalf("%+v", d)
	}
	if d.Objects[3].State != apps.DriftSame {
		t.Errorf("the closing newline of a block scalar: %+v", d.Objects[3])
	}
	if o := d.Objects[0]; o.State != apps.DriftChanged || strings.Join(o.Fields, ",") != "spec.replicas" {
		t.Errorf("changed object %+v", o)
	}
	if d.Objects[1].State != apps.DriftMissing || d.Objects[2].State != apps.DriftUnknown {
		t.Errorf("%+v", d.Objects[1:])
	}
	// The same replicas: in sync, defaults and status ignored.
	same := apps.CheckDrift(context.Background(), dyn, mapper, strings.Replace(manifest, "replicas: 1", "replicas: 5", 1), "web", time.Now())
	if same.Objects[0].State != apps.DriftSame {
		t.Errorf("%+v", same.Objects[0])
	}
}
