package sherlock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Effort (phase-13.1-ai, AI-43): how much a model thinks before it
// answers. An agent sends none (its model's default, as before), a level
// it is set to, or one Auto picks for each message. On the Messages API
// it is output_config.effort, sent only to a model known to take that
// level; on an OpenAI-compatible server it is the chat template's
// enable_thinking: off at low, on above (the owner, November 22, for the
// A5000's Qwen).

// OutputConfig is a request's output_config, or a mid-conversation system
// message's.
type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// BetaMidConversationEffort is the beta that lets an empty system message
// change the effort for the turns after it without costing the prompt
// cache (platform.claude.com, per-message effort).
const BetaMidConversationEffort = "mid-conversation-output-config-2026-07-01"

// The effort levels, lowest first.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// effortModel is what a Claude model takes: its levels, and whether it
// takes a change of effort mid-conversation (the beta).
type effortModel struct {
	prefix     string
	levels     []string
	perMessage bool
}

// effortModels are the Claude models known to take effort, by the prefix
// of their id; the first that matches answers. A model not here is sent
// no effort, so no request is refused for it.
var effortModels = []effortModel{
	{"claude-fable-5", effortLevels, true},
	{"claude-mythos-5", effortLevels, true},
	{"claude-opus-5", effortLevels, true},
	{"claude-sonnet-5-5", effortLevels, true},
	{"claude-haiku-5-5", effortLevels, true},
	{"claude-sonnet-5", effortLevels, false},
	{"claude-opus-4-8", effortLevels, false},
	{"claude-opus-4-7", effortLevels, false},
	{"claude-opus-4-6", []string{"low", "medium", "high", "max"}, false},
	{"claude-sonnet-4-6", []string{"low", "medium", "high", "max"}, false},
	{"claude-opus-4-5", []string{"low", "medium", "high"}, false},
}

// claudeModel strips the prefixes a gateway or a cloud puts before a
// Claude model's id: "anthropic.", "us.anthropic.", a path.
func claudeModel(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, "claude-"); i > 0 {
		m = m[i:]
	}
	return m
}

func effortOf(model string) (effortModel, bool) {
	m := claudeModel(model)
	for _, e := range effortModels {
		if strings.HasPrefix(m, e.prefix) {
			return e, true
		}
	}
	return effortModel{}, false
}

// EffortFor is the effort a Claude model is sent for a level: the level
// when it takes it, the nearest level below that it takes (xhigh is high
// on a model without xhigh), or "" when it takes no effort or the level
// is none.
func EffortFor(model, level string) string {
	r := slices.Index(effortLevels, level)
	if r < 0 {
		return ""
	}
	e, ok := effortOf(model)
	if !ok {
		return ""
	}
	for ; r >= 0; r-- {
		if slices.Contains(e.levels, effortLevels[r]) {
			return effortLevels[r]
		}
	}
	return ""
}

// PerMessageEffort says whether a model takes a change of effort mid-
// conversation, as an empty system message with output_config.
func PerMessageEffort(model string) bool {
	e, ok := effortOf(model)
	return ok && e.perMessage
}

// EffortMessage is the empty system message that changes the effort for
// the reply to the user turn before it and the turns after.
func EffortMessage(level string) Message {
	return Message{Role: "system", Content: []Block{}, OutputConfig: &OutputConfig{Effort: level}}
}

// IsEffortMessage says a message is one of EffortMessage's.
func IsEffortMessage(m Message) bool {
	return m.Role == "system" && m.OutputConfig != nil && len(m.Content) == 0
}

// EffortInForce is the effort a request runs at: the last effort message's,
// or the top-level one's.
func EffortInForce(req Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if IsEffortMessage(req.Messages[i]) {
			return req.Messages[i].OutputConfig.Effort
		}
	}
	if req.OutputConfig != nil {
		return req.OutputConfig.Effort
	}
	return ""
}

// withoutEffortMessages is the messages without the effort messages.
func withoutEffortMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if !IsEffortMessage(m) {
			out = append(out, m)
		}
	}
	return out
}

// claudeEffort readies a Messages API request's effort for its model: each
// level the nearest it takes; the effort messages kept, with their beta,
// where perMessage allows and the model takes them, otherwise folded into
// the top-level effort (a change of which costs the prompt cache); none
// at all for a model that takes no effort.
func claudeEffort(req Request, perMessage bool) Request {
	hasMsgs := slices.ContainsFunc(req.Messages, IsEffortMessage)
	if req.OutputConfig == nil && !hasMsgs {
		return req
	}
	if perMessage && hasMsgs && PerMessageEffort(req.Model) {
		msgs := make([]Message, 0, len(req.Messages))
		for _, m := range req.Messages {
			if IsEffortMessage(m) {
				e := EffortFor(req.Model, m.OutputConfig.Effort)
				if e == "" {
					continue
				}
				m = EffortMessage(e)
			}
			msgs = append(msgs, m)
		}
		req.Messages = msgs
		if req.OutputConfig != nil {
			if e := EffortFor(req.Model, req.OutputConfig.Effort); e != "" {
				req.OutputConfig = &OutputConfig{Effort: e}
			} else {
				req.OutputConfig = nil
			}
		}
		if !slices.Contains(req.Betas, BetaMidConversationEffort) {
			req.Betas = append(slices.Clone(req.Betas), BetaMidConversationEffort)
		}
		return req
	}
	e := EffortFor(req.Model, EffortInForce(req))
	req.Messages = withoutEffortMessages(req.Messages)
	req.OutputConfig = nil
	if e != "" {
		req.OutputConfig = &OutputConfig{Effort: e}
	}
	return req
}

