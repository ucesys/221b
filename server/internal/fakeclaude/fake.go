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
	"strconv"
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
	case strings.HasSuffix(r.URL.Path, "/chat/completions") && r.Method == http.MethodPost:
		f.chat(w, r)
		return
	case strings.HasSuffix(r.URL.Path, "/models") && r.Method == http.MethodGet:
		f.models(w, r)
		return
	}
	http.NotFound(w, r)
}

// Open is the path prefix of the fake's in-house endpoint, which takes no
// credential, like a vLLM server inside the network (Phase 12, wave 5):
// <fake>/open/v1 speaks the Chat Completions API and lists OpenModels.
const Open = "/open/"

// OpenModels are the models the in-house endpoint lists, with their
// context windows as vLLM gives them (max_model_len).
var OpenModels = map[string]int{"qwen3.8-27b": 16384, "llama-4-scout-17b": 8192}

// models lists the models: the in-house endpoint's, OpenAI-shaped as vLLM
// does, or the Claude API's with its key.
func (f *Fake) models(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID          string `json:"id"`
		Object      string `json:"object,omitempty"`
		Type        string `json:"type,omitempty"`
		OwnedBy     string `json:"owned_by,omitempty"`
		MaxModelLen int    `json:"max_model_len,omitempty"`
	}
	var list []model
	switch {
	case strings.Contains(r.URL.Path, Open):
		for id, n := range OpenModels {
			list = append(list, model{ID: id, Object: "model", OwnedBy: "vllm", MaxModelLen: n})
		}
	case r.Header.Get("x-api-key") == "" || strings.Contains(r.Header.Get("x-api-key"), "invalid"):
		apiError(w, 401, "authentication_error", "invalid x-api-key")
		return
	default:
		for _, id := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
			list = append(list, model{ID: id, Type: "model"})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{"object": "list", "data": list})
	w.Write(b)
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
	question, view := splitView(question)
	if strings.Contains(strings.ToLower(question), "overloaded") {
		apiError(w, 529, "overloaded_error", "Overloaded")
		return
	}
	reply := scripted(question, view, round, results)
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

// splitView takes the console's view state (P6U-03) out of a question,
// and a case's context (CF-47), which the scripts do not read.
func splitView(q string) (question, view string) {
	if i := strings.Index(q, "<case-context>"); i >= 0 {
		if j := strings.Index(q[i:], "</case-context>"); j >= 0 {
			q = strings.TrimSpace(q[:i] + q[i+j+len("</case-context>"):])
		}
	}
	i := strings.Index(q, "<console-view>")
	if i < 0 {
		return q, ""
	}
	j := strings.Index(q[i:], "</console-view>")
	if j < 0 {
		return q[:i], q[i+len("<console-view>"):]
	}
	return strings.TrimSpace(q[:i] + q[i+j+len("</console-view>"):]), strings.TrimSpace(q[i+len("<console-view>") : i+j])
}

// reChain is a scripted chain of hand-overs: [[chain troubleshooter,auditor]].
var reChain = regexp.MustCompile(`\[\[chain ([a-zA-Z,]*)\]\]`)

// reDirective is a scripted tool call in a question: [[view_filter {...}]].
var reDirective = regexp.MustCompile(`\[\[([a-z_]+)(?:\s+(\{.*?\}))?\]\]`)

