package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

func hour(h int) time.Time { return time.Date(2026, 11, 19, h, 0, 0, 0, time.UTC) }

// keptAs saves a document through the database and fails unless it reads
// back as a ConfigMap gives it: as its own JSON does, field for field.
func keptAs[T any](t *testing.T, b kv.Backend, key string, doc T) {
	t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(doc)
	var want, got T
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	_, version, err := b.LoadKey(ctx, key)
	if err != nil && !errors.Is(err, kv.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := b.SaveKey(ctx, key, raw, version); err != nil {
		t.Fatalf("saving: %v", err)
	}
	back, _, err := b.LoadKey(ctx, key)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if err := json.Unmarshal(back, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the database reads back\n%s\nand a ConfigMap\n%s", back, raw)
	}
}

func rowsIn(t *testing.T, d *db.DB, table string) (n int) {
	t.Helper()
	if err := d.Pool().QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// copiedOnce copies a ConfigMap's document into a new database, twice.
func copiedOnce[T any](t *testing.T, doc *db.Doc[T], v T) {
	t.Helper()
	ctx := context.Background()
	cm := &kv.Memory{}
	raw, _ := json.Marshal(v)
	_, _ = cm.SaveKey(ctx, doc.Key, raw, "")
	if copied, err := doc.Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("copy: %v %v", copied, err)
	}
	if copied, err := doc.Import(ctx, cm, "test"); err != nil || copied {
		t.Fatalf("second copy: %v %v", copied, err)
	}
}

// Every field of a source is a column and comes back as a ConfigMap gives
// it, with its addresses and exclusions; the sources keep their order; a
// source that goes takes what hangs from it; where the wiki is kept is
// the settings scope.
func TestKnowledgeSettingsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewSettingsPG(d)
	pin := &certpin.Pin{SHA256: "ab:cd", Subject: "CN=wiki", Issuer: "CN=ca", NotAfter: hour(20), By: "ann", At: hour(2)}
	full := Source{ID: "infra", Name: "Everything", Kind: SourceWeb, Off: true, BaseURL: "https://confluence.example", Flavor: "datacenter", Email: "ann@example.com", Space: "INFRA",
		URL: "https://docs.example/start", Branch: "main", Path: "runbooks", Username: "reader", Repository: "runbooks", Bucket: "docs", Prefix: "ops/", Region: "eu-west-1",
		Endpoint: "https://minio.example", PathStyle: true, Credentials: CredentialsIdentity, Addresses: []LiveAddress{{Kind: LivePage, URL: "https://status.example"},
			{Kind: LiveConfluenceSearch, URL: "https://confluence.example", Space: "OPS"}}, Auth: LiveAuthBasic, Depth: 2, Pages: 40,
		Excluded:     []Exclusion{{Pattern: "drafts/", Reason: "not final", By: "ann", At: hour(3)}, {Pattern: "12345", Under: true, Item: true, Title: "Old plans", By: "bob", At: hour(4)}},
		MigratedFrom: []string{"https://docs.example/a", "https://docs.example/b"}, CAPEM: "-----BEGIN CERTIFICATE-----", PinnedCert: pin, Added: hour(1), AddedBy: "ann",
		Credential: "cred-1", CredMigrated: true}
	set := Settings{Sources: []Source{{ID: ConsoleSourceID, Name: "This console", Kind: SourceConsole}, full, {ID: "documents", Name: "Uploaded documents", Kind: SourceUpload}},
		Store:     StoreSettings{Kind: StoreS3, Endpoint: "https://minio.example", Region: "us-east-1", Bucket: "wiki", Prefix: "kb/", PathStyle: true, Credentials: CredentialsKeys, SSE: "aws:kms", KMSKeyID: "k1", CAPEM: "pem", PinnedCert: pin, RefreshMinutes: 5, SavedBy: "ann", SavedAt: hour(5)},
		StaleDays: 45, Migrated: []string{"web-sources"}}
	keptAs(t, b, SettingsKey, set)
	if a, e := rowsIn(t, d, "knowledge_source_addresses"), rowsIn(t, d, "source_exclusions"); rowsIn(t, d, "knowledge_sources") != 3 || a != 2 || e != 2 {
		t.Fatalf("%d addresses, %d exclusions", a, e)
	}
	// One changes, the order changes, one goes.
	full.Off, full.PinnedCert, full.Excluded = false, nil, full.Excluded[:1]
	set.Sources = []Source{full, set.Sources[0]}
	set.StaleDays = 0
	keptAs(t, b, SettingsKey, set)
	set.Sources = nil
	keptAs(t, b, SettingsKey, set)
	if n := rowsIn(t, d, "knowledge_sources") + rowsIn(t, d, "knowledge_source_addresses") + rowsIn(t, d, "source_exclusions"); n != 0 {
		t.Fatalf("%d rows left of sources that are gone", n)
	}
	copiedOnce(t, NewSettingsPG(dbtest.Open(t)), Settings{Sources: []Source{full}, Store: StoreSettings{Kind: StoreGit, URL: "https://git.example/wiki.git"}})
}

