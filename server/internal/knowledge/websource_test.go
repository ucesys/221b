package knowledge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// --- Web sites, exclusions, the items and the uploaded documents (0.12.1) ---

// webSite saves a web site source of the test site and reads it once.
func webSite(t *testing.T, f *fixture, x Source) SyncResult {
	t.Helper()
	if x.ID == "" {
		x.ID = "site"
	}
	if x.Name == "" {
		x.Name = "Docs site"
	}
	x.Kind = SourceWeb
	if _, err := f.s.SaveSource(ctx, admin, x, nil, true); err != nil {
		t.Fatal(err)
	}
	return syncSource(t, f, x.ID)
}

func syncSource(t *testing.T, f *fixture, id string) SyncResult {
	t.Helper()
	f.s.Wait()
	res, err := f.s.SyncWith(ctx, SyncRequest{Source: id, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (x *site) reset() {
	x.mu.Lock()
	x.asked = nil
	x.mu.Unlock()
}

// itemsOf lists every item of a source, as AIOps › Mycroft › Sources does.
func itemsOf(t *testing.T, f *fixture, q ItemsQuery) map[string]SourceItem {
	t.Helper()
	q.Limit = MaxItemsLimit
	p, err := f.s.Items(ctx, admin, q)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]SourceItem{}
	for _, it := range p.Items {
		out[it.ID] = it
	}
	return out
}

// A web site is read from its start address with the pages of the site
// it links to, as deep and as many as its settings say, robots.txt and
// the pages' robots tags respected, never another site (KN-44); the pages
// left out are listed with why (KN-47).
func TestAWebSiteIsReadAsASource(t *testing.T) {
	f, x := crawlFixture(t)
	res := webSite(t, f, Source{URL: x.URL + "/docs", Depth: 2, Pages: 20})
	if res.Read != 3 || len(res.Errors) != 0 {
		t.Fatalf("the read: %+v", res)
	}
	items := itemsOf(t, f, ItemsQuery{Source: "site"})
	for id, want := range map[string]string{"/docs/": StateRead, "/docs/runbook.html": StateRead, "/docs/notes.txt": StateRead, "/private/keys.html": StateSkipped, "/docs/deep.html": StateSkipped, "/docs/photo.png": StateSkipped} {
		if it, ok := items[id]; !ok || it.State != want || it.Type != TypeWebPage {
			t.Fatalf("%s: %+v (all %+v)", id, it, items)
		}
	}
	if !strings.Contains(items["/private/keys.html"].Why, "robots.txt") || !strings.Contains(items["/docs/deep.html"].Why, "not to be indexed") {
		t.Fatalf("why skipped: %+v", items)
	}
	for _, p := range []string{"/private/keys.html", "/docs/ignored", "/docs/deeper.html"} {
		if x.wasAsked(p) {
			t.Fatalf("%s was read", p)
		}
	}
	hits := f.s.Search("restart web pods", "excerpt", 5)
	if len(hits) == 0 || hits[0].URL != x.URL+"/docs/runbook.html" || strings.Contains(hits[0].Text, "abcdef0123456789abcdef0123") {
		t.Fatalf("the excerpt: %+v", hits)
	}
	// Unchanged, nothing is sent to Mycroft again.
	calls := len(f.model.calls())
	if res := syncSource(t, f, "site"); res.Read != 0 || res.Unchanged != 3 || len(f.model.calls()) != calls {
		t.Fatalf("read again: %+v", res)
	}
	// Its job reads it every night.
	set, _ := f.s.Settings(ctx)
	src, _ := set.source("site")
	if k := f.s.syncKind(src); !strings.Contains(k.Reads, x.URL+"/docs") || k.Default.Schedule == nil {
		t.Fatalf("its job: %+v", k)
	}
}

// What a web site's settings may not be, and what its read refuses.
func TestWebSiteRefusals(t *testing.T) {
	f, x := crawlFixture(t)
	for _, src := range []Source{
		{URL: "ftp://example.com/", Pages: 5},
		{URL: "https://ann:pw@example.com/", Pages: 5},
		{URL: x.URL, Depth: 4, Pages: 5},
		{URL: x.URL, Pages: MaxSitePages + 1},
		{URL: x.URL, Pages: 5, Auth: LiveAuthBearer},
		{URL: "https://example.com/", Pages: 5, Auth: LiveAuthBasic},
		{URL: "https://example.com/", Pages: 5, Auth: "kerberos"},
	} {
		src.ID, src.Name, src.Kind = "bad", "Bad", SourceWeb
		if _, err := f.s.SaveSource(ctx, admin, src, nil, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", src, err)
		}
	}
	if _, err := f.s.SaveSource(ctx, bob, Source{ID: "w", Name: "W", Kind: SourceWeb, URL: x.URL, Pages: 5}, nil, true); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin adds a source: %v", err)
	}
	// Adding a web address in Mycroft is a web site now.
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddURL, URL: x.URL + "/docs/"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Web site") {
		t.Fatalf("an address added: %v", err)
	}
	// robots.txt that cannot be read fails the read, which keeps what it
	// read before.
	webSite(t, f, Source{URL: x.URL + "/docs/", Depth: 1, Pages: 5})
	x.mu.Lock()
	x.status = http.StatusServiceUnavailable
	x.mu.Unlock()
	if res := syncSource(t, f, "site"); len(res.Errors) == 0 || !strings.Contains(res.Errors[0], "robots.txt") || res.Gone != 0 {
		t.Fatalf("robots.txt down: %+v", res)
	}
	if len(f.s.Search("restart web pods", "excerpt", 5)) == 0 {
		t.Fatal("a failed read lost the excerpts")
	}
	// One that shuts out the console's agent by name is obeyed.
	x.mu.Lock()
	x.status, x.robots = 0, "User-agent: 221b-mycroft\nDisallow: /\n\nUser-agent: *\nAllow: /\n"
	x.mu.Unlock()
	syncSource(t, f, "site")
	if it := itemsOf(t, f, ItemsQuery{Source: "site", State: StateSkipped})["/docs/"]; !strings.Contains(it.Why, "robots.txt") {
		t.Fatalf("shut out by name: %+v", it)
	}
	// Without a client of its own (production), the read refuses the pod's
	// loopback.
	f.s.o.CrawlHTTP = nil
	if res := syncSource(t, f, "site"); len(res.Errors) == 0 || !strings.Contains(res.Errors[0], "loopback, link-local") {
		t.Fatalf("loopback: %+v", res)
	}
}

