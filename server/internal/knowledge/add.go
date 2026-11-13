package knowledge

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/scrub"
)

// --- Mycroft tab (Phase 12, wave 5, MY-04 to MY-08, ADR-118) ---
//
// What people add in Mycroft's wiki (since MY-11 in Settings › Knowledge's
// Added in Mycroft card, once AIOps › Mycroft › Wiki › Add): a note of plain text,
// a web address to read (the pages of that site it links to, as deep and
// as many as asked, robots.txt respected), or documents. Each goes in as
// an item of the source "Added in Mycroft" through the same path as an
// uploaded document: scrubbed of secrets, read by the next sync of that
// source (started at once), kept as excerpts the agents can quote at once,
// and given to Mycroft, whose pages wait for review in Wiki › Drafts like
// every other change of the wiki. A person's addition is a source, not a
// page: Mycroft keeps one page per subject, and the review stays the one
// way into the approved pages.

// The kinds of addition.
const (
	AddText     = "text"
	AddURL      = "url"
	AddDocument = "document"
)

// What an addition holds at most (MY-05, MY-06).
const (
	// MaxNote is a note's text, in bytes.
	MaxNote = 200 << 10
	// maxNoteTitle is a note's title, in characters.
	maxNoteTitle = 120
	// DefaultCrawlDepth and MaxCrawlDepth are how many links away from
	// the address a crawl goes; DefaultCrawlPages and MaxCrawlPages how
	// many pages it reads.
	DefaultCrawlDepth = 1
	MaxCrawlDepth     = 3
	DefaultCrawlPages = 20
	MaxCrawlPages     = 50
	// MaxCrawlPage is one page's size, and MaxCrawlBytes a whole crawl's.
	MaxCrawlPage  = 2 << 20
	MaxCrawlBytes = 16 << 20
	// CrawlTime is how long a crawl may take.
	CrawlTime = 90 * time.Second
)

