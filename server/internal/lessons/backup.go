package lessons

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Exported is what a backup keeps (FX-11): the exceptions, whole. They
// hold no credential: what they quote was scrubbed when it was recorded.
// A backup of a build before wave 6e holds RCAs as well, which a restore
// leaves out: an RCA is its case's now (CF-44), restored with the case.
type Exported struct {
	Exceptions    []Exception `json:"exceptions,omitempty"`
	NextException int         `json:"nextException,omitempty"`
}

// Export is the backup's part; nil when there is nothing.
func (s *Service) Export(ctx context.Context) (*Exported, error) {
	ex, err := s.loadExceptions(ctx)
	if err != nil {
		return nil, err
	}
	if len(ex.Items) == 0 {
		return nil, nil
	}
	return &Exported{Exceptions: ex.Items, NextException: ex.Next}, nil
}

// number is the n of "EX-n"; 0 when it is not one.
func number(id, prefix string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
	if err != nil || !strings.HasPrefix(id, prefix) || n <= 0 {
		return 0
	}
	return n
}

// Import adds a backup's exceptions where missing, by id, and says how
// many it added; those here already stay as they are.
func (s *Service) Import(ctx context.Context, raw json.RawMessage) (int, error) {
	var in Exported
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, fmt.Errorf("the exceptions in the backup cannot be read: %w", err)
	}
	for _, e := range in.Exceptions {
		if number(e.ID, "EX-") == 0 || e.Check == "" || e.Reason == "" {
			return 0, fmt.Errorf("the exception %q in the backup is not valid", e.ID)
		}
	}
	added := 0
	err := s.updateExceptions(ctx, func(rec *exceptionsRecord) error {
		added = 0
		have := map[string]bool{}
		for _, e := range rec.Items {
			have[e.ID] = true
		}
		for _, e := range in.Exceptions {
			if !have[e.ID] {
				rec.Items = append(rec.Items, e)
				rec.Next = max(rec.Next, number(e.ID, "EX-"))
				added++
			}
		}
		rec.Next = max(rec.Next, in.NextException)
		slices.SortStableFunc(rec.Items, func(a, b Exception) int { return number(b.ID, "EX-") - number(a.ID, "EX-") })
		return nil
	})
	return added, err
}
