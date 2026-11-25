package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
)

// The fixes of the review of server/internal/automation (November 24,
// 2026), through the API.

// On production a sensitive value reaches a parameter of another name only
// with a second admin (FU-05, review finding 1): a binding to a sensitive
// variable is weighed as sensitive, and a digest given under a name that
// is not sensitive is refused, as the variables refuse it.
func TestOnProductionASensitiveValueNeedsASecondAdminUnderAnyName(t *testing.T) {
	e := valuesSetup(t)
	code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"var:MODE"}`))
	if code != http.StatusAccepted || !strings.Contains(body, "ZONE is marked sensitive") {
		t.Fatalf("a binding to the sensitive MODE: %d %s", code, body)
	}
	if runs, _ := e.engine.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("it started: %+v", runs)
	}
	vars, err := e.store.Variables(context.Background(), clusters.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	digest := vars[allNodes].Values["MODE"]
	code, body = e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"`+digest+`"}`))
	if code != http.StatusBadRequest || !strings.Contains(body, "ZONE is given a hidden value") {
		t.Fatalf("MODE's digest under ZONE: %d %s", code, body)
	}
	if runs, _ := e.engine.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("it started: %+v", runs)
	}
}
