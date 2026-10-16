package sherlock

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
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
	// MaxUIActions bounds the UI actions of one answer (P6U-09).
	MaxUIActions = 10
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
	seq    int
}

// UIResult is what the window posts back.
type UIResult struct {
	// Applied says the view changed; Dropped that it did not because the
	// person changed the view during the answer (P6U-09).
	Applied bool   `json:"applied"`
	Dropped bool   `json:"dropped,omitempty"`
	Error   string `json:"error,omitempty"`
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
	c := s.Lookup(k)
	if c == nil {
		return ErrNoUIAction
	}
	c.mu.Lock()
	ch := c.uiWait[id]
	delete(c.uiWait, id)
	c.mu.Unlock()
	if ch == nil {
		return ErrNoUIAction
	}
	ch <- res
	return nil
}

// uiLog is the UI actions of one answer, for its audit record (P6U-15).
type uiLog struct {
	count   int
	entries []string
}

func (l *uiLog) add(name string, input json.RawMessage, outcome string) {
	args := Scrub(string(input))
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

// runUI runs one UI action in the asking window and waits for the result.
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
	ch := make(chan UIResult, 1)
	c.mu.Lock()
	c.uiSeq++
	req := UIRequest{ID: newID(), Window: q.Window, Answer: ans.ID, Action: a.Name, Input: input, seq: c.uiSeq}
	c.uiOut = append(c.uiOut, req)
	if len(c.uiOut) > 32 {
		c.uiOut = c.uiOut[len(c.uiOut)-32:]
	}
	c.uiWait[req.ID] = ch
	c.changedLocked()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.uiWait, req.ID)
		c.mu.Unlock()
	}()

	timer := time.NewTimer(s.opts.UITimeout)
	defer timer.Stop()
	var res UIResult
	select {
	case <-ctx.Done():
		log.add(a.Name, input, "stopped")
		return ToolResult{Error: true, Text: "Stopped."}
	case <-timer.C:
		log.add(a.Name, input, "no window")
		return ToolResult{Error: true, Text: fmt.Sprintf("The console window did not answer within %s: it may be closed or asleep. Tell the person where to look, with links, instead.", s.opts.UITimeout)}
	case res = <-ch:
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
		out["note"] = "Not applied: the person changed the view themselves during this answer. Do not change it again in this answer; say what you would have shown."
	case res.Applied && a.Changes:
		log.add(a.Name, input, "applied")
		if res.Label != "" {
			c.mu.Lock()
			ans.View = append(ans.View, ViewChange{Label: truncate(res.Label, 200)})
			c.changedLocked()
			c.mu.Unlock()
		}
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
	return "<console-view>\n" + ScrubKnown(view, s.secrets()) + "\n</console-view>"
}
