package sherlock

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/fakeclaude"
)

// Each request is logged once, at Debug, with the agent, the model, the
// effort and the betas it was sent with, and no credential (the
// measurement of November 23, 2026, F8; AI-55).
func TestEachRequestLogsItsEffortAndBetasAI55(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var logs bytes.Buffer
	const key = "sk-ant-test-0123456789abcdef"
	client := &Client{BaseURL: srv.URL, Key: func() string { return key }, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	q := Message{Role: "user", Content: []Block{{Type: "text", Text: "Are the nodes ready?"}}}
	// Auto's change of effort in a conversation: the message and its beta.
	if _, err := client.Stream(context.Background(), Request{Model: "claude-sonnet-5-5", MaxTokens: 100, Agent: "sherlock", Messages: []Message{q, EffortMessage("medium")}}, func(Delta) {}); err != nil {
		t.Fatal(err)
	}
	// A fixed effort, at the top level.
	if _, err := client.Stream(context.Background(), Request{Model: "claude-opus-5-5", MaxTokens: 100, Agent: "auditor", Messages: []Message{q}, OutputConfig: &OutputConfig{Effort: "low"}}, func(Delta) {}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines:\n%s", len(lines), logs.String())
	}
	for i, want := range [][]string{
		{"level=DEBUG", "agent=sherlock", "model=claude-sonnet-5-5", `effort="medium (message)"`, "betas=" + BetaMidConversationEffort},
		{"agent=auditor", "model=claude-opus-5-5", `effort="low (top level)"`, `betas=""`},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i], w) {
				t.Errorf("line %d lacks %s: %s", i, w, lines[i])
			}
		}
	}
	if strings.Contains(logs.String(), key) || strings.Contains(logs.String(), "nodes ready") {
		t.Fatalf("the log carries the key or the question:\n%s", logs.String())
	}
}
