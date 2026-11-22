package knowledge

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// AIOps › Mycroft › Sources lists sources, not files (KN-50): one row a
// source, with its items, those failed and excluded, on or off, and its
// last sync.
func TestTheSourcesAreListedWithTheirLastSyncKN50(t *testing.T) {
	f, x := crawlFixture(t)
	res := webSite(t, f, Source{URL: x.URL + "/docs/", Depth: 2, Pages: 20})
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddText, Title: "Note a", Text: "Words of note a."}); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "status", Name: "Status", Kind: SourceLive, Addresses: []LiveAddress{{Kind: LivePage, URL: "https://status.example.com/"}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	if w := serve(t, f.s, &viewer, "GET", "sources", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer: %d %s", w.Code, w.Body)
	}
	w := serve(t, f.s, &bob, "GET", "sources", "", nil)
	var v SourcesView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != http.StatusOK {
		t.Fatalf("an Admin: %d %s", w.Code, w.Body)
	}
	if v.MayExclude || !v.MayRemove {
		t.Fatalf("an Admin's rights: %+v", v)
	}
	rows := map[string]SourceSummary{}
	for _, r := range v.Sources {
		rows[r.ID] = r
	}
	// The web site: what the items list holds of it, and the sync just run.
	var items ItemsPage
	_ = json.Unmarshal(serve(t, f.s, &bob, "GET", "items?source=site&limit=200", "", nil).Body.Bytes(), &items)
	site := rows["site"]
	if site.Name != "Docs site" || site.Kind != SourceWeb || site.Off || site.Items != items.Total || site.Items == 0 {
		t.Fatalf("the web site: %+v (items %d)", site, items.Total)
	}
	if ls := site.LastSync; ls == nil || ls.Changed == nil || *ls.Changed != res.Read || ls.Failed || ls.Every || ls.At.IsZero() {
		t.Fatalf("the web site's last sync: %+v (read %d)", site.LastSync, res.Read)
	}
	// The note's source counts it; the live source is never synced.
	if added := rows[AddedSourceID]; added.Items != 1 || added.Kind != SourceUpload {
		t.Fatalf("added in Mycroft: %+v", added)
	}
	if live := rows["status"]; live.Kind != SourceLive || live.Items != 1 || live.LastSync != nil {
		t.Fatalf("the live source: %+v", live)
	}
	// Excluded and failed items are counted.
	if _, err := f.s.Exclude(ctx, admin, "site", ExcludeInput{Item: "/docs/runbook.html"}); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(serve(t, f.s, &admin, "GET", "sources", "", nil).Body.Bytes(), &v)
	for _, r := range v.Sources {
		if r.ID == "site" && (r.Excluded != 1 || r.Items != site.Items) {
			t.Fatalf("an item excluded: %+v", r)
		}
	}
	if !v.MayExclude {
		t.Fatal("a SuperAdmin may exclude")
	}
}

// The last sync is the newest finished one that read the source, alone or
// with every source; a failed read says so; with no sync kept, the last
// read; a source never read has none.
func TestASourcesLastSyncKN50(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 22, h, 0, 0, 0, time.UTC) }
	x := Source{ID: "docs", Name: "Docs", Kind: SourceGit}
	st := Status{
		History: []SyncResult{
			{Source: "docs", SourceName: "Docs", Started: at(0), Finished: at(1), Read: 2},
			{Source: "other", SourceName: "Other", Started: at(2), Finished: at(3), Read: 9},
			// Still running: not finished.
			{Source: "docs", SourceName: "Docs", Started: at(4)},
		},
	}
	ls := lastSync(st, x)
	if ls == nil || !ls.At.Equal(at(1)) || ls.Changed == nil || *ls.Changed != 2 || ls.Failed || ls.Every {
		t.Fatalf("its own sync: %+v", ls)
	}
	// A sync of every source, newer: no count of its own.
	st.Last = &SyncResult{Started: at(5), Finished: at(6), Read: 40, Errors: []string{"Other: unreachable"}}
	if ls = lastSync(st, x); ls == nil || !ls.At.Equal(at(6)) || ls.Changed != nil || !ls.Every || ls.Failed {
		t.Fatalf("every source: %+v", ls)
	}
	// It failed to read this one.
	st.Last.Errors = append(st.Last.Errors, "Docs: authentication required")
	if ls = lastSync(st, x); ls == nil || !ls.Failed || ls.Error != "authentication required" {
		t.Fatalf("a failed read: %+v", ls)
	}
	// A sync of the source alone that failed, under the name it had then.
	st.Last = &SyncResult{Source: "docs", SourceName: "Old docs", Started: at(7), Finished: at(8), Errors: []string{"Old docs: no such repository"}}
	if ls = lastSync(st, Source{ID: "docs", Name: "Docs", Kind: SourceGit}); ls == nil || !ls.Failed || ls.Changed == nil || *ls.Changed != 0 || ls.Error != "no such repository" {
		t.Fatalf("its own sync, failed: %+v", ls)
	}
	// No sync kept names it: its last read, without a count.
	st = Status{Sources: map[string]SourceStatus{"docs": {LastRead: at(9), Items: 3}}}
	if ls = lastSync(st, x); ls == nil || !ls.At.Equal(at(9)) || ls.Changed != nil || ls.Failed {
		t.Fatalf("the last read: %+v", ls)
	}
	// Never read.
	if ls = lastSync(Status{}, x); ls != nil {
		t.Fatalf("never: %+v", ls)
	}
}
