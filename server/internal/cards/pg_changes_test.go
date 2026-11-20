package cards

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
)

func rowsOf(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// kept saves a document through the database and through memory, as a
// ConfigMap keeps it, and fails unless both read back the same, field for
// field, and as it was saved.
func kept[T any](t *testing.T, b kv.Backend, key string, doc T) T {
	t.Helper()
	mem := &kv.Memory{}
	want, _ := roundTrip(t, mem, key, doc, "")
	_, version, err := b.LoadKey(context.Background(), key)
	if err != nil && version == "" {
		t.Fatal(err)
	}
	got, _ := roundTrip(t, b, key, doc, version)
	if !reflect.DeepEqual(got, want) {
		x, _ := json.MarshalIndent(got, "", " ")
		y, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("the database reads back\n%s\nand a ConfigMap\n%s", x, y)
	}
	return got
}

func fullCard() Card {
	exit := 1
	finished, last := day(7), day(8)
	return Card{ID: "PC-2",
		Proposal: Proposal{Agent: "troubleshooter", Source: Source{Kind: SourceCase, ID: "C-12", Label: "Case C-12", SetBy: "alice@example.com", Job: "Nightly audit"}, Cluster: "prod-1",
			Title: "Raise the memory limit of web", Reasoning: "It is killed for memory every few minutes.",
			Evidence: []Evidence{{Kind: "event", Text: "OOMKilled", Ref: "shop/Pod/web-1", At: day(1)}, {Kind: "fact", Text: "limit 128Mi"}},
			Actions: []Action{{Kind: KindKubectl, Args: map[string]any{"command": "kubectl -n shop set resources deploy/web --limits=memory=256Mi", "replicas": float64(3), "dry": true, "labels": map[string]any{"app": "web"}},
				Impact: "The pods restart once.", Risk: "low"}, {Kind: KindRecord, Args: map[string]any{}}, {Kind: KindHelm}},
			Condition: Condition{Kind: ConditionAlert, Args: map[string]any{"key": "KubePodCrashLooping/web"}}, DedupKey: "shop/web/memory", Expires: day(23),
			Resolves: []string{"C-12", "KI-3", "C-14"}, ResolvesBy: map[string]string{"C-12": LinkedByAgent, "C-14": "Bob", "C-99": "Carol"}},
		Model: "claude-opus-5-5", State: Failed, Created: day(2), Updated: day(8), Requester: &Person{Key: "alice", Name: "Alice", Kind: ActorPerson}, Production: true, Needed: 2,
		Approvals: []Decision{{By: Person{Key: "bob", Name: "Bob"}, At: day(4), Comment: "go"}, {By: Person{Key: "standing:s1", Name: "Standing approval"}, At: day(5), Standing: "s1"}},
		Rejection: &Decision{By: SystemPerson, At: day(6), Comment: "its case closed"},
		Recheck:   &Recheck{At: day(3), OK: true, Why: "nothing changed", Checked: []string{"The alert still fires."}, Warnings: []string{"action 2: the fleet has no node now"}},
		Plans: []Plan{{Summary: "Set the memory limit of deploy/web", Targets: []string{"shop/Deployment/web"}, Impact: []string{"3 pods restart"}, Fingerprint: "rv:41", Warning: "the limit halves"},
			{Summary: "record", Unread: "no such record"}, {Summary: "helm"}},
		Results: []Result{{Action: 0, OK: true, Summary: "done", Detail: "deployment.apps/web resource requirements updated", Ref: "w12", At: day(6)}, {Action: 1, Summary: "refused", At: day(6)}},
		Reason:  "action 2 of 3 failed: refused", ReplacedBy: "PC-9", Replaces: "PC-1", Standing: "s1", WatsonItem: "w11",
		History:    []Event{{At: day(2), What: "proposed", By: "Troubleshooter", Actor: &Actor{Name: "Troubleshooter", Kind: ActorAgent}}, {At: day(4), What: "approved", By: "Bob"}},
		ProposedBy: &Actor{Name: "Alice", Kind: ActorPerson}, LastBy: &Actor{Name: "System", Kind: ActorSystem}, LastAt: &last,
		Commands: []Command{{ID: "w12", Line: "kubectl -n shop get pods", By: "Bob", At: day(5), Status: "done"},
			{ID: "w13", Line: "kubectl -n shop delete pod web-1", By: "Bob", At: day(6), Change: true, Status: "failed", Exit: &exit, Approver: "Carol", Finished: &finished}}}
}

func plainCard(n int) Card {
	id := "PC-" + string(rune('0'+n))
	return Card{ID: id, Proposal: Proposal{Agent: "mycroft", Source: Source{Kind: SourceJob, Label: "Audit"}, Cluster: "dev-1", Title: "Change " + id,
		Actions: []Action{{Kind: KindKubectl, Args: map[string]any{"command": "kubectl get ns"}}}, Condition: Condition{Kind: ConditionNone}},
		State: Queued, Created: day(n), Updated: day(n), Needed: 1, Plans: []Plan{{Summary: "kubectl get ns", Fingerprint: "x"}}}
}

// Every field of a proposed change is a column and comes back as a
// ConfigMap gives it; a card that did not change is not written again,
// whether cards come in, go, or change order; one that changed keeps its
// row and its key (DB-56); a save over a stale version is refused; what
// hangs from a card goes with it.
func TestCardsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewCardsPG(d)
	ctx := context.Background()
	rec := record{Next: 4, Cards: []Card{plainCard(1), fullCard(), plainCard(3)}}
	kept(t, b, CardsKey, rec)
	for table, want := range map[string]int{"changes": 3, "change_evidence": 2, "change_actions": 5, "change_plans": 5, "change_results": 2, "change_decisions": 3,
		"change_events": 2, "change_commands": 2, "change_resolves": 4, "agents": 2} {
		if got := rowsOf(t, d, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
	rowOf := func(key string) (id string) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, "SELECT change_id::text FROM changes WHERE change_key = $1", key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	untouched, other := rowOf("PC-2"), rowOf("PC-3")
	// A card comes in at the end, one changes, the oldest goes.
	changed := plainCard(3)
	changed.State, changed.Updated, changed.Reason = Expired, day(9), "nobody decided it"
	rec.Next, rec.Cards = 5, []Card{fullCard(), changed, plainCard(4)}
	kept(t, b, CardsKey, rec)
	if rowOf("PC-2") != untouched || rowsOf(t, d, "changes") != 3 {
		t.Fatal("a card that did not change was written again")
	}
	if rowOf("PC-3") != other {
		t.Fatal("a card that changed did not keep its row")
	}
	// Changed in every list that hangs from it, it still does.
	whole := fullCard()
	whole.ID, whole.Created = "PC-3", day(3)
	rec.Cards[1] = whole
	kept(t, b, CardsKey, rec)
	rec.Cards[1] = changed
	kept(t, b, CardsKey, rec)
	if rowOf("PC-3") != other || rowOf("PC-2") != untouched {
		t.Fatal("a card that gained and lost what hangs from it did not keep its row")
	}
	// Any other order is kept too, as a backup put back gives it.
	rec.Cards = []Card{plainCard(4), plainCard(5), fullCard(), changed}
	kept(t, b, CardsKey, rec)
	if rowOf("PC-2") != untouched {
		t.Fatal("a card that only moved was written again")
	}
	// Another writer came first.
	_, version, _ := b.LoadKey(ctx, CardsKey)
	rec.Next = 6
	raw, _ := json.Marshal(rec)
	if _, err := b.SaveKey(ctx, CardsKey, raw, version); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveKey(ctx, CardsKey, raw, version); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	rec.Cards = nil
	kept(t, b, CardsKey, rec)
	for _, table := range []string{"changes", "change_evidence", "change_actions", "change_plans", "change_results", "change_decisions", "change_events", "change_commands", "change_resolves"} {
		if n := rowsOf(t, d, table); n != 0 {
			t.Errorf("%s has %d rows of cards that are gone", table, n)
		}
	}
	// What the ConfigMap held is copied once.
	cm := &kv.Memory{}
	raw, _ = json.Marshal(record{Next: 3, Cards: []Card{plainCard(1), fullCard()}})
	_, _ = cm.SaveKey(ctx, CardsKey, raw, "")
	fresh := NewCardsPG(dbtest.Open(t))
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("copy: %v %v", copied, err)
	}
	if copied, err := fresh.Import(ctx, cm, "test"); err != nil || copied {
		t.Fatalf("second copy: %v %v", copied, err)
	}
	got, _, err := fresh.LoadKey(ctx, CardsKey)
	if err != nil {
		t.Fatal(err)
	}
	var back, want record
	_ = json.Unmarshal(got, &back)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("copied %+v, want %+v", back, want)
	}
}