// A web site that signs in sends its credential to the site only, and its
// Test lists what it would read without saving anything (KN-44).
func TestAWebSiteSignsIn(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer site-token-0123456789" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head><title>Private runbook</title></head><body><p>Rotate the certificates on edge-1 each spring.</p></body></html>"))
	}))
	t.Cleanup(srv.Close)
	// The site's certificate is trusted by the crawl and by the Test's
	// handshake.
	f.s.o.CrawlHTTP, f.s.o.HTTP = srv.Client(), srv.Client()
	src := Source{ID: "intra", Name: "Intranet", Kind: SourceWeb, URL: srv.URL + "/ops/", Pages: 5, Auth: LiveAuthBearer}
	bad := "wrong-token-0123456789"
	if r, err := f.s.TestSourceAsTyped(ctx, admin, src, CredsChange{Token: &bad}, ""); err == nil {
		t.Fatalf("a wrong token tests: %+v", r)
	} else if strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), "refused the credential") {
		t.Fatalf("the test's error: %v", err)
	}
	tok := "site-token-0123456789"
	r, err := f.s.TestSourceAsTyped(ctx, admin, src, CredsChange{Token: &tok}, "")
	if err != nil || r.Items != 1 || r.Sample[0] != "Private runbook" {
		t.Fatalf("the test: %+v %v", r, err)
	}
	if _, err := f.s.SaveSource(ctx, admin, src, &tok, true); err != nil {
		t.Fatal(err)
	}
	if res := syncSource(t, f, "intra"); res.Read != 1 {
		t.Fatalf("read: %+v", res)
	}
	view, _ := f.s.GetView(ctx, admin)
	b, _ := json.Marshal(view)
	if strings.Contains(string(b), tok) || !view.Creds["intra"].Token {
		t.Fatalf("the token is set and never shown: %s", b)
	}
}

