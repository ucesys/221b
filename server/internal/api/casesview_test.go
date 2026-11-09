package api

import "testing"

// A question asked from a page of Cases turns Sherlock's Cases skill on
// (IB-10); any other view, or none, does not.
func TestFromCasesReadsTheView(t *testing.T) {
	for view, want := range map[string]bool{
		`{"cluster":"local","view":"cases","path":["inbox"],"item":{"kind":"proposal","id":"PC-12"}}`: true,
		`{"cluster":"local","view":"tabs","tab":"pods"}`:                                              false,
		``:         false,
		`not json`: false,
	} {
		if got := fromCases(view); got != want {
			t.Errorf("%s: %v, want %v", view, got, want)
		}
	}
}
