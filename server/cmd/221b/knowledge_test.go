package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// ask_knowledge gives the open known issues a question matches beside
// the wiki's pages and excerpts, each by its id with its status,
// symptoms and workaround; a resolved one, or one that matches too
// little, is left out (CF-43).
func TestAskKnowledgeGivesTheKnownIssuesCF43(t *testing.T) {
	ctx := context.Background()
	svc := issues.New(issues.Options{Backend: &kv.Memory{}, Cases: &kv.MemoryObjects{}})
	op := issues.Caller{Person: issues.Person{ID: "olivia", Name: "Olivia"}, Operator: true}
	str := func(s string) *string { return &s }
	if _, err := svc.SaveKnown(ctx, op, "", issues.KnownInput{Title: str("Checkout fails with 502 errors"), Symptoms: []string{"checkout returns 502 bad gateway"}, Workaround: str("Retry the payment")}); err != nil {
		t.Fatal(err)
	}
	hits := knownHits(ctx, svc, "Why does checkout return 502 bad gateway?")
	if len(hits) != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	h := hits[0]
	if h.Kind != sherlock.KindKnownIssue || h.Path != "KI-1" || h.Title != "Checkout fails with 502 errors" || !strings.Contains(h.Text, "Symptoms: checkout returns 502 bad gateway.") || !strings.Contains(h.Text, "Workaround: Retry the payment") {
		t.Fatalf("hit: %+v", h)
	}
	if hits := knownHits(ctx, svc, "certificates on the ingress"); len(hits) != 0 {
		t.Fatalf("an unrelated question: %+v", hits)
	}
}
