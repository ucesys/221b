package knowledge

import (
	"context"
	"strings"
	"time"
)

// --- AIOps › Mycroft › Sources lists sources, not files (phase-13.1-ai, KN-50, MY-13) ---
//
// The page lists one row a source: its name and kind, how many items it
// holds (with how many failed or are excluded), on or off, and when its
// last Wiki sync finished with a short result, from the syncs kept
// (knowledge_syncs: the last sync and the newest twenty in short). A live
// source is looked up when asked and never synced; a source no sync kept
// names, but which was read, says when it was read; one never read says
// so. A source's items are listed when it is opened (KN-47's list, by
// that source).

// SourceSync is when a source's last sync finished, and how it went.
type SourceSync struct {
	At time.Time `json:"at"`
	// Changed is how many changed items the sync read; absent when it is
	// not known: a sync of every source counts them for all sources
	// together, and a read no kept sync names says no count.
	Changed *int `json:"changed,omitempty"`
	// Failed says the source could not be read; Error why.
	Failed bool   `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`
	// Every says it was a sync of every source, not of this one alone.
	Every bool `json:"every,omitempty"`
}

// SourceSummary is a source as AIOps › Mycroft › Sources lists it.
type SourceSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Off  bool   `json:"off,omitempty"`
	// Items is how many items it holds, those that failed and those
	// excluded among them.
	Items    int `json:"items"`
	Failed   int `json:"failed,omitempty"`
	Excluded int `json:"excluded,omitempty"`
	// LastSync is its last sync; absent for a live source (looked up when
	// asked) and for a source never read.
	LastSync *SourceSync `json:"lastSync,omitempty"`
}

// SourcesView is the list of sources, in the settings' order.
type SourcesView struct {
	Sources    []SourceSummary `json:"sources"`
	MayExclude bool            `json:"mayExclude"`
	MayRemove  bool            `json:"mayRemove"`
}

// SourceSummaries lists every source with its items and its last sync
// (KN-50): an Admin's or a SuperAdmin's, as the items are.
func (s *Service) SourceSummaries(ctx context.Context, c Caller) (SourcesView, error) {
	if !c.MayAdd() {
		return SourcesView{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin sees the sources")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return SourcesView{}, err
	}
	st, err := s.Status(ctx)
	if err != nil {
		return SourcesView{}, err
	}
	type counts struct{ items, failed, excluded int }
	by := map[string]*counts{}
	for _, it := range s.allItems(set) {
		n := by[it.Source]
		if n == nil {
			n = &counts{}
			by[it.Source] = n
		}
		n.items++
		switch it.State {
		case StateError:
			n.failed++
		case StateExcluded:
			n.excluded++
		}
	}
	out := SourcesView{Sources: []SourceSummary{}, MayExclude: c.SuperAdmin, MayRemove: true}
	for _, x := range set.Sources {
		row := SourceSummary{ID: x.ID, Name: x.Name, Kind: x.Kind, Off: x.Off}
		if n := by[x.ID]; n != nil {
			row.Items, row.Failed, row.Excluded = n.items, n.failed, n.excluded
		}
		if x.Kind != SourceLive {
			row.LastSync = lastSync(st, x)
		}
		out.Sources = append(out.Sources, row)
	}
	return out, nil
}

// lastSync is the newest finished sync that read the source: a sync of it
// alone, or of every source. With none kept, the source's last read, when
// there was one.
func lastSync(st Status, x Source) *SourceSync {
	var best *SyncResult
	consider := func(r *SyncResult) {
		if r == nil || r.Finished.IsZero() || (r.Source != "" && r.Source != x.ID) {
			return
		}
		if best == nil || r.Finished.After(best.Finished) {
			best = r
		}
	}
	consider(st.Last)
	for i := range st.History {
		consider(&st.History[i])
	}
	if best == nil {
		ss, ok := st.Sources[x.ID]
		if !ok || ss.LastRead.IsZero() {
			return nil
		}
		return &SourceSync{At: ss.LastRead.UTC()}
	}
	out := &SourceSync{At: best.Finished.UTC(), Every: best.Source == ""}
	name := x.Name
	if !out.Every {
		read := best.Read
		out.Changed = &read
		if best.SourceName != "" {
			name = best.SourceName
		}
	}
	// A source that could not be read is an error of the sync's that
	// starts with its name (SyncWith).
	for _, e := range best.Errors {
		if rest, ok := strings.CutPrefix(e, name+": "); ok {
			out.Failed, out.Error = true, rest
			break
		}
	}
	return out
}
