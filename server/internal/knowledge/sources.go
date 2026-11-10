package knowledge

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/confluence"
)

// Item is one thing a source holds: a page, a file, a document, a record
// of the console.
type Item struct {
	ID       string
	Title    string
	Version  string
	Modified time.Time
	URL      string
	// By is who added an item in Mycroft, and Corrects the page a
	// correction was written for (KN-23).
	By, Corrects string
	// Check is the agents' check the item is about, when it has one (a
	// fix's, KN-34): the fixes of that check are given with it first.
	Check string
}

// reader reads one source.
type reader interface {
	// List lists the items, those changed since a time when it is set;
	// complete says the listing is every item, so one missing is gone.
	List(ctx context.Context, since time.Time) (items []Item, complete bool, err error)
	// Read reads an item's text.
	Read(ctx context.Context, it Item) (string, error)
}

// Console is what the console itself knows, read through its own
// services with the agents' read-only rules: no Secret's value, no
// credential, no host key.
type Console interface {
	// Items are the console's records as text, one item each: its
	// clusters, nodes and hosts with their facts, fleets, Helm
	// applications and Automation scripts.
	Items(ctx context.Context) ([]ConsoleItem, error)
	// Hosts are the nodes and hosts the console knows, for the drift
	// check.
	Hosts(ctx context.Context) ([]ConsoleHost, error)
}

// ConsoleFleets is a Console that also lists the fleets of a cluster,
// each with its members now, so Mycroft's first import proposes only the
// fleets it lacks: not one of a name the console has, nor one whose
// labels select the same nodes as a fleet it has (KN-30).
type ConsoleFleets interface {
	Fleets(ctx context.Context, cluster string) ([]ConsoleFleet, error)
}

// ConsoleFleet is a fleet the console has, with the names of the nodes and
// hosts in it now.
type ConsoleFleet struct {
	Name    string
	Members []string
}

// ConsoleItem is one record of the console, as text.
type ConsoleItem struct {
	ID    string
	Title string
	Text  string
	// URL is where it is in the console.
	URL string
}

