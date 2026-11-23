package sherlock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/fakeclaude"
)

// marks lists where a request's cache_control markers are: "tools.N",
// "system.N" and "messages.M.N".
func marks(t *testing.T, body []byte) []string {
	t.Helper()
	var r Request
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, d := range r.Tools {
		if d.CacheControl != nil {
			out = append(out, "tools."+itoa(i))
		}
	}
	for i, b := range r.System {
		if b.CacheControl != nil {
			out = append(out, "system."+itoa(i))
		}
	}
	for m, msg := range r.Messages {
		for i, b := range msg.Content {
			if b.CacheControl != nil {
				out = append(out, "messages."+itoa(m)+"."+itoa(i))
			}
		}
	}
	return out
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// unmarked is a request's prefix with its markers taken off, to compare
// what two requests send: the tools, the system blocks and the first n
// messages.
func unmarked(t *testing.T, body []byte, n int) string {
	t.Helper()
	var r Request
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	for i := range r.Tools {
		r.Tools[i].CacheControl = nil
	}
	for i := range r.System {
		r.System[i].CacheControl = nil
	}
	if n > len(r.Messages) {
		t.Fatalf("%d messages, not %d", len(r.Messages), n)
	}
	msgs := r.Messages[:n]
	for m := range msgs {
		for i := range msgs[m].Content {
			msgs[m].Content[i].CacheControl = nil
		}
	}
	b, _ := json.Marshal(struct {
		Tools  []ToolDef
		System []Block
		Msgs   []Message
	}{r.Tools, r.System, msgs})
	return string(b)
}

// The conversation's history is cached (the measurement of November 23,
// 2026, F5; AI-50): every request marks its newest message, and from the
// second turn the last message before the question too, within the four
// markers the API takes with the system blocks'; the time goes with the
// question, so the next turn sends the same prefix the last request of
// the turn before wrote to the cache, and the fake API takes them all.
func TestTheHistoryIsCachedAI50(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }}
	s := New(Options{Client: client})
	k := Key{Session: "s1", Cluster: "local"}
	for _, text := range []string{"Why is web/api-0 failing?", "And pod web/api-0 again?"} {
		if _, err := s.Ask(Question{Key: k, Text: text, Cluster: "prod", Tools: stubTools{}}); err != nil {
			t.Fatal(err)
		}
		s.Wait()
	}
	v := s.Conversation(k).View()
	if len(v.Messages) != 4 || v.Messages[1].Status != "done" || v.Messages[3].Status != "done" || v.Messages[3].Error != "" {
		t.Fatalf("the answers: %+v", v.Messages)
	}
	reqs := fake.Requests()
	if len(reqs) < 4 {
		t.Fatalf("%d requests: the questions called no tool", len(reqs))
	}
	second := -1 // the second question's first request
	for i, body := range reqs {
		var r Request
		_ = json.Unmarshal(body, &r)
		m := marks(t, body)
		if len(m) > MaxBreakpoints {
			t.Fatalf("request %d has %d markers: %q", i, len(m), m)
		}
		newest := len(r.Messages) - 1
		if !strings.HasPrefix(m[len(m)-1], "messages."+itoa(newest)+".") {
			t.Errorf("request %d: the newest message carries no marker: %q", i, m)
		}
		for _, b := range r.System {
			if strings.HasPrefix(b.Text, "It is ") {
				t.Errorf("request %d: the time is in the system blocks", i)
			}
		}
		if second < 0 && strings.Contains(string(body), "And pod web/api-0 again?") {
			second = i
		}
	}
	if second < 1 {
		t.Fatal("the second question was not sent")
	}
	var r2 Request
	_ = json.Unmarshal(reqs[second], &r2)
	q := len(r2.Messages) - 1
	if q < 2 || !strings.HasPrefix(r2.Messages[q].Content[len(r2.Messages[q].Content)-1].Text, "It is ") {
		t.Fatalf("the question does not end with the time: %+v", r2.Messages[q])
	}
	m := marks(t, reqs[second])
	if len(m) != MaxBreakpoints || !strings.HasPrefix(m[len(m)-2], "messages."+itoa(q-1)+".") {
		t.Fatalf("the second question's first request marks %q, want the system blocks, the message before the question and the question", m)
	}
	// The last request of the first answer wrote everything it sent to the
	// cache; the second question's first request sends the same, before
	// the first answer's last reply.
	var last Request
	_ = json.Unmarshal(reqs[second-1], &last)
	n := len(last.Messages)
	if a, b := unmarked(t, reqs[second-1], n), unmarked(t, reqs[second], n); a != b {
		t.Fatalf("the prefix changed between the turns:\n%s\n%s", a, b)
	}
}

