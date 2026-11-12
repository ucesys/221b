package main

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/cards"
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

// The proposed changes' ConfigMap is <release>-proposals since CF-104: at
// start, the records an install kept in <release>-cases are copied over
// once and the copy is logged, so a card filed before the rename is still
// there after it; once the new object is in use, nothing more is copied.
func TestTheProposedChangesOfBeforeAreCopiedOnceCF104(t *testing.T) {
	ctx := context.Background()
	before, after := &kv.Memory{}, &kv.Memory{}
	old := cardStoreOn(before)
	id, err := old.Propose(ctx, restart("Restart shop/web"))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	copyProposals(ctx, before, after, "ns/r-cases", "ns/r-proposals", log)
	if !strings.Contains(logs.String(), "copied once") || !strings.Contains(logs.String(), "from=ns/r-cases") || !strings.Contains(logs.String(), cards.CardsKey) {
		t.Fatalf("the copy is not logged: %s", logs.String())
	}
	if c, err := cardStoreOn(after).Get(ctx, id); err != nil || c.Title != "Restart shop/web" {
		t.Fatalf("the card filed before the rename: %+v %v", c, err)
	}

	// Both present: nothing is copied again, and what the new object
	// holds stays as it is.
	if _, err := old.Propose(ctx, restart("Restart shop/api")); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	copyProposals(ctx, before, after, "ns/r-cases", "ns/r-proposals", log)
	if logs.Len() != 0 {
		t.Fatalf("copied again: %s", logs.String())
	}
	list, err := cardStoreOn(after).List(ctx, cards.Filter{})
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("the new object's cards: %+v %v", list, err)
	}
}

// A copy cut short is finished at the next start, and never overwrites a
// key the new object holds.
func TestACopyCutShortIsFinishedCF104(t *testing.T) {
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
	keys, err := copyOnce(ctx, before, after, proposalKeys)
	if err != nil || !slices.Equal(keys, []string{cards.NotifyKey, cards.ChatsKey, cards.CardsKey}) {
		t.Fatalf("copied %v %v", keys, err)
	}
	if b, _, _ := after.LoadKey(ctx, cards.StandingKey); string(b) != "new" {
		t.Errorf("the new object's key was overwritten: %q", b)
	}
	if b, _, _ := after.LoadKey(ctx, cards.CardsKey); string(b) != "old "+cards.CardsKey {
		t.Errorf("the cards: %q", b)
	}
	// Nothing of before: nothing to do.
	if keys, err := copyOnce(ctx, &kv.Memory{}, &kv.Memory{}, proposalKeys); err != nil || len(keys) != 0 {
		t.Errorf("from an empty object: %v %v", keys, err)
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
