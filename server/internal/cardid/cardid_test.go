package cardid

import "testing"

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
