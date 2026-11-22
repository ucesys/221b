package sherlock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/fakeclaude"
)

// A model is sent only an effort it takes (AI-43).
func TestEffortForAModelAI43(t *testing.T) {
	for _, c := range []struct{ model, level, want string }{
		{"claude-opus-5-5", "xhigh", "xhigh"},
		{"claude-haiku-5-5", "max", "max"},
		{"claude-sonnet-5-5", "low", "low"},
		{"us.anthropic.claude-opus-4-6", "xhigh", "high"},
		{"claude-opus-4-5", "max", "high"},
		{"claude-haiku-4-5", "high", ""},
		{"gpt-5", "high", ""},
		{"claude-opus-5-5", "", ""},
		{"claude-opus-5-5", "extreme", ""},
	} {
		if got := EffortFor(c.model, c.level); got != c.want {
			t.Errorf("EffortFor(%s, %s) = %q, want %q", c.model, c.level, got, c.want)
		}
	}
	if !PerMessageEffort("claude-sonnet-5-5") || PerMessageEffort("claude-sonnet-5") || PerMessageEffort("claude-opus-4-8") {
		t.Fatal("PerMessageEffort")
	}
}

// capture is a provider that records the request and answers one text.
type capture struct {
	mu     sync.Mutex
	bodies []map[string]any
	beta   []string
	path   []string
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	c.mu.Lock()
	c.bodies = append(c.bodies, m)
	c.beta = append(c.beta, r.Header.Get("anthropic-beta"))
	c.path = append(c.path, r.URL.Path)
	c.mu.Unlock()
	if strings.Contains(r.URL.Path, "chat/completions") {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		return
	}
	w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
}

func (c *capture) last() (map[string]any, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[len(c.bodies)-1], c.beta[len(c.beta)-1]
}

func withEffortMsg(model, top string) Request {
	r := Request{Model: model, MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}, EffortMessage("high")}}
	if top != "" {
		r.OutputConfig = &OutputConfig{Effort: top}
	}
	return r
}

