package db_test

import (
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0039 on a database that holds jobs (DB-80): each time a job handled
// is a claim of schedule_claims, kept beside the claims there were; a row
// of jobs that held only that time goes, and one that holds more stays;
// jobs.handled_at is gone.
func TestStep0039MakesTheTimesHandledClaims(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 38); err != nil || n != 38 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	day := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	for _, q := range []string{"INSERT INTO jobs (job_id, is_kept, handled_at) VALUES ('backup', false, $1), ('audit', true, $2), ('report', false, $1)",
		"UPDATE jobs SET spend_day = '2026-11-02' WHERE job_id = 'report' AND $1::timestamptz IS NOT NULL AND $2::timestamptz IS NOT NULL",
		"INSERT INTO schedule_claims (job_id, slot_at) VALUES ('audit', $1), ('audit', $2)"} {
		if _, err := d.Pool().Exec(ctx, q, day, day.Add(24*time.Hour)); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-38 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-38, err)
	}
	var jobs, claims, backup int
	var newest time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs), (SELECT count(*) FROM schedule_claims),
		(SELECT count(*) FROM schedule_claims WHERE job_id = 'backup' AND slot_at = $1), (SELECT max(slot_at) FROM schedule_claims WHERE job_id = 'audit')`, day).Scan(&jobs, &claims, &backup, &newest); err != nil ||
		jobs != 2 || claims != 4 || backup != 1 || !newest.Equal(day.Add(24*time.Hour)) {
		t.Fatalf("%d jobs, %d claims, backup's %d, audit's newest %v: %v", jobs, claims, backup, newest, err)
	}
	var has bool
	if err := d.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'handled_at')").Scan(&has); err != nil || has {
		t.Fatalf("jobs.handled_at is there: %v %v", has, err)
	}
}
