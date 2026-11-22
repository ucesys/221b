package knowledge

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
)

// --- What Mycroft draws on (0.12.1, KN-47, MY-12 to MY-14) ---
//
// AIOps › Mycroft › Sources lists every item the agents can draw on, a page
// at a time (a source may hold thousands): the uploaded documents and
// notes, a web site's pages, Confluence pages, a repository's files, a
// bucket's objects, the console's records and the fixes, and the live
// sources' addresses, which are looked up when asked and never copied.
// Each says its state: being read, read, its last read failed (and why),
// left out by a read (and why), or excluded (KN-45). Its details give the
// first excerpts, the wiki's pages that drew on it (the manifest records
// them for every item, ItemState.Pages) and a link to the original. Admins
// and SuperAdmins see them; a SuperAdmin excludes and includes again; an
// Admin or a SuperAdmin removes an uploaded document or note.
//
// Administration › AI › Knowledge's Uploaded documents (MY-12), and an Admin's card
// in Administration, list the notes and documents of both sources of
// uploaded documents together: "Uploaded documents" (once a SuperAdmin's
// Upload) and "Added in Mycroft" (once Add). Nothing is moved between them,
// so every page keeps the source it cites; new ones go to Added in
// Mycroft, and a document of a name replaces the one of that name in
// either.

// The types of item.
const (
	TypeNote           = "note"
	TypeDocument       = "document"
	TypeWebPage        = "webPage"
	TypeConfluencePage = "confluencePage"
	TypeGitFile        = "gitFile"
	TypeS3Object       = "s3Object"
	TypeRecord         = "record"
	TypeFix            = "fix"
	TypeLiveAddress    = "liveAddress"
)

// The states of an item.
const (
	StateReading  = "reading"
	StateRead     = "read"
	StateError    = "error"
	StateSkipped  = "skipped"
	StateExcluded = "excluded"
	StateLive     = "live"
)

// UploadsSource is the filter that lists both sources of uploaded
// documents together.
const UploadsSource = "uploads"

// typeOf is what an item is, by its source's kind and, for what people
// added, what they added.
func typeOf(kind string, st *ItemState) string {
	switch kind {
	case SourceWeb:
		return TypeWebPage
	case SourceConfluence:
		return TypeConfluencePage
	case SourceGit:
		return TypeGitFile
	case SourceS3:
		return TypeS3Object
	case SourceConsole:
		return TypeRecord
	case SourceFixes:
		return TypeFix
	case SourceLive:
		return TypeLiveAddress
	}
	switch st.Type {
	case AddText:
		return TypeNote
	case AddDocument:
		return TypeDocument
	}
	// Added before its type was kept: told by what it holds and its name.
	switch {
	case st.Source == AddedSourceID && st.URL != "":
		return TypeWebPage
	case st.Corrects != "", st.Source == AddedSourceID && strings.HasSuffix(st.ID, ".md") && (strings.HasPrefix(st.ID, "note ") || strings.HasPrefix(st.ID, "correction ")):
		return TypeNote
	}
	return TypeDocument
}

// stateOf is an item's state, and why when it is not read.
func stateOf(st *ItemState) (string, string) {
	switch {
	case st.Skipped != "":
		return StateSkipped, st.Skipped
	case st.Error != "":
		return StateError, st.Error
	case st.Hash == "" && st.Version == "":
		return StateReading, "read again at the source's next read"
	}
	return StateRead, ""
}

