package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

type toy struct {
	A string `json:"a"`
}

// toyDoc keeps a toy under key, in a settings scope.
func toyDoc(d *db.DB, key, scope, importAs string) *db.Doc[toy] {
	return &db.Doc[toy]{DB: d, Key: key, ImportAs: importAs,
		Read: func(ctx context.Context, q db.Querier) (toy, bool, error) {
			var v toy
			b, _, err := db.LoadScope(ctx, q, scope)
			if errors.Is(err, db.ErrNotFound) {
				return v, false, nil
			}
			if err != nil {
				return v, false, err
			}
			return v, true, json.Unmarshal(b, &v)
		},
		Write: func(ctx context.Context, tx pgx.Tx, _, next toy) error {
			b, _ := json.Marshal(next)
			_, err := db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
			return err
		}}
}

func held(t *testing.T, doc *db.Doc[toy], key string) string {
	t.Helper()
	b, _, err := doc.LoadKey(context.Background(), key)
	if errors.Is(err, kv.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	var v toy
	_ = json.Unmarshal(b, &v)
	return v.A
}

// Two record sets whose ConfigMaps name their key alike are each copied,
// under a name of their own (the fifth cluster test, F2: the jobs' and
// Automation's runs.json; before, the one copied first took the name and
// the other was never copied). And a record set whose name is new to a
// database that holds its records already is left as the database has it.
func TestRecordSetsWithOneKeyAreEachCopied(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	jobsCM, autoCM := &kv.Memory{}, &kv.Memory{}
	_, _ = jobsCM.SaveKey(ctx, "runs.json", []byte(`{"a":"the jobs' runs"}`), "")
	_, _ = autoCM.SaveKey(ctx, "runs.json", []byte(`{"a":"automation's runs"}`), "")

	// Under the key alone, the second is never copied.
	first, second := toyDoc(d, "runs.json", "toy-first", ""), toyDoc(d, "runs.json", "toy-second", "")
	if first.ImportID() != second.ImportID() {
		t.Fatal("two documents under one key have two names")
	}
	if copied, err := first.Import(ctx, autoCM, "configmap"); err != nil || !copied {
		t.Fatalf("the first: %v %v", copied, err)
	}
	if copied, err := second.Import(ctx, jobsCM, "configmap"); err != nil || copied || held(t, second, "runs.json") != "" {
		t.Fatalf("the second under the same name: %v %v", copied, err)
	}

	// Each under a name of its own: both are copied, once.
	jobs, auto := toyDoc(d, "runs.json", "toy-jobs", "jobs/runs.json"), toyDoc(d, "runs.json", "toy-auto", "automation/runs.json")
	if jobs.ImportID() == auto.ImportID() || jobs.ImportID() == first.ImportID() {
		t.Fatalf("names: %q %q", jobs.ImportID(), auto.ImportID())
	}
	for _, c := range []struct {
		doc  *db.Doc[toy]
		from kv.Backend
		want string
	}{{auto, autoCM, "automation's runs"}, {jobs, jobsCM, "the jobs' runs"}} {
		if copied, err := c.doc.Import(ctx, c.from, "configmap"); err != nil || !copied || held(t, c.doc, "runs.json") != c.want {
			t.Fatalf("%s: copied %v, holds %q: %v", c.want, copied, held(t, c.doc, "runs.json"), err)
		}
		if copied, err := c.doc.Import(ctx, c.from, "configmap"); err != nil || copied {
			t.Fatalf("%s again: %v %v", c.want, copied, err)
		}
	}

	// A database that copied the records under the old name holds them,
	// and has changed them since: the new name copies nothing over them.
	renamed := toyDoc(d, "runs.json", "toy-first", "renamed/runs.json")
	b, version, err := renamed.LoadKey(ctx, "runs.json")
	if err != nil || len(b) == 0 {
		t.Fatal(err)
	}
	if _, err := renamed.SaveKey(ctx, "runs.json", []byte(`{"a":"changed since"}`), version); err != nil {
		t.Fatal(err)
	}
	if copied, err := renamed.Import(ctx, autoCM, "configmap"); err != nil || copied || held(t, renamed, "runs.json") != "changed since" {
		t.Fatalf("a new name over records held: copied %v, holds %q: %v", copied, held(t, renamed, "runs.json"), err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM imports WHERE import_id = 'renamed/runs.json'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("the new name is recorded %d times: %v", n, err)
	}
}
