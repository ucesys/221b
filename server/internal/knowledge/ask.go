package knowledge

import (
	"context"
	"strings"
)

// What agents and people read: the approved pages and the excerpts, from
// this replica's local copy and index.

// Search finds pages and excerpts; kind "" is both.
func (s *Service) Search(query, kind string, limit int) []Hit {
	hits, _ := s.search(context.Background(), query, kind, limit)
	return hits
}

// search is Search, saying why the index in the database failed: the
// pages of this replica's copy answer then, alone.
func (s *Service) search(ctx context.Context, query, kind string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	if s.pg != nil {
		hits, err := s.searchPG(ctx, query, kind, limit)
		if err == nil {
			return hits, nil
		}
		s.o.Logger.Warn("the knowledge index in the database could not be searched; the pages of this replica's copy answer", "err", err)
		if kind == "excerpt" {
			return nil, err
		}
		s.mu.RLock()
		idx := s.index
		s.mu.RUnlock()
		return idx.Search(query, "page", limit), err
	}
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	return idx.Search(query, kind, limit), nil
}

// Ask is ask_knowledge's lookup: the pages and the excerpts of the
// sources that answer a question best.
func (s *Service) Ask(ctx context.Context, query string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 5
	}
	hits, err := s.search(ctx, query, "", limit)
	if len(hits) > 0 {
		return hits, nil
	}
	return hits, err
}

// Pages lists the approved pages, stale ones marked, without their bodies.
func (s *Service) Pages() []Page {
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	// Never nil: the list is an empty array in JSON, which the Wiki tab
	// takes for "no pages", not for "still loading" (MY-02).
	pages := append([]Page{}, idx.Pages()...)
	for i := range pages {
		pages[i].Body = ""
	}
	return pages
}

// RetiredPages lists the pages people retired (KN-24), with why and who,
// without their bodies; never nil.
func (s *Service) RetiredPages() []Page {
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	pages := append([]Page{}, idx.Retired()...)
	for i := range pages {
		pages[i].Body = ""
	}
	return pages
}

// PageAt is one approved page, with its body: a retired one too, marked
// so (KN-24), for people to read; the agents never get it.
func (s *Service) PageAt(path string) (Page, bool) {
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	for _, p := range append(idx.Pages(), idx.Retired()...) {
		if p.Path == path {
			return p, true
		}
	}
	return Page{}, false
}

// Connected says whether the knowledge base holds anything to answer
// from.
func (s *Service) Connected() bool {
	if s.pg != nil {
		return s.docs.Load() > 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index.Size() > 0
}

// NothingFound is ask_knowledge's answer when nothing matches.
func NothingFound(query string) string {
	return "The knowledge base has no page or source excerpt on “" + strings.TrimSpace(query) + "”. Say it is not known there, and answer from what the tools show."
}
