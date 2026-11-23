package main

import (
	"context"
	"fmt"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// knowledgeEmbedder embeds the knowledge search's passages and queries
// with a provider of Administration › AI (KN-55): an OpenAI-compatible
// one's /embeddings, with its credential, as the agents reach it.
type knowledgeEmbedder struct{ ai *ai.Service }

func (e knowledgeEmbedder) Embed(ctx context.Context, provider, model string, texts []string) ([][]float32, error) {
	if e.ai == nil {
		return nil, fmt.Errorf("the providers are not open")
	}
	p, ok := e.ai.ProviderOf(ctx, provider)
	if !ok {
		return nil, fmt.Errorf("there is no provider %q", provider)
	}
	if p.Provider.Kind != sherlock.KindOpenAI {
		return nil, fmt.Errorf("%s is not an OpenAI-compatible provider", p.Name)
	}
	return e.ai.Client(provider).Embed(ctx, model, texts)
}

func (e knowledgeEmbedder) Providers(ctx context.Context) []knowledge.EmbeddingProvider {
	var out []knowledge.EmbeddingProvider
	if e.ai == nil {
		return out
	}
	for _, p := range e.ai.View(ctx).Providers {
		if p.Resolved.Kind != sherlock.KindOpenAI {
			continue
		}
		out = append(out, knowledge.EmbeddingProvider{ID: p.ID, Name: p.Name, Endpoint: p.Resolved.Endpoint})
	}
	return out
}
