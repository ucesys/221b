package fixes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

func hour(h int) time.Time { return time.Date(2026, 11, 19, h, 0, 0, 0, time.UTC) }

// kept saves a document through the database and through memory, as a
// ConfigMap keeps it, and fails unless both read back the same, field for
// field.
func kept(t *testing.T, b kv.Backend, doc record) {
	t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(doc)
	var want, got record
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	_, version, err := b.LoadKey(ctx, Key)
	if err != nil && !errors.Is(err, kv.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := b.SaveKey(ctx, Key, raw, version); err != nil {
		t.Fatalf("saving: %v", err)
	}
	back, _, err := b.LoadKey(ctx, Key)
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

func idOf(t *testing.T, d *db.DB, key string) (id string) {
	t.Helper()
	if err := d.Pool().QueryRow(context.Background(), "SELECT fix_id::text FROM fixes WHERE fix_key = $1", key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Every field of a fix is a column and comes back as a ConfigMap gives
// it; a fix keeps its row through a change; one that did not change is
// not written again; a fix that goes takes the cases it was seen in.
func TestFixesInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewPG(d)
	ctx := context.Background()
	full := Fix{ID: "FX-2", Title: "Web has no memory limit", Key: "pod.no-limits", Cluster: "prod", Resource: "shop/Deployment/web", Symptoms: "It is killed for memory.",
		Cause: "No limit is set.", Steps: "Raise the limit to 512Mi\nRoll the deployment", Commands: []string{"kubectl -n shop set resources deploy/web --limits=memory=512Mi", "kubectl -n shop rollout status deploy/web"},
		Prevention: "Set limits in the chart.", Status: Pending, Reason: ReasonContradicts, DuplicateOf: "FX-1", Contradicts: []string{"FX-1", "FX-3"}, Accepted: []string{"FX-3"},
		Cases:   []Seen{{Case: "C-12", At: hour(1), Cluster: "prod", By: "olivia"}, {Case: "C-14", At: hour(2)}},
		Created: hour(1), Updated: hour(3), CreatedBy: "olivia", UpdatedBy: "Housekeeping"}
	plain := func(n int) Fix {
		id := "FX-" + string(rune('0'+n))
		return Fix{ID: id, Title: "Fix " + id, Status: Active, Cases: []Seen{{Case: "C-" + string(rune('0'+n)), At: hour(n)}}, Created: hour(n), Updated: hour(n), CreatedBy: "olivia", UpdatedBy: "olivia"}
	}
	rec := record{Next: 4, Items: []Fix{plain(1), full, plain(3)}}
	kept(t, b, rec)
	row := idOf(t, d, "FX-2")
	var cases int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM fix_cases").Scan(&cases); err != nil || cases != 4 {
		t.Fatalf("fix_cases has %d rows: %v", cases, err)
	}
	// One changes, one comes in at the end, the first goes.
	full.Status, full.Reason, full.Updated = Active, "", hour(4)
	full.Cases = append(full.Cases, Seen{Case: "C-20", At: hour(4), By: "sam"})
	rec = record{Next: 5, Items: []Fix{full, plain(3), plain(4)}}
	kept(t, b, rec)
	if idOf(t, d, "FX-2") != row {
		t.Fatal("a fix that changed lost its row")
	}
	// Another order, as the housekeeping or a backup put back leaves it.
	rec.Items = []Fix{plain(4), full, plain(3)}
	kept(t, b, rec)
	// Another writer came first.
	_, version, _ := b.LoadKey(ctx, Key)
	rec.Next = 6
	raw, _ := json.Marshal(rec)
	if _, err := b.SaveKey(ctx, Key, raw, version); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveKey(ctx, Key, raw, version); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	rec.Items = nil
	kept(t, b, rec)
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM fix_cases) + (SELECT count(*) FROM fixes)").Scan(&cases); err != nil || cases != 0 {
		t.Fatalf("%d rows left of fixes that are gone: %v", cases, err)
	}
	// What the ConfigMap held is copied once.
	cm := &kv.Memory{}
	raw, _ = json.Marshal(record{Next: 3, Items: []Fix{plain(1), full}})
	_, _ = cm.SaveKey(ctx, Key, raw, "")
	fresh := NewPG(dbtest.Open(t))
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("copy: %v %v", copied, err)
	}
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || copied {
		t.Fatalf("second copy: %v %v", copied, err)
	}
}
