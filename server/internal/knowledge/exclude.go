package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
)

// Exclusions (0.12.1, KN-45). A SuperAdmin excludes an item of a web
// site, a Confluence space, a repository or a bucket from AIOps › Mycroft
// › Sources: its excerpts are emptied at once (as a removed document's
// are, its manifest entry marked gone and the index read again) and it
// joins its source's Excluded list, with who, when and why, kept with the
// source's settings. Every read of the source leaves out what is
// excluded: the crawl of a web site does not fetch an excluded page, nor
// follow the links found only on it. An exclusion is one item, by its id,
// or a pattern: a glob with * (a web site's /blog/*), a prefix that ends
// in / (a repository's folder, a bucket's prefix), or a Confluence page
// and its children. Include again takes it off the list; the item is read
// again at the source's next read (its Wiki sync job's next run, or Run
// now). The wiki's pages that drew on an excluded item are not changed:
// the answer names them, for a person to review or to ask Mycroft to
// rewrite (MY-14). Uploaded documents and notes are not excluded but
// removed.

// Exclusion is one item, or a pattern of items, a source does not read.
type Exclusion struct {
	// Pattern is an item's id, a glob with *, or a prefix ending in /.
	Pattern string `json:"pattern"`
	// Under is a Confluence page and its children.
	Under bool `json:"under,omitempty"`
	// Title is the item's title, when it is one.
	Title  string    `json:"title,omitempty"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// MaxExclusions bounds a source's exclusions; a reason is up to
// maxReason, as a request's.
const MaxExclusions = 200

// excludable says whether a source's items may be excluded: those read
// from somewhere else, again at every read.
func excludable(kind string) bool {
	return kind == SourceWeb || kind == SourceConfluence || kind == SourceGit || kind == SourceS3
}

// matches says whether an exclusion covers an item, by its id and, for a
// Confluence page, its ancestors' ids.
func (e Exclusion) matches(kind, id string, ancestors []string) bool {
	switch {
	case e.Pattern == "":
		return false
	case kind == SourceConfluence:
		return id == e.Pattern || e.Under && slices.Contains(ancestors, e.Pattern)
	case strings.Contains(e.Pattern, "*"):
		return globPattern(e.Pattern).MatchString(id)
	case strings.HasSuffix(e.Pattern, "/"):
		return strings.HasPrefix(id, e.Pattern)
	}
	return id == e.Pattern
}

// globPattern makes a glob a whole-id pattern: * any characters.
func globPattern(p string) *regexp.Regexp {
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

// matchExclusion is the exclusion that covers an item, if any.
func matchExclusion(kind string, list []Exclusion, id string, ancestors []string) (Exclusion, bool) {
	for _, e := range list {
		if e.matches(kind, id, ancestors) {
			return e, true
		}
	}
	return Exclusion{}, false
}

// ancestorsOf are a Confluence page's ancestors' ids, nearest first, from
// the parents known.
func ancestorsOf(id string, parent map[string]string) []string {
	var out []string
	for p := parent[id]; p != "" && len(out) < 50 && !slices.Contains(out, p); p = parent[p] {
		out = append(out, p)
	}
	return out
}

// parentsOf are the parents of a source's pages as the manifest knows
// them, and as a listing gives them.
func parentsOf(m *Manifest, source string, items []Item) map[string]string {
	out := map[string]string{}
	for _, st := range m.bySource(source) {
		if st.Parent != "" {
			out[st.ID] = st.Parent
		}
	}
	for _, it := range items {
		if it.Parent != "" {
			out[it.ID] = it.Parent
		}
	}
	return out
}

// itemRoot is where a source's items are read from, as their ids begin
// (0.12.1, KN-49): a bucket's prefix, a repository's path, the directory
// of a web site's start address ("/" for the whole site); "" for a whole
// bucket or repository. A bucket's prefix that does not end in / is its
// folder when the id is under it, else the folder that holds it.
func itemRoot(x Source, id string) string {
	switch x.Kind {
	case SourceS3:
		p := x.Prefix
		switch {
		case p == "", strings.HasSuffix(p, "/"):
			return p
		case strings.HasPrefix(id, p+"/"):
			return p + "/"
		}
		return p[:strings.LastIndex(p, "/")+1]
	case SourceGit:
		if d := strings.Trim(x.Path, "/"); d != "" {
			return d + "/"
		}
	case SourceWeb:
		u, err := crawlStart(x.URL)
		if err != nil {
			return "/"
		}
		p := u.EscapedPath()
		if !strings.HasSuffix(p, "/") {
			p = path.Dir(p)
		}
		return strings.TrimSuffix(p, "/") + "/"
	}
	return ""
}

// RelPath is an item's id, or an exclusion's pattern, as Mycroft ›
// Sources shows it: relative to its source's root (KN-49). A web site's
// keeps its leading /, and a page outside the start address's directory
// is shown with its host; "" for a Confluence page (shown by its title)
// and every other kind.
func RelPath(x Source, id string) string {
	switch x.Kind {
	case SourceS3, SourceGit:
		if r := itemRoot(x, id); r != "" && strings.HasPrefix(id, r) && len(id) > len(r) {
			return id[len(r):]
		}
		return id
	case SourceWeb:
		if strings.HasPrefix(id, "*") {
			return id
		}
		r := itemRoot(x, id)
		if strings.HasPrefix(id, r) {
			return "/" + id[len(r):]
		}
		if u, err := crawlStart(x.URL); err == nil {
			return u.Host + id
		}
		return id
	}
	return ""
}

// Shown is an item's id, or a pattern, with its source's name leading,
// as Mycroft › Sources shows it ("docs › runbooks/a.md"); "" when it is
// not shown by a path.
func Shown(x Source, id string) string {
	if p := RelPath(x, id); p != "" {
		return x.Name + " › " + p
	}
	return ""
}

// UnderPattern is the pattern of everything under an item's place, as a
// row offers it (Exclude everything under …), and its words, relative to
// the source's root; "" when the item's place is the source's root (or
// above it): a whole source is turned off in Settings › Knowledge, not
// excluded (KN-49). The pattern keeps the full id, as exclusions do.
func UnderPattern(x Source, id string) (pattern string, under bool, words string) {
	var dir string
	switch x.Kind {
	case SourceConfluence:
		return id, true, "this page and its children"
	case SourceWeb:
		p, _, _ := strings.Cut(id, "?")
		dir = path.Dir(p)
		if strings.HasSuffix(p, "/") {
			dir = strings.TrimSuffix(p, "/")
		}
	case SourceGit, SourceS3:
		dir = path.Dir(id)
	default:
		return "", false, ""
	}
	if dir == "/" || dir == "." || dir == "" || strings.HasPrefix(itemRoot(x, id), dir+"/") {
		return "", false, ""
	}
	pattern = dir + "/"
	words = Shown(x, pattern)
	if x.Kind == SourceWeb {
		pattern += "*"
	}
	return pattern, false, words
}

// ExcludeInput is what Exclude is asked: an item, or a pattern, and why.
type ExcludeInput struct {
	// Item is one item's id; Pattern a pattern (Under for a Confluence
	// page and its children). One of them.
	Item    string `json:"item,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Under   bool   `json:"under,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// PageBrief is a wiki page by its path and title.
type PageBrief struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// ExcludeResult is what an exclusion did: how many items it emptied, and
// the wiki's pages that drew on them.
type ExcludeResult struct {
	Exclusion Exclusion   `json:"exclusion"`
	Items     int         `json:"items"`
	Pages     []PageBrief `json:"pages"`
	Text      string      `json:"text"`
}

// Exclude excludes an item, or a pattern of items, of a source (KN-45):
// a SuperAdmin's.
func (s *Service) Exclude(ctx context.Context, c Caller, source string, in ExcludeInput) (ExcludeResult, error) {
	if !c.SuperAdmin {
		return ExcludeResult{}, wrapf(ErrDenied, "only a SuperAdmin excludes a source's items")
	}
	if err := s.leads(); err != nil {
		return ExcludeResult{}, err
	}
	e := Exclusion{Pattern: strings.TrimSpace(in.Pattern), Under: in.Under, Reason: strings.TrimSpace(in.Reason), By: c.label(), At: s.o.Now().UTC()}
	if in.Item != "" {
		// One item; with Under, a Confluence page and its children.
		e.Pattern = in.Item
	}
	switch {
	case e.Pattern == "" || len(e.Pattern) > 2000 || !utf8.ValidString(e.Pattern):
		return ExcludeResult{}, invalid("say which item or pattern to exclude")
	case utf8.RuneCountInString(e.Reason) > maxReason:
		return ExcludeResult{}, invalid("a reason is up to %d characters", maxReason)
	}
	var x Source
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == source })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", source)
		}
		x = set.Sources[i]
		if !excludable(x.Kind) {
			if x.Kind == SourceUpload {
				return invalid("an uploaded document or note is removed, not excluded")
			}
			return invalid("only a web site's, a Confluence space's, a repository's or a bucket's items are excluded")
		}
		if e.Under && x.Kind != SourceConfluence {
			return invalid("a page and its children are a Confluence space's; give a pattern instead")
		}
		if slices.ContainsFunc(x.Excluded, func(o Exclusion) bool { return o.Pattern == e.Pattern && o.Under == e.Under }) {
			return conflictf("%s is excluded already", e.Pattern)
		}
		if len(x.Excluded) >= MaxExclusions {
			return invalid("a source has at most %d exclusions: exclude a pattern instead", MaxExclusions)
		}
		if in.Item != "" {
			s.mu.RLock()
			if st := s.manifest.Items[itemKey(source, in.Item)]; st != nil {
				e.Title = clip(st.Title, 200)
			}
			s.mu.RUnlock()
		}
		set.Sources[i].Excluded = append(set.Sources[i].Excluded, e)
		x = set.Sources[i]
		return nil
	})
	if err != nil {
		return ExcludeResult{}, err
	}
	res := ExcludeResult{Exclusion: e, Pages: []PageBrief{}}
	n, pages, err := s.emptyExcluded(ctx, x, e)
	res.Items, res.Pages = n, pages
	what := e.Pattern
	if e.Under {
		what += " and its children"
	}
	detail := fmt.Sprintf("%s excluded from %s (%d items emptied)", what, x.ID, n)
	if e.Reason != "" {
		detail += ": " + clip(e.Reason, 200)
	}
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeFailed
		detail += "; its excerpts could not be emptied yet, the next read leaves them out: " + clip(err.Error(), 200)
	}
	s.audit(audit.Event{Action: ActionExclude, Who: c.who(), Target: x.ID, Outcome: outcome, Detail: detail, From: c.From})
	res.Text = excludeReport(res, x)
	if err != nil {
		res.Text += " Its excerpts could not be emptied now (" + clip(err.Error(), 200) + "): the next read of the source leaves them out."
	}
	return res, nil
}