func fullMsgs() []sherlock.Msg {
	first, done := day(1).Add(2*time.Second), day(1).Add(9*time.Second)
	return []sherlock.Msg{
		{ID: 1, Role: "user", Text: "Why does web restart?", Asker: "Alice", At: day(1)},
		{ID: 2, Role: "sherlock", Text: "It is killed for memory.", Note: "Reading the events once more.", Status: "done", Error: "the stream ended early", At: day(1).Add(time.Second),
			FirstAt: &first, DoneAt: &done, Model: "claude-opus-5-5", Usage: &sherlock.Usage{InputTokens: 1200, OutputTokens: 80, CacheCreationInputTokens: 300, CacheReadInputTokens: 900},
			CostUSD: 0.0123, Agent: "troubleshooter",
			Steps: []sherlock.Step{{Label: "Reading the events of shop", Tool: "events", Status: "done"}},
			Refs:  []sherlock.Ref{{Kind: "Pod", Namespace: "shop", Name: "web-1"}, {Kind: "Node", Name: "n1"}},
			View:  []sherlock.ViewChange{{Label: "Opened the pod web-1"}},
			Show:  []sherlock.ViewShow{{Action: "open", Input: json.RawMessage(`{"z":1,"a":{"kind":"Pod","name":"web-1"}}`), Why: "moved"}},
			Hands: []*sherlock.Hand{{ID: 1, From: "sherlock", FromName: "Sherlock", To: "auditor", ToName: "Auditor", Task: "Check the limits", Status: "done", Text: "They are low.",
				Steps: []sherlock.Step{{Label: "Reading", Tool: "get", Status: "done"}}, Model: "claude-haiku-5-5", Usage: &sherlock.Usage{InputTokens: 10, OutputTokens: 2}, CostUSD: 0.001,
				Hands: []*sherlock.Hand{{ID: 2, From: "auditor", FromName: "Auditor", To: "mycroft", ToName: "Mycroft", Task: "Find the runbook", Status: "refused", Error: "not allowed"}}}}},
		{ID: 3, Role: "user", Text: "And now?", At: day(2)},
	}
}

