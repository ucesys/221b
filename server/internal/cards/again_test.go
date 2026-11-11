package cards

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cardid"
)

// keyed is the restart of shop/web an agent proposes for a case, keyed as
// a job keys it: by its cluster, what it resolves and its commands
// (ADR-148).
func keyed(cluster string) Proposal {
	p := restart(cluster, "")
	p.Resolves = []string{"C-4"}
	p.DedupKey = cardid.Key(cluster, p.Resolves, []string{cardid.Step(KindKubectl, p.Actions[0].Args)})
	return p
}

func historyOf(c Card) string {
	var b strings.Builder
	for _, e := range c.History {
		b.WriteString(e.What + "\n")
	}
	return b.String()
}

// CF-110: the same change an agent proposes again while it waits is the
// card that waits. On a production cluster the first of its two approvals
// stays, the second applies it, and nothing is replaced.
func TestTheSameChangeProposedAgainKeepsItsCardAndItsApproval(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, err := w.st.Propose(ctx, keyed("prod-1"))
	if err != nil {
		t.Fatal(err)
	}
	if c, err := w.st.Approve(ctx, id, person("pat"), ""); err != nil || c.State != Queued || c.Needed != 2 || len(c.Approvals) != 1 {
		t.Fatalf("the first of two approvals: %+v %v", c, err)
	}
	first, _ := w.st.Get(ctx, id)
	// The next night's run proposes it again, in other words.
	w.clock.add(12 * time.Hour)
	p := keyed("prod-1")
	p.Title, p.Reasoning = "Restart the web deployment", "web still cannot sign in to its database."
	p.Source = Source{Kind: SourceJob, ID: "run-9", Label: "Nightly audit run-9"}
	again, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("proposed again, it is another card: %s, not %s", again, id)
	}
	all, _ := w.st.List(ctx, Filter{})
	c, _ := w.st.Get(ctx, id)
	if len(all) != 1 || c.State != Queued || len(c.Approvals) != 1 || c.Approvals[0].By.Key != "pat" || c.Title != "Restart shop/web" {
		t.Fatalf("one card with its approval: %d cards, %+v", len(all), c)
	}
	if !strings.Contains(historyOf(c), "proposed again by Troubleshooter: Nightly audit run-9") {
		t.Errorf("its history does not say it was proposed again:\n%s", historyOf(c))
	}
	// It waits as long as the proposal made now would.
	if !c.Expires.After(first.Expires) || !c.Expires.Equal(w.clock.Now().Add(Lifetime)) {
		t.Errorf("expires %v, was %v", c.Expires, first.Expires)
	}
	if log := w.audit.String(); strings.Contains(log, `"cards.replace"`) || !strings.Contains(log, "proposed again; "+id+" waits already and is kept, with 1 of 2 approval(s)") {
		t.Errorf("the audit log: %s", log)
	}
	// The second person's approval applies it.
	if _, err := w.st.Approve(ctx, id, person("sam"), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, id, Applied)
	// Applied, it no longer waits: proposed once more, it is a new card.
	next, err := w.st.Propose(ctx, keyed("prod-1"))
	if err != nil || next == id {
		t.Fatalf("proposed after it was applied: %s %v", next, err)
	}
}

