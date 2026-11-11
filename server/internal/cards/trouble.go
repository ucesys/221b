package cards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// --- A proposed change's Sherlock and Watson (Phase 12, CF-61, CF-62,
// ADR-136) ---
//
// The proposed change's drawer has Sherlock and Watson beside it, as a
// case's has (CF-46 to CF-49). A proposal may resolve several cases, so it
// has its own conversation with Sherlock: one, shared by everyone who may
// approve proposals or works the cases, each question asked as its asker
// and naming them, kept in ChatsKey while the proposal waits, bounded, and
// deleted when it is decided, whatever decides it (applied or failed,
// rejected, expired, replaced). Each command run from the drawer goes
// through Watson with from = "proposal:PC-12", the proposal's own actions
// too once it is approved, and the card logs it: the line, who, when, how
// it ended, read or change, who approved it; never its output. Once the
// proposal is decided, its log keeps only the changes that finished done.

// ChatsKey is the cases ConfigMap's key of the waiting proposals'
// conversations with Sherlock, one per proposal.
const ChatsKey = "cards-chat.json"

// DefaultChatsBytes is the most ChatsKey holds; MaxChatBytes the most one
// proposal's conversation does.
const (
	DefaultChatsBytes = 192 << 10
	MaxChatBytes      = 48 << 10
)

// FromPrefix starts what a command run from a proposal says it came from,
// and the session of its conversation: "proposal:PC-12".
const FromPrefix = "proposal:"

// Limits of a proposal's command log: the commands it logs, and the
// characters of each line.
const (
	maxLogged      = 30
	MaxCommandLine = 500
)

// errUnchanged ends an edit that has nothing to save.
var errUnchanged = errors.New("unchanged")

// ChatSession is the session of a proposal's conversation, and what the
// commands run from it say they came from: "proposal:PC-12".
func ChatSession(id string) string { return FromPrefix + cardid.Canon(id) }

// ProposalFrom reads the proposal a conversation or a command comes from:
// "proposal:PC-12" is PC-12.
func ProposalFrom(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, FromPrefix)
	if !ok || !cardid.Is(rest) {
		return "", false
	}
	return cardid.Canon(rest), true
}

