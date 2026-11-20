package sherlock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// --- A kept conversation keeps its hand-overs (DB-58) ---

// handedMsgs is an exchange whose answer handed work to an agent, which
// handed on to another, with what looks like a secret in every text.
func handedMsgs() []*Msg {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	done := at.Add(40 * time.Second)
	var steps []Step
	for range keptSteps + 10 {
		steps = append(steps, Step{Label: "Running psql --db-password hunter2secret --port 80", Tool: "exec", Status: "done"})
	}
	return []*Msg{
		{ID: 1, Role: "user", Text: "Why does web restart?", Asker: "Pat", At: at},
		{ID: 2, Role: "sherlock", Text: "It cannot sign in to its database.", Status: "done", At: at.Add(time.Second), DoneAt: &done, Model: "claude-opus-5-5",
			Steps: []Step{{Label: "Asking the Troubleshooter", Tool: "hand_over", Status: "done"}},
			Hands: []*Hand{
				{ID: 1, From: "sherlock", FromName: "Sherlock", To: "troubleshooter", ToName: "Troubleshooter", Task: "Find why web restarts. It signs in with password=hunter2 today.",
					Status: "done", Text: "The Secret shop-db changed: api_key=LogPlantedKey-77QzXwv4 is the new one. " + strings.Repeat("é", KeptText), Steps: steps,
					Model: "claude-sonnet-5-5", Usage: &Usage{InputTokens: 900, OutputTokens: 120}, CostUSD: 0.004,
					Hands: []*Hand{
						{ID: 2, From: "troubleshooter", FromName: "Troubleshooter", To: "auditor", ToName: "Auditor", Task: "Who changed the Secret shop-db?", Status: "failed",
							Text: "Sam did, and set token: abc123def, then stopped.", Error: "the stream ended at api_key=LogPlantedKey-77QzXwv4",
							Steps: []Step{{Label: "Reading the audit log", Tool: "audit", Status: "done"}}, Model: "claude-haiku-5-5", Usage: &Usage{InputTokens: 300, OutputTokens: 40}, CostUSD: 0.001,
							Hands: []*Hand{{ID: 3, From: "auditor", FromName: "Auditor", To: "mycroft", ToName: "Mycroft", Task: "Find the runbook for password=hunter2", Status: "refused", Error: "not allowed"}}},
					}},
				{ID: 4, From: "sherlock", FromName: "Sherlock", To: "mycroft", ToName: "Mycroft", Task: "Is there a runbook?", Status: "stopped"},
			}},
	}
}

// What is kept of a message's hand-overs: every one, with its own inside
// it, who asked whom and how it ended as they were; every text scrubbed
// of what looks like a secret (X-01) and clipped, the steps too; the
// conversation in memory is not touched.
func TestKeptHandOversAreScrubbedAndClipped(t *testing.T) {
	in := handedMsgs()
	out := keptMsgs(in)
	if len(out) != 2 || len(out[1].Hands) != 2 {
		t.Fatalf("kept %+v", out)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"hunter2", "LogPlantedKey-77QzXwv4", "abc123def"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s is kept", secret)
		}
	}
	h := out[1].Hands[0]
	if h.ID != 1 || h.From != "sherlock" || h.ToName != "Troubleshooter" || h.Status != "done" || h.Model != "claude-sonnet-5-5" || h.CostUSD != 0.004 || h.Usage == nil || h.Usage.InputTokens != 900 {
		t.Errorf("the first hand-over: %+v", h)
	}
	if !strings.HasPrefix(h.Task, "Find why web restarts.") || !strings.HasPrefix(h.Text, "The Secret shop-db changed: ") || len(h.Text) > KeptText+len("…") || !strings.HasSuffix(h.Text, "…") {
		t.Errorf("the first hand-over's texts: task %q, %d bytes of text %.60q", h.Task, len(h.Text), h.Text)
	}
	if len(h.Steps) != keptSteps || !strings.HasPrefix(h.Steps[0].Label, "Running psql") || h.Steps[0].Tool != "exec" {
		t.Errorf("the first hand-over keeps %d steps: %+v", len(h.Steps), h.Steps[:1])
	}
	if len(h.Hands) != 1 || len(h.Hands[0].Hands) != 1 {
		t.Fatalf("the hand-overs inside: %+v", h.Hands)
	}
	inner, last := h.Hands[0], h.Hands[0].Hands[0]
	if inner.To != "auditor" || inner.Status != "failed" || !strings.HasPrefix(inner.Text, "Sam did") || !strings.HasPrefix(inner.Error, "the stream ended") || len(inner.Steps) != 1 {
		t.Errorf("the second: %+v", inner)
	}
	if last.ID != 3 || last.To != "mycroft" || last.Status != "refused" || last.Error != "not allowed" || !strings.HasPrefix(last.Task, "Find the runbook") {
		t.Errorf("the third: %+v", last)
	}
	if out[1].Hands[1].ID != 4 || out[1].Hands[1].Status != "stopped" {
		t.Errorf("the fourth: %+v", out[1].Hands[1])
	}
	// What is in memory is as it was.
	was := in[1].Hands[0]
	if !strings.Contains(was.Task, "hunter2") || len(was.Steps) != keptSteps+10 || !strings.Contains(was.Steps[0].Label, "hunter2secret") || !strings.Contains(was.Hands[0].Hands[0].Task, "hunter2") {
		t.Errorf("keeping changed the conversation in memory: %+v", was)
	}
	was.Usage.InputTokens = 1
	if h.Usage.InputTokens != 900 {
		t.Error("what is kept shares its numbers with the conversation in memory")
	}
}

