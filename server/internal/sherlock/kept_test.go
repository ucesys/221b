package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/fakeclaude"
)

// --- A case's conversation (wave 6f, CF-47) ---

// memKeeper keeps the conversations of the sessions "case:…" in memory,
// as a case's object would.
type memKeeper struct {
	mu     sync.Mutex
	saved  map[string][]Msg
	refuse bool
	saves  int
}

func (m *memKeeper) Keeps(k Key) bool { return strings.HasPrefix(k.Session, "case:") }

func (m *memKeeper) Load(_ context.Context, k Key) ([]Msg, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Msg(nil), m.saved[k.Session]...), nil
}

func (m *memKeeper) Save(_ context.Context, k Key, msgs []Msg) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refuse {
		return errors.New("the case is closed")
	}
	m.saves++
	m.saved[k.Session] = msgs
	return nil
}

func (m *memKeeper) get(session string) []Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved[session]
}

// A case's conversation is one for everyone who works the case: each
// question names its asker, the case's context goes before the first
// question, the history sent is the questions and answers as text (no
// one's tool results), it is saved after each question and answer, read
// back after a restart, kept when sessions end, and dropped from memory
// when the case closes (CF-47).
func TestACaseConversationIsSharedAttributedAndKeptCF47(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	kp := &memKeeper{saved: map[string][]Msg{}}
	client := &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }}
	s := New(Options{Client: client, Keeper: kp})
	k := Key{Session: "case:C-12", Cluster: "local"}
	ctxText := "<case-context>\nThis conversation is about the case C-12.\n</case-context>"
	pat := audit.Who{Name: "Pat", Email: "pat@corp.example"}
	ann := audit.Who{Email: "ann@corp.example"}
	if _, err := s.Ask(Question{Key: k, Text: "Why is web/api-0 failing?", Cluster: "prod", Tools: stubTools{}, Who: pat, Context: ctxText}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	reqs := fake.Requests()
	var first Request
	_ = json.Unmarshal(reqs[0], &first)
	if len(first.Messages) != 1 || len(first.Messages[0].Content) != 2 || first.Messages[0].Content[0].Text != ctxText || first.Messages[0].Content[1].Text != "Why is web/api-0 failing?" {
		t.Fatalf("the first question: %+v", first.Messages)
	}
	// Ann, another person, asks into the same conversation.
	if _, err := s.Ask(Question{Key: k, Text: "And pod web/api-0 again?", Cluster: "prod", Tools: stubTools{}, Who: ann, Context: ctxText}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	v := s.Conversation(k).View()
	if len(v.Messages) != 4 || v.Messages[0].Asker != "Pat" || v.Messages[2].Asker != "ann@corp.example" || v.Messages[3].Status != "done" {
		t.Fatalf("view %+v", v.Messages)
	}
	reqs = fake.Requests()
	var last Request
	_ = json.Unmarshal(reqs[len(reqs)-2], &last)
	// Her question's first request: the first exchange as text, the
	// context at its start, then hers; none of Pat's tool calls.
	if len(last.Messages) != 3 || last.Messages[0].Content[0].Text != ctxText || last.Messages[1].Role != "assistant" || !strings.Contains(last.Messages[1].Content[0].Text, "CrashLoopBackOff") {
		t.Fatalf("history sent: %+v", last.Messages)
	}
	for _, m := range last.Messages {
		for _, b := range m.Content {
			if b.Type != "text" {
				t.Errorf("a %s block was sent of another question", b.Type)
			}
		}
	}
	saved := kp.get("case:C-12")
	if len(saved) != 4 || saved[2].Asker != "ann@corp.example" || saved[3].Status != "done" || kp.saves < 4 {
		t.Fatalf("saved %d messages in %d saves: %+v", len(saved), kp.saves, saved)
	}
	// Sessions ending leave it; a session's own conversation goes.
	mine := Key{Session: "s1", Cluster: "local"}
	if _, err := s.Ask(Question{Key: mine, Text: "Why is web/api-0 failing?", Tools: stubTools{}, Who: pat}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if m := s.Conversation(mine).View().Messages; m[0].Asker != "" {
		t.Errorf("a session's question names its asker: %+v", m[0])
	}
	s.Sweep(func(string) bool { return false })
	if s.Count() != 1 {
		t.Fatalf("after the sweep %d conversations", s.Count())
	}
	// A restart reads it back, and the next question carries it with the
	// context first.
	kp.mu.Lock()
	kp.saved["case:C-12"][3].Status = "answering"
	kp.mu.Unlock()
	s2 := New(Options{Client: client, Keeper: kp})
	v2 := s2.Conversation(k).View()
	if len(v2.Messages) != 4 || v2.Messages[3].Status != "stopped" || v2.Messages[3].Error == "" {
		t.Fatalf("read back %+v", v2.Messages)
	}
	if _, err := s2.Ask(Question{Key: k, Text: "Is node worker-1 ready?", Cluster: "prod", Tools: stubTools{}, Who: pat, Context: ctxText}); err != nil {
		t.Fatal(err)
	}
	s2.Wait()
	reqs = fake.Requests()
	var after Request
	_ = json.Unmarshal(reqs[len(reqs)-2], &after)
	if len(after.Messages) != 5 || after.Messages[0].Content[0].Text != ctxText {
		t.Fatalf("after a restart: %+v", after.Messages)
	}
	if v := s2.Conversation(k).View(); len(v.Messages) != 6 || v.Messages[5].ID != 6 {
		t.Fatalf("numbered on: %+v", v.Messages)
	}
	// The case closes: dropped from memory, nothing more saved.
	conv := s2.Conversation(k)
	if n := s2.ForgetSession("case:C-12"); n != 1 || s2.Count() != 0 {
		t.Fatalf("forgot %d, %d left", n, s2.Count())
	}
	before := kp.saves
	s2.keep(conv, k)
	if kp.saves != before || len(conv.View().Messages) != 0 {
		t.Errorf("a forgotten conversation was saved")
	}
	// Reopened: what the keeper has (nothing, once the case deleted it).
	kp.mu.Lock()
	kp.saved = map[string][]Msg{}
	kp.mu.Unlock()
	if v := s2.Conversation(k).View(); len(v.Messages) != 0 {
		t.Errorf("reopened with %d messages", len(v.Messages))
	}
}

// What is kept of a case's conversation is bounded: its last
// KeptMessages, each text clipped and scrubbed of what looks like a
// secret, without the asking window's parts (CF-47, X-01).
func TestAKeptConversationIsBoundedCF47(t *testing.T) {
	var in []*Msg
	for i := 0; i < KeptMessages+6; i++ {
		in = append(in, &Msg{ID: i + 1, Role: "user", Text: "with api_key=LogPlantedKey-77QzXwv4 " + strings.Repeat("é", KeptText), Note: "note", View: []ViewChange{{Label: "x"}}, Hands: []*Hand{{ID: 1}}})
	}
	out := keptMsgs(in)
	if len(out) != KeptMessages || out[0].ID != 7 {
		t.Fatalf("%d kept, the first %d", len(out), out[0].ID)
	}
	for _, m := range out {
		if len(m.Text) > KeptText+len("…") || strings.Contains(m.Text, "LogPlantedKey-77QzXwv4") || m.Note != "" || m.View != nil || m.Hands != nil {
			t.Fatalf("kept %d bytes: %.40q…", len(m.Text), m.Text)
		}
	}
	// A kept conversation's own view is bounded the same way.
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	kp := &memKeeper{saved: map[string][]Msg{}}
	s := New(Options{Client: &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }}, Keeper: kp})
	k := Key{Session: "case:C-3", Cluster: "local"}
	for i := 0; i < KeptMessages/2+3; i++ {
		if _, err := s.Ask(Question{Key: k, Text: "Is node worker-1 ready?", Tools: stubTools{}, Who: audit.Who{Name: "Pat"}}); err != nil {
			t.Fatal(err)
		}
		s.Wait()
	}
	if v := s.Conversation(k).View(); len(v.Messages) != KeptMessages || len(kp.get("case:C-3")) != KeptMessages {
		t.Fatalf("%d shown, %d kept", len(v.Messages), len(kp.get("case:C-3")))
	}
	// A keeper that refuses (the case closed) leaves the answer shown.
	kp.refuse = true
	if _, err := s.Ask(Question{Key: k, Text: "Is node worker-1 ready?", Tools: stubTools{}, Who: audit.Who{Name: "Pat"}}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if v := s.Conversation(k).View(); v.Messages[len(v.Messages)-1].Status != "done" {
		t.Errorf("answer %+v", v.Messages[len(v.Messages)-1])
	}
}

// prefixKeeper keeps the sessions that start with its prefix.
type prefixKeeper struct {
	memKeeper
	prefix string
}

func (p *prefixKeeper) Keeps(k Key) bool { return strings.HasPrefix(k.Session, p.prefix) }

// A case's and a proposed change's conversations each have their own
// keeper (CF-61): a key goes to the keeper that keeps it, and to none
// when nobody does.
func TestKeepersEachKeepTheirOwnCF61(t *testing.T) {
	ctx := context.Background()
	cases, proposals := &prefixKeeper{memKeeper{saved: map[string][]Msg{}}, "case:"}, &prefixKeeper{memKeeper{saved: map[string][]Msg{}}, "proposal:"}
	s := New(Options{})
	s.AddKeeper(cases)
	s.AddKeeper(proposals)
	for _, session := range []string{"case:C-1", "proposal:PC-1"} {
		kp := s.keeperOf(Key{Session: session})
		if kp == nil {
			t.Fatalf("%s is not kept", session)
		}
		if err := kp.Save(ctx, Key{Session: session}, []Msg{{ID: 1, Role: "user", Text: session}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := cases.get("case:C-1"); len(got) != 1 || proposals.get("case:C-1") != nil {
		t.Errorf("the case's: %+v", got)
	}
	if got := proposals.get("proposal:PC-1"); len(got) != 1 || cases.get("proposal:PC-1") != nil {
		t.Errorf("the proposal's: %+v", got)
	}
	if s.keeperOf(Key{Session: "s-123"}) != nil {
		t.Error("a person's own conversation is kept")
	}
}
