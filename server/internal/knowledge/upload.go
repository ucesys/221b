package knowledge

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/scrub"
)

// Uploaded documents are read when uploaded or replaced (by their file
// hash): their text is held in memory until Mycroft has read it, and
// only the excerpts, the manifest and the pages it writes are kept.

var docName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._()-]{0,159}$`)

// Upload reads a document into a source of uploaded documents and starts
// a sync of that source, in the background.
func (s *Service) Upload(ctx context.Context, c Caller, source, name string, body []byte) (string, error) {
	if !c.SuperAdmin {
		return "", wrapf(ErrDenied, "only a SuperAdmin uploads Mycroft's documents")
	}
	if err := s.leads(); err != nil {
		return "", err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	x, ok := set.source(source)
	if !ok || x.Kind != SourceUpload {
		return "", wrapf(ErrNotFound, "no source of uploaded documents %q", source)
	}
	name = path.Base(strings.TrimSpace(name))
	if !docName.MatchString(name) {
		return "", invalid("a document's name is letters, digits, spaces, dots, dashes and brackets, up to 160")
	}
	if _, _, err := s.currentStore(ctx); err != nil {
		return "", err
	}
	text, err := Extract(name, body)
	if err != nil {
		return "", err
	}
	text = scrub.Known(scrub.Text(text), s.secretValues(ctx))
	h := hashOf(body)
	s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: source, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("document %s uploaded to %s (%d bytes)", name, source, len(body)), From: c.From})
	s.readSoon(ctx, c, []pendingDoc{{source: source, name: name, text: text, hash: h, at: s.o.Now().UTC(), by: c.label()}}, "upload of "+name)
	return h[:16], nil
}

// readSoon holds documents read but not yet given to Mycroft, and starts
// a sync of their source in the background, after any sync running; the
// text is let go once that sync has read it.
func (s *Service) readSoon(ctx context.Context, c Caller, docs []pendingDoc, what string) {
	if len(docs) == 0 {
		return
	}
	source := docs[0].source
	s.pendingMu.Lock()
	for _, d := range docs {
		s.pending[d.source+"/"+d.name] = d
	}
	s.pendingMu.Unlock()
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx := context.WithoutCancel(ctx)
		for {
			_, err := s.SyncWith(ctx, SyncRequest{Source: source, By: what + " by " + c.label(), Who: c.who()})
			if err != ErrBusy {
				break
			}
			// Another sync runs: this one follows it.
			s.syncMu.Lock()
			s.syncMu.Unlock()
		}
		s.pendingMu.Lock()
		for _, d := range docs {
			if p, ok := s.pending[d.source+"/"+d.name]; ok && p.hash == d.hash {
				delete(s.pending, d.source+"/"+d.name)
			}
		}
		s.pendingMu.Unlock()
	}()
}

// Documents lists a source's uploaded documents, as the manifest holds
// them, and those waiting to be read.
func (s *Service) Documents(source string) []ItemState {
	s.mu.RLock()
	m := s.manifest
	s.mu.RUnlock()
	var out []ItemState
	seen := map[string]bool{}
	for _, st := range m.bySource(source) {
		if !st.Gone {
			out = append(out, *st)
			seen[st.ID] = true
		}
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for _, p := range s.pending {
		if p.source == source && !seen[p.name] {
			out = append(out, ItemState{Source: source, ID: p.name, Title: p.label(), URL: p.url, Version: p.hash[:16], Modified: p.at})
		}
	}
	return out
}

// RemoveDocument forgets an uploaded document: it leaves the manifest and
// the index; the pages that drew on it stay, and grow stale.
func (s *Service) RemoveDocument(ctx context.Context, c Caller, source, name string) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin removes Mycroft's documents")
	}
	return s.removeDocument(ctx, c, source, name)
}

func (s *Service) removeDocument(ctx context.Context, c Caller, source, name string) error {
	if err := s.leads(); err != nil {
		return err
	}
	// One still waiting to be read is let go first.
	s.pendingMu.Lock()
	_, waiting := s.pending[source+"/"+name]
	delete(s.pending, source+"/"+name)
	s.pendingMu.Unlock()
	store, _, err := s.currentStore(ctx)
	if err != nil {
		return err
	}
	s.mu.RLock()
	m := s.manifest.clone()
	s.mu.RUnlock()
	key := itemKey(source, name)
	st := m.Items[key]
	if st == nil && waiting {
		s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: source, Outcome: audit.OutcomeOK, Detail: "document " + name + " removed from " + source + " before it was read", From: c.From})
		return nil
	}
	if st == nil {
		return wrapf(ErrNotFound, "no document %q", name)
	}
	st.Gone = true
	empty := []byte(`{"key":"` + key + `","chunks":[]}`)
	if err := store.Write(ctx, []Change{{Path: excerptPath(key), Body: empty}, {Path: ManifestPath, Body: m.encode()}}, "Mycroft forgets the document "+name); err != nil {
		return err
	}
	s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: source, Outcome: audit.OutcomeOK, Detail: "document " + name + " removed from " + source, From: c.From})
	return s.Refresh(ctx)
}
