package knowledge

import (
	"errors"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The search is measured with questions and the passage that answers
// each: its rank in each way of searching, and how often it is among the
// 6 hits (KN-60).
func TestTheSearchIsMeasured(t *testing.T) {
	d := dbtest.Open(t)
	store := NewMemoryStore()
	s := pgReplica(t, d, store, true)
	s.o.Embedder = &topics{}
	if err := store.Write(ctx, []Change{
		excerptFile("documents:servers.md", "Servers", "The database runs on the Dell R740-A in rack 4."),
		excerptFile("documents:backups.md", "Backups", "Copies are kept on the NAS for thirty days, and an archive monthly."),
		excerptFile("documents:network.md", "Network", "The firewall fw-1 separates the VLAN of the shop."),
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	qs := []EvalQuestion{
		{Question: "where is the r740a", Source: "Uploaded documents", Path: "servers.md"},
		{Question: "firewall of the shop", Source: "documents", Path: "network.md"},
		{Question: "where are the archives kept", Source: "documents", Path: "nowhere.md"},
	}
	if _, err := s.Evaluate(ctx, viewer, qs); !errors.Is(err, ErrDenied) {
		t.Fatalf("a Viewer measures: %v", err)
	}
	if _, err := s.Evaluate(ctx, admin, []EvalQuestion{{Question: "x"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a question without its passage: %v", err)
	}
	rep, err := s.Evaluate(ctx, bob, qs)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Limit != 6 || rep.Questions != 3 || len(rep.Answers) != 3 {
		t.Fatalf("the report: %+v", rep)
	}
	if r := rep.Answers[0].Ranks; r[ModeFuzzy] != 1 || r[ModeKeyword] != 0 || r["hybrid"] != 1 {
		t.Errorf("r740a: %v", r)
	}
	if r := rep.Answers[1].Ranks; r[ModeKeyword] != 1 || r["hybrid"] != 1 {
		t.Errorf("firewall: %v", r)
	}
	if r := rep.Answers[2].Ranks; r["hybrid"] != 0 || len(rep.Answers[2].Found) == 0 {
		t.Errorf("a passage of none: %v, %v", r, rep.Answers[2].Found)
	}
	if rep.Hits["hybrid"] != 2 || rep.Recall[ModeKeyword] != 1.0/3 || rep.Semantic || rep.Note == "" {
		t.Errorf("the totals: %+v", rep)
	}
	// With an embedding model and pgvector, the semantic search is measured.
	if !s.pg.features(ctx).vector {
		t.Skip("no pgvector here")
	}
	if err := s.SetEmbeddings(ctx, admin, EmbeddingSettings{Provider: "a5000", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if err := s.embedAll(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Evaluate(ctx, bob, []EvalQuestion{{Question: "how do I recover lost data", Source: "documents", Path: "backups.md"}})
	if err != nil || !rep.Semantic || rep.Answers[0].Ranks[ModeSemantic] != 1 || rep.Answers[0].Ranks[ModeKeyword] != 0 {
		t.Fatalf("semantic: %+v, %v", rep, err)
	}
}
