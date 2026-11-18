package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
)

// On production a run's own values are weighed as a change of the
// fleet's values (FU-05): one no approved script needs approved starts at
// once, as before the values were refused there. A dry run may too.
func TestOnProductionARunMayGiveValuesOfItsOwn(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvProduction)
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:dry-run yes\n# 221b:k8s get pods\n# 221b:param MODE optional m\nkubectl get pods -A\n"
	approved(t, e.store, "reads", text)
	code, body := e.do(t, "POST", "/api/v1/automation/runs", `{"script":"reads","fleet":"_cluster","params":{"MODE":"wipe"},"rollout":{"mode":"all"}}`)
	if code != http.StatusOK || !strings.Contains(body, `"MODE":"wipe"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := e.do(t, "POST", "/api/v1/automation/runs", `{"script":"reads","fleet":"_cluster","dryRun":true,"params":{"MODE":"wipe"},"rollout":{"mode":"all"}}`); code != 200 {
		t.Fatalf("a dry run: %d %s", code, body)
	}
}

// A start sent twice with the same request starts one run.
func TestAStartSentTwiceStartsOneRun(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	approved(t, e.store, "reads", autoKube)
	req := `{"script":"reads","fleet":"_cluster","rollout":{"mode":"all"},"request":"abc-1"}`
	_, first := e.do(t, "POST", "/api/v1/automation/runs", req)
	code, second := e.do(t, "POST", "/api/v1/automation/runs", req)
	if code != 200 || !strings.Contains(first, `"id":1,`) || !strings.Contains(second, `"id":1,`) {
		t.Fatalf("%d\n%s\n%s", code, first, second)
	}
}