// An excluded item's excerpts are emptied at once; the reads leave it out,
// and the crawl neither fetches an excluded page nor follows the links
// found only on it; Include again reads it at the next read (KN-45).
func TestExcludeAndIncludeAgain(t *testing.T) {
	f, x := crawlFixture(t)
	webSite(t, f, Source{URL: x.URL + "/docs/", Depth: 2, Pages: 20})
	if len(f.s.Search("restart web pods", "excerpt", 5)) == 0 {
		t.Fatal("not read")
	}
	// Only a SuperAdmin excludes; an uploaded document is removed instead.
	if _, err := f.s.Exclude(ctx, bob, "site", ExcludeInput{Item: "/docs/runbook.html"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin excludes: %v", err)
	}
	if _, err := f.s.Exclude(ctx, admin, "documents", ExcludeInput{Item: "x.md"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an upload excluded: %v", err)
	}
	res, err := f.s.Exclude(ctx, admin, "site", ExcludeInput{Item: "/docs/runbook.html", Reason: "outdated"})
	if err != nil || res.Items != 1 || res.Exclusion.Title != "Runbook & more" || res.Exclusion.By != "Ann" || len(res.Pages) != 1 {
		t.Fatalf("excluded: %+v %v", res, err)
	}
	if hits := f.s.Search("restart web pods", "excerpt", 5); len(hits) != 0 {
		t.Fatalf("still quoted: %+v", hits)
	}
	if _, err := f.s.Exclude(ctx, admin, "site", ExcludeInput{Item: "/docs/runbook.html"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("twice: %v", err)
	}
	it := itemsOf(t, f, ItemsQuery{Source: "site", State: StateExcluded})["/docs/runbook.html"]
	if it.Exclusion == nil || it.Exclusion.Reason != "outdated" || !strings.Contains(it.Why, "outdated") {
		t.Fatalf("listed excluded: %+v", it)
	}
	// The next read does not fetch it, nor notes.txt, found only on it.
	x.reset()
	calls := len(f.model.calls())
	r := syncSource(t, f, "site")
	if x.wasAsked("/docs/runbook.html") || x.wasAsked("/docs/notes.txt") || !x.wasAsked("/docs/") {
		t.Fatalf("asked: %v", x.asked)
	}
	if len(f.model.calls()) != calls || r.Read != 0 {
		t.Fatalf("read: %+v", r)
	}
	if hits := f.s.Search("restart web pods", "excerpt", 5); len(hits) != 0 {
		t.Fatalf("quoted after a read: %+v", hits)
	}
	// Include again: read at the next read.
	if err := f.s.Include(ctx, bob, "site", "/docs/runbook.html", false); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin includes: %v", err)
	}
	if err := f.s.Include(ctx, admin, "site", "/docs/nothing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not excluded: %v", err)
	}
	if err := f.s.Include(ctx, admin, "site", "/docs/runbook.html", false); err != nil {
		t.Fatal(err)
	}
	if len(f.s.Search("restart web pods", "excerpt", 5)) != 0 {
		t.Fatal("included again before a read")
	}
	syncSource(t, f, "site")
	if hits := f.s.Search("restart web pods", "excerpt", 5); len(hits) == 0 {
		t.Fatal("not read again")
	}
	if it := itemsOf(t, f, ItemsQuery{Source: "site"})["/docs/runbook.html"]; it.State != StateRead {
		t.Fatalf("read again: %+v", it)
	}
	// A pattern excludes everything under a place; a rule that covers no
	// item known is listed itself.
	if _, err := f.s.Exclude(ctx, admin, "site", ExcludeInput{Pattern: "/docs/*"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Exclude(ctx, admin, "site", ExcludeInput{Pattern: "/blog/*"}); err != nil {
		t.Fatal(err)
	}
	ex := itemsOf(t, f, ItemsQuery{Source: "site", State: StateExcluded})
	if ex["/docs/notes.txt"].State != StateExcluded || ex["/blog/*"].Exclusion == nil {
		t.Fatalf("patterns: %+v", ex)
	}
	if len(f.s.Search("db-1", "excerpt", 5)) != 0 {
		t.Fatal("the pattern's items are quoted")
	}
	// Edited in its dialog, the source keeps its exclusions.
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "site", Name: "Docs", Kind: SourceWeb, URL: x.URL + "/docs/", Depth: 2, Pages: 20}, nil, false); err != nil {
		t.Fatal(err)
	}
	set, _ := f.s.Settings(ctx)
	if src, _ := set.source("site"); len(src.Excluded) != 2 {
		t.Fatalf("kept: %+v", src.Excluded)
	}
	acts := f.actions()
	if !slices.Contains(acts, ActionExclude) || !slices.Contains(acts, ActionInclude) {
		t.Fatalf("audited: %v", acts)
	}
}

// What an exclusion covers, by the kind of its source (KN-45).
func TestExclusionPatterns(t *testing.T) {
	for _, c := range []struct {
		kind, pattern string
		under         bool
		id            string
		anc           []string
		want          bool
	}{
		{SourceWeb, "/blog/*", false, "/blog/2024/a.html", nil, true},
		{SourceWeb, "/blog/*", false, "/blogs/a.html", nil, false},
		{SourceWeb, "/docs/a.html", false, "/docs/a.html", nil, true},
		{SourceWeb, "/docs/a.html", false, "/docs/a.html?x=1", nil, false},
		{SourceWeb, "*.pdf", false, "/x/y.pdf", nil, true},
		{SourceGit, "docs/old/", false, "docs/old/a.md", nil, true},
		{SourceGit, "docs/old/", false, "docs/older.md", nil, false},
		{SourceGit, "docs/a.md", false, "docs/a.md", nil, true},
		{SourceS3, "archive/", false, "archive/2020/x.pdf", nil, true},
		{SourceS3, "archive/", false, "runbooks/archive/x.pdf", nil, false},
		{SourceConfluence, "100", true, "102", []string{"101", "100"}, true},
		{SourceConfluence, "100", true, "100", nil, true},
		{SourceConfluence, "100", false, "102", []string{"100"}, false},
		{SourceConfluence, "100", true, "103", []string{"99"}, false},
	} {
		if got := (Exclusion{Pattern: c.pattern, Under: c.under}).matches(c.kind, c.id, c.anc); got != c.want {
			t.Fatalf("%s %s %s: %v", c.kind, c.pattern, c.id, got)
		}
	}
	for _, c := range []struct{ kind, id, want string }{
		{SourceWeb, "/blog/2024/a.html", "/blog/2024/*"},
		{SourceWeb, "/docs/", "/docs/*"},
		{SourceWeb, "/a.html", ""},
		{SourceGit, "docs/old/a.md", "docs/old/"},
		{SourceGit, "a.md", ""},
		{SourceS3, "archive/x.pdf", "archive/"},
		{SourceConfluence, "100", "100"},
	} {
		if p, _, _ := UnderPattern(c.kind, c.id); p != c.want {
			t.Fatalf("%s %s: %q", c.kind, c.id, p)
		}
	}
	if p := parentsOf(newManifest(), "x", []Item{{ID: "3", Parent: "2"}, {ID: "2", Parent: "1"}}); !slices.Equal(ancestorsOf("3", p), []string{"2", "1"}) {
		t.Fatalf("ancestors: %v", ancestorsOf("3", p))
	}
}

// A Confluence page and its children are excluded together, and the
// reads leave them out (KN-45).
func TestExcludeAConfluencePageAndItsChildren(t *testing.T) {
	f := newFixture(t)
	home := f.conf.AddSpace("OPS")
	parent := f.conf.AddPage("OPS", home, "Old platform", "The old platform ran on vm-1.")
	f.conf.AddPage("OPS", parent, "Old platform backups", "Backups of vm-1 went to tape.")
	f.conf.AddPage("OPS", home, "New platform", "The new platform runs on k8s-1.")
	tok := "read-token-123456"
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "ops", Name: "OPS", Kind: SourceConfluence, BaseURL: f.conf.URL, Flavor: "datacenter", Space: "OPS"}, &tok, true); err != nil {
		t.Fatal(err)
	}
	syncSource(t, f, "ops")
	if len(f.s.Search("tape backups vm-1", "excerpt", 5)) == 0 {
		t.Fatal("not read")
	}
	items := itemsOf(t, f, ItemsQuery{Source: "ops"})
	it := items[parent]
	if it.Under != parent || it.UnderLabel != "this page and its children" {
		t.Fatalf("offered: %+v", it)
	}
	res, err := f.s.Exclude(ctx, admin, "ops", ExcludeInput{Item: parent, Under: true, Reason: "retired"})
	if err != nil || res.Items != 2 {
		t.Fatalf("excluded: %+v %v", res, err)
	}
	if hits := f.s.Search("tape backups vm-1", "excerpt", 5); len(hits) != 0 {
		t.Fatalf("still quoted: %+v", hits)
	}
	r := syncSource(t, f, "ops")
	if len(f.s.Search("tape backups vm-1", "excerpt", 5)) != 0 || len(f.s.Search("new platform k8s-1", "excerpt", 5)) == 0 || r.Read != 0 {
		t.Fatalf("after a read: %+v", r)
	}
	if n := len(itemsOf(t, f, ItemsQuery{Source: "ops", State: StateExcluded})); n != 2 {
		t.Fatalf("excluded listed: %d", n)
	}
}