// Command is a command run from a proposal's drawer through Watson
// (CF-61): its line, who asked for it, when, whether it changes anything,
// and how it ended (Watson's status, its exit code, who approved it).
// Never its output. The shape is a case's (casework.Command).
type Command struct {
	// ID is Watson's item.
	ID       string     `json:"id"`
	Line     string     `json:"line"`
	By       string     `json:"by"`
	At       time.Time  `json:"at"`
	Change   bool       `json:"change"`
	Status   string     `json:"status"`
	Exit     *int       `json:"exit,omitempty"`
	Approver string     `json:"approver,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
}

// ended says a command's status is final.
func ended(status string) bool {
	switch status {
	case "done", "failed", "stopped", "lapsed", "discarded", "refused":
		return true
	}
	return false
}

// Fixed says a command is a change that finished done: what a decided
// proposal keeps of its log, and what the cases it resolves are given.
func (c Command) Fixed() bool {
	return c.Change && c.Status == "done" && (c.Exit == nil || *c.Exit == 0)
}

// SetForget gives the store what drops a proposal's conversation from
// Sherlock's memory when the proposal is decided (sherlock.ForgetSession).
func (s *Store) SetForget(f func(session string) int) {
	s.emu.Lock()
	defer s.emu.Unlock()
	s.forget = f
}

// ---------- the conversation ----------

// chatRecord is one proposal's conversation, as Sherlock keeps it.
type chatRecord struct {
	Cluster  string         `json:"cluster,omitempty"`
	Messages []sherlock.Msg `json:"messages,omitempty"`
	Updated  time.Time      `json:"updated,omitzero"`
}

// chatsRecord is ChatsKey: the conversations, by proposal.
type chatsRecord struct {
	Chats map[string]chatRecord `json:"chats,omitempty"`
}

// changeChats edits ChatsKey over the version read.
func (s *Store) changeChats(ctx context.Context, edit func(*chatsRecord) error) error {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return changeJSON(ctx, s.opts.Backend, ChatsKey, func(rec *chatsRecord) error {
		if rec.Chats == nil {
			rec.Chats = map[string]chatRecord{}
		}
		return edit(rec)
	})
}

// Chats is the keeper of the proposals' conversations, for Sherlock.
func (s *Store) Chats() sherlock.Keeper { return chats{s} }

type chats struct{ s *Store }

// Keeps says a key is a proposal's conversation: its session is
// "proposal:PC-12".
func (c chats) Keeps(k sherlock.Key) bool {
	_, ok := ProposalFrom(k.Session)
	return ok
}

// Load reads a proposal's conversation; none when it keeps none.
func (c chats) Load(ctx context.Context, k sherlock.Key) ([]sherlock.Msg, error) {
	id, ok := ProposalFrom(k.Session)
	if !ok {
		return nil, nil
	}
	rec, _, err := loadJSON[chatsRecord](ctx, c.s.opts.Backend, ChatsKey)
	if err != nil {
		return nil, err
	}
	return rec.Chats[id].Messages, nil
}

// errDecidedChat refuses to keep the conversation of a decided proposal.
var errDecidedChat = fmt.Errorf("%w: the proposed change is decided, and keeps no conversation", ErrState)

// startsWithQuestion drops a conversation's first messages until it
// starts with a question.
func startsWithQuestion(msgs []sherlock.Msg) []sherlock.Msg {
	for len(msgs) > 0 && msgs[0].Role != "user" {
		msgs = msgs[1:]
	}
	return msgs
}

// dropQuestion drops a conversation's first question with its answer.
func dropQuestion(msgs []sherlock.Msg) []sherlock.Msg {
	if len(msgs) == 0 {
		return msgs
	}
	return startsWithQuestion(msgs[1:])
}

// Save keeps a proposal's conversation while the proposal waits, within
// MaxChatBytes, the oldest messages going first; and all of them within
// the key's limit: past it, the conversation spoken in longest ago loses
// its oldest question first.
func (c chats) Save(ctx context.Context, k sherlock.Key, msgs []sherlock.Msg) error {
	id, ok := ProposalFrom(k.Session)
	if !ok {
		return nil
	}
	s := c.s
	card, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !card.Open() {
		return errDecidedChat
	}
	one := chatRecord{Cluster: k.Cluster, Messages: msgs, Updated: s.now()}
	b, _ := json.Marshal(one)
	for len(b) > MaxChatBytes && len(one.Messages) > 1 {
		one.Messages = startsWithQuestion(one.Messages[1:])
		b, _ = json.Marshal(one)
	}
	if len(b) > MaxChatBytes {
		return fmt.Errorf("%w: the conversation of %s would pass its %d KiB", ErrFull, id, MaxChatBytes>>10)
	}
	limit := s.opts.Limits.ChatsBytes
	limit -= limit / 16
	return s.changeChats(ctx, func(rec *chatsRecord) error {
		rec.Chats[id] = one
		for {
			if b, _ := json.Marshal(rec); len(b) <= limit {
				return nil
			}
			// The conversation spoken in longest ago, this one last.
			oldest := ""
			for x, r := range rec.Chats {
				if x == id {
					continue
				}
				if oldest == "" || r.Updated.Before(rec.Chats[oldest].Updated) {
					oldest = x
				}
			}
			if oldest == "" {
				oldest = id
			}
			r := rec.Chats[oldest]
			r.Messages = dropQuestion(r.Messages)
			if len(r.Messages) == 0 {
				delete(rec.Chats, oldest)
				if oldest == id {
					return fmt.Errorf("%w: the proposed changes' conversations have no room left", ErrFull)
				}
				continue
			}
			rec.Chats[oldest] = r
		}
	})
}

// TroubleText is a proposal as the data its conversation starts from
// (CF-61): its id, title, cluster, state, who proposed it and why, the
// evidence, and each action with its command, the agent's impact and
// risk, and what Watson read of it (its targets and impact).
func (c Card) TroubleText(clusterName, agentName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "id: %s\ntitle: %s\ncluster: %s\nstate: %s\nproposed by: %s\n", c.ID, c.Title, firstNonEmpty(clusterName, c.Cluster), c.State, firstNonEmpty(agentName, c.Agent))
	if c.Source.Label != "" {
		fmt.Fprintf(&b, "started by: %s\n", c.Source.Label)
	}
	fmt.Fprintf(&b, "approvals: %d of %d needed\n", len(c.Approvals), c.Needed)
	if c.Reasoning != "" {
		b.WriteString("reasoning:\n" + c.Reasoning + "\n")
	}
	for _, e := range c.Evidence {
		fmt.Fprintf(&b, "evidence (%s): %s\n", e.Kind, e.Text)
	}
	for i, a := range c.Actions {
		what := a.Kind
		if cmd, ok := a.Args["command"].(string); ok && cmd != "" {
			what = cmd
		} else if i < len(c.Plans) && c.Plans[i].Summary != "" {
			what = c.Plans[i].Summary
		}
		fmt.Fprintf(&b, "action %d of %d: %s\n", i+1, len(c.Actions), what)
		if a.Impact != "" {
			fmt.Fprintf(&b, "  the agent's impact: %s\n", a.Impact)
		}
		if a.Risk != "" {
			fmt.Fprintf(&b, "  risk: %s\n", a.Risk)
		}
		if i < len(c.Plans) {
			p := c.Plans[i]
			if len(p.Targets) > 0 {
				fmt.Fprintf(&b, "  acts on: %s\n", strings.Join(p.Targets, ", "))
			}
			for _, l := range p.Impact {
				fmt.Fprintf(&b, "  Watson read: %s\n", l)
			}
			if p.Unread != "" {
				fmt.Fprintf(&b, "  Watson could not read its target: %s\n", p.Unread)
			}
		}
	}
	if len(c.Resolves) > 0 {
		fmt.Fprintf(&b, "resolves: %s\n", strings.Join(c.Resolves, ", "))
	}
	return b.String()
}

// ChatAsked records a question asked on a proposal's conversation
// (cards.chat): who asked, never what.
func (s *Store) ChatAsked(c Card, by Person) {
	s.record(audit.ActionCardChat, c, by, audit.OutcomeOK, "asked Sherlock on the proposed change's conversation")
}

// ---------- the command log ----------

// CheckCommand says whether a command may be run from a proposal on a
// cluster (CF-61): the proposal waits for a decision, and is on that
// cluster. Who may is the API's to say.
func (s *Store) CheckCommand(ctx context.Context, id, cluster string) (Card, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return c, err
	}
	switch {
	case !c.Open():
		return c, fmt.Errorf("%w: %s is decided (%s): nothing more runs from it", ErrState, c.ID, DecidedText(c))
	case c.Cluster != cluster:
		return c, fmt.Errorf("%w: %s is on the cluster %s, not %s", ErrInvalid, c.ID, c.Cluster, cluster)
	}
	return c, nil
}

// clipLine cuts a command's line to MaxCommandLine bytes.
func clipLine(l string) string {
	if len(l) <= MaxCommandLine {
		return l
	}
	return l[:MaxCommandLine] + "…"
}

// logCommand adds a command to a card's log, or brings its entry up to
// date: a status that ended is never taken back to one that has not. Past
// maxLogged, the oldest entry that is not a change that finished done
// goes first.
func logCommand(c *Card, cmd Command) bool {
	cmd.Line = clipLine(cmd.Line)
	for i := range c.Commands {
		e := &c.Commands[i]
		if e.ID != cmd.ID {
			continue
		}
		if ended(e.Status) && !ended(cmd.Status) {
			return false
		}
		if e.Status == cmd.Status && e.Approver == cmd.Approver && (e.Exit == nil) == (cmd.Exit == nil) {
			return false
		}
		e.Status, e.Exit, e.Approver, e.Finished = cmd.Status, cmd.Exit, cmd.Approver, cmd.Finished
		return true
	}
	c.Commands = append(c.Commands, cmd)
	for len(c.Commands) > maxLogged {
		i := slices.IndexFunc(c.Commands, func(x Command) bool { return !x.Fixed() && ended(x.Status) })
		if i < 0 {
			i = 0
		}
		c.Commands = slices.Delete(c.Commands, i, i+1)
	}
	return true
}

// LogCommands adds the commands just run from a waiting proposal to its
// log, each audited (cards.command).
func (s *Store) LogCommands(ctx context.Context, id string, cmds []Command, by Person) error {
	var added []Command
	c, err := s.edit(ctx, id, func(x *Card) error {
		added = added[:0]
		if !x.Open() {
			return fmt.Errorf("%w: %s", ErrState, DecidedText(*x))
		}
		for _, cmd := range cmds {
			fresh := !slices.ContainsFunc(x.Commands, func(e Command) bool { return e.ID == cmd.ID })
			if logCommand(x, cmd) && fresh {
				added = append(added, cmd)
			}
		}
		if len(added) == 0 {
			return errUnchanged
		}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, cmd := range added {
		kind := map[bool]string{true: "change", false: "read"}[cmd.Change]
		s.record(audit.ActionCardCommand, c, by, audit.OutcomeOK, fmt.Sprintf("command %q from the proposed change's drawer; kind %s; status %s", clipLine(cmd.Line), kind, cmd.Status))
	}
	return nil
}

// CommandEnded records how a command run from a proposal ended: its own
// actions, run once it was approved, and the commands run from its
// drawer. A proposal decided by then keeps only the changes that finished
// done.
func (s *Store) CommandEnded(ctx context.Context, id string, cmd Command) error {
	_, err := s.edit(ctx, id, func(x *Card) error {
		if x.Finished() && !cmd.Fixed() {
			i := slices.IndexFunc(x.Commands, func(e Command) bool { return e.ID == cmd.ID })
			if i < 0 {
				return errUnchanged
			}
			x.Commands = slices.Delete(x.Commands, i, i+1)
			return nil
		}
		if !logCommand(x, cmd) {
			return errUnchanged
		}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}

// ---------- once decided ----------

// endTrouble ends a proposal's troubleshooting once it is decided (CF-61):
// its conversation is deleted, in ChatsKey and in Sherlock's memory, and
// its log keeps only the changes that finished done. A proposal still
// waiting or approved, or one already settled, changes nothing.
func (s *Store) endTrouble(c Card) {
	if !c.Finished() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	messages, err := s.dropChats(ctx, func(id string) bool { return id == c.ID })
	if err != nil {
		s.opts.Logger.Warn("a decided proposal's conversation could not be deleted", "proposal", c.ID, "err", err)
	}
	s.emu.RLock()
	forget := s.forget
	s.emu.RUnlock()
	if forget != nil {
		forget(ChatSession(c.ID))
	}
	before, after := 0, 0
	out, err := s.edit(ctx, c.ID, func(x *Card) error {
		before = len(x.Commands)
		var keep []Command
		for _, cmd := range x.Commands {
			if cmd.Fixed() {
				keep = append(keep, cmd)
			}
		}
		after = len(keep)
		if after == before {
			return errUnchanged
		}
		x.Commands = keep
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) && !errors.Is(err, ErrNotFound) {
		s.opts.Logger.Warn("a decided proposal's command log could not be settled", "proposal", c.ID, "err", err)
		out = c
	}
	if out.ID == "" {
		out = c
	}
	if messages > 0 || before != after {
		s.record(audit.ActionCardChat, out, SystemPerson, audit.OutcomeOK, fmt.Sprintf("%s: the conversation deleted (%d messages); %d of %d commands kept (the changes that finished done)", out.State, messages, after, before))
	}
}

// dropChats deletes the conversations of the proposals gone says, and
// says how many messages it deleted.
func (s *Store) dropChats(ctx context.Context, gone func(id string) bool) (int, error) {
	messages := 0
	err := s.changeChats(ctx, func(rec *chatsRecord) error {
		messages = 0
		dropped := 0
		for id, r := range rec.Chats {
			if gone(id) {
				messages += len(r.Messages)
				delete(rec.Chats, id)
				dropped++
			}
		}
		if dropped == 0 {
			return errUnchanged
		}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return 0, nil
	}
	return messages, err
}

// sweepChats deletes the conversations of proposals no longer waiting,
// and of those gone from the record: decided on a replica that could not
// delete them, or pruned.
func (s *Store) sweepChats(ctx context.Context, rec record) {
	open := map[string]bool{}
	for _, c := range rec.Cards {
		if !c.Finished() {
			open[c.ID] = true
		}
	}
	if _, err := s.dropChats(ctx, func(id string) bool { return !open[id] }); err != nil {
		s.opts.Logger.Warn("the decided proposals' conversations could not be deleted", "err", err)
	}
}

// --- end of a proposed change's Sherlock and Watson.
