package cards

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- A proposed change names the cases it resolves (Phase 12, wave 6b) ---

// A card keeps the cases and known issues it resolves, written one way and
// each once, linked by the agent (CF-25); one that names something else,
// or more than 20, is refused. A chat's card and Mycroft's may resolve
// nothing (CF-27).
func TestACardResolvesCasesAndKnownIssuesCF25(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "")
	p.Resolves = []string{" c-12", "KI-3", "C-12"}
	id, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Get(ctx, id)
	if !slices.Equal(c.Resolves, []string{"C-12", "KI-3"}) || c.ResolvesBy["C-12"] != LinkedByAgent || c.ResolvesBy["KI-3"] != LinkedByAgent || len(c.ResolvesBy) != 2 {
		t.Fatalf("resolves %v by %v", c.Resolves, c.ResolvesBy)
	}
	if !strings.Contains(w.audit.String(), "resolves C-12, KI-3") {
		t.Errorf("audit: %s", w.audit.String())
	}
	bad := restart("dev-1", "")
	bad.Resolves = []string{"X-1"}
	if _, err := w.st.Propose(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("a resolve that is no case: %v", err)
	}
	many := restart("dev-1", "")
	for i := 1; i <= MaxResolves+1; i++ {
		many.Resolves = append(many.Resolves, fmt.Sprintf("C-%d", i))
	}
	if _, err := w.st.Propose(ctx, many); !errors.Is(err, ErrInvalid) {
		t.Errorf("21 resolves: %v", err)
	}
	// A chat's card and Mycroft's resolve nothing, and are kept.
	chat := restart("dev-1", "")
	chat.Source = Source{Kind: SourceChat, Label: "Sherlock chat", SetBy: "Ann"}
	if c, err := w.st.ProposeFor(ctx, chat, Person{Key: "ann", Name: "Ann"}, ""); err != nil || len(c.Resolves) != 0 || c.ResolvesBy != nil {
		t.Errorf("a chat's card: %+v %v", c.Resolves, err)
	}
	drift := restart("dev-1", "")
	drift.Agent, drift.Source = "mycroft", Source{Kind: SourceKnowledge, Label: "Drift check"}
	if _, err := w.st.Propose(ctx, drift); err != nil {
		t.Errorf("Mycroft's card: %v", err)
	}
}

// A card a newer proposal replaces passes what it resolves on to the
// newer card, with who linked each (CF-28).
func TestAReplacedCardPassesItsResolvesOnCF28(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "restart:dev-1/shop/web")
	p.Resolves = []string{"C-12"}
	old, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Link(ctx, old, "C-14", Person{Key: "ann", Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	q := restart("dev-1", "restart:dev-1/shop/web")
	q.Resolves = []string{"C-15", "C-12"}
	newer, err := w.st.Propose(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := w.st.Get(ctx, newer)
	if n.Replaces != old || !slices.Equal(n.Resolves, []string{"C-15", "C-12", "C-14"}) || n.ResolvesBy["C-14"] != "Ann" || n.ResolvesBy["C-12"] != LinkedByAgent {
		t.Fatalf("the newer card: %v by %v", n.Resolves, n.ResolvesBy)
	}
}

// A person links a case to a card and unlinks one they linked; the
// agent's link stays (CF-28).
func TestAPersonLinksAndUnlinksACaseCF28(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "")
	p.Resolves = []string{"C-12"}
	id, _ := w.st.Propose(ctx, p)
	c, err := w.st.Link(ctx, id, "c-20", Person{Key: "ann", Name: "Ann"})
	if err != nil || !slices.Equal(c.Resolves, []string{"C-12", "C-20"}) || c.ResolvesBy["C-20"] != "Ann" || c.History[len(c.History)-1].What != "linked to C-20" {
		t.Fatalf("linked: %v %v %v", c.Resolves, c.ResolvesBy, err)
	}
	if _, err := w.st.Link(ctx, id, "C-20", Person{Key: "bob", Name: "Bob"}); !errors.Is(err, ErrState) {
		t.Errorf("linked twice: %v", err)
	}
	if _, err := w.st.Link(ctx, id, "nonsense", Person{Name: "Bob"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a link to nothing: %v", err)
	}
	if _, err := w.st.Link(ctx, "PC-99", "C-1", Person{Name: "Bob"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such card: %v", err)
	}
	if _, err := w.st.Unlink(ctx, id, "C-12", Person{Key: "ann", Name: "Ann"}); !errors.Is(err, ErrState) {
		t.Errorf("the agent's link was taken off: %v", err)
	}
	if _, err := w.st.Unlink(ctx, id, "C-21", Person{Key: "ann", Name: "Ann"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a link that is not there: %v", err)
	}
	c, err = w.st.Unlink(ctx, id, "C-20", Person{Key: "bob", Name: "Bob"})
	if err != nil || !slices.Equal(c.Resolves, []string{"C-12"}) || c.ResolvesBy["C-20"] != "" {
		t.Fatalf("unlinked: %v %v %v", c.Resolves, c.ResolvesBy, err)
	}
}

// What listens hears each card whose state changed: proposed, replaced,
// rejected, expired, applied and failed (CF-29); AppliedAt is when it was
// applied.
func TestTheStoreTellsOfEachCardStateCF29(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var mu sync.Mutex
	var heard []string
	w.st.OnChange(func(c Card) {
		mu.Lock()
		heard = append(heard, c.ID+" "+c.State)
		mu.Unlock()
	})
	a, _ := w.st.Propose(ctx, restart("dev-1", "same"))
	// What it acts on changed: the newer proposal replaces the older.
	w.exec.set("v2")
	b, _ := w.st.Propose(ctx, restart("dev-1", "same"))
	if _, err := w.st.Reject(ctx, b, Person{Key: "pat", Name: "Pat"}, "not now"); err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Propose(ctx, restart("dev-1", ""))
	w.clock.add(25 * time.Hour)
	w.st.Sweep(ctx)
	w.clock.add(time.Minute)
	w.fire("pod:shop/web-7f9", true)
	d, _ := w.st.Propose(ctx, restart("dev-1", ""))
	w.clock.add(time.Minute)
	if _, err := w.st.Approve(ctx, d, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	done := waitState(t, w.st, d, Applied)
	w.exec.mu.Lock()
	w.exec.fail = true
	w.exec.mu.Unlock()
	e, _ := w.st.Propose(ctx, restart("dev-1", ""))
	if _, err := w.st.Approve(ctx, e, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, e, Failed)
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{a + " queued", a + " replaced", b + " queued", b + " rejected", c + " expired", d + " applied", e + " failed"} {
		if !slices.Contains(heard, want) {
			t.Errorf("not heard: %s in %v", want, heard)
		}
	}
	if at := done.AppliedAt(); at.IsZero() || at.After(w.clock.Now()) {
		t.Errorf("applied at %v", at)
	}
	if (Card{State: Queued}).AppliedAt() != (time.Time{}) {
		t.Error("a card not applied has a time it was applied")
	}
}