// UploadItem is a note or a document of the uploaded documents (MY-12).
type UploadItem struct {
	Source   string    `json:"source"`
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Title    string    `json:"title"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at,omitzero"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	LastRead time.Time `json:"lastRead,omitzero"`
	Pages    []string  `json:"pages,omitempty"`
	Corrects string    `json:"corrects,omitempty"`
	Reported string    `json:"reported,omitempty"`
}

// SourceRefBrief names a source by its id and name.
type SourceRefBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UploadsView is Administration › AI › Knowledge's Uploaded documents (MY-12).
type UploadsView struct {
	Items    []UploadItem `json:"items"`
	Limits   AddLimits    `json:"limits"`
	MayAdd   bool         `json:"mayAdd"`
	MaySetup bool         `json:"maySetup"`
	Store    string       `json:"store"`
	Model    bool         `json:"model"`
	// Off are the sources of uploaded documents switched off: their
	// documents are not read, and nothing is added while Added in Mycroft
	// is off; a SuperAdmin switches them on here.
	Off []SourceRefBrief `json:"off"`
}

func addLimits() AddLimits {
	return AddLimits{Note: MaxNote, Document: MaxDocument, Depth: MaxCrawlDepth, DefaultDepth: DefaultCrawlDepth, Pages: MaxCrawlPages, DefaultPages: DefaultCrawlPages,
		Formats: []string{".pdf", ".docx", ".md", ".markdown", ".html", ".htm", ".txt", ".csv"}}
}

// Uploads lists the notes and documents of both sources of uploaded
// documents, newest first (MY-12): an Admin's or a SuperAdmin's.
func (s *Service) Uploads(ctx context.Context, c Caller) (UploadsView, error) {
	if !c.MayAdd() {
		return UploadsView{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin sees the uploaded documents")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return UploadsView{}, err
	}
	v := UploadsView{Items: []UploadItem{}, Limits: addLimits(), MayAdd: true, MaySetup: c.SuperAdmin, Store: s.storeKind(set.Store), Model: s.o.Model != nil, Off: []SourceRefBrief{}}
	if s.o.Store != nil && v.Store == "" {
		v.Store = s.o.Store.Kind()
	}
	for _, x := range set.Sources {
		if x.Kind != SourceUpload {
			continue
		}
		if x.Off {
			v.Off = append(v.Off, SourceRefBrief{ID: x.ID, Name: x.Name})
		}
		for _, it := range s.uploadRows(x.ID) {
			if it.Type == TypeNote || it.Type == TypeDocument {
				v.Items = append(v.Items, it)
			}
		}
	}
	sort.SliceStable(v.Items, func(i, j int) bool { return v.Items[i].At.After(v.Items[j].At) })
	return v, nil
}

// uploadRows are a source of uploaded documents' items: those waiting to
// be read, and those the manifest holds, a failed read's too.
func (s *Service) uploadRows(source string) []UploadItem {
	s.mu.RLock()
	m := s.manifest
	s.mu.RUnlock()
	var out []UploadItem
	seen := map[string]bool{}
	s.pendingMu.Lock()
	for _, p := range s.pending {
		if p.source != source {
			continue
		}
		st := &ItemState{Source: source, ID: p.name, URL: p.url, Corrects: p.corrects, Type: p.kind}
		out = append(out, UploadItem{Source: source, ID: p.name, Type: typeOf(SourceUpload, st), Title: p.label(), By: p.by, At: p.at, State: StateReading, Corrects: p.corrects})
		seen[p.name] = true
	}
	s.pendingMu.Unlock()
	for _, st := range m.bySource(source) {
		if seen[st.ID] || st.Gone {
			continue
		}
		state, why := stateOf(st)
		it := UploadItem{Source: source, ID: st.ID, Type: typeOf(SourceUpload, st), Title: st.Title, By: st.By, At: st.Modified, State: state, LastRead: st.LastRead, Pages: st.Pages, Corrects: st.Corrects, Reported: st.Reported}
		if it.At.IsZero() {
			it.At = st.LastRead
		}
		if state == StateError {
			it.Error = why
		}
		out = append(out, it)
	}
	return out
}

// RemoveResult is what removing an uploaded document or note did: the
// wiki's pages that drew on it, which are not changed (MY-14).
type RemoveResult struct {
	Removed bool        `json:"removed"`
	Pages   []PageBrief `json:"pages"`
}

// RemoveUpload removes a note or a document of either source of uploaded
// documents (MY-12): an Admin's or a SuperAdmin's. It is final: it
// leaves the index at once.
func (s *Service) RemoveUpload(ctx context.Context, c Caller, source, name string) (RemoveResult, error) {
	if !c.MayAdd() {
		return RemoveResult{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin removes an uploaded document or note")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return RemoveResult{}, err
	}
	if x, ok := set.source(source); !ok || x.Kind != SourceUpload {
		return RemoveResult{}, wrapf(ErrNotFound, "no source of uploaded documents %q", source)
	}
	var pages []string
	s.mu.RLock()
	if st := s.manifest.Items[itemKey(source, name)]; st != nil {
		pages = slices.Clone(st.Pages)
	}
	s.mu.RUnlock()
	if err := s.removeDocument(ctx, c, source, name); err != nil {
		return RemoveResult{}, err
	}
	return RemoveResult{Removed: true, Pages: s.pageBriefs(pages)}, nil
}

// SourceItem is one item the agents can draw on, as AIOps › Mycroft ›
// Sources lists it (KN-47).
type SourceItem struct {
	Source     string     `json:"source"`
	SourceName string     `json:"sourceName"`
	Kind       string     `json:"kind"`
	Type       string     `json:"type"`
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	URL        string     `json:"url,omitempty"`
	By         string     `json:"by,omitempty"`
	At         time.Time  `json:"at,omitzero"`
	LastRead   time.Time  `json:"lastRead,omitzero"`
	State      string     `json:"state"`
	Why        string     `json:"why,omitempty"`
	Pages      int        `json:"pages,omitempty"`
	Exclusion  *Exclusion `json:"exclusion,omitempty"`
	Excludable bool       `json:"excludable,omitempty"`
	// Path is where the item is in its source, as the list shows it after
	// the source's name: relative to the bucket's prefix, the repository's
	// path or the web site's start address (KN-49); "" for what is shown
	// by its title. ID stays the full key.
	Path string `json:"path,omitempty"`
	// Rule is an exclusion's pattern as the list shows it, with the
	// source's name, when it is a pattern.
	Rule string `json:"rule,omitempty"`
	// Under is the pattern of everything under the item's place, and
	// UnderLabel its words, for Exclude everything under …; none at the
	// source's root.
	Under      string `json:"under,omitempty"`
	UnderLabel string `json:"underLabel,omitempty"`
	// key is the manifest's key of the item.
	key string
}

// ItemSource is a source as the items' page names it: UploadsSource,
// which stands for both sources of uploaded documents, then every source
// by itself.
type ItemSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Off  bool   `json:"off,omitempty"`
}

// ItemsQuery is a page of the items: by source (an id, or UploadsSource),
// type, state and words, from Offset, Limit of them.
type ItemsQuery struct {
	Source, Type, State, Q string
	Offset, Limit          int
}

// The items' pages.
const (
	DefaultItemsLimit = 50
	MaxItemsLimit     = 200
)

// ItemsPage is a page of the items, with how many match in all.
type ItemsPage struct {
	Items      []SourceItem `json:"items"`
	Total      int          `json:"total"`
	Offset     int          `json:"offset"`
	Limit      int          `json:"limit"`
	Sources    []ItemSource `json:"sources"`
	MayExclude bool         `json:"mayExclude"`
	MayRemove  bool         `json:"mayRemove"`
}

// Items lists a page of the items the agents can draw on (KN-47): an
// Admin's or a SuperAdmin's.
func (s *Service) Items(ctx context.Context, c Caller, q ItemsQuery) (ItemsPage, error) {
	if !c.MayAdd() {
		return ItemsPage{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin sees the sources' items")
	}
	if q.Limit <= 0 {
		q.Limit = DefaultItemsLimit
	}
	q.Limit, q.Offset = min(q.Limit, MaxItemsLimit), max(q.Offset, 0)
	set, err := s.Settings(ctx)
	if err != nil {
		return ItemsPage{}, err
	}
	out := ItemsPage{Items: []SourceItem{}, Offset: q.Offset, Limit: q.Limit, MayExclude: c.SuperAdmin, MayRemove: true,
		Sources: []ItemSource{{ID: UploadsSource, Name: "Uploaded documents", Kind: UploadsSource}}}
	// Then every source by itself, those of uploaded documents too: a
	// source opened in Mycroft › Sources names itself (KN-50).
	for _, x := range set.Sources {
		out.Sources = append(out.Sources, ItemSource{ID: x.ID, Name: x.Name, Kind: x.Kind, Off: x.Off})
	}
	words := strings.Fields(strings.ToLower(q.Q))
	var kept []SourceItem
	for _, it := range s.allItems(set) {
		switch {
		case q.Source == UploadsSource && it.Kind != SourceUpload,
			q.Source != "" && q.Source != UploadsSource && it.Source != q.Source,
			q.Type != "" && it.Type != q.Type,
			q.State != "" && it.State != q.State:
			continue
		}
		hay := strings.ToLower(it.Title + " " + it.ID + " " + it.URL + " " + it.By)
		if it.Path != "" {
			hay += " " + strings.ToLower(it.SourceName+" › "+it.Path)
		}
		if !allIn(hay, words) {
			continue
		}
		kept = append(kept, it)
	}
	out.Total = len(kept)
	if q.Offset < len(kept) {
		out.Items = kept[q.Offset:min(len(kept), q.Offset+q.Limit)]
	}
	return out, nil
}

func allIn(hay string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// allItems are every source's items, in the sources' order, each by
// title.
func (s *Service) allItems(set Settings) []SourceItem {
	s.mu.RLock()
	m := s.manifest
	s.mu.RUnlock()
	// The web pages people added before 0.12.1 are their web site's
	// until its first read reads them again (KN-46).
	migrated := map[string]Source{}
	for _, x := range set.Sources {
		for _, id := range x.MigratedFrom {
			migrated[id] = x
		}
	}
	var out []SourceItem
	for _, x := range set.Sources {
		var rows []SourceItem
		switch x.Kind {
		case SourceLive:
			for _, a := range x.Addresses {
				title := a.URL
				if a.Kind == LiveConfluenceSearch {
					title = a.URL + " (space " + a.Space + ")"
				}
				rows = append(rows, SourceItem{Source: x.ID, SourceName: x.Name, Kind: x.Kind, Type: TypeLiveAddress, ID: a.URL, Title: title, URL: a.URL, State: StateLive, Why: "looked up when asked, never copied"})
			}
		case SourceUpload:
			for _, u := range s.uploadRows(x.ID) {
				it := SourceItem{Source: x.ID, SourceName: x.Name, Kind: x.Kind, Type: u.Type, ID: u.ID, Title: u.Title, By: u.By, At: u.At, LastRead: u.LastRead, State: u.State, Why: u.Error, Pages: len(u.Pages), key: itemKey(x.ID, u.ID)}
				if w, ok := migrated[u.ID]; ok && x.ID == AddedSourceID {
					it.Source, it.SourceName, it.Kind = w.ID, w.Name, w.Kind
					it.Why = "read when it was added in Mycroft; read again at this web site's first read"
				}
				if st := m.Items[it.key]; st != nil {
					it.URL = st.URL
				}
				rows = append(rows, it)
			}
		default:
			parents := parentsOf(m, x.ID, nil)
			matched := map[int]bool{}
			for _, st := range m.bySource(x.ID) {
				it := SourceItem{Source: x.ID, SourceName: x.Name, Kind: x.Kind, Type: typeOf(x.Kind, st), ID: st.ID, Title: st.Title, URL: st.URL, By: st.By, At: st.Modified, LastRead: st.LastRead, Pages: len(st.Pages), Path: RelPath(x, st.ID), key: itemKey(x.ID, st.ID)}
				if it.At.IsZero() {
					it.At = st.LastRead
				}
				if excludable(x.Kind) {
					it.Excludable = true
					it.Under, _, it.UnderLabel = UnderPattern(x, st.ID)
					for i, e := range x.Excluded {
						if e.matches(x.Kind, st.ID, ancestorsOf(st.ID, parents)) {
							it.Exclusion, matched[i] = &e, true
							break
						}
					}
				}
				switch {
				case it.Exclusion != nil:
					it.State = StateExcluded
					it.Why, it.Rule = exclusionWords(x, *it.Exclusion), ruleOf(x, *it.Exclusion)
				case st.Gone && st.Skipped == "":
					continue
				default:
					it.State, it.Why = stateOf(st)
				}
				rows = append(rows, it)
			}
			// An exclusion that covers no item known is listed itself, so
			// it can be included again.
			for i, e := range x.Excluded {
				if matched[i] {
					continue
				}
				rel := RelPath(x, e.Pattern)
				title := cmp.Or(rel, e.Pattern)
				if e.Title != "" {
					title = e.Title
				}
				if e.Under {
					title += " and its children"
				}
				rows = append(rows, SourceItem{Source: x.ID, SourceName: x.Name, Kind: x.Kind, Type: typeOf(x.Kind, &ItemState{}), ID: e.Pattern, Title: title, Path: rel, Rule: ruleOf(x, e), State: StateExcluded, Why: exclusionWords(x, e), Exclusion: &e, Excludable: true, At: e.At})
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Title) < strings.ToLower(rows[j].Title) })
		out = append(out, rows...)
	}
	return out
}

// isPattern says whether an exclusion is a pattern, not one item.
func (e Exclusion) isPattern() bool {
	return e.Pattern != "" && !e.Item && (strings.Contains(e.Pattern, "*") || strings.HasSuffix(e.Pattern, "/") || e.Under)
}

// ruleOf is an exclusion's pattern as the list shows it, relative to its
// source's root with the source's name (KN-49); "" for one item, or a
// Confluence page and its children.
func ruleOf(x Source, e Exclusion) string {
	if !e.isPattern() || e.Under {
		return ""
	}
	return Shown(x, e.Pattern)
}

// exclusionWords say who excluded an item, when and why.
func exclusionWords(x Source, e Exclusion) string {
	w := "excluded by " + e.By + " on " + e.At.UTC().Format("2006-01-02")
	if e.isPattern() {
		w += " (" + cmp.Or(ruleOf(x, e), e.Pattern)
		if e.Under {
			w += " and its children"
		}
		w += ")"
	}
	if e.Reason != "" {
		w += ": " + e.Reason
	}
	return w
}

// ItemDetail is one item's details (KN-47): its first excerpts, the wiki's
// pages that drew on it, and where the original is.
type ItemDetail struct {
	Item         SourceItem  `json:"item"`
	Excerpts     []string    `json:"excerpts"`
	ExcerptCount int         `json:"excerptCount"`
	Pages        []PageBrief `json:"pages"`
	// Provenance says the pages are as the manifest records them: every
	// page Mycroft drafted from an item, kept as the item's pages.
	Provenance bool `json:"provenance"`
}

// detailExcerpts is how many excerpts the details give.
const detailExcerpts = 3

// Item gives one item's details: an Admin's or a SuperAdmin's.
func (s *Service) Item(ctx context.Context, c Caller, source, id string) (ItemDetail, error) {
	if !c.MayAdd() {
		return ItemDetail{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin sees the sources' items")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return ItemDetail{}, err
	}
	var found *SourceItem
	for _, it := range s.allItems(set) {
		if it.Source == source && it.ID == id {
			found = &it
			break
		}
	}
	if found == nil {
		return ItemDetail{}, wrapf(ErrNotFound, "no item %q in %q", id, source)
	}
	d := ItemDetail{Item: *found, Excerpts: []string{}, Pages: []PageBrief{}, Provenance: true}
	if found.key == "" {
		return d, nil
	}
	s.mu.RLock()
	var pages []string
	if st := s.manifest.Items[found.key]; st != nil {
		pages = slices.Clone(st.Pages)
	}
	f, ok := s.files[excerptPath(found.key)]
	s.mu.RUnlock()
	if ok {
		var ex Excerpt
		if json.Unmarshal(f.Body, &ex) == nil {
			d.ExcerptCount = len(ex.Chunks)
			d.Excerpts = append(d.Excerpts, ex.Chunks[:min(len(ex.Chunks), detailExcerpts)]...)
		}
	}
	d.Pages = s.pageBriefs(pages)
	return d, nil
}

// RewriteResult is what asking Mycroft to rewrite pages did (MY-14).
type RewriteResult struct {
	Sent   int      `json:"sent"`
	Failed []string `json:"failed,omitempty"`
	Text   string   `json:"text"`
}

// maxRewrite bounds the pages one request asks Mycroft to rewrite.
const maxRewrite = 20

// Rewrite asks Mycroft to rewrite the wiki's pages that drew on an item
// removed or excluded, without it (MY-14): one This is outdated…
// correction per page (KN-23), whose redraft waits for review like every
// change; nothing changes until it is approved. An Admin's or a
// SuperAdmin's.
func (s *Service) Rewrite(ctx context.Context, c Caller, pages []string, title, why string) (RewriteResult, error) {
	if !c.MayAdd() {
		return RewriteResult{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin asks Mycroft to rewrite the wiki's pages")
	}
	switch {
	case len(pages) == 0:
		return RewriteResult{}, invalid("say which pages to rewrite")
	case len(pages) > maxRewrite:
		return RewriteResult{}, invalid("at most %d pages at a time", maxRewrite)
	case strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 300:
		return RewriteResult{}, invalid("say which source the pages drew on")
	}
	gone := "removed from"
	if why == StateExcluded {
		gone = "excluded from"
	}
	text := fmt.Sprintf("The source “%s” was %s Mycroft's sources by %s on %s. Rewrite this page without what it took from that source: keep only what the other sources support, and leave out what only it said.", strings.TrimSpace(title), gone, c.label(), s.o.Now().UTC().Format("2006-01-02"))
	var res RewriteResult
	for _, p := range slices.Compact(slices.Sorted(slices.Values(pages))) {
		if _, err := s.Outdated(ctx, c, p, text); err != nil {
			res.Failed = append(res.Failed, p+": "+err.Error())
			continue
		}
		res.Sent++
	}
	s.audit(audit.Event{Action: ActionOutdated, Who: c.who(), Target: clip(title, 200), Outcome: map[bool]string{true: audit.OutcomeOK, false: audit.OutcomeFailed}[len(res.Failed) == 0], Detail: fmt.Sprintf("Mycroft asked to rewrite %d pages without “%s” (%d could not be asked)", res.Sent, clip(title, 200), len(res.Failed)), From: c.From})
	switch {
	case res.Sent == 0:
		res.Text = "No page could be sent to Mycroft: " + strings.Join(res.Failed, "; ")
	case s.o.Model == nil:
		res.Text = fmt.Sprintf("%d %s sent, but Mycroft has no model here: no page is rewritten.", res.Sent, plural(res.Sent, "request was", "requests were"))
	default:
		res.Text = fmt.Sprintf("Mycroft rewrites %d %s; each redraft waits for review under Drafts, and nothing changes until it is approved.", res.Sent, plural(res.Sent, "page", "pages"))
	}
	return res, nil
}
