package casework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// errUnchanged, returned by an update's function, saves nothing.
var errUnchanged = errors.New("unchanged")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ---------- the work on a case, in its object ----------

// loadWork reads the work on a case from its object: empty when there is
// none yet.
func (s *Service) loadWork(ctx context.Context, n int) (Work, string, error) {
	keys, version, err := s.cases().Load(ctx, strconv.Itoa(n))
	if errors.Is(err, kv.ErrNotFound) {
		return Work{Number: n}, "", fmt.Errorf("%w: there is no case C-%d", ErrNotFound, n)
	}
	if err != nil {
		return Work{}, "", err
	}
	w := Work{Number: n}
	if b, ok := keys[issues.WorkKey]; ok {
		if err := json.Unmarshal(b, &w); err != nil {
			return Work{}, "", fmt.Errorf("the work on C-%d cannot be read: %w", n, err)
		}
	}
	w.canonCards()
	shareDrafts(&w)
	return w, version, nil
}

// canonCards writes the ids of the cards the work keeps the one way,
// PC-<n> (CF-56): work kept before names them c<n>, and is saved so the
// next time it changes. Doing it again changes nothing.
func (w *Work) canonCards() {
	var ids []string
	for _, x := range w.Proposals {
		if x = cardid.Canon(x); !slices.Contains(ids, x) {
			ids = append(ids, x)
		}
	}
	w.Proposals = ids
	w.LinkedBy, w.Handled = canonKeys(w.LinkedBy), canonKeys(w.Handled)
	if f := w.FixApplied; f != nil && f.Change != "" {
		if id := cardid.Canon(f.Change); id != f.Change {
			f.Text = strings.Replace(f.Text, "change "+f.Change, "change "+id, 1)
			f.Change = id
		}
	}
}

// canonKeys is a map keyed by cards, its keys the one way (CF-56).
func canonKeys(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	// A key kept the new way wins over the same card's of before.
	for k, v := range m {
		c := cardid.Canon(k)
		if _, ok := out[c]; !ok || c == k {
			out[c] = v
		}
	}
	return out
}

// updateWork changes the work on a case over the version read, again
// when another writer (the issue service on the same object) came first.
func (s *Service) updateWork(ctx context.Context, n int, f func(*Work) error) error {
	for try := 0; try < 10; try++ {
		w, version, err := s.loadWork(ctx, n)
		if err != nil {
			return err
		}
		if err := f(&w); errors.Is(err, errUnchanged) {
			return nil
		} else if err != nil {
			return err
		}
		w.Number = n
		b, _ := json.Marshal(w)
		if len(b) > s.o.WorkBytes {
			// The oldest history goes first: the first event and the newest
			// stay.
			for len(b) > s.o.WorkBytes && len(w.History) > 2 {
				w.History = append(w.History[:1:1], w.History[2:]...)
				b, _ = json.Marshal(w)
			}
			if len(b) > s.o.WorkBytes {
				return fmt.Errorf("%w: the work on C-%d would pass its %d KiB; clear the draft of what is no longer needed", ErrFull, n, s.o.WorkBytes>>10)
			}
		}
		if _, err = s.cases().Save(ctx, strconv.Itoa(n), issues.WorkKey, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("the work on C-%d kept changing", n)
}

// ---------- the casework ConfigMap's own records ----------

// record is SettingsKey: Settings › Cases, the counter, the analyses
// under way (case number to since when), and when the retention ran.
type record struct {
	Settings   Settings             `json:"settings"`
	Duplicates int                  `json:"duplicatesOfAgentCases,omitempty"`
	Analyzing  map[string]time.Time `json:"analyzing,omitempty"`
	Retained   time.Time            `json:"retained,omitzero"`
}

// checksRecord is ChecksKey: the check ids seen on each cluster.
type checksRecord struct {
	Clusters map[string][]string `json:"clusters,omitempty"`
}

func loadJSON[T any](ctx context.Context, b kv.Backend, key string, v *T) (string, error) {
	raw, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return version, nil
	}
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return "", fmt.Errorf("the cases' record %s cannot be read: %w", key, err)
	}
	return version, nil
}

// update changes a key over the version read, again when another replica
// wrote first. A function that returns errUnchanged saves nothing.
func update[T any](ctx context.Context, b kv.Backend, key string, f func(*T) error) error {
	for try := 0; try < 10; try++ {
		var v T
		version, err := loadJSON(ctx, b, key, &v)
		if err != nil {
			return err
		}
		if err := f(&v); errors.Is(err, errUnchanged) {
			return nil
		} else if err != nil {
			return err
		}
		out, _ := json.Marshal(v)
		if _, err = b.SaveKey(ctx, key, out, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("the cases' record %s kept changing", key)
}

func (s *Service) loadRecord(ctx context.Context) (record, error) {
	var rec record
	_, err := loadJSON(ctx, s.o.Backend, SettingsKey, &rec)
	rec.Settings = rec.Settings.withDefaults()
	return rec, err
}

func (s *Service) updateRecord(ctx context.Context, f func(*record) error) error {
	return update(ctx, s.o.Backend, SettingsKey, func(r *record) error {
		r.Settings = r.Settings.withDefaults()
		return f(r)
	})
}

// settings are Settings › Cases as they stand; the defaults when they
// cannot be read.
func (s *Service) settings(ctx context.Context) Settings {
	rec, err := s.loadRecord(ctx)
	if err != nil {
		return DefaultSettings()
	}
	out := rec.Settings
	out.DuplicatesOfAgentCases = rec.Duplicates
	return out
}

// ---------- helpers ----------

// clip cuts a text to at most n characters, with anything that looks
// like a credential taken out (X-01).
func clip(s string, n int) string {
	s = scrub.Text(strings.TrimSpace(s))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// addEvent adds a line to a case's history: the first and the newest are
// kept.
func addEvent(w *Work, e Event) {
	e.What = clip(e.What, 500)
	if e.By.Name == "" {
		e.By = sys
	}
	w.History = append(w.History, e)
	if len(w.History) > maxHistory {
		w.History = append(w.History[:1:1], w.History[len(w.History)-maxHistory+1:]...)
	}
}

// number reads a case's id: "C-12", "12" or "#12".
func number(id string) (int, bool) {
	id = strings.TrimPrefix(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(id)), "C-"), "#")
	n, err := strconv.Atoi(id)
	return n, err == nil && n > 0
}

// caseID is a case's id.
func caseID(n int) string { return fmt.Sprintf("C-%d", n) }

// idOf is a case's id, "" for none.
func idOf(n int) string {
	if n == 0 {
		return ""
	}
	return caseID(n)
}
