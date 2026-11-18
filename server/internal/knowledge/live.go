package knowledge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/scrub"
)

// Live sources (0.12.1, KN-39 to KN-43). A source in mode Live is never
// copied or indexed: it is a list of addresses (a web page, a Confluence
// page, or a search of a Confluence space) that the agents look up when
// they troubleshoot (lookup_live_sources), and that a report no known
// issue explains is checked against (issues.Options.Live). Each address
// is read at question time, at most livePageBytes of it, within
// liveTimeout, and kept for liveCacheTTL. Only the addresses a SuperAdmin
// set are read: the tool takes words, never an address. Its credential,
// a bearer token or a user and password (a Confluence API token), is in
// the Secret like any source's; it is sent to the address's own site
// only (redirects stay on it), and never shown, logged or given to a
// model. What is read is scrubbed (X-01) and given as data, never as
// instructions.

// SourceLive is a live source's kind.
const SourceLive = "live"

// The kinds of a live source's address.
const (
	// LivePage is any web page, read as text.
	LivePage = "page"
	// LiveConfluencePage is a Confluence page by its address: read
	// through Confluence's REST API when the address names the page's id,
	// as a web page otherwise.
	LiveConfluencePage = "confluencePage"
	// LiveConfluenceSearch is a search of one space of a Confluence site
	// (URL is the site, with /wiki for Cloud): CQL text ~ the words
	// looked up, the best few pages.
	LiveConfluenceSearch = "confluenceSearch"
)

// How a live source signs in: none, a bearer token, or a user (an
// account's email for Confluence Cloud) and a password or API token.
const (
	LiveAuthBearer = "bearer"
	LiveAuthBasic  = "basic"
)

// The bounds of a live lookup.
const (
	// MaxLiveAddresses bounds a live source's addresses.
	MaxLiveAddresses = 20
	// livePageBytes is the most read of one page; the rest is left out.
	livePageBytes = 200 << 10
	// liveAPIBytes is the most read of one answer of Confluence's API,
	// whose pages are each cut to livePageBytes of text.
	liveAPIBytes = 1 << 20
	// liveCallBytes is the most one lookup reads in all.
	liveCallBytes = 2 << 20
	// liveTimeout bounds the read of one address, and liveCallTimeout a
	// whole lookup.
	liveTimeout     = 10 * time.Second
	liveCallTimeout = 25 * time.Second
	// liveCacheTTL is how long what an address gave is kept.
	liveCacheTTL = 5 * time.Minute
	// liveSearchResults is how many pages a space's search gives.
	liveSearchResults = 5
	// liveExcerpts, liveExcerptRunes and liveAnswerRunes bound what a
	// lookup gives an agent.
	liveExcerpts     = 8
	liveExcerptRunes = 1500
	liveAnswerRunes  = 9000
	// liveParallel is how many addresses are read at once.
	liveParallel = 4
)

// LiveAddress is one address of a live source.
type LiveAddress struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// Space is the space's key, for LiveConfluenceSearch.
	Space string `json:"space,omitempty"`
}

var spaceKeyPattern = regexp.MustCompile(`^~?[A-Za-z0-9_]{1,255}$`)

// validateLive checks a live source's addresses and sign-in.
func (x Source) validateLive() error {
	if len(x.Addresses) == 0 {
		return invalid("give a live source at least one address")
	}
	if len(x.Addresses) > MaxLiveAddresses {
		return invalid("a live source has at most %d addresses", MaxLiveAddresses)
	}
	switch x.Auth {
	case "", LiveAuthBearer:
	case LiveAuthBasic:
		if strings.TrimSpace(x.Username) == "" || len(x.Username) > 200 {
			return invalid("give the user the password or API token goes with (for Confluence Cloud, the account's email)")
		}
	default:
		return invalid("a live source signs in with none, bearer or basic")
	}
	for i, a := range x.Addresses {
		what := fmt.Sprintf("address %d", i+1)
		if len(a.URL) > 2000 {
			return invalid("%s is too long", what)
		}
		if err := httpsURL(what, a.URL, false); err != nil {
			return err
		}
		switch a.Kind {
		case LivePage, LiveConfluencePage:
		case LiveConfluenceSearch:
			if !spaceKeyPattern.MatchString(a.Space) {
				return invalid("%s searches a Confluence space: give the space's key, such as OPS", what)
			}
		default:
			return invalid("%s is a page, a Confluence page or a Confluence space search", what)
		}
	}
	return nil
}

