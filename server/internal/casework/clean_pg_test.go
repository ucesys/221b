package casework

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
)

// A clean start cleans the records where the console keeps them: with a
// database the runs, the agents' proposed changes, the exceptions and the
// fixes, and Mycroft's drafts and syncs' state, are rows, reached through
// the backends their services read, and the rows go.
func TestACleanStartCleansTheDatabasesRows(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	runs := &kv.Keyed{Keys: map[string]kv.Backend{jobs.RunsKey: jobs.NewRunsPG(d)}, Rest: &kv.Memory{}}
	proposed := &kv.Keyed{Keys: map[string]kv.Backend{cards.CardsKey: cards.NewCardsPG(d)}, Rest: &kv.Memory{}}
	if _, err := runs.SaveKey(ctx, jobs.RunsKey, []byte(`{"next":2,"runs":[{"id":"run-1","job":"audit","started":"2026-11-07T02:00:00Z","findings":[{"id":"run-1.1"}]}]}`), ""); err != nil {
		t.Fatal(err)
	}
	card := func(id, kind string) string {
		return `{"id":"` + id + `","agent":"auditor","source":{"kind":"` + kind + `","label":"x"},"cluster":"local","title":"t","actions":[{"kind":"kubectl","args":{"command":"kubectl get ns"}}],` +
			`"condition":{"kind":"none"},"state":"queued","created":"2026-11-07T02:00:00Z","updated":"2026-11-07T02:00:00Z","needed":1,"plans":[{"summary":"s"}]}`
	}
	if _, err := proposed.SaveKey(ctx, cards.CardsKey, []byte(`{"next":5,"cards":[`+strings.Join([]string{card("PC-1", "job"), card("PC-2", "chat"), card("PC-3", "case"), card("PC-4", "alert"), card("PC-5", "knowledge")}, ",")+`]}`), ""); err != nil {
		t.Fatal(err)
	}
	beside := &kv.Keyed{Keys: map[string]kv.Backend{lessons.ExceptionsKey: lessons.NewExceptionsPG(d), fixes.Key: fixes.NewPG(d)}, Rest: &kv.Memory{}}
	if _, err := beside.SaveKey(ctx, lessons.ExceptionsKey, []byte(`{"next":2,"items":[{"id":"EX-1","check":"pod.no-limits","title":"t","reason":"r","by":"bob","at":"2026-11-07T02:00:00Z","matches":0}]}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := beside.SaveKey(ctx, fixes.Key, []byte(`{"next":2,"items":[{"id":"FX-1","title":"t","status":"active","cases":[{"case":"C-1","at":"2026-11-07T02:00:00Z"}],"created":"2026-11-07T02:00:00Z","updated":"2026-11-07T02:00:00Z","createdBy":"bob","updatedBy":"bob"}]}`), ""); err != nil {
		t.Fatal(err)
	}
	wiki := &kv.Keyed{Keys: map[string]kv.Backend{knowledge.DraftsKey: knowledge.NewDraftsPG(d), knowledge.StatusKey: knowledge.NewStatusPG(d), knowledge.SettingsKey: knowledge.NewSettingsPG(d)}, Rest: &kv.Memory{}}
	for key, doc := range map[string]string{
		knowledge.DraftsKey:   `{"runs":[{"id":"r1","started":"2026-11-07T02:00:00Z","by":"sync","state":"waiting","store":"git","pages":[{"path":"pages/a.md","title":"A","kind":"page","diff":"+a","added":1,"removed":0,"state":"waiting"}]}]}`,
		knowledge.StatusKey:   `{"running":false,"sources":{"infra":{"items":3}},"history":[{"started":"2026-11-07T02:00:00Z","by":"sync","full":true,"read":3,"unchanged":0,"pages":1,"proposals":0,"text":"ok"}]}`,
		knowledge.SettingsKey: `{"sources":[{"id":"infra","name":"Infra","kind":"git","url":"https://git.example/r.git"}],"store":{"kind":"git"}}`,
	} {
		if _, err := wiki.SaveKey(ctx, key, []byte(doc), ""); err != nil {
			t.Fatal(key, err)
		}
	}
	st := CleanStores{Casework: beside, Runs: runs, Cards: proposed, Knowledge: wiki}
	if res, err := CleanStart(ctx, st, "2026-11-19", false, time.Now(), slogTo(&strings.Builder{}), nil); err != nil || !res.Done {
		t.Fatalf("clean: %+v %v", res, err)
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT count(*) FROM job_runs") + count("SELECT count(*) FROM findings"); n != 0 {
		t.Fatalf("%d rows of runs and findings left", n)
	}
	if n := count("SELECT count(*) FROM exceptions") + count("SELECT count(*) FROM fixes") + count("SELECT count(*) FROM fix_cases"); n != 0 {
		t.Fatalf("%d rows of exceptions and fixes left", n)
	}
	// Mycroft's drafts and the syncs' state go; the sources stay.
	if n := count("SELECT count(*) FROM wiki_runs") + count("SELECT count(*) FROM wiki_changes") + count("SELECT count(*) FROM knowledge_syncs") + count("SELECT count(*) FROM knowledge_source_reads"); n != 0 {
		t.Fatalf("%d rows of drafts and syncs left", n)
	}
	if n := count("SELECT count(*) FROM knowledge_sources"); n != 1 {
		t.Fatalf("%d sources left, want the one", n)
	}
	var kept string
	if err := d.Pool().QueryRow(ctx, "SELECT string_agg(change_key, ',' ORDER BY position) FROM changes").Scan(&kept); err != nil || kept != "PC-2,PC-5" {
		t.Fatalf("the cards kept: %q, %v", kept, err)
	}
	if n := count("SELECT count(*) FROM change_actions"); n != 2 {
		t.Fatalf("%d actions left of 2 cards", n)
	}
}