// The syncs' state: each source's last read, the last sync whole and the
// history in short are rows and come back as a ConfigMap gives them.
func TestKnowledgeStatusInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewStatusPG(d)
	last := SyncResult{Run: "r7", Started: hour(1), Finished: hour(2), By: "Sync now", Full: true, Rebuild: true, Read: 12, Unchanged: 30, Gone: 2, Pages: 5, Proposals: 1,
		Reported: []string{"infra/42: a secret was left out"}, Errors: []string{"runbooks: the repository did not answer"}, NotProposed: []string{"a fleet that selects no node"},
		Text: "Read 12 items.", Source: "infra", SourceName: "Confluence space INFRA", Skipped: 3, SkippedWhy: "too large",
		Batches: []BatchOutcome{{Items: []string{"infra/1", "infra/2"}, Outcome: BatchOK, Pages: 2}, {Items: []string{"infra/3"}, Outcome: BatchFailed, Note: "the model did not answer"}},
		Unsent:  4, Model: "claude-opus-5-5", TokensIn: 12000, TokensOut: 900, CostUSD: 0.42, Fixes: []string{"FX-1"}, Contradictions: []string{"pages/web.md contradicts FX-2"}}
	short := func(n int) SyncResult {
		return SyncResult{Run: "r" + string(rune('0'+n)), Started: hour(n), Finished: hour(n), By: "knowledge sync", Read: n, Text: "ok"}
	}
	st := Status{Running: true, Last: &last, History: []SyncResult{short(5), short(6), last},
		Sources: map[string]SourceStatus{"infra": {LastRead: hour(2), Items: 42, Error: "it answered 503", Reported: 1, Skipped: 3, SkippedWhy: "too large", LastFull: hour(1)}, "documents": {Items: 0}}}
	keptAs(t, b, StatusKey, st)
	if s, n := rowsIn(t, d, "knowledge_syncs"), rowsIn(t, d, "knowledge_sync_batches"); s != 4 || n != 4 || rowsIn(t, d, "knowledge_source_reads") != 2 {
		t.Fatalf("%d syncs, %d batches", s, n)
	}
	// The next sync ends: the oldest of the history goes, a source's read changes, one source goes.
	next := short(8)
	st.Running, st.Last, st.History = false, &next, []SyncResult{short(6), last, next}
	st.Sources = map[string]SourceStatus{"infra": {LastRead: hour(8), Items: 43}}
	keptAs(t, b, StatusKey, st)
	// Nothing is kept of the syncs, as after a clean start; the status is still there.
	keptAs(t, b, StatusKey, Status{})
	if n := rowsIn(t, d, "knowledge_syncs") + rowsIn(t, d, "knowledge_sync_batches") + rowsIn(t, d, "knowledge_source_reads"); n != 0 {
		t.Fatalf("%d rows left", n)
	}
	copiedOnce(t, NewStatusPG(dbtest.Open(t)), st)
}

