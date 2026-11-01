package watson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	release "helm.sh/helm/v4/pkg/release/v1"
)

// helm status shows a release's notes as the Helm tab does, masked, in
// every format.
func TestHelmStatusMasksTheNotes(t *testing.T) {
	rel := &release.Release{Name: "shop", Info: &release.Info{Notes: "Log in as admin / hunter2typed. See the docs."}, Config: map[string]any{"adminPass": "hunter2typed"}}
	var table, doc bytes.Buffer
	if err := (statusPrinter{rel: rel}).WriteTable(&table); err != nil {
		t.Fatal(err)
	}
	if err := (statusPrinter{rel: rel}).WriteJSON(&doc); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(doc.Bytes()) {
		t.Fatalf("not JSON: %s", doc.String())
	}
	for _, out := range []string{table.String(), doc.String()} {
		if strings.Contains(out, "hunter2") || !strings.Contains(out, "See the docs.") {
			t.Errorf("status: %s", out)
		}
	}
	if !strings.Contains(rel.Info.Notes, "hunter2typed") {
		t.Error("the release itself changed")
	}
}
