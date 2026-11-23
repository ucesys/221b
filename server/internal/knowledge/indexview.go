package knowledge

import (
	"context"
	"time"
)

// IndexView is the search index as Administration › AI › Knowledge shows
// it (KN-53 to KN-58).
type IndexView struct {
	Documents int `json:"documents"`
	Passages  int `json:"passages"`
	// Fuzzy and Semantic say the database has pg_trgm and pgvector.
	Fuzzy    bool `json:"fuzzy"`
	Semantic bool `json:"semantic"`
	// Embedded are the passages with an embedding by the chosen model;
	// EmbedError why the last pass stopped, EmbeddedAt when it ran.
	Embedded   int       `json:"embedded"`
	EmbedError string    `json:"embedError,omitempty"`
	EmbeddedAt time.Time `json:"embeddedAt,omitzero"`
	// Providers are the providers an embedding model may be chosen on.
	Providers []EmbeddingProvider `json:"providers"`
}

// indexView is the index's view; nil without the index in the database.
func (s *Service) indexView(ctx context.Context, set Settings) *IndexView {
	if s.pg == nil {
		return nil
	}
	v := &IndexView{Providers: []EmbeddingProvider{}}
	f := s.pg.features(ctx)
	v.Fuzzy, v.Semantic = f.trgm, f.vector
	if d, p, err := s.pg.Count(ctx); err == nil {
		v.Documents, v.Passages = d, p
	}
	if set.Embeddings.on() {
		v.Embedded, _ = s.pg.embedded(ctx, set.Embeddings.Model)
		v.EmbeddedAt, v.EmbedError = s.embedded.get()
	}
	if s.o.Embedder != nil {
		v.Providers = append(v.Providers, s.o.Embedder.Providers(ctx)...)
	}
	return v
}
