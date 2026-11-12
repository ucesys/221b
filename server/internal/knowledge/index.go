package knowledge

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The search index (ADR-96): one over the approved pages and the scrubbed
// excerpts of the sources, built on each replica from its local copy.
// Words are weighted by how rare they are, BM25's way, as the issue
// service's matcher weighs them; a page's title counts twice. There is no
// semantic search: the console has no embeddings to build one from.

// Hit is one answer to a search: a page, or an excerpt of a source.
type Hit struct {
	// Kind is "page" or "excerpt".
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Path is a page's.
	Path string `json:"path,omitempty"`
	// PageKind is a page's kind.
	PageKind string `json:"pageKind,omitempty"`
	// Date is a page's last update, or a source's own date.
	Date  time.Time `json:"date,omitzero"`
	Stale bool      `json:"stale,omitempty"`
	// Source, Version and URL name an excerpt's source item.
	Source  string  `json:"source,omitempty"`
	Version string  `json:"version,omitempty"`
	URL     string  `json:"url,omitempty"`
	Text    string  `json:"text"`
	Score   float64 `json:"score"`
	// Sources are a page's.
	Sources []SourceRef `json:"sources,omitempty"`
}

type doc struct {
	hit   Hit
	terms map[string]int
	len   int
	// paras are the doc's paragraphs, for the part of it shown.
	paras []string
}

// Index is the search index.
type Index struct {
	docs  []doc
	df    map[string]int
	avg   float64
	pages []Page
	// retired are the pages people retired (KN-24): kept to be shown,
	// never searched.
	retired []Page
}

// stopWords say nothing a search is about.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about above after again all am an and any are as at be because been before being below between both but by can cannot could did do does doing down during each few for from further had has have having he her here hers him his how i if in into is it its itself just me more most my no nor not of off on once only or other our out over own same she should so some such than that the their them then there these they this those through to too under until up very was we were what when where which while who whom why will with would you your yours what's how's please also get`) {
		stopWords[w] = true
	}
}

// stem takes common endings off a word, so "restarts" and "restarting"
// meet.
func stem(w string) string {
	if len(w) <= 4 || strings.IndexFunc(w, unicode.IsDigit) >= 0 {
		return w
	}
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			if suf == "s" && strings.HasSuffix(w, "ss") {
				return w
			}
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// terms are a text's words, lower case and stemmed, stop words left out.
func terms(text string) []string {
	text = strings.ReplaceAll(strings.ToLower(text), "'", "")
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' }) {
		f = strings.Trim(f, "-_")
		if len(f) < 2 || stopWords[f] {
			continue
		}
		out = append(out, stem(f))
	}
	return out
}

func (d *doc) add(text string, weight int) {
	for _, t := range terms(text) {
		d.terms[t] += weight
		d.len += weight
	}
}

