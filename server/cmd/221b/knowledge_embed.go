package main

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/api/resource"

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

// clusterVersions are the Kubernetes versions the clusters run, for the
// knowledge sources that follow them (KN-56).
func (w *wiring) clusterVersions(context.Context) []string {
	var out []string
	if w.manager == nil {
		return out
	}
	for _, c := range w.manager.List() {
		if c.Version != "" {
			out = append(out, c.Version)
		}
	}
	return out
}

// volumeBytes is the database volume's size the chart gives (KN-58); 0
// when it gives none or one that is not a quantity, which is logged.
func volumeBytes(size string, log *slog.Logger) int64 {
	if size == "" {
		return 0
	}
	q, err := resource.ParseQuantity(size)
	if err != nil {
		log.Warn("the database's volume size is not a quantity; the knowledge index does not warn when the database fills it", "size", size, "err", err)
		return 0
	}
	return q.Value()
}
