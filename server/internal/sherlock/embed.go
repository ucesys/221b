package sherlock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Embeddings (phase-13.1-ai, part B, KN-55): the knowledge search's
// semantic part asks an OpenAI-compatible provider's /embeddings, as vLLM,
// Ollama, OpenAI and most gateways serve it, for the embeddings of its
// passages and of each query.

// maxEmbedAnswer bounds an embeddings answer: 32 passages of 4,096
// dimensions written as JSON fit well under it.
const maxEmbedAnswer = 64 << 20

// Embed gives each text's embedding by a model of the provider: an
// OpenAI-compatible provider's <endpoint>/embeddings.
func (c *Client) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	p := c.provider()
	if p.Kind != KindOpenAI {
		return nil, errors.New("embeddings are asked of an OpenAI-compatible provider")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	addr := strings.TrimSuffix(strings.TrimSuffix(p.Endpoint, "/chat/completions"), "/") + "/embeddings"
	if p.Query != "" {
		addr += "?" + p.Query
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": texts, "encoding_format": "float"})
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	for k, v := range p.Headers {
		hr.Header.Set(k, v)
	}
	if err := c.authorize(ctx, p, hr, body); err != nil {
		return nil, err
	}
	hc, err := c.httpClient(p)
	if err != nil {
		return nil, err
	}
	res, err := hc.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", p.Name(), err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, maxEmbedAnswer))
	if res.StatusCode != http.StatusOK {
		e := apiError(res.StatusCode, b)
		e.Provider = p.Name()
		return nil, e
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s answered with embeddings that are not JSON", p.Name())
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) || len(d.Embedding) == 0 {
			return nil, fmt.Errorf("%s answered an embedding for no text it was given", p.Name())
		}
		vecs[d.Index] = d.Embedding
	}
	for i, v := range vecs {
		if v == nil {
			return nil, fmt.Errorf("%s gave no embedding for text %d of %d", p.Name(), i+1, len(texts))
		}
		if len(v) != len(vecs[0]) {
			return nil, fmt.Errorf("%s gave embeddings of different sizes", p.Name())
		}
	}
	return vecs, nil
}
