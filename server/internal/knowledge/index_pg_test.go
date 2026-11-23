package knowledge

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// pgReplica is a replica of the console over a database and a wiki's
// store shared with the others: its settings in the tables, its search
// index in the database (KN-53).
func pgReplica(t *testing.T, d *db.DB, store Store, leads bool) *Service {
	t.Helper()
	s := New(Options{Backend: &kv.Keyed{Keys: map[string]kv.Backend{SettingsKey: NewSettingsPG(d), StatusKey: NewStatusPG(d), DraftsKey: NewDraftsPG(d)}, Rest: &kv.Memory{}},
		Store: store, Database: d, Dir: t.TempDir(), Leads: func() bool { return leads }, Model: &model{},
		Now: func() time.Time { return time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC) }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(s.Wait)
	return s
}

// excerptFile is an excerpt of an item of a source, as a sync writes it.
func excerptFile(key, title string, chunks ...string) Change {
	source, _, _ := strings.Cut(key, ":")
	b, _ := json.Marshal(Excerpt{Key: key, Source: source, Kind: SourceUpload, Title: title, Version: "v1", Chunks: chunks})
	return Change{Path: excerptPath(key), Body: b}
}

// The index is in the database, shared by every replica: the leader keeps
// it as the wiki holds, any replica finds what it holds, every chunk of an
// excerpt is searched, and a file gone from the wiki is gone from it
// (KN-53).
func TestTheIndexIsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	store := NewMemoryStore()
	leader := pgReplica(t, d, store, true)
	other := pgReplica(t, d, store, false)
	// Every chunk of a long excerpt is kept: before, the first 24.
	long := chunks(strings.Repeat("The shop's queue drains in the evening, after the batch runs.\n\n", 1200))
	if len(long) <= 24 {
		t.Fatalf("the excerpt is cut at %d chunks", len(long))
	}
	long = append(long, "The zebrafish rotation of the on-call runs on Sundays.")
	page := Page{Path: "pages/runbook/restart-shop.md", Title: "Restart the shop", Kind: "runbook", Body: "Restart the shop's web pods one at a time, then check the queue."}
	retired := Page{Path: "pages/runbook/old-restart.md", Title: "Old restart", Kind: "runbook", Body: "Restart the shop by rebooting every node."}
	m := newManifest()
	m.Retired = map[string]Retirement{retired.Path: {Reason: "outdated"}}
	if err := store.Write(ctx, []Change{
		{Path: page.Path, Body: page.Format()}, {Path: retired.Path, Body: retired.Format()},
		excerptFile("documents:oncall.md", "On-call", long...),
		excerptFile("documents:network.md", "Network notes", "The database db-3 sits behind the firewall fw-1."),
		{Path: ManifestPath, Body: m.encode()},
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := leader.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// The read of the wiki brings the index up to it, in the background.
	leader.Wait()
	if docs, _, err := leader.pg.Count(ctx); err != nil || docs != 3 {
		t.Fatalf("the leader indexed %d documents: %v", docs, err)
	}
	// The other replica has no copy of the excerpts, and finds them.
	if err := other.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	other.mu.RLock()
	inMemory := other.index.Size()
	other.mu.RUnlock()
	if inMemory != 1 {
		t.Fatalf("a replica keeps the pages alone in memory, not %d documents", inMemory)
	}
	hits := other.Search("zebrafish rotation", "", 6)
	if len(hits) != 1 || hits[0].Kind != "excerpt" || hits[0].Source != "documents:oncall.md" || !strings.Contains(hits[0].Text, "zebrafish") {
		t.Fatalf("the last chunk of a long excerpt: %+v", hits)
	}
	hits = other.Search("restart the shop", "", 6)
	if len(hits) == 0 || hits[0].Kind != "page" || hits[0].Path != page.Path {
		t.Fatalf("the page: %+v", hits)
	}
	for _, h := range hits {
		if h.Path == retired.Path {
			t.Fatalf("a retired page is never searched: %+v", h)
		}
	}
	if hits := other.Search("firewall", "page", 6); len(hits) != 0 {
		t.Fatalf("pages only: %+v", hits)
	}
	if !other.Connected() {
		t.Error("the other replica is connected")
	}
	// Ask is ask_knowledge's: the same, through the database.
	if hits, err := other.Ask(ctx, "which firewall is the database behind", 6); err != nil || len(hits) != 1 || hits[0].Title != "Network notes" {
		t.Fatalf("ask: %+v, %v", hits, err)
	}
	// A search for people, records ranked with them.
	found := other.Find("database firewall", nil, 10, []Record{{Kind: KindFix, ID: "F-2", Title: "Open the firewall for db-3", Text: "The database could not be reached through fw-1."}})
	if found.Counts["excerpt"] != 1 || found.Counts[KindFix] != 1 || len(found.Hits) != 2 || len(found.Hits[0].Terms) == 0 {
		t.Fatalf("find: %+v", found)
	}
	// Unchanged files are not written again; a file gone is gone.
	if res, err := leader.SyncIndex(ctx); err != nil || res.Written != 0 || res.Kept != 3 {
		t.Fatalf("again: %+v, %v", res, err)
	}
	empty, _ := json.Marshal(Excerpt{Key: "documents:network.md", Chunks: []string{}})
	if err := store.Write(ctx, []Change{{Path: excerptPath("documents:network.md"), Body: empty}}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := leader.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	leader.Wait()
	if hits := other.Search("firewall", "", 6); len(hits) != 0 {
		t.Fatalf("a gone excerpt is found: %+v", hits)
	}
	var docs, passages int
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM knowledge_documents), (SELECT count(*) FROM knowledge_passages)").Scan(&docs, &passages); err != nil || docs != 2 || passages != len(long)+1 {
		t.Fatalf("the tables hold %d documents and %d passages: %v", docs, passages, err)
	}
	// Only the leader writes the index.
	if _, err := other.SyncIndex(ctx); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("another replica writes the index: %v", err)
	}
	// A source's removal takes its excerpts with it.
	if _, err := d.Pool().Exec(ctx, "DELETE FROM knowledge_sources WHERE knowledge_source_id = 'documents'"); err != nil {
		t.Fatal(err)
	}
	if hits := other.Search("zebrafish", "", 6); len(hits) != 0 {
		t.Fatalf("a removed source's excerpts: %+v", hits)
	}
}

// The leader brings the index up to the wiki after a sync by itself.
func TestASyncIsIndexedInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	store := NewMemoryStore()
	leader := pgReplica(t, d, store, true)
	start := leader.o.Now()
	if _, err := leader.Upload(ctx, admin, "documents", "plan.md", []byte("# Backups\n\nKeep backups for 30 days on the NAS.")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := leader.Status(ctx)
		if st.Last != nil && !st.Running && !st.Last.Started.Before(start) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sync did not end")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		if hits := leader.Search("backups NAS", "excerpt", 3); len(hits) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the excerpt is not in the index: %+v", leader.Search("backups NAS", "", 3))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