func excludeReport(r ExcludeResult, x Source) string {
	var b strings.Builder
	switch r.Items {
	case 0:
		fmt.Fprintf(&b, "Excluded from %s: the agents quote nothing of it, and its next read leaves it out.", x.Name)
	case 1:
		fmt.Fprintf(&b, "1 item of %s was excluded: the agents no longer quote it, and its reads leave it out.", x.Name)
	default:
		fmt.Fprintf(&b, "%d items of %s were excluded: the agents no longer quote them, and its reads leave them out.", r.Items, x.Name)
	}
	if n := len(r.Pages); n > 0 {
		fmt.Fprintf(&b, " %d wiki %s drew on it and %s not changed: review %s.", n, plural(n, "page", "pages"), plural(n, "is", "are"), plural(n, "it", "them"))
	}
	return b.String()
}

// emptyExcluded empties the excerpts of a source's items an exclusion
// covers, at once: each is marked gone in the manifest, its excerpt
// emptied, and the index read again. It says how many, and the pages that
// drew on them.
func (s *Service) emptyExcluded(ctx context.Context, x Source, e Exclusion) (int, []PageBrief, error) {
	store, _, err := s.currentStore(ctx)
	if err != nil {
		return 0, nil, err
	}
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	s.mu.RLock()
	m := s.manifest.clone()
	s.mu.RUnlock()
	parents := parentsOf(m, x.ID, nil)
	var writes []Change
	var drawn []string
	n := 0
	for _, st := range m.bySource(x.ID) {
		if st.Gone || !e.matches(x.Kind, st.ID, ancestorsOf(st.ID, parents)) {
			continue
		}
		st.Gone = true
		// Read again in whole if it is included again: its excerpt is
		// emptied now.
		wasRead := st.Hash != ""
		st.Hash, st.Version = "", ""
		if st.Skipped != "" || !wasRead {
			// A record of an item never read holds no excerpt.
			continue
		}
		n++
		key := itemKey(x.ID, st.ID)
		b, _ := json.Marshal(Excerpt{Key: key, Chunks: []string{}})
		writes = append(writes, Change{Path: excerptPath(key), Body: b})
		for _, p := range st.Pages {
			if !slices.Contains(drawn, p) {
				drawn = append(drawn, p)
			}
		}
	}
	if len(writes) == 0 {
		return 0, s.pageBriefs(drawn), nil
	}
	writes = append(writes, Change{Path: ManifestPath, Body: m.encode()})
	if err := store.Write(ctx, writes, fmt.Sprintf("Mycroft leaves out %d excluded items of %s", n, x.Name)); err != nil {
		return 0, nil, err
	}
	if err := s.Refresh(ctx); err != nil {
		s.o.Logger.Warn("reading the wiki after an exclusion failed", "err", err)
	}
	return n, s.pageBriefs(drawn), nil
}

