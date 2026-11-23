package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
)

// Mycroft writes pages for what matters (phase-13.1-ai, part B, KN-59):
// not for every item read, so that its cost and the Inbox's approvals do
// not grow with a large source. Every item read is excerpted and indexed
// for the search; Mycroft is given, to write or update pages from, an
// item of a source of high trust (by default the organisation's own:
// Confluence spaces, git repositories of runbooks, uploaded documents and
// notes, the console's records, the fixes; web sites and buckets are
// normal; a SuperAdmin raises or lowers any source, KN-57), a
// correction (KN-23), and, on request, any item whose excerpts the wiki
// holds (WritePage). Vendor documentation and other bulk sources stay
// excerpts, which ask_knowledge gives marked unreviewed.

// writesPages says whether Mycroft is given an item read to write pages
// from.
func writesPages(ri readItem) bool {
	return ri.source.trustLevel() == TrustHigh || ri.item.Corrects != ""
}

// ActionPageRequest is a person's request that Mycroft write pages from
// an item (KN-59).
const ActionPageRequest = "knowledge.page.request"

// PageRequestResult is what asking Mycroft to write pages from an item
// did: the run of page changes that waits for review, if any.
type PageRequestResult struct {
	Run   string `json:"run,omitempty"`
	Pages int    `json:"pages"`
	Text  string `json:"text"`
}

// WritePage has Mycroft write or update the wiki's pages from one item of
// a source, from its excerpts (a long document's sections, KN-52),
// whatever its source's trust: an Admin's or a SuperAdmin's. Its page
// changes wait for review in AIOps › Inbox as a run, as a sync's do.
func (s *Service) WritePage(ctx context.Context, c Caller, source, id string) (PageRequestResult, error) {
	if !c.MayAdd() {
		return PageRequestResult{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin asks Mycroft for a page")
	}
	if err := s.leads(); err != nil {
		return PageRequestResult{}, err
	}
	if s.o.Model == nil {
		return PageRequestResult{}, invalid("Mycroft has no model here: no page is written")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return PageRequestResult{}, err
	}
	x, ok := set.source(source)
	if !ok {
		return PageRequestResult{}, wrapf(ErrNotFound, "no source %q", source)
	}
	if !s.syncMu.TryLock() {
		return PageRequestResult{}, ErrBusy
	}
	defer s.syncMu.Unlock()
	s.syncing.Store(true)
	defer s.syncing.Store(false)
	store, _, err := s.currentStore(ctx)
	if err != nil {
		return PageRequestResult{}, err
	}
	files, err := store.Pull(ctx)
	if err != nil {
		return PageRequestResult{}, fmt.Errorf("reading the wiki: %w", err)
	}
	s.setFiles(ctx, files, true)
	s.mu.RLock()
	m := s.manifest.clone()
	s.mu.RUnlock()
	key := itemKey(source, id)
	st := m.Items[key]
	if st == nil || st.Gone || st.Of != "" {
		return PageRequestResult{}, wrapf(ErrNotFound, "no item %q in %s", id, x.Name)
	}
	// The item's excerpts, or its sections'.
	keys := []string{key}
	for _, sec := range sectionsOf(m, source, id) {
		if !sec.Gone {
			keys = append(keys, itemKey(source, sec.ID))
		}
	}
	var items []readItem
	for _, k := range keys {
		f, ok := files[excerptPath(k)]
		if !ok {
			continue
		}
		var e Excerpt
		if json.Unmarshal(f.Body, &e) != nil || len(e.Chunks) == 0 {
			continue
		}
		is := m.Items[k]
		items = append(items, readItem{source: x, item: Item{ID: is.ID, Title: is.Title, Version: is.Version, Modified: is.Modified, URL: is.URL, By: is.By, Type: is.Type}, key: k, text: strings.Join(e.Chunks, "\n\n")})
	}
	if len(items) == 0 {
		return PageRequestResult{}, invalid("the wiki keeps no excerpt of “%s” to write a page from: read its source first", st.Title)
	}
	now := s.o.Now().UTC()
	req := SyncRequest{Source: source, By: fmt.Sprintf("a page asked by %s from “%s”", c.label(), clip(st.Title, 120)), Who: c.who()}
	res := SyncResult{Started: now, By: req.By, Source: source, SourceName: x.Name, Read: len(items)}
	run, _, err := s.draft(ctx, store, files, m, items, req, s.secretValues(ctx), &res)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	if run != nil {
		res.Run, res.Pages = run.ID, len(run.Pages)
		if err := s.saveRun(ctx, *run); err != nil {
			res.Errors = append(res.Errors, "the drafts could not be listed for review: "+err.Error())
		}
	}
	// The manifest learns the pages the item drew on, with the
	// retirements as they are now (KN-24).
	s.manifestMu.Lock()
	s.mu.RLock()
	m.Retired = maps.Clone(s.manifest.Retired)
	s.mu.RUnlock()
	m.Updated = now
	if err := store.Write(ctx, []Change{{Path: ManifestPath, Body: m.encode()}}, req.By); err != nil {
		res.Errors = append(res.Errors, "the manifest could not be written: "+err.Error())
	} else if err := s.Refresh(ctx); err != nil {
		s.o.Logger.Warn("reading the wiki after a page request failed", "err", err)
	}
	s.manifestMu.Unlock()
	res.Finished = s.o.Now().UTC()
	res.Text = syncReport(res)
	s.saveStatus(ctx, res, nil)
	outcome := audit.OutcomeOK
	if len(res.Errors) > 0 {
		outcome = audit.OutcomeFailed
	}
	s.audit(audit.Event{Action: ActionPageRequest, Who: c.who(), Target: key, Outcome: outcome, From: c.From,
		Detail: fmt.Sprintf("Mycroft asked to write pages from “%s” of %s: %d page changes for review; %d errors", clip(st.Title, 200), x.Name, res.Pages, len(res.Errors))})
	out := PageRequestResult{Run: res.Run, Pages: res.Pages}
	switch {
	case len(res.Errors) > 0 && res.Pages == 0:
		out.Text = "Mycroft wrote no page: " + strings.Join(res.Errors, "; ")
	case res.Pages == 0:
		out.Text = "Mycroft found nothing to add to the wiki from it."
	default:
		out.Text = fmt.Sprintf("%d page %s wait for review in AIOps › Inbox (run %s).", res.Pages, plural(res.Pages, "change", "changes"), res.Run)
	}
	return out, nil
}
