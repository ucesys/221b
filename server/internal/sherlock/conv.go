package sherlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// Toolbox is the read-only tools for one person in one cluster.
type Toolbox interface {
	Defs() []ToolDef
	// Label says what a call does, for the steps shown under the answer:
	// "Read pod web/api-0".
	Label(name string, input json.RawMessage) string
	Run(ctx context.Context, name string, input json.RawMessage) ToolResult
}

// ToolResult is what a tool returns to the model.
type ToolResult struct {
	Text string
	// Refs are the objects the result shows, which the answer may cite.
	Refs  []Ref
	Error bool
}

// Step is one tool call, as shown under an answer.
type Step struct {
	Label  string `json:"label"`
	Tool   string `json:"tool"`
	Status string `json:"status"` // running, done, failed
}

// Msg is one message of a conversation as the browser shows it.
type Msg struct {
	ID     int    `json:"id"`
	Role   string `json:"role"` // user or sherlock
	Text   string `json:"text"`
	Steps  []Step `json:"steps,omitempty"`
	Refs   []Ref  `json:"refs,omitempty"`
	Status string `json:"status,omitempty"` // thinking, answering, done, stopped, failed
	Error  string `json:"error,omitempty"`
	// Asker names who asked, for a conversation seen in another window.
	At time.Time `json:"at"`
	// FirstAt is when the first step or word arrived; DoneAt when the
	// answer ended.
	FirstAt *time.Time `json:"firstAt,omitempty"`
	DoneAt  *time.Time `json:"doneAt,omitempty"`
	Model   string     `json:"model,omitempty"`
	Usage   *Usage     `json:"usage,omitempty"`
	CostUSD float64    `json:"costUSD,omitempty"`
	// View is what the answer changed in the asking window's view (P6U-10).
	View []ViewChange `json:"view,omitempty"`
}

// Key names a conversation: one per session and cluster.
type Key struct {
	Session string
	Cluster string
}

// Conversation is a person's conversation with Sherlock about a cluster.
// It lives on the server for the session, so every window of the session
// shows it (P5-08) and a reload keeps it.
type Conversation struct {
	ID string

	mu       sync.Mutex
	msgs     []*Msg
	history  []Message
	starts   []int // where each question starts in history
	known    Known
	busy     bool
	cancel   context.CancelFunc
	seq      int
	lastUsed time.Time
	subs     map[chan struct{}]struct{}
	rev      int
	// The UI actions sent to the browser, and those waiting for a result.
	uiOut  []UIRequest
	uiSeq  int
	uiWait map[string]chan UIResult
}

// View is a conversation as the browser receives it.
type View struct {
	ID       string `json:"id"`
	Messages []*Msg `json:"messages"`
	Busy     bool   `json:"busy"`
	Rev      int    `json:"rev"`
}

// View returns the conversation as it is now.
func (c *Conversation) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewLocked()
}

func (c *Conversation) viewLocked() View {
	out := make([]*Msg, len(c.msgs))
	for i, m := range c.msgs {
		cp := *m
		cp.Steps = append([]Step(nil), m.Steps...)
		cp.Refs = append([]Ref(nil), m.Refs...)
		cp.View = append([]ViewChange(nil), m.View...)
		out[i] = &cp
	}
	return View{ID: c.ID, Messages: out, Busy: c.busy, Rev: c.rev}
}

// Subscribe returns a channel told about every change; Unsubscribe stops it.
func (c *Conversation) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch
}

func (c *Conversation) Unsubscribe(ch chan struct{}) {
	c.mu.Lock()
	delete(c.subs, ch)
	c.mu.Unlock()
}

