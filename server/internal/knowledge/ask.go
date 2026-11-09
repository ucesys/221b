package knowledge

import (
	"context"
	"strings"
)

// What agents and people read: the approved pages and the excerpts, from
// this replica's local copy and index.

// Search finds pages and excerpts; kind "" is both.
func (s *Service) Search(query, kind string, limit int) []Hit {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	return idx.Search(query, kind, limit)
}

// Ask is ask_knowledge's lookup: the pages that answer a question best,
// then excerpts of the sources where pages do not cover it.
func (s *Service) Ask(_ context.Context, query string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 5
	}
	return s.Search(query, "", limit), nil
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

// PageAt is one approved page, with its body.
func (s *Service) PageAt(path string) (Page, bool) {
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	for _, p := range idx.Pages() {
		if p.Path == path {
			return p, true
		}
	}
	return Page{}, false
}

// Connected says whether the knowledge base holds anything to answer
// from.
func (s *Service) Connected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index.Size() > 0
}

// NothingFound is ask_knowledge's answer when nothing matches.
func NothingFound(query string) string {
	return "The knowledge base has no page or source excerpt on “" + strings.TrimSpace(query) + "”. Say it is not known there, and answer from what the tools show."
}
