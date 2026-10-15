// Package fakeclaude is a stand-in for the Claude Messages API, for the
// simulator and the tests: it streams scripted answers that call
// Sherlock's tools the way a model would, and records every request so a
// test can check what would have been sent (P5-03).
package fakeclaude

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Fake is the fake API.
type Fake struct {
	mu       sync.Mutex
	requests []json.RawMessage
	// Delay is the pause between streamed pieces.
	Delay time.Duration
}

// New returns a fake.
func New() *Fake { return &Fake{Delay: 15 * time.Millisecond} }

// MaxRequests is how many requests are kept for inspection.
const MaxRequests = 500

// Requests returns the request bodies received, oldest first.
func (f *Fake) Requests() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.requests...)
}

// Reset forgets the requests.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
}

// ServeHTTP serves POST /v1/messages, and GET or DELETE /requests for
// tests.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/requests") && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(f.Requests())
		w.Write(b)
		return
	case strings.HasSuffix(r.URL.Path, "/requests") && r.Method == http.MethodDelete:
		f.Reset()
		w.WriteHeader(http.StatusNoContent)
		return
	case strings.HasSuffix(r.URL.Path, "/v1/messages") && r.Method == http.MethodPost:
		f.messages(w, r)
		return
	}
	http.NotFound(w, r)
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Tools    []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func apiError(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
	w.Write(b)
}

func (f *Fake) messages(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&body); err != nil {
		apiError(w, 400, "invalid_request_error", "the body is not JSON")
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, body)
	if len(f.requests) > MaxRequests {
		f.requests = f.requests[len(f.requests)-MaxRequests:]
	}
	f.mu.Unlock()
	key := r.Header.Get("x-api-key")
	if key == "" || strings.Contains(key, "invalid") {
		apiError(w, 401, "authentication_error", "invalid x-api-key")
		return
	}
	if r.Header.Get("anthropic-version") == "" {
		apiError(w, 400, "invalid_request_error", "anthropic-version header is required")
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		apiError(w, 400, "invalid_request_error", "messages: at least one message is required")
		return
	}
	if !req.Stream {
		apiError(w, 400, "invalid_request_error", "this fake only streams")
		return
	}
	if msg := checkThinking(body); msg != "" {
		apiError(w, 400, "invalid_request_error", msg)
		return
	}
	question, round, results := where(req.Messages)
	if strings.Contains(strings.ToLower(question), "overloaded") {
		apiError(w, 529, "overloaded_error", "Overloaded")
		return
	}
	reply := script(question, round, results)
	f.stream(w, r, len(body), reply)
}

// sigPrefix starts the signature of every thinking block the fake sends.
const sigPrefix = "fakesig-"

// checkThinking holds a request to what the API asks of the thinking
// blocks it sent: back exactly as they were, a thinking block with its
// thinking (empty, as the models send it by default) and signature, a
// redacted one with its data.
func checkThinking(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	for i, m := range req.Messages {
		var blocks []map[string]json.RawMessage
		if m.Role != "assistant" || json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for j, b := range blocks {
			var typ, sig, data string
			_ = json.Unmarshal(b["type"], &typ)
			switch typ {
			case "thinking":
				if _, ok := b["thinking"]; !ok {
					return fmt.Sprintf("messages.%d.content.%d.thinking.thinking: Field required", i, j)
				}
				if json.Unmarshal(b["signature"], &sig) != nil || !strings.HasPrefix(sig, sigPrefix) {
					return fmt.Sprintf("messages.%d.content.%d: Invalid signature in thinking block", i, j)
				}
			case "redacted_thinking":
				if json.Unmarshal(b["data"], &data) != nil || !strings.HasPrefix(data, sigPrefix) {
					return fmt.Sprintf("messages.%d.content.%d.redacted_thinking.data: Field required", i, j)
				}
			}
		}
	}
	return ""
}

// where reads the conversation: the question being answered, how many
// tool rounds it has had, and the latest tool results.
func where(msgs []message) (question string, round int, results []string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" {
			round++
			continue
		}
		text := ""
		for _, b := range m.Content {
			if b.Type == "text" {
				text += b.Text
			}
		}
		if text != "" {
			return text, round, results
		}
		if i == len(msgs)-1 {
			for _, b := range m.Content {
				if b.Type == "tool_result" {
					var s string
					if json.Unmarshal(b.Content, &s) != nil {
						s = string(b.Content)
					}
					results = append(results, s)
				}
			}
		}
	}
	return "", round, results
}

// reply is what the fake answers: text, then optionally a tool call.
type reply struct {
	text  []string
	tool  string
	input any
	slow  bool
}