// changedLocked tells the subscribers.
func (c *Conversation) changedLocked() {
	c.rev++
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Settings are what an administrator chose, read for every question.
type Settings struct {
	Model string
	Caps  Caps
	// PriceIn and PriceOut, when set, price the model in US dollars per
	// million tokens; otherwise the listed price, or the highest.
	PriceIn, PriceOut float64
}

// Price is the model with the price its spend is counted at.
func (s Settings) Price() Model {
	m, _ := ModelOf(s.Model)
	if s.PriceIn > 0 || s.PriceOut > 0 {
		m.In, m.Out, m.LongAbove = s.PriceIn, s.PriceOut, 0
	}
	return m
}

// Options configure the service.
type Options struct {
	Client   *Client
	Ledger   *Ledger
	Settings func() Settings
	// Secrets are values never to send, such as the API key itself.
	Secrets func() []string
	Audit   *audit.Log
	Now     func() time.Time
	Logger  *slog.Logger
	// MaxRounds bounds the tool rounds of one answer.
	MaxRounds int
	// MaxTokens bounds one reply, its thinking included.
	MaxTokens int
	// Idle is how long an unused conversation is kept.
	Idle time.Duration
	// MaxResult bounds one tool result, in characters.
	MaxResult int
	// MaxHistory bounds what is sent of the conversation, in characters.
	MaxHistory int
	// MaxQuestions bounds the questions, with their answers, a
	// conversation shows; older ones are dropped.
	MaxQuestions int
	// UITimeout is how long a UI action waits for the browser.
	UITimeout time.Duration
}

// Service keeps the conversations and answers questions.
type Service struct {
	opts  Options
	mu    sync.Mutex
	convs map[Key]*Conversation
	// running counts answers in progress, for /metrics.
	running int
	wg      sync.WaitGroup
}

// New returns the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxRounds <= 0 {
		o.MaxRounds = 12
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 16000
	}
	if o.Idle <= 0 {
		o.Idle = 12 * time.Hour
	}
	if o.MaxResult <= 0 {
		o.MaxResult = 24_000
	}
	if o.MaxHistory <= 0 {
		o.MaxHistory = 360_000
	}
	if o.MaxQuestions <= 0 {
		o.MaxQuestions = 100
	}
	if o.UITimeout <= 0 {
		o.UITimeout = UITimeout
	}
	if o.Settings == nil {
		o.Settings = func() Settings { return Settings{Model: DefaultModel} }
	}
	if o.Ledger == nil {
		o.Ledger = NewLedger(nil, o.Now, o.Logger)
	}
	return &Service{opts: o, convs: map[Key]*Conversation{}}
}

// Ledger is the spend.
func (s *Service) Ledger() *Ledger { return s.opts.Ledger }

// Conversation returns the conversation for a key, making it if needed.
func (s *Service) Conversation(k Key) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convs[k]
	if c == nil {
		c = &Conversation{ID: newID(), known: Known{}, subs: map[chan struct{}]struct{}{}, uiWait: map[string]chan UIResult{}}
		s.convs[k] = c
	}
	c.lastUsed = s.opts.Now()
	return c
}

// Running says how many answers are in progress.
func (s *Service) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Count says how many conversations are kept.
func (s *Service) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.convs)
}

// Sweep forgets conversations unused for longer than the idle time, and
// those of sessions that ended (alive says whether a session is).
func (s *Service) Sweep(alive func(session string) bool) {
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.convs {
		c.mu.Lock()
		gone := !c.busy && (now.Sub(c.lastUsed) > s.opts.Idle || (alive != nil && k.Session != "" && !alive(k.Session)))
		c.mu.Unlock()
		if gone {
			delete(s.convs, k)
		}
	}
}

// ErrBusy is returned while an answer is still coming.
var ErrBusy = errors.New("Sherlock is still answering the last question; wait for it or stop it")

// Question is one question to answer.
type Question struct {
	Key     Key
	Text    string
	Cluster string // the cluster's name, for the prompt
	Version string
	// Platform describes the cluster for the prompt: its distribution,
	// the operating systems of its nodes and its add-ons, with the
	// guidance for each (platform.Profile.Prompt). Empty, the prompt
	// names only the cluster and its version.
	Platform string
	// Notes are what the administrators wrote about their infrastructure
	// (R2-05), after the cluster's description.
	Notes string
	// Instructions replace the built-in block when set (R2-05 review).
	Instructions string
	Tools        Toolbox
	Who          audit.Who
	From         string
	// Window is the console window whose view the UI actions change: the
	// one that asked, or the main window Sherlock's own window follows
	// (P6U-08). Empty when there is none.
	Window string
	// View is that window's view state when asked (P6U-03).
	View string
	// ViewChanges offers the UI actions that change the view (P6U-14).
	ViewChanges bool
}

// MaxQuestion bounds a question, in characters.
const MaxQuestion = 4000

