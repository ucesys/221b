package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
)

// The web pages added in Mycroft become web sites (0.12.1, KN-46). Before
// 0.12.1 a web address added in Mycroft was crawled once, its pages kept
// as items of Added in Mycroft. At the upgrade, on the leader, once the
// wiki's manifest has been read, each such page becomes a Web site source
// of its own: named after the page's title (renamable), its start address
// the page's, 0 links deep and 1 page (what the item was), no sign-in.
// Nothing is deleted: the page's excerpts stay, quoted as before and
// listed under its web site, until the site's first read (a run of its
// Wiki sync job, off until a SuperAdmin turns it on, or Run now) reads
// the page again; only then are the old excerpts emptied, and the pages
// that drew on them count the new item. A read that fails leaves them as
// they are. It is done once: Settings.Migrated records it, so a web site
// removed is not made again.

// migrationWebSites is the migration's name in Settings.Migrated.
const migrationWebSites = "web-sites"

// errNotReadYet is a migration that waits for the manifest to be read.
var errNotReadYet = errors.New("the wiki has not been read yet")

// MigrateWebItems makes a Web site source of each web page added in
// Mycroft (KN-46), once, on the leader, and says how many. It waits
// (errNotReadYet) until the wiki's manifest has been read; with no store
// there is nothing to migrate.
func (s *Service) MigrateWebItems(ctx context.Context) (int, error) {
	if err := s.leads(); err != nil {
		return 0, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	if slices.Contains(set.Migrated, migrationWebSites) {
		return 0, nil
	}
	_, _, storeErr := s.currentStore(ctx)
	if storeErr != nil && !errors.Is(storeErr, ErrNoStore) {
		return 0, storeErr
	}
	s.mu.RLock()
	m, pulled := s.manifest, s.pulled
	s.mu.RUnlock()
	if pulled.IsZero() && storeErr == nil {
		return 0, errNotReadYet
	}
	var pages []*ItemState
	for _, st := range m.documents(AddedSourceID) {
		if !st.Gone && typeOf(SourceUpload, st) == TypeWebPage {
			pages = append(pages, st)
		}
	}
	var made []Source
	var left []string
	_, err = s.editSettings(ctx, func(set *Settings) error {
		made, left = nil, nil
		if slices.Contains(set.Migrated, migrationWebSites) {
			return nil
		}
		taken := map[string]bool{}
		for _, x := range set.Sources {
			taken[x.ID] = true
			for _, id := range x.MigratedFrom {
				taken["from:"+id] = true
			}
		}
		for _, st := range pages {
			if taken["from:"+st.ID] {
				continue
			}
			if len(set.Sources) >= MaxSources {
				left = append(left, st.ID)
				continue
			}
			x := Source{ID: migratedID(st.ID, taken), Name: migratedName(st), Kind: SourceWeb, URL: st.URL, Depth: 0, Pages: 1, MigratedFrom: []string{st.ID},
				Added: s.o.Now().UTC(), AddedBy: "221b (the upgrade to 0.12.1)"}
			if err := x.Validate(); err != nil {
				left = append(left, st.ID)
				continue
			}
			taken[x.ID], taken["from:"+st.ID] = true, true
			set.Sources = append(set.Sources, x)
			made = append(made, x)
		}
		set.Migrated = append(set.Migrated, migrationWebSites)
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, x := range made {
		s.audit(audit.Event{Action: ActionSource, Who: audit.Who{Name: "221b"}, Target: x.ID, Outcome: audit.OutcomeOK,
			Detail: fmt.Sprintf("source %s (web) made from the web page %q added in Mycroft (KN-46): %s, 0 links deep, 1 page; its excerpts stay until its first read", x.ID, x.MigratedFrom[0], x.URL)})
	}
	if len(left) > 0 {
		s.o.Logger.Warn("some web pages added in Mycroft stay as they are: no web site could be made of them (at most 50 sources, or an address no longer valid); remove them in AIOps › Mycroft › Sources if they are not wanted", "pages", len(left))
	}
	if len(made) > 0 {
		s.syncKinds(ctx)
	}
	return len(made), nil
}

// migratedID is a web site's id made from a page's: web- and a hash.
func migratedID(item string, taken map[string]bool) string {
	h := hashOf([]byte(item))
	for n := 10; n <= 30; n += 4 {
		if id := "web-" + h[:n]; !taken[id] {
			return id
		}
	}
	return "web-" + h[:36]
}

// migratedName is a web site's name made from a page's title.
func migratedName(st *ItemState) string {
	name := strings.Join(strings.Fields(st.Title), " ")
	if name == "" {
		name = st.URL
	}
	if utf8.RuneCountInString(name) > 120 {
		name = strings.TrimSpace(string([]rune(name)[:119])) + "…"
	}
	return name
}

// migrateWebSites runs the migration until it is done, as SetJobs's loop
// calls it.
func (s *Service) migrateWebSites(ctx context.Context) {
	if s.webMigrated.Load() || !s.o.Leads() {
		return
	}
	n, err := s.MigrateWebItems(ctx)
	switch {
	case errors.Is(err, errNotReadYet):
		return
	case err != nil:
		s.o.Logger.Warn("the web pages added in Mycroft could not be made web sites yet; trying again", "err", err)
		return
	case n > 0:
		s.o.Logger.Info("the web pages added in Mycroft are web sites now (KN-46)", "sources", n)
	}
	s.webMigrated.Store(true)
}

// handOver ends a migrated web site's wait (KN-46): once its read listed
// its pages, the pages added in Mycroft it was made from are gone, their
// excerpts emptied, and the wiki's pages that drew on them count the
// site's page of the same address (or its first). It says the keys gone.
func handOver(m *Manifest, x Source, items []Item) []string {
	if len(x.MigratedFrom) == 0 || len(items) == 0 {
		return nil
	}
	var gone []string
	for _, id := range x.MigratedFrom {
		key := itemKey(AddedSourceID, id)
		old := m.Items[key]
		if old == nil || old.Gone {
			continue
		}
		to := items[0]
		for _, it := range items {
			if it.URL == old.URL {
				to = it
				break
			}
		}
		st := m.Items[itemKey(x.ID, to.ID)]
		if st == nil || st.Hash == "" || st.record() {
			// Not read: the old page's excerpts stay until it is.
			continue
		}
		for _, p := range old.Pages {
			if !slices.Contains(st.Pages, p) {
				st.Pages = append(st.Pages, p)
			}
		}
		old.Gone, old.Hash = true, ""
		gone = append(gone, key)
	}
	return gone
}
