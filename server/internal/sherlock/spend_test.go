package sherlock

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
)

// Two replicas count into one ConfigMap key: neither save puts back what
// the other one added (P7-02), and each sees the other's within
// ledgerFresh.
func TestLedgerAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	shared := &kv.Memory{}
	a := NewLedger(shared, clock, nil)
	b := NewLedger(shared, clock, nil)
	a.Now(ctx)
	b.Now(ctx) // both have read the empty spend
	a.Add(ctx, 1.0, true)
	b.Add(ctx, 0.5, true)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx); err != nil { // read before a's save: it reads again
		t.Fatal(err)
	}
	c := NewLedger(shared, clock, nil)
	if got := c.Now(ctx); got.DayUSD != 1.5 || got.DayAsked != 2 || got.MonthUSD != 1.5 {
		t.Fatalf("saved %+v", got)
	}
	// a's read of b's save waits for ledgerFresh, or a Reload.
	if got := a.Now(ctx); got.DayUSD != 1.0 {
		t.Fatalf("a re-read at once: %+v", got)
	}
	now = now.Add(ledgerFresh + time.Second)
	if got := a.Now(ctx); got.DayUSD != 1.5 {
		t.Fatalf("a after %v: %+v", ledgerFresh, got)
	}
	b.Add(ctx, 0.25, false)
	_ = b.Save(ctx)
	a.Reload()
	if got := a.Now(ctx); got.DayUSD != 1.75 || got.DayAsked != 2 {
		t.Fatalf("a after Reload: %+v", got)
	}
	// Nothing counted: nothing written.
	before, v1, _ := shared.LoadKey(ctx, SpendKey)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	after, v2, _ := shared.LoadKey(ctx, SpendKey)
	if v1 != v2 || string(before) != string(after) {
		t.Fatalf("an empty save wrote %s", after)
	}
}

// conflicted is a backend where another replica saves once between a
// save's read and its write.
type conflicted struct {
	kv.Memory
	other func()
}

func (c *conflicted) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if f := c.other; f != nil {
		c.other = nil
		f()
	}
	return c.Memory.SaveKey(ctx, key, b, version)
}

func TestLedgerSaveRetriesAfterAConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := &conflicted{}
	a := NewLedger(c, clock, nil)
	other := NewLedger(&c.Memory, clock, nil)
	c.other = func() {
		other.Add(ctx, 2, true)
		if err := other.Save(ctx); err != nil {
			t.Error(err)
		}
	}
	a.Add(ctx, 1, true)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	a.Reload()
	if got := a.Now(ctx); got.DayUSD != 3 || got.DayAsked != 2 {
		t.Fatalf("after a conflict %+v", got)
	}
}

func TestKeysReload(t *testing.T) {
	ctx := context.Background()
	shared := &MemoryKey{}
	a := NewKeys(shared, "", nil)
	b := NewKeys(shared, "", nil)
	_ = a.Load(ctx)
	_ = b.Load(ctx)
	if err := a.Set(ctx, "sk-ant-api03-abcdefgh", "ann"); err != nil {
		t.Fatal(err)
	}
	if b.Key() != "" {
		t.Fatal("b has the key before reloading")
	}
	if err := b.Reload(ctx); err != nil || b.Key() != "sk-ant-api03-abcdefgh" || b.Info().SetBy != "ann" {
		t.Fatalf("b after reload: %q %+v %v", b.Key(), b.Info(), err)
	}
	if err := a.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Reload(ctx); err != nil || b.Key() != "" || b.Info().Set {
		t.Fatalf("b after the key was removed: %q %+v %v", b.Key(), b.Info(), err)
	}
	// A key from the chart is not read.
	fixed := NewKeys(shared, "sk-fixed-0123456789", nil)
	if err := fixed.Reload(ctx); err != nil || fixed.Key() != "sk-fixed-0123456789" {
		t.Fatalf("fixed %q %v", fixed.Key(), err)
	}
}

// sse answers with the lines given; a stream that stops without a last
// line is a stream cut off.
func sse(lines ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			_, _ = w.Write([]byte("data: " + l + "\n\n"))
		}
	}
}

func chatProvider(addr string) Provider {
	return Provider{Kind: KindOpenAI, Endpoint: addr, AuthHeaderName: "Authorization", AuthPrefix: "Bearer "}
}

func streamOnce(t *testing.T, p Provider, text string) (Response, error) {
	t.Helper()
	c := &Client{Provider: func() Provider { return p }, Key: func() string { return "k" }}
	return c.Stream(context.Background(), Request{Model: "m", MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: text}}}}}, nil)
}

var longWords = strings.Repeat("word ", 200)

// A cut stream, a finished one with no usage, and one with real figures.
func TestAChatStreamWithoutUsageIsEstimated(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"content":"` + longWords + `"}}]}`
	for _, c := range []struct {
		name    string
		lines   []string
		failed  bool
		noUsage bool
	}{
		{"cut off", []string{chunk}, true, false},
		{"no usage reported", []string{chunk, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(sse(c.lines...))
			defer srv.Close()
			resp, err := streamOnce(t, chatProvider(srv.URL), strings.Repeat("q", 4000))
			if (err != nil) != c.failed {
				t.Fatalf("err %v", err)
			}
			if !resp.Estimated || resp.NoUsage != c.noUsage || resp.Usage.InputTokens < 1000 || resp.Usage.OutputTokens != len(longWords)/charsPerToken {
				t.Fatalf("%+v", resp)
			}
		})
	}
	srv := httptest.NewServer(sse(chunk, `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, "[DONE]"))
	defer srv.Close()
	resp, err := streamOnce(t, chatProvider(srv.URL), "question")
	if err != nil || resp.Estimated || resp.NoUsage || resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("real figures: %+v %v", resp, err)
	}
}