func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// NewIndex builds the index of a local copy: its pages, with stale ones
// marked from the manifest, and its excerpts.
func NewIndex(files map[string]File, m *Manifest, staleDays int, now time.Time) *Index {
	idx := &Index{df: map[string]int{}}
	if m == nil {
		m = newManifest()
	}
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.Path, PagesDir):
			p := ParsePage(f)
			if r, ok := m.Retired[p.Path]; ok {
				// A retired page is out of the index: no agent reads it.
				p.Retired = &r
				idx.retired = append(idx.retired, p)
				continue
			}
			if last, found := m.lastSeen(p.Sources); found && staleDays > 0 && now.Sub(last) > time.Duration(staleDays)*24*time.Hour {
				p.Stale = true
			}
			idx.pages = append(idx.pages, p)
			d := doc{hit: Hit{Kind: "page", Title: p.Title, Path: p.Path, PageKind: p.Kind, Date: p.Updated, Stale: p.Stale, Sources: p.Sources}, terms: map[string]int{}, paras: paragraphs(p.Body)}
			d.add(p.Title, 2)
			d.add(p.Body, 1)
			idx.docs = append(idx.docs, d)
		case strings.HasPrefix(f.Path, ExcerptsDir):
			var e Excerpt
			if json.Unmarshal(f.Body, &e) != nil {
				continue
			}
			for _, c := range e.Chunks {
				d := doc{hit: Hit{Kind: "excerpt", Title: e.Title, Date: e.Date, Source: e.Key, Version: e.Version, URL: e.URL}, terms: map[string]int{}, paras: paragraphs(c)}
				d.add(e.Title, 1)
				d.add(c, 1)
				idx.docs = append(idx.docs, d)
			}
		}
	}
	sort.Slice(idx.pages, func(i, j int) bool { return idx.pages[i].Path < idx.pages[j].Path })
	sort.Slice(idx.retired, func(i, j int) bool { return idx.retired[i].Path < idx.retired[j].Path })
	sort.SliceStable(idx.docs, func(i, j int) bool {
		a, b := idx.docs[i].hit, idx.docs[j].hit
		if a.Kind != b.Kind {
			return a.Kind == "page"
		}
		return a.Path+a.Source < b.Path+b.Source
	})
	total := 0
	for _, d := range idx.docs {
		total += d.len
		for t := range d.terms {
			idx.df[t]++
		}
	}
	if len(idx.docs) > 0 {
		idx.avg = float64(total) / float64(len(idx.docs))
	}
	return idx
}

// Pages are the approved pages, without their bodies' search terms.
func (x *Index) Pages() []Page { return append([]Page(nil), x.pages...) }

// Retired are the pages people retired, with their bodies; no search
// finds them.
func (x *Index) Retired() []Page { return append([]Page(nil), x.retired...) }

// Size is how many documents the index holds.
func (x *Index) Size() int { return len(x.docs) }

// The BM25 constants.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// maxHitText bounds the text of one hit.
const maxHitText = 1500

// Search finds the documents that match a query best: pages first, then
// excerpts to fill up to limit. kind "" is both, "page" or "excerpt" one.
func (x *Index) Search(query, kind string, limit int) []Hit {
	q := terms(query)
	if len(q) == 0 || len(x.docs) == 0 {
		return nil
	}
	n := float64(len(x.docs))
	type scored struct {
		i     int
		score float64
	}
	var pages, excerpts []scored
	for i, d := range x.docs {
		if kind != "" && d.hit.Kind != kind {
			continue
		}
		score := 0.0
		for _, t := range q {
			tf := float64(d.terms[t])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(x.df[t])+0.5)/(float64(x.df[t])+0.5))
			score += idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(d.len)/math.Max(x.avg, 1)))
		}
		if score <= 0 {
			continue
		}
		if d.hit.Kind == "page" {
			pages = append(pages, scored{i, score})
		} else {
			excerpts = append(excerpts, scored{i, score})
		}
	}
	by := func(l []scored) {
		sort.SliceStable(l, func(a, b int) bool { return l[a].score > l[b].score })
	}
	by(pages)
	by(excerpts)
	var out []Hit
	for _, list := range [][]scored{pages, excerpts} {
		for _, sc := range list {
			if len(out) >= limit {
				return out
			}
			d := x.docs[sc.i]
			h := d.hit
			h.Score = math.Round(sc.score*100) / 100
			h.Text = bestText(d.paras, q)
			out = append(out, h)
		}
	}
	return out
}

// bestText is the part of a document that holds most of the query's
// words: its best paragraph and the ones after it, up to maxHitText.
func bestText(paras []string, q []string) string {
	best, bestScore := 0, -1
	for i, p := range paras {
		have := map[string]bool{}
		for _, t := range terms(p) {
			have[t] = true
		}
		score := 0
		for _, t := range q {
			if have[t] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	var b strings.Builder
	for i := best; i < len(paras) && b.Len() < maxHitText; i++ {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(paras[i])
	}
	return clip(b.String(), maxHitText)
}
