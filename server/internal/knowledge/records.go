package knowledge

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/scrub"
)

// --- Findings, exceptions and RCAs (Phase 12, ADR-110) ---

// RecordKinds are the kinds of the pages the console writes from its own
// records (package lessons): an accepted exception, and a case's
// root-cause analysis before wave 6e (CF-44: an RCA is its case's only
// now, and no page is written). A person decided each already, so they
// are written straight to the approved pages, not staged for review; and
// Mycroft does not write them (ValidPagePath refuses its pages of these
// kinds).
var RecordKinds = []string{"exception", "rca"}

// WriteRecord writes one page of the console's records into the wiki's
// approved pages, replacing the one of its path, and returns the path:
// pages/<kind>/<name>.md. The local copy and the index take it at once,
// so ask_knowledge finds it; the other replicas read it on their next
// refresh. ErrNoStore while no wiki is kept; only the leader writes.
func (s *Service) WriteRecord(ctx context.Context, kind, name, title, body string) (string, error) {
	if !slices.Contains(RecordKinds, kind) {
		return "", invalid("%q is not a kind of record page", kind)
	}
	path := PagesDir + kind + "/" + strings.ToLower(name) + ".md"
	if !pagePath.MatchString(path) {
		return "", invalid("%q is not a page's name", name)
	}
	if err := s.leads(); err != nil {
		return "", err
	}
	st, _, err := s.currentStore(ctx)
	if err != nil {
		return "", err
	}
	page := Page{Path: path, Title: clip(strings.TrimSpace(title), 200), Kind: kind, Updated: s.o.Now().UTC(), Body: scrub.Text(body)}
	file := page.Format()
	if err := st.Write(ctx, []Change{{Path: path, Body: file}}, "The console's record "+path); err != nil {
		return "", err
	}
	s.mu.RLock()
	files := maps.Clone(s.files)
	s.mu.RUnlock()
	if files == nil {
		files = map[string]File{}
	}
	files[path] = File{Path: path, Body: file}
	s.setFiles(ctx, files, true)
	s.audit(audit.Event{Action: ActionPublish, Who: audit.Who{Subject: "221b", Name: "console records"}, Target: path, Outcome: audit.OutcomeOK, Detail: "the page " + path + " written from the console's records"})
	return path, nil
}

// --- One flow for findings and cases (Phase 12, wave 6, CF-23) ---

// WipeMycroft deletes Mycroft's pages and the sources' excerpts from the
// wiki, with the manifest of what was read, so the next sync reads every
// source again (the clean start); the sources and their credentials stay.
// With dryRun it only says what it would delete. ErrNoStore while no wiki
// is kept; only the leader writes.
func (s *Service) WipeMycroft(ctx context.Context, dryRun bool) ([]string, error) {
	if err := s.leads(); err != nil {
		return nil, err
	}
	st, _, err := s.currentStore(ctx)
	if err != nil {
		return nil, err
	}
	files, err := st.Pull(ctx)
	if err != nil {
		return nil, err
	}
	var gone []string
	var changes []Change
	for p := range files {
		if strings.HasPrefix(p, PagesDir) || strings.HasPrefix(p, ExcerptsDir) || p == ManifestPath {
			gone = append(gone, p)
			changes = append(changes, Change{Path: p, Delete: true})
		}
	}
	slices.Sort(gone)
	if dryRun || len(changes) == 0 {
		return gone, nil
	}
	if err := st.Write(ctx, changes, "The clean start: Mycroft's pages and excerpts deleted"); err != nil {
		return nil, err
	}
	kept := map[string]File{}
	for p, f := range files {
		if !slices.Contains(gone, p) {
			kept[p] = f
		}
	}
	s.setFiles(ctx, kept, true)
	s.audit(audit.Event{Action: ActionPublish, Who: audit.Who{Subject: "221b", Name: "clean start"}, Target: "wiki", Outcome: audit.OutcomeOK, Detail: "the clean start deleted Mycroft's pages, the excerpts and the manifest: " + strconv.Itoa(len(gone)) + " files"})
	return gone, nil
}
