package db_test

import (
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0046 on a database that holds a draft waiting for its request
// (DB-91): the draft's row gets the request's key from its number; a row
// whose number names no request of its kind gets none; the six keys that
// nothing filled, approval_decisions.approval_policy_id and
// approval_requests.expires_at are gone.
func TestStep0046LinksADraftToItsRequest(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 45); err != nil || n != 45 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	req, other := db.NewID(), db.NewID()
	for _, q := range []string{
		"INSERT INTO approval_requests (approval_request_id, request_number, kind, state) VALUES ('" + req + "', 7, 'prompt', 'waiting'), ('" + other + "', 8, 'environment', 'waiting')",
		"INSERT INTO agents (agent_id) VALUES ('sherlock'), ('auditor')",
		"INSERT INTO prompt_versions (prompt_version_id, agent_id, state, request_number) VALUES ('" + db.NewID() + "', 'sherlock', 'draft', 7), ('" + db.NewID() + "', 'auditor', 'draft', 8)",
	} {
		if _, err := d.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-45 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-45, err)
	}
	var sherlock, auditor *string
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT approval_request_id::text FROM prompt_versions WHERE agent_id = 'sherlock'),
		(SELECT approval_request_id::text FROM prompt_versions WHERE agent_id = 'auditor')`).Scan(&sherlock, &auditor); err != nil {
		t.Fatal(err)
	}
	if sherlock == nil || *sherlock != req || auditor != nil {
		t.Fatalf("the draft's request %v, the other's %v; want %s and none", sherlock, auditor, req)
	}
	var left int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE
		(column_name = 'approval_request_id' AND table_name IN ('script_versions', 'script_runs', 'variable_changes', 'changes', 'wiki_changes', 'toolbox_sessions'))
		OR (table_name = 'approval_decisions' AND column_name = 'approval_policy_id') OR (table_name = 'approval_requests' AND column_name = 'expires_at')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d of the columns dropped are there: %v", left, err)
	}
}