// openAIThinking is what an OpenAI-compatible server is told of the
// effort: enable_thinking off at low and on above, in the chat template's
// arguments that vLLM and SGLang read; nothing when no effort is set, and
// nothing to OpenAI's own API and Azure's, which refuse arguments they do
// not know.
func openAIThinking(base string, req Request) (map[string]any, bool) {
	e := EffortInForce(req)
	if e == "" || !slices.Contains(effortLevels, e) {
		return nil, false
	}
	if u, err := url.Parse(base); err == nil && (u.Host == "api.openai.com" || strings.HasSuffix(u.Host, ".openai.azure.com")) {
		return nil, false
	}
	return map[string]any{"enable_thinking": e != "low"}, true
}

// ChooseOne asks an OpenAI-compatible model for one word of choices (the
// classifier, AI-45): vLLM's structured_outputs choice, thinking off, at
// most five tokens, temperature 0, without streaming. It returns the
// answer as given and what it used.
func (c *Client) ChooseOne(ctx context.Context, model, system, text string, choices []string) (string, Usage, error) {
	p := c.provider()
	if p.Kind != KindOpenAI {
		return "", Usage{}, errors.New("the classifier runs on an OpenAI-compatible provider")
	}
	body, err := json.Marshal(map[string]any{
		"model":                model,
		"messages":             []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": text}},
		"max_tokens":           5,
		"temperature":          0,
		"structured_outputs":   map[string]any{"choice": choices},
		"chat_template_kwargs": map[string]bool{"enable_thinking": false},
	})
	if err != nil {
		return "", Usage{}, err
	}
	addr := strings.TrimSuffix(p.Endpoint, "/")
	if !strings.HasSuffix(addr, "/chat/completions") {
		addr += "/chat/completions"
	}
	if p.Query != "" {
		addr += "?" + p.Query
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	for k, v := range p.Headers {
		hr.Header.Set(k, v)
	}
	if err := c.authorize(ctx, p, hr, body); err != nil {
		return "", Usage{}, err
	}
	hc, err := c.httpClient(p)
	if err != nil {
		return "", Usage{}, err
	}
	res, err := hc.Do(hr)
	if err != nil {
		return "", Usage{}, fmt.Errorf("reaching %s: %w", p.Name(), err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		e := apiError(res.StatusCode, b)
		e.Provider = p.Name()
		return "", Usage{}, e
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", Usage{}, fmt.Errorf("reading %s's answer: %w", p.Name(), err)
	}
	u := Usage{InputTokens: out.Usage.Prompt, OutputTokens: out.Usage.Completion}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == nil {
		return "", u, fmt.Errorf("%s gave no answer", p.Name())
	}
	return *out.Choices[0].Message.Content, u, nil
}

// DayShare is the share of the day's budget spent that is the closest to
// its limit: the total's, the agent's provider's or the agent's own; 0
// when none has a daily limit (AI-46).
func DayShare(total Caps, s Spend, a *Agent) float64 {
	share := 0.0
	if total.DailyUSD > 0 {
		share = max(share, s.DayUSD/total.DailyUSD)
	}
	if a == nil {
		return share
	}
	if a.Provider != "" && a.ProviderCaps.DailyUSD > 0 {
		share = max(share, s.Provider(a.Provider).DayUSD/a.ProviderCaps.DailyUSD)
	}
	if a.Caps.DailyUSD > 0 {
		share = max(share, s.Agent(a.ID).DayUSD/a.Caps.DailyUSD)
	}
	return share
}

// Chooser picks an agent's effort and model for a conversation's message,
// or for a task handed to it (AI-45): package ai's Auto.
type Chooser interface {
	Choose(ctx context.Context, in ChooseInput) Choice
}

// ChooseInput is what a pick is made from.
type ChooseInput struct {
	// Text is the message, or the task handed over.
	Text string
	// Model is the model the conversation started on with Auto, "" on its
	// first message; MovedUp says it was moved up already.
	Model   string
	MovedUp bool
	// DayShare is the share of the day's budget spent (DayShare).
	DayShare float64
	// Inherit, set in a job's run, is the effort of the agent that handed
	// the task: picked by rules, never by the classifier (the owner).
	Inherit string
	// Single says one request outside a conversation and a run (Complete):
	// the defaults, no classifier.
	Single bool
}

// Choice is a pick: the model (with the price its spend counts at) and
// the effort, how and why, what the classifier's call cost and on which
// provider, and the model it moved up from.
type Choice struct {
	Model    string
	Price    Model
	Effort   string
	By       string
	Why      string
	CostUSD  float64
	Provider string
	// MovedUpFrom is the model this message moved the conversation up
	// from; "" when it did not.
	MovedUpFrom string
}

// betasOf is a Messages API request's anthropic-beta header: its betas,
// with the effort's once readied for its model.
func betasOf(req Request) string {
	return strings.Join(claudeEffort(req, true).Betas, ",")
}
