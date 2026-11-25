package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// Embeddings for semantic search (phase-13.1-ai, part B, KN-55; decided
// by the owner on November 23): an OpenAI-compatible /v1/embeddings
// endpoint, one of Administration › AI's providers, with an embedding
// model's name, chosen in Administration › AI › Knowledge by a SuperAdmin;
// none by default, when keyword and fuzzy search answer alone. The
// leader embeds every passage of the index that has no embedding by the
// chosen model, in the background after each Sync of the index, so a
// change of model embeds every passage again; every replica embeds the
// query it searches for, and searches without it when the endpoint does
// not answer.

// EmbeddingSettings are the embedding model the search uses.
type EmbeddingSettings struct {
	// Provider is a provider of Administration › AI, by id.
	Provider string `json:"provider,omitempty"`
	// Model is its embedding model's name, as the endpoint knows it.
	Model string `json:"model,omitempty"`
}

func (e EmbeddingSettings) on() bool { return e.Provider != "" && e.Model != "" }

// EmbeddingProvider is a provider an embedding model may be chosen on: an
// OpenAI-compatible one of Administration › AI.
type EmbeddingProvider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint,omitempty"`
}

// Embedder embeds texts with a provider's model: package sherlock's
// client, through cmd/221b.
type Embedder interface {
	// Embed gives each text's embedding, in order.
	Embed(ctx context.Context, provider, model string, texts []string) ([][]float32, error)
	// Providers are the providers an embedding model may be chosen on.
	Providers(ctx context.Context) []EmbeddingProvider
}

// ActionEmbeddings is a change of the embedding model (KN-55).
const ActionEmbeddings = "knowledge.embeddings"

// embedBatch is how many passages one request embeds; queryEmbedTimeout
// bounds the embedding of a search's query.
const (
	embedBatch        = 32
	queryEmbedTimeout = 5 * time.Second
)

// embedState is the leader's last pass of embeddings, for the view.
type embedState struct {
	mu    sync.Mutex
	at    time.Time
	error string
}

func (e *embedState) note(err error, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.at = at
	e.error = ""
	if err != nil {
		e.error = clip(err.Error(), 300)
	}
}

func (e *embedState) get() (time.Time, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.at, e.error
}

// SetEmbeddings chooses the embedding model, or none (an empty provider):
// a SuperAdmin's, audited.
func (s *Service) SetEmbeddings(ctx context.Context, c Caller, e EmbeddingSettings) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin chooses the embedding model")
	}
	if err := s.leads(); err != nil {
		return err
	}
	e.Provider, e.Model = strings.TrimSpace(e.Provider), strings.TrimSpace(e.Model)
	if e.Provider == "" {
		e.Model = ""
	} else {
		if s.o.Embedder == nil {
			return invalid("this console has no embedding provider to choose")
		}
		found := false
		for _, p := range s.o.Embedder.Providers(ctx) {
			found = found || p.ID == e.Provider
		}
		switch {
		case !found:
			return invalid("%q is not an OpenAI-compatible provider of Administration › AI", e.Provider)
		case e.Model == "" || len(e.Model) > 200:
			return invalid("name the embedding model, as its endpoint knows it")
		}
	}
	var was EmbeddingSettings
	if _, err := s.editSettings(ctx, func(set *Settings) error {
		was = set.Embeddings
		set.Embeddings = e
		return nil
	}); err != nil {
		return err
	}
	words := func(x EmbeddingSettings) string {
		if !x.on() {
			return "none"
		}
		return x.Model + " on " + x.Provider
	}
	s.audit(audit.Event{Action: ActionEmbeddings, Who: c.who(), Target: firstOf(e.Provider, was.Provider), Outcome: audit.OutcomeOK, From: c.From,
		Detail: fmt.Sprintf("the knowledge search's embedding model: %s, was %s", words(e), words(was))})
	s.kickIndex()
	return nil
}

// embedAll embeds every passage without an embedding by the chosen
// model, on the leader; it stops at the first failure, which the view
// says, and the next pass tries again.
func (s *Service) embedAll(ctx context.Context) error {
	if s.pg == nil || s.o.Embedder == nil {
		return nil
	}
	set, err := s.Settings(ctx)
	if err != nil || !set.Embeddings.on() {
		return err
	}
	if !s.pg.features(ctx).vector {
		err := errors.New("the database has no pgvector (the extension vector): semantic search is off")
		s.embedded.note(err, s.o.Now())
		return err
	}
	e := set.Embeddings
	total := 0
	for ctx.Err() == nil {
		ids, texts, err := s.pg.unembedded(ctx, e.Model, embedBatch)
		if err != nil {
			s.embedded.note(err, s.o.Now())
			return err
		}
		if len(ids) == 0 {
			break
		}
		vecs, err := s.o.Embedder.Embed(ctx, e.Provider, e.Model, texts)
		if err == nil && len(vecs) != len(ids) {
			err = fmt.Errorf("the endpoint gave %d embeddings for %d passages", len(vecs), len(ids))
		}
		if err != nil {
			err = fmt.Errorf("embedding with %s: %w", e.Model, err)
			s.embedded.note(err, s.o.Now())
			return err
		}
		if err := s.pg.storeEmbeddings(ctx, e.Model, ids, vecs); err != nil {
			s.embedded.note(err, s.o.Now())
			return err
		}
		total += len(ids)
	}
	if total > 0 {
		s.o.Logger.Info("the knowledge index's passages were embedded", "model", e.Model, "passages", total)
	}
	s.embedded.note(ctx.Err(), s.o.Now())
	return ctx.Err()
}

// queryVector is a query's embedding by the chosen model, or none: no
// model chosen, no pgvector, or the endpoint not answering in time.
func (s *Service) queryVector(ctx context.Context, query string) ([]float32, string) {
	if s.o.Embedder == nil || s.pg == nil {
		return nil, ""
	}
	set, err := s.Settings(ctx)
	if err != nil || !set.Embeddings.on() || !s.pg.features(ctx).vector {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, queryEmbedTimeout)
	defer cancel()
	vecs, err := s.o.Embedder.Embed(ctx, set.Embeddings.Provider, set.Embeddings.Model, []string{query})
	if err != nil || len(vecs) != 1 {
		s.o.Logger.Debug("the query could not be embedded; keyword and fuzzy search answer", "err", err)
		return nil, ""
	}
	return vecs[0], set.Embeddings.Model
}