// A waiting change's conversation is rows of chats and chat_messages,
// joined to the change by its key (DB-57): what is asked of a message is
// a column, its parts one JSON value that reads back as it was written; a
// message that did not change is not written again; a conversation that
// is dropped takes its messages; a change that changes keeps its
// conversation, and one that goes takes it.
func TestCardChatsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b, cardsDoc := NewChatsPG(d), NewCardsPG(d)
	ctx := context.Background()
	cardsRec := record{Next: 5, Cards: []Card{plainCard(2), plainCard(3), plainCard(4)}}
	kept(t, cardsDoc, CardsKey, cardsRec)
	rec := chatsRecord{Chats: map[string]chatRecord{
		"PC-2": {Cluster: "prod-1", Messages: fullMsgs(), Updated: day(2)},
		"PC-3": {Cluster: "dev-1", Messages: fullMsgs()[:1], Updated: day(1)},
		"PC-4": {},
	}}
	kept(t, b, ChatsKey, rec)
	if got := rowsOf(t, d, "chats"); got != 3 {
		t.Fatalf("chats has %d rows", got)
	}
	if got := rowsOf(t, d, "chat_messages"); got != 4 {
		t.Fatalf("chat_messages has %d rows", got)
	}
	// The conversation's row has the key of its change's row, and no case's.
	var chatID, changeID, parts string
	var tokens int
	if err := d.Pool().QueryRow(ctx, `SELECT c.chat_id::text, g.change_id::text, m.parts::text, m.tokens_in
		FROM chat_messages m JOIN chats c USING (chat_id) JOIN changes g USING (change_id) WHERE g.change_key = 'PC-2' AND c.case_id IS NULL AND m.position = 1`).
		Scan(&chatID, &changeID, &parts, &tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 1200 || !json.Valid([]byte(parts)) {
		t.Fatalf("the answer's row: %d tokens, parts %s", tokens, parts)
	}
	// The row's own stamp says which messages were written.
	written := func() map[int]string {
		t.Helper()
		out := map[int]string{}
		rows, err := d.Pool().Query(ctx, "SELECT m.position, m.xmin::text FROM chat_messages m JOIN chats c USING (chat_id) JOIN changes g USING (change_id) WHERE g.change_key = 'PC-2'")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var p int
			var x string
			if err := rows.Scan(&p, &x); err != nil {
				t.Fatal(err)
			}
			out[p] = x
		}
		return out
	}
	before := written()
	// The answer grows and a question is added; the other conversations
	// are as they were.
	one := rec.Chats["PC-2"]
	msgs := fullMsgs()
	msgs[2].Text = "And now, after the change?"
	one.Messages, one.Updated = append(msgs, sherlock.Msg{ID: 4, Role: "sherlock", Status: "thinking", At: day(3)}), day(3)
	rec.Chats["PC-2"] = one
	kept(t, b, ChatsKey, rec)
	after := written()
	if len(after) != 4 || after[0] != before[0] || after[1] != before[1] || after[2] == before[2] {
		t.Fatalf("written before %v, after %v", before, after)
	}
	// The change changes, as when it is rechecked or a case is linked to
	// it: its row keeps its key, and the conversation is the row it was.
	cardsRec.Cards[0].Title, cardsRec.Cards[0].Updated = "Another title", day(9)
	cardsRec.Cards[0].History = []Event{{At: day(9), What: "edited", By: "Pat"}}
	kept(t, cardsDoc, CardsKey, cardsRec)
	var chatNow, changeNow string
	if err := d.Pool().QueryRow(ctx, "SELECT c.chat_id::text, g.change_id::text FROM chats c JOIN changes g USING (change_id) WHERE g.change_key = 'PC-2'").Scan(&chatNow, &changeNow); err != nil {
		t.Fatal(err)
	}
	if chatNow != chatID || changeNow != changeID || len(written()) != 4 {
		t.Fatalf("after the change changed: conversation %s of change %s, was %s of %s", chatNow, changeNow, chatID, changeID)
	}
	kept(t, b, ChatsKey, rec)
	// The oldest question goes, and a conversation is dropped.
	one.Messages = one.Messages[2:]
	rec.Chats["PC-2"] = one
	delete(rec.Chats, "PC-3")
	kept(t, b, ChatsKey, rec)
	if got := rowsOf(t, d, "chat_messages"); got != 2 {
		t.Fatalf("chat_messages has %d rows", got)
	}
	// A change that is no longer kept takes its conversation with it.
	cardsRec.Cards = cardsRec.Cards[1:]
	kept(t, cardsDoc, CardsKey, cardsRec)
	delete(rec.Chats, "PC-2")
	raw, version, err := b.LoadKey(ctx, ChatsKey)
	var left chatsRecord
	_ = json.Unmarshal(raw, &left)
	if err != nil || len(left.Chats) != 1 || rowsOf(t, d, "chats") != 1 || rowsOf(t, d, "chat_messages") != 0 {
		t.Fatalf("after its change went: %s (%v), %d conversations with %d messages", raw, err, rowsOf(t, d, "chats"), rowsOf(t, d, "chat_messages"))
	}
	if _, has := left.Chats["PC-4"]; !has {
		t.Fatalf("the conversation left is not PC-4's: %s", raw)
	}
	rec.Chats = nil
	raw, _ = json.Marshal(rec)
	if _, err := b.SaveKey(ctx, ChatsKey, raw, version); err != nil {
		t.Fatal(err)
	}
	if n := rowsOf(t, d, "chats") + rowsOf(t, d, "chat_messages"); n != 0 {
		t.Fatalf("%d rows left of conversations that are gone", n)
	}
	if _, _, err := b.LoadKey(ctx, ChatsKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("reading no conversations: %v", err)
	}
	// A case's conversation is not a change's.
	caseID := db.NewID()
	if _, err := d.Pool().Exec(ctx, "INSERT INTO cases (case_id, object_name, case_number) VALUES ($1, '12', 12)", caseID); err != nil {
		t.Fatal(err)
	}
	batch := &pgx.Batch{}
	sherlock.QueueCaseChat(batch, caseID, nil, &sherlock.KeptChat{Cluster: "prod-1", Messages: fullMsgs()[:1]})
	if err := d.Pool().SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.LoadKey(ctx, ChatsKey); !errors.Is(err, kv.ErrNotFound) || rowsOf(t, d, "chats") != 1 {
		t.Fatalf("a case's conversation read as a change's: %v", err)
	}
}

