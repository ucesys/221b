package knowledge

import (
	"errors"
	"strings"
	"testing"
)

// Mycroft writes pages from a source of high trust, not from a bulk one,
// whose items are excerpted and indexed alone; a person asks for a page
// from any item (KN-59).
func TestMycroftWritesPagesForWhatMatters(t *testing.T) {
	f, x := crawlFixture(t)
	res := webSite(t, f, Source{URL: x.URL + "/docs/", Depth: 2, Pages: 20, Trust: TrustNormal})
	if res.Read == 0 || res.Run != "" || res.Pages != 0 || len(f.model.calls()) != 0 {
		t.Fatalf("a source of normal trust went to Mycroft: %+v, %d calls", res, len(f.model.calls()))
	}
	if !strings.Contains(res.Text, "indexed for the search alone") {
		t.Fatalf("the report: %s", res.Text)
	}
	if hits := f.s.Search("restart web pods", "excerpt", 5); len(hits) == 0 {
		t.Fatal("its excerpts are not searched")
	}
	if n, _ := f.s.Waiting(ctx, admin); n != 0 {
		t.Fatalf("%d runs wait in the Inbox", n)
	}
	// On request: one item's page.
	if _, err := f.s.WritePage(ctx, viewer, "site", "/docs/runbook.html"); !errors.Is(err, ErrDenied) {
		t.Fatalf("a Viewer asks for a page: %v", err)
	}
	if _, err := f.s.WritePage(ctx, bob, "site", "/docs/nothing.html"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an item of none: %v", err)
	}
	out, err := f.s.WritePage(ctx, bob, "site", "/docs/runbook.html")
	if err != nil || out.Run == "" || out.Pages != 1 || !strings.Contains(out.Text, "wait for review") {
		t.Fatalf("a page asked: %+v, %v", out, err)
	}
	calls := f.model.calls()
	if len(calls) != 1 || strings.Count(calls[0], "<source ") != 1 || !strings.Contains(calls[0], "site:/docs/runbook.html") {
		t.Fatalf("Mycroft was given: %d calls", len(calls))
	}
	if acts := f.actions(); acts[len(acts)-1] != ActionPageRequest {
		t.Fatalf("audited: %v", acts)
	}
	f.s.mu.RLock()
	pages := f.s.manifest.Items[itemKey("site", "/docs/runbook.html")].Pages
	f.s.mu.RUnlock()
	if len(pages) != 1 {
		t.Fatalf("the item's pages: %v", pages)
	}
	// Trusted high, the source's changed items go to Mycroft.
	if _, err := f.s.SetSourceIndex(ctx, admin, "site", SourceIndex{Trust: TrustHigh}); err != nil {
		t.Fatal(err)
	}
	res, err = f.s.SyncWith(ctx, SyncRequest{Source: "site", Rebuild: true, By: "test"})
	if err != nil || res.Run == "" || len(f.model.calls()) < 2 {
		t.Fatalf("trusted high: %+v, %v", res, err)
	}
}
