package knowledge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
)

// --- Mycroft tab (Phase 12, wave 5) ---

// The cause of the Wiki tab's "Loading…" that never ended (MY-02): with
// no approved page the list was null in JSON, which the page took for
// "not answered yet". It is an empty list now, with or without a store,
// and the tab's state says whether a wiki is kept.
func TestNoPagesIsAnEmptyList(t *testing.T) {
	s := New(Options{Dir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w := serve(t, s, &viewer, "GET", "pages", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pages":[]`) {
		t.Fatalf("no pages: %d %s", w.Code, w.Body)
	}
	w = serve(t, s, &viewer, "GET", "wiki", "", nil)
	var st WikiState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusOK {
		t.Fatalf("the wiki's state: %d %s", w.Code, w.Body)
	}
	if st.Store != "" || st.Model || st.MayAdd || st.MaySetup || st.MayReview || !strings.Contains(w.Body.String(), `"added":[]`) {
		t.Fatalf("a viewer, no store, no model: %+v", st)
	}
	if !slices.ContainsFunc(st.Sources, func(b SourceBrief) bool { return b.ID == AddedSourceID && b.Kind == SourceUpload }) {
		t.Fatalf("the source of additions is listed: %+v", st.Sources)
	}
	// With no store an addition says what to do.
	w = serve(t, s, &admin, "POST", "add", `{"kind":"text","title":"Backups","text":"Kept 30 days."}`, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Settings › Knowledge") {
		t.Fatalf("no store: %d %s", w.Code, w.Body)
	}
	if w := serve(t, s, &admin, "GET", "wiki", "", nil); !strings.Contains(w.Body.String(), `"maySetup":true`) || !strings.Contains(w.Body.String(), `"mayAdd":true`) {
		t.Fatalf("a SuperAdmin's state: %s", w.Body)
	}
	if w := serve(t, s, nil, "GET", "wiki", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", w.Code)
	}
}

// A note goes in as an item of the source of additions: scrubbed, read
// by a sync of that source at once, quoted by the agents, and written up
// by Mycroft as pages that wait for review (MY-04, MY-05).
func TestAddANote(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.Add(ctx, viewer, AddInput{Kind: AddText, Title: "x", Text: "y"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer adds: %v", err)
	}
	for _, in := range []AddInput{{Kind: AddText, Text: "y"}, {Kind: AddText, Title: "x"}, {Kind: AddText, Title: "x", Text: strings.Repeat("a", MaxNote+1)}, {Kind: "poem"}} {
		if _, err := f.s.Add(ctx, bob, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", in.Kind, err)
		}
	}
	res, err := f.s.Add(ctx, bob, AddInput{Kind: AddText, Title: "Backup policy: NAS / EU", Text: "Backups are kept 30 days on nas-1.\n\npassword=hunter2-very-secret"})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != "note Backup policy- NAS - EU.md" || !res.Model || !strings.Contains(res.Text, "Drafts") {
		t.Fatalf("added: %+v %v", res, err)
	}
	f.s.Wait()
	hits := f.s.Search("backups kept nas", "excerpt", 5)
	if len(hits) == 0 || hits[0].Title != "Backup policy: NAS / EU" {
		t.Fatalf("the excerpt is quoted: %+v", hits)
	}
	for _, x := range append(f.model.calls(), hits[0].Text) {
		if strings.Contains(x, "hunter2-very-secret") {
			t.Fatal("a password reached the model or the index")
		}
	}
	runs, _ := f.s.Runs(ctx, false)
	if len(runs) != 1 || len(runs[0].Pages) != 1 || runs[0].Pages[0].State != StateWaiting {
		t.Fatalf("Mycroft's page waits for review: %+v", runs)
	}
	st, err := f.s.Wiki(ctx, bob)
	if err != nil || st.Store != "memory" || !st.MayAdd || st.MaySetup || len(st.Added) != 1 || st.Added[0].Title != "Backup policy: NAS / EU" || st.Added[0].LastRead.IsZero() {
		t.Fatalf("the state lists it: %+v %v", st, err)
	}
	if !slices.Contains(f.actions(), ActionAdd) {
		t.Fatalf("audited: %v", f.actions())
	}
	// Removed, it leaves the index.
	if err := f.s.RemoveAdded(ctx, viewer, res.Items[0].ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer removes: %v", err)
	}
	if err := f.s.RemoveAdded(ctx, bob, res.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if hits := f.s.Search("backups kept nas", "excerpt", 5); len(hits) != 0 {
		t.Fatalf("still quoted: %+v", hits)
	}
}

// Documents of every kind the form offers are read; CSV by its rows
// (MY-05).
func TestAddDocuments(t *testing.T) {
	f := newFixture(t)
	csv := "\xef\xbb\xbfhost,address,role\r\nweb-1,10.0.0.1,web\r\n\"db-1\",10.0.0.9,\"database, primary\"\r\n"
	for _, d := range []struct {
		name string
		data []byte
		want string
	}{
		{"hosts.csv", []byte(csv), "db-1 | 10.0.0.9 | database, primary"},
		{"plan.docx", docx(t, "Backup policy", "Keep backups for 30 days."), "Keep backups for 30 days."},
		{"runbook.pdf", pdf(t), "Restart the database first"},
		{"notes.md", []byte("# Notes\n\nPayments go through stripe-proxy."), "Payments go through stripe-proxy."},
		{"page.html", []byte("<html><head><title>T</title><script>var x = '<b>';</script></head><body><h1>Shop</h1><p>Checkout uses <b>redis-7</b>.</p></body></html>"), "Checkout uses redis-7"},
		{"readme.txt", []byte("Plain words."), "Plain words."},
	} {
		res, err := f.s.Add(ctx, bob, AddInput{Kind: AddDocument, Name: d.name, Data: d.data})
		if err != nil || len(res.Items) != 1 || res.Items[0].ID != d.name {
			t.Fatalf("%s: %+v %v", d.name, res, err)
		}
		f.s.Wait()
		f.s.pendingMu.Lock()
		n := len(f.s.pending)
		f.s.pendingMu.Unlock()
		if n != 0 {
			t.Fatalf("%s: still held after its read", d.name)
		}
		ex := f.s.Search(d.want, "excerpt", 5)
		if len(ex) == 0 || !strings.Contains(ex[0].Text, d.want) {
			t.Fatalf("%s: %+v", d.name, ex)
		}
	}
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddDocument, Name: "photo.png", Data: []byte("x")}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "CSV") {
		t.Fatalf("an image: %v", err)
	}
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddDocument, Name: "big.txt", Data: make([]byte, MaxDocument+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too large: %v", err)
	}
	// Through the API, the bytes as base64.
	body := fmt.Sprintf(`{"kind":"document","name":"more.csv","data":%q}`, base64.StdEncoding.EncodeToString([]byte("a,b\n1,2\n")))
	if w := serve(t, f.s, &bob, "POST", "add", body, nil); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"id":"more.csv"`) {
		t.Fatalf("the API: %d %s", w.Code, w.Body)
	}
	f.s.Wait()
}

// site is a small web site for the crawl: pages that link to each other
// and elsewhere, a robots.txt, and what it was asked.
type site struct {
	*httptest.Server
	mu     sync.Mutex
	asked  []string
	robots string
	status int
}

func newSite(t *testing.T) *site {
	t.Helper()
	x := &site{robots: "User-agent: *\nDisallow: /private\n\nUser-agent: OtherBot\nDisallow: /\n"}
	pages := map[string]string{
		"/docs/":             `<html><head><title>Docs home</title></head><body><nav><a href="/docs/nav-only">nav</a></nav><h1>Docs</h1><p>The shop runs on db-1.</p><a href="runbook.html">Runbook</a> <a href="/private/keys.html">keys</a> <a href="https://elsewhere.example.com/x">out</a> <a href="/docs/ignored" rel="nofollow">x</a> <a href="#top">top</a></body></html>`,
		"/docs/runbook.html": `<html><head><title>Runbook &amp; more</title></head><body><p>Restart the web pods after db-1. token=abcdef0123456789abcdef0123</p><a href="/docs/deep.html">deeper</a><a href="/docs/notes.txt">notes</a><a href="/docs/photo.png">photo</a></body></html>`,
		"/docs/deep.html":    `<html><head><meta name="robots" content="noindex"><title>Deep</title></head><body><p>Deep words.</p><a href="/docs/deeper.html">deeper</a></body></html>`,
		"/docs/deeper.html":  `<html><body><p>Deeper words.</p></body></html>`,
		"/docs/notes.txt":    "Plain notes about db-1.",
		"/private/keys.html": `<html><body>secret</body></html>`,
	}
	x.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x.mu.Lock()
		x.asked = append(x.asked, r.URL.Path)
		robots, status := x.robots, x.status
		x.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("User-Agent"), CrawlAgent) {
			http.Error(w, "who are you", http.StatusForbidden)
			return
		}
		switch p := r.URL.Path; {
		case p == "/robots.txt":
			if status != 0 {
				http.Error(w, "down", status)
				return
			}
			if robots == "" {
				http.NotFound(w, r)
				return
			}
			io.WriteString(w, robots)
		case p == "/docs":
			http.Redirect(w, r, "/docs/", http.StatusFound)
		case p == "/away":
			http.Redirect(w, r, "https://elsewhere.example.com/", http.StatusFound)
		case p == "/docs/photo.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG"))
		case strings.HasSuffix(p, ".txt"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, pages[p])
		default:
			body, ok := pages[p]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, body)
		}
	}))
	t.Cleanup(x.Close)
	return x
}

func (x *site) wasAsked(p string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return slices.Contains(x.asked, p)
}

func crawlFixture(t *testing.T) (*fixture, *site) {
	t.Helper()
	f := newFixture(t)
	x := newSite(t)
	f.s.o.CrawlHTTP = x.Client()
	return f, x
}

// A web address is read with the pages of its site it links to, as deep
// and as many as asked, robots.txt and the pages' robots tags respected,
// never another site (MY-06).
func TestAddAWebAddress(t *testing.T) {
	f, x := crawlFixture(t)
	two := 2
	res, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/docs", Depth: &two})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range res.Items {
		titles = append(titles, it.Title)
		if !strings.HasPrefix(it.URL, x.URL+"/docs/") || !strings.HasPrefix(it.ID, "web ") {
			t.Fatalf("an item: %+v", it)
		}
	}
	if !slices.Equal(titles, []string{"Docs home", "Runbook & more", strings.TrimPrefix(x.URL, "http://") + "/docs/notes.txt"}) {
		t.Fatalf("pages read: %q (skipped %+v)", titles, res.Skipped)
	}
	why := map[string]string{}
	for _, s := range res.Skipped {
		why[strings.TrimPrefix(s.URL, x.URL)] = s.Why
	}
	if !strings.Contains(why["/private/keys.html"], "robots.txt") || !strings.Contains(why["/docs/deep.html"], "not to be indexed") || !strings.Contains(why["/docs/photo.png"], "image/png") {
		t.Fatalf("skipped: %+v", why)
	}
	for _, p := range []string{"/private/keys.html", "/docs/ignored", "/docs/deeper.html"} {
		if x.wasAsked(p) {
			t.Fatalf("%s was read", p)
		}
	}
	f.s.Wait()
	hits := f.s.Search("restart web pods", "excerpt", 5)
	if len(hits) == 0 || hits[0].URL != x.URL+"/docs/runbook.html" || strings.Contains(hits[0].Text, "abcdef0123456789abcdef0123") {
		t.Fatalf("the excerpt: %+v", hits)
	}
	// Depth 0 reads the address alone; a page limit stops it.
	zero := 0
	if res, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/docs/", Depth: &zero}); err != nil || len(res.Items) != 1 || res.More {
		t.Fatalf("depth 0: %+v %v", res, err)
	}
	if res, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/docs/", Pages: 1}); err != nil || len(res.Items) != 1 || !res.More || !strings.Contains(res.Text, "page limit") {
		t.Fatalf("one page: %+v %v", res, err)
	}
	f.s.Wait()
}

func TestCrawlRefusals(t *testing.T) {
	f, x := crawlFixture(t)
	three, four := 3, 4
	for _, in := range []AddInput{
		{Kind: AddURL, URL: "ftp://example.com/"},
		{Kind: AddURL, URL: "https://ann:pw@example.com/"},
		{Kind: AddURL, URL: x.URL, Depth: &four},
		{Kind: AddURL, URL: x.URL, Pages: MaxCrawlPages + 1},
		{Kind: AddURL, URL: x.URL + "/away", Depth: &three},
		{Kind: AddURL, URL: x.URL + "/nothing-here"},
	} {
		if _, err := f.s.Add(ctx, bob, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", in.URL, err)
		}
	}
	// robots.txt that cannot be read stops the crawl; one that is
	// missing allows everything; one that shuts out everyone is obeyed.
	x.mu.Lock()
	x.status = http.StatusServiceUnavailable
	x.mu.Unlock()
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/docs/"}); err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Fatalf("robots.txt down: %v", err)
	}
	x.mu.Lock()
	x.status, x.robots = 0, ""
	x.mu.Unlock()
	if res, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/private/keys.html"}); err != nil || len(res.Items) != 1 {
		t.Fatalf("no robots.txt: %+v %v", res, err)
	}
	x.mu.Lock()
	x.robots = "User-agent: 221b-mycroft\nDisallow: /\n\nUser-agent: *\nAllow: /\n"
	x.mu.Unlock()
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/docs/"}); err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Fatalf("shut out by name: %v", err)
	}
	f.s.Wait()
	// Without a client of its own (production), the crawl refuses the
	// pod's loopback and the cloud's metadata address.
	f.s.o.CrawlHTTP = nil
	for _, u := range []string{x.URL + "/docs/", "http://169.254.169.254/latest/meta-data/"} {
		if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: u}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "loopback, link-local") {
			t.Fatalf("%s: %v", u, err)
		}
	}
}

func TestRobotsRules(t *testing.T) {
	r := parseRobots("# comment\nUser-agent: *\nUser-agent: other\nDisallow: /a\nAllow: /a/open\nDisallow: /*.pdf$\nDisallow:\n\nUser-agent: googlebot\nDisallow: /\n", CrawlAgent)
	for p, want := range map[string]bool{"/": true, "/a": false, "/a/b": false, "/a/open/x": true, "/doc.pdf": false, "/doc.pdf?x=1": true, "/b": true} {
		if got := r.allowed(p); got != want {
			t.Fatalf("%s: %v", p, got)
		}
	}
	if !parseRobots("", CrawlAgent).allowed("/x") {
		t.Fatal("empty allows")
	}
}

// Who may add, and the audit record names what, never the text.
func TestAddAudit(t *testing.T) {
	f := newFixture(t)
	var got []audit.Event
	f.s.o.Audit = func(e audit.Event) { got = append(got, e) }
	if _, err := f.s.Add(ctx, admin, AddInput{Kind: AddText, Title: "Shop", Text: "Shop secrets go here: password=abc123abc123"}); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"knowledge.add"`) || strings.Contains(string(b), "abc123abc123") || strings.Contains(string(b), "Shop secrets") {
		t.Fatalf("audit: %s", b)
	}
	// A source switched off takes nothing.
	if err := f.s.SetSourceOn(ctx, admin, AddedSourceID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Add(ctx, admin, AddInput{Kind: AddText, Title: "x", Text: "y"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("switched off: %v", err)
	}
}
