package knowledge

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/s3"
	"github.com/ucesys/221b/server/internal/s3/s3test"
)

// remote makes an empty bare repository to clone, push to and fetch from.
func remote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wiki.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
	return dir
}

func files(t *testing.T, st Store) map[string]File {
	t.Helper()
	f, err := st.Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The wiki in git: direct writes for the manifest, each run on a branch
// merged on approval; a person's commit to another page is rebased over,
// one to the same page waits for a person.
func TestTheWikiInGit(t *testing.T) {
	url := remote(t)
	mycroft := NewGitStore(t.TempDir(), url, "main", "", "", "")
	person := NewGitStore(t.TempDir(), url, "main", "", "", "")
	if got := files(t, mycroft); len(got) != 0 {
		t.Fatalf("an empty repository: %v", got)
	}
	if err := mycroft.Write(ctx, []Change{{Path: ManifestPath, Body: []byte(`{"items":{}}`)}, {Path: "README.md", Body: []byte("not the wiki's")}}, "manifest"); err != nil {
		t.Fatal(err)
	}
	got := files(t, person)
	if _, ok := got[ManifestPath]; !ok || len(got) != 1 {
		t.Fatalf("the local copy holds the wiki's files only: %v", got)
	}
	a, b := "pages/application/shop.md", "pages/system/db.md"
	if err := mycroft.Stage(ctx, "r-1", []Change{{Path: a, Body: []byte("shop v1")}, {Path: b, Body: []byte("db v1")}}, "run r-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := files(t, person)[a]; ok {
		t.Fatal("a staged page is not on the branch before approval")
	}
	// A person writes the database page first.
	if err := person.Write(ctx, []Change{{Path: b, Body: []byte("db by Dana")}}, "Dana's page"); err != nil {
		t.Fatal(err)
	}
	conflicts, err := mycroft.Publish(ctx, "r-1", []Change{{Path: a}, {Path: b}}, "approved")
	if err != nil || len(conflicts) != 1 || conflicts[0] != b {
		t.Fatalf("publish: %v, %v", conflicts, err)
	}
	got = files(t, person)
	if string(got[a].Body) != "shop v1" || string(got[b].Body) != "db by Dana" {
		t.Fatalf("after the merge: shop %q, db %q", got[a].Body, got[b].Body)
	}
	// A change to the shop page, with someone committing another page
	// meanwhile: Mycroft's is made again over theirs.
	if err := mycroft.Stage(ctx, "r-2", []Change{{Path: a, Body: []byte("shop v2"), Base: got[a].Version}}, "run r-2"); err != nil {
		t.Fatal(err)
	}
	if err := person.Write(ctx, []Change{{Path: "pages/runbook/x.md", Body: []byte("x")}}, "another page"); err != nil {
		t.Fatal(err)
	}
	if conflicts, err := mycroft.Publish(ctx, "r-2", []Change{{Path: a, Base: got[a].Version}}, "approved"); err != nil || len(conflicts) != 0 {
		t.Fatalf("rebased: %v, %v", conflicts, err)
	}
	got = files(t, person)
	if string(got[a].Body) != "shop v2" || string(got["pages/runbook/x.md"].Body) != "x" {
		t.Fatal("both changes are kept")
	}
	if err := mycroft.Discard(ctx, "r-2"); err != nil {
		t.Fatal(err)
	}
	r, _ := git.PlainOpen(url)
	if _, err := r.Reference("refs/heads/mycroft/r-2", true); err == nil {
		t.Fatal("a decided run's branch is removed")
	}
	if _, err := r.Reference("refs/heads/main", true); err != nil {
		t.Fatal(err)
	}
}

// A git source lists the repository's documents by their blobs and reads
// them as text.
func TestAGitSourceReadsItsDocuments(t *testing.T) {
	url := remote(t)
	w := NewGitStore(t.TempDir(), url, "main", "", "", "")
	if err := w.Write(ctx, []Change{{Path: "runbooks/restart.md", Body: []byte("# Restart\n\nDrain first.")}, {Path: "runbooks/policy.docx", Body: docx(t, "Keep 30 days.")}, {Path: "src/main.go", Body: []byte("package main")}}, "runbooks"); err != nil {
		t.Fatal(err)
	}
	rd := &gitReader{g: newRepo(t.TempDir(), url, "main", "", "", ""), dir: "runbooks", web: "https://git.example.com/ops/runbooks.git"}
	items, complete, err := rd.List(ctx, time.Time{})
	if err != nil || !complete || len(items) != 2 {
		t.Fatalf("items: %+v, %v", items, err)
	}
	text, err := rd.Read(ctx, items[0])
	if err != nil || !strings.Contains(text, "Keep 30 days.") || !strings.HasPrefix(items[0].URL, "https://git.example.com/ops/runbooks/blob/main/") {
		t.Fatalf("read %q: %v (%s)", text, err, items[0].URL)
	}
}

func s3Store(t *testing.T, f *s3test.Fake) *S3Store {
	t.Helper()
	c, err := s3.New(s3.Config{Endpoint: f.URL, Region: "eu-west-1", Bucket: "wiki", PathStyle: true, HTTP: &http.Client{Transport: f.Transport()},
		Credentials: func(context.Context) (awsv4.Credentials, error) { return f.Key, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return NewS3Store(c, "kb/", s3.SSEKMS, "key-1")
}

// The wiki in a bucket: drafts under drafts/, copied on approval with a
// conditional write; a page someone uploaded meanwhile is not
// overwritten. A bucket without versioning is warned of.
func TestTheWikiInABucket(t *testing.T) {
	f := s3test.New("wiki", "eu-west-1", awsv4.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"})
	defer f.Close()
	st := s3Store(t, f)
	check, err := st.Check(ctx)
	if err != nil || len(check.Warnings) != 1 {
		t.Fatalf("no versioning: %+v, %v", check, err)
	}
	f.Versioning = "Enabled"
	if check, _ := st.Check(ctx); len(check.Warnings) != 0 || check.Versioning != "Enabled" {
		t.Fatalf("versioning on: %+v", check)
	}
	a, b := "pages/application/shop.md", "pages/system/db.md"
	if err := st.Write(ctx, []Change{{Path: a, Body: []byte("shop v1")}}, ""); err != nil {
		t.Fatal(err)
	}
	got := files(t, st)
	if err := st.Stage(ctx, "r-1", []Change{{Path: a, Body: []byte("shop v2"), Base: got[a].Version}, {Path: b, Body: []byte("db v1")}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := files(t, st)[b]; ok {
		t.Fatal("a draft is not among the approved files")
	}
	f.Set("kb/"+a, []byte("shop by Dana"), f.Now())
	conflicts, err := st.Publish(ctx, "r-1", []Change{{Path: a, Base: got[a].Version}, {Path: b}}, "")
	if err != nil || len(conflicts) != 1 || conflicts[0] != a {
		t.Fatalf("publish: %v, %v", conflicts, err)
	}
	objs := f.Objects()
	if string(objs["kb/"+a].Body) != "shop by Dana" || string(objs["kb/"+b].Body) != "db v1" || objs["kb/"+b].SSE != s3.SSEKMS || objs["kb/"+b].KMSKeyID != "key-1" {
		t.Fatalf("after publishing: %+v", objs)
	}
	if err := st.Discard(ctx, "r-1"); err != nil {
		t.Fatal(err)
	}
	for k := range f.Objects() {
		if strings.HasPrefix(k, "kb/drafts/") {
			t.Fatalf("a draft stays: %s", k)
		}
	}
}

// Moving the wiki copies every page, the manifest and the excerpts to
// the new store; it waits while runs wait for review.
func TestMovingTheWikiCopiesEveryFile(t *testing.T) {
	fx := newFixture(t)
	fx.store.Put("pages/application/shop.md", []byte("shop"))
	fx.store.Put(ManifestPath, []byte(`{"items":{}}`))
	fx.store.Put(excerptPath("infra:1"), []byte(`{"key":"infra:1","chunks":["x"]}`))
	to := NewMemoryStore()
	fx.s.open = func(StoreSettings, Creds) (Store, error) { return to, nil }
	in := StoreInput{Store: StoreSettings{Kind: StoreS3, Bucket: "wiki", Prefix: "kb", Credentials: CredentialsIdentity}}
	if _, err := fx.s.MoveStore(ctx, viewer, in); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer moves the wiki: %v", err)
	}
	n, err := fx.s.MoveStore(ctx, admin, in)
	if err != nil || n != 3 {
		t.Fatalf("moved %d: %v", n, err)
	}
	got, _ := to.Pull(ctx)
	if len(got) != 3 || string(got["pages/application/shop.md"].Body) != "shop" {
		t.Fatalf("moved: %v", got)
	}
	set, _ := fx.s.Settings(ctx)
	if set.Store.Kind != StoreS3 || set.Store.Prefix != "kb/" || set.Store.SavedBy != "Ann" {
		t.Fatalf("the settings name the new store: %+v", set.Store)
	}
}
