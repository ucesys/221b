package cards

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- Who did it, everywhere (Phase 12, wave 6c, CF-35) ---

// A card names who proposed it (the agent, or the person who asked in a
// chat), who decided it (a person, or the system), who changed it last
// and when, and each history line's actor with its kind.
func TestACardNamesWhoDidWhatCF35(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, err := w.st.Propose(ctx, restart("dev-1", ""))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Get(ctx, id)
	agent := Actor{Name: "Troubleshooter", Kind: ActorAgent}
	if c.ProposedBy == nil || *c.ProposedBy != agent || c.Proposer() != agent || c.LastBy == nil || *c.LastBy != agent || c.LastAt == nil || c.Decider() != nil {
		t.Fatalf("proposed: %+v %+v %+v", c.ProposedBy, c.LastBy, c.Decider())
	}
	if c.History[0].Actor == nil || *c.History[0].Actor != agent || c.History[0].By != "Sam" {
		t.Errorf("the first line: %+v", c.History[0])
	}
	c, err = w.st.Approve(ctx, id, person("ann"), "")
	if err != nil {
		t.Fatal(err)
	}
	c = waitState(t, w.st, id, Applied)
	ann := Actor{Name: "Ann", Kind: ActorPerson}
	if d := c.Decider(); d == nil || *d != ann || *c.LastBy != ann {
		t.Fatalf("applied: decided by %+v, last %+v", d, c.LastBy)
	}
	// A chat's card is the person's who asked.
	chat := restart("dev-1", "")
	chat.Source = Source{Kind: SourceChat, Label: "Sherlock chat", SetBy: "Bo"}
	cc, err := w.st.ProposeFor(ctx, chat, Person{Key: "bo", Name: "Bo"}, "")
	if err != nil || cc.ProposedBy == nil || *cc.ProposedBy != (Actor{Name: "Bo", Kind: ActorPerson}) {
		t.Fatalf("a chat's card: %+v %v", cc.ProposedBy, err)
	}
	// Rejected by the console, it is the system's.
	cc, err = w.st.Reject(ctx, cc.ID, SystemPerson, "Case C-1 was closed as fixed")
	if err != nil {
		t.Fatal(err)
	}
	if d := cc.Decider(); d == nil || d.Kind != ActorSystem || cc.LastBy.Kind != ActorSystem || cc.History[len(cc.History)-1].Actor.Kind != ActorSystem {
		t.Fatalf("rejected by the system: %+v %+v", d, cc.LastBy)
	}
	b, _ := json.Marshal(cc)
	for _, want := range []string{`"proposedBy":{"name":"Bo","kind":"person"}`, `"lastBy":{"name":"System","kind":"system"}`, `"actor":{"name":"System","kind":"system"}`, `"lastAt":"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %s in %s", want, b)
		}
	}
	// A standing approval is the system.
	if a := ActorOf(Person{Key: "standing:s1", Name: "standing approval"}); a.Kind != ActorSystem {
		t.Errorf("a standing approval: %+v", a)
	}
}
