package knowledge

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/confluence"
)

// --- Mycroft tab (Phase 12, wave 5, MY-06, ADR-119) ---
//
// The crawl of a web address that a person adds: the page at the address
// and the pages of the same site (scheme, host and port) it links to,
// breadth first, up to a depth and a number of pages, one request at a
// time. robots.txt is read first and respected (the group for 221B-Mycroft,
// else "*"; Allow and Disallow, longest match; * and $), as are a page's
// robots meta tags (noindex, nofollow). Redirects stay on the site. HTML,
// plain text, Markdown, PDF and Word documents are read; anything else is
// skipped with why. The console reaches the site from its pod, so the
// address must be reachable under the chart's network policy
// (knowledge.egressCIDRs); it never reads a loopback, link-local
// (cloud metadata) or multicast address.

// CrawlAgent is the user agent the crawl sends, and the robots.txt group
// it looks for first.
const CrawlAgent = "221B-Mycroft"

// crawled is one page read.
type crawled struct {
	url, host, path, title, text string
}

type crawlResult struct {
	start   string
	pages   []crawled
	skipped []Skipped
	more    bool
}

// errRefusedAddress is an address the crawl does not reach.
var errRefusedAddress = errors.New("the console does not read loopback, link-local or multicast addresses")

// guardedDial refuses addresses inside the pod or the cloud's metadata
// service, whatever a name resolves to, at the moment of connecting.
func guardedDial(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("%s: %w", host, errRefusedAddress)
	}
	return nil
}

// crawlClient is the crawl's HTTP client: the service's own in tests,
// otherwise one whose dialer refuses the addresses above. Redirects are
// followed on the same site only, five at most.
func (s *Service) crawlClient(site *url.URL) *http.Client {
	var base http.RoundTripper
	if s.o.CrawlHTTP != nil {
		base = s.o.CrawlHTTP.Transport
		if base == nil {
			base = http.DefaultTransport
		}
	} else {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		// Straight to the site, never through a proxy, so the guard
		// sees the address connected to.
		tr.Proxy = nil
		tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: guardedDial}).DialContext
		base = tr
	}
	return &http.Client{
		Transport: base,
		Timeout:   20 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !sameSite(site, r.URL) {
				return fmt.Errorf("it redirects to another site (%s)", r.URL.Host)
			}
			return nil
		},
	}
}