// The fake API refuses what the Messages API refuses: a fifth marker, and
// one on a thinking block.
func TestTheFakeHoldsTheCacheRulesAI50(t *testing.T) {
	srv := httptest.NewServer(fakeclaude.New())
	defer srv.Close()
	post := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", bytes.NewBufferString(body))
		req.Header.Set("x-api-key", "sk-ant-test")
		req.Header.Set("anthropic-version", APIVersion)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cc := `"cache_control":{"type":"ephemeral"}`
	five := `{"model":"m","max_tokens":10,"stream":true,"system":[{"type":"text","text":"a",` + cc + `},{"type":"text","text":"b",` + cc + `}],"tools":[{"name":"t","description":"d","input_schema":{"type":"object"},` + cc + `}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"q",` + cc + `}]},{"role":"assistant","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":"q2",` + cc + `}]}]}`
	if got := post(five); got != 400 {
		t.Errorf("five markers: %d", got)
	}
	thinking := `{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"q"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"fakesig-1",` + cc + `}]},{"role":"user","content":[{"type":"text","text":"q2"}]}]}`
	if got := post(thinking); got != 400 {
		t.Errorf("a marker on a thinking block: %d", got)
	}
	four := strings.Replace(five, `{"type":"text","text":"q",`+cc+`}`, `{"type":"text","text":"q"}`, 1)
	if got := post(four); got != 200 {
		t.Errorf("four markers: %d", got)
	}
}

// A hand-over's rounds mark their newest message too, within the four.
func TestWithHistoryCacheKeepsToFour(t *testing.T) {
	sys := []Block{{Type: "text", Text: "a", CacheControl: &CacheControl{Type: "ephemeral"}}, {Type: "text", Text: "b", CacheControl: &CacheControl{Type: "ephemeral"}}}
	tools := []ToolDef{{Name: "t", CacheControl: &CacheControl{Type: "ephemeral"}}}
	history := []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "q"}}},
		{Role: "assistant", Content: []Block{{Type: "text", Text: "x"}, {Type: "thinking", Signature: "s"}}},
		{Role: "user", Content: []Block{{Type: "text", Text: "q2"}}},
		EffortMessage("low"),
	}
	r := withHistoryCache(Request{System: sys, Tools: tools, Messages: history}, 2)
	if r.Messages[2].Content[0].CacheControl == nil || r.Messages[1].Content[0].CacheControl != nil || history[2].Content[0].CacheControl != nil {
		t.Fatalf("with the tools marked, only the newest message: %+v (the history: %+v)", r.Messages, history)
	}
	r = withHistoryCache(Request{System: sys, Messages: history}, 2)
	if r.Messages[2].Content[0].CacheControl == nil || r.Messages[1].Content[0].CacheControl == nil || r.Messages[1].Content[1].CacheControl != nil {
		t.Fatalf("the question and the text before the thinking block: %+v", r.Messages)
	}
	full := append([]Block(nil), sys...)
	full = append(full, Block{Type: "text", Text: "c", CacheControl: &CacheControl{Type: "ephemeral"}})
	r = withHistoryCache(Request{System: full, Tools: tools, Messages: history}, 2)
	if r.Messages[2].Content[0].CacheControl != nil {
		t.Fatal("a fifth marker")
	}
}
