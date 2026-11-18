package knowledge

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// --- One search across Mycroft (Phase 12.1, KN-35) ---
//
// People search the wiki's approved pages, the sources' excerpts and the
// console's own records (the known issues, the fixes and the accepted
// exceptions) in one box at the top of AIOps › Mycroft. The records are
// ranked in the same BM25 as the wiki (index.go), scored among the index's
// documents, so one list holds every kind ranked together; equal scores
// keep FindKinds' order. Which records a person may see is their
// services' rule, applied by whoever gives them (internal/api).

// The kinds of the console's records a search finds beside the wiki's
// "page" and "excerpt"; ask_knowledge names them the same.
const (
	KindKnownIssue = "knownIssue"
	KindFix        = "fix"
	KindException  = "exception"
)

// FindKinds are every kind a search for people covers, in the order equal
// scores take.
var FindKinds = []string{"page", KindKnownIssue, KindFix, KindException, "excerpt"}

// Record is one of the console's records searched beside the wiki, as
// its service gives it to the person asking.
type Record struct {
	Kind   string
	ID     string
	Title  string
	Status string
	Date   time.Time
	// Text is what is searched besides the title and the id, and what the
	// snippet is taken from: scrubbed by whoever gives it (X-01).
	Text string
}

// Found is a search's answer for people.
type Found struct {
	Hits []Hit `json:"hits"`
	// Counts are how many hits of each kind match, whichever kinds were
	// asked and before the limit, for the filter's chips.
	Counts map[string]int `json:"counts"`
}

// The most hits a search for people gives, and its default.
const (
	MaxFindHits     = 100
	DefaultFindHits = 30
)

// snippetRunes bounds a hit's snippet.
const snippetRunes = 240

// Find ranks the pages, the excerpts and the records together for a
// query: each source's best excerpt only, each hit with a snippet around
// its first matched word and the words it matched. kinds narrows the hits
// listed (nil: all); limit bounds them.
func (x *Index) Find(query string, kinds []string, limit int, records []Record) Found {
	out := Found{Hits: []Hit{}, Counts: map[string]int{}}
	q := terms(query)
	if len(q) == 0 {
		return out
	}
	q = uniq(q)
	if limit <= 0 || limit > MaxFindHits {
		limit = DefaultFindHits
	}
	recs := make([]doc, 0, len(records))
	rdf := map[string]int{}
	rtotal := 0
	for _, r := range records {
		d := doc{hit: Hit{Kind: r.Kind, ID: r.ID, Title: r.Title, Status: r.Status, Date: r.Date}, terms: map[string]int{}, paras: paragraphs(r.Text)}
		d.add(r.ID, 2)
		d.add(r.Title, 2)
		d.add(r.Text, 1)
		for _, t := range q {
			if d.terms[t] > 0 {
				rdf[t]++
			}
		}
		rtotal += d.len
		recs = append(recs, d)
	}
	n := float64(len(x.docs) + len(recs))
	if n == 0 {
		return out
	}
	avg := float64(x.total+rtotal) / n
	df := func(t string) int { return x.df[t] + rdf[t] }
	type scored struct {
		d     *doc
		score float64
	}
	var all []scored
	// An excerpt's source is listed once, by its best chunk.
	bestOf := map[string]int{}
	consider := func(d *doc) {
		score := bm25(d, q, n, avg, df)
		if score <= 0 {
			return
		}
		if d.hit.Kind == "excerpt" {
			key := d.hit.Source + "\x00" + d.hit.Title
			if i, ok := bestOf[key]; ok {
				if all[i].score < score {
					all[i] = scored{d, score}
				}
				return
			}
			bestOf[key] = len(all)
		}
		all = append(all, scored{d, score})
	}
	for i := range x.docs {
		consider(&x.docs[i])
	}
	for i := range recs {
		consider(&recs[i])
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
		if ra, rb := rank(a.d.hit.Kind), rank(b.d.hit.Kind); ra != rb {
			return ra < rb
		}
		return a.d.hit.Title < b.d.hit.Title
	})
	for _, sc := range all {
		out.Counts[sc.d.hit.Kind]++
		if len(out.Hits) >= limit || (len(kinds) > 0 && !slices.Contains(kinds, sc.d.hit.Kind)) {
			continue
		}
		h := sc.d.hit
		h.Score = float64(int(sc.score*100+0.5)) / 100
		h.Sources = nil
		for _, t := range q {
			if sc.d.terms[t] > 0 {
				h.Terms = append(h.Terms, t)
			}
		}
		h.Text = snippet(bestText(sc.d.paras, q), h.Terms)
		out.Hits = append(out.Hits, h)
	}
	return out
}

func uniq(list []string) []string {
	var out []string
	for _, s := range list {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// snippet is a text on one line, at most snippetRunes long, around the
// first word that starts with one of the terms.
func snippet(text string, terms []string) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= snippetRunes {
		return string(runes)
	}
	lower := []rune(strings.ToLower(string(runes)))
	start := 0
	if len(lower) == len(runes) {
		first := -1
		for _, t := range terms {
			tr := []rune(t)
			for i := 0; i+len(tr) <= len(lower); i++ {
				if (i == 0 || !isWordRune(lower[i-1])) && string(lower[i:i+len(tr)]) == t {
					if first < 0 || i < first {
						first = i
					}
					break
				}
			}
		}
		// The match comes a little after the snippet's start, at a word.
		if first > snippetRunes/4 {
			start = first - snippetRunes/4
			for start < first && isWordRune(runes[start-1]) {
				start++
			}
		}
	}
	end := min(start+snippetRunes, len(runes))
	if end == len(runes) {
		start = max(0, end-snippetRunes)
	}
	s := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Find is the search for people over this replica's index and the
// records given (KN-35).
func (s *Service) Find(query string, kinds []string, limit int, records []Record) Found {
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	if idx == nil {
		idx = NewIndex(nil, nil, 0, time.Time{})
	}
	return idx.Find(query, kinds, limit, records)
}
