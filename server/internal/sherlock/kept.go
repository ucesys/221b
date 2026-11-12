package sherlock

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/scrub"
)

// --- A case's conversation (Phase 12, wave 6f, CF-47, ADR-134) ---
//
// A conversation of a session lives in memory for the session. A kept
// conversation is another's: a case's, kept with the case by a Keeper,
// shared by everyone who works the case. Each question in it is asked as
// its asker (their toolbox, their rights) and names them; the
// conversation is loaded when first used, saved after each question and
// each answer, and survives a restart and a change of leader. The history
// sent to the model is rebuilt from the messages for every question, the
// questions and answers as text, with the context the asker gives (the
// case, as data) at its start: one person's tool results never reach
// another's question except through the answers everyone sees.

// Keeper keeps conversations outside the service.
type Keeper interface {
	// Keeps says whether a key's conversation is kept.
	Keeps(k Key) bool
	// Load reads a kept conversation's messages; none when there are none.
	Load(ctx context.Context, k Key) ([]Msg, error)
	// Save keeps its messages. The keeper may refuse: a case closed keeps
	// no conversation.
	Save(ctx context.Context, k Key, msgs []Msg) error
}

// Bounds of a kept conversation: the messages it shows and keeps (twenty
// questions with their answers), and the characters of each one's text.
const (
	KeptMessages = 40
	KeptText     = 8000
	keptSteps    = 30
)

// SetKeeper gives the service the keeper of the kept conversations.
func (s *Service) SetKeeper(k Keeper) {
	s.mu.Lock()
	s.opts.Keeper = k
	s.mu.Unlock()
}

// AddKeeper adds a keeper of other conversations beside those kept
// already: a case's and a proposed change's (Phase 12, CF-61) each have
// their own, each keeping its own sessions.
func (s *Service) AddKeeper(k Keeper) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch cur := s.opts.Keeper.(type) {
	case nil:
		s.opts.Keeper = k
	case Keepers:
		s.opts.Keeper = append(cur[:len(cur):len(cur)], k)
	default:
		s.opts.Keeper = Keepers{cur, k}
	}
}

// Keepers are several keepers: a key's conversation is kept by the first
// that keeps it.
type Keepers []Keeper

func (ks Keepers) of(k Key) Keeper {
	for _, kp := range ks {
		if kp != nil && kp.Keeps(k) {
			return kp
		}
	}
	return nil
}

// Keeps says whether one of them keeps a key's conversation.
func (ks Keepers) Keeps(k Key) bool { return ks.of(k) != nil }

// Load reads a key's conversation from the keeper that keeps it.
func (ks Keepers) Load(ctx context.Context, k Key) ([]Msg, error) {
	if kp := ks.of(k); kp != nil {
		return kp.Load(ctx, k)
	}
	return nil, nil
}

// Save keeps a key's conversation with the keeper that keeps it.
func (ks Keepers) Save(ctx context.Context, k Key, msgs []Msg) error {
	if kp := ks.of(k); kp != nil {
		return kp.Save(ctx, k, msgs)
	}
	return nil
}

// keeper is the keeper of a key's conversation, or nil when it is not kept.
func (s *Service) keeperOf(k Key) Keeper {
	s.mu.Lock()
	kp := s.opts.Keeper
	s.mu.Unlock()
	if kp == nil || !kp.Keeps(k) {
		return nil
	}
	return kp
}

// loadKept fills a new conversation from its keeper. An answer that was
// under way when the console stopped reads as stopped.
func (s *Service) loadKept(c *Conversation, kp Keeper, k Key) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msgs, err := kp.Load(ctx, k)
	if err != nil {
		s.opts.Logger.Warn("a kept conversation could not be read", "conversation", k.Session, "err", err)
	}
	for i := range msgs {
		m := msgs[i]
		switch m.Status {
		case "thinking", "answering":
			m.Status, m.Error = "stopped", "The console restarted before the answer was done."
		}
		c.msgs = append(c.msgs, &m)
		c.seq = max(c.seq, m.ID)
	}
}

// keep saves a kept conversation as it is now.
func (s *Service) keep(c *Conversation, k Key) {
	kp := s.keeperOf(k)
	if kp == nil {
		return
	}
	c.mu.Lock()
	if c.forgotten {
		c.mu.Unlock()
		return
	}
	msgs := keptMsgs(c.msgs)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := kp.Save(ctx, k, msgs); err != nil {
		s.opts.Logger.Warn("a kept conversation could not be saved", "conversation", k.Session, "err", err)
	}
}

// keptMsgs are a conversation's messages as they are kept: the last
// KeptMessages, each text clipped and scrubbed (X-01), without what only
// the asking window needs (the view it changed, the note, the parts
// handed to other agents).
func keptMsgs(in []*Msg) []Msg {
	if n := len(in) - KeptMessages; n > 0 {
		in = in[n:]
	}
	out := make([]Msg, 0, len(in))
	for _, m := range in {
		cp := *m
		cp.Text = clipText(scrub.Text(cp.Text), KeptText)
		cp.Error = clipText(scrub.Text(cp.Error), 500)
		cp.Note, cp.View, cp.Hands = "", nil, nil
		cp.Steps = append([]Step(nil), m.Steps...)
		if len(cp.Steps) > keptSteps {
			cp.Steps = cp.Steps[:keptSteps]
		}
		cp.Refs = append([]Ref(nil), m.Refs...)
		out = append(out, cp)
	}
	return out
}

// clipText cuts a text to at most n bytes, on a character's boundary.
func clipText(t string, n int) string {
	if len(t) <= n {
		return t
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(t[cut]) {
		cut--
	}
	return t[:cut] + "…"
}

// keptHistory is what is sent of a kept conversation before a new
// question: its questions and answers as text, the context at the start
// of the first question, and where each question starts.
func keptHistory(msgs []*Msg, context string) ([]Message, []int) {
	var history []Message
	var starts []int
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role != "user" {
			continue
		}
		answer := ""
		if i+1 < len(msgs) && msgs[i+1].Role == "sherlock" {
			a := msgs[i+1]
			answer = strings.TrimSpace(a.Text)
			if answer == "" {
				answer = "(This answer was " + firstNonEmpty(a.Status, "not given") + " before it was complete.)"
			}
			i++
		}
		if answer == "" {
			// A question with no answer kept, as after a restart: left out.
			continue
		}
		content := []Block{{Type: "text", Text: m.Text}}
		if len(history) == 0 && context != "" {
			content = append([]Block{{Type: "text", Text: context}}, content...)
		}
		starts = append(starts, len(history))
		history = append(history, Message{Role: "user", Content: content}, Message{Role: "assistant", Content: []Block{{Type: "text", Text: answer}}})
	}
	return history, starts
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ForgetSession drops the conversations of a session, whatever their
// cluster, stopping any answer in progress: a case closed (CF-47). It
// says how many it dropped. A kept conversation's record is the keeper's
// to delete.
func (s *Service) ForgetSession(session string) int {
	s.mu.Lock()
	var gone []*Conversation
	for k, c := range s.convs {
		if k.Session == session {
			gone = append(gone, c)
			delete(s.convs, k)
		}
	}
	s.mu.Unlock()
	for _, c := range gone {
		c.mu.Lock()
		if c.cancel != nil {
			c.cancel()
		}
		c.forgotten = true
		c.msgs, c.history, c.starts, c.known = nil, nil, nil, Known{}
		c.changedLocked()
		c.mu.Unlock()
	}
	return len(gone)
}

// --- end of a case's conversation.