// The web pages added in Mycroft before 0.12.1 become a web site each, at
// the upgrade, once; their excerpts stay until the site's first read
// reads them again (KN-46).
func TestWebPagesAddedBecomeWebSites(t *testing.T) {
	f, x := crawlFixture(t)
	// A page added before 0.12.1, as the crawl of Add kept it.
	f.s.readSoon(ctx, bob, []pendingDoc{{source: AddedSourceID, name: "web " + strings.TrimPrefix(x.URL, "http://") + "/docs/runbook.html", title: "Runbook & more", url: x.URL + "/docs/runbook.html",
		text: "Restart the web pods after db-1.", hash: hashOf([]byte("old")), at: f.clock.now(), by: "Bob"}}, "addition")
	f.s.Wait()
	if len(f.s.Search("restart web pods", "excerpt", 5)) == 0 {
		t.Fatal("not read")
	}
	n, err := f.s.MigrateWebItems(ctx)
	if err != nil || n != 1 {
		t.Fatalf("migrated: %d %v", n, err)
	}
	set, _ := f.s.Settings(ctx)
	i := slices.IndexFunc(set.Sources, func(s Source) bool { return s.Kind == SourceWeb })
	if i < 0 {
		t.Fatalf("no web site: %+v", set.Sources)
	}
	w := set.Sources[i]
	if w.Name != "Runbook & more" || w.URL != x.URL+"/docs/runbook.html" || w.Depth != 0 || w.Pages != 1 || len(w.MigratedFrom) != 1 {
		t.Fatalf("the web site: %+v", w)
	}
	// Once only, and nothing is lost meanwhile.
	if n, err := f.s.MigrateWebItems(ctx); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
	if len(f.s.Search("restart web pods", "excerpt", 5)) == 0 {
		t.Fatal("the migration lost the excerpts")
	}
	if it := itemsOf(t, f, ItemsQuery{Source: w.ID}); len(it) != 1 {
		t.Fatalf("listed under its web site: %+v", it)
	}
	// Its first read takes the page over: one item, its own.
	syncSource(t, f, w.ID)
	hits := f.s.Search("restart web pods", "excerpt", 5)
	if len(hits) != 1 || !strings.HasPrefix(hits[0].Source, w.ID+":") {
		t.Fatalf("after its first read: %+v", hits)
	}
	set, _ = f.s.Settings(ctx)
	if src, _ := set.source(w.ID); len(src.MigratedFrom) != 0 {
		t.Fatalf("still waiting: %+v", src)
	}
	f.s.mu.RLock()
	var old *ItemState
	for _, st := range f.s.manifest.bySource(AddedSourceID) {
		if st.URL != "" {
			old = st
		}
	}
	f.s.mu.RUnlock()
	if old == nil || !old.Gone {
		t.Fatalf("the old page: %+v", old)
	}
	// Removed, it is not made again.
	if err := f.s.RemoveSource(ctx, admin, w.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.s.MigrateWebItems(ctx); n != 0 {
		t.Fatalf("made again: %d", n)
	}
}

// The items are listed a page at a time, by source, type, state and
// words; Admins and SuperAdmins see them, a SuperAdmin excludes (KN-47).
func TestTheItemsArePagedAndFiltered(t *testing.T) {
	f, x := crawlFixture(t)
	webSite(t, f, Source{URL: x.URL + "/docs/", Depth: 2, Pages: 20})
	for _, n := range []string{"a", "b", "c"} {
		if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddText, Title: "Note " + n, Text: "Words of note " + n + "."}); err != nil {
			t.Fatal(err)
		}
		f.s.Wait()
	}
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "status", Name: "Status", Kind: SourceLive, Addresses: []LiveAddress{{Kind: LivePage, URL: "https://status.example.com/"}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	if w := serve(t, f.s, &viewer, "GET", "items", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer: %d", w.Code)
	}
	w := serve(t, f.s, &bob, "GET", "items?source=uploads&type=note&limit=2", "", nil)
	var p ItemsPage
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != http.StatusOK {
		t.Fatalf("an Admin: %d %s", w.Code, w.Body)
	}
	if p.Total != 3 || len(p.Items) != 2 || p.MayExclude || !p.MayRemove || p.Sources[0].ID != UploadsSource {
		t.Fatalf("a page: %+v", p)
	}
	w = serve(t, f.s, &bob, "GET", "items?source=uploads&type=note&limit=2&offset=2", "", nil)
	p = ItemsPage{}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Total != 3 || len(p.Items) != 1 || p.Items[0].Title != "Note c" {
		t.Fatalf("the next page: %+v", p)
	}
	w = serve(t, f.s, &admin, "GET", "items?q=runbook", "", nil)
	p = ItemsPage{}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Total != 1 || p.Items[0].ID != "/docs/runbook.html" || !p.MayExclude || !p.Items[0].Excludable || p.Items[0].Under != "/docs/*" {
		t.Fatalf("words: %+v", p)
	}
	w = serve(t, f.s, &admin, "GET", "items?state=live", "", nil)
	p = ItemsPage{}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Total != 1 || p.Items[0].Type != TypeLiveAddress || !strings.Contains(p.Items[0].Why, "never copied") {
		t.Fatalf("live: %+v", p)
	}
	w = serve(t, f.s, &admin, "GET", "items?source=site&state=skipped", "", nil)
	p = ItemsPage{}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	// robots.txt, noindex, an image, and a link that answered 404.
	if p.Total != 4 {
		t.Fatalf("skipped: %+v", p)
	}
	// The details: the first excerpts and the pages that drew on it.
	w = serve(t, f.s, &bob, "GET", "items/detail?source=site&id=%2Fdocs%2Frunbook.html", "", nil)
	var d ItemDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || w.Code != http.StatusOK || len(d.Excerpts) == 0 || len(d.Pages) != 1 || !d.Provenance {
		t.Fatalf("details: %d %s", w.Code, w.Body)
	}
	if w := serve(t, f.s, &viewer, "GET", "items/detail?source=site&id=%2Fdocs%2F", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer's details: %d", w.Code)
	}
	// Exclude and include again through the API: a SuperAdmin's.
	if w := serve(t, f.s, &bob, "POST", "sources/site/exclusions", `{"item":"/docs/runbook.html"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("an Admin excludes: %d", w.Code)
	}
	if w := serve(t, f.s, &admin, "POST", "sources/site/exclusions", `{"item":"/docs/runbook.html","reason":"old"}`, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":1`) {
		t.Fatalf("excluded: %d %s", w.Code, w.Body)
	}
	if w := serve(t, f.s, &bob, "DELETE", "sources/site/exclusions?pattern=%2Fdocs%2Frunbook.html", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("an Admin includes: %d", w.Code)
	}
	if w := serve(t, f.s, &admin, "DELETE", "sources/site/exclusions?pattern=%2Fdocs%2Frunbook.html", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Run now") {
		t.Fatalf("included: %d %s", w.Code, w.Body)
	}
}