// scripted answers questions that script their tool calls, one per round,
// and questions about what the console shows; script answers the rest.
func scripted(question, view string, round int, results []string) reply {
	last := ""
	if len(results) > 0 {
		last = results[0]
	}
	// A test's grading (Phase 12): a point is made when the answer says
	// it word for word, ignoring case.
	if strings.Contains(question, "<points>") && strings.Contains(question, "<answer>") {
		return reply{text: []string{grade(question)}}
	}
	// Mycroft's Wiki sync (KN-27, KN-28): a page for each source.
	if r, ok := syncReply(question); ok {
		return r
	}
	// A chain of hand-overs (Phase 12): [[chain a,b,c]] hands the rest of
	// the chain to a, which hands it to b, and so on; the last one answers.
	if m := reChain.FindStringSubmatch(question); m != nil {
		agents := strings.Split(m[1], ",")
		if agents[0] == "" {
			return reply{text: []string{"End of the chain."}}
		}
		if round == 0 {
			rest := strings.Join(agents[1:], ",")
			return reply{text: []string{"Asking " + agents[0] + "."}, tool: "hand_over", input: map[string]any{"agent": agents[0], "task": "[[chain " + rest + "]]", "context": "a test chain"}}
		}
		return reply{text: []string{"Chain: " + summarize(last)}}
	}
	if found := reDirective.FindAllStringSubmatch(question, -1); len(found) > 0 {
		// [[sleep 2000]] waits before the next call, as a slow model would.
		type call struct {
			tool, input string
			pause       time.Duration
		}
		var calls []call
		var pause time.Duration
		for _, c := range found {
			if c[1] == "sleep" {
				continue
			}
			calls = append(calls, call{tool: c[1], input: c[2], pause: pause})
			pause = 0
		}
		for _, c := range regexp.MustCompile(`\[\[sleep (\d+)\]\]`).FindAllStringSubmatchIndex(question, -1) {
			ms, _ := strconv.Atoi(question[c[2]:c[3]])
			// The pause goes before the first call after it.
			before := 0
			for _, d := range reDirective.FindAllStringSubmatchIndex(question[:c[0]], -1) {
				if question[d[2]:d[3]] != "sleep" {
					before++
				}
			}
			if before < len(calls) {
				calls[before].pause += time.Duration(ms) * time.Millisecond
			} else {
				pause += time.Duration(ms) * time.Millisecond
			}
		}
		if round < len(calls) {
			c := calls[round]
			var input any = map[string]any{}
			if c.input != "" {
				_ = json.Unmarshal([]byte(c.input), &input)
			}
			return reply{text: []string{fmt.Sprintf("Step %d.", round+1)}, tool: c.tool, input: input, pause: c.pause}
		}
		return reply{text: []string{uiSummary(last)}, pause: pause}
	}
	// Two of the evaluation's UI tasks, so its run against this fake
	// checks the stand-in window too.
	for phrase, call := range map[string][2]string{
		"only the control-plane nodes": {"view_open", `{"tab":"nodes","filters":{"role":"control-plane"}}`},
		"only the alerts about nodes":  {"view_open", `{"tab":"alerts","filters":{"kind":"nodes"}}`},
	} {
		if strings.Contains(strings.ToLower(question), phrase) {
			if round == 0 {
				var input any
				_ = json.Unmarshal([]byte(call[1]), &input)
				return reply{text: []string{"Opening it."}, tool: call[0], input: input}
			}
			return reply{text: []string{uiSummary(last)}}
		}
	}
	// Sherlock's setup skill (Phase 12, GS-07 to GS-10).
	if r, ok := setupReply(question, round, last); ok {
		return r
	}
	// Agents on their own (Phase 12, jobs): an investigation of a report,
	// a check of its fix, the nightly audit, an alert's diagnosis and the
	// daily digest, each ending with the JSON block the console reads.
	if r, ok := jobReply(question, round, last); ok {
		return r
	}
	// The home page's troubleshooting of a cluster with errors or warnings.
	if strings.Contains(strings.ToLower(question), "troubleshooting report for cluster") {
		if round == 0 {
			return reply{text: []string{"Reading the alerts."}, tool: "list_alerts", input: map[string]any{}}
		}
		return reply{text: []string{"**Summary**: the cluster's problems, most severe first."}}
	}
	// The home page's deficiency review.
	if strings.Contains(strings.ToLower(question), "deficiency review") {
		if round == 0 {
			return reply{text: []string{"Reviewing the cluster."}, tool: "review_cluster", input: map[string]any{}}
		}
		var r struct {
			Findings []struct {
				Area     string `json:"area"`
				Severity string `json:"severity"`
				Title    string `json:"title"`
				Count    int    `json:"count"`
			} `json:"findings"`
		}
		_ = json.Unmarshal([]byte(last), &r)
		var b strings.Builder
		fmt.Fprintf(&b, "**Deficiency review**: %d findings.\n\n", len(r.Findings))
		for i, f := range r.Findings {
			if i == 8 {
				break
			}
			fmt.Fprintf(&b, "- %s (%s, %s): %d\n", f.Title, f.Area, f.Severity, f.Count)
		}
		return reply{text: []string{b.String()}}
	}
	if strings.Contains(strings.ToLower(question), "this node") {
		var v struct {
			Details struct {
				Open string `json:"open"`
			} `json:"details"`
		}
		_ = json.Unmarshal([]byte(view), &v)
		if name, ok := strings.CutPrefix(v.Details.Open, "node:"); ok && name != "" {
			if round == 0 {
				return reply{tool: "get_node", input: map[string]any{"name": name}}
			}
			return reply{text: []string{fmt.Sprintf("[%s](221b://Node/%s), the node you have open: %s", name, name, summarize(last))}}
		}
		return reply{text: []string{"No node is open on your screen; name the node."}}
	}
	return script(question, round, results)
}

