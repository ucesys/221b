package knowledge

import (
	"context"
	"math"
	"slices"
	"sort"
	"time"
)

// The service's side of the index in the database (KN-53): the leader
// keeps it as the wiki holds, after every read of the wiki; every replica
// searches it.

// searchTimeout bounds one search of the index.
const searchTimeout = 10 * time.Second

// kickIndex has the index brought up to the wiki in the background, on
// the leader: one Sync at a time, and one more after it when the wiki
// changed while it ran.
func (s *Service) kickIndex() {
	if s.pg == nil || !s.o.Leads() {
		return
	}
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexRunning {
		s.indexDirty = true
		return
	}
	s.indexRunning = true
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if _, err := s.SyncIndex(ctx); err != nil {
				s.o.Logger.Warn("the knowledge index could not be brought up to the wiki; the next read of the wiki tries again", "err", err)
			} else if err := s.embedAll(ctx); err != nil {
				s.o.Logger.Warn("the knowledge index's passages could not all be embedded; the next read of the wiki tries again", "err", err)
			}
			cancel()
			s.indexMu.Lock()
			if !s.indexDirty {
				s.indexRunning = false
				s.indexMu.Unlock()
				return
			}
			s.indexDirty = false
			s.indexMu.Unlock()
		}
	}()
}

// SyncIndex brings the index in the database up to this replica's copy of
// the wiki, and says what it wrote.
func (s *Service) SyncIndex(ctx context.Context) (IndexSync, error) {
	if s.pg == nil {
		return IndexSync{}, errNoIndex
	}
	if err := s.leads(); err != nil {
		return IndexSync{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return IndexSync{}, err
	}
	// A source the settings list by default is a row once they are saved:
	// an excerpt is kept by its source's row.
	if !s.sourcesSaved.Load() {
		if _, err := s.editSettings(ctx, func(*Settings) error { return nil }); err != nil {
			return IndexSync{}, err
		}
		s.sourcesSaved.Store(true)
	}
	sources, bySource := map[string]bool{}, map[string]Source{}
	for _, x := range set.Sources {
		sources[x.ID], bySource[x.ID] = true, x
	}
	s.mu.RLock()
	files, m := s.files, s.manifest
	s.mu.RUnlock()
	s.pg.ensureExtensions(ctx)
	res, err := s.pg.Sync(ctx, docsOf(files, m, bySource), sources)
	if err == nil && (res.Written > 0 || res.Deleted > 0) {
		s.o.Logger.Info("the knowledge index follows the wiki", "written", res.Written, "deleted", res.Deleted, "kept", res.Kept)
	}
	return res, err
}

// searchPG searches the index in the database: the pages and the
// excerpts that answer a query best, ranked together.
func (s *Service) searchPG(ctx context.Context, query, kind string, limit int) ([]Hit, error) {
	return s.searchModes(ctx, query, kind, limit, nil)
}

// searchModes is searchPG by the ways of searching named (none: all).
func (s *Service) searchModes(ctx context.Context, query, kind string, limit int, modes []string) ([]Hit, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	iq := IndexQuery{Text: query, Kind: kind, Limit: limit, Modes: modes}
	if iq.mode(ModeSemantic) {
		iq.Vector, iq.Model = s.queryVector(ctx, query)
	}
	// Each source's trust weighs its passages, and an approved page more
	// (KN-57); a source that follows the clusters keeps to their versions
	// (KN-56).
	levels := map[string]string{}
	if set, err := s.Settings(ctx); err == nil {
		for _, x := range set.Sources {
			levels[x.ID] = x.trustLevel()
			if x.FollowsClusters {
				iq.Follow = append(iq.Follow, x.ID)
			}
		}
	}
	if len(iq.Follow) > 0 {
		iq.Minors = s.clusterMinors(ctx)
	}
	iq.Weight = func(h indexHit) float64 {
		if h.reviewed {
			return weightPage
		}
		return trustWeight(levels[h.source])
	}
	found, err := s.pg.Search(ctx, iq)
	if err != nil {
		return nil, err
	}
	q := terms(query)
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	pages := map[string]Page{}
	for _, p := range idx.Pages() {
		pages[p.Path] = p
	}
	out := make([]Hit, 0, len(found))
	for _, f := range found {
		h := Hit{Kind: f.kind, Title: f.title, Date: f.date, Score: math.Round(f.score*1e4) / 1e4, Text: bestText(paragraphs(f.body), q),
			Reviewed: f.reviewed, Heading: f.heading, Product: f.product, ProductVersion: f.productVersion}
		if f.kind == "page" {
			h.Path, h.PageKind = f.path, f.pageKind
			if p, ok := pages[f.path]; ok {
				h.Stale, h.Sources, h.Date = p.Stale, p.Sources, p.Updated
			}
		} else {
			h.Source, h.Version, h.URL, h.Trust = f.itemKey, f.version, f.url, levels[f.source]
		}
		out = append(out, h)
	}
	return out, nil
}

// findPG is the search for people over the index in the database (KN-35,
// KN-53): the pages and excerpts it ranks, and the records ranked among
// themselves, merged by their ranks; each source item's best excerpt only.
func (s *Service) findPG(ctx context.Context, query string, kinds []string, limit int, records []Record) (Found, error) {
	out := Found{Hits: []Hit{}, Counts: map[string]int{}}
	q := uniq(terms(query))
	if len(q) == 0 {
		return out, nil
	}
	if limit <= 0 || limit > MaxFindHits {
		limit = DefaultFindHits
	}
	wiki, err := s.searchPG(ctx, query, "", MaxFindHits)
	if err != nil {
		return out, err
	}
	// The records, ranked among themselves as the wiki's search ranks.
	recs := NewIndex(nil, nil, 0, time.Time{}).Find(query, nil, MaxFindHits, records).Hits
	type ranked struct {
		h     Hit
		score float64
	}
	var all []ranked
	for i, h := range wiki {
		all = append(all, ranked{h, 1.0 / float64(rrfK+i+1)})
	}
	for i, h := range recs {
		all = append(all, ranked{h, 1.0 / float64(rrfK+i+1)})
	}
	rank := func(kind string) int {
		if i := slices.Index(FindKinds, kind); i >= 0 {
			return i
		}
		return len(FindKinds)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if ra, rb := rank(a.h.Kind), rank(b.h.Kind); ra != rb {
			return ra < rb
		}
		return a.h.Title < b.h.Title
	})
	for _, r := range all {
		out.Counts[r.h.Kind]++
		if len(out.Hits) >= limit || (len(kinds) > 0 && !slices.Contains(kinds, r.h.Kind)) {
			continue
		}
		h := r.h
		h.Score = math.Round(r.score*1e4) / 1e4
		h.Sources = nil
		if h.Kind == "page" || h.Kind == "excerpt" {
			have := map[string]bool{}
			for _, t := range terms(h.Title + " " + h.Text) {
				have[t] = true
			}
			h.Terms = nil
			for _, t := range q {
				if have[t] {
					h.Terms = append(h.Terms, t)
				}
			}
			h.Text = snippet(h.Text, h.Terms)
		}
		out.Hits = append(out.Hits, h)
	}
	return out, nil
}
