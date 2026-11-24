package knowledge

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

func ptr(s string) *string { return &s }

// lastAudit is the last event audited, of the action when one is named.
func lastAudit(f *fixture, action ...string) audit.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.audits) - 1; i >= 0; i-- {
		if len(action) == 0 || f.audits[i].Action == action[0] {
			return f.audits[i]
		}
	}
	return audit.Event{}
}

// uploadOf is an uploaded document or note as Uploaded documents lists it.
func uploadOf(t *testing.T, f *fixture, id string) UploadItem {
	t.Helper()
	v, err := f.s.Uploads(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range v.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no upload %s in %+v", id, v.Items)
	return UploadItem{}
}

// An uploaded document has a trust level of its own, its source's by
// default: a SuperAdmin chooses it at the upload and changes it on its
// row, audited; Mycroft writes pages by itself only from a document of
// high trust; an Admin's upload never changes it (KN-61).
func TestADocumentHasItsOwnTrust(t *testing.T) {
	f := newFixture(t)
	doc := func(c Caller, name, text string, trust *string) error {
		_, err := f.s.Add(ctx, c, AddInput{Kind: AddDocument, Name: name, Data: []byte(text), Trust: trust})
		f.s.Wait()
		return err
	}
	if err := doc(bob, "low.md", "# Low\n\nA vendor's note.", ptr(TrustLow)); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin chooses a trust: %v", err)
	}
	if err := doc(admin, "low.md", "# Low\n\nA vendor's note.", ptr("max")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a level of none: %v", err)
	}
	if _, err := f.s.Add(ctx, admin, AddInput{Kind: AddText, Title: "A note", Text: "Some text.", Trust: ptr(TrustLow)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a note's trust: %v", err)
	}
	if err := doc(admin, "low.md", "# Low\n\nRestart the vendor's appliance twice.", ptr("Low")); err != nil {
		t.Fatal(err)
	}
	if err := doc(admin, "plain.md", "# Plain\n\nRestart the web pods, then the cache.", ptr("")); err != nil {
		t.Fatal(err)
	}
	if e := lastAudit(f, ActionAdd); !strings.HasSuffix(e.Detail, "; trust its source's") {
		t.Fatalf("the upload's audit: %+v", e)
	}
	calls := strings.Join(f.model.calls(), "\n")
	if !strings.Contains(calls, "added:plain.md") || strings.Contains(calls, "added:low.md") {
		t.Fatalf("Mycroft was given the low one, or not the other: %s", calls)
	}
	if it := uploadOf(t, f, "low.md"); it.Trust != TrustLow || it.SourceTrust != TrustHigh {
		t.Fatalf("low.md: %+v", it)
	}
	if it := uploadOf(t, f, "plain.md"); it.Trust != "" || it.SourceTrust != TrustHigh {
		t.Fatalf("plain.md: %+v", it)
	}
	page, err := f.s.Items(ctx, admin, ItemsQuery{Source: AddedSourceID})
	if err != nil || !page.MayTrust {
		t.Fatalf("the items: %+v %v", page, err)
	}
	for _, it := range page.Items {
		if it.ID == "low.md" && (it.Trust != TrustLow || it.SourceTrust != TrustHigh) {
			t.Fatalf("its row: %+v", it)
		}
	}
	if page, _ := f.s.Items(ctx, bob, ItemsQuery{Source: AddedSourceID}); page.MayTrust {
		t.Fatal("an Admin may set a document's trust")
	}

	// On its row: a SuperAdmin's, audited with what it was.
	if _, err := f.s.SetDocumentTrust(ctx, bob, AddedSourceID, "plain.md", TrustLow); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin sets it: %v", err)
	}
	if _, err := f.s.SetDocumentTrust(ctx, admin, AddedSourceID, "none.md", TrustLow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a document of none: %v", err)
	}
	if _, err := f.s.SetDocumentTrust(ctx, admin, "site", "plain.md", TrustLow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a source of none: %v", err)
	}
	w := serve(t, f.s, &admin, "PUT", "sources/added/documents/plain.md/trust", `{"trust":"normal"}`, nil)
	var got DocumentTrust
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.Trust != TrustNormal || got.Level != TrustNormal || got.SourceTrust != TrustHigh {
		t.Fatalf("set on its row: %d %s", w.Code, w.Body)
	}
	if e := lastAudit(f, ActionIndexSettings); e.Action != ActionIndexSettings || e.Target != "added:plain.md" || e.Detail != "Added in Mycroft › plain.md: trust normal, was high (its source's)" {
		t.Fatalf("audited: %+v", e)
	}
	if it := uploadOf(t, f, "plain.md"); it.Trust != TrustNormal {
		t.Fatalf("kept: %+v", it)
	}
	// It is in the wiki's manifest, and a rebuild keeps it.
	if _, err := f.s.SyncWith(ctx, SyncRequest{Source: AddedSourceID, Rebuild: true, By: "test"}); err != nil {
		t.Fatal(err)
	}
	files, _ := f.store.Pull(ctx)
	m := parseManifest(files[ManifestPath].Body)
	if st := m.Items["added:plain.md"]; st == nil || st.Trust != TrustNormal {
		t.Fatalf("the manifest: %+v", st)
	}
	n := len(f.actions())
	if _, err := f.s.SetDocumentTrust(ctx, admin, AddedSourceID, "plain.md", TrustNormal); err != nil || len(f.actions()) != n {
		t.Fatalf("unchanged, audited again: %v", err)
	}

	// An Admin uploads it again: it keeps its own trust.
	if err := doc(bob, "low.md", "# Low\n\nRestart the vendor's appliance three times.", nil); err != nil {
		t.Fatal(err)
	}
	if it := uploadOf(t, f, "low.md"); it.Trust != TrustLow || it.State != StateRead {
		t.Fatalf("an Admin's upload changed it: %+v", it)
	}
	if strings.Contains(strings.Join(f.model.calls(), "\n"), "added:low.md") {
		t.Fatal("a document of low trust went to Mycroft")
	}
	// Removed, it goes with it: uploaded again it is its source's.
	if _, err := f.s.RemoveUpload(ctx, bob, AddedSourceID, "low.md"); err != nil {
		t.Fatal(err)
	}
	if err := doc(bob, "low.md", "# Low\n\nRestart the vendor's appliance.", nil); err != nil {
		t.Fatal(err)
	}
	if it := uploadOf(t, f, "low.md"); it.Trust != "" {
		t.Fatalf("removed and uploaded again: %+v", it)
	}
}

