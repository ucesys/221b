package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0048 on a database where a race made rows twice before they were
// declared unique (DB-93): the step does not fail; a second fleet of one
// key and a second host of one name keep their rows under a name of
// their own, the listed fleet keeping the key; a second acknowledgement
// of one alert goes, the newest kept; a second request of one number is
// numbered after the greatest and the counter moves past it; and the
// pairs are unique from then on.
func TestStep0048DeclaresUniqueWhatTheCodeLooksUp(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 47); err != nil || n != 47 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	const listed, unlisted, older, newer = "00000000-0000-7000-8000-0000000000f2", "00000000-0000-7000-8000-0000000000f1", "00000000-0000-7000-8000-0000000000a1", "00000000-0000-7000-8000-0000000000a2"
	for _, q := range []string{
		"INSERT INTO clusters (cluster_id) VALUES ('c1')",
		"INSERT INTO fleets (fleet_id, cluster_id, fleet_key, kind) VALUES ('" + unlisted + "', 'c1', 'gpu', 'unlisted'), ('" + listed + "', 'c1', 'gpu', 'selector'), ('" + db.NewID() + "', NULL, 'gpu', 'list')",
		"INSERT INTO hosts (host_id, cluster_id, name, created_at) VALUES ('" + older + "', 'c1', 'node-1', '2026-11-01'), ('" + newer + "', 'c1', 'node-1', '2026-11-02')",
		"INSERT INTO alert_acks (alert_ack_id, cluster_id, alert_key, reason, acked_at) VALUES ('" + db.NewID() + "', 'c1', 'KubeDown', 'old', '2026-11-01'), ('" + db.NewID() + "', 'c1', 'KubeDown', 'new', '2026-11-02')",
		"INSERT INTO approval_requests (approval_request_id, request_number, kind, created_at) VALUES ('" + older + "', 3, 'environment', '2026-11-01'), ('" + newer + "', 3, 'superadmin', '2026-11-02'), ('" + db.NewID() + "', 5, 'prompt', '2026-11-03')",
		`INSERT INTO settings (scope, value, version) VALUES ('approvals', '{"next": 5}', 1)`,
	} {
		if _, err := d.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-47 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-47, err)
	}
	var gpu, other, node, second, reason, renumbered string
	var fleets, acks, next int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT fleet_id::text FROM fleets WHERE cluster_id = 'c1' AND fleet_key = 'gpu'),
		(SELECT fleet_key FROM fleets WHERE fleet_id = $1), (SELECT count(*) FROM fleets),
		(SELECT host_id::text FROM hosts WHERE name = 'node-1'), (SELECT name FROM hosts WHERE host_id = $2),
		(SELECT count(*) FROM alert_acks), (SELECT reason FROM alert_acks),
		(SELECT request_number::text FROM approval_requests WHERE approval_request_id = $2), (SELECT (value->>'next')::integer FROM settings WHERE scope = 'approvals')`,
		unlisted, newer).Scan(&gpu, &other, &fleets, &node, &second, &acks, &reason, &renumbered, &next); err != nil {
		t.Fatal(err)
	}
	if gpu != listed || other != "gpu~"+unlisted[:8] || fleets != 3 {
		t.Errorf("fleets: gpu is %s (want the listed %s), the other is %q, %d rows", gpu, listed, other, fleets)
	}
	if node != older || second != "node-1~"+newer[:8] {
		t.Errorf("hosts: node-1 is %s (want the older %s), the other is %q", node, older, second)
	}
	if acks != 1 || reason != "new" {
		t.Errorf("acknowledgements: %d, kept %q", acks, reason)
	}
	if renumbered != "6" || next != 6 {
		t.Errorf("the second request 3 is %s, the counter %d; want 6 and 6", renumbered, next)
	}
	for _, q := range []string{
		"INSERT INTO fleets (fleet_id, cluster_id, fleet_key) VALUES ('" + db.NewID() + "', 'c1', 'gpu')",
		"INSERT INTO fleets (fleet_id, cluster_id, fleet_key) VALUES ('" + db.NewID() + "', NULL, 'gpu')",
		"INSERT INTO hosts (host_id, cluster_id, name) VALUES ('" + db.NewID() + "', 'c1', 'node-1')",
		"INSERT INTO alert_acks (alert_ack_id, cluster_id, alert_key) VALUES ('" + db.NewID() + "', 'c1', 'KubeDown')",
		"INSERT INTO approval_requests (approval_request_id, request_number) VALUES ('" + db.NewID() + "', 5)",
	} {
		if _, err := d.Pool().Exec(ctx, q); !db.IsUnique(err) {
			t.Errorf("%s: %v, want a unique violation", q, err)
		}
	}
}

// Step 0049 renames what misled and changes two types (DB-93): a value
// stored before is read under its new name, a cost is rounded to a
// millionth of a dollar, and a source with no pinned certificate reads ”.
func TestStep0049RenamesAndRetypes(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	if n, err := d.MigrateTo(ctx, 48); err != nil || n != 48 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	for _, q := range []string{
		"INSERT INTO clusters (cluster_id) VALUES ('c1')",
		"INSERT INTO alert_acks (alert_ack_id, cluster_id, alert_key, job_run_id, case_id, exception_id) VALUES ('" + db.NewID() + "', 'c1', 'KubeDown', 'run-1', 'C-2', 'E-3')",
		"INSERT INTO jobs (job_id, schedule_at, skips_overlap, catches_up) VALUES ('audit', '02:00', true, true)",
		"INSERT INTO knowledge_sources (knowledge_source_id, name) VALUES ('src-1', 'wiki')",
	} {
		if _, err := d.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var run, cs, exc, at, pin string
	var skips, catches bool
	if err := d.Pool().QueryRow(ctx, `SELECT a.job_run_key, a.case_key, a.exception_key, j.schedule_time_of_day, j.is_overlap_skipped, j.is_catching_up, s.pinned_cert_sha256
		FROM alert_acks a, jobs j, knowledge_sources s`).Scan(&run, &cs, &exc, &at, &skips, &catches, &pin); err != nil {
		t.Fatal(err)
	}
	if run != "run-1" || cs != "C-2" || exc != "E-3" || at != "02:00" || !skips || !catches || pin != "" {
		t.Fatalf("read back: %s %s %s %s %v %v %q", run, cs, exc, at, skips, catches, pin)
	}
	var kind string
	if err := d.Pool().QueryRow(ctx, "SELECT data_type FROM information_schema.columns WHERE table_name = 'chat_messages' AND column_name = 'cost_usd'").Scan(&kind); err != nil || kind != "numeric" {
		t.Fatalf("chat_messages.cost_usd is %s: %v", kind, err)
	}
}