// A message's hand-overs are bounded together, nested ones counted: the
// first keptHands of them, and keptHandsText characters of what they
// said, after which a hand-over keeps who and how only. The message then
// fits a keeper's bound of one conversation many times over.
func TestKeptHandOversAreBounded(t *testing.T) {
	var hands []*Hand
	n := 0
	for range keptHands {
		n += 2
		hands = append(hands, &Hand{ID: n - 1, From: "sherlock", To: "troubleshooter", Task: strings.Repeat("t", 4000), Status: "done", Text: strings.Repeat("a", 3*KeptText),
			Steps: []Step{{Label: "Reading", Tool: "get", Status: "done"}},
			Hands: []*Hand{{ID: n, From: "troubleshooter", To: "auditor", Task: "Check", Status: "done", Text: strings.Repeat("b", KeptText)}}})
	}
	out := keptMsgs([]*Msg{{ID: 1, Role: "sherlock", Text: "Done.", Hands: hands}})
	count, text := 0, 0
	var walk func(hs []*Hand)
	var lastKept *Hand
	walk = func(hs []*Hand) {
		for _, h := range hs {
			count++
			lastKept = h
			text += len(h.Task) + len(h.Text) + len(h.Error)
			if len(h.Text) > KeptText+len("…") {
				t.Errorf("hand-over %d keeps %d bytes of text", h.ID, len(h.Text))
			}
			walk(h.Hands)
		}
	}
	walk(out[0].Hands)
	// Each clipped text may end in the mark of the cut.
	if count != keptHands || text > keptHandsText+3*len("…")*keptHands {
		t.Fatalf("%d hand-overs kept with %d bytes of text; at most %d and about %d", count, text, keptHands, keptHandsText)
	}
	if lastKept.ID != keptHands || lastKept.From != "troubleshooter" || lastKept.Status != "done" || len(lastKept.Text) > len("…") || len(lastKept.Steps) != 0 {
		t.Errorf("the last one kept: %+v", lastKept)
	}
	if out[0].Hands[0].Text != strings.Repeat("a", KeptText)+"…" || len(out[0].Hands[0].Steps) != 1 {
		t.Errorf("the first one kept: %d bytes of text, %d steps", len(out[0].Hands[0].Text), len(out[0].Hands[0].Steps))
	}
	if raw, _ := json.Marshal(out); len(raw) > 32<<10 {
		t.Errorf("the message is kept as %d bytes", len(raw))
	}
}

// pgKeeper keeps the conversation of one case in the message tables, as
// the case work's keeper does through the case's object.
type pgKeeper struct {
	d      *db.DB
	caseID string
}

func (p pgKeeper) Keeps(k Key) bool { return strings.HasPrefix(k.Session, "case:") }