// AddInput is one addition.
type AddInput struct {
	Kind string `json:"kind"`
	// A note: its title and text.
	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"`
	// A web address, and how deep and how many pages to read; zero is
	// the default.
	URL   string `json:"url,omitempty"`
	Depth *int   `json:"depth,omitempty"`
	Pages int    `json:"pages,omitempty"`
	// A document: its file's name and bytes (base64 in JSON).
	Name string `json:"name,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// AddedItem is one item an addition made.
type AddedItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
	Bytes int    `json:"bytes"`
}

// Skipped is a web page a crawl did not read, and why.
type Skipped struct {
	URL string `json:"url"`
	Why string `json:"why"`
}

// AddResult is what an addition did.
type AddResult struct {
	Kind    string      `json:"kind"`
	Items   []AddedItem `json:"items"`
	Skipped []Skipped   `json:"skipped,omitempty"`
	// More says the crawl stopped at its page limit with links left.
	More bool `json:"more,omitempty"`
	// Model says Mycroft has a model to write pages from it; without
	// one the excerpts are kept and no pages are written.
	Model bool `json:"model"`
	// Text is the outcome, in words.
	Text string `json:"text"`
}

// MayAdd says whether a person may add to the wiki: an Admin or a
// SuperAdmin, those who review its changes.
func (c Caller) MayAdd() bool { return c.Reviewer || c.SuperAdmin }

var notWordChars = regexp.MustCompile(`[^\p{L}\p{N} ._()-]+`)

// itemName makes a document's name of an addition from words: letters,
// digits, spaces, dots, dashes and brackets, as the uploaded documents'
// names are.
func itemName(prefix, words string) string {
	n := strings.TrimSpace(notWordChars.ReplaceAllString(words, "-"))
	n = strings.Trim(regexp.MustCompile(`-{2,}`).ReplaceAllString(n, "-"), "-. ")
	if n == "" {
		n = "untitled"
	}
	n = prefix + n
	if utf8.RuneCountInString(n) > 150 {
		n = strings.TrimRight(string([]rune(n)[:150]), "-. ")
	}
	return n
}

// Add takes one addition (MY-04): it checks it, reads its text, scrubs it,
// and has the source of additions read in the background. ErrNoStore
// while no wiki is kept: the excerpts and Mycroft's drafts are kept in
// the wiki's store.
func (s *Service) Add(ctx context.Context, c Caller, in AddInput) (AddResult, error) {
	if !c.MayAdd() {
		return AddResult{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin adds to the wiki")
	}
	if err := s.leads(); err != nil {
		return AddResult{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return AddResult{}, err
	}
	if x, ok := set.source(AddedSourceID); !ok || x.Off {
		return AddResult{}, invalid("the source %q is switched off in Settings › Knowledge: switch it on to add to the wiki", "Added in Mycroft")
	}
	if _, _, err := s.currentStore(ctx); err != nil {
		return AddResult{}, err
	}
	secrets := s.secretValues(ctx)
	now := s.o.Now().UTC()
	res := AddResult{Kind: in.Kind, Model: s.o.Model != nil}
	var docs []pendingDoc
	keep := func(name, title, url, text string, raw []byte) {
		text = scrub.Known(scrub.Text(text), secrets)
		docs = append(docs, pendingDoc{source: AddedSourceID, name: name, title: title, url: url, text: text, hash: hashOf(append([]byte(url+"\x00"+title+"\x00"), raw...)), at: now, by: c.label()})
		res.Items = append(res.Items, AddedItem{ID: name, Title: title, URL: url, Bytes: len(text)})
	}
	var detail string
	switch in.Kind {
	case AddText:
		title := strings.Join(strings.Fields(in.Title), " ")
		text := strings.TrimSpace(strings.ReplaceAll(in.Text, "\r\n", "\n"))
		switch {
		case title == "":
			return AddResult{}, invalid("give the note a title")
		case utf8.RuneCountInString(title) > maxNoteTitle:
			return AddResult{}, invalid("a note's title is up to %d characters", maxNoteTitle)
		case text == "":
			return AddResult{}, invalid("the note has no text")
		case len(text) > MaxNote:
			return AddResult{}, invalid("a note is up to %d KiB; add a longer text as a document", MaxNote>>10)
		case !utf8.ValidString(text):
			return AddResult{}, invalid("the note is not UTF-8 text")
		}
		keep(itemName("note ", title)+".md", title, "", text, []byte(text))
		detail = fmt.Sprintf("note %q added to the wiki's sources (%d bytes)", title, len(text))
	case AddDocument:
		name := path.Base(strings.TrimSpace(in.Name))
		if !docName.MatchString(name) {
			return AddResult{}, invalid("a document's name is letters, digits, spaces, dots, dashes and brackets, up to 160")
		}
		if !Readable(name) {
			return AddResult{}, invalid("%s is not a document the console reads: PDF, Word (.docx), Markdown, HTML, plain text or CSV", name)
		}
		text, err := Extract(name, in.Data)
		if err != nil {
			return AddResult{}, err
		}
		keep(name, name, "", text, in.Data)
		detail = fmt.Sprintf("document %s added to the wiki's sources (%d bytes)", name, len(in.Data))
	case AddURL:
		depth := DefaultCrawlDepth
		if in.Depth != nil {
			depth = *in.Depth
		}
		pages := in.Pages
		if pages == 0 {
			pages = DefaultCrawlPages
		}
		if depth < 0 || depth > MaxCrawlDepth {
			return AddResult{}, invalid("a crawl follows links 0 to %d deep", MaxCrawlDepth)
		}
		if pages < 1 || pages > MaxCrawlPages {
			return AddResult{}, invalid("a crawl reads 1 to %d pages", MaxCrawlPages)
		}
		cctx, cancel := context.WithTimeout(ctx, CrawlTime)
		defer cancel()
		cr, err := s.crawl(cctx, strings.TrimSpace(in.URL), depth, pages)
		if err != nil {
			return AddResult{}, err
		}
		for _, p := range cr.pages {
			keep(itemName("web ", p.host+p.path), p.title, p.url, p.text, []byte(p.text))
		}
		res.Skipped, res.More = cr.skipped, cr.more
		if len(res.Items) == 0 {
			why := "no page could be read"
			if len(cr.skipped) > 0 {
				why = cr.skipped[0].URL + ": " + cr.skipped[0].Why
			}
			return AddResult{}, invalid("nothing was read from %s: %s", clip(in.URL, 200), why)
		}
		detail = fmt.Sprintf("%d web pages of %s added to the wiki's sources (depth %d, at most %d pages; %d skipped)", len(res.Items), cr.start, depth, pages, len(cr.skipped))
	default:
		return AddResult{}, invalid("an addition is text, url or document")
	}
	s.audit(audit.Event{Action: ActionAdd, Who: c.who(), Target: AddedSourceID, Outcome: audit.OutcomeOK, Detail: detail, From: c.From})
	s.readSoon(ctx, c, docs, "addition")
	res.Text = addReport(res)
	return res, nil
}

func addReport(r AddResult) string {
	var b strings.Builder
	switch len(r.Items) {
	case 1:
		fmt.Fprintf(&b, "“%s” was added.", r.Items[0].Title)
	default:
		fmt.Fprintf(&b, "%d pages were read and added.", len(r.Items))
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, " %d were left out.", len(r.Skipped))
	}
	if r.More {
		b.WriteString(" The page limit was reached before every link was followed.")
	}
	if r.Model {
		b.WriteString(" Mycroft reads it now: the agents can quote it once it is read, and the pages Mycroft writes from it wait for review under Drafts.")
	} else {
		b.WriteString(" Mycroft has no model here: the agents can quote it once it is read, and no pages are written from it.")
	}
	return b.String()
}

// AddedItems are what people added, as the manifest and the waiting
// documents hold them.
func (s *Service) AddedItems() []ItemState { return s.Documents(AddedSourceID) }

// RemoveAdded forgets an item people added: it leaves the index; the pages
// that drew on it stay, and grow stale. An Admin or a SuperAdmin.
func (s *Service) RemoveAdded(ctx context.Context, c Caller, name string) error {
	if !c.MayAdd() {
		return wrapf(ErrDenied, "only an Admin or a SuperAdmin removes what was added to the wiki")
	}
	return s.removeDocument(ctx, c, AddedSourceID, name)
}

// SourceBrief is a source as the Wiki tab lists it: no address, account
// or credential, only what it is and how its last read went.
type SourceBrief struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Off      bool      `json:"off,omitempty"`
	Items    int       `json:"items"`
	LastRead time.Time `json:"lastRead,omitzero"`
	Error    string    `json:"error,omitempty"`
	Reported int       `json:"reported,omitempty"`
	// Skipped are the items it holds that Mycroft does not read, and why
	// (KN-25).
	Skipped    int    `json:"skipped,omitempty"`
	SkippedWhy string `json:"skippedWhy,omitempty"`
}

// WikiState is what the Mycroft tab's Wiki needs before anything else
// (MY-02, MY-03): whether a wiki is kept, and where; whether Mycroft has a
// model; what the person may do; the sources and what people added.
type WikiState struct {
	// Store is where the wiki is kept: "git", "s3", "configmap" (the
	// temporary store, CF-57), or "" for nowhere yet.
	Store    string `json:"store"`
	Model    bool   `json:"model"`
	Pages    int    `json:"pages"`
	Excerpts int    `json:"excerpts"`
	Running  bool   `json:"running"`
	// MayAdd, MayReview and MaySetup say what the person may do: add to
	// the wiki, review its changes, and set up its sources and store.
	MayAdd    bool          `json:"mayAdd"`
	MayReview bool          `json:"mayReview"`
	MaySetup  bool          `json:"maySetup"`
	Sources   []SourceBrief `json:"sources"`
	Added     []ItemState   `json:"added"`
	Limits    AddLimits     `json:"limits"`
	// Usage is the ConfigMap store's room, when the wiki is kept there:
	// a wiki of hundreds of pages fills it, and the Wiki says so before
	// (KN-37).
	Usage *StoreUsage `json:"usage,omitempty"`
}

// StoreUsage is how much of the ConfigMap wiki store is used, in bytes.
type StoreUsage struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

// AddLimits are the additions' limits, for the form to say.
type AddLimits struct {
	Note         int      `json:"note"`
	Document     int      `json:"document"`
	Depth        int      `json:"depth"`
	DefaultDepth int      `json:"defaultDepth"`
	Pages        int      `json:"pages"`
	DefaultPages int      `json:"defaultPages"`
	Formats      []string `json:"formats"`
}

// Wiki is the Wiki tab's state, for anyone who reads the wiki.
func (s *Service) Wiki(ctx context.Context, c Caller) (WikiState, error) {
	if !c.Reader {
		return WikiState{}, wrapf(ErrDenied, "sign in to the console to read the wiki")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return WikiState{}, err
	}
	st, err := s.Status(ctx)
	if err != nil {
		return WikiState{}, err
	}
	w := WikiState{Store: s.storeKind(set.Store), Model: s.o.Model != nil, Running: st.Running, MayAdd: c.MayAdd(), MayReview: c.Reviewer, MaySetup: c.SuperAdmin,
		Sources: []SourceBrief{}, Added: []ItemState{},
		Limits: AddLimits{Note: MaxNote, Document: MaxDocument, Depth: MaxCrawlDepth, DefaultDepth: DefaultCrawlDepth, Pages: MaxCrawlPages, DefaultPages: DefaultCrawlPages,
			Formats: []string{".pdf", ".docx", ".md", ".markdown", ".html", ".htm", ".txt", ".csv"}}}
	if s.o.Store != nil && w.Store == "" {
		w.Store = s.o.Store.Kind()
	}
	if w.Store == StoreConfigMap && s.cmStore != nil {
		// A failure to read it leaves the room unsaid, not the Wiki.
		if used, err := s.cmStore.Used(ctx); err == nil {
			w.Usage = &StoreUsage{Used: used, Limit: s.cmStore.limit}
		}
	}
	s.mu.RLock()
	for p := range s.files {
		switch {
		case strings.HasPrefix(p, PagesDir):
			w.Pages++
		case strings.HasPrefix(p, ExcerptsDir):
			w.Excerpts++
		}
	}
	m := s.manifest
	s.mu.RUnlock()
	for _, x := range set.Sources {
		b := SourceBrief{ID: x.ID, Name: x.Name, Kind: x.Kind, Off: x.Off}
		if ss, ok := st.Sources[x.ID]; ok {
			b.LastRead, b.Error, b.Reported = ss.LastRead, ss.Error, ss.Reported
			b.Skipped, b.SkippedWhy = ss.Skipped, ss.SkippedWhy
		}
		for _, it := range m.bySource(x.ID) {
			if !it.Gone {
				b.Items++
			}
		}
		w.Sources = append(w.Sources, b)
	}
	for _, it := range s.AddedItems() {
		// What the tab shows of an item: never its hosts, fleets or hash.
		w.Added = append(w.Added, ItemState{Source: it.Source, ID: it.ID, Title: it.Title, Version: it.Version, URL: it.URL, Modified: it.Modified, LastRead: it.LastRead, LastSeen: it.LastSeen, Pages: it.Pages, Reported: it.Reported, Corrects: it.Corrects, By: it.By})
	}
	return w, nil
}
