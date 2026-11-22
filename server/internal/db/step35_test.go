package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0035 on a database that holds rows (DB-62): applications, which
// held none, is made again, and the rows of the tables it points at stay
// as they were. An application then names a cluster that has a row, at
// most one application has a cluster's namespace and name, and removing a
// repository or a credential's row empties the key and keeps the
// application.
func TestStep0035MakesApplicationsAgain(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 34); err != nil || n != 34 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	repo := db.NewID()
	exec("INSERT INTO clusters (cluster_id) VALUES ('local')")
	exec("INSERT INTO credentials (credential_id) VALUES ('cr-aaaaaaaaaaaa')")
	exec("INSERT INTO repositories (repository_id, credential_id, name) VALUES ($1, 'cr-aaaaaaaaaaaa', 'charts')", repo)
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-34 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-34, err)
	}
	var rows int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM clusters) + (SELECT count(*) FROM credentials) + (SELECT count(*) FROM repositories WHERE credential_id = 'cr-aaaaaaaaaaaa')`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("%d of the three rows are left: %v", rows, err)
	}
	var values int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name IN ('applications', 'application_commands') AND column_name IN ('values', 'params', 'parameters')`).Scan(&values); err != nil || values != 0 {
		t.Fatalf("%d columns for the values or the parameters: %v", values, err)
	}
	app := db.NewID()
	exec("INSERT INTO applications (application_id, cluster_id, repository_id, credential_id, namespace, name) VALUES ($1, 'local', $2, 'cr-aaaaaaaaaaaa', 'web', 'shop')", app, repo)
	exec("INSERT INTO application_commands (application_id, position, name) VALUES ($1, 0, 'Scale up'), ($1, 1, 'Scale up')", app)
	if _, err := d.Pool().Exec(ctx, "INSERT INTO applications (application_id, cluster_id, namespace, name) VALUES ($1, 'local', 'web', 'shop')", db.NewID()); !db.IsUnique(err) {
		t.Errorf("a second application of one name: %v", err)
	}
	if _, err := d.Pool().Exec(ctx, "INSERT INTO applications (application_id, cluster_id, namespace, name) VALUES ($1, 'nowhere', 'web', 'shop')", db.NewID()); !db.IsForeignKey(err) {
		t.Errorf("an application of a cluster that has no row: %v", err)
	}
	exec("DELETE FROM repositories")
	exec("DELETE FROM credentials")
	var left int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM applications WHERE repository_id IS NULL AND credential_id IS NULL").Scan(&left); err != nil || left != 1 {
		t.Fatalf("the application after its repository and credential went: %d: %v", left, err)
	}
	exec("DELETE FROM clusters")
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM applications) + (SELECT count(*) FROM application_commands)").Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d rows left of a removed cluster's applications: %v", left, err)
	}
}
