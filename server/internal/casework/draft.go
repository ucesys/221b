package casework

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
)

// ---------- the case's one draft (CF-59) ----------

// DraftInput saves the case's draft (Save, CF-59): its parts, whole, the
// stamp of the draft the editor started from, and Force to save over a
// newer one.
type DraftInput struct {
	Reply string      `json:"reply,omitempty"`
	Fix   string      `json:"fix,omitempty"`
	Close *Closing    `json:"close,omitempty"`
	RCA   *Completion `json:"rca,omitempty"`
	// Base is the draft's Updated when the editor started from it; left
	// out when there was no draft yet.
	Base time.Time `json:"base,omitzero"`
	// Force saves over a draft someone saved since (Overwrite); the case's
	// history says so.
	Force bool `json:"force,omitempty"`
}

// DraftConflict is a save refused because someone saved the draft, or
// sent a part of it, since the editor started from it (CF-59): the API
// answers 409 with the draft as it is now.
type DraftConflict struct{ Draft Draft }

func (e *DraftConflict) Error() string {
	return fmt.Sprintf("%s changed this draft since you started; keep theirs, or save yours over it", firstOf(e.Draft.By, "someone"))
}

// Keep saves the case's draft for everyone who works it (CF-59). The
// stored draft must still be the one the editor started from (Base);
// otherwise the save is refused with the draft as it is, unless Force
// saves over it, which the history records. Each part changed names the
// caller among its drafters; an empty draft keeps only who emptied it,
// and when.
func (s *Service) Keep(ctx context.Context, id string, in DraftInput, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	d := Draft{Reply: clip(in.Reply, MaxDetail), Fix: clip(in.Fix, MaxDetail)}
	if x := in.Close; x != nil {
		d.Close = &Closing{Outcome: issues.Outcome(clip(string(x.Outcome), 40)).Canonical(), Explanation: clip(x.Explanation, MaxDetail), Original: clip(x.Original, 20)}
	}
	if x := in.RCA; x != nil {
		d.RCA = &Completion{Cause: clip(x.Cause, MaxRCAField), Fix: clip(x.Fix, MaxRCAField), Prevention: clip(x.Prevention, MaxRCAField), Text: clip(x.Text, MaxRCAField)}
	}
	name := c.actor().Name
	now := s.now()
	var over *Draft
	saved := false
	err = s.updateWork(ctx, n, func(w *Work) error {
		over, saved = nil, false
		old := Draft{}
		if w.Draft != nil {
			old = *w.Draft
		}
		// What is there already is no conflict.
		if sameParts(old, d) {
			return errUnchanged
		}
		if !old.Updated.Equal(in.Base) {
			if !in.Force {
				return &DraftConflict{Draft: old}
			}
			over = &old
		}
		next := d
		next.Drafters = drafters(old, d, name)
		next.By, next.Updated = name, now
		w.Draft, w.Updated, saved = &next, now, true
		if over != nil {
			addEvent(w, Event{At: now, What: fmt.Sprintf("Draft saved over %s's newer changes", firstOf(over.By, "someone")), By: c.actor()})
		}
		return nil
	})
	if err != nil {
		return View{}, err
	}
	if saved {
		what := "saved"
		switch {
		case over != nil:
			what = fmt.Sprintf("saved over %s's newer changes", firstOf(over.By, "someone"))
		case d.Empty():
			what = "cleared"
		}
		s.record(audit.Event{Action: ActionKeep, Who: who(c.Person), Cluster: v.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: its draft %s by %s", caseID(n), what, c.Label())})
	}
	return s.Get(ctx, c, caseID(n))
}

// sameParts says whether two drafts hold the same in every part.
func sameParts(a, b Draft) bool {
	for _, p := range draftParts {
		if a.part(p) != b.part(p) {
			return false
		}
	}
	return true
}

// drafters are who wrote each part of a draft saved by name over old: a
// part unchanged keeps its drafters, a part changed adds name, an empty
// part has none.
func drafters(old, next Draft, name string) map[string][]string {
	out := map[string][]string{}
	for _, p := range draftParts {
		if next.part(p) == "" {
			continue
		}
		list := slices.Clone(old.Drafters[p])
		if next.part(p) != old.part(p) && !slices.Contains(list, name) {
			list = append(list, name)
		}
		if len(list) > 0 {
			out[p] = list
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clearSent takes the parts just sent out of the case's draft, for
// everyone (CF-59), and says who drafted them and who sent them, for the
// history: "" when the draft held none of them or nobody is named.
func clearSent(w *Work, by issues.Actor, now time.Time, parts ...string) string {
	d := w.Draft
	if d == nil {
		return ""
	}
	var names []string
	cleared := false
	for _, p := range parts {
		if d.part(p) == "" {
			continue
		}
		for _, x := range d.Drafters[p] {
			if !slices.Contains(names, x) {
				names = append(names, x)
			}
		}
		d.clear(p)
		cleared = true
	}
	if !cleared {
		return ""
	}
	if len(d.Drafters) == 0 {
		d.Drafters = nil
	}
	d.By, d.Updated = firstOf(by.Name, sys.Name), now
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("drafted by %s; sent by %s", strings.Join(names, ", "), firstOf(by.Name, sys.Name))
}

// shareDrafts turns the drafts each person kept on a case before CF-59
// into its one draft: for each part, the newest. Once done it changes
// nothing; the next write keeps it.
func shareDrafts(w *Work) {
	if len(w.Drafts) == 0 {
		return
	}
	if w.Draft == nil {
		keys := make([]string, 0, len(w.Drafts))
		for k := range w.Drafts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := Draft{Drafters: map[string][]string{}}
		at := map[string]time.Time{}
		for _, k := range keys {
			p := w.Drafts[k]
			for _, part := range draftParts {
				if p.part(part) == "" {
					continue
				}
				if t, ok := at[part]; ok && !p.At.After(t) {
					continue
				}
				at[part] = p.At
				d.Drafters[part] = []string{k}
				switch part {
				case partReply:
					d.Reply = p.Reply
				case partFix:
					d.Fix = p.Fix
				case partClose:
					c := *p.Close
					d.Close = &c
				case partRCA:
					r := *p.RCA
					d.RCA = &r
				}
			}
			if d.By == "" || p.At.After(d.Updated) {
				d.By, d.Updated = k, p.At
			}
		}
		if !d.Empty() {
			w.Draft = &d
		}
	}
	w.Drafts = nil
}
