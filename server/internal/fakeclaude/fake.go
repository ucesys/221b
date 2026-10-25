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

// splitView takes the console's view state (P6U-03) out of a question.
func splitView(q string) (question, view string) {
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

// reDirective is a scripted tool call in a question: [[view_filter {...}]].
var reDirective = regexp.MustCompile(`\[\[([a-z_]+)(?:\s+(\{.*?\}))?\]\]`)

// scripted answers questions that script their tool calls, one per round,
// and questions about what the console shows; script answers the rest.
func scripted(question, view string, round int, results []string) reply {
	last := ""
	if len(results) > 0 {
		last = results[0]
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
