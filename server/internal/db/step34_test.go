package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0034 on a database that holds rows (DB-60): the rows credentials
// holds with only an id stay, not kept, and what points at them still
// does; a source's credential becomes a key, none of them null, and one
// the table did not know gets a row with only its id.
func TestStep0034KeepsTheRowsWithOnlyAnID(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 33); err != nil || n != 33 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	repo := db.NewID()
	exec("INSERT INTO credentials (credential_id) VALUES ('cr-aaaaaaaaaaaa')")
	exec("INSERT INTO repositories (repository_id, credential_id, name) VALUES ($1, 'cr-aaaaaaaaaaaa', 'charts')", repo)
	exec("INSERT INTO knowledge_sources (knowledge_source_id, name, credential_id) VALUES ('src-none', 'wiki', ''), ('src-known', 'docs', 'cr-aaaaaaaaaaaa'), ('src-new', 'site', 'cr-bbbbbbbbbbbb')")
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-33 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-33, err)
	}
	var rows, kept int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE is_kept) FROM credentials WHERE credential_id IN ('cr-aaaaaaaaaaaa', 'cr-bbbbbbbbbbbb')").Scan(&rows, &kept); err != nil || rows != 2 || kept != 0 {
		t.Fatalf("%d rows, %d of them kept: %v", rows, kept, err)
	}
	var of string
	if err := d.Pool().QueryRow(ctx, "SELECT coalesce(credential_id, '') FROM repositories WHERE repository_id = $1", repo).Scan(&of); err != nil || of != "cr-aaaaaaaaaaaa" {
		t.Fatalf("the repository's credential is %q: %v", of, err)
	}
	for id, want := range map[string]string{"src-none": "", "src-known": "cr-aaaaaaaaaaaa", "src-new": "cr-bbbbbbbbbbbb"} {
		var got *string
		if err := d.Pool().QueryRow(ctx, "SELECT credential_id FROM knowledge_sources WHERE knowledge_source_id = $1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (want == "") || (got != nil && *got != want) {
			t.Errorf("the source %s names %v, want %q", id, got, want)
		}
	}
	// A key from now on: a credential that is not there is refused, and no
	// two kept credentials have one name, whatever its case.
	if _, err := d.Pool().Exec(ctx, "INSERT INTO knowledge_sources (knowledge_source_id, credential_id) VALUES ('src-bad', 'cr-nowhere')"); !db.IsForeignKey(err) {
		t.Errorf("a source of a credential that is not there: %v", err)
	}
	exec("UPDATE credentials SET is_kept = true, name = 'GitHub' WHERE credential_id = 'cr-aaaaaaaaaaaa'")
	if _, err := d.Pool().Exec(ctx, "UPDATE credentials SET is_kept = true, name = 'github' WHERE credential_id = 'cr-bbbbbbbbbbbb'"); !db.IsUnique(err) {
		t.Errorf("a second credential of one name: %v", err)
	}
	// The rows with only an id have no name, and any number of them may.
	exec("INSERT INTO credentials (credential_id) VALUES ('cr-cccccccccccc'), ('cr-dddddddddddd')")
}
