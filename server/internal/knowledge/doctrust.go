package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
)

// A trust level per uploaded document (0.13.2, KN-61; asked by the owner
// on November 23, 2026). Every upload lands in one source (Added in
// Mycroft; older ones in Uploaded documents), so a source's trust (KN-57)
// was every upload's. A SuperAdmin now chooses a document's own level at
// its upload (Administration › AI › Knowledge › Uploaded documents) and
// changes it on its row in AIOps › Mycroft › Sources; none is its
// source's. It weighs the document's passages in the search, its
// sections' too, and Mycroft writes pages from it by itself only when it
// is high (KN-59). It is kept in the manifest, on the document's entry
// (ItemState.Trust), with what else the wiki keeps of the document; one
// still waiting to be read keeps it with its text (pendingDoc) until a
// sync writes its entry. An Admin's upload chooses none: a document
// uploaded again, in either source of uploaded documents, keeps its own,
// so an Admin cannot raise one a SuperAdmin lowered.

// DocumentTrust is an uploaded document's trust: its own (Trust, "" for
// none), its source's, and the one that counts (Level).
type DocumentTrust struct {
	Source      string `json:"source"`
	ID          string `json:"id"`
	Trust       string `json:"trust"`
	SourceTrust string `json:"sourceTrust"`
	Level       string `json:"level"`
}

// chosenTrust is the trust level chosen at an upload: none (nil), or ""
// for its source's, or a level; only a SuperAdmin chooses.
func chosenTrust(c Caller, t *string) (level string, set bool, err error) {
	if t == nil {
		return "", false, nil
	}
	if !c.SuperAdmin {
		return "", false, wrapf(ErrDenied, "only a SuperAdmin chooses a document's trust")
	}
	level = strings.TrimSpace(strings.ToLower(*t))
	if level != "" && level != TrustHigh && level != TrustNormal && level != TrustLow {
		return "", false, invalid("a document's trust is high, normal or low, or its source's")
	}
	return level, true, nil
}

// trustWords are an upload's trust as its audit says it.
func trustWords(level string, set bool) string {
	switch {
	case !set:
		return ""
	case level == "":
		return "; trust its source's"
	}
	return "; trust " + level
}

// ownTrust is a document's own trust level: the one chosen for it while
// it waits to be read, else its manifest entry's.
func (s *Service) ownTrust(source, name string) string {
	s.pendingMu.Lock()
	p, ok := s.pending[source+"/"+name]
	s.pendingMu.Unlock()
	if ok && p.setTrust {
		return p.trust
	}
	return s.entryTrust(source, name)
}

// keptTrust is the trust of a document that replaces one of the same name
// in another source of uploaded documents: the one chosen, else the one
// it replaces had.
func (s *Service) keptTrust(source, name, level string, set bool) (string, bool) {
	if set {
		return level, set
	}
	if own := s.ownTrust(source, name); own != "" {
		return own, true
	}
	return level, set
}

// documentLevels are the uploaded documents' own trust levels, by their
// manifest keys: those that have one.
func (s *Service) documentLevels() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for key, st := range s.manifest.Items {
		if st.Trust != "" && st.Of == "" && !st.Gone {
			out[key] = st.Trust
		}
	}
	return out
}

// SetDocumentTrust sets an uploaded document's trust level: high, normal
// or low, or "" for its source's. A SuperAdmin's, as a source's trust is,
// audited knowledge.trust with what it was. It counts for the search at
// once and for Mycroft's pages from the document's next read (a page from
// it is asked for on its row meanwhile, WritePage).
func (s *Service) SetDocumentTrust(ctx context.Context, c Caller, source, name, level string) (DocumentTrust, error) {
	if !c.SuperAdmin {
		return DocumentTrust{}, wrapf(ErrDenied, "only a SuperAdmin sets a document's trust")
	}
	if err := s.leads(); err != nil {
		return DocumentTrust{}, err
	}
	level, _, err := chosenTrust(c, &level)
	if err != nil {
		return DocumentTrust{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return DocumentTrust{}, err
	}
	x, ok := set.source(source)
	if !ok || x.Kind != SourceUpload {
		return DocumentTrust{}, wrapf(ErrNotFound, "no source of uploaded documents %q", source)
	}
	// As a sync writes the manifest: one set while a sync runs is kept
	// (keepTrust).
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	found, note := false, false
	var was string
	s.pendingMu.Lock()
	if p, ok := s.pending[source+"/"+name]; ok {
		found, note = true, p.kind == AddText
		if p.setTrust {
			was = p.trust
		} else {
			was = s.entryTrust(source, name)
		}
		if !note {
			p.trust, p.setTrust = level, true
			s.pending[source+"/"+name] = p
		}
	}
	s.pendingMu.Unlock()
	s.mu.RLock()
	m := s.manifest.clone()
	s.mu.RUnlock()
	st := m.Items[itemKey(source, name)]
	if st != nil && (st.Gone || st.Of != "") {
		st = nil
	}
	if st != nil {
		note = note || typeOf(SourceUpload, st) == TypeNote
		if !found {
			was = st.Trust
		}
		found = true
	}
	switch {
	case !found:
		return DocumentTrust{}, wrapf(ErrNotFound, "no document %q in %s", name, x.Name)
	case note:
		return DocumentTrust{}, invalid("a note takes its source's trust; a document has its own")
	}
	if st != nil && st.Trust != level {
		store, _, err := s.currentStore(ctx)
		if err != nil {
			return DocumentTrust{}, err
		}
		st.Trust = level
		if err := store.Write(ctx, []Change{{Path: ManifestPath, Body: m.encode()}}, fmt.Sprintf("Mycroft: the trust of %s is %s", name, trustName(x, level))); err != nil {
			return DocumentTrust{}, err
		}
		if err := s.Refresh(ctx); err != nil {
			return DocumentTrust{}, err
		}
	}
	if was != level {
		s.audit(audit.Event{Action: ActionIndexSettings, Who: c.who(), Target: itemKey(source, name), Outcome: audit.OutcomeOK, From: c.From,
			Detail: fmt.Sprintf("%s › %s: trust %s, was %s", x.Name, name, trustName(x, level), trustName(x, was))})
	}
	out := DocumentTrust{Source: source, ID: name, Trust: level, SourceTrust: x.trustLevel(), Level: level}
	if level == "" {
		out.Level = out.SourceTrust
	}
	return out, nil
}

// entryTrust is a document's manifest entry's own trust. pendingMu may be
// held.
func (s *Service) entryTrust(source, name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st := s.manifest.Items[itemKey(source, name)]; st != nil && !st.Gone {
		return st.Trust
	}
	return ""
}

// trustName is a document's trust in words: its own, or its source's.
func trustName(x Source, level string) string {
	if level == "" {
		return x.trustLevel() + " (its source's)"
	}
	return level
}
