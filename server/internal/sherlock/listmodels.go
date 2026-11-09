package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// The models a provider's endpoint lists, for the model field's dropdown
// in Settings › Providers (Phase 12, wave 5, AS-05): an OpenAI-compatible
// endpoint's /models (with vLLM's max_model_len, the context window, when
// it says), Ollama's /api/tags when there is no /models, the Claude API's
// /v1/models, and Amazon Bedrock's foundation models. Vertex AI lists
// none here: its model is typed.

// ModelInfo is a model an endpoint lists.
type ModelInfo struct {
	ID string `json:"id"`
	// ContextWindow is its context in tokens, when the endpoint says.
	ContextWindow int    `json:"contextWindow,omitempty"`
	Owner         string `json:"owner,omitempty"`
}

// ErrNoModelList is a provider whose models are not listed here.
var ErrNoModelList = errors.New("this provider does not list its models here: type the model's name")

// maxListed bounds the models listed.
const maxListed = 500

// Models lists the models the provider's endpoint offers, sorted by id.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	p := c.provider()
	var list []ModelInfo
	var err error
	switch p.Kind {
	case KindOpenAI:
		base := strings.TrimSuffix(strings.TrimSuffix(p.Endpoint, "/chat/completions"), "/")
		list, err = c.listOpenAI(ctx, p, base+"/models")
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			// Ollama answers /api/tags at its root, beside /v1.
			list, err = c.listOllama(ctx, p, strings.TrimSuffix(base, "/v1")+"/api/tags")
		}
	case KindBedrock:
		if p.custom {
			return nil, ErrNoModelList
		}
		region := p.Region
		if region == "" {
			region = regionOf(strings.TrimPrefix(strings.TrimPrefix(p.Endpoint, "https://"), "http://"))
		}
		list, err = c.listBedrock(ctx, p, "https://bedrock."+region+".amazonaws.com/foundation-models?byOutputModality=TEXT")
	case KindVertex:
		return nil, ErrNoModelList
	default:
		base := strings.TrimSuffix(strings.TrimSuffix(p.Endpoint, "/messages"), "/v1")
		list, err = c.listOpenAI(ctx, p, base+"/v1/models?limit=1000")
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	if len(list) > maxListed {
		list = list[:maxListed]
	}
	return list, nil
}

// get sends a GET with the provider's headers and credential and reads
// the JSON answer.
func (c *Client) get(ctx context.Context, p Provider, addr string, out any) error {
	if p.Query != "" {
		if strings.Contains(addr, "?") {
			addr += "&" + p.Query
		} else {
			addr += "?" + p.Query
		}
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return err
	}
	hr.Header.Set("Accept", "application/json")
	if p.Kind == KindAnthropic {
		hr.Header.Set("anthropic-version", APIVersion)
	}
	for k, v := range p.Headers {
		hr.Header.Set(k, v)
	}
	if err := c.authorize(ctx, p, hr, nil); err != nil {
		return err
	}
	hc, err := c.httpClient(p)
	if err != nil {
		return err
	}
	res, err := hc.Do(hr)
	if err != nil {
		return fmt.Errorf("reaching %s: %w", p.Name(), err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		e := apiError(res.StatusCode, b)
		e.Provider = p.Name()
		return e
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s answered with a list that is not JSON", p.Name())
	}
	return nil
}

// listOpenAI reads an OpenAI-shaped list, which the Claude API's is too:
// data[].id, with the context window as vLLM (max_model_len), OpenRouter
// and LM Studio (context_length) or others (context_window,
// max_input_tokens) give it.
func (c *Client) listOpenAI(ctx context.Context, p Provider, addr string) ([]ModelInfo, error) {
	var out struct {
		Data []struct {
			ID             string `json:"id"`
			OwnedBy        string `json:"owned_by"`
			MaxModelLen    int    `json:"max_model_len"`
			ContextLength  int    `json:"context_length"`
			ContextWindow  int    `json:"context_window"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := c.get(ctx, p, addr, &out); err != nil {
		return nil, err
	}
	var list []ModelInfo
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		w := m.MaxModelLen
		for _, x := range []int{m.ContextLength, m.ContextWindow, m.MaxInputTokens} {
			if w <= 0 {
				w = x
			}
		}
		list = append(list, ModelInfo{ID: m.ID, ContextWindow: max(w, 0), Owner: m.OwnedBy})
	}
	return list, nil
}

// listOllama reads Ollama's own list.
func (c *Client) listOllama(ctx context.Context, p Provider, addr string) ([]ModelInfo, error) {
	var out struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := c.get(ctx, p, addr, &out); err != nil {
		return nil, err
	}
	var list []ModelInfo
	for _, m := range out.Models {
		id := m.Model
		if id == "" {
			id = m.Name
		}
		if id != "" {
			list = append(list, ModelInfo{ID: id})
		}
	}
	return list, nil
}

// listBedrock reads Amazon Bedrock's foundation models.
func (c *Client) listBedrock(ctx context.Context, p Provider, addr string) ([]ModelInfo, error) {
	var out struct {
		ModelSummaries []struct {
			ModelID      string `json:"modelId"`
			ProviderName string `json:"providerName"`
		} `json:"modelSummaries"`
	}
	if err := c.get(ctx, p, addr, &out); err != nil {
		return nil, err
	}
	var list []ModelInfo
	for _, m := range out.ModelSummaries {
		if m.ModelID != "" {
			list = append(list, ModelInfo{ID: m.ModelID, Owner: m.ProviderName})
		}
	}
	return list, nil
}
