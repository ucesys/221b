package knowledge

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// topics is a fake embedding model: a text's embedding counts its words
// of each topic, so texts on one topic are close whatever their words.
type topics struct {
	mu     sync.Mutex
	calls  int
	failOn func(texts []string) bool
}

var topicWords = [][]string{
	{"backup", "backups", "restore", "nas", "archive", "data", "copies", "recover"},
	{"network", "firewall", "vlan", "switch", "route", "packets"},
	{"kubelet", "pods", "node", "nodes", "cluster"},
}

func (e *topics) Embed(_ context.Context, provider, model string, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	fail := e.failOn
	e.mu.Unlock()
	if provider != "a5000" {
		return nil, errors.New("no such provider")
	}
	if fail != nil && fail(texts) {
		return nil, errors.New("the endpoint does not answer")
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(topicWords)+1)
		v[len(topicWords)] = 0.1
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !isWordRune(r) }) {
			for d, list := range topicWords {
				for _, x := range list {
					if w == x {
						v[d]++
					}
				}
			}
		}
		if model == "other" {
			v[0], v[1] = v[1], v[0]
		}
		n := 0.0
		for _, x := range v {
			n += float64(x * x)
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(n))
		}
		out[i] = v
	}
	return out, nil
}

func (e *topics) Providers(context.Context) []EmbeddingProvider {
	return []EmbeddingProvider{{ID: "a5000", Name: "A5000 vLLM", Endpoint: "http://a5000:8000/v1"}}
}

// Keyword, fuzzy and semantic search, merged; keyword and fuzzy alone when
// the query cannot be embedded (KN-54, KN-55).
func TestHybridSearch(t *testing.T) {
	d := dbtest.Open(t)
	store := NewMemoryStore()
	leader := pgReplica(t, d, store, true)
	emb := &topics{}
	leader.o.Embedder = emb
	var audits []audit.Event
	leader.o.Audit = func(e audit.Event) { audits = append(audits, e) }
	if err := store.Write(ctx, []Change{
		excerptFile("documents:servers.md", "Servers", "The database runs on the Dell R740-A in rack 4."),
		excerptFile("documents:nodes.md", "Nodes", "When the kubelet stops, the node goes NotReady and its pods are evicted."),
		excerptFile("documents:backups.md", "Backups", "Copies are kept on the NAS for thirty days, and an archive monthly."),
		excerptFile("documents:network.md", "Network", "The firewall fw-1 separates the VLAN of the shop."),
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := leader.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := leader.SyncIndex(ctx); err != nil {
		t.Fatal(err)
	}
	f := leader.pg.features(ctx)
	titles := func(hits []Hit) string {
		var out []string
		for _, h := range hits {
			out = append(out, h.Title)
		}
		return strings.Join(out, ", ")
	}
	search := func(q string, modes ...string) []Hit {
		t.Helper()
		hits, err := leader.searchModes(ctx, q, "", 6, modes)
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	// Spellings: the letters and digits meet.
	for _, q := range []string{"r740a", "R740-A", "r740 a"} {
		if got := titles(search(q, ModeFuzzy)); !strings.HasPrefix(got, "Servers") {
			t.Errorf("fuzzy %q: %s", q, got)
		}
	}
	if got := titles(search("r740a", ModeKeyword)); got != "" {
		t.Errorf("keyword search does not join spellings: %s", got)
	}
	if got := titles(search("r740a")); !strings.HasPrefix(got, "Servers") {
		t.Errorf("hybrid r740a: %s", got)
	}
	if f.trgm {
		// Typos, with pg_trgm.
		if got := titles(search("kubelte", ModeFuzzy)); !strings.HasPrefix(got, "Nodes") {
			t.Errorf("fuzzy kubelte: %s", got)
		}
	} else {
		t.Log("no pg_trgm here: typos are not tried")
	}
	// No embedding model: no semantic search, keyword and fuzzy answer.
	if got := titles(search("how do I recover lost data", ModeSemantic)); got != "" {
		t.Errorf("semantic search without a model: %s", got)
	}
	if err := leader.SetEmbeddings(ctx, bob, EmbeddingSettings{Provider: "a5000", Model: "qwen3-embedding"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin chooses the model: %v", err)
	}
	if err := leader.SetEmbeddings(ctx, admin, EmbeddingSettings{Provider: "nowhere", Model: "m"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown provider: %v", err)
	}
	if err := leader.SetEmbeddings(ctx, admin, EmbeddingSettings{Provider: "a5000", Model: "qwen3-embedding"}); err != nil {
		t.Fatal(err)
	}
	if len(audits) == 0 || audits[len(audits)-1].Action != ActionEmbeddings || !strings.Contains(audits[len(audits)-1].Detail, "qwen3-embedding on a5000, was none") {
		t.Fatalf("the change is audited: %+v", audits)
	}
	if !f.vector {
		t.Skip("no pgvector here: semantic search is not tried")
	}
	leader.Wait()
	if err := leader.embedAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := leader.pg.embedded(ctx, "qwen3-embedding"); n != 4 {
		t.Fatalf("%d passages embedded", n)
	}
	// Meaning, without a word in common.
	if got := titles(search("how do I recover lost data", ModeKeyword)); got != "" {
		t.Errorf("keyword: %s", got)
	}
	if got := titles(search("how do I recover lost data", ModeSemantic)); !strings.HasPrefix(got, "Backups") {
		t.Errorf("semantic: %s", got)
	}
	if hits, err := leader.Ask(ctx, "how do I recover lost data", 6); err != nil || !strings.Contains(titles(hits), "Backups") {
		t.Errorf("ask: %s, %v", titles(hits), err)
	}
	v, err := leader.GetView(ctx, admin)
	if err != nil || v.Index == nil || v.Index.Embedded != 4 || v.Index.Passages != 4 || len(v.Index.Providers) != 1 || v.Settings.Embeddings.Model != "qwen3-embedding" {
		t.Fatalf("the view: %+v, %v", v.Index, err)
	}
	// The endpoint away when a query is embedded: keyword and fuzzy answer.
	emb.failOn = func(texts []string) bool { return len(texts) == 1 }
	if got := titles(search("Dell R740-A")); !strings.HasPrefix(got, "Servers") {
		t.Errorf("without the endpoint: %s", got)
	}
	emb.failOn = nil
	// A change of model embeds every passage again.
	if err := leader.SetEmbeddings(ctx, admin, EmbeddingSettings{Provider: "a5000", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	leader.Wait()
	if err := leader.embedAll(ctx); err != nil {
		t.Fatal(err)
	}
	var models []string
	rows, _ := d.Pool().Query(ctx, "SELECT DISTINCT model FROM knowledge_embeddings")
	for rows.Next() {
		var m string
		_ = rows.Scan(&m)
		models = append(models, m)
	}
	if strings.Join(models, ",") != "other" {
		t.Fatalf("the embeddings after a change of model: %v", models)
	}
	// None turns semantic search off.
	if err := leader.SetEmbeddings(ctx, admin, EmbeddingSettings{}); err != nil {
		t.Fatal(err)
	}
	if got := titles(search("how do I recover lost data", ModeSemantic)); got != "" {
		t.Errorf("semantic search with none: %s", got)
	}
}