// ConsoleHost is a node or a host the console knows.
type ConsoleHost struct {
	Cluster string `json:"cluster"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	// Kind is "node" or "host".
	Kind string `json:"kind"`
	// Answering says it answered the console's last check: a node Ready,
	// a host reached.
	Answering bool `json:"answering"`
	// Labels are its labels now: what a fleet's labels select (KN-30).
	Labels map[string]string `json:"labels,omitempty"`
}

// readerFor makes a source's reader, with its saved credential.
func (s *Service) readerFor(ctx context.Context, x Source) (reader, error) {
	var c Creds
	if x.Kind == SourceConfluence || x.Kind == SourceGit || x.Kind == SourceS3 {
		var err error
		if c, err = s.creds(ctx, sourceKey(x.ID)); err != nil {
			return nil, err
		}
	}
	return s.readerWith(x, c)
}

// readerWith makes a source's reader with a credential: the saved one,
// or one typed in its dialog and not saved (KN-26).
func (s *Service) readerWith(x Source, c Creds) (reader, error) {
	switch x.Kind {
	case SourceConfluence:
		if c.Token == "" {
			return nil, invalid("no read token is saved for %s", x.Name)
		}
		hc := s.o.HTTP
		if x.CAPEM != "" {
			hc = nil
		}
		client, err := confluence.New(confluence.Config{BaseURL: x.BaseURL, Flavor: x.Flavor, Email: x.Email, Token: c.Token, CAPEM: x.CAPEM, HTTP: hc})
		if err != nil {
			return nil, invalid("%v", err)
		}
		return &confluenceReader{c: client, key: x.Space}, nil
	case SourceGit:
		return &gitReader{g: newRepo(repoDir(s.dir(), "source", x.URL, x.Branch), x.URL, x.Branch, x.Username, c.Token, x.CAPEM), dir: strings.Trim(x.Path, "/"), web: x.URL}, nil
	case SourceS3:
		return s.bucketReader(x, c)
	case SourceUpload:
		return &uploadReader{s: s, source: x.ID}, nil
	case SourceConsole:
		if s.o.Console == nil {
			return nil, errors.New("the console's own records are not offered here")
		}
		return &consoleReader{c: s.o.Console}, nil
	case SourceFixes:
		return &fixesReader{s: s}, nil
	}
	return nil, invalid("a source is confluence, git, s3, upload or console")
}

// confluenceReader reads a Confluence space's pages.
type confluenceReader struct {
	c     *confluence.Client
	key   string
	space *confluence.Space
}

func (r *confluenceReader) List(ctx context.Context, since time.Time) ([]Item, bool, error) {
	if r.space == nil {
		sp, err := r.c.Space(ctx, r.key)
		if err != nil {
			return nil, false, err
		}
		r.space = &sp
	}
	pages, err := r.c.Pages(ctx, *r.space, since)
	if err != nil {
		return nil, false, err
	}
	out := make([]Item, 0, len(pages))
	for _, p := range pages {
		out = append(out, Item{ID: p.ID, Title: p.Title, Version: strconv.Itoa(p.Version), Modified: p.Modified, URL: p.URL})
	}
	return out, since.IsZero(), nil
}

func (r *confluenceReader) Read(ctx context.Context, it Item) (string, error) {
	p, err := r.c.Page(ctx, it.ID)
	if err != nil {
		return "", err
	}
	return confluence.Text(p.Body), nil
}

// gitReader reads a repository's documents at its branch's head.
type gitReader struct {
	g   *repo
	dir string
	web string
}

func (r *gitReader) List(ctx context.Context, _ time.Time) ([]Item, bool, error) {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	_, head, err := r.g.fetch(ctx)
	if err != nil {
		return nil, false, err
	}
	if head == nil {
		return nil, true, nil
	}
	es, err := entries(head)
	if err != nil {
		return nil, false, err
	}
	var out []Item
	for p, e := range es {
		if r.dir != "" && !strings.HasPrefix(p, r.dir+"/") {
			continue
		}
		if !Readable(p) {
			continue
		}
		out = append(out, Item{ID: p, Title: path.Base(p), Version: e.hash.String(), Modified: head.Committer.When, URL: strings.TrimSuffix(r.web, ".git") + "/blob/" + r.g.branch + "/" + p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, true, nil
}

func (r *gitReader) Read(ctx context.Context, it Item) (string, error) {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	rp, err := r.g.open()
	if err != nil {
		return "", err
	}
	h, err := hashFrom(it.Version)
	if err != nil {
		return "", err
	}
	b, err := readBlob(rp, h)
	if err != nil {
		return "", err
	}
	return Extract(it.ID, b)
}

// consoleReader reads the console's own records.
type consoleReader struct {
	c     Console
	items map[string]ConsoleItem
}

func (r *consoleReader) List(ctx context.Context, _ time.Time) ([]Item, bool, error) {
	list, err := r.c.Items(ctx)
	if err != nil {
		return nil, false, err
	}
	r.items = map[string]ConsoleItem{}
	out := make([]Item, 0, len(list))
	for _, ci := range list {
		r.items[ci.ID] = ci
		out = append(out, Item{ID: ci.ID, Title: ci.Title, Version: hashOf([]byte(ci.Text))[:16], URL: ci.URL})
	}
	return out, true, nil
}

func (r *consoleReader) Read(_ context.Context, it Item) (string, error) {
	ci, ok := r.items[it.ID]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, it.ID)
	}
	return ci.Text, nil
}

// pendingDoc is an uploaded document read but not yet given to Mycroft:
// its text is held only until then (raw copies are not kept).
type pendingDoc struct {
	source, name string
	text         string
	hash         string
	at           time.Time
	by           string
	// title and url, when set, are a web page's or a note's (MY-04):
	// what its excerpt and the pages drawing on it name.
	title, url string
	// corrects is the page a correction is for (KN-23).
	corrects string
}

// label is how the document is named: its title, or its file's name.
func (p pendingDoc) label() string {
	if p.title != "" {
		return p.title
	}
	return p.name
}

// uploadReader lists the documents uploaded to a source: those waiting,
// and those already read, which stay seen while they are listed.
type uploadReader struct {
	s      *Service
	source string
}

func (r *uploadReader) List(context.Context, time.Time) ([]Item, bool, error) {
	r.s.pendingMu.Lock()
	defer r.s.pendingMu.Unlock()
	r.s.mu.RLock()
	m := r.s.manifest
	r.s.mu.RUnlock()
	seen := map[string]bool{}
	var out []Item
	for _, p := range r.s.pending {
		if p.source == r.source {
			out = append(out, Item{ID: p.name, Title: p.label(), Version: p.hash[:16], Modified: p.at, URL: p.url, By: p.by, Corrects: p.corrects})
			seen[p.name] = true
		}
	}
	for _, st := range m.bySource(r.source) {
		if !seen[st.ID] && !st.Gone {
			out = append(out, Item{ID: st.ID, Title: st.Title, Version: st.Version, Modified: st.Modified, URL: st.URL, By: st.By, Corrects: st.Corrects})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, true, nil
}

func (r *uploadReader) Read(_ context.Context, it Item) (string, error) {
	r.s.pendingMu.Lock()
	defer r.s.pendingMu.Unlock()
	if p, ok := r.s.pending[r.source+"/"+it.ID]; ok {
		return p.text, nil
	}
	return "", fmt.Errorf("%w: %s must be uploaded again to be read", ErrNotFound, it.ID)
}