// A conversation is of its change's row: one written for a change that
// has no row is not kept, without an error, and reads back as none, while
// the others saved with it are kept; once the change has its row, the
// conversation is kept as any other. The cards are therefore copied into
// the database before their conversations (DB-57).
func TestAConversationOfAChangeWithNoRowIsNotKept(t *testing.T) {
	d := dbtest.Open(t)
	b, cardsDoc := NewChatsPG(d), NewCardsPG(d)
	ctx := context.Background()
	kept(t, cardsDoc, CardsKey, record{Next: 3, Cards: []Card{plainCard(2)}})
	rec := chatsRecord{Chats: map[string]chatRecord{
		"PC-2": {Cluster: "prod-1", Messages: fullMsgs(), Updated: day(2)},
		"PC-9": {Cluster: "dev-1", Messages: fullMsgs(), Updated: day(3)},
	}}
	save := func() chatsRecord {
		t.Helper()
		raw, _ := json.Marshal(rec)
		_, version, _ := b.LoadKey(ctx, ChatsKey)
		if _, err := b.SaveKey(ctx, ChatsKey, raw, version); err != nil {
			t.Fatalf("saving a conversation of a change with no row: %v", err)
		}
		got, _, err := b.LoadKey(ctx, ChatsKey)
		if err != nil {
			t.Fatal(err)
		}
		var back chatsRecord
		_ = json.Unmarshal(got, &back)
		return back
	}
	back := save()
	if _, has := back.Chats["PC-9"]; has || len(back.Chats) != 1 || len(back.Chats["PC-2"].Messages) != 3 || rowsOf(t, d, "chats") != 1 || rowsOf(t, d, "chat_messages") != 3 {
		t.Fatalf("read back %d conversations, %d rows of chats, %d of chat_messages", len(back.Chats), rowsOf(t, d, "chats"), rowsOf(t, d, "chat_messages"))
	}
	// Saved again as the service has it, it is still not there, and nothing fails.
	if back = save(); len(back.Chats) != 1 {
		t.Fatalf("read back %d conversations", len(back.Chats))
	}
	// The change gets its row: the next save keeps its conversation.
	kept(t, cardsDoc, CardsKey, record{Next: 10, Cards: []Card{plainCard(2), plainCard(9)}})
	if back = save(); len(back.Chats) != 2 || len(back.Chats["PC-9"].Messages) != 3 || len(back.Chats["PC-9"].Messages[1].Hands) != 1 {
		t.Fatalf("once the change has a row: %+v", back.Chats["PC-9"])
	}
	// What a ConfigMap held is copied whole when the cards are copied first.
	cm := &kv.Memory{}
	rawCards, _ := json.Marshal(record{Next: 10, Cards: []Card{plainCard(2), plainCard(9)}})
	rawChats, _ := json.Marshal(rec)
	_, _ = cm.SaveKey(ctx, CardsKey, rawCards, "")
	_, _ = cm.SaveKey(ctx, ChatsKey, rawChats, "")
	fresh := dbtest.Open(t)
	if copied, err := NewCardsPG(fresh).Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("copying the cards: %v %v", copied, err)
	}
	if copied, err := NewChatsPG(fresh).Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("copying the conversations: %v %v", copied, err)
	}
	if rowsOf(t, fresh, "chats") != 2 || rowsOf(t, fresh, "chat_messages") != 6 {
		t.Fatalf("copied %d conversations with %d messages", rowsOf(t, fresh, "chats"), rowsOf(t, fresh, "chat_messages"))
	}
}

