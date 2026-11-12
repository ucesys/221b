package knowledge

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// --- A wiki page's own actions (KN-23, KN-24) ---

const backupsPath = "pages/system/backups.md"

// approvedPage puts a page in the store, as approved, and reads the
// wiki again.
func approvedPage(t *testing.T, f *fixture, path, title, body string) {
	t.Helper()
	f.store.Put(path, Page{Path: path, Title: title, Kind: "system", Updated: f.clock.now(), Body: body}.Format())
	if err := f.s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
}

var mustLine = regexp.MustCompile(`(?m)^- (pages/[^:]+): corrected by (.+)$`)

// redraft answers as Mycroft rewriting the pages it must change, naming
// no source: the console adds the correction itself.
func redraft(text string) string {
	var pages []string
	for _, m := range mustLine.FindAllStringSubmatch(text, -1) {
		pages = append(pages, fmt.Sprintf(`{"path":%q,"title":"Backups","sources":[],"body":"Backups are kept 90 days on nas-2 (source: the correction)."}`, m[1]))
	}
	return `{"pages":[` + strings.Join(pages, ",") + `]}`
}

func (f *fixture) auditDetails() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, e := range f.audits {
		b.WriteString(e.Action + " " + e.Detail + "\n")
	}
	return b.String()
}