// Ask starts answering a question; the answer arrives in the conversation.
func (s *Service) Ask(q Question) (*Conversation, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, errors.New("ask a question")
	}
	if len(text) > MaxQuestion {
		return nil, fmt.Errorf("a question is kept to %d characters", MaxQuestion)
	}
	q.Text = text
	set := s.opts.Settings()
	if err := set.Caps.Check(s.opts.Ledger.Now(context.Background())); err != nil {
		s.record(q, "", nil, Usage{}, 0, audit.OutcomeDenied, err.Error())
		return nil, err
	}
	c := s.Conversation(q.Key)
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return c, ErrBusy
	}
	now := s.opts.Now()
	c.seq++
	c.msgs = append(c.msgs, &Msg{ID: c.seq, Role: "user", Text: text, At: now})
	c.seq++
	ans := &Msg{ID: c.seq, Role: "sherlock", Status: "thinking", At: now, Model: set.Model}
	c.msgs = append(c.msgs, ans)
	// A conversation kept open for days shows its last questions only, as
	// the history sent to the model holds the last ones only: each change
	// sends the whole conversation to every panel that shows it.
	if n := len(c.msgs) - 2*s.opts.MaxQuestions; n > 0 {
		c.msgs = append([]*Msg(nil), c.msgs[n:]...)
	}
	c.starts = append(c.starts, len(c.history))
	content := []Block{{Type: "text", Text: text}}
	if v := s.viewBlock(q.View); v != "" {
		content = append(content, Block{Type: "text", Text: v})
	}
	c.history = append(c.history, Message{Role: "user", Content: content})
	ctx, cancel := context.WithCancel(context.Background())
	c.busy, c.cancel = true, cancel
	c.changedLocked()
	c.mu.Unlock()

	s.mu.Lock()
	s.running++
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.answer(ctx, c, ans, q, set)
		s.mu.Lock()
		s.running--
		s.mu.Unlock()
	}()
	return c, nil
}

// Stop stops the answer in progress, if any.
func (s *Service) Stop(k Key) bool {
	s.mu.Lock()
	c := s.convs[k]
	s.mu.Unlock()
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.busy || c.cancel == nil {
		return false
	}
	c.cancel()
	return true
}

// Reset starts the conversation over, stopping any answer in progress.
func (s *Service) Reset(k Key) {
	s.mu.Lock()
	c := s.convs[k]
	s.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.msgs, c.history, c.starts, c.known = nil, nil, nil, Known{}
	c.changedLocked()
	c.mu.Unlock()
}

// Wait waits for the answers in progress, for tests and shutdown.
func (s *Service) Wait() { s.wg.Wait() }

