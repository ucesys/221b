package knowledge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Hybrid search (phase-13.1-ai, part B, KN-54): three rankings of the
// passages, merged by reciprocal rank fusion (each passage scores 1/(60 +
// its rank) in each ranking it is in, summed).
//
//   - Keyword: PostgreSQL's full-text search, the query's words stemmed.
//   - Fuzzy: pg_trgm's word similarity of each of the query's words to the
//     passage (typos: "kubelte" finds "kubelet"), and the word's letters
//     and digits found in the passage's (spellings: "r740a" finds
//     "R740-A", and "r740-a" "r740a"). Without pg_trgm, the second alone.
//   - Semantic: the cosine distance of the query's embedding to each
//     passage's, by the embedding model chosen in Administration › AI ›
//     Knowledge (KN-55), with pgvector. None without either: keyword and
//     fuzzy search answer.

// The ways of searching, as IndexQuery.Modes names them.
const (
	ModeKeyword  = "keyword"
	ModeFuzzy    = "fuzzy"
	ModeSemantic = "semantic"
)

// features are the extensions the database has.
type features struct {
	trgm, vector bool
}

// featureCache keeps what the database has for a while: a replica looks
// again every featureTTL, so an extension created later is used.
type featureCache struct {
	mu   sync.Mutex
	at   time.Time
	have features
	// made says the trigram indexes were made.
	made bool
}

const featureTTL = 2 * time.Minute

// features are the extensions the database has now.
func (x *PGIndex) features(ctx context.Context) features {
	x.feat.mu.Lock()
	defer x.feat.mu.Unlock()
	if !x.feat.at.IsZero() && time.Since(x.feat.at) < featureTTL {
		return x.feat.have
	}
	var f features
	err := x.d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT extname FROM pg_extension WHERE extname IN ('pg_trgm', 'vector')")
		if err != nil {
			return err
		}
		var name string
		_, err = pgx.ForEachRow(rows, []any{&name}, func() error {
			f.trgm = f.trgm || name == "pg_trgm"
			f.vector = f.vector || name == "vector"
			return nil
		})
		return err
	})
	if err != nil {
		return x.feat.have
	}
	x.feat.at, x.feat.have = time.Now(), f
	return f
}

// ensureExtensions, on the leader, creates the extensions where the
// console's role may (pg_trgm is trusted; vector needs a superuser, which
// the bundled database's extensions.sh is, KN-51) and the trigram indexes
// once pg_trgm is there.
func (x *PGIndex) ensureExtensions(ctx context.Context) features {
	f := x.features(ctx)
	if !f.trgm || !f.vector {
		for _, ext := range []string{"pg_trgm", "vector"} {
			if (ext == "pg_trgm" && f.trgm) || (ext == "vector" && f.vector) {
				continue
			}
			if err := x.d.Do(ctx, func(q db.Querier) error {
				_, err := q.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext)
				return err
			}); err != nil {
				x.log.Debug("an extension of the knowledge search could not be created", "extension", ext, "err", err)
			}
		}
		x.feat.mu.Lock()
		x.feat.at = time.Time{}
		x.feat.mu.Unlock()
		f = x.features(ctx)
	}
	x.feat.mu.Lock()
	made := x.feat.made
	x.feat.mu.Unlock()
	if f.trgm && !made {
		err := x.d.Do(ctx, func(q db.Querier) error {
			for _, col := range []string{"body", "squashed"} {
				if _, err := q.Exec(ctx, fmt.Sprintf("CREATE INDEX IF NOT EXISTS knowledge_passages_%s_trgm_idx ON knowledge_passages USING gin (%s gin_trgm_ops)", col, col)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			x.log.Warn("the knowledge search's trigram indexes could not be made; fuzzy search reads every passage", "err", err)
		} else {
			x.feat.mu.Lock()
			x.feat.made = true
			x.feat.mu.Unlock()
		}
	}
	return f
}

// fuzzyWords are a query's words for fuzzy search, each with its letters
// and digits: a word of four letters or more, or of three with a digit,
// stop words left out. A word keeps its dashes, dots and slashes, so
// "r740-a" is one.
func fuzzyWords(text string) (words, squashed []string) {
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '.' && r != '/' && r != '_'
	}) {
		f = strings.Trim(f, "-./_")
		sq := squash(f)
		digit := strings.IndexFunc(sq, unicode.IsDigit) >= 0
		if stopWords[f] || seen[sq] || len(sq) < 3 || (len(sq) < 4 && !digit) {
			continue
		}
		seen[sq] = true
		words, squashed = append(words, f), append(squashed, sq)
	}
	return words, squashed
}

// fuzzyThreshold is the word similarity a passage's closest words need
// to a query's word to match it (pg_trgm's default is 0.6, which a typo
// in a short word does not reach).
const fuzzyThreshold = 0.45