// A document's own trust weighs its passages in the search, its sections'
// too, over its source's (KN-61).
func TestADocumentsTrustWeighsItsPassages(t *testing.T) {
	d := dbtest.Open(t)
	store := NewMemoryStore()
	s := pgReplica(t, d, store, true)
	var audits []audit.Event
	s.o.Audit = func(e audit.Event) { audits = append(audits, e) }
	text := "To restore an etcd snapshot, stop the server on every node and run the cluster reset with the snapshot's path."
	m := newManifest()
	m.Items["added:a.md"] = &ItemState{Source: AddedSourceID, ID: "a.md", Title: "A", Type: AddDocument, Hash: "h", Version: "v1", Trust: TrustLow, Sections: 1}
	m.Items["added:a.md#restore"] = &ItemState{Source: AddedSourceID, ID: "a.md#restore", Of: "a.md", Title: "A › Restore", Type: AddDocument, Hash: "h", Version: "v1"}
	m.Items["added:b.md"] = &ItemState{Source: AddedSourceID, ID: "b.md", Title: "B", Type: AddDocument, Hash: "h", Version: "v1"}
	if err := store.Write(ctx, []Change{excerptFile("added:a.md#restore", "A › Restore", text), excerptFile("added:b.md", "B", text), {Path: ManifestPath, Body: m.encode()}}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	titles := func() string {
		t.Helper()
		if _, err := s.SyncIndex(ctx); err != nil {
			t.Fatal(err)
		}
		hits, err := s.Ask(ctx, "restore an etcd snapshot", 6)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Title+" ["+h.Trust+"]")
		}
		return strings.Join(out, ", ")
	}
	if got := titles(); got != "B [high], A › Restore [low]" {
		t.Fatalf("by the documents' trust: %s", got)
	}
	if _, err := s.SetDocumentTrust(ctx, admin, AddedSourceID, "a.md", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDocumentTrust(ctx, admin, AddedSourceID, "b.md", TrustLow); err != nil {
		t.Fatal(err)
	}
	if got := titles(); got != "A › Restore [high], B [low]" {
		t.Fatalf("changed on their rows: %s", got)
	}
	if len(audits) != 2 || audits[0].Detail != "Added in Mycroft › a.md: trust high (its source's), was low" {
		t.Fatalf("audited: %+v", audits)
	}
	if _, err := s.SetDocumentTrust(ctx, admin, AddedSourceID, "a.md#restore", TrustLow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a section's own trust: %v", err)
	}
}

// A sync writes the manifest it read at its start: a trust set on a
// document's row while it ran is kept, and the one chosen at an upload it
// let go (KN-61).
func TestASyncKeepsTheTrustSetWhileItRan(t *testing.T) {
	now, m := newManifest(), newManifest()
	for _, id := range []string{"a.md", "b.md", "c.md"} {
		now.Items["added:"+id] = &ItemState{Source: AddedSourceID, ID: id}
		m.Items["added:"+id] = &ItemState{Source: AddedSourceID, ID: id}
	}
	atStart := map[string]string{"added:a.md": "", "added:b.md": TrustLow, "added:c.md": ""}
	now.Items["added:a.md"].Trust = TrustNormal
	m.Items["added:b.md"].Trust = TrustLow
	now.Items["added:b.md"].Trust = TrustLow
	keepTrust(now, atStart, m, []pendingDoc{{source: AddedSourceID, name: "c.md", trust: TrustLow, setTrust: true}, {source: AddedSourceID, name: "b.md"}})
	if m.Items["added:a.md"].Trust != TrustNormal || m.Items["added:b.md"].Trust != TrustLow || m.Items["added:c.md"].Trust != TrustLow {
		t.Fatalf("kept: %q %q %q", m.Items["added:a.md"].Trust, m.Items["added:b.md"].Trust, m.Items["added:c.md"].Trust)
	}
}