// The effort is sent as each kind of provider takes it (AI-43, AI-44): on
// the Claude API at the top level, or as an empty system message with its
// beta; folded into the top level where the change mid-conversation is
// not taken; as enable_thinking on an OpenAI-compatible server.
func TestEffortOnTheWireAI43(t *testing.T) {
	cp := &capture{}
	srv := httptest.NewServer(cp)
	defer srv.Close()
	ctx := context.Background()
	c := &Client{BaseURL: srv.URL, Key: func() string { return "k" }}
	if _, err := c.Stream(ctx, Request{Model: "claude-opus-5-5", MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}, OutputConfig: &OutputConfig{Effort: "xhigh"}}, nil); err != nil {
		t.Fatal(err)
	}
	body, beta := cp.last()
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "xhigh" || beta != "" {
		t.Fatalf("top-level effort: %v, beta %q", body["output_config"], beta)
	}
	// The change mid-conversation keeps its place, with the beta.
	if _, err := c.Stream(ctx, withEffortMsg("claude-sonnet-5-5", ""), nil); err != nil {
		t.Fatal(err)
	}
	body, beta = cp.last()
	msgs := body["messages"].([]any)
	sys := msgs[1].(map[string]any)
	if beta != BetaMidConversationEffort || sys["role"] != "system" || sys["output_config"].(map[string]any)["effort"] != "high" || len(sys["content"].([]any)) != 0 || body["output_config"] != nil {
		t.Fatalf("mid-conversation effort: %v beta %q", body, beta)
	}
	// A model without the beta gets the level in force at the top level,
	// the nearest it takes; one without effort gets none.
	if _, err := c.Stream(ctx, withEffortMsg("claude-opus-4-6", "low"), nil); err != nil {
		t.Fatal(err)
	}
	body, beta = cp.last()
	if len(body["messages"].([]any)) != 1 || body["output_config"].(map[string]any)["effort"] != "high" || beta != "" {
		t.Fatalf("folded: %v beta %q", body, beta)
	}
	if _, err := c.Stream(ctx, withEffortMsg("claude-haiku-4-5", "low"), nil); err != nil {
		t.Fatal(err)
	}
	if body, _ = cp.last(); body["output_config"] != nil || len(body["messages"].([]any)) != 1 {
		t.Fatalf("no effort: %v", body)
	}
	// An OpenAI-compatible server: thinking off at low, on above.
	oc := &Client{Key: func() string { return "k" }, Provider: func() Provider { return Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/v1"} }}
	for level, on := range map[string]bool{"low": false, "high": true} {
		if _, err := oc.Stream(ctx, Request{Model: "qwen", MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}, OutputConfig: &OutputConfig{Effort: level}}, nil); err != nil {
			t.Fatal(err)
		}
		body, _ = cp.last()
		k, _ := body["chat_template_kwargs"].(map[string]any)
		if k["enable_thinking"] != on || len(body["messages"].([]any)) != 1 {
			t.Fatalf("%s: %v", level, body)
		}
	}
	if _, err := oc.Stream(ctx, Request{Model: "qwen", MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if body, _ = cp.last(); body["chat_template_kwargs"] != nil {
		t.Fatalf("no effort, no kwargs: %v", body)
	}
	// OpenAI's own API is sent nothing it does not know.
	if _, ok := openAIThinking("https://api.openai.com/v1", Request{OutputConfig: &OutputConfig{Effort: "high"}}); ok {
		t.Fatal("api.openai.com was sent enable_thinking")
	}
}

// The classifier's request: vLLM's constrained choice, thinking off, five
// tokens, temperature 0, not streamed (AI-45).
func TestChooseOneAI45(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "wrong", 400)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"high"}}],"usage":{"prompt_tokens":310,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	c := &Client{Key: func() string { return "k" }, Provider: func() Provider { return Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/v1"} }}
	word, u, err := c.ChooseOne(context.Background(), "qwen3.8-27b", "choose", "prod is down", []string{"low", "medium", "high"})
	if err != nil || word != "high" || u.InputTokens != 310 || u.OutputTokens != 1 {
		t.Fatalf("%q %+v %v", word, u, err)
	}
	so, _ := got["structured_outputs"].(map[string]any)
	k, _ := got["chat_template_kwargs"].(map[string]any)
	if len(so["choice"].([]any)) != 3 || k["enable_thinking"] != false || got["max_tokens"] != float64(5) || got["temperature"] != float64(0) || got["stream"] != nil {
		t.Fatalf("request: %v", got)
	}
	// Only an OpenAI-compatible provider classifies.
	if _, _, err := (&Client{BaseURL: srv.URL}).ChooseOne(context.Background(), "m", "s", "t", nil); err == nil {
		t.Fatal("an Anthropic provider classified")
	}
}

// fixedChooser picks by the message's words, as a classifier would.
type fixedChooser struct {
	mu  sync.Mutex
	got []ChooseInput
}

func (f *fixedChooser) Choose(_ context.Context, in ChooseInput) Choice {
	f.mu.Lock()
	f.got = append(f.got, in)
	f.mu.Unlock()
	if strings.Contains(in.Text, "outage") {
		ch := Choice{Model: in.Model, Effort: "high", By: "classifier", Why: "the classifier said high", CostUSD: 0.0001, Provider: "a5000"}
		if in.Model != "claude-opus-5-5" && !in.MovedUp {
			ch.Model, ch.MovedUpFrom = "claude-opus-5-5", in.Model
		}
		return ch
	}
	m := in.Model
	if m == "" {
		m = "claude-haiku-5-5"
	}
	return Choice{Model: m, Effort: "low", By: "classifier", Why: "the classifier said low"}
}

// Under Auto a conversation's effort is picked for each message and sent
// as an empty system message after it, only when it changes, so the
// prompt cache holds; the model is picked at the start and moved up once;
// each answer keeps the pick, why and what it cost (AI-44, AI-48).
func TestAutoInAConversationAI44(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ch := &fixedChooser{}
	s := New(Options{Client: &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }},
		Agent: func(id string, q Question) (Agent, error) {
			return Agent{ID: SherlockID, Name: "Sherlock", Model: "claude-sonnet-5-5", Auto: ch}, nil
		}})
	k := Key{Session: "s1", Cluster: "local"}
	ask := func(text string) *Msg {
		t.Helper()
		c, err := s.Ask(Question{Key: k, Text: text, Cluster: "prod", Tools: stubTools{}})
		if err != nil {
			t.Fatal(err)
		}
		s.Wait()
		v := c.View()
		a := v.Messages[len(v.Messages)-1]
		if a.Status != "done" {
			t.Fatalf("%q: %s %s", text, a.Status, a.Error)
		}
		return a
	}
	a := ask("Is pod web/api-0 ready?")
	if a.Model != "claude-haiku-5-5" || a.Effort != "low" || a.PickBy != "classifier" || a.PickWhy == "" {
		t.Fatalf("first answer: %+v", a)
	}
	var last Request
	reqs := fake.Requests()
	_ = json.Unmarshal(reqs[len(reqs)-1], &last)
	if last.Model != "claude-haiku-5-5" || last.OutputConfig != nil || !IsEffortMessage(last.Messages[1]) || last.Messages[1].OutputConfig.Effort != "low" {
		t.Fatalf("first request: %+v", last)
	}
	// The same effort again: no new system message.
	ask("Is pod web/api-0 ready again?")
	reqs = fake.Requests()
	_ = json.Unmarshal(reqs[len(reqs)-1], &last)
	n := 0
	for _, m := range last.Messages {
		if IsEffortMessage(m) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("effort messages after the same level: %d", n)
	}
	// An outage: high, moved up to Opus once, with what the pick cost.
	a = ask("There is an outage on web/api-0")
	if a.Model != "claude-opus-5-5" || a.Effort != "high" || a.MovedUpFrom != "claude-haiku-5-5" || a.PickCostUSD != 0.0001 {
		t.Fatalf("moved up: %+v", a)
	}
	reqs = fake.Requests()
	_ = json.Unmarshal(reqs[len(reqs)-1], &last)
	if last.Model != "claude-opus-5-5" || EffortInForce(last) != "high" {
		t.Fatalf("after the move: %s %s", last.Model, EffortInForce(last))
	}
	ask("Another outage on web/api-0")
	ch.mu.Lock()
	in := ch.got[len(ch.got)-1]
	ch.mu.Unlock()
	if in.Model != "claude-opus-5-5" || !in.MovedUp {
		t.Fatalf("the chooser was not told of the move: %+v", in)
	}
}