// normalizedLive trims a live source's addresses and drops the empty ones.
func normalizedLive(list []LiveAddress) []LiveAddress {
	var out []LiveAddress
	for _, a := range list {
		a.URL, a.Space = strings.TrimSpace(a.URL), strings.TrimSpace(a.Space)
		if a.URL == "" {
			continue
		}
		if a.Kind == "" {
			a.Kind = LivePage
		}
		if a.Kind == LiveConfluenceSearch {
			a.URL = strings.TrimRight(a.URL, "/")
		} else {
			a.Space = ""
		}
		out = append(out, a)
	}
	return out
}

// LiveDoc is one page a live source gave: whose, its title, where it is,
// and its text in parts (a paragraph, a list, a table's rows), scrubbed.
type LiveDoc struct {
	SourceID string   `json:"sourceId"`
	Source   string   `json:"source"`
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Parts    []string `json:"parts"`
	// Cut says the page was longer than what was read.
	Cut bool `json:"cut,omitempty"`
}

// LiveExcerpt is a part of a live page that bears on a lookup's words.
type LiveExcerpt struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Text   string `json:"text"`
	Score  int    `json:"score"`
}

// LiveLookup is what a lookup found: the excerpts, the pages read, and
// what could not be read and why.
type LiveLookup struct {
	Excerpts []LiveExcerpt `json:"excerpts"`
	Pages    []LiveDoc     `json:"pages"`
	Problems []string      `json:"problems,omitempty"`
	// Sources is how many live sources were looked up.
	Sources int `json:"sources"`
}

// LiveCheck is one address's Test: whether it was read, how much, and
// what was wrong.
type LiveCheck struct {
	URL   string `json:"url"`
	Kind  string `json:"kind"`
	OK    bool   `json:"ok"`
	Pages int    `json:"pages,omitempty"`
	Bytes int    `json:"bytes,omitempty"`
	Title string `json:"title,omitempty"`
	Cut   bool   `json:"cut,omitempty"`
	Error string `json:"error,omitempty"`
}

// liveEntry is what one address gave, kept for liveCacheTTL.
type liveEntry struct {
	docs  []LiveDoc
	bytes int
	at    time.Time
}

// liveCache keeps what each address gave, by source and address (and the
// words, for a search).
type liveCache struct {
	mu sync.Mutex
	m  map[string]liveEntry
}

func (c *liveCache) get(key string, now time.Time) (liveEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) >= liveCacheTTL {
		return liveEntry{}, false
	}
	return e, true
}

func (c *liveCache) put(key string, e liveEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]liveEntry{}
	}
	for k, old := range c.m {
		if e.at.Sub(old.at) >= liveCacheTTL {
			delete(c.m, k)
		}
	}
	c.m[key] = e
}

// forget drops what a source's addresses gave, when it is changed or
// removed.
func (c *liveCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if strings.HasPrefix(k, id+"\x00") {
			delete(c.m, k)
		}
	}
}

// LiveSources are the live sources switched on.
func (s *Service) LiveSources(ctx context.Context) ([]Source, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	var out []Source
	for _, x := range set.Sources {
		if x.Kind == SourceLive && !x.Off {
			out = append(out, x)
		}
	}
	return out, nil
}

