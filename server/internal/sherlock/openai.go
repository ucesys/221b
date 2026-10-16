package sherlock

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
)

// The Chat Completions API, which OpenAI, Azure OpenAI, Vertex AI, Gemini,
// Mistral, vLLM, Ollama and most AI gateways speak. The conversation is
// translated from the Messages API's shape: system blocks become a system
// message, tool calls become an assistant message's tool_calls and tool
// results become tool messages. Thinking blocks stay with the Messages API.

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func strp(s string) *string { return &s }

// toOpenAI translates the request's messages and tools.
func toOpenAI(req Request) ([]oaiMessage, []oaiTool) {
	var msgs []oaiMessage
	var sys []string
	for _, b := range req.System {
		if b.Text != "" {
			sys = append(sys, b.Text)
		}
	}
	if len(sys) > 0 {
		msgs = append(msgs, oaiMessage{Role: "system", Content: strp(strings.Join(sys, "\n\n"))})
	}
	for _, m := range req.Messages {
		var text []string
		switch m.Role {
		case "assistant":
			out := oaiMessage{Role: "assistant"}
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					text = append(text, b.Text)
				case "tool_use":
					var tc oaiToolCall
					tc.ID, tc.Type = b.ID, "function"
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					out.ToolCalls = append(out.ToolCalls, tc)
				}
			}
			if len(text) > 0 || len(out.ToolCalls) == 0 {
				out.Content = strp(strings.Join(text, ""))
			}
			msgs = append(msgs, out)
		default:
			// Tool results come first, as the API wants them right after
			// the assistant's calls; any text follows as a user message.
			for _, b := range m.Content {
				switch b.Type {
				case "tool_result":
					c := b.Content
					if b.IsError {
						c = "Error: " + c
					}
					msgs = append(msgs, oaiMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: strp(c)})
				case "text":
					text = append(text, b.Text)
				}
			}
			if len(text) > 0 {
				msgs = append(msgs, oaiMessage{Role: "user", Content: strp(strings.Join(text, "\n\n"))})
			}
		}
	}
	var tools []oaiTool
	for _, t := range req.Tools {
		var ot oaiTool
		ot.Type = "function"
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.InputSchema
		tools = append(tools, ot)
	}
	return msgs, tools
}

// buildOpenAI is a Chat Completions request to <base>/chat/completions, or
// to the base itself when it already ends in /chat/completions.
func buildOpenAI(p Provider, base string, req Request) (string, []byte, reader, error) {
	msgs, tools := toOpenAI(req)
	body := map[string]any{
		"model":          req.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	// OpenAI's own and Azure's newer models take max_completion_tokens and
	// refuse max_tokens; the servers that copy the API take max_tokens.
	if u, err := url.Parse(base); err == nil && (u.Host == "api.openai.com" || strings.HasSuffix(u.Host, ".openai.azure.com")) {
		body["max_completion_tokens"] = req.MaxTokens
	} else {
		body["max_tokens"] = req.MaxTokens
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", nil, nil, err
	}
	addr := strings.TrimSuffix(base, "/")
	if !strings.HasSuffix(addr, "/chat/completions") {
		addr += "/chat/completions"
	}
	return addr, b, readOpenAI, nil
}

// readOpenAI reads a streamed Chat Completion into the Messages API's
// shape: the text, then a tool_use block per tool call.
func readOpenAI(r io.Reader, onDelta func(Delta)) (Response, bool, error) {
	var (
		out     Response
		text    strings.Builder
		calls   = map[int]*oaiToolCall{}
		started bool
		finish  string
		done    bool
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var ch struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content   *string       `json:"content"`
					ToolCalls []oaiToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				PromptTokensDetails *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return out, started, fmt.Errorf("reading the model's answer: %w", err)
		}
		if ch.Error != nil {
			return out, started, &APIError{Status: 200, Type: ch.Error.Type, Message: ch.Error.Message}
		}
		if ch.Usage != nil {
			cached := 0
			if ch.Usage.PromptTokensDetails != nil {
				cached = ch.Usage.PromptTokensDetails.CachedTokens
			}
			out.Usage = Usage{InputTokens: ch.Usage.PromptTokens - cached, CacheReadInputTokens: cached, OutputTokens: ch.Usage.CompletionTokens}
		}
		for _, c := range ch.Choices {
			if c.Index != 0 {
				continue
			}
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				text.WriteString(*c.Delta.Content)
				started = true
				if onDelta != nil {
					onDelta(Delta{Text: *c.Delta.Content})
				}
			}
			for _, tc := range c.Delta.ToolCalls {
				i := len(calls)
				if tc.Index != nil {
					i = *tc.Index
				}
				cur := calls[i]
				if cur == nil {
					cur = &oaiToolCall{}
					calls[i] = cur
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					first := cur.Function.Name == ""
					cur.Function.Name += tc.Function.Name
					if first {
						started = true
						if onDelta != nil {
							onDelta(Delta{Tool: tc.Function.Name})
						}
					}
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				finish = *c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return out, started, fmt.Errorf("reading the model's answer: %w", err)
	}
	if !done && finish == "" {
		return out, started, errors.New("the model's answer ended early")
	}
	if text.Len() > 0 {
		out.Content = append(out.Content, Block{Type: "text", Text: text.String()})
	}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		tc := calls[i]
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return out, started, fmt.Errorf("the model sent arguments for %s that are not JSON", tc.Function.Name)
		}
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", n)
		}
		out.Content = append(out.Content, Block{Type: "tool_use", ID: id, Name: tc.Function.Name, Input: json.RawMessage(args)})
	}
	switch {
	case len(calls) > 0:
		out.StopReason = "tool_use"
	case finish == "length":
		out.StopReason = "max_tokens"
	default:
		out.StopReason = "end_turn"
	}
	return out, started, nil
}
