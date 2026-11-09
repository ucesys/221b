package cards

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/backup"
)

// --- Proposed changes are PC-<n> (CF-56) ---

// oldCards are three cards as a record kept before CF-56 names them: c1
// replaced by c2, c2 and c3 waiting.
func oldCards(at time.Time) []Card {
	card := func(id, state string) Card {
		return Card{ID: id, Proposal: restart("dev-1", ""), State: state, Created: at, Updated: at, Needed: 1, Plans: []Plan{{Summary: "kubectl rollout restart deployment web -n shop"}}}
	}
	c1, c2, c3 := card("c1", Replaced), card("c2", Queued), card("c3", Queued)
	c1.ReplacedBy, c1.History = "c2", []Event{{At: at, What: "proposed by Troubleshooter"}, {At: at, What: "replaced by the newer proposal c2"}}
	c2.Replaces = "c1"
	for _, c := range []*Card{&c1, &c2, &c3} {
		c.Expires = at.Add(time.Hour)
		c.Condition = Condition{Kind: ConditionNone}
	}
	return []Card{c1, c2, c3}
}

// A record kept before names its cards c<n>: they read as PC-<n>, are
// found by either id, are saved the new way once the record changes, and
// the next card goes on from the same number.
func TestOldCardIDsReadAsPC(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	raw, _ := json.Marshal(record{Next: 3, Cards: oldCards(w.clock.Now())})
	if _, err := w.backend.SaveKey(ctx, CardsKey, raw, ""); err != nil {
		t.Fatal(err)
	}
	list, err := w.st.List(ctx, Filter{})
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	byID := map[string]Card{}
	for _, c := range list {
		byID[c.ID] = c
	}
	c1, c2 := byID["PC-1"], byID["PC-2"]
	if c1.ReplacedBy != "PC-2" || c2.Replaces != "PC-1" || byID["PC-3"].ID == "" || c1.History[1].What != "replaced by the newer proposal PC-2" {
		t.Fatalf("the old ids do not read as PC-<n>: %+v", list)
	}
	for _, id := range []string{"c2", "C2", "pc-2", "PC-2", " PC-2 "} {
		if c, err := w.st.Get(ctx, id); err != nil || c.ID != "PC-2" {
			t.Errorf("Get(%q) = %q, %v", id, c.ID, err)
		}
	}
	if _, err := w.st.Get(ctx, "C-2"); err == nil {
		t.Error("the case C-2 found a card")
	}
	// Decided by its old id, the card is saved the new way, and so is the
	// rest of the record.
	if c, err := w.st.Reject(ctx, "c3", Person{Key: "sam", Name: "Sam"}, "not now"); err != nil || c.ID != "PC-3" || c.State != Rejected {
		t.Fatalf("reject c3: %+v %v", c, err)
	}
	saved, _, _ := w.backend.LoadKey(ctx, CardsKey)
	if s := string(saved); strings.Contains(s, `"c1"`) || strings.Contains(s, `"c2"`) || strings.Contains(s, `"c3"`) || !strings.Contains(s, `"id":"PC-3"`) || !strings.Contains(s, `"replacedBy":"PC-2"`) {
		t.Errorf("the record is not saved the new way: %s", s)
	}
	id, err := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/api"))
	if err != nil || id != "PC-4" {
		t.Errorf("the next card is %q (%v), want PC-4", id, err)
	}
	if !strings.Contains(w.audit.String(), "proposal PC-3") {
		t.Errorf("the audit names the card the old way: %s", w.audit.String())
	}
}

// A backup made before CF-56 comes back with PC-<n> ids, each card once,
// however often it is restored.
func TestABackupOfOldCardIDsRestoresAsPC(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := Backup{Next: 3, Cards: oldCards(w.clock.Now())}
	for try := range 2 {
		n, _, err := w.st.Import(ctx, b, (&backup.Env{}).Open)
		if err != nil || n != map[int]int{0: 3, 1: 0}[try] {
			t.Fatalf("import %d: %d %v", try, n, err)
		}
	}
	list, _ := w.st.List(ctx, Filter{})
	var ids []string
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	if fmt.Sprint(ids) != "[PC-1 PC-2 PC-3]" {
		t.Fatalf("restored: %v", ids)
	}
	if c, err := w.st.Get(ctx, "c1"); err != nil || c.ReplacedBy != "PC-2" {
		t.Errorf("c1 restored: %+v %v", c, err)
	}
	ex, err := w.st.Export(ctx, (&backup.Env{}).Seal)
	if err != nil || len(ex.Cards) != 3 || ex.Cards[0].ID != "PC-1" || ex.Next != 3 {
		t.Errorf("export: %+v %v", ex, err)
	}
	if id, _ := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/api")); id != "PC-4" {
		t.Errorf("the next card after a restore is %q, want PC-4", id)
	}
}

// --- end of PC-<n>.