// answer runs the rounds of one answer: the model replies, the tools it
// asks for run, their results go back, until it is done.
func (s *Service) answer(ctx context.Context, c *Conversation, ans *Msg, q Question, set Settings) {
	model := set.Price()
	var (
		total   Usage
		cost    float64
		tools   []string
		outcome = audit.OutcomeOK
		detail  string
		ui      uiLog
	)
	var defs []ToolDef
	if q.Tools != nil {
		defs = append(defs, q.Tools.Defs()...)
	}
	defs = append(defs, uiDefs(q.ViewChanges)...)
	if n := len(defs); n > 0 {
		defs = append([]ToolDef(nil), defs...)
		defs[n-1].CacheControl = &CacheControl{Type: "ephemeral"}
	}
	system := systemPrompt(q, s.opts.Now())
	first := func() {
		if ans.FirstAt == nil {
			t := s.opts.Now()
			ans.FirstAt = &t
		}
	}
	finish := func(status, errText string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		now := s.opts.Now()
		text, cited := Cite(ans.Text, c.known)
		ans.Text, ans.Refs = text, cited
		for i := range ans.Steps {
			if ans.Steps[i].Status == "running" {
				ans.Steps[i].Status = "failed"
			}
		}
		ans.Status, ans.Error, ans.DoneAt = status, errText, &now
		u := total
		ans.Usage, ans.CostUSD = &u, cost
		c.repairLocked(status)
		c.busy, c.cancel = false, nil
		c.lastUsed = now
		c.changedLocked()
	}
	defer func() {
		if err := s.opts.Ledger.Save(context.Background()); err != nil {
			s.opts.Logger.Warn("saving the Sherlock spend failed", "err", err)
		}
		if u := ui.String(); u != "" {
			detail = strings.TrimPrefix(detail+"; "+u, "; ")
		}
		s.record(q, set.Model, tools, total, cost, outcome, detail)
	}()

	for round := 0; ; round++ {
		if err := set.Caps.Check(s.opts.Ledger.Now(ctx)); err != nil && round > 0 {
			outcome, detail = audit.OutcomeDenied, err.Error()
			finish("stopped", err.Error())
			return
		}
		if round >= s.opts.MaxRounds {
			c.mu.Lock()
			ans.Text = strings.TrimSpace(ans.Text + "\n\nI stopped after reading the cluster " + fmt.Sprint(s.opts.MaxRounds) + " times without reaching an answer. Ask a narrower question, naming the object.")
			c.mu.Unlock()
			outcome, detail = audit.OutcomeFailed, "too many tool rounds"
			finish("done", "")
			return
		}
		c.mu.Lock()
		s.trimLocked(c)
		req := Request{Model: set.Model, MaxTokens: s.opts.MaxTokens, System: system, Messages: append([]Message(nil), c.history...), Tools: defs}
		base := ans.Text
		c.mu.Unlock()

		var roundText strings.Builder
		resp, err := s.opts.Client.Stream(ctx, req, func(d Delta) {
			c.mu.Lock()
			defer c.mu.Unlock()
			first()
			if d.Text != "" {
				roundText.WriteString(d.Text)
				ans.Text = base + roundText.String()
				ans.Status = "answering"
			}
			c.changedLocked()
		})
		used := model.Cost(resp.Usage)
		total = total.Add(resp.Usage)
		cost += used
		if used > 0 || round == 0 {
			s.opts.Ledger.Add(context.Background(), used, round == 0)
		}
		if err != nil {
			if ctx.Err() != nil {
				outcome, detail = audit.OutcomeStopped, "stopped by the person asking"
				finish("stopped", "")
				return
			}
			outcome, detail = audit.OutcomeFailed, err.Error()
			s.opts.Logger.Warn("Sherlock could not answer", "err", err)
			finish("failed", explain(err))
			return
		}
		c.mu.Lock()
		c.history = append(c.history, Message{Role: "assistant", Content: resp.Content})
		var calls []Block
		for _, b := range resp.Content {
			if b.Type == "tool_use" {
				calls = append(calls, b)
			}
		}
		if resp.StopReason != "tool_use" || len(calls) == 0 {
			if resp.StopReason == "max_tokens" {
				ans.Text = strings.TrimSpace(ans.Text) + "\n\n(The answer was cut off at its length limit.)"
			}
			c.mu.Unlock()
			finish("done", "")
			return
		}
		// Text before a tool call is the model thinking aloud: it stays in
		// the history but not in the answer.
		ans.Text = base
		ans.Status = "thinking"
		start := len(ans.Steps)
		for _, b := range calls {
			label := ""
			if IsUITool(b.Name) {
				label = uiLabel(b.Name)
			} else {
				label = q.Tools.Label(b.Name, b.Input)
			}
			ans.Steps = append(ans.Steps, Step{Label: label, Tool: b.Name, Status: "running"})
			tools = append(tools, b.Name)
		}
		first()
		c.changedLocked()
		c.mu.Unlock()

		results := make([]Block, 0, len(calls))
		for i, b := range calls {
			var r ToolResult
			if IsUITool(b.Name) {
				r = s.runUI(ctx, c, ans, q, b, &ui)
			} else {
				r = q.Tools.Run(ctx, b.Name, b.Input)
			}
			if ctx.Err() != nil {
				outcome, detail = audit.OutcomeStopped, "stopped by the person asking"
				finish("stopped", "")
				return
			}
			text := s.clip(ScrubKnown(r.Text, s.secrets()))
			results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: text, IsError: r.Error})
			c.mu.Lock()
			c.known.Add(r.Refs...)
			if r.Error {
				ans.Steps[start+i].Status = "failed"
			} else {
				ans.Steps[start+i].Status = "done"
			}
			c.changedLocked()
			c.mu.Unlock()
		}
		c.mu.Lock()
		c.history = append(c.history, Message{Role: "user", Content: results})
		c.mu.Unlock()
	}
}

func (s *Service) secrets() []string {
	if s.opts.Secrets == nil {
		return nil
	}
	return s.opts.Secrets()
}

func (s *Service) clip(t string) string {
	if len(t) <= s.opts.MaxResult {
		return t
	}
	cut := s.opts.MaxResult
	for cut > 0 && (t[cut]&0xC0) == 0x80 {
		cut--
	}
	return t[:cut] + fmt.Sprintf("\n…(cut: %d more characters; ask with a narrower filter)", len(t)-cut)
}

