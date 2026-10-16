package sherlock

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/url"
	"strings"
)

// Amazon Bedrock's Converse API, which every Bedrock model with tool use
// speaks (Claude, Llama, Mistral, Nova, …): ConverseStream answers in AWS's
// binary event stream. Behind a gateway the address is the gateway's; the
// path stays Bedrock's, /model/<model>/converse-stream.

type cvContent struct {
	Text       *string       `json:"text,omitempty"`
	ToolUse    *cvToolUse    `json:"toolUse,omitempty"`
	ToolResult *cvToolResult `json:"toolResult,omitempty"`
}

type cvToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type cvToolResult struct {
	ToolUseID string      `json:"toolUseId"`
	Content   []cvContent `json:"content"`
	Status    string      `json:"status,omitempty"`
}

type cvMessage struct {
	Role    string      `json:"role"`
	Content []cvContent `json:"content"`
}

// toConverse translates the request; consecutive turns of one role are
// merged, as Converse wants them to alternate.
func toConverse(req Request) map[string]any {
	var msgs []cvMessage
	add := func(role string, c cvContent) {
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, c)
			return
		}
		msgs = append(msgs, cvMessage{Role: role, Content: []cvContent{c}})
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					add(role, cvContent{Text: strp(b.Text)})
				}
			case "tool_use":
				in := b.Input
				if len(in) == 0 {
					in = json.RawMessage("{}")
				}
				add(role, cvContent{ToolUse: &cvToolUse{ToolUseID: b.ID, Name: b.Name, Input: in}})
			case "tool_result":
				st := "success"
				if b.IsError {
					st = "error"
				}
				txt := b.Content
				if txt == "" {
					txt = "(empty)"
				}
				add(role, cvContent{ToolResult: &cvToolResult{ToolUseID: b.ToolUseID, Content: []cvContent{{Text: strp(txt)}}, Status: st}})
			}
		}
	}
	body := map[string]any{"messages": msgs, "inferenceConfig": map[string]int{"maxTokens": req.MaxTokens}}
	var sys []map[string]string
	for _, b := range req.System {
		if b.Text != "" {
			sys = append(sys, map[string]string{"text": b.Text})
		}
	}
	if len(sys) > 0 {
		body["system"] = sys
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"toolSpec": map[string]any{"name": t.Name, "description": t.Description, "inputSchema": map[string]json.RawMessage{"json": t.InputSchema}}})
		}
		body["toolConfig"] = map[string]any{"tools": tools}
	}
	return body
}

func buildBedrock(p Provider, req Request) (string, []byte, reader, error) {
	body, err := json.Marshal(toConverse(req))
	if err != nil {
		return "", nil, nil, err
	}
	addr := p.Endpoint
	if !strings.HasSuffix(addr, "/converse-stream") {
		addr += "/model/" + url.PathEscape(req.Model) + "/converse-stream"
	}
	return addr, body, readConverse, nil
}

// eventMessage is one message of AWS's event stream.
type eventMessage struct {
	headers map[string]string
	payload []byte
}

// readEvent reads one message: its total length, its headers' length and
// a CRC of those two, the headers, the payload, and a CRC of it all.
func readEvent(r *bufio.Reader) (eventMessage, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return eventMessage{}, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	hlen := binary.BigEndian.Uint32(prelude[4:8])
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return eventMessage{}, errors.New("the event stream is corrupt (prelude checksum)")
	}
	if total < 16 || total > 16<<20 || hlen > total-16 {
		return eventMessage{}, errors.New("the event stream is corrupt (lengths)")
	}
	rest := make([]byte, total-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		return eventMessage{}, err
	}
	crc := crc32.NewIEEE()
	crc.Write(prelude[:])
	crc.Write(rest[:len(rest)-4])
	if crc.Sum32() != binary.BigEndian.Uint32(rest[len(rest)-4:]) {
		return eventMessage{}, errors.New("the event stream is corrupt (message checksum)")
	}
	m := eventMessage{headers: map[string]string{}, payload: rest[hlen : len(rest)-4]}
	h := rest[:hlen]
	for len(h) > 0 {
		n := int(h[0])
		if len(h) < 2+n {
			return m, errors.New("the event stream is corrupt (header)")
		}
		name := string(h[1 : 1+n])
		typ := h[1+n]
		h = h[2+n:]
		size := 0
		switch typ {
		case 0, 1:
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 9:
			size = 16
		case 6, 7:
			if len(h) < 2 {
				return m, errors.New("the event stream is corrupt (header value)")
			}
			l := int(binary.BigEndian.Uint16(h[:2]))
			if len(h) < 2+l {
				return m, errors.New("the event stream is corrupt (header value)")
			}
			if typ == 7 {
				m.headers[name] = string(h[2 : 2+l])
			}
			h = h[2+l:]
			continue
		default:
			return m, fmt.Errorf("the event stream has a header of unknown type %d", typ)
		}
		if len(h) < size {
			return m, errors.New("the event stream is corrupt (header value)")
		}
		h = h[size:]
	}
	return m, nil
}

