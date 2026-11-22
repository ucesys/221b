package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0047 on a database that holds applications (DB-92): one deployed
// to a fleet its cluster has a row of keys to that row, and not to the
// fleet of the same id on another cluster; one whose fleet has no row,
// and one with no fleet, key to none.
func TestStep0047KeysAnApplicationToItsFleet(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 46); err != nil || n != 46 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	const mine, theirs = "00000000-0000-7000-8000-0000000000f1", "00000000-0000-7000-8000-0000000000f2"
	for _, q := range []string{
		"INSERT INTO clusters (cluster_id) VALUES ('a'), ('b')",
		"INSERT INTO fleets (fleet_id, cluster_id, fleet_key, kind) VALUES ('" + theirs + "', 'b', 'web', 'selector'), ('" + mine + "', 'a', 'web', 'selector')",
		`INSERT INTO applications (application_id, cluster_id, namespace, name, fleet_key) VALUES
			('` + db.NewID() + `', 'a', 'web', 'shop', 'web'), ('` + db.NewID() + `', 'a', 'web', 'blog', 'gone'), ('` + db.NewID() + `', 'a', 'web', 'wiki', '')`,
	} {
		if _, err := d.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-46 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-46, err)
	}
	var shop, blog, wiki string
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT coalesce(fleet_id::text, '') FROM applications WHERE name = 'shop'),
		(SELECT coalesce(fleet_id::text, '') FROM applications WHERE name = 'blog'), (SELECT coalesce(fleet_id::text, '') FROM applications WHERE name = 'wiki')`).Scan(&shop, &blog, &wiki); err != nil {
		t.Fatal(err)
	}
	if shop != mine || blog != "" || wiki != "" {
		t.Fatalf("shop %q (want %s), blog %q, wiki %q", shop, mine, blog, wiki)
	}
}
