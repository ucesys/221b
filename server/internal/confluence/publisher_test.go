package confluence_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/confluence/confluencetest"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
)

type memSecret struct {
	mu      sync.Mutex
	data    map[string][]byte
	version int
}

func (m *memSecret) LoadKey(_ context.Context, key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	return b, fmt.Sprint(m.version), ok, nil
}

func (m *memSecret) SaveKeyIf(_ context.Context, key string, b []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = b
	m.version++
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type wiki []confluence.WikiPage

func (w wiki) WikiPages(context.Context) ([]confluence.WikiPage, error) { return w, nil }

type fixture struct {
	f      *confluencetest.Fake
	issues *issues.Service
	p      *confluence.Publisher
	clock  *clock
	kv     *kv.Memory
}

var olga = issues.Caller{Person: issues.Person{ID: "olga", Name: "Olga"}, Operator: true}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}
	f := confluencetest.NewTLS("ann@example.com", "tok-123456")
	t.Cleanup(f.Close)
	f.Now = c.now
	f.AddSpace("STATUS")
	f.AddSpace("INFRA")
	svc := issues.New(issues.Options{Now: c.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	mem := &kv.Memory{}
	p := confluence.NewPublisher(confluence.PublisherOptions{Backend: mem, Limit: 256 << 10, Secrets: &memSecret{}, Issues: confluence.IssueSource(svc),
		Hidden:     func(context.Context) []string { return []string{"worker-7.internal"} },
		ConsoleURL: "https://221b.example.com", HTTP: f.Client(), Now: c.now, Sleep: noSleep})
	svc.OnChange(p.OnIssue)
	tok := "tok-123456"
	if _, err := p.Save(ctx, confluence.Settings{On: true, BaseURL: f.URL, Flavor: confluence.DataCenter, Space: "STATUS", Wiki: true, WikiSpace: "INFRA"}, &tok, "ann"); err != nil {
		t.Fatal(err)
	}
	return &fixture{f: f, issues: svc, p: p, clock: c, kv: mem}
}

func (x *fixture) flush(t *testing.T) {
	t.Helper()
	for i := 0; i < 5; i++ {
		n, err := x.p.Flush(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func (x *fixture) known(t *testing.T) issues.KnownIssue {
	t.Helper()
	if _, err := x.issues.SaveService(ctx, olga, "", issues.ServiceDef{Name: "Shop", RunsOn: []issues.RunsOn{{Kind: "namespace", Value: "shop-prod", Cluster: "prod"}}}); err != nil {
		t.Fatal(err)
	}
	ptr := func(s string) *string { return &s }
	st := issues.KnownFixInProgress
	k, err := x.issues.SaveKnown(ctx, olga, "", issues.KnownInput{Title: ptr("Checkout times out"), Symptoms: []string{"checkout hangs after Pay"}, Services: []string{"shop"}, Status: &st,
		Workaround: ptr("Try again in a few minutes; pods in shop-prod restart, see worker-7.internal at 10.1.2.3."), Expected: ptr("today, 15:00"), Owner: &issues.Person{ID: "olga", Name: "Olga"}})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (x *fixture) page(t *testing.T, title string) confluencetest.Page {
	t.Helper()
	p, ok := x.f.Find("STATUS", title)
	if !ok {
		t.Fatalf("no page %q among %v", title, x.f.Pages("STATUS"))
	}
	return p
}

// A known issue's change writes its page and the Status page, with
// nothing that must stay off Confluence: no namespace, hostname or
// address.
func TestAKnownIssuePublishesItsPageAndTheStatus(t *testing.T) {
	x := newFixture(t)
	k := x.known(t)
	x.flush(t)
	pg := x.page(t, k.ID+": Checkout times out")
	for _, want := range []string{"Fix in progress", "checkout hangs after Pay", "Shop", "today, 15:00", "Olga", "How to report more", "https://221b.example.com/#/cases/known/" + k.ID, "<h2>Notes</h2>"} {
		if !strings.Contains(pg.Body, want) {
			t.Errorf("%q is not on the page:\n%s", want, pg.Body)
		}
	}
	for _, never := range []string{"shop-prod", "worker-7.internal", "10.1.2.3"} {
		if strings.Contains(pg.Body, never) {
			t.Errorf("%q reached Confluence:\n%s", never, pg.Body)
		}
	}
	status := x.page(t, "Known issues: status")
	if !strings.Contains(status.Body, "<h3>Shop</h3>") || !strings.Contains(status.Body, k.ID+": Checkout times out") {
		t.Errorf("the Status page groups the open issue by its service:\n%s", status.Body)
	}
	root := x.page(t, "Known issues")
	if pg.ParentID != root.ID || status.ParentID != root.ID {
		t.Error("the pages are under the tree's top page")
	}
	// A draft, proposed by an agent, is never published.
	title := "Agent's guess"
	if _, err := x.issues.Propose(ctx, issues.SourceAgent, issues.KnownInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	for _, p := range x.f.Pages("STATUS") {
		if strings.Contains(p.Title, "Agent's guess") || strings.Contains(p.Body, "Agent's guess") {
			t.Fatalf("a draft reached Confluence: %s", p.Title)
		}
	}
}

// A known issue made inactive as a duplicate is left off the Status page,
// which reporters read (CF-63).
func TestAnInactiveKnownIssueIsLeftOffTheStatusCF63(t *testing.T) {
	x := newFixture(t)
	k := x.known(t)
	title, yes := "Checkout hangs", true
	dup, err := x.issues.SaveKnown(ctx, olga, "", issues.KnownInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.issues.SaveKnown(ctx, olga, dup.ID, issues.KnownInput{Inactive: &yes}); err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	status := x.page(t, "Known issues: status")
	if !strings.Contains(status.Body, k.ID+": Checkout times out") || strings.Contains(status.Body, dup.ID+":") {
		t.Fatalf("the Status page:\n%s", status.Body)
	}
}

// The Notes part is people's: an edit there stays when the page is
// rewritten. An edit inside the managed part is not overwritten: it waits
// as a conflict, and kept, it goes into the known issue's update log.
func TestAPersonsEditIsNeverOverwritten(t *testing.T) {
	x := newFixture(t)
	k := x.known(t)
	x.flush(t)
	pg := x.page(t, k.ID+": Checkout times out")
	x.f.Edit(pg.ID, strings.Replace(pg.Body, "<h2>Notes</h2><p></p>", "<h2>Notes</h2><p>Call Dana first.</p>", 1))
	if _, err := x.issues.AddUpdate(ctx, olga, k.ID, "The fix is deployed to half the pods.", ""); err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	pg = x.page(t, k.ID+": Checkout times out")
	if !strings.Contains(pg.Body, "Call Dana first.") || !strings.Contains(pg.Body, "The fix is deployed to half the pods.") {
		t.Fatalf("the notes stay and the managed part follows the record:\n%s", pg.Body)
	}
	// Now inside the managed part.
	edited := strings.Replace(pg.Body, "Try again in a few minutes", "Use the US store meanwhile", 1)
	x.f.Edit(pg.ID, edited)
	if _, err := x.issues.AddUpdate(ctx, olga, k.ID, "Nearly done.", ""); err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	pg = x.page(t, k.ID+": Checkout times out")
	if !strings.Contains(pg.Body, "Use the US store meanwhile") || strings.Contains(pg.Body, "Nearly done.") {
		t.Fatalf("the person's edit was overwritten:\n%s", pg.Body)
	}
	conflicts, _ := x.p.Conflicts(ctx)
	if len(conflicts) != 1 || conflicts[0].Issue != k.ID || conflicts[0].Owner != "olga" || !strings.Contains(conflicts[0].Their, "Use the US store meanwhile") {
		t.Fatalf("the owner is asked: %+v", conflicts)
	}
	if _, err := x.p.Resolve(ctx, conflicts[0].ID, "keep", confluence.Person{ID: "olga", Name: "Olga"}); err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	got, _ := x.issues.KnownIssue(ctx, olga, k.ID)
	if last := got.LastUpdate(); last == nil || !strings.Contains(last.Text, "Use the US store meanwhile") {
		t.Fatalf("kept, the edit is copied into the record: %+v", got.Updates)
	}
	pg = x.page(t, k.ID+": Checkout times out")
	if !strings.Contains(pg.Body, "Nearly done.") || !strings.Contains(pg.Body, "Call Dana first.") {
		t.Fatalf("after the decision the page follows the record again, notes kept:\n%s", pg.Body)
	}
	if c, _ := x.p.Conflicts(ctx); len(c) != 0 {
		t.Fatalf("the conflict is settled: %+v", c)
	}
}

// Confluence down: the outbox keeps the page and tries again later.
func TestTheOutboxRetriesWithBackoff(t *testing.T) {
	x := newFixture(t)
	down := true
	x.f.Fail = func(r *http.Request) int {
		if down && r.Method != http.MethodGet {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	k := x.known(t)
	if _, err := x.p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ := x.p.View(ctx)
	if v.Outbox == 0 || v.Failing == 0 || v.LastError == "" {
		t.Fatalf("the failing pages stay in the outbox: %+v", v)
	}
	down = false
	x.flush(t)
	if v, _ := x.p.View(ctx); v.Failing == 0 {
		t.Fatal("a failing page waits for its backoff")
	}
	x.clock.add(time.Hour)
	x.flush(t)
	x.page(t, k.ID+": Checkout times out")
	if v, _ := x.p.View(ctx); v.Outbox != 0 || v.LastError != "" {
		t.Fatalf("written after the backoff: %+v", v)
	}
}

// Pages are never deleted: a removed known issue's page is archived and
// says so; a resolved one moves to the month's archive after 30 days. A
// page someone wrote under the tree becomes a draft known issue.
func TestTheNightlyCheckArchivesAndImports(t *testing.T) {
	x := newFixture(t)
	k := x.known(t)
	ptr := func(s string) *string { return &s }
	gone, err := x.issues.SaveKnown(ctx, olga, "", issues.KnownInput{Title: ptr("Old printer issue")})
	if err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	root := x.page(t, "Known issues")
	x.f.AddPage("STATUS", root.ID, "VPN drops at night", "<p>The VPN drops every night at 02:00.</p>")
	if err := x.issues.DeleteKnown(ctx, olga, gone.ID); err != nil {
		t.Fatal(err)
	}
	st := issues.KnownResolved
	if _, err := x.issues.SaveKnown(ctx, olga, k.ID, issues.KnownInput{Status: &st}); err != nil {
		t.Fatal(err)
	}
	x.flush(t)
	x.clock.add(31 * 24 * time.Hour)
	res, err := x.p.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed := x.page(t, gone.ID+": Old printer issue")
	if !strings.Contains(removed.Body, "removed from the issue service") {
		t.Errorf("a removed issue's page stays and says so:\n%s", removed.Body)
	}
	month := x.page(t, "Known issues: resolved 2026-11")
	if moved := x.page(t, k.ID+": Checkout times out"); moved.ParentID != month.ID {
		t.Errorf("the resolved issue moves to its month's archive")
	}
	if removed.ParentID == root.ID {
		t.Errorf("the removed issue's page is archived")
	}
	all, _ := x.issues.Known(ctx, olga, true)
	var imported *issues.KnownIssue
	for i := range all {
		if all[i].Title == "VPN drops at night" {
			imported = &all[i]
		}
	}
	if imported == nil || !imported.Draft || imported.Source != issues.SourceConfluence || imported.Link == "" {
		t.Fatalf("the hand-written page is imported as a draft: %+v (report %v)", imported, res.Report)
	}
	if _, err := x.p.Check(ctx); err != nil {
		t.Fatal(err)
	}
	all2, _ := x.issues.Known(ctx, olga, true)
	if len(all2) != len(all) {
		t.Fatal("a page is imported once")
	}
	if n := len(x.f.Pages("STATUS")); n < 6 {
		t.Fatalf("no page was deleted: %d", n)
	}
}

// Approved wiki pages are published read-only to their own space, with a
// link back to where they are edited.
func TestTheWikiIsPublishedReadOnly(t *testing.T) {
	x := newFixture(t)
	x.p.SetWiki(wiki{{Path: "pages/runbook/restart-shop.md", Title: "Runbook: restart shop", Markdown: "# Restart\n\n1. Drain\n2. Restart", Updated: x.clock.now(), Sources: []string{"Shop runbook, version 3, 2026-10-01"}, EditURL: "https://git.example.com/wiki/blob/main/pages/runbook/restart-shop.md"}})
	x.p.EnqueueWiki("pages/runbook/restart-shop.md")
	x.flush(t)
	pg, ok := x.f.Find("INFRA", "Runbook: restart shop")
	if !ok {
		t.Fatalf("no wiki page: %v", x.f.Pages("INFRA"))
	}
	for _, want := range []string{"published read-only", "https://git.example.com/wiki/blob/main/pages/runbook/restart-shop.md", "<ol><li>Drain</li>", "Shop runbook, version 3"} {
		if !strings.Contains(pg.Body, want) {
			t.Errorf("%q is missing:\n%s", want, pg.Body)
		}
	}
	saved, err := x.p.Export(ctx, (&backup.Env{}).Seal)
	if err != nil || saved == nil || saved.Token != "<secret: not in the backup>" || len(saved.Pages) == 0 {
		t.Fatalf("a backup holds the settings and pages, the token as a placeholder: %+v, %v", saved, err)
	}
	if _, err := x.p.Import(ctx, confluence.Saved{Settings: saved.Settings, Token: "tok-123456"}, (&backup.Env{}).Open); err == nil {
		t.Fatal("a restore refuses a token in the file")
	}
}