// A newer proposal still replaces the card when it is not the same
// change: what it acts on changed since (nobody could approve the old
// card any more), its steps differ, or it names a case the card does not.
// The approval given goes with the replaced card.
func TestAChangedProposalStillReplacesTheWaitingCard(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		newer func(w *world) Proposal
	}{
		{"what it acts on changed", func(w *world) Proposal { w.exec.set("v2"); return keyed("prod-1") }},
		{"other steps under the same key", func(w *world) Proposal {
			p := keyed("prod-1")
			p.Actions = []Action{{Kind: KindKubectl, Args: map[string]any{"command": "kubectl rollout restart deployment api -n shop"}}}
			return p
		}},
		{"it names another case", func(w *world) Proposal {
			p := keyed("prod-1")
			p.Resolves = []string{"C-4", "C-9"}
			return p
		}},
		{"another condition", func(w *world) Proposal {
			p := keyed("prod-1")
			p.Condition = Condition{Kind: ConditionNone}
			return p
		}},
	} {
		w := newWorld(t)
		w.fire("pod:shop/web-7f9", true)
		id, err := w.st.Propose(ctx, keyed("prod-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.Approve(ctx, id, person("pat"), ""); err != nil {
			t.Fatal(err)
		}
		newer, err := w.st.Propose(ctx, tc.newer(w))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		old, _ := w.st.Get(ctx, id)
		n, _ := w.st.Get(ctx, newer)
		if newer == id || old.State != Replaced || old.ReplacedBy != newer || n.State != Queued || len(n.Approvals) != 0 {
			t.Errorf("%s: the older %s (%s, replaced by %q), the newer %s with %d approvals", tc.name, old.ID, old.State, old.ReplacedBy, n.ID, len(n.Approvals))
		}
	}
}

// What the proposal acts on cannot be read just now: that says nothing
// against the card that waits, which is kept. A card whose own plan was
// never read cannot be approved, so the proposal that can be replaces it.
func TestAPlanThatCannotBeReadKeepsOrReplaces(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, _ := w.st.Propose(ctx, keyed("prod-1"))
	if _, err := w.st.Approve(ctx, id, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	w.exec.mu.Lock()
	w.exec.unread = errors.New("the cluster did not answer")
	w.exec.mu.Unlock()
	if again, err := w.st.Propose(ctx, keyed("prod-1")); err != nil || again != id {
		t.Fatalf("unread now: %s %v, want the card %s kept", again, err, id)
	}
	if c, _ := w.st.Get(ctx, id); c.State != Queued || len(c.Approvals) != 1 || c.Plans[0].Fingerprint != "v1" {
		t.Fatalf("the card kept: %+v", c)
	}
	// A card proposed while nothing could be read, then one that can be.
	unread, err := w.st.Propose(ctx, keyed("dev-1"))
	if err != nil {
		t.Fatal(err)
	}
	w.exec.mu.Lock()
	w.exec.unread = nil
	w.exec.mu.Unlock()
	readable, err := w.st.Propose(ctx, keyed("dev-1"))
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := w.st.Get(ctx, unread); readable == unread || old.State != Replaced {
		t.Fatalf("a card nobody can approve was kept: %s (%s), the proposal %s", old.ID, old.State, readable)
	}
}

// A change a person sends to Watson from a chat again is a new Watson
// card, and its proposal goes with that card: it replaces the one before,
// as it did.
func TestAChatsChangeSentAgainStillReplaces(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "chat:dev-1:kubectl rollout restart deployment web -n shop")
	p.Condition = Condition{Kind: ConditionNone}
	pat := Person{Key: "pat", Name: "Pat"}
	a, err := w.st.ProposeFor(ctx, p, pat, "w1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.st.ProposeFor(ctx, p, pat, "w2")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := w.st.Get(ctx, a.ID)
	if b.ID == a.ID || old.State != Replaced || b.WatsonItem != "w2" {
		t.Fatalf("sent again: %s (%s), then %s for %q", old.ID, old.State, b.ID, b.WatsonItem)
	}
}

// With as many changes waiting as may, the same change proposed again
// is still taken: it adds none.
func TestTheSameChangeProposedAgainAddsNoCardWhenFull(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id, err := w.st.Propose(ctx, keyed("dev-1"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxOpen; i++ {
		p := restart("dev-1", "")
		p.Title = "Another " + strings.Repeat("i", i%7)
		if _, err := w.st.Propose(ctx, p); err != nil {
			t.Fatalf("card %d: %v", i, err)
		}
	}
	if _, err := w.st.Propose(ctx, restart("dev-1", "")); !errors.Is(err, ErrFull) {
		t.Fatalf("one more than may wait: %v", err)
	}
	if again, err := w.st.Propose(ctx, keyed("dev-1")); err != nil || again != id {
		t.Fatalf("the same change, with the list full: %s %v", again, err)
	}
}