// sameSite says whether two addresses are of one site: scheme, host and
// port.
func sameSite(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// crawlStart checks a web address a person typed.
func crawlStart(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, invalid("give a web address that starts with https:// or http://")
	}
	if u.User != nil {
		return nil, invalid("a web address to read has no user or password in it")
	}
	if len(raw) > 2000 {
		return nil, invalid("the web address is too long")
	}
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// crawl reads a site from an address.
func (s *Service) crawl(ctx context.Context, raw string, depth, limit int) (crawlResult, error) {
	start, err := crawlStart(raw)
	if err != nil {
		return crawlResult{}, err
	}
	res := crawlResult{start: start.String()}
	hc := s.crawlClient(start)
	rules, err := readRobots(ctx, hc, start)
	if err != nil {
		return res, err
	}
	type queued struct {
		u     *url.URL
		depth int
	}
	queue := []queued{{start, 0}}
	seen := map[string]bool{start.String(): true}
	read := map[string]bool{}
	total := 0
	for len(queue) > 0 {
		if len(res.pages) >= limit {
			res.more = true
			break
		}
		if ctx.Err() != nil {
			res.skipped = append(res.skipped, Skipped{URL: queue[0].u.String(), Why: "the crawl ran out of time"})
			res.more = true
			break
		}
		q := queue[0]
		queue = queue[1:]
		addr := q.u.String()
		if !rules.allowed(q.u.EscapedPath() + queryOf(q.u)) {
			res.skipped = append(res.skipped, Skipped{URL: addr, Why: "robots.txt asks crawlers not to read it"})
			continue
		}
		page, links, err := fetchPage(ctx, hc, q.u, min(MaxCrawlPage, MaxCrawlBytes-total))
		if err != nil {
			res.skipped = append(res.skipped, Skipped{URL: addr, Why: clip(err.Error(), 200)})
			if errors.Is(err, errRefusedAddress) && q.depth == 0 {
				return res, invalid("%s: %v", start.Host, errRefusedAddress)
			}
			continue
		}
		// A redirect may end on a page read already.
		if read[page.url] {
			continue
		}
		read[page.url], seen[page.url] = true, true
		total += len(page.text)
		if page.text != "" {
			res.pages = append(res.pages, page)
		} else {
			res.skipped = append(res.skipped, Skipped{URL: addr, Why: "it holds no text to read, or asks not to be indexed"})
		}
		if q.depth >= depth {
			continue
		}
		for _, l := range links {
			if !sameSite(start, l) {
				continue
			}
			if k := l.String(); !seen[k] && len(seen) < 20*MaxCrawlPages {
				seen[k] = true
				queue = append(queue, queued{l, q.depth + 1})
			}
		}
		if total >= MaxCrawlBytes {
			res.more = len(queue) > 0
			break
		}
	}
	return res, nil
}

func queryOf(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

var (
	aTag        = regexp.MustCompile(`(?is)<a\s[^>]*>`)
	hrefAttr    = regexp.MustCompile(`(?is)\shref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	relNoFollow = regexp.MustCompile(`(?is)\srel\s*=\s*["']?[^"'>]*\bnofollow\b`)
	titleTag    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	metaRobots  = regexp.MustCompile(`(?is)<meta\s[^>]*name\s*=\s*["']?(?:robots|` + CrawlAgent + `)["']?[^>]*>`)
	metaValue   = regexp.MustCompile(`(?is)content\s*=\s*["']([^"']*)["']`)
	bodyTag     = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)</body>`)
	dropBlocks  = regexp.MustCompile(`(?is)<!--.*?-->|<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<noscript\b[^>]*>.*?</noscript>|<svg\b[^>]*>.*?</svg>|<nav\b[^>]*>.*?</nav>`)
)

// htmlText is a web page's text: its body, without scripts, styles,
// comments and navigation, as the Confluence pages' text is read.
func htmlText(doc string) string {
	if m := bodyTag.FindStringSubmatch(doc); m != nil {
		doc = m[1]
	}
	return strings.TrimSpace(confluence.Text(dropBlocks.ReplaceAllString(doc, " ")))
}

// fetchPage reads one page: its text, title and the links it holds.
func fetchPage(ctx context.Context, hc *http.Client, u *url.URL, limit int) (crawled, []*url.URL, error) {
	page := crawled{url: u.String(), host: u.Host, path: u.EscapedPath() + queryOf(u)}
	if limit <= 0 {
		return page, nil, errors.New("the crawl read as much as it holds")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return page, nil, err
	}
	req.Header.Set("User-Agent", CrawlAgent+"/1.0 (+the 221B console's knowledge)")
	req.Header.Set("Accept", "text/html, text/plain, text/markdown, application/pdf;q=0.8, */*;q=0.1")
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return page, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page, nil, fmt.Errorf("it answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return page, nil, err
	}
	if len(body) > limit {
		return page, nil, fmt.Errorf("it is over %d MiB", max(limit>>20, 1))
	}
	// Where it ended up, after its redirects.
	final := resp.Request.URL
	page.url, page.path = final.String(), final.EscapedPath()+queryOf(final)
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = http.DetectContentType(body)
		ct, _, _ = mime.ParseMediaType(ct)
	}
	name := path.Base(final.Path)
	switch {
	case ct == "text/html" || ct == "application/xhtml+xml":
		doc := string(body)
		if !utf8.ValidString(doc) {
			doc = strings.ToValidUTF8(doc, " ")
		}
		index, follow := robotsMeta(doc)
		var links []*url.URL
		if follow {
			links = pageLinks(final, doc)
		}
		if m := titleTag.FindStringSubmatch(doc); m != nil {
			page.title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
		}
		if index {
			page.text = htmlText(doc)
		}
		page.title = pageTitle(page.title, final)
		return page, links, nil
	case ct == "text/plain" || ct == "text/markdown" || ct == "text/x-markdown":
		if !utf8.Valid(body) {
			return page, nil, errors.New("it is not UTF-8 text")
		}
		page.text = strings.TrimSpace(string(body))
	case ct == "application/pdf":
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			name += ".pdf"
		}
		page.text, err = Extract(name, body)
	case ct == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		if !strings.HasSuffix(strings.ToLower(name), ".docx") {
			name += ".docx"
		}
		page.text, err = Extract(name, body)
	default:
		return page, nil, fmt.Errorf("it is %s, not a page or a document the console reads", ct)
	}
	if err != nil {
		return page, nil, err
	}
	page.title = pageTitle(page.title, final)
	return page, nil, nil
}

// pageTitle is a page's title, or its address without the scheme.
func pageTitle(title string, u *url.URL) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = u.Host + u.Path
	}
	return clip(title, 200)
}

// robotsMeta reads a page's robots meta tags: whether it may be indexed,
// and its links followed.
func robotsMeta(doc string) (index, follow bool) {
	index, follow = true, true
	for _, tag := range metaRobots.FindAllString(doc, 10) {
		m := metaValue.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		for _, v := range strings.Split(strings.ToLower(m[1]), ",") {
			switch strings.TrimSpace(v) {
			case "noindex":
				index = false
			case "nofollow":
				follow = false
			case "none":
				index, follow = false, false
			}
		}
	}
	return index, follow
}

// pageLinks are the http(s) links of a page, resolved, without their
// fragments; rel="nofollow" ones are left out.
func pageLinks(base *url.URL, doc string) []*url.URL {
	var out []*url.URL
	for _, tag := range aTag.FindAllString(doc, 2000) {
		m := hrefAttr.FindStringSubmatch(tag)
		if m == nil || relNoFollow.MatchString(tag) {
			continue
		}
		raw := html.UnescapeString(m[1] + m[2] + m[3])
		l, err := base.Parse(strings.TrimSpace(raw))
		if err != nil || (l.Scheme != "http" && l.Scheme != "https") || l.User != nil {
			continue
		}
		l.Fragment, l.RawFragment = "", ""
		if l.Path == "" {
			l.Path = "/"
		}
		out = append(out, l)
	}
	return out
}

// ---------- robots.txt ----------

type robotRule struct {
	allow   bool
	pattern string
	re      *regexp.Regexp
}

// robotRules are the rules of the group that applies to the crawl.
type robotRules []robotRule

// allowed says whether a path (with its query) may be read: the longest
// matching rule decides, Allow on a tie; no match allows.
func (r robotRules) allowed(p string) bool {
	best, allow := -1, true
	for _, x := range r {
		if x.pattern == "" || !x.re.MatchString(p) {
			continue
		}
		if n := len(x.pattern); n > best || (n == best && x.allow) {
			best, allow = n, x.allow
		}
	}
	return allow
}

// readRobots reads the site's robots.txt. None (404 and the like) allows
// everything; one that cannot be read (5xx, no answer) stops the crawl,
// as RFC 9309 asks.
func readRobots(ctx context.Context, hc *http.Client, site *url.URL) (robotRules, error) {
	u := &url.URL{Scheme: site.Scheme, Host: site.Host, Path: "/robots.txt"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", CrawlAgent+"/1.0 (+the 221B console's knowledge)")
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, errRefusedAddress) {
			return nil, invalid("%s: %v", site.Host, errRefusedAddress)
		}
		return nil, invalid("%s could not be reached: %v", site.Host, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500:
		return nil, invalid("%s's robots.txt could not be read (%s): the site is not crawled until it can be", site.Host, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	return parseRobots(string(b), CrawlAgent), nil
}

// parseRobots reads the rules of the group for agent, or of "*" when no
// group names it.
func parseRobots(text, agent string) robotRules {
	type group struct {
		agents []string
		rules  robotRules
	}
	var groups []*group
	var cur *group
	inAgents := false
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if !inAgents {
				cur = &group{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(value))
			inAgents = true
		case "allow", "disallow":
			inAgents = false
			if cur != nil {
				cur.rules = append(cur.rules, robotRule{allow: key == "allow", pattern: value, re: robotPattern(value)})
			}
		default:
			inAgents = false
		}
	}
	me := strings.ToLower(agent)
	var star robotRules
	var mine robotRules
	found := false
	for _, g := range groups {
		for _, a := range g.agents {
			switch {
			case a == "*":
				star = append(star, g.rules...)
			case a != "" && strings.Contains(me, a):
				mine = append(mine, g.rules...)
				found = true
			}
		}
	}
	if found {
		return mine
	}
	return star
}

// robotPattern makes a rule's path a pattern: * any characters, $ the
// end, anything else as it is, from the start of the path.
func robotPattern(p string) *regexp.Regexp {
	end := strings.HasSuffix(p, "$")
	p = strings.TrimSuffix(p, "$")
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	expr := "^" + strings.Join(parts, ".*")
	if end {
		expr += "$"
	}
	return regexp.MustCompile(expr)
}