var (
	reLogs  = regexp.MustCompile(`logs? of (?:pod )?([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9.-]*)`)
	rePod   = regexp.MustCompile(`([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9.-]*[a-z0-9])`)
	reNS    = regexp.MustCompile(`namespace ([a-z0-9][a-z0-9-]*)`)
	reWho   = regexp.MustCompile(`who can ([a-z]+) ([a-z/]+)`)
	reNode  = regexp.MustCompile(`node ([a-z0-9][a-z0-9.-]*)`)
	reTrace = regexp.MustCompile(`trace .*?([a-z0-9-]+(?:\.[a-z0-9-]+)+)`)
	reObj   = regexp.MustCompile(`(secret|deployment|configmap|statefulset|persistentvolumeclaim|service) ([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9.-]*[a-z0-9])`)
	reRel   = regexp.MustCompile(`release ([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9.-]*[a-z0-9])`)
)

func script(question string, round int, results []string) reply {
	q := strings.ToLower(question)
	last := ""
	if len(results) > 0 {
		last = results[0]
	}
	switch {
	case strings.Contains(q, "slowly"):
		var parts []string
		for i := 1; i <= 50; i++ {
			parts = append(parts, fmt.Sprintf("Part %d of a long answer. ", i))
		}
		return reply{text: parts, slow: true}
	case reLogs.MatchString(q):
		m := reLogs.FindStringSubmatch(q)
		if round == 0 {
			return reply{text: []string{"Reading the log."}, tool: "pod_logs", input: map[string]any{"namespace": m[1], "name": m[2], "tail": 50}}
		}
		lines := strings.Split(strings.TrimSpace(last), "\n")
		if len(lines) > 5 {
			lines = lines[:5]
		}
		return reply{text: []string{fmt.Sprintf("The log of [%s/%s](221b://Pod/%s/%s) starts:\n\n```\n%s\n```\n", m[1], m[2], m[1], m[2], strings.Join(lines, "\n"))}}
	case reWho.MatchString(q):
		m := reWho.FindStringSubmatch(q)
		if round == 0 {
			return reply{tool: "who_can", input: map[string]any{"verb": m[1], "resource": m[2]}}
		}
		var r struct {
			Who []struct {
				Subject string `json:"subject"`
				Binding string `json:"binding"`
			} `json:"who"`
		}
		_ = json.Unmarshal([]byte(last), &r)
		var b strings.Builder
		fmt.Fprintf(&b, "%d subjects can %s %s:\n\n", len(r.Who), m[1], m[2])
		for i, w := range r.Who {
			if i == 8 {
				break
			}
			fmt.Fprintf(&b, "- %s, through %s\n", w.Subject, w.Binding)
		}
		return reply{text: []string{b.String()}}
	case reTrace.MatchString(q):
		m := reTrace.FindStringSubmatch(q)
		if round == 0 {
			return reply{tool: "trace_ingress", input: map[string]any{"host": m[1]}}
		}
		return reply{text: []string{"The trace of " + m[1] + ": " + summarize(last)}}
	case reRel.MatchString(q):
		m := reRel.FindStringSubmatch(q)
		if round == 0 {
			return reply{tool: "get_helm_release", input: map[string]any{"namespace": m[1], "name": m[2]}}
		}
		return reply{text: []string{fmt.Sprintf("[Release %s/%s](221b://HelmRelease/%s/%s): %s", m[1], m[2], m[1], m[2], summarize(last))}}
	case reObj.MatchString(q):
		m := reObj.FindStringSubmatch(q)
		kinds := map[string]string{"secret": "Secret", "deployment": "Deployment", "configmap": "ConfigMap", "statefulset": "StatefulSet", "persistentvolumeclaim": "PersistentVolumeClaim", "service": "Service"}
		k := kinds[m[1]]
		if round == 0 {
			if k == "Service" {
				return reply{tool: "get_service", input: map[string]any{"namespace": m[2], "name": m[3]}}
			}
			return reply{tool: "get_object", input: map[string]any{"kind": k, "namespace": m[2], "name": m[3]}}
		}
		return reply{text: []string{fmt.Sprintf("[%s %s/%s](221b://%s/%s/%s): %s", k, m[2], m[3], k, m[2], m[3], summarize(last))}}
	case reNS.MatchString(q):
		m := reNS.FindStringSubmatch(q)
		if round == 0 {
			return reply{text: []string{"Listing the pods."}, tool: "list_pods", input: map[string]any{"namespace": m[1], "limit": 10}}
		}
		var r struct {
			Total int `json:"total"`
			Pods  []struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				Status    string `json:"status"`
			} `json:"pods"`
		}
		_ = json.Unmarshal([]byte(last), &r)
		if r.Total == 0 {
			return reply{text: []string{"I could not see any pods in " + m[1] + ". " + summarize(last)}}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s has %d pods. The first:\n\n", m[1], r.Total)
		for i, p := range r.Pods {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "- [%s](221b://Pod/%s/%s): %s\n", p.Name, p.Namespace, p.Name, p.Status)
		}
		return reply{text: []string{b.String()}}
	case reNode.MatchString(q) && !rePod.MatchString(q):
		m := reNode.FindStringSubmatch(q)
		name := strings.TrimRight(m[1], ".?")
		if round == 0 {
			return reply{tool: "get_node", input: map[string]any{"name": name}}
		}
		return reply{text: []string{fmt.Sprintf("[%s](221b://Node/%s): %s", name, name, summarize(last))}}
	case rePod.MatchString(q):
		m := rePod.FindStringSubmatch(q)
		if round == 0 {
			return reply{text: []string{"Let me look at the pod."}, tool: "get_pod", input: map[string]any{"namespace": m[1], "name": m[2]}}
		}
		var p struct {
			Status        string `json:"status"`
			Node          string `json:"node"`
			Unschedulable string `json:"unschedulable"`
			Events        []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"events"`
		}
		if json.Unmarshal([]byte(last), &p) != nil || p.Status == "" {
			return reply{text: []string{"I could not read that pod: " + summarize(last)}}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[%s/%s](221b://Pod/%s/%s) is %s", m[1], m[2], m[1], m[2], p.Status)
		if p.Node != "" {
			fmt.Fprintf(&b, " on [%s](221b://Node/%s)", p.Node, p.Node)
		}
		b.WriteString(".")
		if p.Unschedulable != "" {
			b.WriteString(" The scheduler says: " + p.Unschedulable)
		}
		if len(p.Events) > 0 {
			fmt.Fprintf(&b, "\n\nThe latest event: %s: %s", p.Events[0].Reason, p.Events[0].Message)
		}
		fmt.Fprintf(&b, "\n\nTo look closer:\n\n```sh\nkubectl -n %s describe pod %s\n```\n", m[1], m[2])
		return reply{text: []string{b.String()}}
	default:
		if round == 0 {
			return reply{text: []string{"Reading the alerts."}, tool: "list_alerts", input: map[string]any{}}
		}
		var r struct {
			Total  int `json:"total"`
			Alerts []struct {
				Kind      string `json:"kind"`
				Severity  string `json:"severity"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				Status    string `json:"status"`
				Why       string `json:"why"`
				Waiting   string `json:"waiting"`
			} `json:"alerts"`
		}
		_ = json.Unmarshal([]byte(last), &r)
		var b strings.Builder
		fmt.Fprintf(&b, "There are %d alerts.", r.Total)
		if len(r.Alerts) > 0 {
			b.WriteString(" The most urgent:\n\n")
		}
		for i, a := range r.Alerts {
			if i == 4 {
				break
			}
			link := fmt.Sprintf("[%s](221b://Node/%s)", a.Name, a.Name)
			if a.Kind == "pod" {
				link = fmt.Sprintf("[%s/%s](221b://Pod/%s/%s)", a.Namespace, a.Name, a.Namespace, a.Name)
			}
			why := firstOf(a.Why, a.Waiting, a.Status)
			fmt.Fprintf(&b, "- %s: %s, %s\n", link, a.Severity, why)
		}
		// A link to something no tool showed, which the console must not
		// keep as a link.
		b.WriteString("\nNothing is wrong with [ghost](221b://Pod/nowhere/ghost).\n")
		return reply{text: []string{b.String()}}
	}
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func summarize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

