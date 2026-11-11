package cardid

import (
	"strings"
	"testing"
)

// A card is PC-<n>; the c<n> of before, in any case, is the same card,
// and a case's C-<n> is never one (CF-56).
func TestCanon(t *testing.T) {
	for in, want := range map[string]string{
		"c16":         "PC-16",
		"C16":         "PC-16",
		"pc-16":       "PC-16",
		"PC-16":       "PC-16",
		"pc16":        "PC-16",
		" PC-7 ":      "PC-7",
		"C-16":        "C-16",
		"KI-3":        "KI-3",
		"c0":          "c0",
		"c016":        "c016",
		"PC-":         "PC-",
		"pcc-1":       "pcc-1",
		"c-e2e":       "c-e2e",
		"":            "",
		"c1234567890": "c1234567890",
	} {
		if got := Canon(in); got != want {
			t.Errorf("Canon(%q) = %q, want %q", in, got, want)
		}
	}
	if Of(16) != "PC-16" || !Is("c16") || !Is("PC-16") || Is("C-16") || Is("") || !Same("c16", "PC-16") || Same("c16", "C-16") {
		t.Error("Of, Is or Same is wrong")
	}
	// Canon is idempotent.
	for _, s := range []string{"c3", "PC-3", "C-3", "x"} {
		if Canon(Canon(s)) != Canon(s) {
			t.Errorf("Canon is not idempotent on %q", s)
		}
	}
}

// A proposed change's key is its cluster, what it resolves in any order
// and case, and its steps in order; the title is not part of it (W-10).
func TestKey(t *testing.T) {
	steps := []string{Step("kubectl", map[string]any{"command": "kubectl cordon  n1"}), Step("kubectl", map[string]any{"command": "kubectl drain n1"})}
	k := Key("prod", []string{"C-12", "ki-3", "C-12"}, steps)
	if !strings.HasPrefix(k, KeyPrefix+"prod:C-12,KI-3:") {
		t.Fatalf("key %q", k)
	}
	if Key("prod", []string{"KI-3", "C-12"}, []string{"kubectl kubectl cordon n1", "kubectl kubectl drain n1"}) != k {
		t.Error("the same change keys differently")
	}
	for _, other := range []string{
		Key("dev", []string{"C-12", "KI-3"}, steps),
		Key("prod", []string{"C-12"}, steps),
		Key("prod", []string{"C-12", "KI-3"}, []string{steps[1], steps[0]}),
	} {
		if other == k {
			t.Errorf("another change keys the same: %q", other)
		}
	}
	if s := Step("record", map[string]any{"b": 1, "a": "x"}); s != `record {"a":"x","b":1}` {
		t.Errorf("step %q", s)
	}
}
