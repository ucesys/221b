package ai

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// A test made from a trace without a name takes its question as one, cut
// at 60 bytes on a rune boundary, so a multi-byte character is never split
// (A-6).
func TestATracesNameIsCutOnARuneBoundaryA6(t *testing.T) {
	// 59 bytes of ASCII, then "é" (2 bytes) across the 60th byte.
	q := strings.Repeat("a", 59) + "é and more after it"
	c := FromTrace(sherlock.Trace{Agent: "sherlock", Question: q}, "", nil)
	if !utf8.ValidString(c.Name) {
		t.Fatalf("not valid UTF-8: %q", c.Name)
	}
	if c.Name != strings.Repeat("a", 59)+"…" {
		t.Errorf("name %q", c.Name)
	}
	// A short question is the name as it is; a name given is kept.
	if c := FromTrace(sherlock.Trace{Agent: "sherlock", Question: "Why is web down?"}, "", nil); c.Name != "Why is web down?" {
		t.Errorf("short %q", c.Name)
	}
	if c := FromTrace(sherlock.Trace{Agent: "sherlock", Question: q}, "mine", nil); c.Name != "mine" {
		t.Errorf("given %q", c.Name)
	}
}
