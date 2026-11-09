package jobs

import (
	"encoding/json"
	"testing"
)

// A run kept before CF-56 names its proposals c<n>: they read as PC-<n>,
// and one that could not be filed keeps no id.
func TestARunsOldProposalIDsReadAsPC(t *testing.T) {
	var r Run
	if err := json.Unmarshal([]byte(`{"id":"run-3","proposals":[{"id":"c4","title":"Restart web","resolves":["C-2"]},{"id":"","title":"Scale db","error":"resolves nothing"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Proposals) != 2 || r.Proposals[0].ID != "PC-4" || r.Proposals[0].Title != "Restart web" || r.Proposals[0].Resolves[0] != "C-2" || r.Proposals[1].ID != "" || r.Proposals[1].Error == "" {
		t.Errorf("proposals: %+v", r.Proposals)
	}
	b, _ := json.Marshal(r.Proposals[0])
	if string(b) != `{"id":"PC-4","title":"Restart web","resolves":["C-2"]}` {
		t.Errorf("written: %s", b)
	}
}
