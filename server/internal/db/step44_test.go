package db_test

import (
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0044 on a database that holds cases (DB-89): a case's links, an
// RCA's cases and a known issue's services become rows, each keyed to what
// it names where that has a row and empty where it does not, the number or
// the id kept beside; the columns they were are gone.
func TestStep0044MakesTheLinksRows(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	if n, err := d.MigrateTo(ctx, 43); err != nil || n != 43 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	for _, q := range []string{
		`INSERT INTO cases (case_id, object_name, case_number, is_case_kept) VALUES
			('00000000-0000-7000-8000-000000000001', '1', 1, true), ('00000000-0000-7000-8000-000000000002', '2', 2, true)`,
		`UPDATE cases SET duplicate_of_number = 1, regression_of_number = 9, similar_numbers = '{1,9,2}' WHERE case_number = 2`,
		`INSERT INTO rcas (case_id, case_key, cases, written_at) VALUES ('00000000-0000-7000-8000-000000000002', 'C-2', '{C-1,C-9,odd}', now())`,
		`INSERT INTO services (service_id, created_at, updated_at) VALUES ('shop', now(), now())`,
		`INSERT INTO known_issues (known_issue_id, known_issue_key, services, created_at, updated_at) VALUES ('00000000-0000-7000-8000-0000000000aa', 'KI-1', '{gone,shop}', now(), now())`,
	} {
		if _, err := d.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := d.MigrateTo(ctx, 44); err != nil || n != 1 {
		t.Fatalf("the step: applied %d: %v", n, err)
	}
	var links string
	if err := d.Pool().QueryRow(ctx, `SELECT string_agg(kind || ':' || position || ':' || linked_case_number || ':' || coalesce(right(linked_case_id::text, 1), '-'), ' ' ORDER BY kind, position)
		FROM case_links`).Scan(&links); err != nil || links != "duplicate_of:0:1:1 regression_of:0:9:- similar:0:1:1 similar:1:9:- similar:2:2:2" {
		t.Fatalf("the case's links: %q, %v", links, err)
	}
	var rcas string
	if err := d.Pool().QueryRow(ctx, `SELECT string_agg(linked_case_key || ':' || coalesce(right(linked_case_id::text, 1), '-'), ' ' ORDER BY position) FROM rca_cases`).Scan(&rcas); err != nil ||
		rcas != "C-1:1 C-9:- odd:-" {
		t.Fatalf("the RCA's cases: %q, %v", rcas, err)
	}
	var services string
	if err := d.Pool().QueryRow(ctx, `SELECT string_agg(service_key || ':' || coalesce(service_id, '-'), ' ' ORDER BY position) FROM known_issue_services`).Scan(&services); err != nil ||
		services != "gone:- shop:shop" {
		t.Fatalf("the known issue's services: %q, %v", services, err)
	}
	// What each names going empties the key and keeps the number or the id.
	if _, err := d.Pool().Exec(ctx, "DELETE FROM cases WHERE case_number = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, "DELETE FROM services"); err != nil {
		t.Fatal(err)
	}
	var keyed, kept int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM case_links WHERE linked_case_id IS NOT NULL) + (SELECT count(*) FROM rca_cases WHERE linked_case_id IS NOT NULL)
			+ (SELECT count(*) FROM known_issue_services WHERE service_id IS NOT NULL),
		(SELECT count(*) FROM case_links) + (SELECT count(*) FROM rca_cases) + (SELECT count(*) FROM known_issue_services)`).Scan(&keyed, &kept); err != nil || keyed != 1 || kept != 10 {
		t.Fatalf("%d keyed, %d kept, want 1 (C-2's own similar) and 10: %v", keyed, kept, err)
	}
	for _, c := range []string{"cases.similar_numbers", "cases.duplicate_of_number", "rcas.cases", "known_issues.services"} {
		var has bool
		table, column, _ := strings.Cut(c, ".")
		if err := d.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)", table, column).Scan(&has); err != nil || has {
			t.Fatalf("%s is there: %v", c, err)
		}
	}
}
