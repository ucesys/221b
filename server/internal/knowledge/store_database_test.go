package knowledge

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- The wiki's store in the database (DB-96) ---

// The wiki in the database: two replicas over one database see each
// other's writes; a run is staged apart from the approved files, published
// on approval only where the page did not change since it was read, and
// discarded; deleting works; the check counts the files.
func TestTheWikiInTheDatabaseDB95(t *testing.T) {
	d := dbtest.Open(t)
	leader, other := NewDBStore(d, 0), NewDBStore(d, 0)
	if leader.Kind() != StoreDatabase || leader.Limit() != 256<<20 {
		t.Fatalf("kind %q, limit %d", leader.Kind(), leader.Limit())
	}
	if got := files(t, other); len(got) != 0 {
		t.Fatalf("an empty store: %v", got)
	}
	a, b := "pages/application/shop.md", "pages/system/db.md"
	if err := leader.Write(ctx, []Change{{Path: ManifestPath, Body: []byte(`{"items":{}}`)}, {Path: a, Body: []byte("# Shop\n\n<b>v1</b> & more")}, {Path: "README.md", Body: []byte("not the wiki's")}}, "first"); err != nil {
		t.Fatal(err)
	}
	got := files(t, other)
	if len(got) != 2 || string(got[a].Body) != "# Shop\n\n<b>v1</b> & more" || got[a].Version == "" {
		t.Fatalf("the other replica reads the wiki's files only: %v", got)
	}
	if err := leader.Stage(ctx, "r-1", []Change{{Path: a, Body: []byte("shop v2"), Base: got[a].Version}, {Path: b, Body: []byte("db v1")}}, "run r-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := files(t, other)[b]; ok {
		t.Fatal("a staged page is among the approved files")
	}
	// A person changes the shop page meanwhile, through the other replica.
	if err := other.Write(ctx, []Change{{Path: a, Body: []byte("shop by Dana")}}, "Dana"); err != nil {
		t.Fatal(err)
	}
	conflicts, err := leader.Publish(ctx, "r-1", []Change{{Path: a, Base: got[a].Version}, {Path: b}}, "approved")
	if err != nil || len(conflicts) != 1 || conflicts[0] != a {
		t.Fatalf("publish: %v, %v", conflicts, err)
	}
	got = files(t, other)
	if string(got[a].Body) != "shop by Dana" || string(got[b].Body) != "db v1" {
		t.Fatalf("after publishing: shop %q, db %q", got[a].Body, got[b].Body)
	}
	// The conflicting page is still staged; publishing it over the new
	// version goes through, and the run is staged no more.
	if conflicts, err := leader.Publish(ctx, "r-1", []Change{{Path: a, Base: got[a].Version}}, "approved"); err != nil || len(conflicts) != 0 {
		t.Fatalf("publish over the new version: %v, %v", conflicts, err)
	}
	if string(files(t, other)[a].Body) != "shop v2" {
		t.Fatal("the page was not published over the new version")
	}
	if _, err := leader.Publish(ctx, "r-1", []Change{{Path: b}}, "approved"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run published whole is still staged: %v", err)
	}
	// A new page published over one written meanwhile is a conflict too.
	if err := leader.Stage(ctx, "r-2", []Change{{Path: "pages/runbook/x.md", Body: []byte("x by Mycroft")}}, "run r-2"); err != nil {
		t.Fatal(err)
	}
	if err := other.Write(ctx, []Change{{Path: "pages/runbook/x.md", Body: []byte("x by Dana")}}, "Dana"); err != nil {
		t.Fatal(err)
	}
	if conflicts, err := leader.Publish(ctx, "r-2", []Change{{Path: "pages/runbook/x.md"}}, "approved"); err != nil || len(conflicts) != 1 {
		t.Fatalf("a new page written meanwhile: %v, %v", conflicts, err)
	}
	if _, err := leader.Publish(ctx, "r-9", []Change{{Path: a}}, "approved"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run never staged: %v", err)
	}
	for _, run := range []string{"r-2", "r-9"} {
		if err := other.Discard(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	var staged int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM wiki_stages").Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("discarded runs stay: %d, %v", staged, err)
	}
	// Deleting, as the clean start does.
	if err := leader.Write(ctx, []Change{{Path: b, Delete: true}}, "clean"); err != nil {
		t.Fatal(err)
	}
	check, err := other.Check(ctx)
	if err != nil || check.Files != 3 || len(check.Warnings) != 0 {
		t.Fatalf("check: %+v, %v", check, err)
	}
}

// A write past the store's room is refused whole, in plain words, and
// nothing is dropped; a write that frees room still goes through; the
// check warns when the store is nearly full.
func TestAFullDatabaseStoreRefusesDB95(t *testing.T) {
	d := dbtest.Open(t)
	st := NewDBStore(d, 2<<10)
	if err := st.Write(ctx, []Change{{Path: "pages/system/a.md", Body: []byte(strings.Repeat("a", 1200))}}, ""); err != nil {
		t.Fatal(err)
	}
	err := st.Write(ctx, []Change{{Path: "pages/system/b.md", Body: []byte(strings.Repeat("b", 1200))}, {Path: "pages/system/c.md", Body: []byte("c")}}, "")
	if !errors.Is(err, ErrStoreFull) || !strings.Contains(err.Error(), "move the wiki to Git or S3 in Administration › AI › Knowledge") {
		t.Fatalf("full: %v", err)
	}
	if err := st.Stage(ctx, "r-1", []Change{{Path: "pages/system/b.md", Body: []byte(strings.Repeat("b", 1200))}}, ""); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("a run past the room: %v", err)
	}
	got := files(t, st)
	if len(got) != 1 || len(got["pages/system/a.md"].Body) != 1200 {
		t.Fatalf("a refused write changed the store: %v", got)
	}
	if err := st.Write(ctx, []Change{{Path: "pages/system/a.md", Body: []byte(strings.Repeat("a", 1900))}}, ""); err != nil {
		t.Fatalf("a write within the room: %v", err)
	}
	if check, err := st.Check(ctx); err != nil || len(check.Warnings) != 1 || !strings.Contains(check.Warnings[0], "nearly full") {
		t.Fatalf("check: %+v, %v", check, err)
	}
	if err := st.Write(ctx, []Change{{Path: "pages/system/a.md", Delete: true}}, ""); err != nil {
		t.Fatalf("freeing room: %v", err)
	}
	if used, err := st.Used(ctx); err != nil || used != 0 {
		t.Fatalf("used %d, %v", used, err)
	}
}

