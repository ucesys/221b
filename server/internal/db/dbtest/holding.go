package dbtest

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Holding names the tables of d with a row that holds text in any of its
// columns, whatever the column's type (a list, JSON and bytes included),
// each with how many rows do. A test of a record set whose secret part
// stays in a Secret puts a marker in every secret part and asks that no
// table holds it. The marker is letters and digits, so it reads the same
// inside JSON.
func Holding(t testing.TB, d *db.DB, text string) map[string]int {
	t.Helper()
	ctx := context.Background()
	rows, err := d.Pool().Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(tables)
	if len(tables) == 0 {
		t.Fatal("the database has no tables to look in")
	}
	out := map[string]int{}
	for _, table := range tables {
		var n int
		// A row as JSON holds every column's text; bytes are written there
		// in hex, so the text is looked for in hex too.
		if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` r
			WHERE strpos(row_to_json(r)::text, $1) > 0 OR strpos(row_to_json(r)::text, encode(convert_to($1, 'UTF8'), 'hex')) > 0`, text).Scan(&n); err != nil {
			t.Fatalf("looking in %s: %v", table, err)
		}
		if n > 0 {
			out[table] = n
		}
	}
	return out
}