// reSyncSource is a source a Wiki sync gives Mycroft.
var reSyncSource = regexp.MustCompile(`<source key="([^"]+)" title="([^"]+)" version="([^"]*)" date="([^"]*)"`)

// CutMark in a source's text cuts a Wiki sync's answer at the output limit
// while its batch holds more than one source, as a model's answer to too
// much is (KN-27): the console splits the batch and asks each half again.
const CutMark = "[[cut-when-batched]]"

// syncReply answers a Wiki sync's batch as Mycroft does: one page for
// each source, naming it with its version and date.
func syncReply(question string) (reply, bool) {
	if !strings.HasPrefix(question, "The wiki's pages now") {
		return reply{}, false
	}
	type page struct {
		Path    string   `json:"path"`
		Title   string   `json:"title"`
		Sources []string `json:"sources"`
		Body    string   `json:"body"`
	}
	pages := []page{}
	found := reSyncSource.FindAllStringSubmatch(question, -1)
	for _, m := range found {
		slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(m[2]), "-"), "-")
		if slug == "" {
			continue
		}
		pages = append(pages, page{Path: "pages/system/" + slug + ".md", Title: m[2], Sources: []string{m[1]},
			Body: fmt.Sprintf("# %s\n\nWhat %s says (source: “%s”, %s).", m[2], m[2], m[2], m[4])})
	}
	b, _ := json.Marshal(map[string]any{"pages": pages, "hosts": []any{}, "fleets": []any{}})
	text := "```json\n" + string(b) + "\n```"
	if len(found) > 1 && strings.Contains(question, CutMark) {
		return reply{text: []string{text[:len(text)/2]}, cut: true}, true
	}
	return reply{text: []string{text}}, true
}

// uiSummary words the result of the last scripted call.
func uiSummary(last string) string {
	var r struct {
		Applied bool   `json:"applied"`
		Error   string `json:"error"`
		Note    string `json:"note"`
		Link    string `json:"link"`
		View    struct {
			Tab     string `json:"tab"`
			Details struct {
				State string `json:"state"`
			} `json:"details"`
		} `json:"view"`
	}
	if json.Unmarshal([]byte(last), &r) != nil {
		return "Done: " + summarize(last)
	}
	var b strings.Builder
	switch {
	case r.Error != "":
		b.WriteString("I could not: " + r.Error)
	case r.Note != "":
		b.WriteString("You changed the view yourself, so I left it as it is.")
	case r.Applied:
		b.WriteString("Done.")
		if r.View.Tab != "" {
			b.WriteString(" You are on " + r.View.Tab + ".")
		}
	default:
		b.WriteString("Done: " + summarize(last))
	}
	if r.View.Details.State == "not available" {
		b.WriteString(" That object is not available to you.")
	}
	if r.Link != "" {
		fmt.Fprintf(&b, " [Open it there](%s).", r.Link)
	}
	return b.String()
}

