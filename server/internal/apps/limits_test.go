package apps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"

	"github.com/ucesys/221b/server/internal/apps"
)

func withLimits(t *testing.T, set func(*apps.Limits)) {
	t.Helper()
	was := apps.FetchLimits
	t.Cleanup(func() { apps.FetchLimits = was })
	set(&apps.FetchLimits)
}

func localRepo(t *testing.T, files map[string]string) (string, *git.Repository) {
	t.Helper()
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	addCommit(t, dir, r, files)
	return dir, r
}

func addCommit(t *testing.T, dir string, r *git.Repository, files map[string]string) {
	t.Helper()
	for name, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit("files", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
}

func master(dir string) apps.Source {
	return apps.Source{Repo: dir, Ref: "master", RefKind: apps.RefBranch}
}

func TestAFetchPastItsLimitsIsRefusedInPlainWords(t *testing.T) {
	// Half a megabyte of one byte: small as sent, large unpacked.
	dir, _ := localRepo(t, map[string]string{"a.txt": "a", "b.txt": "b", "big.bin": strings.Repeat("x", 512<<10)})
	ctx := context.Background()
	if _, err := apps.Fetch(ctx, master(dir), ""); err != nil {
		t.Fatalf("within the limits: %v", err)
	}
	for name, c := range map[string]struct {
		set  func(*apps.Limits)
		says string
	}{
		"pack":     {func(l *apps.Limits) { l.Pack = 64 }, "more than 0 MB"},
		"objects":  {func(l *apps.Limits) { l.Objects = 2 }, "more than 2 objects"},
		"inflated": {func(l *apps.Limits) { l.Inflated = 1024 }, "unpacks to more than"},
		"checkout": {func(l *apps.Limits) { l.Checkout = 1024 }, "come to more than"},
		"files":    {func(l *apps.Limits) { l.Files = 2 }, "more than 2 files"},
	} {
		t.Run(name, func(t *testing.T) {
			withLimits(t, c.set)
			_, err := apps.Fetch(ctx, master(dir), "")
			if !errors.Is(err, apps.ErrFetchLimit) || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestACommitIsFetchedByItsIdWhileTheBranchHasMoved(t *testing.T) {
	dir, r := localRepo(t, map[string]string{"f.txt": "first"})
	cfg, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WriteString("[uploadpack]\n\tallowAnySHA1InWant = true\n")
	cfg.Close()
	ctx := context.Background()
	first, err := apps.Fetch(ctx, master(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	addCommit(t, dir, r, map[string]string{"f.txt": "second"})
	co, err := apps.FetchCommit(ctx, master(dir), "", first.Commit)
	if err != nil {
		t.Fatal(err)
	}
	f, err := co.FS.Open("/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 16)
	n, _ := f.Read(b)
	if co.Commit != first.Commit || string(b[:n]) != "first" {
		t.Fatalf("%s %q", co.Commit, b[:n])
	}
}

func TestACommitIsRefusedWhenTheServerCannotServeItAndTheBranchMoved(t *testing.T) {
	dir, r := localRepo(t, map[string]string{"f.txt": "first"})
	ctx := context.Background()
	first, err := apps.Fetch(ctx, master(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	if co, err := apps.FetchCommit(ctx, master(dir), "", first.Commit); err != nil || co.Commit != first.Commit {
		t.Fatalf("while the branch is there: %v", err)
	}
	addCommit(t, dir, r, map[string]string{"f.txt": "second"})
	if co, err := apps.FetchCommit(ctx, master(dir), "", first.Commit); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("%+v %v", co, err)
	}
}

func TestARepositoryOnALoopbackLinkLocalOrMetadataHostIsRefused(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.8.8.8", "[::1]", "0.0.0.0", "169.254.169.254", "[fe80::1]", "[::ffff:127.0.0.1]", "168.63.129.16", "100.100.100.200", "[fd00:ec2::254]", "localhost", "LOCALHOST.", "git.localhost", "metadata.google.internal"} {
		if _, err := apps.Normalize(apps.Source{Repo: "https://" + host + "/x/y", Ref: "main"}); err == nil {
			t.Errorf("%s was accepted", host)
		}
	}
	for _, host := range []string{"10.0.0.5", "172.16.0.9", "192.168.1.10", "git.corp.example"} {
		if _, err := apps.Normalize(apps.Source{Repo: "https://" + host + "/x/y", Ref: "main"}); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
}

func TestAHostIsCheckedWhereItResolvesAndAgainAtEveryConnection(t *testing.T) {
	ctx := context.Background()
	// Past Normalize, as a record saved earlier would be: localhost stands
	// for any name that resolves to a refused address.
	if err := apps.CheckRepoHost(ctx, "https://localhost/x/y"); err == nil {
		t.Error("the save accepted a host that resolves to loopback")
	}
	if err := apps.CheckRepoHost(ctx, "https://name.invalid/x/y"); err != nil {
		t.Errorf("a name that does not resolve now is left to the connection: %v", err)
	}
	if _, err := apps.ListRefs(ctx, "https://localhost:1/x/y", ""); err == nil || !strings.Contains(err.Error(), "does not fetch from") {
		t.Errorf("listing refs: %v", err)
	}
	if _, err := apps.Fetch(ctx, apps.Source{Repo: "https://127.0.0.1:1/x/y", Ref: "main", RefKind: apps.RefBranch}, ""); err == nil || !strings.Contains(err.Error(), "does not fetch from") {
		t.Errorf("fetching: %v", err)
	}
}

func templateChart(text string) *chart.Chart {
	return &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "x", Version: "1.0.0"}, Templates: []*common.File{{Name: "templates/x.yaml", Data: []byte(text)}}}
}

func TestARenderThatLoopsOrRepeatsPastItsBudgetIsStopped(t *testing.T) {
	ctx := context.Background()
	for _, text := range []string{
		`{{ range until 2000000000 }}aaaaaaaaaaaaaaaa{{ end }}`,
		`{{ range untilStep 0 2000000000 1 }}a{{ end }}`,
		`{{ range untilStep 2000000000 0 -1 }}a{{ end }}`,
		`{{ seq 2000000000 }}`,
		`{{ seq 0 1 2000000000 }}`,
		`{{ repeat 2000000000 "aaaaaaaa" }}`,
	} {
		start := time.Now()
		if _, err := apps.Render(ctx, templateChart(text), nil, "x", "default", ""); !errors.Is(err, apps.ErrRenderLimit) {
			t.Errorf("%s: %v", text, err)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s took %s", text, d)
		}
	}
}

func TestTheBudgetLeavesOrdinaryTemplatesAsTheyWere(t *testing.T) {
	r, err := apps.Render(context.Background(), templateChart(`# {{ until 3 }} {{ untilStep 0 6 2 }} {{ seq 3 }} {{ seq 3 1 }} {{ seq 1 2 5 }} {{ repeat 2 "ab" }}`), nil, "x", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# [0 1 2] [0 2 4] 1 2 3 3 2 1 1 3 5 abab"; !strings.Contains(r.Manifest, want) {
		t.Fatalf("%q", r.Manifest)
	}
}

func TestChartsStayWithoutTheFunctionsHelmTakesAway(t *testing.T) {
	if _, err := apps.Render(context.Background(), templateChart(`# {{ env "HOME" }}`), nil, "x", "default", ""); err == nil || errors.Is(err, apps.ErrRenderLimit) {
		t.Fatalf("%v", err)
	}
}

func TestARenderLargerThanTheLimitIsRefused(t *testing.T) {
	text := "{{ range .Values.l }}# " + strings.Repeat("a", 4<<20) + "\n{{ end }}"
	list := make([]any, 9)
	if _, err := apps.Render(context.Background(), templateChart(text), map[string]any{"l": list}, "x", "default", ""); !errors.Is(err, apps.ErrRenderLimit) {
		t.Fatalf("%v", err)
	}
}

func TestARenderPastItsMemoryIsRefusedAndTheConsoleGoesOn(t *testing.T) {
	doubling := `{{ $s := "a" }}{{ range until 40 }}{{ $s = printf "%s%s" $s $s }}{{ end }}# {{ len $s }}`
	_, err := apps.Render(context.Background(), templateChart(doubling), nil, "x", "default", "")
	if !errors.Is(err, apps.ErrRenderLimit) || !strings.Contains(err.Error(), "more memory or time than allowed") {
		t.Fatalf("%v", err)
	}
	if r, err := apps.Render(context.Background(), templateChart("# {{ .Release.Name }}"), nil, "after", "default", ""); err != nil || !strings.Contains(r.Manifest, "# after") {
		t.Fatalf("a render after: %q %v", r.Manifest, err)
	}
}

func TestARenderThatDoesNotEndIsStoppedAndGivesBackItsSlot(t *testing.T) {
	defer apps.SetRenderTimeout(2 * time.Second)()
	endless := templateChart(`{{ range 2000000000 }}{{ range 2000000000 }}{{ end }}{{ end }}`)
	errs := make(chan error, 4)
	start := time.Now()
	for range 4 {
		go func() {
			_, err := apps.Render(context.Background(), endless, nil, "x", "default", "")
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; !errors.Is(err, apps.ErrRenderLimit) || !strings.Contains(err.Error(), "in time") {
			t.Fatalf("%v", err)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the renders were stopped after %s", d)
	}
	// Every slot is free again: a render takes one at once.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if r, err := apps.Render(ctx, templateChart("# {{ .Release.Name }}"), nil, "after", "default", ""); err != nil || !strings.Contains(r.Manifest, "# after") {
		t.Fatalf("a render after: %q %v", r.Manifest, err)
	}
}

func TestARenderInItsOwnProcessIsTheRenderOfBefore(t *testing.T) {
	sub := &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "sub", Version: "0.1.0"},
		Values:    map[string]any{"port": int64(8080)},
		Templates: []*common.File{{Name: "templates/svc.yaml", Data: []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: {{ .Release.Name }}-sub\nspec:\n  ports:\n  - port: {{ .Values.port }}\n")}}}
	top := &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "top", Version: "1.0.0", Dependencies: []*chart.Dependency{{Name: "sub", Version: "0.1.0"}}},
		Values: map[string]any{"replicas": float64(1), "list": []any{"a", map[string]any{"b": true}}},
		Templates: []*common.File{
			{Name: "templates/cm.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\ndata:\n  big: {{ .Values.big | quote }}\n  replicas: {{ .Values.replicas | quote }}\n  list: {{ toJson .Values.list | quote }}\n  upgrade: {{ .Release.IsUpgrade | quote }}\n  revision: {{ .Release.Revision | quote }}\n  kube: {{ .Capabilities.KubeVersion.Version | quote }}\n  file: {{ .Files.Get \"f.txt\" | quote }}\n")},
			{Name: "templates/NOTES.txt", Data: []byte("installed {{ .Release.Name }}")},
		},
		Files: []*common.File{{Name: "f.txt", Data: []byte("from a file")}}}
	top.SetDependencies(sub)
	vals := map[string]any{"big": int64(12345678901), "replicas": int64(3)}
	for _, o := range []apps.ReleaseOptions{
		{Name: "r", Namespace: "ns", KubeVersion: "v1.31.2"},
		{Name: "r", Namespace: "ns", Upgrade: true, Revision: 7, Previous: map[string]any{"replicas": int64(2)}},
	} {
		here, err := apps.RenderHere(context.Background(), top, vals, o)
		if err != nil {
			t.Fatal(err)
		}
		there, err := apps.RenderRelease(context.Background(), top, vals, o)
		if err != nil {
			t.Fatal(err)
		}
		if here != there {
			t.Fatalf("in this process:\n%s\nin its own:\n%s", here.Manifest, there.Manifest)
		}
		for _, want := range []string{"big: \"12345678901\"", "replicas: \"3\"", "port: 8080", "file: \"from a file\"", `list: "[\"a\",{\"b\":true}]"`} {
			if !strings.Contains(there.Manifest, want) {
				t.Errorf("%s is not in\n%s", want, there.Manifest)
			}
		}
		if o.Upgrade && !strings.Contains(there.Manifest, `upgrade: "true"`) || o.Upgrade && !strings.Contains(there.Manifest, `revision: "7"`) {
			t.Errorf("an upgrade:\n%s", there.Manifest)
		}
		if !o.Upgrade && !strings.Contains(there.Manifest, `kube: "v1.31.2"`) {
			t.Errorf("the cluster's version:\n%s", there.Manifest)
		}
		if there.Notes != "installed r" {
			t.Errorf("notes %q", there.Notes)
		}
	}
}

func TestAChartPathHoldsPlainPathCharactersOnly(t *testing.T) {
	for _, bad := range []string{"charts/app\nhelm uninstall x", "charts/app --no-hooks", "charts/my app", "--no-hooks", "charts/-x", "a'b", `a"b`, "a#b", "a\tb", "charts/é"} {
		if _, err := apps.Normalize(apps.Source{Repo: "https://github.com/x/y", Path: bad, Ref: "main"}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{"charts/app", "deploy/helm/my_chart-1.2", "a+b/c@d"} {
		if s, err := apps.Normalize(apps.Source{Repo: "https://github.com/x/y", Path: good, Ref: "main"}); err != nil || s.Path != good {
			t.Errorf("%q: %+v %v", good, s, err)
		}
	}
}

func TestARepositoryIsTheSameWhateverWayItWasWritten(t *testing.T) {
	if !apps.SameRepo(apps.Source{Repo: "https://github.com/x/y"}, apps.Source{Repo: "github.com/x/y.git/"}) {
		t.Error("two spellings of one repository differ")
	}
	if apps.SameRepo(apps.Source{Repo: "https://github.com/x/y"}, apps.Source{Repo: "https://github.com/x/z"}) {
		t.Error("two repositories match")
	}
}
