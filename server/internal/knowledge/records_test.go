package knowledge

import (
	"errors"
	"strings"
	"testing"
)

// The console's records are written straight into the approved pages,
// found by ask_knowledge at once; Mycroft may not write their kinds, and
// with no wiki kept the answer is ErrNoStore (FX-09).
func TestWriteRecord(t *testing.T) {
	f := newFixture(t)
	path, err := f.s.WriteRecord(ctx, "exception", "EX-3", "Exception EX-3: No network policy", "# Exception EX-3\n\nShop sits behind its firewall. password=hunter2hunter2")
	if err != nil || path != "pages/exception/ex-3.md" {
		t.Fatalf("written: %q %v", path, err)
	}
	files, _ := f.store.Pull(ctx)
	body := string(files[path].Body)
	if !strings.Contains(body, "kind: exception") || !strings.Contains(body, "Shop sits behind its firewall") || strings.Contains(body, "hunter2hunter2") {
		t.Fatalf("the page: %s", body)
	}
	if p, ok := f.s.PageAt(path); !ok || p.Title != "Exception EX-3: No network policy" || p.Kind != "exception" {
		t.Fatalf("the local copy: %+v %v", p, ok)
	}
	if hits, _ := f.s.Ask(ctx, "firewall network policy", 3); len(hits) == 0 || hits[0].Path != path {
		t.Fatalf("ask: %+v", hits)
	}
	if _, err := f.s.WriteRecord(ctx, "runbook", "x", "t", "b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a runbook: %v", err)
	}
	if ValidPagePath("pages/rca/rca-1.md") {
		t.Fatal("Mycroft may write an RCA's page")
	}
	none := New(Options{Dir: t.TempDir()})
	if _, err := none.WriteRecord(ctx, "rca", "rca-1", "t", "b"); !errors.Is(err, ErrNoStore) {
		t.Fatalf("no wiki: %v", err)
	}
}
