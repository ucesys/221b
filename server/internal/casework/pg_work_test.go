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
// conversation is read by its exact session; a deleted case takes all.
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
	// Case 12's conversation starts as case 1's session does.
	other := chatRecord{Cluster: "prod", Messages: []sherlock.Msg{{ID: 1, Role: "user", Text: "And here?", At: chat.Updated}}}
	rawOther, _ := json.Marshal(other)
	if _, err := s.Save(ctx, "12", ChatKey, rawOther, ""); err != nil {
		t.Fatal(err)
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
