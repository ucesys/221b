package api

import (
	"strings"
	"testing"
)

// review_cluster counts what the cluster's setup lacks, as the person may
// see it, in one call (Phase 6 review).
func TestSherlockReviewsTheCluster(t *testing.T) {
	srv, ops, fake, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	m := askWith(t, srv, ops, svc, map[string]any{"question": "Find deficiencies [[review_cluster]]"})
	if m.Status != "done" || len(m.Steps) != 1 || m.Steps[0].Label != "Review the cluster's setup" || m.Steps[0].Status != "done" {
		t.Fatalf("answer: %+v", m)
	}
	body := lastRequest(t, fake)
	for _, want := range []string{`\"findings\":[`, `\"area\":\"`, `\"severity\":\"`, `\"advice\":\"`, `\"scope\":\"every namespace but the system ones\"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the review lacks %s", want)
		}
	}
	// Narrowed to a namespace.
	askWith(t, srv, ops, svc, map[string]any{"question": `Review web [[review_cluster {"namespace":"web"}]]`})
	if body := lastRequest(t, fake); !strings.Contains(body, `\"scope\":\"namespace web\"`) {
		t.Fatalf("namespace scope missing")
	}
}