// worldOn is a test's world with its cards kept in the database d,
// whatever TEST_STORE says.
func worldOn(t *testing.T, d *db.DB) *world {
	t.Helper()
	w := &world{exec: &fakeExec{print: "v1"}, clock: &clock{t: time.Date(2026, 11, 5, 3, 12, 0, 0, time.UTC)}, audit: &syncBuffer{}, firing: map[string]bool{}, prod: map[string]bool{"prod-1": true},
		backend: &kv.Keyed{Keys: map[string]kv.Backend{CardsKey: NewCardsPG(d), ChatsKey: NewChatsPG(d), StandingKey: NewStandingPG(d), NotifyKey: NewNotifyPG(d)}, Rest: &kv.Memory{}}}
	w.st = w.open()
	return w
}

// A change keeps its row, and the row its key, through everything the
// service does to it (DB-56): a case linked, a command logged, a recheck,
// an approval, its actions applied. Its conversation hangs from that row
// the whole time, and is deleted when the change is decided; the change's
// row is then still the same one.
func TestAChangeKeepsItsRowThroughItsLife(t *testing.T) {
	d := dbtest.Open(t)
	w := worldOn(t, d)
	ctx := context.Background()
	w.fire("pod:shop/web-7f9", true)
	id, err := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/web"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/api"))
	if err != nil {
		t.Fatal(err)
	}
	rowOf := func(key string) (row string) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, "SELECT change_id::text FROM changes WHERE change_key = $1", key).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	chatOf := func(key string) (chat string, messages int) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, `SELECT coalesce(max(c.chat_id::text), ''), count(m.chat_id) FROM changes g LEFT JOIN chats c USING (change_id) LEFT JOIN chat_messages m USING (chat_id)
			WHERE g.change_key = $1`, key).Scan(&chat, &messages); err != nil {
			t.Fatal(err)
		}
		return chat, messages
	}
	row, otherRow := rowOf(id), rowOf(other)
	keeper := w.st.Chats()
	if err := keeper.Save(ctx, keyOf(id), exchange("Why restart?")); err != nil {
		t.Fatal(err)
	}
	chat, n := chatOf(id)
	if chat == "" || n != 2 {
		t.Fatalf("the conversation: %q with %d messages", chat, n)
	}
	pat := Person{Key: "pat", Name: "Pat"}
	same := func(after string) {
		t.Helper()
		if got := rowOf(id); got != row {
			t.Fatalf("after %s the change's row is %s, was %s", after, got, row)
		}
		if got := rowOf(other); got != otherRow {
			t.Fatalf("after %s the other change's row is %s, was %s", after, got, otherRow)
		}
	}
	talks := func(after string) {
		t.Helper()
		same(after)
		if got, n := chatOf(id); got != chat || n != 2 {
			t.Fatalf("after %s the conversation is %q with %d messages, was %q with 2", after, got, n, chat)
		}
		if msgs, err := keeper.Load(ctx, keyOf(id)); err != nil || len(msgs) != 2 || msgs[0].Text != "Why restart?" {
			t.Fatalf("after %s the conversation reads %+v %v", after, msgs, err)
		}
	}
	if _, err := w.st.Link(ctx, id, "C-14", pat); err != nil {
		t.Fatal(err)
	}
	talks("a case was linked")
	at := w.clock.Now()
	if err := w.st.LogCommands(ctx, id, []Command{{ID: "w1", Line: "kubectl get pods -n shop", By: "Pat", At: at, Status: "running"}}, pat); err != nil {
		t.Fatal(err)
	}
	talks("a command was logged")
	if c, err := w.st.Recheck(ctx, id, pat); err != nil || c.Recheck == nil {
		t.Fatalf("recheck: %+v %v", c, err)
	}
	talks("a recheck")
	// A new store on the same rows: the console restarted.
	w.st = w.open()
	keeper = w.st.Chats()
	talks("a restart")
	if _, err := w.st.Approve(ctx, id, person("sam"), "go"); err != nil {
		t.Fatal(err)
	}
	c := waitState(t, w.st, id, Applied)
	same("it was approved and applied")
	if len(c.Approvals) != 1 || len(c.Results) != 1 || len(c.Resolves) != 1 {
		t.Fatalf("applied: %+v", c)
	}
	// Decided, it keeps no conversation; its row is the one it had.
	if got, n := chatOf(id); got != "" || n != 0 {
		t.Fatalf("a decided change still has the conversation %q with %d messages", got, n)
	}
	if _, err := w.st.Reject(ctx, other, pat, "Not now."); err != nil {
		t.Fatal(err)
	}
	same("the other was rejected")
}
