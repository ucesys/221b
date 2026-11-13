package sherlock

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ucesys/221b/server/internal/scrub"
	"strings"
	"time"
)

// Sherlock's UI actions (P6U-04 to P6U-17) change the asking person's own
// view: the tab, its filters, a table's search text, sort and columns,
// the details pane, a highlighted row. The model calls them like its other
// tools, but the server cannot run them: it sends each to the console
// window that asked, as a ui event on the conversation's stream, and waits
// for the result the window posts back. They never reach a cluster or
// anything other people see.
//
// A change to the view is not made as the model asks for it (SR-18): the
// window checks it and the answer holds it, and once the answer is done
// the server sends the changes it held, in order, for the window to make
// then. The person's screen moves only when the whole answer is there.

//go:embed ui_actions.json
var uiActionsJSON []byte

// UIAction is one action of the list that the server and the browser share.
type UIAction struct {
	Name        string          `json:"name"`
	Changes     bool            `json:"changes"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

var uiActions = func() []UIAction {
	var f struct {
		Actions []UIAction `json:"actions"`
	}
	if err := json.Unmarshal(uiActionsJSON, &f); err != nil {
		panic("ui_actions.json: " + err.Error())
	}
	return f.Actions
}()

// UIActions returns the list.
func UIActions() []UIAction { return append([]UIAction(nil), uiActions...) }

// UIActionsJSON is the list as the browser reads it, for the contract test.
func UIActionsJSON() []byte { return append([]byte(nil), uiActionsJSON...) }

func uiAction(name string) (UIAction, bool) {
	for _, a := range uiActions {
		if a.Name == name {
			return a, true
		}
	}
	return UIAction{}, false
}

// IsUITool says whether a tool is a UI action.
func IsUITool(name string) bool {
	_, ok := uiAction(name)
	return ok
}

// uiDefs are the tool definitions of the UI actions offered: all of them,
// or with changes off (P6U-14) only view_state.
func uiDefs(changes bool) []ToolDef {
	var out []ToolDef
	for _, a := range uiActions {
		if a.Changes && !changes {
			continue
		}
		out = append(out, ToolDef{Name: a.Name, Description: a.Description, InputSchema: a.InputSchema})
	}
	return out
}

const (
	// MaxUIActions bounds the UI actions of one answer (P6U-09), reads
	// included: the hard cap.
	MaxUIActions = 10
	// ViewChangesAsked is how many view changes the prompt asks Sherlock
	// to keep to for one question, below the hard cap (S-7).
	ViewChangesAsked = 4
	// UITimeout is how long a UI action waits for the window (P6U-08).
	UITimeout = 5 * time.Second
	// MaxView bounds the view state sent with a question (P6U-03).
	MaxView = 2000
)

// UIRequest is one action sent to a window, as the ui event carries it.
type UIRequest struct {
	ID string `json:"id"`
	// Window is the console window that is to run it.
	Window string `json:"window"`
	// Answer is the answer it belongs to, for the change notice and Undo.
	Answer int             `json:"answer"`
	Action string          `json:"action"`
	Input  json.RawMessage `json:"input"`
	// Hold asks the window to check a change and leave the view as it is:
	// the answer holds it until it is done (SR-18).
	Hold bool `json:"hold,omitempty"`
	seq  int
	// session is the session whose window runs it, in a shared
	// conversation (SR-19); empty in a person's own.
	session string
}

// For says whether a request goes to a session's windows: in a shared
// conversation only the asker's.
func (r UIRequest) For(session string) bool {
	return r.session == "" || r.session == session
}

// uiWaiter is an action waiting for its window's result, and the session
// that may post it ("" is the conversation's own).
type uiWaiter struct {
	ch      chan UIResult
	session string
}

// UIResult is what the window posts back.
type UIResult struct {
	// Applied says the view changed; Dropped that it did not because the
	// person changed the view during the answer (P6U-09).
	Applied bool `json:"applied"`
	Dropped bool `json:"dropped,omitempty"`
	// Deferred says it did not because the window has unsaved changes
	// open: a form or drawer the person is editing (SR-14).
	Deferred bool `json:"deferred,omitempty"`
	// Held says the window checked a change it was asked to hold and left
	// the view as it is, for the end of the answer (SR-18).
	Held  bool   `json:"held,omitempty"`
	Error string `json:"error,omitempty"`
	// Label says what changed, for the notice under the answer.
	Label string `json:"label,omitempty"`
	// Link is a link the model may offer instead, such as to another cluster.
	Link string `json:"link,omitempty"`
	// View is the view state after the action.
	View json.RawMessage `json:"view,omitempty"`
}

// ViewChange is one change Sherlock made to the view, shown under the
// answer with an Undo button (P6U-10).
type ViewChange struct {
	Label string `json:"label"`
}

// ViewShow is a view change the window left undone, kept under the answer
// as a Show in 221B button that applies it when the person presses it
// (SR-15): the action and its input, as the model gave them.
type ViewShow struct {
	Action string          `json:"action"`
	Input  json.RawMessage `json:"input"`
	// Why it was left: unsaved (changes being edited), moved (the person
	// changed the view during the answer), asleep (the window did not
	// answer) or stopped (the answer stopped or failed before it was done,
	// so the changes it held were not made, SR-18).
	Why string `json:"why"`
}

// maxShowInput bounds the input kept for a Show in 221B button.
const maxShowInput = 2048

// keepShow keeps a change the window left for the answer's Show in 221B
// button: at most MaxUIActions of them, each with its input bounded.
func (c *Conversation) keepShow(ans *Msg, name string, input json.RawMessage, why string) {
	if len(input) > maxShowInput || !json.Valid(input) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ans.Show) >= MaxUIActions {
		return
	}
	ans.Show = append(ans.Show, ViewShow{Action: name, Input: append(json.RawMessage(nil), input...), Why: why})
	c.changedLocked()
}

// ErrNoUIAction is returned for a result nobody waits for: unknown, from
// another session, or late.
var ErrNoUIAction = errors.New("no view action of this conversation is waiting for that result")

// UIRequestsAfter returns the requests sent after seq, and the latest seq.
func (c *Conversation) UIRequestsAfter(seq int) ([]UIRequest, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []UIRequest
	for _, r := range c.uiOut {
		if r.seq > seq {
			out = append(out, r)
		}
	}
	return out, c.uiSeq
}

// Lookup returns the conversation for a key if there is one.
func (s *Service) Lookup(k Key) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.convs[k]
}

// DeliverUI hands a window's result to the action waiting for it. The key
// is the poster's session and cluster, so another person's session, or a
// result after the action gave up, finds nothing (P6U-17).
func (s *Service) DeliverUI(k Key, id string, res UIResult) error {
	return s.DeliverUIFrom(k, "", id, res)
}

// DeliverUIFrom is DeliverUI on a shared conversation (a case's or a
// proposed change's, SR-19): only the session of the person who asked may
// post the result; another's finds nothing.
func (s *Service) DeliverUIFrom(k Key, session, id string, res UIResult) error {
	c := s.Lookup(k)
	if c == nil {
		return ErrNoUIAction
	}
	c.mu.Lock()
	w, ok := c.uiWait[id]
	if ok && w.session != "" && w.session != session {
		ok = false
	}
	if ok {
		delete(c.uiWait, id)
	}
	c.mu.Unlock()
	if !ok {
		return ErrNoUIAction
	}
	w.ch <- res
	return nil
}

// uiLog is the UI actions of one answer, for its audit record (P6U-15).
type uiLog struct {
	count   int
	entries []string
	// held are the view changes the answer holds until it is done (SR-18).
	held []heldUI
}

// heldUI is one view change held for the end of the answer.
type heldUI struct {
	name  string
	input json.RawMessage
}

func (l *uiLog) add(name string, input json.RawMessage, outcome string) {
	args := scrub.Text(string(input))
	if len(args) > 160 {
		args = args[:160] + "…"
	}
	l.entries = append(l.entries, fmt.Sprintf("%s %s %s", name, args, outcome))
}

func (l *uiLog) String() string {
	if len(l.entries) == 0 {
		return ""
	}
	return "ui [" + strings.Join(l.entries, ", ") + "]"
}

func uiText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Why a UI action got no result from its window.
var (
	errUIStopped = errors.New("stopped")
	errUIAsleep  = errors.New("no window")
)

// sendUI sends one action to the asking window and waits for its result.
func (s *Service) sendUI(ctx context.Context, c *Conversation, ans *Msg, q Question, name string, input json.RawMessage, hold bool) (UIResult, error) {
	ch := make(chan UIResult, 1)
	c.mu.Lock()
	c.uiSeq++
	req := UIRequest{ID: newID(), Window: q.Window, Answer: ans.ID, Action: name, Input: input, Hold: hold, seq: c.uiSeq, session: q.WindowSession}
	c.uiOut = append(c.uiOut, req)
	if len(c.uiOut) > 32 {
		c.uiOut = c.uiOut[len(c.uiOut)-32:]
	}
	c.uiWait[req.ID] = uiWaiter{ch: ch, session: q.WindowSession}
	c.changedLocked()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.uiWait, req.ID)
		c.mu.Unlock()
	}()

	timer := time.NewTimer(s.opts.UITimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return UIResult{}, errUIStopped
	case <-timer.C:
		return UIResult{}, errUIAsleep
	case res := <-ch:
		return res, nil
	}
}

// heldNote is what the model reads for a view change held for the end of
// its answer (SR-18).
const heldNote = "Held: this change is made with your other view changes, in the order you made them, once your answer is complete, so the person's screen moves only when they can read the whole answer. Until then view_state shows the view as it still is. Do not call it again; carry on and finish the answer."

// runUI runs one UI action in the asking window and waits for the result.
// A read runs at once; a change is checked by the window and held for the
// end of the answer (SR-18).
func (s *Service) runUI(ctx context.Context, c *Conversation, ans *Msg, q Question, call Block, log *uiLog) ToolResult {
	a, _ := uiAction(call.Name)
	input := call.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	log.count++
	if log.count > MaxUIActions {
		log.add(a.Name, input, "refused")
		return ToolResult{Error: true, Text: fmt.Sprintf("Not run: at most %d view actions per answer. Answer with what you have.", MaxUIActions)}
	}
	if a.Changes && !q.ViewChanges {
		log.add(a.Name, input, "refused")
		return ToolResult{Error: true, Text: "Not run: an administrator has turned off view changes. Tell the person where to look instead."}
	}
	if q.Window == "" {
		log.add(a.Name, input, "no window")
		return ToolResult{Error: true, Text: "No console window is open to show this in. Tell the person where to look, with links, instead of changing the view."}
	}
	res, err := s.sendUI(ctx, c, ans, q, a.Name, input, a.Changes)
	switch err {
	case errUIStopped:
		log.add(a.Name, input, "stopped")
		return ToolResult{Error: true, Text: "Stopped."}
	case errUIAsleep:
		log.add(a.Name, input, "no window")
		if a.Changes {
			c.keepShow(ans, a.Name, input, "asleep")
		}
		return ToolResult{Error: true, Text: fmt.Sprintf("The console window did not answer within %s: it may be closed or asleep. A Show in 221B button under your answer applies the change when the person presses it; tell them where to look, with links, too.", s.opts.UITimeout)}
	}
	view := json.RawMessage(nil)
	if len(res.View) > 0 && json.Valid(res.View) {
		view = res.View
	}
	out := map[string]any{"applied": res.Applied}
	switch {
	case res.Error != "":
		log.add(a.Name, input, "refused")
		out["error"] = res.Error
	case res.Dropped:
		log.add(a.Name, input, "dropped")
		if a.Changes {
			c.keepShow(ans, a.Name, input, "moved")
		}
		out["note"] = "Not applied: the person changed the view themselves during this answer. Do not change it again in this answer; say what you would have shown. A Show in 221B button under your answer applies it when they press it."
	case res.Deferred && a.Changes:
		log.add(a.Name, input, "deferred")
		c.keepShow(ans, a.Name, input, "unsaved")
		out["note"] = "Not applied: the person has unsaved changes open in this window (a form or drawer they are editing), so the view was left as it is. A Show in 221B button under your answer applies this change, and any others of this answer left the same way, when they press it. Answer in words with links, and say that the button shows it."
	case a.Changes:
		// Checked and held: made once the answer is done (SR-18). The
		// view the window sent is the one before it, so it is left out.
		log.add(a.Name, input, "held")
		log.held = append(log.held, heldUI{name: a.Name, input: append(json.RawMessage(nil), input...)})
		out = map[string]any{"held": true, "note": heldNote}
		view = nil
	default:
		log.add(a.Name, input, "read")
	}
	if res.Link != "" {
		out["link"] = res.Link
	}
	if view != nil {
		out["view"] = view
	}
	return ToolResult{Error: res.Error != "", Text: uiText(out)}
}

// flushUI makes the view changes an answer held, in order, now that it is
// done (SR-18), each as the window would have made it during the answer:
// applied, or kept for the Show in 221B button when the person moved the
// view, has unsaved changes open or the window does not answer.
func (s *Service) flushUI(ctx context.Context, c *Conversation, ans *Msg, q Question, log *uiLog) {
	held := log.held
	log.held = nil
	for i, h := range held {
		res, err := s.sendUI(ctx, c, ans, q, h.name, h.input, false)
		switch {
		case err == errUIStopped:
			log.add(h.name, h.input, "stopped")
			log.held = held[i:]
			c.keepHeld(ans, log, "stopped")
			return
		case err == errUIAsleep:
			// A window that did not answer one will not answer the rest:
			// they are kept for the button without waiting again.
			log.add(h.name, h.input, "no window")
			for _, r := range held[i:] {
				c.keepShow(ans, r.name, r.input, "asleep")
			}
			return
		case res.Error != "":
			log.add(h.name, h.input, "refused")
		case res.Dropped:
			log.add(h.name, h.input, "dropped")
			c.keepShow(ans, h.name, h.input, "moved")
		case res.Deferred:
			log.add(h.name, h.input, "deferred")
			c.keepShow(ans, h.name, h.input, "unsaved")
		case res.Applied:
			log.add(h.name, h.input, "applied")
			if res.Label != "" {
				c.mu.Lock()
				ans.View = append(ans.View, ViewChange{Label: truncate(res.Label, 200)})
				c.changedLocked()
				c.mu.Unlock()
			}
		default:
			log.add(h.name, h.input, "not applied")
		}
	}
}

// keepHeld keeps the view changes an answer still holds for its Show in
// 221B button, when it ends without being done (SR-18).
func (c *Conversation) keepHeld(ans *Msg, log *uiLog, why string) {
	for _, h := range log.held {
		c.keepShow(ans, h.name, h.input, why)
	}
	log.held = nil
}

// uiLabel is a UI action's step under the answer.
func uiLabel(name string) string {
	switch name {
	case "view_state":
		return "Read your view"
	case "view_open":
		return "Opened a tab"
	case "view_filter":
		return "Changed the filters"
	case "view_search":
		return "Searched the table"
	case "view_sort":
		return "Sorted the table"
	case "view_columns":
		return "Changed the columns"
	case "view_details":
		return "Opened the details"
	case "view_highlight":
		return "Highlighted a row"
	case "view_aiops":
		return "Opened AIOps"
	case "view_admin":
		return "Opened a page"
	case "open_tool_dialog":
		return "Opened a tool dialog"
	}
	return name
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// viewBlock is the view state sent with a question (P6U-03), scrubbed like
// a tool result.
func (s *Service) viewBlock(view string) string {
	view = strings.TrimSpace(view)
	if view == "" {
		return ""
	}
	if len(view) > MaxView {
		cut := MaxView
		for cut > 0 && (view[cut]&0xC0) == 0x80 {
			cut--
		}
		view = view[:cut] + "…"
	}
	return "<console-view>\n" + scrub.Known(view, s.secrets()) + "\n</console-view>"
}