// The temporary ConfigMap store of before (CF-57) is copied in once: every
// file, and every staged run, which is then published from the database;
// a second copy does nothing, also when the ConfigMap holds more.
func TestTheConfigMapStoreIsCopiedIntoTheDatabaseOnceDB95(t *testing.T) {
	d := dbtest.Open(t)
	st := NewDBStore(d, 0)
	cm := &kv.Memory{}
	if _, err := cm.SaveKey(ctx, WikiKey, []byte(`{"seq":3,"files":{"manifest.json":{"body":"{}","version":"c1"},"pages/system/db.md":{"body":"db <v1>","version":"c2"}},"staged":{"r-7":{"pages/system/web.md":"web v1"}}}`), ""); err != nil {
		t.Fatal(err)
	}
	n, ran, err := st.Import(ctx, cm, "configmap")
	if err != nil || !ran || n != 3 {
		t.Fatalf("the copy: %d, %v, %v", n, ran, err)
	}
	got := files(t, st)
	if len(got) != 2 || string(got["pages/system/db.md"].Body) != "db <v1>" || got["pages/system/db.md"].Version != "c2" {
		t.Fatalf("the files copied, at their versions: %v", got)
	}
	// A run staged over a page's version in the ConfigMap publishes as it
	// would have there.
	if err := st.Stage(ctx, "r-8", []Change{{Path: "pages/system/db.md", Body: []byte("db v2"), Base: "c2"}}, ""); err != nil {
		t.Fatal(err)
	}
	if conflicts, err := st.Publish(ctx, "r-8", []Change{{Path: "pages/system/db.md", Base: "c2"}}, "approved"); err != nil || len(conflicts) != 0 {
		t.Fatalf("a run over a copied version: %v, %v", conflicts, err)
	}
	if conflicts, err := st.Publish(ctx, "r-7", []Change{{Path: "pages/system/web.md"}}, "approved"); err != nil || len(conflicts) != 0 {
		t.Fatalf("a staged run copied: %v, %v", conflicts, err)
	}
	if _, err := cm.SaveKey(ctx, WikiKey, []byte(`{"files":{"pages/system/late.md":{"body":"late"}}}`), "1"); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := st.Import(ctx, cm, "configmap"); err != nil || ran {
		t.Fatalf("a second copy ran: %v, %v", ran, err)
	}
	if _, ok := files(t, st)["pages/system/late.md"]; ok {
		t.Fatal("a second copy copied")
	}
	if copied, err := d.Imported(ctx, WikiImportID); err != nil || !copied {
		t.Fatalf("the copy is not recorded: %v", err)
	}
	// An empty ConfigMap is a copy of nothing, recorded all the same.
	d2 := dbtest.Open(t)
	if n, ran, err := NewDBStore(d2, 0).Import(ctx, &kv.Memory{}, "configmap"); err != nil || !ran || n != 0 {
		t.Fatalf("an empty ConfigMap: %d, %v, %v", n, ran, err)
	}
}

// The database store is where the wiki is kept while the settings name no
// store, and while they name the ConfigMap store of before; the Wiki says
// its room, and a run staged in the ConfigMap store is decided in it.
func TestTheWikiIsKeptInTheDatabaseByDefaultDB95(t *testing.T) {
	d := dbtest.Open(t)
	s := New(Options{Dir: t.TempDir(), WikiStore: NewDBStore(d, 0), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, kind := range []string{"", StoreConfigMap} {
		if got := s.storeKind(StoreSettings{Kind: kind}); got != StoreDatabase {
			t.Errorf("settings naming %q keep the wiki in %q", kind, got)
		}
	}
	if s.storeKind(StoreSettings{Kind: StoreGit}) != StoreGit {
		t.Error("a git store is not kept")
	}
	st, err := s.Wiki(ctx, Caller{Reader: true})
	if err != nil || st.Store != StoreDatabase || st.Usage == nil || st.Usage.Limit != 256<<20 {
		t.Fatalf("the Wiki: %+v, %v", st, err)
	}
	if !sameStore(StoreConfigMap, StoreDatabase) || sameStore(StoreGit, StoreDatabase) {
		t.Error("a run of the ConfigMap store of before is not one of the database store")
	}
	if err := (StoreSettings{Kind: StoreDatabase}).Validate(); err != nil {
		t.Errorf("the database store is refused: %v", err)
	}
	none := New(Options{Dir: t.TempDir()})
	if got := none.storeKind(StoreSettings{}); got != "" {
		t.Errorf("without a database store: %q", got)
	}
}
