package apps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/util"

	"github.com/ucesys/221b/server/internal/apps/repotest"
)

// --- Helm repositories (Phase 12, wave 5) ---

const cmTemplate = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-cm\ndata:\n  color: {{ .Values.color | quote }}\n"

func newRepos(t *testing.T, f *repotest.Registry) *Repos {
	t.Helper()
	AllowLocalRepos = true
	t.Cleanup(func() { AllowLocalRepos = false })
	return NewRepos(RepoOptions{Client: f.Client()})
}

func str(s string) *string { return &s }

// A chart is named as a helm line names it, and checked (HR-01, HR-07).
func TestChartRefs(t *testing.T) {
	s, err := ParseChartRef("bitnami/nginx", "18.1.0")
	if err != nil || s.Kind != KindHelm || s.Repository != "bitnami" || s.Chart != "nginx" || s.Ref != "18.1.0" || s.RefKind != RefVersion || s.String() != "bitnami/nginx @ 18.1.0" {
		t.Fatalf("helm: %+v %v", s, err)
	}
	s, err = ParseChartRef("oci://ghcr.io/org/charts/app", "")
	if err != nil || s.Kind != KindOCI || s.Repo != "oci://ghcr.io/org/charts" || s.Chart != "app" || s.Ref != "" || s.ChartRef() != "oci://ghcr.io/org/charts/app" {
		t.Fatalf("oci: %+v %v", s, err)
	}
	for _, bad := range []string{"nginx", "a/b/c", "https://x/y", "oci://", "oci://ghcr.io", "Bit_nami/nginx", "bitnami/-x", "oci://169.254.169.254/x/y", "oci://localhost/x/y"} {
		if _, err := ParseChartRef(bad, ""); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	if _, err := ParseChartRef("bitnami/nginx", "1.0 --set"); err == nil {
		t.Error("a version with a space")
	}
	if _, err := Normalize(Source{Kind: "svn", Repo: "x"}); err == nil {
		t.Error("an unknown kind")
	}
	if !SameRepo(Source{Kind: KindHelm, Repository: "a", Chart: "x"}, Source{Kind: KindHelm, Repository: "a", Chart: "y"}) || SameRepo(Source{Kind: KindHelm, Repository: "a"}, Source{Kind: KindHelm, Repository: "b"}) || SameRepo(Source{Kind: KindHelm, Repository: "a"}, Source{Repo: "https://github.com/o/a", Ref: "main"}) {
		t.Error("same repo")
	}
}

// Repositories are kept with their credentials apart, and checked (HR-02).
func TestRepositoriesAreKept(t *testing.T) {
	f := repotest.New()
	defer f.Close()
	r := newRepos(t, f)
	ctx := context.Background()
	for _, bad := range []RepositoryInput{
		{Kind: KindHelm, URL: "ftp://x"},
		{Kind: "git", URL: "https://x"},
		{Kind: KindHelm, URL: "https://user@x/charts"},
		{Kind: KindHelm, URL: "https://x/charts", Auth: AuthBasic, Secret: str("pw")},
		{Kind: KindOCI, URL: "https://x"},
	} {
		if _, _, err := r.Save(ctx, "x", bad, "a"); !errors.Is(err, ErrInvalidRepo) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, _, err := r.Save(ctx, "Bad Name", RepositoryInput{Kind: KindHelm, URL: "https://x"}, "a"); !errors.Is(err, ErrInvalidRepo) {
		t.Error("a bad name")
	}
	repo, created, err := r.Save(ctx, "local", RepositoryInput{Kind: KindHelm, URL: f.URL + "/charts/index.yaml", Auth: AuthBasic, Username: "u", Secret: str("pw")}, "alice")
	if err != nil || !created || repo.URL != f.URL+"/charts" || !repo.HasSecret || repo.SetBy != "alice" {
		t.Fatalf("saved: %+v %v", repo, err)
	}
	list, _ := r.List(ctx)
	if len(list) != 1 || list[0].Name != "local" {
		t.Fatalf("list: %+v", list)
	}
	_, secret, _ := r.Get(ctx, "local")
	if secret != "pw" {
		t.Fatalf("secret: %q", secret)
	}
	// The secret is kept when not given, and belongs to its address.
	if _, _, err := r.Save(ctx, "local", RepositoryInput{Kind: KindHelm, URL: f.URL + "/charts", Auth: AuthBasic, Username: "u2"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, s, _ := r.Get(ctx, "local"); s != "pw" {
		t.Fatalf("kept: %q", s)
	}
	if _, _, err := r.Save(ctx, "local", RepositoryInput{Kind: KindHelm, URL: "https://elsewhere.example/charts", Auth: AuthBasic, Username: "u"}, "bob"); !errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("moved with the old password: %v", err)
	}
	if repo, _, err := r.Save(ctx, "local", RepositoryInput{Kind: KindHelm, URL: f.URL + "/charts", Auth: AuthNone}, "bob"); err != nil || repo.HasSecret {
		t.Fatalf("none: %+v %v", repo, err)
	}
	if _, s, _ := r.Get(ctx, "local"); s != "" {
		t.Fatalf("the password stayed: %q", s)
	}
	if err := r.Delete(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Get(ctx, "local"); !errors.Is(err, ErrNoRepository) {
		t.Fatalf("deleted: %v", err)
	}
}

// A Helm repository's chart is fetched at a version, or the newest,
// checked against its digest, and reads as a checkout (HR-04, HR-05).
func TestHelmRepositoryChart(t *testing.T) {
	f := repotest.New()
	defer f.Close()
	f.User, f.Password = "reader", "s3cret"
	f.Add("shop", "1.2.0", map[string]string{"values.yaml": "color: blue\n", "templates/cm.yaml": cmTemplate})
	f.Add("shop", "1.10.0", map[string]string{"values.yaml": "color: green\n", "templates/cm.yaml": cmTemplate})
	f.Add("shop", "2.0.0-rc.1", map[string]string{"values.yaml": "color: red\n", "templates/cm.yaml": cmTemplate})
	r := newRepos(t, f)
	ctx := context.Background()
	if res := r.Test(ctx, Repository{Name: "x", Kind: KindHelm, URL: f.URL + "/charts", Auth: AuthNone}, ""); res.OK || !strings.Contains(res.Text, "needs credentials") {
		t.Fatalf("test without credentials: %+v", res)
	}
	if res := r.Test(ctx, Repository{Name: "x", Kind: KindHelm, URL: f.URL + "/charts", Auth: AuthBasic, Username: "reader"}, "wrong"); res.OK || !strings.Contains(res.Text, "refused") {
		t.Fatalf("test with a wrong password: %+v", res)
	}
	if res := r.Test(ctx, Repository{Name: "x", Kind: KindHelm, URL: f.URL + "/charts", Auth: AuthBasic, Username: "reader"}, "s3cret"); !res.OK || res.Charts != 1 {
		t.Fatalf("test: %+v", res)
	}
	if _, _, err := r.Save(ctx, "shop-charts", RepositoryInput{Kind: KindHelm, URL: f.URL + "/charts", Auth: AuthBasic, Username: "reader", Secret: str("s3cret")}, "alice"); err != nil {
		t.Fatal(err)
	}
	charts, err := r.Charts(ctx, "shop-charts")
	if err != nil || len(charts) != 1 || charts[0].Name != "shop" || charts[0].Latest != "1.10.0" || charts[0].Versions != 3 {
		t.Fatalf("charts: %+v %v", charts, err)
	}
	src, _ := ParseChartRef("shop-charts/shop", "")
	refs, err := r.Refs(ctx, src, "")
	if err != nil || refs.Default != "1.10.0" || strings.Join(refs.Tags, ",") != "2.0.0-rc.1,1.10.0,1.2.0" {
		t.Fatalf("refs: %+v %v", refs, err)
	}
	co, err := r.Fetch(ctx, src, "")
	if err != nil || co.Version != "1.10.0" || !strings.HasPrefix(co.Commit, "sha256:") || co.CommitDate.IsZero() || co.CommitSubject != "chart shop 1.10.0" {
		t.Fatalf("newest: %+v %v", co, err)
	}
	if b, _ := util.ReadFile(co.FS, "/values.yaml"); string(b) != "color: green\n" {
		t.Fatalf("values: %q", b)
	}
	src.Ref = "1.2.0"
	co, err = r.Fetch(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	ch, vals, err := Compose(co, src, nil, []string{"color=pink"})
	if err != nil || ch.Metadata.Version != "1.2.0" {
		t.Fatalf("compose: %v", err)
	}
	out, err := Render(ctx, ch, vals, "web", "shop", "")
	if err != nil || !strings.Contains(out.Manifest, `color: "pink"`) {
		t.Fatalf("render: %+v %v", out, err)
	}
	if insp := Inspect(co); len(insp.Charts) != 1 || insp.Charts[0].Path != "" {
		t.Fatalf("inspect: %+v", insp)
	}
	// The archive a card showed, or nothing.
	if _, err := r.FetchDigest(ctx, src, "", co.Commit); err != nil {
		t.Fatalf("digest: %v", err)
	}
	if _, err := r.FetchDigest(ctx, src, "", "sha256:00"); err == nil || !strings.Contains(err.Error(), "changed in its repository") {
		t.Fatalf("another digest: %v", err)
	}
	// The credentials went to the repository's own host.
	if f.Auth["/charts/archives/shop-1.2.0.tgz"] != repotest.BasicAuth("reader", "s3cret") {
		t.Fatalf("auth: %q", f.Auth["/charts/archives/shop-1.2.0.tgz"])
	}
	src.Ref = "9.9.9"
	if _, err := r.Fetch(ctx, src, ""); err == nil || !strings.Contains(err.Error(), "no version 9.9.9") {
		t.Fatalf("a missing version: %v", err)
	}
	if _, err := r.Fetch(ctx, Source{Kind: KindHelm, Repository: "nowhere", Chart: "shop", RefKind: RefVersion}, ""); !errors.Is(err, ErrNoRepository) {
		t.Fatalf("an unknown repository: %v", err)
	}
	var none *Repos
	if _, err := none.Fetch(ctx, src, ""); err == nil {
		t.Fatal("a console without repositories fetched a chart")
	}
}

// An OCI registry's chart is fetched with a token its token service
// gives for the credentials, and checked against the manifest's digest
// (HR-04, HR-06).
func TestOCIChart(t *testing.T) {
	f := repotest.New()
	defer f.Close()
	f.User, f.Password = "robot", "pat-123"
	f.Add("app", "0.3.0", map[string]string{"values.yaml": "color: teal\n", "templates/cm.yaml": cmTemplate})
	f.Add("app", "0.4.0+build.1", map[string]string{"values.yaml": "color: navy\n", "templates/cm.yaml": cmTemplate})
	r := newRepos(t, f)
	ctx := context.Background()
	src, err := ParseChartRef("oci://"+f.Host()+"/org/charts/app", "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Fetch(ctx, src, ""); err == nil || !strings.Contains(err.Error(), "needs credentials") {
		t.Fatalf("anonymous: %v", err)
	}
	if res := r.Test(ctx, Repository{Kind: KindOCI, URL: "oci://" + f.Host() + "/org", Auth: AuthBasic, Username: "robot"}, "pat-123"); !res.OK {
		t.Fatalf("test: %+v", res)
	}
	if _, _, err := r.Save(ctx, "org", RepositoryInput{Kind: KindOCI, URL: "oci://" + f.Host() + "/org", Auth: AuthBasic, Username: "robot", Secret: str("pat-123")}, "alice"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Resolve(ctx, src); got.Repository != "org" {
		t.Fatalf("resolved: %+v", got)
	}
	co, err := r.Fetch(ctx, src, "")
	if err != nil || co.Version != "0.3.0" {
		t.Fatalf("fetch: %+v %v", co, err)
	}
	if b, _ := util.ReadFile(co.FS, "/values.yaml"); string(b) != "color: teal\n" {
		t.Fatalf("values: %q", b)
	}
	if !strings.HasPrefix(f.Auth["/v2/org/charts/app/manifests/0.3.0"], "Bearer registry-token-repository:org/charts/app:pull") {
		t.Fatalf("auth: %+v", f.Auth)
	}
	versions, err := r.Versions(ctx, src)
	if err != nil || strings.Join(versions, ",") != "0.4.0+build.1,0.3.0" {
		t.Fatalf("versions: %v %v", versions, err)
	}
	src.Ref = ""
	if co, err := r.Fetch(ctx, src, ""); err != nil || co.Version != "0.4.0+build.1" {
		t.Fatalf("newest: %+v %v", co, err)
	}
	// A chart outside the registry entry's path goes without its credentials.
	other, _ := ParseChartRef("oci://"+f.Host()+"/elsewhere/app", "0.3.0")
	if got, _ := r.Resolve(ctx, other); got.Repository != "" {
		t.Fatalf("another path took the credentials: %+v", got)
	}
}

// A chart archive past the limits is refused before it is unpacked.
func TestChartArchiveLimits(t *testing.T) {
	old := FetchLimits
	defer func() { FetchLimits = old }()
	FetchLimits.Files = 2
	if _, err := unpackChart(repotest.ChartArchive("x", "1.0.0", map[string]string{"a.yaml": "a", "b.yaml": "b"}), "x", "1.0.0"); !errors.Is(err, ErrFetchLimit) {
		t.Fatalf("too many files: %v", err)
	}
	FetchLimits = old
	if _, err := unpackChart([]byte("not a tgz"), "x", "1"); err == nil {
		t.Fatal("not an archive")
	}
	if c, p := parseChallenge(`Bearer realm="https://auth.example/token",service="registry.example",scope="repository:a/b:pull"`); c != "bearer" || p["realm"] != "https://auth.example/token" || p["service"] != "registry.example" || p["scope"] != "repository:a/b:pull" {
		t.Fatalf("challenge: %s %v", c, p)
	}
}
