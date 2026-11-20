package db_test

import (
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0037 (DB-66): the sign-in sessions' table is keyed by the SHA-256
// of the session's id, as text, and takes nothing else for a key; a row
// that was there, which no console wrote, does not stop the step.
func TestStep0037KeysTheSessionsByAHash(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 36); err != nil || n != 36 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	if _, err := d.Pool().Exec(ctx, "INSERT INTO auth_sessions (auth_session_id, username) VALUES ($1, 'by hand')", db.NewID()); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-36 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-36, err)
	}
	var rows int
	var kind string
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM auth_sessions), data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'auth_sessions' AND column_name = 'auth_session_id'`).Scan(&rows, &kind); err != nil || rows != 0 || kind != "text" {
		t.Fatalf("%d rows, the key is %s: %v", rows, kind, err)
	}
	insert := func(key string) error {
		_, err := d.Pool().Exec(ctx, "INSERT INTO auth_sessions (auth_session_id, groups_read_at, last_seen_at, expires_at) VALUES ($1, now(), now(), now())", key)
		return err
	}
	hash := strings.Repeat("0123456789abcdef", 4)
	if err := insert(hash); err != nil {
		t.Fatalf("a hash as the key: %v", err)
	}
	if err := insert(hash); !db.IsUnique(err) {
		t.Errorf("the same hash twice: %v", err)
	}
	// A session's id is 43 characters of URL-safe base64, and a signed
	// session's value has dots: neither is a key.
	for _, key := range []string{"", "Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV", "s1.payload.mac", strings.ToUpper(hash), hash + "0", db.NewID()} {
		if err := insert(key); err == nil {
			t.Errorf("the table took %q as a session's key", key)
		}
	}
	// Every column but the key has a default, and none is null.
	var nulls int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM auth_sessions s, jsonb_each(to_jsonb(s)) AS f(name, value) WHERE value = 'null'::jsonb`).Scan(&nulls); err != nil || nulls != 0 {
		t.Errorf("%d null columns: %v", nulls, err)
	}
}