// pageBriefs are pages by path, with their titles where the wiki has
// them.
func (s *Service) pageBriefs(paths []string) []PageBrief {
	out := make([]PageBrief, 0, len(paths))
	for _, p := range paths {
		b := PageBrief{Path: p, Title: strings.TrimSuffix(path.Base(p), ".md")}
		if pg, ok := s.PageAt(p); ok {
			b.Title = pg.Title
		}
		out = append(out, b)
	}
	return out
}

// Include takes an exclusion off a source's list (KN-45): what it
// covered is read again at the source's next read. A SuperAdmin's.
func (s *Service) Include(ctx context.Context, c Caller, source, pattern string, under bool) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin includes a source's items again")
	}
	if err := s.leads(); err != nil {
		return err
	}
	var name string
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == source })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", source)
		}
		j := slices.IndexFunc(set.Sources[i].Excluded, func(o Exclusion) bool { return o.Pattern == pattern && o.Under == under })
		if j < 0 {
			return wrapf(ErrNotFound, "%s is not excluded from %s", pattern, set.Sources[i].Name)
		}
		set.Sources[i].Excluded = slices.Delete(set.Sources[i].Excluded, j, j+1)
		name = set.Sources[i].Name
		return nil
	})
	if err != nil {
		return err
	}
	what := pattern
	if under {
		what += " and its children"
	}
	s.audit(audit.Event{Action: ActionInclude, Who: c.who(), Target: source, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s included again in %s (%s): read at its next read", what, source, name), From: c.From})
	return nil
}
