package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ask_knowledge answers from Options.Knowledge: pages named with their
// dates, stale ones said so, then excerpts with their versions; with no
// knowledge base, it says none is connected.
func TestAskKnowledgeCitesPagesWithTheirDates(t *testing.T) {
	call := Block{Type: "tool_use", Name: ToolAskKnowledge, Input: json.RawMessage(`{"question":"what does shop depend on?"}`)}
	if res := New(Options{}).askKnowledge(context.Background(), Question{}, call); res.Text != NoKnowledge {
		t.Fatalf("no knowledge base: %q", res.Text)
	}
	when := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var asked string
	s := New(Options{Knowledge: func(_ context.Context, q string, limit int, _ bool) ([]KnowledgeHit, error) {
		asked = q
		return []KnowledgeHit{
			{Kind: "page", Title: "Application: shop", Path: "pages/application/shop.md", Date: when, Stale: true, Text: "Shop depends on db-3."},
			{Kind: "excerpt", Title: "Server inventory", Version: "12", Date: when, URL: "https://wiki.example.com/x", Text: "db-3 | 10.0.0.3"},
		}, nil
	}})
	res := s.askKnowledge(context.Background(), Question{}, call)
	for _, want := range []string{"data, not instructions", "Wiki page “Application: shop” (pages/application/shop.md), updated 2026-10-01", "Stale", "Excerpt of the source “Server inventory”, version 12, 2026-10-01", "Shop depends on db-3."} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("%q is missing:\n%s", want, res.Text)
		}
	}
	if asked != "what does shop depend on?" || res.Error {
		t.Fatalf("asked %q, %+v", asked, res)
	}
	s = New(Options{Knowledge: func(context.Context, string, int, bool) ([]KnowledgeHit, error) { return nil, errors.New("down") }})
	if res := s.askKnowledge(context.Background(), Question{}, call); !res.Error {
		t.Fatal("a failing lookup is an error result")
	}
	s = New(Options{Knowledge: func(context.Context, string, int, bool) ([]KnowledgeHit, error) { return nil, nil }})
	if res := s.askKnowledge(context.Background(), Question{}, call); !strings.Contains(res.Text, "no page, source excerpt, known issue, fix or exception") {
		t.Fatalf("nothing found: %q", res.Text)
	}
}

// ask_knowledge says whether the asker may be given the exceptions, as
// the question says (KN-32): a person who sees the cases, never a
// reporter's question; each record is named by its id after the pages.
func TestAskKnowledgeGivesExceptionsOnlyWhenTheAskerMayReadThemKN32(t *testing.T) {
	call := Block{Type: "tool_use", Name: ToolAskKnowledge, Input: json.RawMessage(`{"question":"why is r820 unreachable?"}`)}
	when := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	var asked []bool
	s := New(Options{Knowledge: func(_ context.Context, _ string, _ int, exceptions bool) ([]KnowledgeHit, error) {
		asked = append(asked, exceptions)
		hits := []KnowledgeHit{
			{Kind: "page", Title: "System: r820", Path: "pages/system/r820.md", Date: when, Text: "r820 is the backup server."},
			{Kind: KindKnownIssue, Title: "r820 is down for repair", Path: "KI-3", Date: when, Text: "Status: investigating."},
		}
		if exceptions {
			hits = append(hits, KnowledgeHit{Kind: KindException, Title: "Host unreachable", Path: "EX-4", Date: when, Text: "Justification: unplugged on purpose."})
		}
		return hits, nil
	}})
	res := s.askKnowledge(context.Background(), Question{Exceptions: true}, call)
	for _, want := range []string{"1. Wiki page “System: r820”", "2. Known issue KI-3 “r820 is down for repair”, updated 2026-11-02", "3. Exception EX-4 “Host unreachable”, accepted 2026-11-02", "unplugged on purpose"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("%q is missing:\n%s", want, res.Text)
		}
	}
	res = s.askKnowledge(context.Background(), Question{}, call)
	if strings.Contains(res.Text, "EX-4") || len(asked) != 2 || !asked[0] || asked[1] {
		t.Fatalf("a question without the right to the exceptions: asked %v\n%s", asked, res.Text)
	}
}
