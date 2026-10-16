// Package sherlock answers questions about a cluster (Phase 5): a
// conversation per person and cluster, kept on the server, in which the
// Claude API is given the question and a set of read-only tools that run
// as the person asking. Nothing it can call changes a cluster.
package sherlock

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// APIVersion is the Messages API version the client speaks.
const APIVersion = "2023-06-01"

// DefaultBaseURL is the Claude API.
const DefaultBaseURL = "https://api.anthropic.com"

// Block is one content block of a message, as the Messages API has it.
type Block struct {
	Type string `json:"type"`
	// Text is a text block's text.
	Text string `json:"text,omitempty"`
	// ID, Name and Input are a tool_use block's.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// ToolUseID, Content and IsError are a tool_result block's.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// Thinking and Signature are a thinking block's; Data a
	// redacted_thinking block's. The models think before they answer and
	// between tool calls; the API wants these blocks back exactly as it
	// sent them, with the tool results that follow.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
	// CacheControl marks the end of a prefix to cache.
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON writes a block with the fields its type has: a thinking
// block always carries its thinking, empty when the model keeps it to
// itself, and its signature.
func (b Block) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "thinking":
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{b.Type, b.Thinking, b.Signature})
	case "redacted_thinking":
		return json.Marshal(struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}{b.Type, b.Data})
	}
	type plain Block
	return json.Marshal(plain(b))
}

// CacheControl asks the API to cache the prompt up to here.
type CacheControl struct {
	Type string `json:"type"`
}

// Message is one turn of the conversation sent to the API.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

// Request is a Messages API request; the client always streams.
type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    []Block   `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	Stream    bool      `json:"stream"`
}

// Usage is what one request used.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Add sums two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.InputTokens + o.InputTokens, u.OutputTokens + o.OutputTokens, u.CacheCreationInputTokens + o.CacheCreationInputTokens, u.CacheReadInputTokens + o.CacheReadInputTokens}
}

// Prompt is every input token, cached or not.
func (u Usage) Prompt() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// Response is a finished message.
type Response struct {
	Content    []Block
	StopReason string
	Usage      Usage
}

// APIError is an error the API answered with.
type APIError struct {
	Status  int
	Type    string
	Message string
	// Provider names who answered, for the message: "the Claude API" when
	// empty.
	Provider string
}

func (e *APIError) Error() string {
	who := e.Provider
	if who == "" {
		who = "the Claude API"
	}
	if e.Message == "" {
		return fmt.Sprintf("%s answered %d", who, e.Status)
	}
	if e.Type == "" {
		return fmt.Sprintf("%s answered %d: %s", who, e.Status, e.Message)
	}
	return fmt.Sprintf("%s answered %d (%s): %s", who, e.Status, e.Type, e.Message)
}

// Retryable says whether the request may be sent again: overloaded, rate
// limited or a server error.
func (e *APIError) Retryable() bool {
	return e.Status == 429 || e.Status == 529 || e.Status >= 500 || e.Type == "overloaded_error" || e.Type == "rate_limit_error"
}

// apiError reads an error answer: the Messages and Chat Completions APIs'
// {"error": {"type", "message"}}, Vertex's {"error": {"status", "message"}}
// and Bedrock's {"message"}.
func apiError(status int, b []byte) *APIError {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(b, &e)
	var inner struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &inner) != nil {
		var s string
		if json.Unmarshal(e.Error, &s) == nil {
			inner.Message = s
		}
	}
	out := &APIError{Status: status, Type: inner.Type, Message: inner.Message}
	if out.Type == "" {
		out.Type = inner.Status
	}
	if out.Message == "" {
		out.Message = e.Message
	}
	if out.Message == "" && len(b) > 0 && b[0] != '{' {
		out.Message = strings.TrimSpace(string(b))
		if len(out.Message) > 300 {
			out.Message = out.Message[:300] + "…"
		}
	}
	return out
}

// readStream reads the server-sent events of a streamed message.
func readStream(r io.Reader, onDelta func(Delta)) (Response, bool, error) {
	var (
		out     Response
		partial = map[int]*strings.Builder{}
		started bool
		done    bool
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var event string
	var data bytes.Buffer
	dispatch := func() error {
		defer func() { event = ""; data.Reset() }()
		if data.Len() == 0 {
			return nil
		}
		var ev struct {
			Type    string          `json:"type"`
			Index   int             `json:"index"`
			Message json.RawMessage `json:"message"`
			Block   Block           `json:"content_block"`
			Delta   struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage *Usage `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data.Bytes(), &ev); err != nil {
			return fmt.Errorf("reading the Claude API's answer: %w", err)
		}
		if ev.Type == "" {
			ev.Type = event
		}
		switch ev.Type {
		case "message_start":
			var m struct {
				Usage Usage `json:"usage"`
			}
			_ = json.Unmarshal(ev.Message, &m)
			out.Usage = m.Usage
		case "content_block_start":
			for len(out.Content) <= ev.Index {
				out.Content = append(out.Content, Block{})
			}
			b := ev.Block
			if b.Type == "tool_use" {
				b.Input = nil
				partial[ev.Index] = &strings.Builder{}
				started = true
				if onDelta != nil {
					onDelta(Delta{Tool: b.Name})
				}
			}
			out.Content[ev.Index] = b
		case "content_block_delta":
			if ev.Index >= len(out.Content) {
				return fmt.Errorf("the Claude API sent a delta for a block it had not started")
			}
			switch ev.Delta.Type {
			case "text_delta":
				out.Content[ev.Index].Text += ev.Delta.Text
				started = true
				if onDelta != nil && ev.Delta.Text != "" {
					onDelta(Delta{Text: ev.Delta.Text})
				}
			case "input_json_delta":
				if p := partial[ev.Index]; p != nil {
					p.WriteString(ev.Delta.PartialJSON)
				}
			case "thinking_delta":
				out.Content[ev.Index].Thinking += ev.Delta.Thinking
			case "signature_delta":
				out.Content[ev.Index].Signature += ev.Delta.Signature
			}
		case "content_block_stop":
			if p := partial[ev.Index]; p != nil && ev.Index < len(out.Content) {
				in := strings.TrimSpace(p.String())
				if in == "" {
					in = "{}"
				}
				if !json.Valid([]byte(in)) {
					return fmt.Errorf("the Claude API sent tool input that is not JSON")
				}
				out.Content[ev.Index].Input = json.RawMessage(in)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				// The final output count; input counts come with message_start
				// and are repeated here by newer versions when they change.
				out.Usage.OutputTokens = ev.Usage.OutputTokens
				if ev.Usage.InputTokens > 0 {
					out.Usage.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					out.Usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					out.Usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
			}
		case "message_stop":
			done = true
		case "error":
			e := &APIError{Status: 200}
			if ev.Error != nil {
				e.Type, e.Message = ev.Error.Type, ev.Error.Message
			}
			return e
		}
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return out, started, err
			}
			if done {
				return out, started, nil
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return out, started, fmt.Errorf("reading the Claude API's answer: %w", err)
	}
	if err := dispatch(); err != nil {
		return out, started, err
	}
	if !done {
		return out, started, errors.New("the Claude API's answer ended early")
	}
	return out, started, nil
}
