package issues

import (
	"encoding/json"
	"testing"
)

// A case fixed before CF-56 names the change c<n>: it reads as PC-<n>.
func TestAnOldAppliedChangeReadsAsPC(t *testing.T) {
	var r Case
	if err := json.Unmarshal([]byte(`{"number":3,"appliedBy":{"card":"c12","title":"Set web's limits","by":"Pat","at":"2026-11-06T10:00:00Z"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if a := r.AppliedBy; a == nil || a.Card != "PC-12" || a.Title != "Set web's limits" || a.By != "Pat" || a.At.IsZero() {
		t.Errorf("applied by: %+v", r.AppliedBy)
	}
}
