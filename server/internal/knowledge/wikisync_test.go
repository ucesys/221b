package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
)

// syncBuf is an audit log's output a test reads while jobs write it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *syncBuf) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

// withJobs gives the fixture the console's jobs over backend (a new one
// when nil), as cmd/221b does.
func (f *fixture) withJobs(t *testing.T, backend kv.Backend, log io.Writer) *jobs.Service {
	t.Helper()
	if backend == nil {
		backend = &kv.Memory{}
	}
	if log == nil {
		log = io.Discard
	}
	js := jobs.New(jobs.Options{Backend: backend, Kinds: jobs.NewRegistry(), Now: f.clock.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Audit: audit.New(audit.Options{Out: log})})
	t.Cleanup(js.Stop)
	cctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.s.SetJobs(cctx, js)
	return js
}

func runsOf(t *testing.T, js *jobs.Service, job string) []jobs.Run {
	t.Helper()
	briefs, _, err := js.Runs(ctx, jobs.RunFilter{Job: job})
	if err != nil {
		t.Fatal(err)
	}
	var out []jobs.Run
	for _, b := range briefs {
		r, err := js.GetRun(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func fact(r jobs.Run, label string) jobs.Fact {
	for _, f := range r.Facts {
		if f.Label == label {
			return f
		}
	}
	return jobs.Fact{}
}

// Batches that fit (KN-27): at most four items and 32 KiB of their text a
// request.
func TestBatchesFitKN27(t *testing.T) {
	var items []readItem
	for i := 0; i < 9; i++ {
		items = append(items, readItem{key: string(rune('a' + i)), text: "short"})
	}
	if b := batches(items); len(b) != 3 || len(b[0]) != 4 || len(b[2]) != 1 {
		t.Fatalf("by count: %d batches", len(b))
	}
	big := strings.Repeat("x", 20<<10)
	if b := batches([]readItem{{text: big}, {text: big}, {text: "small"}}); len(b) != 2 || len(b[0]) != 1 || len(b[1]) != 2 {
		t.Fatalf("by size: %v", len(b))
	}
}

// A cut answer (KN-27): the live bug's answer, cut at the output limit,
// splits its batch in two, each half asked again; an item still too large
// alone is reported so, its excerpts kept and not sent again; the other
// pages are drafted and the sync does not fail. Each batch's outcome is
// on the result and in the report.
func TestACutAnswerSplitsItsBatchKN27(t *testing.T) {
	f := newFixture(t)
	f.model.cut = func(text string) bool {
		return strings.Count(text, "<source ") > 1 || strings.Contains(text, `title="Huge runbook"`)
	}
	ids := f.space(t, map[string]string{"Backups": "<p>Nightly at 02:00.</p>", "Huge runbook": "<p>Very many steps.</p>", "Web tier": "<p>Three pods.</p>"})
	res, err := f.s.Sync(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Read != 3 || res.Pages != 2 || res.Run == "" {
		t.Fatalf("the sync: %+v", res)
	}
	counts := map[string]int{}
	for _, b := range res.Batches {
		counts[b.Outcome]++
	}
	if counts[BatchSplit] != 2 || counts[BatchTooLarge] != 1 || counts[BatchOK] != 2 || counts[BatchNotSent] != 0 {
		t.Fatalf("the batches: %+v", res.Batches)
	}
	if res.Batches[0].Outcome != BatchSplit || len(res.Batches[0].Items) != 3 || !strings.Contains(res.Batches[0].Note, "cut at the most Mycroft may write (16000 tokens)") {
		t.Fatalf("the first batch: %+v", res.Batches[0])
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "“Huge runbook” is too large for one answer of Mycroft's") || !strings.Contains(res.Errors[0], "its excerpts are kept") {
		t.Fatalf("the errors: %v", res.Errors)
	}
	for _, want := range []string{"**Mycroft’s batches**", "split in two and asked again", "too large for one answer", "**Errors**"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("the report lacks %q: %s", want, res.Text)
		}
	}
	if res.TokensIn == 0 || res.CostUSD < 0.05 || res.Model != "claude-test" {
		t.Fatalf("what the answers took: %+v", res)
	}
	// The excerpts of the item too large are kept for the search.
	files, _ := f.store.Pull(ctx)
	if _, ok := files[excerptPath(itemKey("infra", ids["Huge runbook"]))]; !ok {
		t.Fatal("the too-large item's excerpts were not kept")
	}
	// It is not sent again: the next sync reads nothing new.
	n := len(f.model.calls())
	if res, _ := f.s.Sync(ctx, true); res.Read != 0 || len(f.model.calls()) != n {
		t.Fatalf("sent again: %+v", res)
	}
}

// A model that fails leaves what it never had to the next sync (KN-27).
func TestWhatMycroftNeverHadIsReadAgainKN27(t *testing.T) {
	f := newFixture(t)
	fail := true
	f.model.reply = func(text string) string {
		if fail {
			return "no JSON at all"
		}
		return `{"pages":[]}`
	}
	f.space(t, map[string]string{"Backups": "<p>Nightly at 02:00.</p>"})
	res, _ := f.s.Sync(ctx, true)
	if len(res.Batches) != 1 || res.Batches[0].Outcome != BatchTooLarge || !strings.Contains(res.Batches[0].Note, "could not be read") {
		t.Fatalf("an answer that is not JSON: %+v", res.Batches)
	}
	// The budget spent stops before the next batch; its items come again.
	fail = false
	f2 := newFixture(t)
	f2.space(t, map[string]string{"A": "<p>a</p>", "B": "<p>b</p>", "C": "<p>c</p>", "D": "<p>d</p>", "E": "<p>e</p>"})
	spent := 0
	res, _ = f2.s.SyncWith(ctx, SyncRequest{Full: true, By: "test", Spend: func(Completion) error {
		spent++
		return jobs.ErrBudget
	}})
	if spent != 1 || res.Unsent != 1 || res.stop == nil || res.Batches[len(res.Batches)-1].Outcome != BatchNotSent {
		t.Fatalf("budget: %d %+v", spent, res)
	}
	if res, _ := f2.s.Sync(ctx, true); res.Read != 1 {
		t.Fatalf("the item not sent is read again: %+v", res)
	}
}

// A Wiki sync job per source (KN-28): each source has its job, by its
// kind; adding a source makes its job and reads it at once as a run;
// renaming renames it; switching off pauses it; removing lets it go with
// its runs kept. A run's report and facts say what the sync did.
func TestAWikiSyncJobPerSourceKN28(t *testing.T) {
	f := newFixture(t)
	log := &syncBuf{}
	js := f.withJobs(t, nil, log)
	// Scheduled jobs start off until a SuperAdmin turns them on (JB-03);
	// the job of what people add runs only when they do, and starts on.
	for id, want := range map[string]string{"console": "Every day at 01:00", "documents": "When its source changes", "added": "When its source changes"} {
		v, err := js.Get(ctx, SyncJobID(id))
		if err != nil || v.On != (id != "console") || v.AgentName != "Mycroft" || !strings.HasPrefix(v.WhenText, want) {
			t.Fatalf("%s: %+v %v", id, v, err)
		}
	}
	src := Source{ID: "infra", Name: "Confluence space INFRA", Kind: SourceConfluence, BaseURL: f.conf.URL, Flavor: "datacenter", Space: "INFRA"}
	f.space(t, map[string]string{"Backups": "<p>Nightly at 02:00.</p>", "Web tier": "<p>Three pods.</p>"})
	js.Wait()
	v, err := js.Get(ctx, "wiki-sync-infra")
	if err != nil || v.Label != "Wiki sync: Confluence space INFRA" || v.When.Schedule == nil || v.When.Schedule.Cron != "*/15 * * * *" || v.On {
		t.Fatalf("its job: %+v %v", v, err)
	}
	runs := runsOf(t, js, "wiki-sync-infra")
	if len(runs) != 1 {
		t.Fatalf("read at once: %d runs", len(runs))
	}
	r := runs[0]
	if r.Status != jobs.StatusOK || r.Trigger.What != "Source added by Ann" || !r.NoFindings || len(r.Findings) != 0 || r.Tokens.In == 0 || r.CostUSD == 0 {
		t.Fatalf("the run: %+v", r)
	}
	if got := fact(r, "Items read").Text; got != "2 changed items" {
		t.Fatalf("items read: %q", got)
	}
	if d := fact(r, "Pages drafted"); !strings.HasPrefix(d.Href, "#/cases/inbox/knowledge/r-") || d.Text != "2 page changes wait for review in AIOps › Inbox" {
		t.Fatalf("drafts: %+v", d)
	}
	if fact(r, "Source").Text != "Confluence space INFRA (Confluence space)" || fact(r, "Read").Text != "Every item" {
		t.Fatalf("facts: %+v", r.Facts)
	}
	if !strings.Contains(r.Report, "2 page changes wait for review in [AIOps › Inbox](#/cases/inbox/knowledge/r-") {
		t.Fatalf("report: %s", r.Report)
	}

	// Renamed alone: its job's name follows, and the source is not read
	// again for it.
	src.Name = "INFRA wiki"
	if _, err := f.s.SaveSource(ctx, admin, src, nil, false); err != nil {
		t.Fatal(err)
	}
	js.Wait()
	if v, _ := js.Get(ctx, "wiki-sync-infra"); v.Label != "Wiki sync: INFRA wiki" {
		t.Fatalf("renamed: %q", v.Label)
	}
	if runs := runsOf(t, js, "wiki-sync-infra"); len(runs) != 1 {
		t.Fatalf("a rename read it again: %d runs", len(runs))
	}
	// Changed otherwise (an email Data Center does not use): read again at once.
	src.Email = "ann@example.com"
	if _, err := f.s.SaveSource(ctx, admin, src, nil, false); err != nil {
		t.Fatal(err)
	}
	js.Wait()
	if runs := runsOf(t, js, "wiki-sync-infra"); len(runs) != 2 || runs[0].Trigger.What != "Source changed by Ann" || fact(runs[0], "Unchanged").Text != "3 items" {
		t.Fatalf("read again: %+v", runs[0])
	}

	// Rebuild now (KN-29): every item to Mycroft again, unchanged ones too.
	if _, err := js.RebuildNow(ctx, "wiki-sync-infra", jobs.Person{Subject: "ann", Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	js.Wait()
	if runs := runsOf(t, js, "wiki-sync-infra"); len(runs) != 3 || !runs[0].Trigger.Rebuild || runs[0].Trigger.What != "Rebuild now by Ann" || fact(runs[0], "Items read").Text != "2 changed items" || !strings.Contains(fact(runs[0], "Read").Text, "unchanged ones too") {
		t.Fatalf("rebuilt: %+v", runs[0])
	}

	// Switched off: paused; on again: on, as the SuperAdmin asked.
	if err := f.s.SetSourceOn(ctx, admin, "infra", false); err != nil {
		t.Fatal(err)
	}
	if v, _ := js.Get(ctx, "wiki-sync-infra"); v.On {
		t.Fatal("a source switched off left its job on")
	}
	if err := f.s.SetSourceOn(ctx, admin, "infra", true); err != nil {
		t.Fatal(err)
	}
	if v, _ := js.Get(ctx, "wiki-sync-infra"); !v.On {
		t.Fatal("a source switched on left its job paused")
	}

	// Removed: its job goes, its runs stay.
	if err := f.s.RemoveSource(ctx, admin, "infra"); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Get(ctx, "wiki-sync-infra"); err == nil {
		t.Fatal("the removed source's job is still listed")
	}
	if runs := runsOf(t, js, "wiki-sync-infra"); len(runs) != 3 {
		t.Fatalf("its runs: %d", len(runs))
	}
}

// What people upload starts a run of its source's job (KN-28); a paused
// job reads it at its next run, Run now.
func TestAnUploadStartsARunKN28(t *testing.T) {
	f := newFixture(t)
	js := f.withJobs(t, nil, nil)
	if _, err := f.s.Upload(ctx, admin, "documents", "restart.md", []byte("# Restart\n\nRestart the web pods, then the cache.")); err != nil {
		t.Fatal(err)
	}
	js.Wait()
	runs := runsOf(t, js, "wiki-sync-documents")
	if len(runs) != 1 || runs[0].Trigger.Kind != jobs.TriggerEvent || runs[0].Trigger.What != "Upload of restart.md by Ann" || fact(runs[0], "Items read").Text != "1 changed item" {
		t.Fatalf("the upload's run: %+v", runs)
	}
	// Its drafts are Ann's, who uploaded it.
	if d, _ := f.s.RunByID(ctx, strings.TrimPrefix(fact(runs[0], "Pages drafted").Href, "#/cases/inbox/knowledge/")); d.StartedBy != admin.ID {
		t.Fatalf("the drafts are not the uploader's: %+v", d)
	}
	f.s.pendingMu.Lock()
	held := len(f.s.pending)
	f.s.pendingMu.Unlock()
	if held != 0 {
		t.Fatal("the upload's text was held after Mycroft read it")
	}

	off := false
	if _, err := js.Update(ctx, "wiki-sync-documents", jobs.Change{On: &off}, jobs.Person{Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Upload(ctx, admin, "documents", "later.md", []byte("# Later\n\nRead at the next run.")); err != nil {
		t.Fatal(err)
	}
	js.Wait()
	if runs := runsOf(t, js, "wiki-sync-documents"); len(runs) != 1 {
		t.Fatalf("a paused job ran: %d", len(runs))
	}
	if _, err := js.RunNow(ctx, "wiki-sync-documents", jobs.Person{Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	js.Wait()
	if runs := runsOf(t, js, "wiki-sync-documents"); len(runs) != 2 || fact(runs[0], "Items read").Text != "1 changed item" {
		t.Fatalf("Run now read what waited: %+v", runs[0])
	}
}

// The jobs that read every source before are let go at start (KN-28):
// once, and a second start finds nothing more to do.
func TestRetiredKnowledgeJobsAreLetGoKN28(t *testing.T) {
	backend := &kv.Memory{}
	seed := `{"jobs":{"knowledgeSync":{"id":"knowledgeSync","kind":"knowledgeSync","when":{"schedule":{"kind":"daily","at":"01:00"}},"budget":{"runUSD":3},"on":true,"skipOverlap":true,"catchUp":true},` +
		`"knowledgeChanges":{"id":"knowledgeChanges","kind":"knowledgeChanges","when":{},"budget":{},"on":false,"skipOverlap":true,"catchUp":true}},"handled":{"knowledgeSync":"2026-11-04T01:00:00Z"}}`
	if _, err := backend.SaveKey(ctx, jobs.JobsKey, []byte(seed), ""); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	log := &syncBuf{}
	js := f.withJobs(t, backend, log)
	b, _, _ := backend.LoadKey(ctx, jobs.JobsKey)
	var rec struct {
		Jobs    map[string]json.RawMessage `json:"jobs"`
		Handled map[string]time.Time       `json:"handled"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.Jobs["knowledgeSync"]; ok || len(rec.Jobs) != 0 || len(rec.Handled) != 0 {
		t.Fatalf("not let go: %s", b)
	}
	if strings.Count(log.String(), "each knowledge source is read by its own Wiki sync job now") != 2 {
		t.Fatalf("audit: %s", log.String())
	}
	list, _ := js.List(ctx)
	// A second console over the same records: the same jobs, nothing let
	// go again.
	g := newFixture(t)
	js2 := g.withJobs(t, backend, log)
	again, _ := js2.List(ctx)
	// This console, Uploaded documents, Added in Mycroft and Fixes.
	if len(again) != len(list) || len(list) != 4 {
		t.Fatalf("jobs: %d then %d", len(list), len(again))
	}
	if b2, _, _ := backend.LoadKey(ctx, jobs.JobsKey); !bytes.Equal(b, b2) {
		t.Fatalf("the records changed: %s", b2)
	}
	if strings.Count(log.String(), "each knowledge source is read by its own Wiki sync job now") != 2 {
		t.Fatalf("let go twice: %s", log.String())
	}
}

// A Confluence space's scheduled runs read every page once a night: the
// first run after 01:00 (KN-28).
func TestNightlyFullReadKN28(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 11, 5, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		last, now time.Time
		want      bool
	}{
		{time.Time{}, day(10, 0), true},
		{day(1, 10).AddDate(0, 0, -1), day(0, 30), false},
		{day(1, 10).AddDate(0, 0, -1), day(1, 5), true},
		{day(1, 5), day(1, 20), false},
	} {
		if got := nightlyDue(c.last, c.now, time.UTC); got != c.want {
			t.Errorf("last %v now %v: %v", c.last, c.now, got)
		}
	}
}
