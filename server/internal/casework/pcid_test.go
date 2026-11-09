package casework

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- Proposed changes are PC-<n> (CF-56) ---

// The records kept before CF-56 name the cards c<n>: the cards' record
// (c1 to c3), and a case's work, its record and a known issue's update
// that name c2, the change that fixed it. The case lists PC-2 with its
// state, its fix and who linked it; the card acts no second time; a
// person unlinks it by its old id, and the work is saved the new way.
func TestOldCardIDsOnACaseReadAsPC(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	backend := &kv.Memory{}
	st := cards.New(cards.Options{Backend: backend, Now: func() time.Time { return w.now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w.svc.SetCards(st)
	w.jobs.SetRouter(w.svc)
	kase, err := w.svc.Create(ctx, NewCase{Title: "The shop is slow", Cluster: "prod"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	ki, err := w.iss.SaveKnown(ctx, olivia.issue(), "", issues.KnownInput{Title: ptrS("Certificates renewed by hand")})
	if err != nil {
		t.Fatal(err)
	}
	fixed := w.card("c2", cards.Applied, kase.ID, ki.ID)
	fixed.ResolvesBy[kase.ID] = "Olivia"
	raw, _ := json.Marshal(map[string]any{"next": 3, "cards": []cards.Card{w.card("c1", cards.Queued), fixed, w.card("c3", cards.Queued)}})
	if _, err := backend.SaveKey(ctx, cards.CardsKey, raw, ""); err != nil {
		t.Fatal(err)
	}
	text := "Fix applied by change c2: Set web's limits"
	if err := w.svc.updateWork(ctx, 1, func(x *Work) error {
		x.Proposals, x.LinkedBy, x.Handled = []string{"c2"}, map[string]string{"c2": "Olivia"}, map[string]string{"c2": cards.Applied}
		x.FixApplied = &Note{Text: text, By: "Pat", At: w.now, Change: "c2"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.iss.Apply(ctx, 1, func(_ *issues.Tx, r *issues.Report) error {
		r.AppliedBy = &issues.AppliedChange{Card: "c2", Title: "Set web's limits", By: "Pat", At: w.now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.iss.Batch(ctx, func(tx *issues.Tx) error {
		tx.Known(ki.ID).Log(issues.Update{At: w.now, By: system.Person, Text: "Fix applied by change c2"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	v := w.get(t, kase.ID)
	if len(v.Proposals) != 1 || v.Proposals[0].ID != "PC-2" || v.Proposals[0].State != cards.Applied || v.Proposals[0].LinkedBy != "Olivia" || len(v.ProposalIDs) != 1 || v.ProposalIDs[0] != "PC-2" {
		t.Fatalf("the case's changes: %+v %v", v.Proposals, v.ProposalIDs)
	}
	if f := v.FixApplied; f == nil || f.Change != "PC-2" || f.Text != "Fix applied by change PC-2: Set web's limits" {
		t.Errorf("fix applied: %+v", v.FixApplied)
	}
	if v.AppliedBy == nil || v.AppliedBy.Card != "PC-2" {
		t.Errorf("applied by: %+v", v.AppliedBy)
	}
	// The card acted on the case and the known issue already: nothing more.
	got, err := st.Get(ctx, "c2")
	if err != nil || got.ID != "PC-2" {
		t.Fatalf("c2: %+v %v", got, err)
	}
	w.svc.CardChanged(got)
	if m := messages(w.get(t, kase.ID)); strings.Contains(m, "Fix applied by change") {
		t.Errorf("the card acted again on the case: %s", m)
	}
	k, _ := w.iss.KnownIssue(ctx, system, ki.ID)
	n := 0
	for _, u := range k.Updates {
		if strings.HasPrefix(u.Text, "Fix applied by change") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the card acted again on the known issue: %+v", k.Updates)
	}
	// Unlinked by its old id; the work no longer names it either way.
	if v, err = w.svc.UnlinkChange(ctx, kase.ID, "c2", olivia); err != nil || len(v.ProposalIDs) != 0 {
		t.Fatalf("unlink c2: %v %v", err, v.ProposalIDs)
	}
	keys, _, _ := w.objects.Load(ctx, "1")
	if s := string(keys[issues.WorkKey]); strings.Contains(s, `"c2"`) || !strings.Contains(s, `"change":"PC-2"`) {
		t.Errorf("the work is not saved the new way: %s", s)
	}
}

// --- end of PC-<n>.