// LookupLive reads the live sources for words (empty: every page as it
// starts) and gives the parts of their pages that bear on them, best
// first, with each page's source, title and address to cite.
func (s *Service) LookupLive(ctx context.Context, query string) (LiveLookup, error) {
	docs, problems, n, err := s.LiveDocs(ctx, query)
	if err != nil {
		return LiveLookup{}, err
	}
	return LiveLookup{Excerpts: rankLive(docs, query), Pages: docs, Problems: problems, Sources: n}, nil
}

// LiveDocs reads the live sources' pages for words, as they are now or
// as read within liveCacheTTL: the pages, scrubbed, what could not be
// read, and how many live sources there are.
func (s *Service) LiveDocs(ctx context.Context, query string) ([]LiveDoc, []string, int, error) {
	sources, err := s.LiveSources(ctx)
	if err != nil || len(sources) == 0 {
		return nil, nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, liveCallTimeout)
	defer cancel()
	secrets := s.secretValues(ctx)
	terms := liveTerms(query)
	type job struct {
		x    Source
		c    Creds
		a    LiveAddress
		slot int
	}
	var jobs []job
	var problems []string
	for _, x := range sources {
		x, c, err := s.sourceAuth(ctx, x)
		if err != nil {
			problems = append(problems, x.Name+": its credential could not be used: "+scrub.Known(err.Error(), secrets))
			continue
		}
		for _, a := range x.Addresses {
			jobs = append(jobs, job{x: x, c: c, a: a, slot: len(jobs)})
		}
	}
	results := make([][]LiveDoc, len(jobs))
	errs := make([]string, len(jobs))
	var read atomic.Int64
	sem := make(chan struct{}, liveParallel)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			key := liveKey(j.x, j.a, terms)
			if e, ok := s.live.get(key, s.o.Now()); ok {
				results[j.slot] = e.docs
				return
			}
			if read.Load() >= liveCallBytes {
				errs[j.slot] = j.x.Name + ": " + j.a.URL + ": not read, this lookup read as much as it holds"
				return
			}
			docs, n, err := s.fetchLive(ctx, j.x, j.c, j.a, terms)
			read.Add(int64(n))
			if err != nil {
				errs[j.slot] = scrub.Known(j.x.Name+": "+j.a.URL+": "+err.Error(), secrets)
				return
			}
			for i := range docs {
				docs[i].SourceID, docs[i].Source = j.x.ID, j.x.Name
				for k, p := range docs[i].Parts {
					docs[i].Parts[k] = scrub.Known(scrub.Text(p), secrets)
				}
				docs[i].Title = scrub.Known(scrub.Text(docs[i].Title), secrets)
			}
			s.live.put(key, liveEntry{docs: docs, bytes: n, at: s.o.Now()})
			results[j.slot] = docs
		}()
	}
	wg.Wait()
	var out []LiveDoc
	for i := range jobs {
		out = append(out, results[i]...)
		if errs[i] != "" {
			problems = append(problems, errs[i])
		}
	}
	return out, problems, len(sources), nil
}

// liveKey names what an address gives: a page whatever the words, a
// search for its words.
func liveKey(x Source, a LiveAddress, terms []string) string {
	k := x.ID + "\x00" + a.Kind + "\x00" + a.URL + "\x00" + a.Space
	if a.Kind == LiveConfluenceSearch {
		k += "\x00" + strings.Join(terms, " ")
	}
	return k
}

// testLive reads each address of a live source as typed, and says
// whether it was read, how much, and what was wrong, without a
// credential in it. Nothing is kept.
func (s *Service) testLive(ctx context.Context, x Source, c Creds) SourceTest {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	secrets := c.secrets()
	out := SourceTest{}
	for _, a := range x.Addresses {
		ck := LiveCheck{URL: a.URL, Kind: a.Kind}
		docs, n, err := s.fetchLive(ctx, x, c, a, nil)
		switch {
		case err != nil:
			ck.Error = scrub.Known(err.Error(), secrets)
		case len(docs) == 0:
			ck.Error = "it gave no page"
		default:
			ck.OK, ck.Pages, ck.Bytes, ck.Title = true, len(docs), n, docs[0].Title
			for _, d := range docs {
				ck.Cut = ck.Cut || d.Cut
			}
			out.Items++
			out.Sample = append(out.Sample, docs[0].Title)
		}
		out.Live = append(out.Live, ck)
	}
	if len(out.Sample) > testSample {
		out.Sample = out.Sample[:testSample]
	}
	return out
}

