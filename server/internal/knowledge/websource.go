package knowledge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// A web site (0.12.1, KN-44): a source read by crawling from a start
// address, as an address added in Mycroft was (MY-06): the same site only
// (scheme, host and port), breadth first, as many links away and as many
// pages as its settings say, robots.txt and the pages' noindex and
// nofollow respected, loopback, link-local and multicast addresses
// refused. It signs in as a live source does: none, a bearer token or a
// user with a password or API token, the credential in the Secret and
// sent to the site only. Each page is an item whose id is its path and
// query and whose version is its text's hash, so a page unchanged is not
// sent to Mycroft again; a page gone from the site leaves the index at the
// next full read. Its excluded pages (KN-45) are never fetched, and the
// links found only on them never followed.

// MaxSitePages bounds a web site's pages a read; DefaultCrawlPages is
// its default. SiteCrawlTime and MaxSiteBytes bound one read.
const (
	MaxSitePages  = 200
	SiteCrawlTime = 5 * time.Minute
	MaxSiteBytes  = 32 << 20
)

// validateWeb checks a web site's start address, reach and sign-in.
func (x Source) validateWeb() error {
	u, err := crawlStart(x.URL)
	if err != nil {
		return err
	}
	if x.Depth < 0 || x.Depth > MaxCrawlDepth {
		return invalid("a web site is read 0 to %d links away from its start address", MaxCrawlDepth)
	}
	if x.Pages < 1 || x.Pages > MaxSitePages {
		return invalid("a web site's read takes 1 to %d pages", MaxSitePages)
	}
	switch x.Auth {
	case "":
	case LiveAuthBearer, LiveAuthBasic:
		if u.Scheme != "https" {
			return invalid("a web site that signs in is read over https only")
		}
		if x.Auth == LiveAuthBasic && (strings.TrimSpace(x.Username) == "" || len(x.Username) > 200) {
			return invalid("give the user the password or API token goes with")
		}
	default:
		return invalid("a web site signs in with none, bearer or basic")
	}
	return nil
}

// siteAuth sets a web site's credential on a request, as a live source's.
func siteAuth(x Source, c Creds) func(*http.Request) {
	if c.apply != nil {
		// A credential signs only the pages under its endpoint (CR-04).
		return c.apply
	}
	switch x.Auth {
	case LiveAuthBearer:
		if c.Token == "" {
			return nil
		}
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.Token) }
	case LiveAuthBasic:
		return func(r *http.Request) { r.SetBasicAuth(x.Username, c.Token) }
	}
	return nil
}

// webReader reads a web site: List crawls it and keeps each page's text
// until Read takes it.
type webReader struct {
	s     *Service
	x     Source
	c     Creds
	pages map[string]crawled
	// skipped are the pages the crawl did not read, with why.
	skipped  []Skipped
	excluded int
}

// webItemID is a page's id: its path and query.
func webItemID(p crawled) string { return p.path }

func (r *webReader) List(ctx context.Context, _ time.Time) ([]Item, bool, error) {
	if r.x.Auth == LiveAuthBearer && r.c.Token == "" || r.x.Auth == LiveAuthBasic && r.c.Token == "" {
		return nil, false, invalid("no credential is saved for %s", r.x.Name)
	}
	cctx, cancel := context.WithTimeout(ctx, SiteCrawlTime)
	defer cancel()
	ex := r.x.Excluded
	res, err := r.s.crawlWith(cctx, r.x.URL, r.x.Depth, r.x.Pages, crawlOpts{source: &r.x, auth: siteAuth(r.x, r.c), maxBytes: MaxSiteBytes, skip: func(p string) bool {
		_, ok := matchExclusion(r.x.Kind, ex, p, nil)
		return ok
	}})
	if err != nil {
		return nil, false, err
	}
	if len(res.pages) == 0 && len(res.skipped) > 0 && res.skipped[0].failed {
		return nil, false, fmt.Errorf("%s could not be read: %s", clip(res.start, 200), res.skipped[0].Why)
	}
	r.pages = map[string]crawled{}
	r.skipped, r.excluded = res.skipped, res.excluded
	// No address or title holds the credential, even one a site wrote
	// back into a redirect.
	known := urlSecrets(r.c.secrets())
	for i := range r.skipped {
		r.skipped[i].URL = redactValues(r.skipped[i].URL, known)
	}
	out := make([]Item, 0, len(res.pages))
	for _, p := range res.pages {
		p.url, p.path, p.title = redactValues(p.url, known), redactValues(p.path, known), redactValues(p.title, known)
		id := webItemID(p)
		r.pages[id] = p
		out = append(out, Item{ID: id, Title: p.title, Version: hashOf([]byte(p.title + "\x00" + p.text))[:16], URL: p.url})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	// A read that could not fetch a page, or stopped at its limit, is not
	// every page: what it did not reach is not gone.
	complete := !res.more
	for _, sk := range res.skipped {
		complete = complete && !sk.failed
	}
	return out, complete, nil
}

func (r *webReader) Read(_ context.Context, it Item) (string, error) {
	p, ok := r.pages[it.ID]
	if !ok {
		return "", fmt.Errorf("%w: %s was not read", ErrNotFound, it.ID)
	}
	return p.text, nil
}

// Skipped says how many pages the read left out, and why.
func (r *webReader) Skipped() (int, string) {
	if len(r.skipped) == 0 {
		return 0, ""
	}
	why := map[string]int{}
	for _, sk := range r.skipped {
		w := sk.Why
		if sk.failed {
			w = "could not be read"
		}
		why[w]++
	}
	keys := make([]string, 0, len(why))
	for k := range why {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", why[k], k))
	}
	return len(r.skipped), strings.Join(parts, "; ")
}

// SkippedItems are the pages left out, each by its id and why (KN-47).
func (r *webReader) SkippedItems() []skippedItem {
	out := make([]skippedItem, 0, len(r.skipped))
	for _, sk := range r.skipped {
		id, title := sk.URL, sk.URL
		if u, err := url.Parse(sk.URL); err == nil {
			id = u.EscapedPath() + queryOf(u)
		}
		out = append(out, skippedItem{ID: id, Title: title, URL: sk.URL, Why: sk.Why})
	}
	return out
}

// skippedItem is an item a source holds that Mycroft does not read.
type skippedItem struct {
	ID, Title, URL, Why string
}

// skippedLister is a reader that names the items it left out (KN-47).
type skippedLister interface {
	SkippedItems() []skippedItem
}

// maxSkippedKept bounds the items left out a source's read records in
// the manifest, for AIOps › Mycroft › Sources; the count is whole.
const maxSkippedKept = 100
