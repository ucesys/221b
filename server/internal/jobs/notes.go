package jobs

import (
	"fmt"
	"strings"
)

// --- Jobs and findings, round 2 (Phase 12, wave 5) ---
//
// A run's note says what went wrong along the way, one line per thing;
// the same line said many times (a proposed command Watson would refuse,
// once per proposal) is said once, with how many (JR-02).

// plurals are the notes the runs make often, as they read for more than
// one: the singular's start, and the plural's.
var plurals = []struct{ one, many string }{
	{"a proposed command Watson would refuse was left out", "%d proposed commands Watson would refuse were left out"},
	{"a proposed command that is not kubectl or helm was left out", "%d proposed commands that are not kubectl or helm were left out"},
}

// mergeNotes says each note once, in the order first said, with how many
// times it was said.
func mergeNotes(notes []string) []string {
	var order []string
	count := map[string]int{}
	for _, n := range notes {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if count[n] == 0 {
			order = append(order, n)
		}
		count[n]++
	}
	out := make([]string, 0, len(order))
	for _, n := range order {
		out = append(out, counted(n, count[n]))
	}
	return out
}

// counted is a note said n times.
func counted(note string, n int) string {
	if n < 2 {
		return note
	}
	for _, p := range plurals {
		if rest, ok := strings.CutPrefix(note, p.one); ok {
			return fmt.Sprintf(p.many, n) + rest
		}
	}
	return fmt.Sprintf("%s (%d times)", note, n)
}
