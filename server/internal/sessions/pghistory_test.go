package sessions

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

func recordAt(id string, min int) Record {
	exit := 0
	return Record{ID: id, Cluster: "prod", Tool: "shell", Summary: "shell in web-1", Namespace: "shop", Pod: "web-1", Container: "web", Owner: "Ada", OwnerUser: "ada",
		Approver: "Bob", Reason: "a stuck request", Created: time.Date(2026, 11, 18, 10, min, 0, 0, time.UTC), Started: time.Date(2026, 11, 18, 10, min, 5, 0, time.UTC),
		Ended: time.Date(2026, 11, 18, 10, min, 50, 0, time.UTC), EndedBy: "owner", EndNote: "closed", Exit: &exit, Recorded: true, Bytes: 2048, Conns: 2, Watchers: []string{"Cy"}}
}

// Every field of a record comes back; the newest ended is first; no more
// than Keep are kept; a record added twice is kept once.
func TestPGHistory(t *testing.T) {
	h := &PGHistory{DB: dbtest.Open(t), Keep: 3}
	a, b := recordAt("00000000000000aa", 1), recordAt("00000000000000bb", 2)
	b.Exit, b.Watchers, b.Started = nil, nil, time.Time{}
	h.Add(a)
	h.Add(b)
	h.Add(a)
	if got := h.List(); !reflect.DeepEqual(got, []Record{b, a}) {
		t.Fatalf("got  %+v\nwant %+v", got, []Record{b, a})
	}
	h.Add(recordAt("00000000000000cc", 3))
	h.Add(recordAt("00000000000000dd", 4))
	got := h.List()
	if len(got) != 3 || got[0].ID != "00000000000000dd" || got[2].ID != "00000000000000bb" {
		t.Fatalf("after five: %d kept, from %s to %s", len(got), got[0].ID, got[len(got)-1].ID)
	}
}

// A backup's records are merged by id, and the ConfigMap's are copied
// once, with what it got later copied by a later start.
func TestPGHistoryMergeAndImport(t *testing.T) {
	h := &PGHistory{DB: dbtest.Open(t), Keep: 50}
	ctx := context.Background()
	h.Add(recordAt("00000000000000aa", 1))
	n, err := h.Merge(ctx, []Record{recordAt("00000000000000aa", 1), recordAt("00000000000000bb", 2)})
	if err != nil || n != 1 {
		t.Fatalf("merged %d: %v", n, err)
	}
	old := &MemoryHistory{}
	old.Add(recordAt("00000000000000cc", 3))
	if n, err := h.Import(ctx, old, "test"); err != nil || n != 1 {
		t.Fatalf("copied %d: %v", n, err)
	}
	if n, err := h.Import(ctx, old, "test"); err != nil || n != 0 {
		t.Fatalf("copied %d the second time: %v", n, err)
	}
	// What a console rolled back to a release without a database added to
	// the ConfigMap since is copied by the next start, and nothing older.
	old.Add(recordAt("00000000000000dd", 4))
	if n, err := h.Import(ctx, old, "test"); err != nil || n != 1 {
		t.Fatalf("copied %d after the rollback: %v", n, err)
	}
	if n, err := h.Import(ctx, old, "test"); err != nil || n != 0 {
		t.Fatalf("copied %d the time after: %v", n, err)
	}
	if got := h.List(); len(got) != 4 || got[0].ID != "00000000000000dd" {
		t.Fatalf("%d kept, the newest %s", len(got), got[0].ID)
	}
}
