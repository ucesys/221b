package cards

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// --- A proposed change's Sherlock and Watson (CF-61) ---

func keyOf(id string) sherlock.Key { return sherlock.Key{Session: ChatSession(id), Cluster: "dev-1"} }

func exchange(q string) []sherlock.Msg {
	return []sherlock.Msg{{ID: 1, Role: "user", Text: q, Asker: "Pat"}, {ID: 2, Role: "sherlock", Text: "An answer.", Status: "done"}}
}

// A proposal's conversation and commands come from "proposal:PC-12", read
// in either form of its id; nothing else does.
func TestProposalFromCF61(t *testing.T) {
	for in, want := range map[string]string{"proposal:PC-12": "PC-12", "proposal:pc12": "PC-12", "proposal:c12": "PC-12"} {
		if got, ok := ProposalFrom(in); !ok || got != want {
			t.Errorf("%s: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"case:C-12", "proposal:C-12", "Sherlock", "proposal:"} {
		if _, ok := ProposalFrom(in); ok {
			t.Errorf("%s reads as a proposal", in)
		}
	}
	if ChatSession("c7") != "proposal:PC-7" {
		t.Error(ChatSession("c7"))
	}
}

// Each proposal keeps its own conversation while it waits; it is deleted,
// in the key and in Sherlock's memory, once the proposal is decided, by
// any path: rejected, expired, replaced or applied.
func TestAProposalsConversationIsKeptUntilItIsDecidedCF61(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	var forgot []string
	w.st.SetForget(func(session string) int { forgot = append(forgot, session); return 1 })
	keeper := w.st.Chats()
	ids := map[string]string{}
	for _, how := range []string{"reject", "expire", "replace", "apply", "wait"} {
		id, err := w.st.Propose(ctx, restart("dev-1", "restart:"+how))
		if err != nil {
			t.Fatal(err)
		}
		ids[how] = id
		if !keeper.Keeps(keyOf(id)) {
			t.Fatalf("%s is not kept", id)
		}
		if err := keeper.Save(ctx, keyOf(id), exchange("Why "+how+"?")); err != nil {
			t.Fatal(err)
		}
	}
	if keeper.Keeps(sherlock.Key{Session: "case:C-1"}) {
		t.Error("a case's conversation is kept with the proposals")
	}
	if got, _ := keeper.Load(ctx, keyOf(ids["reject"])); len(got) != 2 || got[0].Text != "Why reject?" || got[0].Asker != "Pat" {
		t.Fatalf("loaded %+v", got)
	}
	if _, err := w.st.Reject(ctx, ids["reject"], Person{Key: "sam", Name: "Sam"}, "Not now."); err != nil {
		t.Fatal(err)
	}
	w.clock.add(Lifetime / 2)
	// A newer proposal for the same thing, whose objects changed since,
	// replaces the older one.
	w.exec.set("v2")
	if _, err := w.st.Propose(ctx, restart("dev-1", "restart:replace")); err != nil {
		t.Fatal(err)
	}
	w.exec.set("v1")
	if _, err := w.st.Approve(ctx, ids["apply"], Approver{Person: Person{Key: "sam", Name: "Sam"}}, ""); err != nil {
		t.Fatal(err)
	}
	w.st.Wait()
	// The others wait until their time ends: one is found expired when
	// approved, the other by the sweep.
	c, _ := w.st.Get(ctx, ids["expire"])
	w.clock.add(c.Expires.Sub(w.clock.Now()) + time.Minute)
	if _, err := w.st.Approve(ctx, ids["wait"], Approver{Person: Person{Key: "sam", Name: "Sam"}}, ""); err == nil {
		t.Fatal("approved after it expired")
	}
	w.st.Sweep(ctx)
	for how, id := range ids {
		got, err := keeper.Load(ctx, keyOf(id))
		if err != nil || len(got) != 0 {
			t.Errorf("%s (%s): its conversation is still there: %+v %v", how, id, got, err)
		}
		if !strings.Contains(strings.Join(forgot, ","), ChatSession(id)) {
			t.Errorf("%s (%s) was not forgotten: %v", how, id, forgot)
		}
	}
	// A decided proposal keeps none.
	if err := keeper.Save(ctx, keyOf(ids["reject"]), exchange("Again?")); err == nil {
		t.Error("a decided proposal kept a conversation")
	}
	if !strings.Contains(w.audit.String(), `"action":"cards.chat"`) {
		t.Error("the deletion is not audited")
	}
}

// A conversation is bounded: one proposal's within MaxChatBytes, all of
// them within their key, the conversation spoken in longest ago losing its
// oldest question first; each still starts with a question.
func TestAProposalsConversationIsBoundedCF61(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.st.opts.Limits.ChatsBytes = 120 << 10
	keeper := w.st.Chats()
	long := func(n int) []sherlock.Msg {
		var out []sherlock.Msg
		for i := range n {
			out = append(out, sherlock.Msg{ID: 2*i + 1, Role: "user", Text: strings.Repeat("q", 2000), Asker: "Pat"}, sherlock.Msg{ID: 2*i + 2, Role: "sherlock", Text: strings.Repeat("a", 4000), Status: "done"})
		}
		return out
	}
	var ids []string
	for i := range 3 {
		id, err := w.st.Propose(ctx, restart("dev-1", "bounded:"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if err := keeper.Save(ctx, keyOf(id), long(20)); err != nil {
			t.Fatal(err)
		}
		w.clock.add(time.Minute)
	}
	raw, _, _ := w.backend.LoadKey(ctx, ChatsKey)
	if len(raw) > 120<<10 {
		t.Fatalf("the key holds %d bytes", len(raw))
	}
	var rec chatsRecord
	_ = json.Unmarshal(raw, &rec)
	for i, id := range ids {
		r := rec.Chats[id]
		if b, _ := json.Marshal(r); len(b) > MaxChatBytes {
			t.Errorf("%s holds %d bytes", id, len(b))
		}
		if len(r.Messages) > 0 && r.Messages[0].Role != "user" {
			t.Errorf("%s starts with %s", id, r.Messages[0].Role)
		}
		if i == 2 && len(r.Messages) == 0 {
			t.Error("the newest conversation lost everything")
		}
	}
	if len(rec.Chats[ids[0]].Messages) >= len(rec.Chats[ids[2]].Messages) {
		t.Errorf("the oldest conversation did not give way first: %d, %d", len(rec.Chats[ids[0]].Messages), len(rec.Chats[ids[2]].Messages))
	}
}

// The commands run from a proposal are checked against it and logged on
// it, every one while it waits, audited; once decided, the log keeps only
// the changes that finished done, and a change that ends later is still
// kept.
func TestAProposalsCommandLogCF61(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id, err := w.st.Propose(ctx, restart("dev-1", "log"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.CheckCommand(ctx, id, "prod-1"); err == nil {
		t.Error("a command from the proposal ran on another cluster")
	}
	if _, err := w.st.CheckCommand(ctx, "PC-99", "dev-1"); err == nil {
		t.Error("a command from no proposal")
	}
	if _, err := w.st.CheckCommand(ctx, id, "dev-1"); err != nil {
		t.Fatal(err)
	}
	at := w.clock.Now()
	zero, one := 0, 1
	pat := Person{Key: "pat", Name: "Pat"}
	if err := w.st.LogCommands(ctx, id, []Command{{ID: "w1", Line: "kubectl get pods -n shop", By: "Pat", At: at, Status: "running"}, {ID: "w2", Line: "kubectl rollout restart deployment web -n shop", By: "Pat", At: at, Change: true, Status: "pending"}}, pat); err != nil {
		t.Fatal(err)
	}
	if err := w.st.LogCommands(ctx, id, []Command{{ID: "w3", Line: "kubectl delete pod web-1 -n shop", By: "Pat", At: at, Change: true, Status: "pending"}}, pat); err != nil {
		t.Fatal(err)
	}
	_ = w.st.CommandEnded(ctx, id, Command{ID: "w1", Line: "kubectl get pods -n shop", Status: "done", Exit: &zero})
	_ = w.st.CommandEnded(ctx, id, Command{ID: "w2", Line: "kubectl rollout restart deployment web -n shop", Change: true, Status: "done", Exit: &zero, Approver: "Sam"})
	_ = w.st.CommandEnded(ctx, id, Command{ID: "w3", Line: "kubectl delete pod web-1 -n shop", Change: true, Status: "done", Exit: &one, Approver: "Sam"})
	// An end does not go back to running.
	_ = w.st.CommandEnded(ctx, id, Command{ID: "w1", Status: "running"})
	c, _ := w.st.Get(ctx, id)
	if len(c.Commands) != 3 || c.Commands[0].Status != "done" || c.Commands[1].Approver != "Sam" || !c.Commands[1].Fixed() || c.Commands[2].Fixed() {
		t.Fatalf("the log: %+v", c.Commands)
	}
	if n := strings.Count(w.audit.String(), `"action":"cards.command"`); n != 3 {
		t.Errorf("%d commands audited, want 3", n)
	}
	if _, err := w.st.Reject(ctx, id, Person{Key: "sam", Name: "Sam"}, "Done by hand."); err != nil {
		t.Fatal(err)
	}
	c, _ = w.st.Get(ctx, id)
	if len(c.Commands) != 1 || c.Commands[0].ID != "w2" {
		t.Fatalf("kept once decided: %+v", c.Commands)
	}
	// Decided, nothing more is run from it; a change that ends now, done,
	// is still kept, and anything else is not.
	if _, err := w.st.CheckCommand(ctx, id, "dev-1"); err == nil {
		t.Error("a command from a decided proposal")
	}
	if err := w.st.LogCommands(ctx, id, []Command{{ID: "w9", Line: "kubectl get pods", Status: "running"}}, pat); err == nil {
		t.Error("a decided proposal logged a new command")
	}
	_ = w.st.CommandEnded(ctx, id, Command{ID: "w4", Line: "kubectl scale deployment web --replicas=3 -n shop", Change: true, Status: "done", Exit: &zero})
	_ = w.st.CommandEnded(ctx, id, Command{ID: "w5", Line: "kubectl get pods", Status: "done", Exit: &zero})
	c, _ = w.st.Get(ctx, id)
	if len(c.Commands) != 2 || c.Commands[1].ID != "w4" {
		t.Fatalf("after the decision: %+v", c.Commands)
	}
}

// What the conversation starts from: the proposal as data, its commands
// with their impact and risk and what Watson read of them.
func TestAProposalAsDataCF61(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "data")
	p.Actions[0].Impact, p.Actions[0].Risk = "web restarts", "medium"
	p.Resolves = []string{"C-4"}
	id, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Get(ctx, id)
	txt := c.TroubleText("Development", "Troubleshooter")
	for _, want := range []string{"id: " + id, "title: Restart shop/web", "cluster: Development", "proposed by: Troubleshooter", "reasoning:\nweb cannot sign in", "action 1 of 1: kubectl rollout restart deployment web -n shop", "the agent's impact: web restarts", "risk: medium", "acts on: deployment shop/web", "Watson read: 2 pods replaced", "resolves: C-4"} {
		if !strings.Contains(txt, want) {
			t.Errorf("no %q in %q", want, txt)
		}
	}
}
