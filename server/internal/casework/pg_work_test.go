package casework

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// A case's object in the database holds three keys at one version: every
// field of the work on the case and of its conversation comes back as a
// ConfigMap gives it; a save of one key moves the version for all; the
// conversation is joined to its case's row by the row's key (DB-57), so
// case 1's is never case 12's, and one written before the case itself
// makes the row the case then keeps; a deleted case takes all.
func TestTheWorkAndTheChatOfACaseInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	s := issues.NewCasesPG(d, CaseParts())
	ctx := context.Background()
	kept := func(v, out any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	same := func(what string, got []byte, out, want any) {
		t.Helper()
		if err := json.Unmarshal(got, out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reflect.ValueOf(out).Elem().Interface(), reflect.ValueOf(want).Elem().Interface()) {
			w, _ := json.Marshal(want)
			t.Fatalf("%s: the database reads back\n%s\nand a ConfigMap\n%s", what, got, w)
		}
	}
	var work Work
	dbtest.Fill(&work)
	work.Number = 1
	var chat chatRecord
	dbtest.Fill(&chat)
	rawWork, _ := json.Marshal(work)
	rawChat, _ := json.Marshal(chat)
	rawCase, _ := json.Marshal(issues.Case{Number: 1, Reporter: issues.Person{ID: "ann"}, Status: "new"})
	// The work comes first here, before the case itself: the object is made by any key.
	v, err := s.Save(ctx, "1", issues.WorkKey, rawWork, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, err = s.Save(ctx, "1", issues.CaseKey, rawCase, v); err != nil {
		t.Fatal(err)
	}
	stale := v
	if v, err = s.Save(ctx, "1", ChatKey, rawChat, v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, "1", issues.WorkKey, rawWork, stale); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save of the work over a version from before the chat's save: %v", err)
	}
	// Case 12's conversation, whose session starts as case 1's does, is
	// written before case 12 itself: the write makes the case's row, with
	// only the conversation kept of it so far.
	other := chatRecord{Cluster: "prod", Messages: []sherlock.Msg{{ID: 1, Role: "user", Text: "And here?", At: chat.Updated}}}
	rawOther, _ := json.Marshal(other)
	v12, err := s.Save(ctx, "12", ChatKey, rawOther, "")
	if err != nil {
		t.Fatal(err)
	}
	// Each conversation's row has its case's key and no change's.
	chatOf := func(name string) (caseID, chatID string, isKept bool, messages int) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, `SELECT k.case_id::text, c.chat_id::text, k.is_case_kept, (SELECT count(*) FROM chat_messages m WHERE m.chat_id = c.chat_id)
			FROM cases k JOIN chats c USING (case_id) WHERE k.object_name = $1 AND c.change_id IS NULL`, name).Scan(&caseID, &chatID, &isKept, &messages); err != nil {
			t.Fatalf("the conversation of case %s: %v", name, err)
		}
		return caseID, chatID, isKept, messages
	}
	case1, chat1, kept1, n1 := chatOf("1")
	case12, chat12, kept12, n12 := chatOf("12")
	if case1 == case12 || chat1 == chat12 || !kept1 || kept12 || n1 != len(chat.Messages) || n12 != 1 {
		t.Fatalf("case 1: row %s kept %v, conversation %s with %d messages; case 12: row %s kept %v, conversation %s with %d", case1, kept1, chat1, n1, case12, kept12, chat12, n12)
	}
	keys, got, err := s.Load(ctx, "1")
	if err != nil || got != v || len(keys) != 3 {
		t.Fatalf("load: %d keys at %q, want %q: %v", len(keys), got, v, err)
	}
	var wantWork, gotWork Work
	kept(work, &wantWork)
	same("the work", keys[issues.WorkKey], &gotWork, &wantWork)
	var wantChat, gotChat chatRecord
	kept(chat, &wantChat)
	same("the conversation", keys[ChatKey], &gotChat, &wantChat)
	keys, _, _ = s.Load(ctx, "12")
	var wantOther, gotOther chatRecord
	kept(other, &wantOther)
	if len(keys) != 1 {
		t.Fatalf("case 12 has %d keys", len(keys))
	}
	same("case 12's conversation", keys[ChatKey], &gotOther, &wantOther)
	// Case 12 itself comes: its row is the one its conversation made, and
	// the conversation is still its own.
	rawCase12, _ := json.Marshal(issues.Case{Number: 12, Reporter: issues.Person{ID: "ann"}, Status: "new"})
	if _, err := s.Save(ctx, "12", issues.CaseKey, rawCase12, v12); err != nil {
		t.Fatal(err)
	}
	if id, c, isKept, n := chatOf("12"); id != case12 || c != chat12 || !isKept || n != 1 {
		t.Fatalf("once case 12 is kept: row %s (was %s), conversation %s (was %s) with %d messages, kept %v", id, case12, c, chat12, n, isKept)
	}
	keys, _, _ = s.Load(ctx, "12")
	gotOther = chatRecord{}
	same("case 12's conversation, once the case is kept", keys[ChatKey], &gotOther, &wantOther)
	if keys, _, _ = s.Load(ctx, "1"); len(keys) != 3 {
		t.Fatalf("case 1 has %d keys", len(keys))
	}
	gotChat = chatRecord{}
	same("case 1's conversation, after case 12's was written", keys[ChatKey], &gotChat, &wantChat)
	// The work changes; the conversation is emptied, as when a case closes.
	work.Analysis, work.Drafts, work.Proposals = nil, nil, work.Proposals[:1]
	rawWork, _ = json.Marshal(work)
	if v, err = s.Save(ctx, "1", issues.WorkKey, rawWork, v); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "1", ChatKey, []byte("{}"), v); err != nil {
		t.Fatal(err)
	}
	keys, _, _ = s.Load(ctx, "1")
	if _, has := keys[ChatKey]; has || len(keys) != 2 {
		t.Fatalf("after the conversation was emptied: %d keys", len(keys))
	}
	wantWork, gotWork = Work{}, Work{}
	kept(work, &wantWork)
	same("the work, changed", keys[issues.WorkKey], &gotWork, &wantWork)
	if err := s.Delete(ctx, "12"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM cases) + (SELECT count(*) FROM case_work) + (SELECT count(*) FROM case_events) + (SELECT count(*) FROM case_drafts)
		+ (SELECT count(*) FROM case_commands) + (SELECT count(*) FROM rcas) + (SELECT count(*) FROM chats) + (SELECT count(*) FROM chat_messages)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows left of cases that are gone: %v", n, err)
	}
}
