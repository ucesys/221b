package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The search index in the console's database (phase-13.1-ai, part B,
// KN-53): the approved pages and the sources' excerpts, one row of
// knowledge_documents each and one of knowledge_passages a chunk, shared
// by every replica, in place of the BM25 index each replica built in
// memory from its copy of the wiki (KN-07, KN-11). The leader keeps the
// tables as the wiki holds (Sync): a document whose file or tags changed
// is written again whole, one whose file is gone deleted; so the first
// start of a release that has them builds them. Each replica still keeps
// its copy of the wiki and an index of the approved pages in memory, for
// the Wiki tab and Mycroft's own reading; the excerpts are searched here
// only. A passage's words are PostgreSQL's full-text search's (English
// stemming), its document's title and heading weighted above its body.

// PGIndex is the search index in the database.
type PGIndex struct {
	d   *db.DB
	log *slog.Logger
	// syncMu lets one Sync run at a time.
	syncMu sync.Mutex
}

// NewPGIndex keeps the index in d.
func NewPGIndex(d *db.DB, log *slog.Logger) *PGIndex {
	if log == nil {
		log = slog.Default()
	}
	return &PGIndex{d: d, log: log}
}

// indexDoc is a document as the wiki holds it now.
type indexDoc struct {
	path, kind, source, itemKey, title, heading, pageKind, version, url string
	date                                                                time.Time
	product, productVersion                                             string
	reviewed                                                            bool
	passages                                                            []string
	hash                                                                string
}

// docsOf are the documents of a copy of the wiki: its approved pages (a
// retired one is never searched, KN-24) and its excerpts that hold text.
func docsOf(files map[string]File, m *Manifest) []indexDoc {
	if m == nil {
		m = newManifest()
	}
	var out []indexDoc
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.Path, PagesDir):
			p := ParsePage(f)
			if _, gone := m.Retired[p.Path]; gone || strings.TrimSpace(p.Body) == "" {
				continue
			}
			d := indexDoc{path: f.Path, kind: "page", title: p.Title, pageKind: p.Kind, date: p.Updated, reviewed: true, passages: chunks(p.Body)}
			d.hash = hashOf(f.Body)
			out = append(out, d)
		case strings.HasPrefix(f.Path, ExcerptsDir):
			var e Excerpt
			if json.Unmarshal(f.Body, &e) != nil || len(e.Chunks) == 0 || e.Source == "" {
				continue
			}
			d := indexDoc{path: f.Path, kind: "excerpt", source: e.Source, itemKey: e.Key, title: e.Title, heading: e.Heading, version: e.Version, url: e.URL, date: e.Date, passages: e.Chunks}
			d.hash = hashOf(f.Body)
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// syncBatch is how many documents one transaction of a Sync writes: a
// Sync stopped half way keeps what it wrote, and the next goes on.
const syncBatch = 100

// SyncResult of the index: what a Sync wrote.
type IndexSync struct {
	Written, Deleted, Kept int
	// Skipped are the excerpts of a source the settings no longer hold.
	Skipped int
}

// Sync makes the index hold the documents given, and only them. sources
// are the sources the settings hold, which an excerpt's must be one of.
func (x *PGIndex) Sync(ctx context.Context, docs []indexDoc, sources map[string]bool) (IndexSync, error) {
	x.syncMu.Lock()
	defer x.syncMu.Unlock()
	var res IndexSync
	have := map[string]string{}
	rows := map[string]bool{}
	err := x.d.Do(ctx, func(q db.Querier) error {
		r, err := q.Query(ctx, "SELECT path, content_hash FROM knowledge_documents")
		if err != nil {
			return err
		}
		var p, h string
		if _, err := pgx.ForEachRow(r, []any{&p, &h}, func() error { have[p] = h; return nil }); err != nil {
			return err
		}
		r, err = q.Query(ctx, "SELECT knowledge_source_id FROM knowledge_sources")
		if err != nil {
			return err
		}
		_, err = pgx.ForEachRow(r, []any{&p}, func() error { rows[p] = true; return nil })
		return err
	})
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	var write []indexDoc
	for _, d := range docs {
		if d.kind == "excerpt" && (!rows[d.source] || (sources != nil && !sources[d.source])) {
			res.Skipped++
			continue
		}
		want[d.path] = true
		if have[d.path] == d.hash {
			res.Kept++
			continue
		}
		write = append(write, d)
	}
	var gone []string
	for p := range have {
		if !want[p] {
			gone = append(gone, p)
		}
	}
	if len(gone) > 0 {
		if err := x.d.Do(ctx, func(q db.Querier) error {
			_, err := q.Exec(ctx, "DELETE FROM knowledge_documents WHERE path = ANY($1)", gone)
			return err
		}); err != nil {
			return res, err
		}
		res.Deleted = len(gone)
	}
	for len(write) > 0 {
		n := min(syncBatch, len(write))
		part := write[:n]
		write = write[n:]
		if err := x.d.Tx(ctx, func(tx pgx.Tx) error {
			b := &pgx.Batch{}
			for _, d := range part {
				id := db.NewID()
				var src *string
				if d.kind == "excerpt" {
					s := d.source
					src = &s
				}
				b.Queue("DELETE FROM knowledge_documents WHERE path = $1", d.path)
				b.Queue(`INSERT INTO knowledge_documents (knowledge_document_id, path, kind, knowledge_source_id, item_key, title, heading, page_kind, item_version, url, dated_at,
						product, product_version, is_reviewed, content_hash, indexed_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())`,
					id, d.path, d.kind, src, d.itemKey, d.title, d.heading, d.pageKind, d.version, d.url, pgAtOrNone(d.date), d.product, d.productVersion, d.reviewed, d.hash)
				for i, body := range d.passages {
					b.Queue(`INSERT INTO knowledge_passages (knowledge_passage_id, knowledge_document_id, position, body, squashed, search)
						VALUES ($1, $2, $3, $4, $5, setweight(to_tsvector('english', $6), 'A') || setweight(to_tsvector('english', $4), 'D'))`,
						db.NewID(), id, i, body, squash(body), strings.TrimSpace(d.title+" "+d.heading))
				}
			}
			return tx.SendBatch(ctx, b).Close()
		}); err != nil {
			return res, err
		}
		res.Written += n
	}
	return res, nil
}

