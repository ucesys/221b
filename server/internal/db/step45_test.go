package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0045 on a database that holds findings, changes, exceptions and
// cases that name each other by the ids people read (DB-90): each gets the
// key of what it names where that has a row, and none where it has not;
// the ids stay. Duplicates of what is now unique do not stop the step: a
// finding's id repeated is marked, a case's number repeated is 0 but on
// the case its object is named by. The names that misled are gone.
func TestStep0045KeysWhatTheIDsName(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	if n, err := d.MigrateTo(ctx, 44); err != nil || n != 44 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	for _, q := range []string{
		`INSERT INTO cases (case_id, object_name, case_number, is_case_kept, service_key) VALUES
			('00000000-0000-7000-8000-000000000007', '7', 7, true, 'shop'), ('00000000-0000-7000-8000-000000000008', '8', 8, true, 'gone'),
			('00000000-0000-7000-8000-000000000009', '07', 7, true, '')`,
		`INSERT INTO services (service_id, created_at, updated_at) VALUES ('shop', now(), now())`,
		`INSERT INTO known_issues (known_issue_id, known_issue_key, created_at, updated_at) VALUES ('00000000-0000-7000-8000-0000000000aa', 'KI-1', now(), now())`,
		`INSERT INTO exceptions (exception_id, exception_key, created_at, case_key) VALUES ('00000000-0000-7000-8000-0000000000ee', 'EX-1', now(), 'C-7')`,
		`INSERT INTO job_runs (job_run_id, job_id) VALUES ('00000000-0000-7000-8000-0000000000f0', 'audit')`,
		`INSERT INTO findings (finding_id, job_run_id, finding_key, case_key, known_issue_key, exception_key) VALUES
			('00000000-0000-7000-8000-000000000101', '00000000-0000-7000-8000-0000000000f0', 'r.1', 'C-7', 'KI-1', 'EX-1'),
			('00000000-0000-7000-8000-000000000102', '00000000-0000-7000-8000-0000000000f0', 'r.1', 'C-99', 'KI-9', ''),
			('00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-0000000000f0', '', '', '', ''),
			('00000000-0000-7000-8000-000000000104', '00000000-0000-7000-8000-0000000000f0', '', '', '', '')`,
		`INSERT INTO changes (change_id, change_key, created_at, updated_at) VALUES ('00000000-0000-7000-8000-0000000000c0', 'PC-1', now(), now())`,
		`INSERT INTO change_resolves (change_id, position, resolved_key) VALUES ('00000000-0000-7000-8000-0000000000c0', 0, 'C-8'), ('00000000-0000-7000-8000-0000000000c0', 1, 'KI-1'),
			('00000000-0000-7000-8000-0000000000c0', 2, 'C-3')`,
		`INSERT INTO case_work (case_id, case_number, updated_at) VALUES ('00000000-0000-7000-8000-000000000007', 7, now())`,
	} {
		if _, err := d.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := d.MigrateTo(ctx, 45); err != nil || n != 1 {
		t.Fatalf("the step: applied %d: %v", n, err)
	}
	var findings, resolves, exception, services, numbers string
	for _, c := range []struct {
		q    string
		v    *string
		want string
	}{
		{`SELECT string_agg(finding_key || ':' || coalesce(right(case_id::text, 1), '-') || coalesce(right(known_issue_id::text, 1), '-') || coalesce(right(exception_id::text, 1), '-'), ' ' ORDER BY finding_id)
			FROM findings`, &findings, "r.1:7ae r.1~2:--- :--- :---"},
		{`SELECT string_agg(resolved_key || ':' || coalesce(right(resolved_case_id::text, 1), '-') || coalesce(right(resolved_known_issue_id::text, 1), '-'), ' ' ORDER BY position)
			FROM change_resolves`, &resolves, "C-8:8- KI-1:-a C-3:--"},
		{`SELECT string_agg(case_key || ':' || coalesce(right(case_id::text, 1), '-'), ' ') FROM exceptions`, &exception, "C-7:7"},
		{`SELECT string_agg(service_key || ':' || coalesce(service_id, '-'), ' ' ORDER BY object_name) FROM cases`, &services, ":- shop:shop gone:-"},
		{`SELECT string_agg(object_name || ':' || case_number, ' ' ORDER BY object_name) FROM cases`, &numbers, "07:0 7:7 8:8"},
	} {
		if err := d.Pool().QueryRow(ctx, c.q).Scan(c.v); err != nil || *c.v != c.want {
			t.Errorf("%q, want %q: %v", *c.v, c.want, err)
		}
	}
	// What is named goes: the key is emptied, the id stays.
	if _, err := d.Pool().Exec(ctx, "DELETE FROM cases WHERE object_name = '7'"); err != nil {
		t.Fatal(err)
	}
	var keyed int
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM findings WHERE case_id IS NOT NULL) + (SELECT count(*) FROM exceptions WHERE case_id IS NOT NULL)").Scan(&keyed); err != nil || keyed != 0 {
		t.Fatalf("%d keys to a case that is gone: %v", keyed, err)
	}
	// Unique now: a second finding with an id, a second case with a number.
	if _, err := d.Pool().Exec(ctx, `INSERT INTO findings (finding_id, job_run_id, finding_key) VALUES ('00000000-0000-7000-8000-000000000105', '00000000-0000-7000-8000-0000000000f0', 'r.1')`); err == nil {
		t.Fatal("a finding's id was taken twice")
	}
	if _, err := d.Pool().Exec(ctx, `INSERT INTO cases (case_id, object_name, case_number) VALUES ('00000000-0000-7000-8000-000000000010', '008', 8)`); err == nil {
		t.Fatal("a case's number was taken twice")
	}
	for _, c := range []struct{ table, column string }{{"test_case_results", "case_key"}, {"test_run_cases", "case_key"}, {"case_work", "case_number"}, {"test_case_results", "test_case_id"}} {
		var has bool
		if err := d.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)", c.table, c.column).Scan(&has); err != nil ||
			has != (c.column == "test_case_id") {
			t.Errorf("%s.%s there: %v, %v", c.table, c.column, has, err)
		}
	}
}