// Mycroft's runs with their page changes and decisions, the page requests
// and the corrections are rows and come back as a ConfigMap gives them; a
// run that did not change keeps its row.
func TestKnowledgeDraftsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewDraftsPG(d)
	ctx := context.Background()
	full := Run{ID: "r7", Started: hour(1), By: "Sync now", StartedBy: "ann", State: StatePartial, Store: StoreGit, Reported: []string{"infra/42: a secret was left out"}, Decided: hour(6),
		Pages: []DraftPage{
			{Path: "pages/web.md", Title: "Web", Kind: "runbook", Critical: true, New: true, Base: "v3", Diff: "--- pages/web.md\n+++ pages/web.md\n+limit 512Mi\n", Added: 1, Removed: 0, Clipped: true,
				Sources: []SourceRef{{Key: "infra/1", Title: "Web runbook", Version: "7", Date: hour(0), URL: "https://confluence.example/1"}, {Key: "fixes/FX-1", Title: "Raise the limit"}},
				State:   "approved", Approvals: []Approval{{By: "ann", Name: "Ann", At: hour(3)}, {By: "bob", Name: "Bob", At: hour(4)}}, Reason: "two people approved",
				Contradicts: []Contradiction{{Fix: "FX-2", Title: "Move to the operator", Cases: 2, Why: "It sets no limit."}}},
			{Path: "pages/db.md", Title: "Database", Kind: "page", Diff: "", State: "waiting"}},
		Decisions: []Decision{{By: "ann", Name: "Ann", At: hour(3), Action: "approve", Comment: "fine", Pages: []string{"pages/web.md"}}, {By: "bob", Name: "Bob", At: hour(4), Action: "reject"}}}
	plain := func(n int) Run {
		return Run{ID: "r" + string(rune('0'+n)), Started: hour(n), By: "knowledge sync", State: StateWaiting, Store: StoreConfigMap, Pages: []DraftPage{{Path: "pages/a.md", Title: "A", Kind: "page", Diff: "+a\n", Added: 1, State: "waiting"}}}
	}
	doc := draftsDoc{Runs: []Run{plain(5), full, plain(8)},
		Requests: []PageRequest{{ID: "q1", Action: RequestRetire, Path: "pages/old.md", Title: "Old", Reason: "Replaced.", By: "ann", Name: "Ann", At: hour(2), State: RequestWaiting},
			{ID: "q2", Action: RequestActivate, Path: "pages/web.md", Title: "Web", Reason: "Needed again.", By: "bob", Name: "Bob", At: hour(3), State: "approved", DecidedBy: "ann", DecidedName: "Ann", Decided: hour(4), Comment: "yes"}},
		Corrections: []Correction{{ID: "c1", Path: "pages/web.md", Title: "Web", By: "sam", Name: "Sam", At: hour(5), State: "drafted", Run: "r8", Note: "The limit is 512Mi now."}, {ID: "c2", Path: "pages/db.md", Title: "Database", By: "sam", Name: "Sam", At: hour(6), State: "sent"}}}
	keptAs(t, b, DraftsKey, doc)
	for table, want := range map[string]int{"wiki_runs": 3, "wiki_changes": 4, "wiki_change_sources": 2, "wiki_change_approvals": 2, "wiki_change_contradictions": 1, "wiki_run_decisions": 2,
		"wiki_page_requests": 2, "wiki_corrections": 2} {
		if got := rowsIn(t, d, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
	rowOf := func(key string) (id string) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, "SELECT wiki_run_id::text FROM wiki_runs WHERE run_key = $1", key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	untouched := rowOf("r7")
	// A run is decided, the oldest goes, a request is decided, a correction goes.
	changed := plain(8)
	changed.State, changed.Decided = StateDone, hour(9)
	doc.Runs = []Run{full, changed, plain(9)}
	doc.Requests[0].State, doc.Requests[0].DecidedBy, doc.Requests[0].Decided = "rejected", "bob", hour(9)
	doc.Corrections = doc.Corrections[1:]
	keptAs(t, b, DraftsKey, doc)
	if rowOf("r7") != untouched {
		t.Fatal("a run that did not change was written again")
	}
	_, version, _ := b.LoadKey(ctx, DraftsKey)
	raw, _ := json.Marshal(draftsDoc{Runs: []Run{full}})
	if _, err := b.SaveKey(ctx, DraftsKey, raw, version); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveKey(ctx, DraftsKey, raw, version); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	if err := b.DeleteKey(ctx, DraftsKey); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"wiki_runs", "wiki_changes", "wiki_change_sources", "wiki_change_approvals", "wiki_change_contradictions", "wiki_run_decisions", "wiki_page_requests", "wiki_corrections"} {
		if n := rowsIn(t, d, table); n != 0 {
			t.Errorf("%s has %d rows left", table, n)
		}
	}
	copiedOnce(t, NewDraftsPG(dbtest.Open(t)), doc)
}