func (f *Fake) stream(w http.ResponseWriter, r *http.Request, inBytes int, rep reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	send := func(event string, v any) bool {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		if fl != nil {
			fl.Flush()
		}
		return r.Context().Err() == nil
	}
	pause := func(d time.Duration) bool {
		select {
		case <-r.Context().Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	inTokens := inBytes / 4
	cacheRead := 0
	if inTokens > 2000 {
		cacheRead = 2000
		inTokens -= 2000
	}
	if !send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fake", "type": "message", "role": "assistant", "model": "fake", "content": []any{}, "usage": map[string]int{"input_tokens": inTokens, "output_tokens": 1, "cache_read_input_tokens": cacheRead}}}) {
		return
	}
	idx, out := 0, 0
	// The models think first, and by default keep the thinking to
	// themselves: an empty thinking block with its signature. Between tool
	// calls some of it may come redacted.
	sig := fmt.Sprintf("%s%d", sigPrefix, time.Now().UnixNano())
	send("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
	send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "thinking_delta", "thinking": ""}})
	send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "signature_delta", "signature": sig}})
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
	idx++
	out += 20
	if rep.tool != "" {
		send("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "redacted_thinking", "data": sig + "-redacted"}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	if len(rep.text) > 0 {
		send("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "text", "text": ""}})
		for _, t := range rep.text {
			// Words a few at a time, as a model streams them.
			for _, piece := range chunks(t, 24) {
				d := f.Delay
				if rep.slow {
					d = 250 * time.Millisecond
				}
				if !pause(d) {
					return
				}
				if !send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "text_delta", "text": piece}}) {
					return
				}
				out += len(piece)/4 + 1
			}
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	stop := "end_turn"
	if rep.tool != "" {
		in, _ := json.Marshal(rep.input)
		send("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_fake_%d", time.Now().UnixNano()), "name": rep.tool, "input": map[string]any{}}})
		half := len(in) / 2
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(in[:half])}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(in[half:])}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		stop = "tool_use"
		out += len(in) / 4
	}
	send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": out}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := n
		for cut < len(s) && s[cut] != ' ' && s[cut] != '\n' {
			cut++
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
