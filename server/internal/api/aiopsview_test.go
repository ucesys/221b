package api

import "testing"

// A question asked from a page of AIOps turns Sherlock's AIOps skill on
// (IB-10); any other view, or none, does not. A page loaded before the
// area's id was aiops says "cases" (CF-104).
func TestFromAIOpsReadsTheView(t *testing.T) {
	for view, want := range map[string]bool{
		`{"cluster":"local","view":"aiops","path":["inbox"],"item":{"kind":"proposal","id":"PC-12"}}`: true,
		`{"cluster":"local","view":"cases","path":["inbox"]}`:                                         true,
		`{"cluster":"local","view":"tabs","tab":"pods"}`:                                              false,
		``:         false,
		`not json`: false,
	} {
		if got := fromAIOps(view); got != want {
			t.Errorf("%s: %v, want %v", view, got, want)
		}
	}
}