// reply is what the fake answers: text, then optionally a tool call.
type reply struct {
	text  []string
	tool  string
	input any
	slow  bool
	// pause is waited before the reply starts.
	pause time.Duration
	// cut ends the reply at the output limit, as a provider stops an
	// answer that would be longer (KN-27).
	cut bool
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
	case strings.Contains(q, "storage class"):
		// The Storage tab's classes (ST-22).
		if round == 0 {
			return reply{text: []string{"Reading the storage classes."}, tool: "list_storage_classes", input: map[string]any{}}
		}
		return reply{text: []string{"The storage classes: " + summarize(last)}}
	case strings.Contains(q, "claims") || strings.Contains(q, "volumes"):
		// The Storage tab's volumes and claims, with why each is not
		// healthy (ST-22, ST-14).
		if round == 0 {
			in := map[string]any{}
			if strings.Contains(q, "pending") || strings.Contains(q, "attention") {
				in["needs_attention"] = true
			}
			return reply{text: []string{"Reading the volumes and claims."}, tool: "list_volumes", input: in}
		}
		var r struct {
			Total   int `json:"total"`
			Volumes []struct {
				Claim  string `json:"claim"`
				Volume string `json:"volume"`
				Status string `json:"status"`
				Why    string `json:"why"`
			} `json:"volumes"`
		}
		_ = json.Unmarshal([]byte(last), &r)
		var b strings.Builder
		fmt.Fprintf(&b, "%d volumes and claims:\n\n", r.Total)
		for i, v := range r.Volumes {
			if i == 8 {
				break
			}
			if v.Claim != "" {
				ns, name, _ := strings.Cut(v.Claim, "/")
				fmt.Fprintf(&b, "- [%s](221b://PersistentVolumeClaim/%s/%s): %s", v.Claim, ns, name, v.Status)
			} else {
				fmt.Fprintf(&b, "- %s: %s", v.Volume, v.Status)
			}
			if v.Why != "" {
				b.WriteString(", " + v.Why)
			}
			b.WriteString("\n")
		}
		return reply{text: []string{b.String()}}
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
	if rep.pause > 0 {
		select {
		case <-time.After(rep.pause):
		case <-r.Context().Done():
			return
		}
	}
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
	if rep.cut {
		stop = "max_tokens"
	}
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

// chat serves the Chat Completions API with the same scripts, for a
// console set to an OpenAI-compatible provider (Phase 6 review): the
// conversation is read back into the Messages API's shape, and the reply
// streams as chat completion chunks.
func (f *Fake) chat(w http.ResponseWriter, r *http.Request) {
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
	auth := r.Header.Get("Authorization")
	if strings.Contains(r.URL.Path, Open) {
		// The in-house endpoint takes no credential.
		auth = "Bearer none"
	}
	if !strings.HasPrefix(auth, "Bearer ") || strings.Contains(auth, "invalid") {
		apiError(w, 401, "invalid_api_key", "Incorrect API key provided")
		return
	}
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role       string  `json:"role"`
			Content    *string `json:"content"`
			ToolCallID string  `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 || !req.Stream {
		apiError(w, 400, "invalid_request_error", "messages and stream are required")
		return
	}
	var msgs []message
	for _, m := range req.Messages {
		text := ""
		if m.Content != nil {
			text = *m.Content
		}
		switch m.Role {
		case "system":
		case "assistant":
			msgs = append(msgs, message{Role: "assistant", Content: []block{{Type: "text", Text: text}}})
		case "tool":
			c, _ := json.Marshal(text)
			b := block{Type: "tool_result", ToolUseID: m.ToolCallID, Content: c}
			if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && len(msgs[n-1].Content) > 0 && msgs[n-1].Content[0].Type == "tool_result" {
				msgs[n-1].Content = append(msgs[n-1].Content, b)
			} else {
				msgs = append(msgs, message{Role: "user", Content: []block{b}})
			}
		default:
			msgs = append(msgs, message{Role: "user", Content: []block{{Type: "text", Text: text}}})
		}
	}
	question, round, results := where(msgs)
	question, view := splitView(question)
	rep := scripted(question, view, round, results)
	if rep.pause > 0 {
		select {
		case <-time.After(rep.pause):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	out := 0
	for _, t := range rep.text {
		for _, piece := range chunks(t, 24) {
			send(chunk(map[string]any{"content": piece}, nil))
			out += len(piece) / 4
			if f.Delay > 0 {
				time.Sleep(f.Delay)
			}
		}
	}
	finish := "stop"
	if rep.cut {
		finish = "length"
	}
	if rep.tool != "" {
		args, _ := json.Marshal(rep.input)
		id := fmt.Sprintf("call_%d", time.Now().UnixNano())
		send(chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": rep.tool, "arguments": ""}}}}, nil))
		send(chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": string(args)}}}}, nil))
		finish = "tool_calls"
		out += 20
	}
	send(chunk(map[string]any{}, finish))
	send(map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "choices": []any{}, "usage": map[string]int{"prompt_tokens": len(body) / 4, "completion_tokens": out + 1, "total_tokens": len(body)/4 + out + 1}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// grade answers a grading request: {"points":[…]}, true for each numbered
// point the answer contains.
func grade(q string) string {
	answer := q
	if i, j := strings.Index(q, "<answer>"), strings.Index(q, "</answer>"); i >= 0 && j > i {
		answer = q[i+len("<answer>") : j]
	}
	answer = strings.ToLower(answer)
	var got []bool
	if i, j := strings.Index(q, "<points>"), strings.Index(q, "</points>"); i >= 0 && j > i {
		for _, line := range strings.Split(strings.TrimSpace(q[i+len("<points>"):j]), "\n") {
			_, point, ok := strings.Cut(line, ". ")
			if !ok {
				continue
			}
			got = append(got, strings.Contains(answer, strings.ToLower(strings.TrimSpace(point))))
		}
	}
	b, _ := json.Marshal(map[string]any{"points": got})
	return string(b)
}

// between is the text between <tag> and </tag>, or "".
func between(s, tag string) string {
	_, rest, ok := strings.Cut(s, "<"+tag+">")
	if !ok {
		return ""
	}
	in, _, _ := strings.Cut(rest, "</"+tag+">")
	return strings.TrimSpace(in)
}

// rechecked answers the fixes a task lists to re-check (W-1), one entry
// each: still there when one of the findings has its check and object
// ("Namespace/analytics" as the task writes it), gone otherwise.
func rechecked(question string, findings []any) []any {
	out := []any{}
	for _, line := range strings.Split(between(question, "recheck"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		check, rest, ok := strings.Cut(line, " on ")
		if !ok {
			continue
		}
		object, _, _ := strings.Cut(rest, ": ")
		if object == "the cluster as a whole" {
			object = ""
		}
		still := false
		for _, f := range findings {
			if m, ok := f.(map[string]any); ok && m["check"] == check && strings.EqualFold(fmt.Sprint(m["resource"]), object) {
				still = true
			}
		}
		evidence := "read " + object + " again: the problem is gone"
		if still {
			evidence = "read " + object + " again: the problem is still there"
		}
		out = append(out, map[string]any{"check": check, "resource": object, "still": still, "evidence": evidence})
	}
	return out
}

// jobBlock is an answer with the JSON block of an agent on its own.
func jobBlock(prose string, v any) reply {
	b, _ := json.Marshal(v)
	return reply{text: []string{prose + "\n\n```json\n" + string(b) + "\n```"}}
}

// jobReply answers the tasks of the jobs (Phase 12).
func jobReply(question string, round int, last string) (reply, bool) {
	q := strings.ToLower(question)
	switch {
	// --- Case work (Phase 12): a case's RCA drafted, and a case analysed:
	// a Deployment named is restarted.
	case strings.Contains(q, "<case-history>"):
		return jobBlock("Read the case's record.", map[string]any{"cause": "The pods behind it ran out of memory under load.", "fixApplied": "The deployment was restarted with a higher memory limit.", "prevention": "Alert on memory use above 80% of the limit."}), true
	case strings.Contains(q, "<case>"):
		object := ""
		for _, line := range strings.Split(between(question, "case"), "\n") {
			if v, ok := strings.CutPrefix(line, "object: "); ok {
				object = strings.TrimSpace(v)
			}
		}
		fix := map[string]any{"summary": "", "commands": []string{}}
		if parts := strings.Split(object, "/"); len(parts) == 3 && strings.EqualFold(parts[1], "deployment") {
			fix = map[string]any{"summary": "Restart " + parts[2], "commands": []string{"kubectl rollout restart deployment/" + parts[2] + " -n " + parts[0]}}
		}
		// A case about something asleep is the system working as intended
		// (CF-82): no problem, which lowers a case the console opened (CF-83).
		if strings.Contains(strings.ToLower(between(question, "case")), "asleep") {
			return jobBlock("**Case analysis**: the node was put to sleep on purpose.", map[string]any{"summary": "No change needed: the node sleeps on purpose.", "cause": "",
				"evidence": []string{"nodepower/status: asleep"}, "fix": map[string]any{"summary": "", "commands": []string{}}, "proposals": []any{},
				"expected": true, "expectedWhy": "Its node is annotated nodepower/status: asleep and was cordoned before it stopped reporting."}), true
		}
		return jobBlock("**Case analysis**: read the object, its events and its pods.", map[string]any{"summary": "The pods restart: they run out of memory.", "cause": "The memory limit is too low for the load.",
			"evidence": []string{"OOMKilled 4 times in the last hour"}, "fix": fix, "proposals": []any{}}), true
	// --- end of case work.
	case strings.Contains(q, "<report>") && strings.Contains(q, "marked fixed"):
		fixed := !strings.Contains(strings.ToLower(between(question, "report")), "still broken")
		return jobBlock("Looked again at what the investigation named.", map[string]any{"fixed": fixed, "evidence": []string{"the pods it named run and are ready"}, "technical": "Checked again."}), true
	case strings.Contains(q, "<report>"):
		report := strings.ToLower(between(question, "report"))
		service, confidence := "", 0
		for _, line := range strings.Split(between(question, "services"), "\n") {
			id, rest, ok := strings.Cut(line, ": ")
			name, _, _ := strings.Cut(rest, ";")
			name, _, _ = strings.Cut(name, " (")
			if ok && name != "" && strings.Contains(report, strings.ToLower(strings.TrimSpace(name))) {
				service, confidence = strings.TrimSpace(id), 80
				break
			}
		}
		out := map[string]any{"service": service, "confidence": confidence, "cause": "The pods behind it restart: they run out of memory.",
			"evidence": []string{"shop/web-7f9: OOMKilled 4 times in the last hour"}, "technical": "The web pods are OOMKilled under load; their memory limit is 256Mi.",
			"plain": "The shop is slow because part of it keeps restarting. We are looking into it.", "knownIssue": nil, "proposals": []any{}}
		if strings.Contains(report, "everyone") {
			out["knownIssue"] = map[string]any{"title": "The shop is slow", "symptoms": []string{"the shop is slow"}, "services": []string{service}, "workaround": "Try again in a few minutes."}
		}
		if strings.Contains(report, "restart") {
			out["proposals"] = []any{map[string]any{"title": "Restart the web pods", "reasoning": "They are stuck.", "commands": []string{"kubectl rollout restart deployment/web -n shop"}, "impact": "a rolling restart", "risk": "low"}}
		}
		return jobBlock("Read the alerts and the pods behind the service.", out), true
	case strings.Contains(q, "nightly audit"):
		if round == 0 {
			return reply{text: []string{"Reviewing the cluster."}, tool: "review_cluster", input: map[string]any{}}, true
		}
		// Its proposal names the finding it fixes (Phase 12, wave 6b), so
		// it resolves the finding's case. Its second finding is
		// information (P4), below the job's lowest priority by default:
		// the run keeps it as ignored (wave 6c, CF-33). Two more, also
		// information, name ids of the agent's own, alike in words, each
		// with its description (CF-86): the validation rules list them as
		// not on the list, and the housekeeping suggests them as one rule
		// once found in three runs (CF-85, CF-87).
		// It re-checks every fix it is given (W-1): still there when it
		// is one of its findings, gone otherwise.
		findings := []any{map[string]any{"severity": "warning", "check": "netpol.missing", "title": "Namespaces without network policies", "detail": "Pods there accept traffic from anywhere.", "evidence": []string{summarize(last)}, "resource": "Namespace/analytics"},
			map[string]any{"severity": "info", "check": "quota.missing", "title": "analytics has no resource quota", "detail": "A shared namespace without a quota can take the cluster's room.", "resource": "Namespace/analytics"},
			map[string]any{"severity": "info", "check": "other.node-clock-skew", "title": "The nodes' clocks differ by seconds", "detail": "Certificates and leases depend on the clocks agreeing.", "resource": "",
				"checkWhy": "The nodes' clocks are not in step with each other. No rule given covers time keeping on the nodes."},
			map[string]any{"severity": "info", "check": "other.node-clock-drift", "title": "A node's clock drifts", "detail": "Its time moves away from the others between syncs.", "resource": "",
				"checkWhy": "A node's clock drifts from true time between syncs. The rules given are about workloads and access, none about time."}}
		return jobBlock("**Nightly audit**: the review found what is below. "+summarize(last), map[string]any{"findings": findings,
			"proposals": []any{map[string]any{"title": "Mark analytics for a default-deny network policy", "reasoning": "Its pods accept traffic from anywhere.", "fixes": []any{1}, "commands": []string{"kubectl label namespace analytics e2e.221b/netpol=wanted"}, "impact": "a label on the namespace; no traffic changes yet", "risk": "low"}},
			"rechecked": rechecked(question, findings)}), true
	case strings.Contains(q, "<alert>"):
		alert := between(question, "alert")
		object := ""
		for _, line := range strings.Split(alert, "\n") {
			if v, ok := strings.CutPrefix(line, "object: "); ok {
				object = strings.TrimSpace(v)
			}
		}
		if round == 0 {
			return reply{text: []string{"Reading the events."}, tool: "list_events", input: map[string]any{}}, true
		}
		kind := "Pod"
		for _, line := range strings.Split(alert, "\n") {
			if v, ok := strings.CutPrefix(line, "kind: "); ok && strings.TrimSpace(v) != "" {
				kind = strings.TrimSpace(v)
			}
		}
		props := []any{}
		resource := kind + "/" + object
		if ns, name, ok := strings.Cut(object, "/"); ok {
			resource = ns + "/" + kind + "/" + name
			props = append(props, map[string]any{"title": "Delete the pod " + object + " so it starts again", "reasoning": "It is stuck.", "fixes": []any{1}, "commands": []string{"kubectl delete pod " + name + " -n " + ns}, "impact": "the pod is made again", "risk": "low"})
		}
		// An object asleep is expected (CF-82): an expected finding, no
		// case, the alert acknowledged.
		if strings.Contains(strings.ToLower(object), "asleep") {
			findings := []any{map[string]any{"severity": "critical", "check": "node.not-ready", "title": object + " stopped with its node", "detail": alert, "resource": resource,
				"expected": true, "expectedWhy": "Its node is annotated nodepower/status: asleep and was cordoned before it stopped reporting."}}
			return jobBlock("**Diagnosis** of "+object+": its node sleeps on purpose.", map[string]any{"findings": findings, "proposals": []any{}, "rechecked": rechecked(question, findings)}), true
		}
		findings := []any{map[string]any{"severity": "critical", "check": "pod.crashloop", "title": object + " fails to start", "detail": alert, "resource": resource}}
		return jobBlock("**Diagnosis** of "+object+": it fails to start.", map[string]any{"findings": findings, "proposals": props, "rechecked": rechecked(question, findings)}), true
	case strings.Contains(q, "daily digest"):
		n := 0
		if log := between(question, "audit-log"); log != "" {
			n = strings.Count(log, "\n") + 1
		}
		return jobBlock(fmt.Sprintf("**Digest**: %d records in the audit log; nothing needs anyone today.", n), map[string]any{"findings": []any{}, "proposals": []any{}}), true
	}
	return reply{}, false
}

// setupReply answers the setup skill's questions: what is next, from
// setup_status; a token pasted into the chat, refused; and which model
// the Auditor takes, from the knowledge base.
func setupReply(question string, round int, last string) (reply, bool) {
	q := strings.ToLower(strings.ReplaceAll(question, "’", "'"))
	switch {
	case strings.Contains(q, "what's next") || strings.Contains(q, "what is next"):
		if round == 0 {
			return reply{text: []string{"Reading Getting started."}, tool: "setup_status", input: map[string]any{}}, true
		}
		next := ""
		for _, line := range strings.Split(last, "\n") {
			if v, ok := strings.CutPrefix(line, "Next: "); ok {
				next = v
			}
		}
		if next == "" {
			return reply{text: []string{"Every step of Getting started is done or dismissed."}}, true
		}
		return reply{text: []string{"Next is " + next + " The other open steps are on Settings › Getting started. A password, key or token goes into its own field on that screen, never into this chat."}}, true
	case strings.Contains(q, "token") && strings.Contains(q, "set it up"):
		return reply{text: []string{"I will not take a token in the chat. Type it into the token field of the source on [Settings › Knowledge](#/admin/settings/knowledge), which keeps it in a Kubernetes Secret, and replace it if others could have seen it here."}}, true
	case strings.Contains(q, "which model") && strings.Contains(q, "auditor"):
		if round == 0 {
			return reply{text: []string{"Asking the knowledge base."}, tool: "ask_knowledge", input: map[string]any{"question": "Which model should the Auditor use?"}}, true
		}
		return reply{text: []string{"For the Auditor, a strong model with a long context: it reads a whole cluster's review at once. The knowledge base says: " + summarize(last)}}, true
	}
	return reply{}, false
}