// The Messages API says the input first and the output last: cut between
// them, the input stands and the output is estimated, whatever small count
// the first event gave.
func TestAMessagesStreamCutOffKeepsTheInputItReported(t *testing.T) {
	srv := httptest.NewServer(sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":50,"cache_read_input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+longWords+`"}}`,
	))
	defer srv.Close()
	resp, err := streamOnce(t, Provider{Endpoint: srv.URL}, "question")
	if err == nil {
		t.Fatal("a cut stream did not fail")
	}
	if !resp.Estimated || resp.NoUsage || resp.Usage.InputTokens != 50 || resp.Usage.CacheReadInputTokens != 10 || resp.Usage.OutputTokens != len(longWords)/charsPerToken {
		t.Fatalf("%+v", resp)
	}
}

// Bedrock reports usage in its last event.
func TestABedrockStreamCutOffIsEstimated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(event(map[string]string{":message-type": "event", ":event-type": "contentBlockDelta"}, `{"contentBlockIndex":0,"delta":{"text":"`+longWords+`"}}`))
	}))
	defer srv.Close()
	resp, err := streamOnce(t, Provider{Kind: KindBedrock, Endpoint: srv.URL, Auth: AuthHeader, AuthHeaderName: "x-gw"}, strings.Repeat("q", 4000))
	if err == nil {
		t.Fatal("a cut stream did not fail")
	}
	if !resp.Estimated || resp.Usage.InputTokens < 1000 || resp.Usage.OutputTokens != len(longWords)/charsPerToken {
		t.Fatalf("%+v", resp)
	}
}

// An answer refused before it began costs nothing to count.
func TestAnErrorBeforeTheAnswerIsNotEstimated(t *testing.T) {
	srv := httptest.NewServer(sse(`{"error":{"type":"invalid_request_error","message":"no"}}`))
	defer srv.Close()
	resp, err := streamOnce(t, chatProvider(srv.URL), "question")
	if err == nil || resp.Estimated || resp.Usage != (Usage{}) {
		t.Fatalf("%+v %v", resp, err)
	}
}

// A provider that never reports usage reaches the cap all the same, says
// so once, and the audit record says the figures are estimates.
func TestAProviderThatReportsNoUsageStillReachesTheCap(t *testing.T) {
	srv := httptest.NewServer(sse(`{"choices":[{"index":0,"delta":{"content":"`+longWords+`"},"finish_reason":"stop"}]}`, "[DONE]"))
	defer srv.Close()
	var logged lockedBuf
	var audited bytes.Buffer
	settings := Settings{Model: DefaultModel}
	s := New(Options{
		Client:   &Client{Provider: func() Provider { return chatProvider(srv.URL) }, Key: func() string { return "k" }},
		Settings: func() Settings { return settings },
		Audit:    audit.New(audit.Options{Out: &audited}),
		Logger:   slog.New(slog.NewTextHandler(&logged, nil)),
	})
	ask := func() error {
		t.Helper()
		_, err := s.Ask(Question{Key: Key{Cluster: "c", Session: "s"}, Text: "why", Tools: stubTools{}})
		s.Wait()
		return err
	}
	if err := ask(); err != nil {
		t.Fatal(err)
	}
	if err := ask(); err != nil {
		t.Fatal(err)
	}
	if got := s.Ledger().Now(context.Background()); got.DayUSD <= 0 {
		t.Fatalf("spend %+v", got)
	}
	if n := strings.Count(logged.String(), "reports no token use"); n != 1 {
		t.Fatalf("warned %d times: %s", n, logged.String())
	}
	if len(s.NoUsage()) != 1 || !strings.Contains(s.NoUsage()[0], DefaultModel) {
		t.Fatalf("no usage from %v", s.NoUsage())
	}
	if !strings.Contains(audited.String(), "estimated") {
		t.Fatalf("audit %s", audited.String())
	}
	settings.Caps = Caps{DailyUSD: 0.0001}
	var capped *ErrCap
	if err := ask(); !errors.As(err, &capped) {
		t.Fatalf("past the cap: %v", err)
	}
}

// Stopped part way, an answer still counts.
func TestAnAnswerStoppedPartWayCounts(t *testing.T) {
	sent := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"` + longWords + `"}}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		close(sent)
		<-r.Context().Done()
	}))
	defer srv.Close()
	s := New(Options{Client: &Client{Provider: func() Provider { return chatProvider(srv.URL) }, Key: func() string { return "k" }}})
	k := Key{Cluster: "c", Session: "s"}
	if _, err := s.Ask(Question{Key: k, Text: "why", Tools: stubTools{}}); err != nil {
		t.Fatal(err)
	}
	<-sent
	time.Sleep(20 * time.Millisecond)
	s.Stop(k)
	s.Wait()
	if got := s.Ledger().Now(context.Background()); got.DayUSD <= 0 {
		t.Fatalf("a stopped answer counted nothing: %+v", got)
	}
}
