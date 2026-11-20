package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
)

// oldConfigMap is a ConfigMap of before in a test: its keys, its name,
// and a delete of the whole object, which a test can make fail. Once
// deleted it holds nothing; made again, it holds what is saved after.
type oldConfigMap struct {
	name    string
	mu      sync.Mutex
	keys    *kv.Memory
	gone    bool
	refuse  error
	removed int
}

func newOldConfigMap(name string, keys map[string]string) *oldConfigMap {
	c := &oldConfigMap{name: name, keys: &kv.Memory{}}
	for k, v := range keys {
		_, _ = c.keys.SaveKey(context.Background(), k, []byte(v), "")
	}
	return c
}

func (c *oldConfigMap) Ref() string { return "221b/" + c.name }

func (c *oldConfigMap) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil, "", kv.ErrNotFound
	}
	return c.keys.LoadKey(ctx, key)
}

func (c *oldConfigMap) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys.SaveKey(ctx, key, b, version)
}

func (c *oldConfigMap) Remove(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refuse != nil {
		return c.refuse
	}
	c.gone, c.keys = true, &kv.Memory{}
	c.removed++
	return nil
}

// madeAgain is the ConfigMap as Helm would make it again, empty.
func (c *oldConfigMap) madeAgain() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gone, c.keys = false, &kv.Memory{}
}

func (c *oldConfigMap) isGone() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gone
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

