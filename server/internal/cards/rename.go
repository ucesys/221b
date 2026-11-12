package cards

import (
	"context"
	"errors"
	"strings"
)

// errNoneRenamed saves nothing: no card named a case renamed.
var errNoneRenamed = errors.New("no card renamed")

// --- Cases replace reports (Phase 12, wave 5, CU-12) ---

// RenameCases renames the cases the cards name (old id to new): their
// source, its label and what a newer proposal replaces them by. Package
// casework calls it once, when it carries the cases of before over into
// the one numbering. It says how many cards changed.
func (s *Store) RenameCases(ctx context.Context, ids map[string]string) (int, error) {
	n := 0
	err := s.changeCards(ctx, func(rec *record) error {
		n = 0
		for i := range rec.Cards {
			c := &rec.Cards[i]
			to, ok := ids[c.Source.ID]
			if c.Source.Kind != "case" || !ok {
				continue
			}
			old := c.Source.ID
			c.Source.ID = to
			c.Source.Label = strings.Replace(c.Source.Label, "Case "+old+":", "Case "+to+":", 1)
			c.DedupKey = strings.Replace(c.DedupKey, "case:"+old+":", "case:"+to+":", 1)
			n++
		}
		if n == 0 {
			return errNoneRenamed
		}
		return nil
	})
	if errors.Is(err, errNoneRenamed) {
		return 0, nil
	}
	return n, err
}

// --- end of cases.
