package lessons

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

func hour(h int) time.Time { return time.Date(2026, 11, 19, h, 0, 0, 0, time.UTC) }

// Every field of an accepted exception is a column and comes back as a
// ConfigMap gives it; the list keeps its order, the newest first; an
// exception keeps its row through a change; a save over a stale version
// is refused; what the ConfigMap held is copied once.
func TestExceptionsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewExceptionsPG(d)
	ctx := context.Background()
	kept := func(doc exceptionsRecord) {
		t.Helper()
		raw, _ := json.Marshal(doc)
		var want, got exceptionsRecord
		_ = json.Unmarshal(raw, &want)
		_, version, err := b.LoadKey(ctx, ExceptionsKey)
		if err != nil && !errors.Is(err, kv.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := b.SaveKey(ctx, ExceptionsKey, raw, version); err != nil {
			t.Fatalf("saving: %v", err)
		}
		back, _, err := b.LoadKey(ctx, ExceptionsKey)
		if err != nil {
			t.Fatalf("reading back: %v", err)
		}
		_ = json.Unmarshal(back, &got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("the database reads back\n%s\nand a ConfigMap\n%s", back, raw)
		}
	}
	idOf := func(key string) (id string) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, "SELECT exception_id::text FROM exceptions WHERE exception_key = $1", key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	full := Exception{ID: "EX-2", Cluster: "prod", Resource: "shop/Deployment/web", Check: "pod.no-limits", Title: "web has no memory limit", Detail: "No limit is set on web.",
		Finding: "run-7.2", Run: "run-7", Job: "nightly-audit", Reason: "It is a batch job that sizes itself.", By: "bob@example.com", At: hour(1), Expires: hour(20),
		RemovedBy: "Housekeeping", RemovedAt: hour(9), Wiki: "exceptions/ex-2.md", WikiError: "the store was away", Case: "C-12", Matches: 3, LastMatched: hour(8), LastRun: "run-9",
		Updated: hour(9), DuplicateOf: "EX-1"}
	plain := func(n int) Exception {
		return Exception{ID: "EX-" + string(rune('0'+n)), Check: "node.pressure", Title: "Pressure", Reason: "Known.", By: "bob@example.com", At: hour(n)}
	}
	// The newest is first.
	rec := exceptionsRecord{Next: 4, Items: []Exception{plain(3), full, plain(1)}}
	kept(rec)
	row, other := idOf("EX-2"), idOf("EX-1")
	// It matches again, a new one comes in at the front, the oldest goes.
	full.Matches, full.LastMatched, full.LastRun = 4, hour(10), "run-10"
	rec = exceptionsRecord{Next: 5, Items: []Exception{plain(4), plain(3), full}}
	kept(rec)
	if idOf("EX-2") != row {
		t.Fatal("an exception that matched again lost its row")
	}
	// Any other order is kept too.
	rec.Items = []Exception{full, plain(1), plain(4), plain(3)}
	kept(rec)
	if idOf("EX-1") == other {
		t.Fatal("an exception that went and came back kept its row")
	}
	_, version, _ := b.LoadKey(ctx, ExceptionsKey)
	rec.Next = 6
	raw, _ := json.Marshal(rec)
	if _, err := b.SaveKey(ctx, ExceptionsKey, raw, version); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveKey(ctx, ExceptionsKey, raw, version); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	rec.Items = nil
	kept(rec)
	var n int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM exceptions").Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows left: %v", n, err)
	}
	cm := &kv.Memory{}
	raw, _ = json.Marshal(exceptionsRecord{Next: 3, Items: []Exception{full, plain(1)}})
	_, _ = cm.SaveKey(ctx, ExceptionsKey, raw, "")
	fresh := NewExceptionsPG(dbtest.Open(t))
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("copy: %v %v", copied, err)
	}
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || copied {
		t.Fatalf("second copy: %v %v", copied, err)
	}
}