// A fixed effort goes at the top level of every request (AI-43).
func TestAFixedEffortAI43(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(Options{Client: &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }},
		Agent: func(id string, q Question) (Agent, error) {
			return Agent{ID: SherlockID, Name: "Sherlock", Model: "claude-opus-5-5", Effort: "high"}, nil
		}})
	c, err := s.Ask(Question{Key: Key{Session: "s", Cluster: "local"}, Text: "Is pod web/api-0 ready?", Tools: stubTools{}})
	if err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if a := c.View().Messages[1]; a.Effort != "high" || a.PickBy != "" || a.Status != "done" {
		t.Fatalf("%+v", a)
	}
	var last Request
	reqs := fake.Requests()
	_ = json.Unmarshal(reqs[len(reqs)-1], &last)
	if last.OutputConfig == nil || last.OutputConfig.Effort != "high" {
		t.Fatalf("%+v", last.OutputConfig)
	}
	// RunAgent: the same.
	res := s.RunAgent(context.Background(), Agent{ID: SherlockID, Name: "Sherlock", Model: "claude-opus-5-5", Effort: "low"}, Question{Key: Key{Session: "t", Cluster: "local"}, Tools: stubTools{}}, "Is pod web/api-0 ready?")
	if res.Err != nil || res.Effort != "low" {
		t.Fatalf("%+v", res)
	}
}

// A kept conversation's history carries the effort changes where they
// were sent (AI-44).
func TestKeptHistoryKeepsTheEffortAI44(t *testing.T) {
	msgs := []*Msg{
		{Role: "user", Text: "a"}, {Role: "sherlock", Text: "x", Effort: "low", PickBy: "classifier"},
		{Role: "user", Text: "b"}, {Role: "sherlock", Text: "y", Effort: "low", PickBy: "classifier"},
		{Role: "user", Text: "c"}, {Role: "sherlock", Text: "z", Effort: "high", PickBy: "classifier"},
		{Role: "user", Text: "d"}, {Role: "sherlock", Text: "w", Effort: "high"},
	}
	h, starts := keptHistory(msgs, "")
	var roles []string
	for _, m := range h {
		if IsEffortMessage(m) {
			roles = append(roles, m.OutputConfig.Effort)
		} else {
			roles = append(roles, m.Role)
		}
	}
	if strings.Join(roles, ",") != "user,low,assistant,user,assistant,user,high,assistant,user,assistant" || len(starts) != 4 {
		t.Fatalf("%v %v", roles, starts)
	}
}