// fuzzy ranks the passages by how many of the query's words they hold
// close to as written.
func (x *PGIndex) fuzzy(ctx context.Context, q IndexQuery, f features) ([]string, error) {
	words, squashed := fuzzyWords(q.Text)
	if len(words) == 0 {
		return nil, nil
	}
	where, args := docFilter(q, 4)
	var sql string
	if f.trgm {
		sql = `SELECT p.knowledge_passage_id FROM knowledge_passages p JOIN knowledge_documents d USING (knowledge_document_id)
				CROSS JOIN unnest($1::text[], $2::text[]) AS t(word, sq)
			WHERE (t.word <% p.body OR p.squashed LIKE '%' || t.sq || '%') AND ` + where + `
			GROUP BY p.knowledge_passage_id
			ORDER BY sum(greatest(word_similarity(t.word, p.body), CASE WHEN p.squashed LIKE '%' || t.sq || '%' THEN 1 ELSE 0 END)) DESC, p.knowledge_passage_id
			LIMIT $3`
	} else {
		sql = `SELECT p.knowledge_passage_id FROM knowledge_passages p JOIN knowledge_documents d USING (knowledge_document_id)
				CROSS JOIN unnest($1::text[], $2::text[]) AS t(word, sq)
			WHERE p.squashed LIKE '%' || t.sq || '%' AND ` + where + `
			GROUP BY p.knowledge_passage_id
			ORDER BY count(*) DESC, p.knowledge_passage_id
			LIMIT $3`
	}
	var out []string
	err := x.d.Tx(ctx, func(tx pgx.Tx) error {
		out = out[:0]
		if f.trgm {
			if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL pg_trgm.word_similarity_threshold = %g", fuzzyThreshold)); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, sql, append([]any{words, squashed, candidates}, args...)...)
		if err != nil {
			return err
		}
		var id string
		_, err = pgx.ForEachRow(rows, []any{&id}, func() error { out = append(out, id); return nil })
		return err
	})
	return out, err
}

// semantic ranks the passages by how close their embeddings are to the
// query's, by the model that made it.
func (x *PGIndex) semantic(ctx context.Context, q IndexQuery) ([]string, error) {
	if len(q.Vector) == 0 || q.Model == "" {
		return nil, nil
	}
	where, args := docFilter(q, 5)
	var out []string
	err := x.d.Do(ctx, func(qq db.Querier) error {
		rows, err := qq.Query(ctx, `SELECT e.knowledge_passage_id FROM knowledge_embeddings e JOIN knowledge_passages p USING (knowledge_passage_id)
				JOIN knowledge_documents d USING (knowledge_document_id)
			WHERE e.model = $2 AND e.dimensions = $3 AND `+where+`
			ORDER BY e.embedding::vector <=> $1::real[]::vector, e.knowledge_passage_id LIMIT $4`, append([]any{q.Vector, q.Model, len(q.Vector), candidates}, args...)...)
		if err != nil {
			return err
		}
		var id string
		_, err = pgx.ForEachRow(rows, []any{&id}, func() error { out = append(out, id); return nil })
		return err
	})
	return out, err
}

// unembedded are passages without an embedding by a model, with the text
// to embed: their document's title and heading, and their body.
func (x *PGIndex) unembedded(ctx context.Context, model string, limit int) (ids, texts []string, err error) {
	err = x.d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT p.knowledge_passage_id, d.title, d.heading, p.body FROM knowledge_passages p JOIN knowledge_documents d USING (knowledge_document_id)
				LEFT JOIN knowledge_embeddings e ON e.knowledge_passage_id = p.knowledge_passage_id AND e.model = $1
			WHERE e.knowledge_passage_id IS NULL ORDER BY d.path, p.position LIMIT $2`, model, limit)
		if err != nil {
			return err
		}
		var id, title, heading, body string
		_, err = pgx.ForEachRow(rows, []any{&id, &title, &heading, &body}, func() error {
			ids = append(ids, id)
			t := title
			if heading != "" && !strings.Contains(title, heading) {
				t += " › " + heading
			}
			texts = append(texts, t+"\n\n"+body)
			return nil
		})
		return err
	})
	return ids, texts, err
}

// storeEmbeddings keeps passages' embeddings by a model, in place of any
// they had.
func (x *PGIndex) storeEmbeddings(ctx context.Context, model string, ids []string, vecs [][]float32) error {
	return x.d.Tx(ctx, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for i, id := range ids {
			b.Queue(`INSERT INTO knowledge_embeddings (knowledge_passage_id, model, dimensions, embedding, embedded_at) VALUES ($1, $2, $3, $4, now())
				ON CONFLICT (knowledge_passage_id) DO UPDATE SET model = excluded.model, dimensions = excluded.dimensions, embedding = excluded.embedding, embedded_at = excluded.embedded_at`,
				id, model, len(vecs[i]), vecs[i])
		}
		return tx.SendBatch(ctx, b).Close()
	})
}

// embedded is how many passages have an embedding by a model.
func (x *PGIndex) embedded(ctx context.Context, model string) (int, error) {
	n := 0
	err := x.d.Do(ctx, func(q db.Querier) error {
		return q.QueryRow(ctx, "SELECT count(*) FROM knowledge_embeddings WHERE model = $1", model).Scan(&n)
	})
	return n, err
}