// trimLocked drops the oldest questions and their answers while the
// conversation is too long to send.
func (s *Service) trimLocked(c *Conversation) {
	size := func() int {
		n := 0
		for _, m := range c.history {
			for _, b := range m.Content {
				n += len(b.Text) + len(b.Content) + len(b.Input) + len(b.Thinking) + len(b.Signature) + len(b.Data)
			}
		}
		return n
	}
	for len(c.starts) > 1 && size() > s.opts.MaxHistory {
		cut := c.starts[1]
		c.history = append([]Message(nil), c.history[cut:]...)
		next := make([]int, 0, len(c.starts)-1)
		for _, st := range c.starts[1:] {
			next = append(next, st-cut)
		}
		c.starts = next
	}
}

// repairLocked leaves the history as the API expects it after an answer
// that stopped part way: every tool call answered, and the last turn the
// assistant's.
func (c *Conversation) repairLocked(status string) {
	if len(c.history) == 0 {
		return
	}
	last := c.history[len(c.history)-1]
	if last.Role == "assistant" {
		var open []Block
		for _, b := range last.Content {
			if b.Type == "tool_use" {
				open = append(open, Block{Type: "tool_result", ToolUseID: b.ID, Content: "Not run: the answer was " + status + ".", IsError: true})
			}
		}
		if len(open) == 0 {
			return
		}
		c.history = append(c.history, Message{Role: "user", Content: open})
	}
	note := "(This answer was " + status + " before it was complete.)"
	c.history = append(c.history, Message{Role: "assistant", Content: []Block{{Type: "text", Text: note}}})
}

// explain turns an API failure into words for the person asking.
func explain(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		who := ae.Provider
		if who == "" {
			who = "the Claude API"
		}
		Who := strings.ToUpper(who[:1]) + who[1:]
		switch {
		case ae.Status == 401 || ae.Type == "authentication_error" || ae.Type == "UNAUTHENTICATED":
			return Who + " refused the credential. An administrator can set a new one in Settings."
		case ae.Status == 403 || ae.Type == "permission_error" || ae.Type == "PERMISSION_DENIED" || ae.Type == "AccessDeniedException":
			return Who + " does not let this credential use the model. An administrator can pick another, or grant access, in Settings."
		case ae.Status == 404 || ae.Type == "not_found_error" || ae.Type == "NOT_FOUND":
			return Who + " does not know the model in Settings. An administrator can pick another."
		case ae.Status == 429 || ae.Type == "rate_limit_error" || ae.Type == "RESOURCE_EXHAUSTED" || strings.Contains(ae.Type, "Throttling"):
			return Who + " is limiting how fast this credential may ask. Try again in a minute."
		case ae.Status == 529 || ae.Type == "overloaded_error":
			return Who + " is overloaded. Try again in a minute."
		case ae.Status == 400 && strings.Contains(strings.ToLower(ae.Message), "credit"):
			return Who + " has no credit left on this account. " + ae.Message
		}
		return ae.Error()
	}
	return "Sherlock could not reach the model's API: " + err.Error()
}

// record writes the question's audit record: what was asked, the tools
// it used and the tokens it cost (P5-05).
func (s *Service) record(q Question, model string, tools []string, u Usage, cost float64, outcome, detail string) {
	if s.opts.Audit == nil {
		return
	}
	counts := map[string]int{}
	for _, t := range tools {
		counts[t]++
	}
	names := make([]string, 0, len(counts))
	for t := range counts {
		names = append(names, t)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, t := range names {
		if counts[t] > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", t, counts[t]))
		} else {
			parts = append(parts, t)
		}
	}
	question := Scrub(q.Text)
	if len(question) > 300 {
		question = question[:300] + "…"
	}
	d := fmt.Sprintf("question %q; model %s; tools [%s]; tokens in %d (cache write %d, cache read %d), out %d; cost $%.4f", question, model, strings.Join(parts, ", "), u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.OutputTokens, cost)
	if detail != "" {
		d += "; " + detail
	}
	s.opts.Audit.Record(audit.Event{Action: audit.ActionSherlockAsk, Who: q.Who, Cluster: q.Key.Cluster, Target: "sherlock", Detail: d, Outcome: outcome, From: q.From})
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