// squash is a text's letters and digits alone, in lower case.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Count is how many documents and passages the index holds.
func (x *PGIndex) Count(ctx context.Context) (docs, passages int, err error) {
	err = x.d.Do(ctx, func(q db.Querier) error {
		return q.QueryRow(ctx, "SELECT (SELECT count(*) FROM knowledge_documents), (SELECT count(*) FROM knowledge_passages)").Scan(&docs, &passages)
	})
	return docs, passages, err
}

// IndexQuery is a search of the index.
type IndexQuery struct {
	Text string
	// Kind is "page", "excerpt", or "" for both.
	Kind  string
	Limit int
}

// indexHit is a passage the index found, with its document.
type indexHit struct {
	passage                                                                  string
	position                                                                 int
	path, kind, source, itemKey, title, heading, pageKind, version, url, body string
	date                                                                     time.Time
	product, productVersion                                                  string
	reviewed                                                                 bool
	score                                                                    float64
}

// candidates is how many passages each way of searching gives, before
// the best of each document is kept.
const candidates = 60

// tsQuery is a query's words as a full-text query that matches any of
// them: PostgreSQL stems them and drops its stop words.
func tsQuery(text string) string {
	var words []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(f) < 2 || stopWords[f] || seen[f] {
			continue
		}
		seen[f] = true
		words = append(words, f)
	}
	return strings.Join(words, " | ")
}

// docFilter is the condition on knowledge_documents d a search keeps,
// its arguments from $first on.
func docFilter(q IndexQuery, first int) (string, []any) {
	if q.Kind == "" {
		return "true", nil
	}
	return fmt.Sprintf("d.kind = $%d", first), []any{q.Kind}
}

// keyword ranks the passages whose words match the query's.
func (x *PGIndex) keyword(ctx context.Context, q IndexQuery) ([]string, error) {
	tq := tsQuery(q.Text)
	if tq == "" {
		return nil, nil
	}
	where, args := docFilter(q, 3)
	var out []string
	err := x.d.Do(ctx, func(qq db.Querier) error {
		rows, err := qq.Query(ctx, `SELECT p.knowledge_passage_id FROM knowledge_passages p JOIN knowledge_documents d USING (knowledge_document_id),
				to_tsquery('english', $1) tq
			WHERE p.search @@ tq AND `+where+`
			ORDER BY ts_rank_cd(p.search, tq) DESC, p.knowledge_passage_id LIMIT $2`, append([]any{tq, candidates}, args...)...)
		if err != nil {
			return err
		}
		var id string
		_, err = pgx.ForEachRow(rows, []any{&id}, func() error { out = append(out, id); return nil })
		return err
	})
	return out, err
}

// rrfK is reciprocal rank fusion's constant: a passage's score from one
// ranking is 1/(rrfK + its rank).
const rrfK = 60

// Search ranks the passages for a query and gives the best passage of
// each document, best first.
func (x *PGIndex) Search(ctx context.Context, q IndexQuery) ([]indexHit, error) {
	if q.Limit <= 0 {
		q.Limit = 10
	}
	ranked, err := x.keyword(ctx, q)
	if err != nil {
		return nil, err
	}
	score := map[string]float64{}
	for i, id := range ranked {
		score[id] += 1.0 / float64(rrfK+i+1)
	}
	return x.hits(ctx, score, q.Limit, nil)
}

// hits reads the passages scored, weights each by weight (nil: 1), keeps
// the best of each document and gives the limit best.
func (x *PGIndex) hits(ctx context.Context, score map[string]float64, limit int, weight func(indexHit) float64) ([]indexHit, error) {
	if len(score) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(score))
	for id := range score {
		ids = append(ids, id)
	}
	var all []indexHit
	err := x.d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT p.knowledge_passage_id, p.position, p.body, d.path, d.kind, coalesce(d.knowledge_source_id, ''), d.item_key, d.title, d.heading, d.page_kind,
				d.item_version, d.url, d.dated_at, d.product, d.product_version, d.is_reviewed
			FROM knowledge_passages p JOIN knowledge_documents d USING (knowledge_document_id) WHERE p.knowledge_passage_id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		var h indexHit
		var date *time.Time
		_, err = pgx.ForEachRow(rows, []any{&h.passage, &h.position, &h.body, &h.path, &h.kind, &h.source, &h.itemKey, &h.title, &h.heading, &h.pageKind, &h.version, &h.url, &date,
			&h.product, &h.productVersion, &h.reviewed}, func() error {
			x := h
			x.date = pgTimeOf(date)
			all = append(all, x)
			return nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	best := map[string]int{}
	var out []indexHit
	for _, h := range all {
		h.score = score[h.passage]
		if weight != nil {
			h.score *= weight(h)
		}
		if i, ok := best[h.path]; ok {
			if out[i].score < h.score {
				out[i] = h
			}
			continue
		}
		best[h.path] = len(out)
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].path < out[j].path
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// errNoIndex is a search asked of a service without the index in the
// database (a test).
var errNoIndex = errors.New("the knowledge index is not in the database here")
