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
// whether cards come in, go, or change order; a save over a stale version
// is refused; what hangs from a card goes with it.
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
	if rowOf("PC-3") == other {
		t.Fatal("a card that changed kept its row")
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

// A waiting change's conversation is rows of chats and chat_messages:
// what is asked of a message is a column, its parts one JSON value that
// reads back as it was written; a message that did not change is not
// written again; a conversation that is dropped takes its messages.
func TestCardChatsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	b := NewChatsPG(d)
	ctx := context.Background()
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
	var session, parts string
	var tokens int
	if err := d.Pool().QueryRow(ctx, `SELECT c.session, m.parts::text, m.tokens_in FROM chat_messages m JOIN chats c USING (chat_id) WHERE c.session = 'proposal:PC-2' AND m.position = 1`).
		Scan(&session, &parts, &tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 1200 || !json.Valid([]byte(parts)) {
		t.Fatalf("the answer's row: %d tokens, parts %s", tokens, parts)
	}
	// The row's own stamp says which messages were written.
	written := func() map[int]string {
		t.Helper()
		out := map[int]string{}
		rows, err := d.Pool().Query(ctx, "SELECT m.position, m.xmin::text FROM chat_messages m JOIN chats c USING (chat_id) WHERE c.session = 'proposal:PC-2'")
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
	// The oldest question goes, and a conversation is dropped.
	one.Messages = one.Messages[2:]
	rec.Chats["PC-2"] = one
	delete(rec.Chats, "PC-3")
	kept(t, b, ChatsKey, rec)
	if got := rowsOf(t, d, "chat_messages"); got != 2 {
		t.Fatalf("chat_messages has %d rows", got)
	}
	rec.Chats = nil
	raw, _ := json.Marshal(rec)
	_, version, _ := b.LoadKey(ctx, ChatsKey)
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
	batch := &pgx.Batch{}
	sherlock.QueueChat(batch, "case:C-12", nil, &sherlock.KeptChat{Cluster: "prod-1", Messages: fullMsgs()[:1]})
	if err := d.Pool().SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.LoadKey(ctx, ChatsKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("a case's conversation read as a change's: %v", err)
	}
}
