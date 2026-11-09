package knowledge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- The ConfigMap wiki store (temporary, CF-57) ---

// The wiki in a ConfigMap: two replicas over one ConfigMap see each
// other's writes; a run is staged apart from the approved files, published
// on approval only where the page did not change since it was read, and
// discarded; the check counts the files and says the store is temporary.
func TestTheWikiInAConfigMapCF57(t *testing.T) {
	cm := &kv.Memory{}
	leader, other := NewConfigMapStore(cm, 0), NewConfigMapStore(cm, 0)
	if leader.Kind() != StoreConfigMap {
		t.Fatalf("kind %q", leader.Kind())
	}
	if got := files(t, other); len(got) != 0 {
		t.Fatalf("an empty ConfigMap: %v", got)
	}
	a, b := "pages/application/shop.md", "pages/system/db.md"
	if err := leader.Write(ctx, []Change{{Path: ManifestPath, Body: []byte(`{"items":{}}`)}, {Path: a, Body: []byte("# Shop\n\n<b>v1</b> & more")}, {Path: "README.md", Body: []byte("not the wiki's")}}, "first"); err != nil {
		t.Fatal(err)
	}
	got := files(t, other)
	if len(got) != 2 || string(got[a].Body) != "# Shop\n\n<b>v1</b> & more" || got[a].Version == "" {
		t.Fatalf("the other replica reads the wiki's files only: %v", got)
	}
	if raw, _, _ := cm.LoadKey(ctx, WikiKey); strings.Contains(string(raw), "\\u003c") || !strings.Contains(string(raw), "<b>v1</b>") {
		t.Fatal("a page's < is kept escaped, taking room")
	}
	if err := leader.Stage(ctx, "r-1", []Change{{Path: a, Body: []byte("shop v2"), Base: got[a].Version}, {Path: b, Body: []byte("db v1")}}, "run r-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := files(t, other)[b]; ok {
		t.Fatal("a staged page is not among the approved files")
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
	for _, run := range []string{"r-1", "r-2", "r-9"} {
		if err := other.Discard(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if raw, _, _ := cm.LoadKey(ctx, WikiKey); strings.Contains(string(raw), "x by Mycroft") || strings.Contains(string(raw), `"staged"`) {
		t.Fatalf("a discarded run stays: %s", raw)
	}
	// Deleting, as the clean start does.
	if err := leader.Write(ctx, []Change{{Path: b, Delete: true}}, "clean"); err != nil {
		t.Fatal(err)
	}
	check, err := other.Check(ctx)
	if err != nil || check.Files != 3 || len(check.Warnings) != 1 || !strings.Contains(check.Warnings[0], "temporary") || !strings.Contains(check.Warnings[0], "Git or S3") {
		t.Fatalf("check: %+v, %v", check, err)
	}
}

// racing is a ConfigMap another replica writes to between a read and the
// save after it, once.
type racing struct {
	kv.Backend
	before func()
}

func (r *racing) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if f := r.before; f != nil {
		r.before = nil
		f()
	}
	return r.Backend.SaveKey(ctx, key, b, version)
}

// A write that loses the race to another replica reads again and is made
// over theirs: both are kept.
func TestTheConfigMapStoreWritesOverAnotherReplicasCF57(t *testing.T) {
	cm := &kv.Memory{}
	other := NewConfigMapStore(cm, 0)
	r := &racing{Backend: cm}
	r.before = func() {
		if err := other.Write(ctx, []Change{{Path: "pages/system/db.md", Body: []byte("db")}}, ""); err != nil {
			t.Error(err)
		}
	}
	if err := NewConfigMapStore(r, 0).Write(ctx, []Change{{Path: "pages/application/shop.md", Body: []byte("shop")}}, ""); err != nil {
		t.Fatal(err)
	}
	if got := files(t, other); len(got) != 2 {
		t.Fatalf("both writes are kept: %v", got)
	}
}

// A write past the store's room is refused whole, in plain words, and
// nothing is dropped; a write that frees room still goes through.
func TestAFullConfigMapStoreRefusesCF57(t *testing.T) {
	cm := &kv.Memory{}
	st := NewConfigMapStore(cm, 2<<10)
	if err := st.Write(ctx, []Change{{Path: "pages/system/a.md", Body: []byte(strings.Repeat("a", 1200))}}, ""); err != nil {
		t.Fatal(err)
	}
	err := st.Write(ctx, []Change{{Path: "pages/system/b.md", Body: []byte(strings.Repeat("b", 1200))}, {Path: "pages/system/c.md", Body: []byte("c")}}, "")
	if !errors.Is(err, ErrStoreFull) || !strings.Contains(err.Error(), "(2 KiB): move the wiki to Git or S3 in Settings › Knowledge") {
		t.Fatalf("full: %v", err)
	}
	if err := st.Stage(ctx, "r-1", []Change{{Path: "pages/system/b.md", Body: []byte(strings.Repeat("b", 1200))}}, ""); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("a run past the room: %v", err)
	}
	got := files(t, st)
	if len(got) != 1 || len(got["pages/system/a.md"].Body) != 1200 {
		t.Fatalf("nothing is dropped or half written: %v", got)
	}
	if err := st.Write(ctx, []Change{{Path: "pages/system/a.md", Delete: true}}, ""); err != nil {
		t.Fatalf("freeing room: %v", err)
	}
}

// cmService is a console with the chart's ConfigMap for the wiki.
func cmService(t *testing.T, f *fixture, cm kv.Backend, limit int) *Service {
	t.Helper()
	return New(Options{WikiConfigMap: cm, WikiBytes: limit, Model: f.model, Console: f.console, Proposer: f.props, Dir: t.TempDir(), HTTP: f.conf.Client(), Now: f.clock.now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Audit:  func(e audit.Event) { f.mu.Lock(); f.audits = append(f.audits, e); f.mu.Unlock() }})
}

// With the chart's ConfigMap and no store chosen, the wiki is kept in the
// ConfigMap: Settings › Knowledge and the Wiki tab show it, a sync's run
// is staged and published there. Without it, no store, as before; and the
// ConfigMap store cannot be chosen.
func TestTheConfigMapStoreIsTheDefaultCF57(t *testing.T) {
	f := newFixture(t)
	cm := &kv.Memory{}
	f.s = cmService(t, f, cm, 0)
	v, err := f.s.GetView(ctx, admin)
	if err != nil || v.Settings.Store.Kind != StoreConfigMap || !v.ConfigMapStore {
		t.Fatalf("the view: %+v, %v", v.Settings.Store, err)
	}
	if w, err := f.s.Wiki(ctx, admin); err != nil || w.Store != StoreConfigMap {
		t.Fatalf("the Wiki tab: %q, %v", w.Store, err)
	}
	if set, _ := f.s.Settings(ctx); set.Store.Kind != "" {
		t.Fatalf("nothing is saved: %+v", set.Store)
	}
	check, err := f.s.TestStore(ctx, admin, StoreInput{Store: StoreSettings{Kind: StoreConfigMap}})
	if err != nil || len(check.Warnings) != 1 {
		t.Fatalf("the test: %+v, %v", check, err)
	}
	f.model.reply = func(string) string {
		return `{"pages":[{"path":"pages/application/shop.md","title":"Application: shop","sources":[],"body":"Shop (source: “x”)."}]}`
	}
	f.space(t, map[string]string{"Shop": "<p>Shop.</p>"})
	res, err := f.s.Sync(ctx, true)
	if err != nil || res.Pages != 1 || len(res.Errors) != 0 {
		t.Fatalf("sync: %+v, %v", res, err)
	}
	run, err := f.s.Approve(ctx, bob, res.Run)
	if err != nil || run.State != StateDone || run.Store != StoreConfigMap {
		t.Fatalf("approved: %+v, %v", run, err)
	}
	got := files(t, NewConfigMapStore(cm, 0))
	if !strings.Contains(string(got["pages/application/shop.md"].Body), "Shop (source") || got[ManifestPath].Version == "" {
		t.Fatalf("the ConfigMap holds the page and the manifest: %v", got)
	}
	if _, ok := f.s.PageAt("pages/application/shop.md"); !ok {
		t.Fatal("the agents read the published page")
	}

	none := New(Options{Dir: t.TempDir()})
	if err := none.Refresh(ctx); !errors.Is(err, ErrNoStore) {
		t.Fatalf("no ConfigMap: %v", err)
	}
	if v, _ := none.GetView(ctx, admin); v.Settings.Store.Kind != "" || v.ConfigMapStore {
		t.Fatalf("no ConfigMap, no store: %+v", v.Settings.Store)
	}
	if _, err := none.SaveStore(ctx, admin, StoreInput{Store: StoreSettings{Kind: StoreConfigMap}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the ConfigMap store chosen without its ConfigMap: %v", err)
	}
}

// A full ConfigMap store is reported by the sync, never passed over.
func TestASyncReportsAFullConfigMapStoreCF57(t *testing.T) {
	f := newFixture(t)
	f.s = cmService(t, f, &kv.Memory{}, 1<<10)
	f.model.reply = func(string) string {
		return `{"pages":[{"path":"pages/application/shop.md","title":"Application: shop","sources":[],"body":"` + strings.Repeat("Shop. ", 400) + `"}]}`
	}
	f.space(t, map[string]string{"Shop": "<p>" + strings.Repeat("The shop runs on web and db. ", 60) + "</p>"})
	res, err := f.s.Sync(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Errors, " "), "the ConfigMap wiki store is full (1 KiB): move the wiki to Git or S3") {
		t.Fatalf("the sync's errors: %v", res.Errors)
	}
}

// Moving the wiki from the ConfigMap store copies every file to the store
// chosen, which the settings name from then on.
func TestMovingTheWikiOutOfTheConfigMapCF57(t *testing.T) {
	f := newFixture(t)
	cm := &kv.Memory{}
	f.s = cmService(t, f, cm, 0)
	src := NewConfigMapStore(cm, 0)
	if err := src.Write(ctx, []Change{{Path: "pages/application/shop.md", Body: []byte("shop")}, {Path: ManifestPath, Body: []byte(`{"items":{}}`)}, {Path: excerptPath("infra:1"), Body: []byte(`{"key":"infra:1","chunks":["x"]}`)}}, ""); err != nil {
		t.Fatal(err)
	}
	to := NewMemoryStore()
	f.s.open = func(StoreSettings, Creds) (Store, error) { return to, nil }
	n, err := f.s.MoveStore(ctx, admin, StoreInput{Store: StoreSettings{Kind: StoreS3, Bucket: "wiki", Prefix: "kb", Credentials: CredentialsIdentity}})
	if err != nil || n != 3 {
		t.Fatalf("moved %d: %v", n, err)
	}
	if got, _ := to.Pull(ctx); len(got) != 3 || string(got["pages/application/shop.md"].Body) != "shop" {
		t.Fatalf("moved: %v", got)
	}
	if v, _ := f.s.GetView(ctx, admin); v.Settings.Store.Kind != StoreS3 {
		t.Fatalf("the settings name the bucket: %+v", v.Settings.Store)
	}
}