// liveClient reads a live source's addresses: the crawl's own client in
// tests, otherwise one that refuses loopback, link-local and multicast
// addresses (guardedDial), trusts the source's private CA, goes straight
// to the site, and follows redirects on the same site only.
func (s *Service) liveClient(x Source, site *url.URL) *http.Client {
	var base http.RoundTripper
	if s.o.CrawlHTTP != nil {
		base = s.o.CrawlHTTP.Transport
		if base == nil {
			base = http.DefaultTransport
		}
	} else {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.DialContext = (&net.Dialer{Timeout: liveTimeout, Control: guardedDial}).DialContext
		if strings.TrimSpace(x.CAPEM) != "" || x.cert != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: s.rootsFor(x), MinVersion: tls.VersionTLS12}
			if x.cert != nil {
				tr.TLSClientConfig.Certificates = []tls.Certificate{*x.cert}
			}
		}
		base = tr
	}
	return &http.Client{
		Transport: base,
		Timeout:   liveTimeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !sameSite(site, r.URL) {
				return fmt.Errorf("it redirects to another site (%s), which is not read", r.URL.Host)
			}
			return nil
		},
	}
}

// fetchLive reads one address: its pages, and how many bytes it took.
func (s *Service) fetchLive(ctx context.Context, x Source, c Creds, a LiveAddress, terms []string) ([]LiveDoc, int, error) {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, 0, errors.New("not an https address")
	}
	ctx, cancel := context.WithTimeout(ctx, liveTimeout)
	defer cancel()
	hc := s.liveClient(x, u)
	switch a.Kind {
	case LiveConfluenceSearch:
		return liveSearch(ctx, hc, x, c, a, terms)
	case LiveConfluencePage:
		if base, id := confluencePageRef(u); id != "" {
			return liveConfluencePage(ctx, hc, x, c, base, id, a.URL)
		}
	}
	return livePage(ctx, hc, x, c, u)
}

// liveGet reads an address with the source's credential, at most limit
// bytes: the body, its media type, where it ended up, and whether it was
// cut.
func liveGet(ctx context.Context, hc *http.Client, x Source, c Creds, raw, accept string, limit int) ([]byte, string, *url.URL, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", nil, false, err
	}
	req.Header.Set("User-Agent", CrawlAgent+"/1.0 (+the 221B console's live knowledge)")
	req.Header.Set("Accept", accept)
	switch {
	case c.apply != nil:
		// A credential signs only the addresses under its endpoint.
		c.apply(req)
	case x.Auth == LiveAuthBearer:
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
	case x.Auth == LiveAuthBasic:
		req.SetBasicAuth(x.Username, c.Token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", nil, false, fmt.Errorf("it did not answer within %d s", int(liveTimeout/time.Second))
		}
		return nil, "", nil, false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, "", nil, false, fmt.Errorf("it refused the credential (%s)", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, "", nil, false, fmt.Errorf("it answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, "", nil, false, err
	}
	cut := len(body) > limit
	if cut {
		body = body[:limit]
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	return body, ct, resp.Request.URL, cut, nil
}

// livePage reads a web page as text.
func livePage(ctx context.Context, hc *http.Client, x Source, c Creds, u *url.URL) ([]LiveDoc, int, error) {
	body, ct, final, cut, err := liveGet(ctx, hc, x, c, u.String(), "text/html, text/plain, text/markdown;q=0.9, */*;q=0.1", livePageBytes)
	if err != nil {
		return nil, 0, err
	}
	doc := strings.ToValidUTF8(string(body), " ")
	var title, text string
	switch ct {
	case "text/html", "application/xhtml+xml":
		if m := titleTag.FindStringSubmatch(doc); m != nil {
			title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
		}
		text = htmlText(doc)
	case "text/plain", "text/markdown", "text/x-markdown":
		text = strings.TrimSpace(doc)
	default:
		return nil, len(body), fmt.Errorf("it is %s, not a page the console reads", ct)
	}
	return []LiveDoc{{Title: pageTitle(title, final), URL: final.String(), Parts: liveParts(text), Cut: cut}}, len(body), nil
}

// confluencePageRef finds the site and the page's id in a Confluence
// page's address: …/wiki/spaces/OPS/pages/123/Title (Cloud),
// …/pages/viewpage.action?pageId=123 (Data Center), or "" when the
// address names no id (…/display/OPS/Title).
func confluencePageRef(u *url.URL) (base, id string) {
	if v := u.Query().Get("pageId"); v != "" && isDigits(v) {
		id = v
	} else if m := pagesID.FindStringSubmatch(u.Path); m != nil {
		id = m[1]
	}
	if id == "" {
		return "", ""
	}
	p := u.Path
	for _, mark := range []string{"/spaces/", "/pages/", "/display/"} {
		if i := strings.Index(p, mark); i >= 0 {
			p = p[:i]
			break
		}
	}
	b := url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimRight(p, "/")}
	return b.String(), id
}

var pagesID = regexp.MustCompile(`/pages/(\d+)(?:/|$)`)

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// confluenceContent is a page as Confluence's REST API gives it.
type confluenceContent struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
		Base  string `json:"base"`
	} `json:"_links"`
}