// Settings › Knowledge's Uploaded documents list the notes and documents
// of both sources of uploaded documents, newest first; an Admin adds and
// removes; a document replaces the one of its name in either; removing
// says the pages that drew on it, which Mycroft may be asked to rewrite
// (MY-12, MY-14).
func TestUploadedDocumentsAndNotes(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.Upload(ctx, admin, "documents", "runbook.md", []byte("# Runbook\n\nRestart db-1 first.")); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	f.clock.add(60e9)
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddText, Title: "Backups", Text: "Backups are kept 30 days."}); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	if _, err := f.s.Uploads(ctx, viewer); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer: %v", err)
	}
	v, err := f.s.Uploads(ctx, bob)
	if err != nil || len(v.Items) != 2 || v.Items[0].Type != TypeNote || v.Items[0].Title != "Backups" || v.Items[0].By != "Bob" || v.Items[0].State != StateRead ||
		v.Items[1].Type != TypeDocument || v.Items[1].Source != "documents" || v.MaySetup || len(v.Off) != 0 {
		t.Fatalf("the list: %+v %v", v, err)
	}
	// A document of the same name, added by an Admin, replaces the one
	// a SuperAdmin uploaded.
	f.clock.add(60e9)
	if _, err := f.s.Add(ctx, bob, AddInput{Kind: AddDocument, Name: "runbook.md", Data: []byte("# Runbook\n\nRestart db-2 first.")}); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	v, _ = f.s.Uploads(ctx, bob)
	if len(v.Items) != 2 || v.Items[0].Source != AddedSourceID || v.Items[0].Title != "runbook.md" {
		t.Fatalf("replaced: %+v", v.Items)
	}
	if hits := f.s.Search("restart db-1", "excerpt", 5); len(hits) != 0 && strings.Contains(hits[0].Text, "db-1") {
		t.Fatalf("the old one is quoted: %+v", hits)
	}
	// Mycroft's pages approved, so they can be corrected.
	runs, _ := f.s.Runs(ctx, false)
	for _, r := range runs {
		if _, err := f.s.Approve(ctx, admin, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Removed by an Admin: final, and the pages that drew on it named.
	w := serve(t, f.s, &viewer, "DELETE", "uploads/added/runbook.md", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a viewer removes: %d", w.Code)
	}
	w = serve(t, f.s, &bob, "DELETE", "uploads/added/runbook.md", "", nil)
	var rr RemoveResult
	if err := json.Unmarshal(w.Body.Bytes(), &rr); err != nil || w.Code != http.StatusOK || !rr.Removed || len(rr.Pages) != 1 {
		t.Fatalf("removed: %d %s", w.Code, w.Body)
	}
	if hits := f.s.Search("restart db-2", "excerpt", 5); len(hits) != 0 {
		t.Fatalf("still quoted: %+v", hits)
	}
	// Ask Mycroft to rewrite them: a correction each, nothing changed yet.
	if _, err := f.s.Rewrite(ctx, viewer, []string{rr.Pages[0].Path}, "runbook.md", "removed"); !errors.Is(err, ErrDenied) {
		t.Fatalf("a viewer asks: %v", err)
	}
	res, err := f.s.Rewrite(ctx, bob, []string{rr.Pages[0].Path, "pages/system/none.md"}, "runbook.md", "removed")
	if err != nil || res.Sent != 1 || len(res.Failed) != 1 || !strings.Contains(res.Text, "Drafts") {
		t.Fatalf("rewrite: %+v %v", res, err)
	}
	f.s.Wait()
	if !slices.ContainsFunc(f.model.calls(), func(c string) bool {
		return strings.Contains(c, "Rewrite this page without what it took from that source")
	}) {
		t.Fatal("Mycroft was not asked to rewrite the page")
	}
	// A switched-off source is said, for a SuperAdmin to switch it on.
	if err := f.s.SetSourceOn(ctx, admin, "documents", false); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.s.Uploads(ctx, admin); len(v.Off) != 1 || v.Off[0].ID != "documents" || !v.MaySetup {
		t.Fatalf("off: %+v", v)
	}
}