// This is outdated… becomes a note of "Added in Mycroft" tied to the page
// (its path and who wrote it), scrubbed; the sync started at once gives
// Mycroft that page to rewrite, and the redraft names the correction as
// a source and waits for review (KN-23).
func TestOutdatedCorrection(t *testing.T) {
	f := newFixture(t)
	f.model.reply = redraft
	approvedPage(t, f, backupsPath, "Backups", "Backups are kept 30 days on nas-1.")
	if _, err := f.s.Outdated(ctx, viewer, backupsPath, "x"); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer corrects: %v", err)
	}
	if _, err := f.s.Outdated(ctx, bob, backupsPath, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no text: %v", err)
	}
	if _, err := f.s.Outdated(ctx, bob, backupsPath, strings.Repeat("a", MaxNote+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too long: %v", err)
	}
	if _, err := f.s.Outdated(ctx, bob, "pages/system/none.md", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such page: %v", err)
	}
	res, err := f.s.Outdated(ctx, bob, backupsPath, "Kept 90 days now, on nas-2.\n\npassword=hunter2-very-secret")
	if err != nil || !res.Model || res.Correction.Path != backupsPath || res.Correction.Name != "Bob" || !strings.Contains(res.Text, "waits for review") {
		t.Fatalf("sent: %+v %v", res, err)
	}
	f.s.Wait()
	// The note is tied to the page and to who wrote it.
	var item *ItemState
	for _, it := range f.s.AddedItems() {
		if it.ID == res.Correction.ID {
			item = &it
		}
	}
	if item == nil || item.Corrects != backupsPath || item.By != "Bob" || item.Title != "Correction to Backups" || item.LastRead.IsZero() {
		t.Fatalf("the note: %+v", item)
	}
	// Mycroft was told to rewrite that page, the correction marked as such.
	calls := f.model.calls()
	last := calls[len(calls)-1]
	if !strings.Contains(last, "Pages you must change") || !strings.Contains(last, "- "+backupsPath+": corrected by added:") || !strings.Contains(last, `kind="correction"`) || !strings.Contains(last, `<page path="`+backupsPath+`"`) {
		t.Fatalf("the batch: %s", last)
	}
	for _, x := range calls {
		if strings.Contains(x, "hunter2-very-secret") {
			t.Fatal("a password reached the model")
		}
	}
	runs, _ := f.s.Runs(ctx, false)
	if len(runs) != 1 || len(runs[0].Pages) != 1 || runs[0].Pages[0].Path != backupsPath || runs[0].Pages[0].State != StateWaiting {
		t.Fatalf("the redraft waits for review: %+v", runs)
	}
	key := itemKey(AddedSourceID, res.Correction.ID)
	if !slices.ContainsFunc(runs[0].Pages[0].Sources, func(r SourceRef) bool { return r.Key == key && !r.Date.IsZero() }) {
		t.Fatalf("the page names the correction with its date: %+v", runs[0].Pages[0].Sources)
	}
	// The page says a correction waits, until the redraft is decided.
	c, err := f.s.PendingCorrection(ctx, backupsPath)
	if err != nil || c == nil || c.State != CorrectionDrafted || c.Run != runs[0].ID || c.Name != "Bob" {
		t.Fatalf("pending: %+v %v", c, err)
	}
	w := serve(t, f.s, &admin, "GET", "page?path="+backupsPath, "", nil)
	if !strings.Contains(w.Body.String(), `"correction":{`) {
		t.Fatalf("the page's view: %s", w.Body)
	}
	if _, err := f.s.Approve(ctx, admin, runs[0].ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.s.PendingCorrection(ctx, backupsPath); c != nil {
		t.Fatalf("still pending after the approval: %+v", c)
	}
	if p, _ := f.s.PageAt(backupsPath); !strings.Contains(p.Body, "90 days") {
		t.Fatalf("the page after review: %+v", p)
	}
	// Audited, never with its text.
	if !slices.Contains(f.actions(), ActionOutdated) {
		t.Fatalf("audited: %v", f.actions())
	}
	if d := f.auditDetails(); strings.Contains(d, "90 days") || strings.Contains(d, "hunter2") {
		t.Fatalf("the text reached the audit log: %s", d)
	}
	// Over HTTP: a viewer is refused, an Admin's is accepted.
	f.clock.add(time.Minute)
	if w := serve(t, f.s, &viewer, "POST", "outdated", `{"path":"`+backupsPath+`","text":"x"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer over HTTP: %d", w.Code)
	}
	if w := serve(t, f.s, &bob, "POST", "outdated", `{"path":"`+backupsPath+`","text":"Kept 120 days."}`, nil); w.Code != http.StatusAccepted {
		t.Fatalf("over HTTP: %d %s", w.Code, w.Body)
	}
	f.s.Wait()
}

// Without a model the correction is kept as a source, the answer says no
// page is written, and nothing waits (KN-23, as MY-08).
func TestOutdatedWithoutAModel(t *testing.T) {
	f := newFixture(t)
	f.s = New(Options{Store: f.store, Dir: t.TempDir(), Now: f.clock.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Audit: func(e audit.Event) { f.mu.Lock(); f.audits = append(f.audits, e); f.mu.Unlock() }})
	approvedPage(t, f, backupsPath, "Backups", "Backups are kept 30 days.")
	res, err := f.s.Outdated(ctx, bob, backupsPath, "Kept 90 days now.")
	if err != nil || res.Model || !strings.Contains(res.Text, "no page is written") {
		t.Fatalf("sent: %+v %v", res, err)
	}
	f.s.Wait()
	if hits := f.s.Search("kept 90 days", "excerpt", 5); len(hits) == 0 {
		t.Fatal("the correction is not quoted")
	}
	if c, _ := f.s.PendingCorrection(ctx, backupsPath); c != nil {
		t.Fatalf("waits without a model: %+v", c)
	}
}

// Retire page: asked with a reason, approved by one other reviewer (the
// one who asked is refused, 403), the page leaves the index on this
// replica and on another reading the same store, its file stays, a sync
// holds Mycroft's change to it, and Make active again brings it back
// the same way. Every step is audited (KN-24).
func TestRetirePage(t *testing.T) {
	f := newFixture(t)
	approvedPage(t, f, backupsPath, "Backups", "Backups are kept 30 days on zephyr-nas.")
	if _, err := f.s.AskPage(ctx, viewer, RequestRetire, backupsPath, "Gone."); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer asks: %v", err)
	}
	if _, err := f.s.AskPage(ctx, bob, RequestRetire, backupsPath, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := f.s.AskPage(ctx, bob, RequestActivate, backupsPath, "Why not."); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an active page made active: %v", err)
	}
	req, err := f.s.AskPage(ctx, bob, RequestRetire, backupsPath, "The NAS was replaced.")
	if err != nil || req.State != RequestWaiting || req.Title != "Backups" {
		t.Fatalf("asked: %+v %v", req, err)
	}
	if _, err := f.s.AskPage(ctx, admin, RequestRetire, backupsPath, "Again."); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second request: %v", err)
	}
	// The one who asked does not approve it: 403.
	if _, err := f.s.DecidePage(ctx, bob, req.ID, true, ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("their own approval: %v", err)
	}
	if w := serve(t, f.s, &bob, "POST", "requests/"+req.ID+"/approve", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("their own approval over HTTP: %d %s", w.Code, w.Body)
	}
	if n, _ := f.s.Waiting(ctx, bob); n != 0 {
		t.Fatalf("their own request counted for them: %d", n)
	}
	if n, _ := f.s.Waiting(ctx, admin); n != 1 {
		t.Fatalf("another reviewer's count: %d", n)
	}
	w := serve(t, f.s, &admin, "GET", "requests", "", nil)
	if !strings.Contains(w.Body.String(), `"canApprove":true`) {
		t.Fatalf("the list: %s", w.Body)
	}
	got, err := f.s.DecidePage(ctx, admin, req.ID, true, "")
	if err != nil || got.State != RequestApproved || got.DecidedName != "Ann" {
		t.Fatalf("approved: %+v %v", got, err)
	}
	if _, err := f.s.DecidePage(ctx, admin, req.ID, true, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("decided twice: %v", err)
	}
	// Retired: out of the index, shown as retired, the file kept.
	if slices.ContainsFunc(f.s.Pages(), func(p Page) bool { return p.Path == backupsPath }) {
		t.Fatal("a retired page is still listed")
	}
	if hits := f.s.Search("zephyr nas backups", "", 5); len(hits) != 0 {
		t.Fatalf("a retired page is found: %+v", hits)
	}
	retired := f.s.RetiredPages()
	if len(retired) != 1 || retired[0].Retired == nil || retired[0].Retired.Reason != "The NAS was replaced." || retired[0].Retired.AskedBy != "Bob" || retired[0].Retired.ApprovedBy != "Ann" || retired[0].Retired.At.IsZero() {
		t.Fatalf("the retired pages: %+v", retired)
	}
	if p, ok := f.s.PageAt(backupsPath); !ok || p.Retired == nil || p.Body == "" {
		t.Fatalf("a retired page is still read by people: %+v", p)
	}
	files, _ := f.store.Pull(ctx)
	if _, ok := files[backupsPath]; !ok {
		t.Fatal("the retired page's file was deleted")
	}
	// Another replica reading the same store leaves it out too (KN-07).
	other := New(Options{Store: f.store, Dir: t.TempDir(), Now: f.clock.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := other.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if hits := other.Search("zephyr nas backups", "", 5); len(hits) != 0 || len(other.RetiredPages()) != 1 {
		t.Fatalf("another replica: %+v", hits)
	}
	// A sync does not rewrite it: Mycroft's change is held in its run.
	f.model.reply = func(string) string {
		return `{"pages":[{"path":"` + backupsPath + `","title":"Backups","body":"Rewritten."}]}`
	}
	if _, err := f.s.Add(ctx, admin, AddInput{Kind: AddText, Title: "Zephyr", Text: "The zephyr NAS keeps the backups."}); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	runs, _ := f.s.Runs(ctx, true)
	if len(runs) != 1 || runs[0].State != StateDone || runs[0].Pages[0].State != StateHeld || !strings.Contains(runs[0].Pages[0].Reason, "retired") {
		t.Fatalf("the change to a retired page: %+v", runs)
	}
	if !strings.Contains(f.model.calls()[len(f.model.calls())-1], "Pages people retired") {
		t.Fatal("Mycroft is not told the page is retired")
	}
	files, _ = f.store.Pull(ctx)
	if strings.Contains(string(files[backupsPath].Body), "Rewritten") {
		t.Fatal("a retired page was rewritten")
	}
	if p, _ := f.s.PageAt(backupsPath); p.Retired == nil {
		t.Fatal("the sync's manifest lost the retirement")
	}
	// Make active again: asked and approved the same way.
	if _, err := f.s.Outdated(ctx, bob, backupsPath, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a retired page corrected: %v", err)
	}
	back, err := f.s.AskPage(ctx, admin, RequestActivate, backupsPath, "The NAS is back.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DecidePage(ctx, admin, back.ID, true, ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("their own approval: %v", err)
	}
	if _, err := f.s.DecidePage(ctx, bob, back.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(f.s.Pages(), func(p Page) bool { return p.Path == backupsPath }) || len(f.s.RetiredPages()) != 0 {
		t.Fatal("not active again")
	}
	if hits := f.s.Search("zephyr nas backups", "page", 5); len(hits) != 1 {
		t.Fatalf("not in the index again: %+v", hits)
	}
	// Reject discards a request; the one who asked may withdraw theirs.
	again, _ := f.s.AskPage(ctx, bob, RequestRetire, backupsPath, "Maybe.")
	if got, err := f.s.DecidePage(ctx, bob, again.ID, false, "Not yet."); err != nil || got.State != RequestRejected {
		t.Fatalf("rejected: %+v %v", got, err)
	}
	if len(f.s.RetiredPages()) != 0 {
		t.Fatal("a rejected request retired the page")
	}
	for _, a := range []string{ActionRetireRequest, ActionRetire, ActionActivate, ActionRetireReject} {
		if !slices.Contains(f.actions(), a) {
			t.Fatalf("%s not audited: %v", a, f.actions())
		}
	}
}

// A redraft waiting when its page is retired is held at its approval,
// not published (KN-24).
func TestARunWaitingForARetiredPageIsHeld(t *testing.T) {
	f := newFixture(t)
	f.model.reply = redraft
	approvedPage(t, f, backupsPath, "Backups", "Backups are kept 30 days.")
	if _, err := f.s.Outdated(ctx, bob, backupsPath, "Kept 90 days now."); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	runs, _ := f.s.Runs(ctx, false)
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	req, _ := f.s.AskPage(ctx, bob, RequestRetire, backupsPath, "Replaced.")
	if _, err := f.s.DecidePage(ctx, admin, req.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.clock.add(time.Minute)
	run, err := f.s.Approve(ctx, admin, runs[0].ID)
	if err != nil || run.State != StateDone || run.Pages[0].State != StateHeld {
		t.Fatalf("approved: %+v %v", run, err)
	}
	if p, _ := f.s.PageAt(backupsPath); strings.Contains(p.Body, "90 days") || p.Retired == nil {
		t.Fatalf("the retired page was published: %+v", p)
	}
}