// A ConfigMap of before is deleted whole once every record set it held is
// recorded in imports (DB-55, DB-72): a key no record set reads goes with
// it; a copy that fails leaves it, with every key, for the next start, and
// a record set copied from it by another call keeps it too until both are
// copied; a deletion that fails is tried at the next start; one that Helm
// made again empty holds nothing to copy and is deleted again, and the
// database's records are not touched by it.
func TestAConfigMapOfBeforeIsDeletedOnceItsRecordSetsAreCopied(t *testing.T) {
	ctx := t.Context()
	// Copied and deleted, with a key no record set reads.
	d := dbtest.Open(t)
	w := &wiring{log: slog.Default(), database: d}
	cm := newOldConfigMap("r-casework", map[string]string{fixes.Key: oneFix, "other.json": `{"kept":true}`})
	w.inTables(ctx, cm, fixesIn)
	if cm.isGone() || fixRows(t, d) != 1 {
		t.Fatalf("before the wiring is done: gone %v, %d fixes", cm.isGone(), fixRows(t, d))
	}
	w.removeCopiedSources(ctx)
	if !cm.isGone() || fixRows(t, d) != 1 {
		t.Fatalf("after the copy: gone %v, %d fixes in the database", cm.isGone(), fixRows(t, d))
	}

	// Two calls read one ConfigMap (the settings' scopes and tables do):
	// one copy fails, and the ConfigMap stays whole, the copied key too.
	d2 := dbtest.Open(t)
	w2 := &wiring{log: slog.Default(), database: d2}
	both := newOldConfigMap("r", map[string]string{"setup.json": `{"dismissed":["backup"]}`, fixes.Key: oneFix})
	b := w2.inScopes(ctx, both, map[string]string{"setup.json": "setup"})
	w2.inTables(ctx, b, func(d *db.DB) map[string]movedDoc { return map[string]movedDoc{fixes.Key: uncopied{fixes.NewPG(d)}} })
	w2.removeCopiedSources(ctx)
	if both.isGone() {
		t.Fatal("a ConfigMap was deleted with a record set in it not copied")
	}
	for _, key := range []string{"setup.json", fixes.Key} {
		if _, _, err := both.LoadKey(ctx, key); err != nil {
			t.Fatalf("%s left the ConfigMap: %v", key, err)
		}
	}
	if copied, _ := d2.Imported(ctx, fixes.Key); copied {
		t.Fatal("a copy that failed was recorded")
	}
	// The next start copies the rest, and deletes it.
	w3 := &wiring{log: slog.Default(), database: d2}
	w3.inTables(ctx, w3.inScopes(ctx, both, map[string]string{"setup.json": "setup"}), fixesIn)
	w3.removeCopiedSources(ctx)
	if !both.isGone() || fixRows(t, d2) != 1 {
		t.Fatalf("at the next start: gone %v, %d fixes", both.isGone(), fixRows(t, d2))
	}

	// A deletion that fails leaves it, and the next start deletes it.
	d4 := dbtest.Open(t)
	refusing := newOldConfigMap("r-runs", map[string]string{fixes.Key: oneFix})
	refusing.refuse = errors.New("not allowed")
	w4 := &wiring{log: slog.Default(), database: d4}
	w4.inTables(ctx, refusing, fixesIn)
	w4.removeCopiedSources(ctx)
	if refusing.isGone() {
		t.Fatal("deleted though the deletion failed")
	}
	refusing.refuse = nil
	w5 := &wiring{log: slog.Default(), database: d4}
	w5.inTables(ctx, refusing, fixesIn)
	w5.removeCopiedSources(ctx)
	if !refusing.isGone() || fixRows(t, d4) != 1 {
		t.Fatalf("at the next start: gone %v, %d fixes", refusing.isGone(), fixRows(t, d4))
	}

	// Made again empty, as Helm would make one it still renders: nothing
	// to copy, deleted again; the database's records, changed since, are
	// as they were.
	if _, err := d.Pool().Exec(ctx, "UPDATE fixes SET title = 'Changed since'"); err != nil {
		t.Fatal(err)
	}
	cm.madeAgain()
	w6 := &wiring{log: slog.Default(), database: d}
	w6.inTables(ctx, cm, fixesIn)
	w6.removeCopiedSources(ctx)
	var title string
	if err := d.Pool().QueryRow(ctx, "SELECT title FROM fixes").Scan(&title); err != nil || title != "Changed since" {
		t.Fatalf("the database's fix reads %q: %v", title, err)
	}
	if !cm.isGone() || cm.removed != 2 {
		t.Fatalf("one made again: gone %v, deleted %d times", cm.isGone(), cm.removed)
	}

	// A routed key is read where it is routed and where it was kept
	// before: both hold it.
	d7 := dbtest.Open(t)
	settingsCM, auditCM := newOldConfigMap("r", nil), newOldConfigMap("r-audit", nil)
	w7 := &wiring{log: slog.Default(), database: d7}
	w7.readFrom(&kv.Routed{Default: &kv.Limited{Backend: settingsCM}, Routes: map[string]kv.Backend{"audit.json": auditCM}}, "audit.json", "audit.json")
	w7.removeCopiedSources(ctx)
	if settingsCM.isGone() || auditCM.isGone() {
		t.Fatal("deleted with the audit log not copied")
	}
	if got := w7.sources; len(got) != 2 || !got["221b/r"].imports["audit.json"] || !got["221b/r-audit"].imports["audit.json"] {
		t.Fatalf("the sources noted: %v", got)
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
// both are then kept and the ConfigMap is deleted. When the changes' copy
// fails, the conversations are not copied to rows that would not be kept:
// they stay in the ConfigMap, which is kept, for the next start.
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
	cm := newOldConfigMap("r-proposals", map[string]string{cards.CardsKey: oneCard, cards.ChatsKey: oneChat})
	w := &wiring{log: slog.Default(), database: d}
	w.inTables(ctx, cm, cardsIn)
	if chats, messages := rows(d); chats != 1 || messages != 2 {
		t.Fatalf("copied %d conversations of PC-1 with %d messages", chats, messages)
	}
	if w.removeCopiedSources(ctx); !cm.isGone() {
		t.Error("the ConfigMap is still there after both copies")
	}
	// The changes' copy fails: their conversations wait in the ConfigMap.
	d2 := dbtest.Open(t)
	cm2 := newOldConfigMap("r-proposals", map[string]string{cards.CardsKey: oneCard, cards.ChatsKey: oneChat})
	w2 := &wiring{log: slog.Default(), database: d2}
	w2.inTables(ctx, cm2, func(d *db.DB) map[string]movedDoc {
		docs := cardsIn(d)
		docs[cards.CardsKey] = uncopied{docs[cards.CardsKey]}
		return docs
	})
	w2.removeCopiedSources(ctx)
	if got, _, err := cm2.LoadKey(ctx, cards.ChatsKey); err != nil || string(got) != oneChat || cm2.isGone() {
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
// so does the issue service's ConfigMap once its three documents are
// copied (DB-55, DB-72), with a key no record set reads; with no record of
// a copy, they stay.
func TestTheCasesLeaveTheirConfigMapsOnceCopied(t *testing.T) {
	ctx := t.Context()
	objs := &kv.MemoryObjects{}
	_, _ = objs.Save(ctx, "1", issues.CaseKey, []byte(`{"number":1,"reporter":{"id":"ann"},"status":"new","created":"2026-11-07T02:00:00Z","updated":"2026-11-07T02:00:00Z","statusAt":"2026-11-07T02:00:00Z","priority":3,"lastReporter":"2026-11-07T02:00:00Z"}`), "")
	cm := newOldConfigMap("r-issues", map[string]string{issues.IndexKey: `{"next":2,"cases":[{"number":1,"status":"new","priority":3}]}`, issues.KnownKey: `{"next":1,"known":[]}`, "issues-reports.json": `{"next":1}`})
	d := dbtest.Open(t)
	w := &wiring{log: slog.Default(), database: d}
	// No record of a copy: nothing is removed.
	w.removeCopiedObjects(ctx, objs, issues.CasesImportID)
	w.readFrom(cm, issues.NewSetPG(d).ImportID(), issues.IndexKey, issues.KnownKey)
	w.removeCopiedSources(ctx)
	if names, _ := objs.List(ctx); len(names) != 1 {
		t.Fatal("a case's ConfigMap was removed with no copy recorded")
	}
	if _, _, err := cm.LoadKey(ctx, issues.IndexKey); err != nil || cm.isGone() {
		t.Fatalf("the index was removed with no copy recorded: %v", err)
	}
	// The copy, as the wiring makes it.
	w = &wiring{log: slog.Default(), database: d}
	b, cases := w.casesInDatabase(ctx)(cm, kv.Objects(wrapped{objs}))
	if names, _ := objs.List(ctx); len(names) != 0 {
		t.Fatalf("the cases' ConfigMaps left after the copy: %v", names)
	}
	if w.removeCopiedSources(ctx); !cm.isGone() {
		t.Fatal("the issue service's ConfigMap is still there after the copy")
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