func (p pgKeeper) Load(ctx context.Context, _ Key) ([]Msg, error) {
	var out []Msg
	err := p.d.Snapshot(ctx, func(q db.Querier) error {
		c, err := ReadCaseChat(ctx, q, p.caseID)
		if c != nil {
			out = c.Messages
		}
		return err
	})
	return out, err
}

func (p pgKeeper) Save(ctx context.Context, k Key, msgs []Msg) error {
	return p.d.Tx(ctx, func(tx pgx.Tx) error {
		old, err := ReadCaseChat(ctx, tx, p.caseID)
		if err != nil {
			return err
		}
		b := &pgx.Batch{}
		QueueCaseChat(b, p.caseID, old, &KeptChat{Cluster: k.Cluster, Messages: msgs})
		return tx.SendBatch(ctx, b).Close()
	})
}

// A kept conversation read back after a restart (a new service over the
// same keeper) has its hand-overs, the ones inside them too, scrubbed:
// kept in memory as a ConfigMap's JSON keeps them, and in the database's
// message tables (the seventh cluster test's O1).
func TestAKeptConversationHasItsHandOversAfterARestart(t *testing.T) {
	k := Key{Session: "case:C-12", Cluster: "local"}
	check := func(t *testing.T, kp Keeper) {
		s := New(Options{Keeper: kp})
		c := s.Conversation(k)
		c.mu.Lock()
		c.msgs = handedMsgs()
		c.mu.Unlock()
		s.keep(c, k)
		// What the first service showed, as it is kept.
		want, _ := json.Marshal(keptMsgs(handedMsgs())[1].Hands)
		s2 := New(Options{Keeper: kp})
		v := s2.Conversation(k).View()
		if len(v.Messages) != 2 || v.Messages[1].Status != "done" || v.Messages[1].Text != "It cannot sign in to its database." {
			t.Fatalf("read back %+v", v.Messages)
		}
		got, _ := json.Marshal(v.Messages[1].Hands)
		if string(got) != string(want) {
			t.Fatalf("the hand-overs read back\n%s\nwere kept as\n%s", got, want)
		}
		hands := v.Messages[1].Hands
		if len(hands) != 2 || len(hands[0].Hands) != 1 || len(hands[0].Hands[0].Hands) != 1 || hands[0].Hands[0].Hands[0].To != "mycroft" || hands[0].Usage == nil || hands[0].Usage.OutputTokens != 120 {
			t.Fatalf("the hand-overs read back: %s", got)
		}
		for _, secret := range []string{"hunter2", "LogPlantedKey-77QzXwv4", "abc123def"} {
			if strings.Contains(string(got), secret) {
				t.Errorf("%s was kept", secret)
			}
		}
	}
	t.Run("memory", func(t *testing.T) { check(t, &jsonKeeper{saved: map[string][]byte{}}) })
	t.Run("database", func(t *testing.T) {
		d := dbtest.Open(t)
		id := db.NewID()
		if _, err := d.Pool().Exec(context.Background(), "INSERT INTO cases (case_id, object_name, case_number) VALUES ($1, '12', 12)", id); err != nil {
			t.Fatal(err)
		}
		check(t, pgKeeper{d: d, caseID: id})
		var parts string
		if err := d.Pool().QueryRow(context.Background(), `SELECT m.parts::text FROM chat_messages m JOIN chats c USING (chat_id) WHERE c.case_id = $1 AND m.position = 1`, id).Scan(&parts); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(parts, `"hands"`) || strings.Contains(parts, "hunter2") {
			t.Errorf("the answer's parts: %s", parts)
		}
	})
}

// jsonKeeper keeps conversations as JSON, as a ConfigMap's key does.
type jsonKeeper struct{ saved map[string][]byte }

func (j *jsonKeeper) Keeps(k Key) bool { return strings.HasPrefix(k.Session, "case:") }

func (j *jsonKeeper) Load(_ context.Context, k Key) ([]Msg, error) {
	var out []Msg
	if b := j.saved[k.Session]; len(b) > 0 {
		return out, json.Unmarshal(b, &out)
	}
	return nil, nil
}

func (j *jsonKeeper) Save(_ context.Context, k Key, msgs []Msg) error {
	b, err := json.Marshal(msgs)
	j.saved[k.Session] = b
	return err
}
