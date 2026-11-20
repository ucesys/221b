package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// planOnly describes any action and applies none, for a store in a test.
type planOnly struct{}

func (planOnly) Check(cards.Action) error { return nil }
func (planOnly) Describe(context.Context, string, cards.Action) (cards.Plan, error) {
	return cards.Plan{Summary: "kubectl rollout restart"}, nil
}
func (planOnly) Apply(context.Context, cards.Card, int, cards.Approver) (cards.Result, error) {
	return cards.Result{}, nil
}
func (planOnly) Admin() bool { return false }

func cardStoreOn(b kv.Backend) *cards.Store {
	st := cards.New(cards.Options{Backend: b, Logger: slog.New(slog.DiscardHandler), Cluster: func(id string) (cards.ClusterInfo, bool) {
		return cards.ClusterInfo{Name: "Development"}, id == "dev-1"
	}})
	st.Register(cards.KindKubectl, planOnly{})
	return st
}

func restart(title string) cards.Proposal {
	return cards.Proposal{Agent: "troubleshooter", Source: cards.Source{Kind: cards.SourceAlert, Label: "Alert KubePodCrashLooping on shop/web"}, Cluster: "dev-1", Title: title,
		Actions: []cards.Action{{Kind: cards.KindKubectl, Args: map[string]any{"command": "kubectl rollout restart deployment web -n shop"}}}}
}

// The proposed changes' ConfigMap is <release>-proposals since CF-104;
// <release>-cases of before is read in place by the copy into the
// database, and nothing is written to either ConfigMap (DB-72): a card
// filed before the rename is copied, and both ConfigMaps are deleted once
// it is in the database.
func TestTheProposedChangesOfBeforeAreCopiedIntoTheDatabaseCF104(t *testing.T) {
	ctx := t.Context()
	before, after := newOldConfigMap("r-cases", nil), newOldConfigMap("r-proposals", nil)
	old := cardStoreOn(before)
	id, err := old.Propose(ctx, restart("Restart shop/web"))
	if err != nil {
		t.Fatal(err)
	}
	if c, err := cardStoreOn(proposalsOfBefore{now: after, before: before}).Get(ctx, id); err != nil || c.Title != "Restart shop/web" {
		t.Fatalf("the card filed before the rename, read through both: %+v %v", c, err)
	}
	if _, err := (proposalsOfBefore{now: after, before: before}).SaveKey(ctx, cards.CardsKey, []byte("{}"), ""); err == nil {
		t.Fatal("a save to the ConfigMaps of before was taken")
	}
	d := dbtest.Open(t)
	w := &wiring{log: slog.Default(), database: d}
	b := w.inTables(ctx, proposalsOfBefore{now: after, before: before}, func(d *db.DB) map[string]movedDoc {
		return map[string]movedDoc{cards.StandingKey: cards.NewStandingPG(d), cards.NotifyKey: cards.NewNotifyPG(d), cards.CardsKey: cards.NewCardsPG(d), cards.ChatsKey: cards.NewChatsPG(d)}
	})
	if c, err := cardStoreOn(b).Get(ctx, id); err != nil || c.Title != "Restart shop/web" {
		t.Fatalf("the card in the database: %+v %v", c, err)
	}
	w.removeCopiedSources(ctx)
	if !before.isGone() || !after.isGone() {
		t.Fatalf("after the copy: <release>-cases gone %v, <release>-proposals gone %v", before.isGone(), after.isGone())
	}
}

// A key is read from <release>-proposals, and from the ConfigMap of before
// only where the new one does not hold it and holds no cards yet.
func TestTheProposalsOfBeforeAreReadWhereTheNewOneHasNoneCF104(t *testing.T) {
	ctx := context.Background()
	before, after := &kv.Memory{}, &kv.Memory{}
	for _, k := range proposalKeys {
		if _, err := before.SaveKey(ctx, k, []byte("old "+k), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := after.SaveKey(ctx, cards.StandingKey, []byte("new"), ""); err != nil {
		t.Fatal(err)
	}
	read := func(key string) string {
		b, _, err := proposalsOfBefore{now: after, before: before}.LoadKey(ctx, key)
		if err != nil {
			return err.Error()
		}
		return string(b)
	}
	if got := read(cards.StandingKey); got != "new" {
		t.Errorf("the new object's key: %q", got)
	}
	for _, k := range []string{cards.NotifyKey, cards.ChatsKey, cards.CardsKey} {
		if got := read(k); got != "old "+k {
			t.Errorf("%s: %q", k, got)
		}
	}
	// The new object in use: nothing of before.
	if _, err := after.SaveKey(ctx, cards.CardsKey, []byte("new cards"), ""); err != nil {
		t.Fatal(err)
	}
	if got := read(cards.NotifyKey); got != kv.ErrNotFound.Error() {
		t.Errorf("read from before with the new object in use: %q", got)
	}
	// No new object: everything of before.
	if b, _, err := (proposalsOfBefore{before: before}).LoadKey(ctx, cards.CardsKey); err != nil || string(b) != "old "+cards.CardsKey {
		t.Errorf("without <release>-proposals: %q %v", b, err)
	}
}

// The name of before is the release's, beside the new one; a name set by
// hand has none.
func TestTheProposalsConfigMapOfBeforeCF104(t *testing.T) {
	for ref, want := range map[string]string{
		"consoles/k8s-console-221b-proposals": "consoles/k8s-console-221b-cases",
		"r-proposals":                         "r-cases",
		"consoles/-proposals":                 "",
		"consoles/my-cards":                   "",
	} {
		if got := oldProposalsRef(ref); got != want {
			t.Errorf("%s: %q, want %q", ref, got, want)
		}
	}
}
