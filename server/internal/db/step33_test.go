package db_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Step 0033 on a database that holds rows (DB-57): each kept conversation
// gets the key of the change or the case its session named, the name
// matched whole ("case:C-1" is not how case 12's is found); one whose
// change or case is not there goes with its messages; the session is
// dropped; and from then on a conversation is of exactly one record, each
// record has at most one, and it goes with its record's row.
func TestStep0033JoinsTheKeptConversationsByKey(t *testing.T) {
	d := dbtest.OpenConfig(t, dbtest.Config(t))
	ctx := ctxT(t)
	steps, _ := db.Migrations()
	if n, err := d.MigrateTo(ctx, 32); err != nil || n != 32 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	refused := func(what, sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s was taken", what)
		}
	}
	changes, cases := map[string]string{}, map[string]string{}
	for _, key := range []string{"PC-1", "PC-12"} {
		changes[key] = db.NewID()
		exec("INSERT INTO changes (change_id, change_key, created_at, updated_at) VALUES ($1, $2, now(), now())", changes[key], key)
	}
	// Case 12 has only its conversation kept so far; case 1 is whole.
	for name, kept := range map[string]bool{"1": true, "12": false} {
		cases[name] = db.NewID()
		exec("INSERT INTO cases (case_id, object_name, case_number, is_case_kept) VALUES ($1, $2, $3, $4)", cases[name], name, map[string]int{"1": 1, "12": 12}[name], kept)
	}
	sessions := []string{"proposal:PC-1", "proposal:PC-12", "case:C-1", "case:C-12", "proposal:PC-99", "case:C-7", "case:C-", "s-4f1a"}
	for _, session := range sessions {
		id := db.NewID()
		exec("INSERT INTO chats (chat_id, session, cluster_id, updated_at) VALUES ($1, $2, 'prod-1', now())", id, session)
		exec("INSERT INTO chat_messages (chat_id, position, role, body, at) VALUES ($1, 0, 'user', $2, now()), ($1, 1, 'sherlock', $2, now())", id, session)
	}
	if n, err := d.Migrate(ctx); err != nil || n != len(steps)-32 {
		t.Fatalf("the step: applied %d of %d: %v", n, len(steps)-32, err)
	}
	// Each conversation left is the one its session named, with its own
	// messages: the session is worked out from the key.
	rows, err := d.Pool().Query(ctx, `SELECT coalesce('proposal:' || g.change_key, 'case:C-' || k.case_number), coalesce(g.change_id, k.case_id)::text, c.cluster_id,
			(SELECT string_agg(m.body, ',' ORDER BY m.position) FROM chat_messages m WHERE m.chat_id = c.chat_id)
		FROM chats c LEFT JOIN changes g USING (change_id) LEFT JOIN cases k USING (case_id)`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var session, key, cluster, bodies string
	if _, err := pgx.ForEachRow(rows, []any{&session, &key, &cluster, &bodies}, func() error {
		want := changes[strings.TrimPrefix(session, "proposal:")] + cases[strings.TrimPrefix(session, "case:C-")]
		if key != want || cluster != "prod-1" || bodies != session+","+session {
			t.Errorf("%s: key %s, want %s; cluster %q; messages %q", session, key, want, cluster, bodies)
		}
		got = append(got, session)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != "case:C-1 case:C-12 proposal:PC-1 proposal:PC-12" {
		t.Fatalf("the conversations kept: %v", got)
	}
	var messages, column int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM chat_messages),
		(SELECT count(*) FROM information_schema.columns WHERE table_name = 'chats' AND column_name = 'session')`).Scan(&messages, &column); err != nil {
		t.Fatal(err)
	}
	if messages != 8 || column != 0 {
		t.Errorf("%d messages left, want 8; %d columns named session, want none", messages, column)
	}
	// A conversation is of one record, and a record has one conversation.
	spare := db.NewID()
	exec("INSERT INTO changes (change_id, change_key, created_at, updated_at) VALUES ($1, 'PC-13', now(), now())", spare)
	refused("a conversation of nothing", "INSERT INTO chats (chat_id) VALUES ($1)", db.NewID())
	refused("a conversation of a change and a case", "INSERT INTO chats (chat_id, change_id, case_id) VALUES ($1, $2, $3)", db.NewID(), spare, cases["1"])
	refused("a second conversation of a change", "INSERT INTO chats (chat_id, change_id) VALUES ($1, $2)", db.NewID(), changes["PC-1"])
	refused("a second conversation of a case", "INSERT INTO chats (chat_id, case_id) VALUES ($1, $2)", db.NewID(), cases["12"])
	refused("a conversation of a change that is not there", "INSERT INTO chats (chat_id, change_id) VALUES ($1, $2)", db.NewID(), db.NewID())
	// It goes with its record's row.
	exec("DELETE FROM changes WHERE change_key = 'PC-12'")
	exec("DELETE FROM cases WHERE object_name = '1'")
	var chats int
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM chats), (SELECT count(*) FROM chat_messages)").Scan(&chats, &messages); err != nil {
		t.Fatal(err)
	}
	if chats != 2 || messages != 4 {
		t.Errorf("after a change and a case were deleted: %d conversations with %d messages, want 2 with 4", chats, messages)
	}
}