// doc is a page's text in parts, at most livePageBytes of it.
func (p confluenceContent) doc(link string) LiveDoc {
	text := confluence.Text(p.Body.Storage.Value)
	cut := len(text) > livePageBytes
	if cut {
		text = cutBytes(text, livePageBytes)
	}
	return LiveDoc{Title: strings.TrimSpace(p.Title), URL: link, Parts: liveParts(text), Cut: cut}
}

// liveConfluencePage reads one Confluence page through the REST API.
func liveConfluencePage(ctx context.Context, hc *http.Client, x Source, c Creds, base, id, link string) ([]LiveDoc, int, error) {
	body, _, _, cut, err := liveGet(ctx, hc, x, c, base+"/rest/api/content/"+url.PathEscape(id)+"?expand=body.storage", "application/json", liveAPIBytes)
	if err != nil {
		return nil, 0, err
	}
	if cut {
		return nil, len(body), fmt.Errorf("Confluence's answer is over %d KiB", liveAPIBytes>>10)
	}
	var p confluenceContent
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, len(body), errors.New("it did not answer as Confluence's REST API does: check the address")
	}
	return []LiveDoc{p.doc(link)}, len(body), nil
}

// liveSearch searches one space of a Confluence site for the words: its
// best pages, or its latest without words.
func liveSearch(ctx context.Context, hc *http.Client, x Source, c Creds, a LiveAddress, terms []string) ([]LiveDoc, int, error) {
	cql := fmt.Sprintf(`space = "%s" and type = page`, cqlEscape(a.Space))
	if len(terms) > 0 {
		cql += fmt.Sprintf(` and text ~ "%s"`, cqlEscape(strings.Join(terms, " ")))
	} else {
		cql += " order by lastmodified desc"
	}
	q := url.Values{"cql": {cql}, "limit": {fmt.Sprint(liveSearchResults)}, "expand": {"body.storage"}}
	body, _, _, cut, err := liveGet(ctx, hc, x, c, a.URL+"/rest/api/content/search?"+q.Encode(), "application/json", liveAPIBytes)
	if err != nil {
		return nil, 0, err
	}
	if cut {
		return nil, len(body), fmt.Errorf("Confluence's answer is over %d KiB", liveAPIBytes>>10)
	}
	var res struct {
		Results []confluenceContent `json:"results"`
		Links   struct {
			Base string `json:"base"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, len(body), errors.New("it did not answer as Confluence's REST API does: check the address (for Cloud, the site with /wiki)")
	}
	base := strings.TrimRight(res.Links.Base, "/")
	if base == "" {
		base = a.URL
	}
	var out []LiveDoc
	for _, p := range res.Results {
		link := a.URL
		if strings.HasPrefix(p.Links.WebUI, "/") {
			link = base + p.Links.WebUI
		}
		out = append(out, p.doc(link))
	}
	return out, len(body), nil
}

// cqlEscape makes text safe inside a CQL string.
func cqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// cutBytes cuts s to at most n bytes, at a character's boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// liveParts splits a page's text into parts of about a paragraph: a
// heading starts one, a line of a sentence or more (80 bytes) ends one,
// and shorter lines (list items, table rows) gather up to 800 bytes.
func liveParts(text string) []string {
	var out []string
	var cur []string
	size := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
		}
		cur, size = nil, 0
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			flush()
		}
		cur = append(cur, line)
		size += len(line)
		if (len(line) >= 80 && !strings.HasPrefix(line, "#")) || size >= 800 {
			flush()
		}
	}
	flush()
	return out
}

// liveStop are words that say nothing about a problem.
var liveStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for are was were with that this from have has had not but you your our its into out can cannot could would should what when where which who why how all any some there their them they then than also just more most very only over under after before about again here been being does did doing done will shall may might must please today now still get got`) {
		liveStop[w] = true
	}
}

// liveTerms are a query's words worth looking for: lower case, at least
// three letters (or a number), each once, at most twelve.
func liveTerms(q string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '.' && r != '_'
	}) {
		f = strings.Trim(f, "-._")
		if (len(f) < 3 && !isDigits(f)) || liveStop[f] || slices.Contains(out, f) {
			continue
		}
		out = append(out, f)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// liveScore is how much a part bears on the words: each word it holds
// counts ten, each more time it says one counts one (up to five), and
// each word of the page's title one more.
func liveScore(part, title string, terms []string) int {
	p, t := strings.ToLower(part), strings.ToLower(title)
	score := 0
	for _, w := range terms {
		if n := strings.Count(p, w); n > 0 {
			score += 10 + min(n-1, 5)
		}
		if strings.Contains(t, w) {
			score++
		}
	}
	return score
}

// rankLive picks the parts that bear on the query, best first, within
// liveExcerpts and liveAnswerRunes; without words, each page's first
// parts in turn.
func rankLive(docs []LiveDoc, query string) []LiveExcerpt {
	terms := liveTerms(query)
	type cand struct {
		e     LiveExcerpt
		doc   int
		order int
	}
	var all []cand
	for i, d := range docs {
		for j, p := range d.Parts {
			sc := 0
			if len(terms) > 0 {
				if sc = liveScore(p, d.Title, terms); sc < 10 {
					continue
				}
			}
			text := p
			if utf8.RuneCountInString(text) > liveExcerptRunes {
				text = string([]rune(text)[:liveExcerptRunes-1]) + "…"
			}
			all = append(all, cand{e: LiveExcerpt{Source: d.Source, Title: d.Title, URL: d.URL, Text: text, Score: sc}, doc: i, order: j})
		}
	}
	if len(terms) > 0 {
		sort.SliceStable(all, func(a, b int) bool { return all[a].e.Score > all[b].e.Score })
	} else {
		// Each page's first part, then each's second, and so on.
		sort.SliceStable(all, func(a, b int) bool {
			if all[a].order != all[b].order {
				return all[a].order < all[b].order
			}
			return all[a].doc < all[b].doc
		})
	}
	var out []LiveExcerpt
	runes := 0
	for _, c := range all {
		n := utf8.RuneCountInString(c.e.Text)
		if len(out) == liveExcerpts || runes+n > liveAnswerRunes {
			break
		}
		out = append(out, c.e)
		runes += n
	}
	return out
}