// readConverse reads ConverseStream's events into the Messages API's shape.
func readConverse(r io.Reader, onDelta func(Delta)) (Response, bool, error) {
	var (
		out     Response
		started bool
		stopped bool
		blocks  = map[int]*Block{}
		inputs  = map[int]*strings.Builder{}
		order   []int
	)
	br := bufio.NewReader(r)
	block := func(i int) *Block {
		if b := blocks[i]; b != nil {
			return b
		}
		b := &Block{Type: "text"}
		blocks[i] = b
		order = append(order, i)
		return b
	}
	for {
		m, err := readEvent(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, started, fmt.Errorf("reading Amazon Bedrock's answer: %w", err)
		}
		if m.headers[":message-type"] == "exception" {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(m.payload, &e)
			typ := m.headers[":exception-type"]
			status := 500
			if strings.Contains(typ, "Throttling") {
				status = 429
			} else if strings.Contains(typ, "Validation") {
				status = 400
			}
			return out, started, &APIError{Status: status, Type: typ, Message: e.Message}
		}
		var ev struct {
			ContentBlockIndex int `json:"contentBlockIndex"`
			Start             struct {
				ToolUse *struct {
					ToolUseID string `json:"toolUseId"`
					Name      string `json:"name"`
				} `json:"toolUse"`
			} `json:"start"`
			Delta struct {
				Text    *string `json:"text"`
				ToolUse *struct {
					Input string `json:"input"`
				} `json:"toolUse"`
			} `json:"delta"`
			StopReason string `json:"stopReason"`
			Usage      *struct {
				InputTokens           int `json:"inputTokens"`
				OutputTokens          int `json:"outputTokens"`
				CacheReadInputTokens  int `json:"cacheReadInputTokens"`
				CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(m.payload, &ev)
		switch m.headers[":event-type"] {
		case "contentBlockStart":
			if tu := ev.Start.ToolUse; tu != nil {
				b := block(ev.ContentBlockIndex)
				b.Type, b.ID, b.Name = "tool_use", tu.ToolUseID, tu.Name
				inputs[ev.ContentBlockIndex] = &strings.Builder{}
				started = true
				if onDelta != nil {
					onDelta(Delta{Tool: tu.Name})
				}
			}
		case "contentBlockDelta":
			b := block(ev.ContentBlockIndex)
			if ev.Delta.Text != nil {
				b.Text += *ev.Delta.Text
				started = true
				if onDelta != nil && *ev.Delta.Text != "" {
					onDelta(Delta{Text: *ev.Delta.Text})
				}
			}
			if ev.Delta.ToolUse != nil {
				if in := inputs[ev.ContentBlockIndex]; in != nil {
					in.WriteString(ev.Delta.ToolUse.Input)
				}
			}
		case "messageStop":
			stopped = true
			out.StopReason = ev.StopReason
		case "metadata":
			if u := ev.Usage; u != nil {
				out.Usage = Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheWriteInputTokens}
			}
		}
	}
	if !stopped {
		return out, started, errors.New("Amazon Bedrock's answer ended early")
	}
	for _, i := range order {
		b := *blocks[i]
		if b.Type == "tool_use" {
			in := strings.TrimSpace(inputs[i].String())
			if in == "" {
				in = "{}"
			}
			if !json.Valid([]byte(in)) {
				return out, started, fmt.Errorf("the model sent input for %s that is not JSON", b.Name)
			}
			b.Input = json.RawMessage(in)
		} else if b.Text == "" {
			continue
		}
		out.Content = append(out.Content, b)
	}
	switch out.StopReason {
	case "tool_use", "max_tokens", "end_turn":
	case "":
		out.StopReason = "end_turn"
	default:
		out.StopReason = "end_turn"
	}
	return out, started, nil
}
