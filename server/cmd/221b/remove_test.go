package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
)

// keysOnly is a store that cannot delete a key.
type keysOnly struct{ m *kv.Memory }

func (k keysOnly) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	return k.m.LoadKey(ctx, key)
}

func (k keysOnly) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	return k.m.SaveKey(ctx, key, b, version)
}

const oneFix = `{"next":2,"items":[{"id":"FX-1","title":"Raise the limit","status":"active","cases":[{"case":"C-1","at":"2026-11-07T02:00:00Z"}],"created":"2026-11-07T02:00:00Z","updated":"2026-11-07T02:00:00Z","createdBy":"bob","updatedBy":"bob"}]}`

func fixRows(t *testing.T, d *db.DB) (n int) {
	t.Helper()
	if err := d.Pool().QueryRow(t.Context(), "SELECT count(*) FROM fixes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func fixesIn(d *db.DB) map[string]movedDoc { return map[string]movedDoc{fixes.Key: fixes.NewPG(d)} }

// A record set leaves its ConfigMap once its copy is recorded (DB-55):
// the key is deleted, or emptied in a store that cannot delete; a copy
// that fails leaves the ConfigMap as it was; what an earlier build copied
// and left is removed at the next start, and the database's records are
// not touched by it; another key of the ConfigMap stays.
func TestACopiedRecordSetLeavesItsConfigMap(t *testing.T) {
	ctx := t.Context()
	// Copied and deleted.
	d := dbtest.Open(t)
	w := &wiring{log: slog.Default(), database: d}
	cm := &persistent{}
	_, _ = cm.SaveKey(ctx, fixes.Key, []byte(oneFix), "")
	_, _ = cm.SaveKey(ctx, "other.json", []byte(`{"kept":true}`), "")
	w.inTables(ctx, cm, fixesIn)
	if _, _, err := cm.LoadKey(ctx, fixes.Key); !errors.Is(err, kv.ErrNotFound) || fixRows(t, d) != 1 {
		t.Fatalf("after the copy the ConfigMap's key: %v; %d fixes in the database", err, fixRows(t, d))
	}
	if b, _, err := cm.LoadKey(ctx, "other.json"); err != nil || string(b) != `{"kept":true}` {
		t.Fatalf("another key: %s %v", b, err)
	}
	// A store that cannot delete a key has it emptied, and is left alone after.
	d2 := dbtest.Open(t)
	w2 := &wiring{log: slog.Default(), database: d2}
	plain := keysOnly{&kv.Memory{}}
	_, _ = plain.SaveKey(ctx, fixes.Key, []byte(oneFix), "")
	w2.inTables(ctx, plain, fixesIn)
	b, version, err := plain.LoadKey(ctx, fixes.Key)
	if err != nil || string(b) != "{}" || fixRows(t, d2) != 1 {
		t.Fatalf("a store that cannot delete holds %s: %v; %d fixes", b, err, fixRows(t, d2))
	}
	w2.inTables(ctx, plain, fixesIn)
	if _, again, _ := plain.LoadKey(ctx, fixes.Key); again != version || fixRows(t, d2) != 1 {
		t.Fatal("an emptied key was written again, or the fixes changed")
	}
	// A copy that fails leaves the ConfigMap as it was, and records nothing.
	d3 := dbtest.Open(t)
	w3 := &wiring{log: slog.Default(), database: d3}
	bad := &persistent{}
	_, _ = bad.SaveKey(ctx, "refused.json", []byte(`{"n":1}`), "")
	w3.inTables(ctx, bad, func(d *db.DB) map[string]movedDoc {
		return map[string]movedDoc{"refused.json": &db.Doc[map[string]int]{DB: d, Key: "refused.json",
			Read: func(context.Context, db.Querier) (map[string]int, bool, error) { return nil, false, nil },
			Write: func(context.Context, pgx.Tx, map[string]int, map[string]int) error {
				return errors.New("the rows cannot be written")
			}}}
	})
	if got, _, err := bad.LoadKey(ctx, "refused.json"); err != nil || string(got) != `{"n":1}` {
		t.Fatalf("after a copy that failed the ConfigMap holds %s: %v", got, err)
	}
	if copied, _ := d3.Imported(ctx, "refused.json"); copied {
		t.Fatal("a copy that failed was recorded")
	}
	// What an earlier build copied and left is removed at the next start;
	// the database's records, changed since, are as they were.
	d4 := dbtest.Open(t)
	left := &persistent{}
	_, _ = left.SaveKey(ctx, fixes.Key, []byte(oneFix), "")
	if copied, err := fixes.NewPG(d4).Import(ctx, left, "configmap"); err != nil || !copied {
		t.Fatalf("the earlier build's copy: %v %v", copied, err)
	}
	if _, err := d4.Pool().Exec(ctx, "UPDATE fixes SET title = 'Changed since'"); err != nil {
		t.Fatal(err)
	}
	(&wiring{log: slog.Default(), database: d4}).inTables(ctx, left, fixesIn)
	var title string
	if err := d4.Pool().QueryRow(ctx, "SELECT title FROM fixes").Scan(&title); err != nil || title != "Changed since" {
		t.Fatalf("the database's fix reads %q: %v", title, err)
	}
	if _, _, err := left.LoadKey(ctx, fixes.Key); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("what the earlier build left is still there: %v", err)
	}
}

const (
	oneCard = `{"next":2,"cards":[{"id":"PC-1","agent":"troubleshooter","source":{"kind":"alert"},"cluster":"dev-1","title":"Restart shop/web","actions":[{"kind":"kubectl","args":{"command":"kubectl get ns"}}],"condition":{"kind":"none"},"state":"queued","created":"2026-11-05T03:12:00Z","updated":"2026-11-05T03:12:00Z","needed":1,"plans":[{"summary":"kubectl get ns"}]}]}`
	oneChat = `{"chats":{"PC-1":{"cluster":"dev-1","messages":[{"id":1,"role":"user","text":"Why restart?","at":"2026-11-05T03:13:00Z"},{"id":2,"role":"sherlock","text":"It cannot sign in.","status":"done","at":"2026-11-05T03:13:01Z"}],"updated":"2026-11-05T03:13:01Z"}}}`
)

// uncopied is a document whose copy fails.
type uncopied struct{ movedDoc }

func (uncopied) Import(context.Context, kv.Backend, string) (bool, error) {
	return false, errors.New("the rows cannot be written")
}

// The proposed changes are copied into the database before their
// conversations with Sherlock, whose rows hang from the changes' (DB-57):
// both are then kept and both leave the ConfigMap. When the changes' copy
// fails, the conversations are not copied to rows that would not be kept:
// they stay in the ConfigMap for the next start.
func TestTheChangesAreCopiedBeforeTheirConversations(t *testing.T) {
	ctx := t.Context()
	rows := func(d *db.DB) (chats, messages int) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM chats c JOIN changes g USING (change_id) WHERE g.change_key = 'PC-1'), (SELECT count(*) FROM chat_messages)`).
			Scan(&chats, &messages); err != nil {
			t.Fatal(err)
		}
		return chats, messages
	}
	cardsIn := func(d *db.DB) map[string]movedDoc {
		return map[string]movedDoc{cards.ChatsKey: cards.NewChatsPG(d), cards.CardsKey: cards.NewCardsPG(d), cards.StandingKey: cards.NewStandingPG(d), cards.NotifyKey: cards.NewNotifyPG(d)}
	}
	if order := copyOrder(cardsIn(nil)); slices.Index(order, cards.CardsKey) > slices.Index(order, cards.ChatsKey) || len(order) != 4 {
		t.Fatalf("copied in the order %v", order)
	}
	d := dbtest.Open(t)
	cm := &persistent{}
	_, _ = cm.SaveKey(ctx, cards.CardsKey, []byte(oneCard), "")
	_, _ = cm.SaveKey(ctx, cards.ChatsKey, []byte(oneChat), "")
	(&wiring{log: slog.Default(), database: d}).inTables(ctx, cm, cardsIn)
	if chats, messages := rows(d); chats != 1 || messages != 2 {
		t.Fatalf("copied %d conversations of PC-1 with %d messages", chats, messages)
	}
	for _, key := range []string{cards.CardsKey, cards.ChatsKey} {
		if _, _, err := cm.LoadKey(ctx, key); !errors.Is(err, kv.ErrNotFound) {
			t.Errorf("%s is still in the ConfigMap: %v", key, err)
		}
	}
	// The changes' copy fails: their conversations wait in the ConfigMap.
	d2 := dbtest.Open(t)
	cm2 := &persistent{}
	_, _ = cm2.SaveKey(ctx, cards.CardsKey, []byte(oneCard), "")
	_, _ = cm2.SaveKey(ctx, cards.ChatsKey, []byte(oneChat), "")
	w2 := &wiring{log: slog.Default(), database: d2}
	w2.inTables(ctx, cm2, func(d *db.DB) map[string]movedDoc {
		docs := cardsIn(d)
		docs[cards.CardsKey] = uncopied{docs[cards.CardsKey]}
		return docs
	})
	if got, _, err := cm2.LoadKey(ctx, cards.ChatsKey); err != nil || string(got) != oneChat {
		t.Fatalf("after the changes' copy failed the conversations' key holds %s: %v", got, err)
	}
	if copied, _ := d2.Imported(ctx, cards.ChatsKey); copied {
		t.Fatal("the conversations' copy was recorded without the changes'")
	}
	// The next start copies both.
	(&wiring{log: slog.Default(), database: d2}).inTables(ctx, cm2, cardsIn)
	if chats, messages := rows(d2); chats != 1 || messages != 2 {
		t.Fatalf("at the next start: %d conversations of PC-1 with %d messages", chats, messages)
	}
}

// The cases leave their ConfigMaps once copied, each ConfigMap whole, and
// the issue service's three documents their keys; with no record of a
// copy, they stay.
func TestTheCasesLeaveTheirConfigMapsOnceCopied(t *testing.T) {
	ctx := t.Context()
	objs := &kv.MemoryObjects{}
	_, _ = objs.Save(ctx, "1", issues.CaseKey, []byte(`{"number":1,"reporter":{"id":"ann"},"status":"new","created":"2026-11-07T02:00:00Z","updated":"2026-11-07T02:00:00Z","statusAt":"2026-11-07T02:00:00Z","priority":3,"lastReporter":"2026-11-07T02:00:00Z"}`), "")
	cm := &persistent{}
	_, _ = cm.SaveKey(ctx, issues.IndexKey, []byte(`{"next":2,"cases":[{"number":1,"status":"new","priority":3}]}`), "")
	_, _ = cm.SaveKey(ctx, issues.KnownKey, []byte(`{"next":1,"known":[]}`), "")
	_, _ = cm.SaveKey(ctx, "issues-reports.json", []byte(`{"next":1}`), "")
	d := dbtest.Open(t)
	w := &wiring{log: slog.Default(), database: d}
	// No record of a copy: nothing is removed.
	w.removeCopiedObjects(ctx, objs, issues.CasesImportID)
	w.removeCopied(ctx, cm, issues.NewSetPG(d).ImportID(), issues.IndexKey, issues.KnownKey)
	if names, _ := objs.List(ctx); len(names) != 1 {
		t.Fatal("a case's ConfigMap was removed with no copy recorded")
	}
	if _, _, err := cm.LoadKey(ctx, issues.IndexKey); err != nil {
		t.Fatalf("the index was removed with no copy recorded: %v", err)
	}
	// The copy, as the wiring makes it.
	b, cases := w.casesInDatabase(ctx)(cm, kv.Objects(wrapped{objs}))
	if names, _ := objs.List(ctx); len(names) != 0 {
		t.Fatalf("the cases' ConfigMaps left after the copy: %v", names)
	}
	for _, key := range []string{issues.IndexKey, issues.KnownKey} {
		if _, _, err := cm.LoadKey(ctx, key); !errors.Is(err, kv.ErrNotFound) {
			t.Fatalf("%s is still in the ConfigMap: %v", key, err)
		}
	}
	if _, _, err := cm.LoadKey(ctx, "issues-reports.json"); err != nil {
		t.Fatalf("a key that is not copied was removed: %v", err)
	}
	if keys, _, err := cases.Load(ctx, "1"); err != nil || len(keys[issues.CaseKey]) == 0 {
		t.Fatalf("the case in the database: %v", err)
	}
	// The index's counter is copied, and its line is the case's own.
	if got, _, err := b.LoadKey(ctx, issues.IndexKey); err != nil || !strings.Contains(string(got), `"next":2`) || !strings.Contains(string(got), `"number":1`) {
		t.Fatalf("the index in the database: %s %v", got, err)
	}
	_ = casework.ChatKey
}

// wrapped is a store of objects that is not the in-memory one, as the
// cases' ConfigMaps are not.
type wrapped struct{ kv.Objects }
