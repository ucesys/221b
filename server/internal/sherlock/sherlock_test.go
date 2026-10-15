package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/fakeclaude"
)

func TestScrub(t *testing.T) {
	for _, c := range []struct{ in, gone, kept string }{
		{"connecting to postgres://orders:LogPlanted-9f3K2xQ@db.internal:5432/orders", "LogPlanted-9f3K2xQ", "@db.internal:5432"},
		{"with api_key=LogPlantedKey-77QzXwv4 and more", "LogPlantedKey-77QzXwv4", "api_key="},
		{"Authorization: Bearer abcdefghijklmnop.qrstuv", "abcdefghijklmnop", "Authorization: "},
		{"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl here", "eyJhbGciOiJIUzI1NiJ9", "here"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----\nafter", "MIIEow", "after"},
		{`{"password": "hunter2", "user": "bob"}`, "hunter2", `"user": "bob"`},
		{"AWS key AKIAABCDEFGHIJKLMNOP used", "AKIAABCDEFGHIJKLMNOP", "used"},
		{"github token ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghij", "github"},
		{"GET /api/v1/orders 200 3ms", "", "GET /api/v1/orders 200 3ms"},
	} {
		out := Scrub(c.in)
		if c.gone != "" && strings.Contains(out, c.gone) {
			t.Errorf("%q kept %q: %q", c.in, c.gone, out)
		}
		if !strings.Contains(out, c.kept) {
			t.Errorf("%q lost %q: %q", c.in, c.kept, out)
		}
	}
	// A JSON row: an environment variable named like a secret loses its
	// value; the rest stays readable.
	out := string(ScrubJSON([]byte(`{"name":"web","env":[{"name":"DB_PASSWORD","value":"hunter2"},{"name":"MODE","value":"prod"}],"secretsRead":true,"tokens":[{"name":"t"}]}`)))
	if strings.Contains(out, "hunter2") || !strings.Contains(out, `"value":"prod"`) || !strings.Contains(out, `"secretsRead":true`) || !strings.Contains(out, `"tokens":[{"name":"t"}]`) {
		t.Fatalf("ScrubJSON: %s", out)
	}
	if got := ScrubKnown("the key sk-ant-xyz123456 is here", []string{"sk-ant-xyz123456"}); strings.Contains(got, "sk-ant-xyz123456") {
		t.Fatalf("ScrubKnown: %s", got)
	}
}

func TestCite(t *testing.T) {
	known := Known{}
	known.Add(Ref{Kind: "Pod", Namespace: "web", Name: "api-0"}, Ref{Kind: "Node", Name: "worker-1"})
	text, refs := Cite("See [api-0](221b://pod/web/api-0) on [worker-1](221b://Node/worker-1), not [ghost](221b://Pod/x/ghost) or [bad](221b://Pod/) and [api again](221b://Pod/web/api-0).", known)
	want := "See [api-0](221b://Pod/web/api-0) on [worker-1](221b://Node/worker-1), not ghost or bad and [api again](221b://Pod/web/api-0)."
	if text != want {
		t.Fatalf("Cite:\n got %s\nwant %s", text, want)
	}
	if len(refs) != 2 || refs[0].Kind != "Pod" || refs[1].Name != "worker-1" {
		t.Fatalf("refs: %+v", refs)
	}
}

func TestCostAndCaps(t *testing.T) {
	m, ok := ModelOf("claude-opus-5-5")
	if !ok {
		t.Fatal("the default model is not listed")
	}
	// 1M in at $4, 100k cache read at a tenth, 50k cache write at 1.25×, 100k out at $20.
	if got := m.Cost(Usage{InputTokens: 1_000_000, CacheReadInputTokens: 100_000, CacheCreationInputTokens: 50_000, OutputTokens: 100_000}); got != 4+0.04+0.25+2 {
		t.Fatalf("cost %v", got)
	}
	h, _ := ModelOf("claude-haiku-5-5")
	if got := h.Cost(Usage{InputTokens: 200_000, OutputTokens: 1000}); got != 0.1+0.0025 {
		t.Fatalf("long-prompt price %v", got)
	}
	other, ok := ModelOf("claude-someday-9")
	if ok || other.Out < m.Out {
		t.Fatalf("an unknown model is priced at the dearest: %+v", other)
	}
	now := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	l := NewLedger(nil, func() time.Time { return now }, nil)
	caps := Caps{DailyUSD: 1, MonthlyUSD: 1.5}
	l.Add(context.Background(), 0.9, true)
	if err := caps.Check(l.Now(context.Background())); err != nil {
		t.Fatal(err)
	}
	l.Add(context.Background(), 0.2, false)
	var ce *ErrCap
	if err := caps.Check(l.Now(context.Background())); !errors.As(err, &ce) || ce.Which != "daily" || !strings.Contains(err.Error(), "tomorrow") {
		t.Fatalf("daily cap: %v", err)
	}
	now = now.Add(2 * time.Hour) // the next day, same month
	s := l.Now(context.Background())
	if s.DayUSD != 0 || s.MonthUSD < 1.09 || s.MonthAsked != 1 {
		t.Fatalf("rolled over: %+v", s)
	}
	l.Add(context.Background(), 0.5, true)
	if err := caps.Check(l.Now(context.Background())); !errors.As(err, &ce) || ce.Which != "monthly" {
		t.Fatalf("monthly cap: %v", err)
	}
	// Saved and read back.
	if err := l.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	l2 := NewLedger(l.backend, func() time.Time { return now }, nil)
	if got := l2.Now(context.Background()); got.MonthUSD != l.Now(context.Background()).MonthUSD {
		t.Fatalf("read back %+v", got)
	}
}

func TestClientStreams(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	var overloaded atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if overloaded.Add(-1) >= 0 {
			w.WriteHeader(529)
			w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }, Retries: 2, Backoff: time.Millisecond}
	req := Request{Model: DefaultModel, MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Tell me about pod web/api-0"}}}}}
	var tools []string
	overloaded.Store(1)
	resp, err := c.Stream(context.Background(), req, func(d Delta) {
		if d.Tool != "" {
			tools = append(tools, d.Tool)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(tools) != 1 || tools[0] != "get_pod" {
		t.Fatalf("response %+v tools %v", resp, tools)
	}
	var in map[string]string
	for _, b := range resp.Content {
		if b.Type == "tool_use" {
			_ = json.Unmarshal(b.Input, &in)
		}
	}
	if in["namespace"] != "web" || in["name"] != "api-0" || resp.Usage.InputTokens == 0 {
		t.Fatalf("tool input %v usage %+v", in, resp.Usage)
	}
	// A refused key is an API error, not retried.
	bad := &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-invalid-0123456789" }, Retries: 2, Backoff: time.Millisecond}
	_, err = bad.Stream(context.Background(), req, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || !strings.Contains(explain(err), "refused the API key") {
		t.Fatalf("bad key: %v", err)
	}
}

// A toolbox for the conversation tests.
type stubTools struct{}

func (stubTools) Defs() []ToolDef {
	return []ToolDef{{Name: "get_pod", Description: "a pod", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (stubTools) Label(name string, _ json.RawMessage) string { return "Read the pod" }
func (stubTools) Run(_ context.Context, name string, in json.RawMessage) ToolResult {
	return ToolResult{Text: `{"status":"CrashLoopBackOff","node":"worker-1","events":[{"reason":"BackOff","message":"Back-off restarting"}]}`, Refs: []Ref{{Kind: "Pod", Namespace: "web", Name: "api-0"}, {Kind: "Node", Name: "worker-1"}}}
}

func TestConversation(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(Options{Client: &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }}})
	k := Key{Session: "s1", Cluster: "local"}
	c, err := s.Ask(Question{Key: k, Text: "Why is web/api-0 failing?", Cluster: "prod", Tools: stubTools{}})
	if err != nil {
		t.Fatal(err)
	}
	s.Wait()
	v := c.View()
	if len(v.Messages) != 2 || v.Busy {
		t.Fatalf("view %+v", v)
	}
	a := v.Messages[1]
	if a.Status != "done" || !strings.Contains(a.Text, "CrashLoopBackOff") || !strings.Contains(a.Text, "[worker-1](221b://Node/worker-1)") || len(a.Refs) != 2 || a.Steps[0].Status != "done" {
		t.Fatalf("answer %+v", a)
	}
	// The second question carries the first exchange, as the API expects.
	if _, err := s.Ask(Question{Key: k, Text: "And pod web/api-0 again?", Tools: stubTools{}}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	reqs := fake.Requests()
	var last Request
	_ = json.Unmarshal(reqs[len(reqs)-1], &last)
	if len(last.Messages) != 7 || last.Messages[0].Role != "user" || last.System[0].CacheControl == nil {
		t.Fatalf("history sent: %d messages", len(last.Messages))
	}
	// Stopped part way, the history is repaired and the next answer works.
	if _, err := s.Ask(Question{Key: k, Text: "Answer slowly", Tools: stubTools{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(Question{Key: k, Text: "Again", Tools: stubTools{}}); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if !s.Stop(k) {
		t.Fatal("nothing to stop")
	}
	s.Wait()
	v = s.Conversation(k).View()
	if st := v.Messages[len(v.Messages)-1].Status; st != "stopped" {
		t.Fatalf("status %s", st)
	}
	if _, err := s.Ask(Question{Key: k, Text: "Tell me about pod web/api-0", Tools: stubTools{}}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	v = s.Conversation(k).View()
	if st := v.Messages[len(v.Messages)-1].Status; st != "done" {
		t.Fatalf("after a stop: %s %s", st, v.Messages[len(v.Messages)-1].Error)
	}
	// New chat starts over.
	s.Reset(k)
	if v := s.Conversation(k).View(); len(v.Messages) != 0 {
		t.Fatalf("after reset: %+v", v)
	}
	// An idle conversation, or one of a session that ended, is forgotten.
	s.Sweep(func(string) bool { return false })
	if s.Count() != 0 {
		t.Fatalf("after sweep: %d", s.Count())
	}
}

func TestKeys(t *testing.T) {
	k := NewKeys(nil, "", nil)
	if err := k.Set(context.Background(), "short", "me"); err == nil {
		t.Fatal("a short key was taken")
	}
	if err := k.Set(context.Background(), "sk-ant-a-good-key-0123456789", "me"); err != nil {
		t.Fatal(err)
	}
	if k.Key() != "sk-ant-a-good-key-0123456789" || !k.Info().Set || k.Info().SetBy != "me" {
		t.Fatalf("info %+v", k.Info())
	}
	b, _ := json.Marshal(k.Info())
	if strings.Contains(string(b), "good-key") {
		t.Fatalf("the info carries the key: %s", b)
	}
	fixed := NewKeys(nil, "sk-ant-from-the-chart-0123", nil)
	if err := fixed.Set(context.Background(), "sk-ant-a-good-key-0123456789", "me"); !errors.Is(err, ErrFixed) || fixed.Info().Source != "chart" {
		t.Fatalf("fixed: %v %+v", err, fixed.Info())
	}
}

func TestFollowUps(t *testing.T) {
	text, f := FollowUps("The cause is X.\n\nAsk next: Why is pod a/b pending? | Which nodes are full? | What changed? | One too many\n")
	if text != "The cause is X." || len(f) != 3 || f[0] != "Why is pod a/b pending?" {
		t.Fatalf("%q %q", text, f)
	}
	// Only a last line counts.
	if text, f := FollowUps("Ask next: a | b\n\nMore text."); f != nil || text != "Ask next: a | b\n\nMore text." {
		t.Fatalf("%q %q", text, f)
	}
}
