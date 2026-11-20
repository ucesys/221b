package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0036 on a database that holds rows (DB-64): the rows clusters
// holds with only an id stay, not kept, and the acknowledged alerts, the
// applications, the fleets and the hosts that point at them still do. The
// table has a column for every field of a connection and none for its
// credential.
func TestStep0036KeepsTheClustersWithOnlyAnID(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 35); err != nil || n != 35 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("INSERT INTO clusters (cluster_id) VALUES ('local'), ('staging')")
	exec("INSERT INTO clusters (cluster_id, name, secret_ref) VALUES ('named', 'Named', 'a-secret')")
	exec("INSERT INTO alert_acks (alert_ack_id, cluster_id, alert_key) VALUES ($1, 'staging', 'KubePodCrashLooping')", db.NewID())
	exec("INSERT INTO applications (application_id, cluster_id, namespace, name) VALUES ($1, 'staging', 'web', 'shop')", db.NewID())
	exec("INSERT INTO fleets (fleet_id, cluster_id, fleet_key, name) VALUES ($1, 'staging', 'workers', 'Workers')", db.NewID())
	exec("INSERT INTO hosts (host_id, cluster_id, name) VALUES ($1, 'staging', 'node-1')", db.NewID())
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-35 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-35, err)
	}
	var rows, kept, versions int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE is_kept), coalesce(sum(version), 0) FROM clusters").Scan(&rows, &kept, &versions); err != nil || rows != 3 || kept != 0 || versions != 0 {
		t.Fatalf("%d rows, %d of them kept, versions %d: %v", rows, kept, versions, err)
	}
	var pointing int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM alert_acks WHERE cluster_id = 'staging') + (SELECT count(*) FROM applications WHERE cluster_id = 'staging')
		+ (SELECT count(*) FROM fleets WHERE cluster_id = 'staging') + (SELECT count(*) FROM hosts WHERE cluster_id = 'staging')`).Scan(&pointing); err != nil || pointing != 4 {
		t.Fatalf("%d of the four rows still point at the cluster: %v", pointing, err)
	}
	// Every field of a connection has a column, and nothing of a credential has.
	columns := map[string]bool{}
	found, err := d.Pool().Query(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'clusters'")
	if err != nil {
		t.Fatal(err)
	}
	for found.Next() {
		var name string
		if err := found.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	found.Close()
	for _, want := range []string{"cluster_id", "name", "environment", "description", "server_url", "ca_mode", "ca_info", "auth_method", "mode", "is_local", "is_kept",
		"cred_who", "cred_context", "cred_cluster", "cred_issuer", "cred_client_id", "cred_expires_at", "cred_set_at", "created_at", "updated_at", "version",
		// The last test, kept with the connection since step 0041.
		"tested_at", "tested_by", "test_tone", "test_summary", "test_error", "test_version", "test_nodes", "is_test_nodes_more", "test_who", "is_test_impersonate"} {
		if !columns[want] {
			t.Errorf("clusters has no column %s", want)
		}
		delete(columns, want)
	}
	if len(columns) != 0 {
		t.Errorf("clusters has columns no field of a connection is kept in: %v", columns)
	}
	// The keys still hold: a row something points at cannot be made to
	// point nowhere, and removing a cluster's row by hand still takes its
	// applications and alerts with it and empties its fleets' and hosts'.
	if _, err := d.Pool().Exec(ctx, "INSERT INTO applications (application_id, cluster_id, namespace, name) VALUES ($1, 'nowhere', 'web', 'shop')", db.NewID()); !db.IsForeignKey(err) {
		t.Errorf("an application of a cluster that has no row: %v", err)
	}
	exec("DELETE FROM clusters WHERE cluster_id = 'staging'")
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM alert_acks) + (SELECT count(*) FROM applications)
		+ (SELECT count(*) FROM fleets WHERE cluster_id IS NOT NULL) + (SELECT count(*) FROM hosts WHERE cluster_id IS NOT NULL)`).Scan(&pointing); err != nil || pointing != 0 {
		t.Fatalf("%d rows still name a cluster whose row is gone: %v", pointing, err)
	}
}
