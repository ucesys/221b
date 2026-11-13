package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
)

// --- Run values on production, weighed and approved (FU-04, FU-05) ---

// valuesScript reads MODE and ZONE on the cluster; neither is in a
// condition, so only a sensitive mark makes a change need approval.
const valuesScript = "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:dry-run yes\n# 221b:k8s get pods\n# 221b:param MODE optional m\n# 221b:param ZONE optional z\nkubectl get pods -A\n"

// valuesSetup is a production cluster where All nodes set MODE, marked
// sensitive, and ZONE and OTHER, which are not.
func valuesSetup(t *testing.T) autoEnv {
	t.Helper()
	e := autoSetup(t, "ops@corp.example", clusters.EnvProduction)
	approved(t, e.store, "reads", valuesScript)
	if err := e.store.SetVariables(context.Background(), clusters.LocalID, allNodes, automation.FleetVariables{Values: map[string]string{"MODE": "keep", "ZONE": "eu", "OTHER": "us"}, Sensitive: []string{"MODE"}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func doAs(t *testing.T, c *http.Client, e autoEnv, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "221b")
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func runBody(params string) string {
	return `{"script":"reads","fleet":"_cluster","params":` + params + `,"rollout":{"mode":"all"}}`
}

func TestOnProductionValuesThatNeedNoApprovalStartAtOnce(t *testing.T) {
	e := valuesSetup(t)
	// ZONE is not sensitive and no condition reads it; binding it to OTHER
	// is a change, which needs no approval either.
	code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"var:OTHER"}`))
	if code != http.StatusOK || !strings.Contains(body, `"overrides":{"ZONE":"var:OTHER"}`) || !strings.Contains(body, `"ZONE":"us"`) || !strings.Contains(body, "its own values need no approval") {
		t.Fatalf("%d %s", code, body)
	}
	// The same value every node inherits changes nothing.
	if code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"keep"}`)); code != http.StatusOK || !strings.Contains(body, "change nothing a node inherits") {
		t.Fatalf("%d %s", code, body)
	}
	// A dry run is as before: never weighed.
	if code, body := e.do(t, "POST", "/api/v1/automation/runs", `{"script":"reads","fleet":"_cluster","dryRun":true,"params":{"MODE":"wipe"},"rollout":{"mode":"all"}}`); code != http.StatusOK {
		t.Fatalf("a dry run: %d %s", code, body)
	}
}

func TestOnProductionValuesThatNeedApprovalWaitForASecondAdmin(t *testing.T) {
	e := valuesSetup(t)
	// The preview says it would wait, and records nothing.
	code, body := e.do(t, "POST", "/api/v1/automation/runs/weigh", runBody(`{"MODE":"wipe"}`))
	if code != http.StatusOK || !strings.Contains(body, `"needsApproval":true`) || !strings.Contains(body, "MODE is marked sensitive") {
		t.Fatalf("preview: %d %s", code, body)
	}
	if list, _ := e.store.WaitingRuns(context.Background(), ""); len(list) != 0 {
		t.Fatalf("a preview kept %+v", list)
	}
	code, body = e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"wipe"}`))
	if code != http.StatusAccepted || !strings.Contains(body, `"waiting":{"id":1,`) || !strings.Contains(body, `"changed":["MODE"]`) {
		t.Fatalf("%d %s", code, body)
	}
	if runs, _ := e.engine.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("it started: %+v", runs)
	}
	if !strings.Contains(e.audit.String(), "run request 1 waits for approval") {
		t.Fatal("the request is not in the audit log")
	}
	// It waits on Approvals, for the person to withdraw and for others.
	if _, body := e.do(t, "GET", "/api/v1/admin/approvals", ""); !strings.Contains(body, `"kind":"run","state":"pending"`) || !strings.Contains(body, `"own":true`) {
		t.Fatalf("approvals: %s", body)
	}
	// Whoever asked cannot approve it.
	if code, body := e.do(t, "POST", "/api/v1/automation/waiting-runs/1/approve", ""); code != http.StatusForbidden {
		t.Fatalf("own approval: %d %s", code, body)
	}
	other := signIn(t, e.srv, "ops2@corp.example")
	code, body = doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/1/approve", "")
	if code != http.StatusOK {
		t.Fatalf("approval: %d %s", code, body)
	}
	var ans struct {
		Waiting automation.WaitingRun `json:"waiting"`
		Run     automation.Run        `json:"run"`
	}
	if err := json.Unmarshal([]byte(body), &ans); err != nil {
		t.Fatal(err)
	}
	// It starts as the person who asked, with the values they gave.
	if ans.Waiting.State != automation.ChangeApproved || ans.Waiting.Run != ans.Run.ID || ans.Run.By.Name != "Ops" || ans.Run.ValuesApproved == nil || ans.Run.ValuesApproved.By.Name != "Ops Two" ||
		ans.Run.Overrides["MODE"] != "wipe" || ans.Run.Nodes[0].Values["MODE"] != "wipe" || !strings.Contains(ans.Run.Basis, "approved by Ops Two (run request 1)") {
		t.Fatalf("%+v", ans)
	}
	if !strings.Contains(e.audit.String(), "run request 1 approved: run 1 started as Ops") {
		t.Fatal("the approval is not in the audit log")
	}
	// Decided, it is not approved again.
	if code, _ := doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/1/approve", ""); code != http.StatusBadRequest {
		t.Fatalf("approved twice: %d", code)
	}
}

func TestAWaitingRunIsRejectedWithAReason(t *testing.T) {
	e := valuesSetup(t)
	if code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"wipe"}`)); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	other := signIn(t, e.srv, "ops2@corp.example")
	if code, _ := doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/1/reject", `{"comment":""}`); code != http.StatusBadRequest {
		t.Fatalf("a rejection without a reason: %d", code)
	}
	code, body := doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/1/reject", `{"comment":"not this week"}`)
	if code != http.StatusOK || !strings.Contains(body, `"state":"rejected"`) {
		t.Fatalf("%d %s", code, body)
	}
	if runs, _ := e.engine.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("a rejected run started: %+v", runs)
	}
	if !strings.Contains(e.audit.String(), "run request 1 rejected") {
		t.Fatal("the rejection is not in the audit log")
	}
	if _, body := e.do(t, "GET", "/api/v1/admin/approvals", ""); !strings.Contains(body, `"kind":"run","state":"rejected"`) || !strings.Contains(body, `"comment":"not this week"`) {
		t.Fatalf("approvals: %s", body)
	}
}

func TestAnApprovedRunIsCheckedAgainWhenItStarts(t *testing.T) {
	e := valuesSetup(t)
	if code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"wipe"}`)); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	// The version is revoked meanwhile: the approval cannot start it, and
	// it keeps waiting.
	if _, err := e.store.Revoke(context.Background(), "reads", 1, automation.Who{Name: "Sam", User: "sam"}); err != nil {
		t.Fatal(err)
	}
	other := signIn(t, e.srv, "ops2@corp.example")
	code, body := doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/1/approve", "")
	if code < 400 || !strings.Contains(body, "Run request 1 can't start now, so it still waits") {
		t.Fatalf("%d %s", code, body)
	}
	if list, _ := e.store.WaitingRuns(context.Background(), clusters.LocalID); list[0].State != automation.ChangeWaiting {
		t.Fatalf("%+v", list[0])
	}
}

func TestABindingNoTargetHasIsRefused(t *testing.T) {
	e := valuesSetup(t)
	for _, body := range []string{runBody(`{"ZONE":"var:NOWHERE"}`), `{"script":"reads","fleet":"_cluster","dryRun":true,"params":{"ZONE":"var:NOWHERE"},"rollout":{"mode":"all"}}`} {
		code, answer := e.do(t, "POST", "/api/v1/automation/runs", body)
		if code != http.StatusBadRequest || !strings.Contains(answer, "ZONE is bound to variable NOWHERE, which no node of this run has") {
			t.Fatalf("%d %s", code, answer)
		}
	}
	if code, answer := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"var:lower"}`)); code != http.StatusBadRequest || !strings.Contains(answer, `write \\var:`) {
		t.Fatalf("%d %s", code, answer)
	}
	// The preview says so too, and records nothing.
	if code, answer := e.do(t, "POST", "/api/v1/automation/runs/weigh", runBody(`{"ZONE":"var:NOWHERE"}`)); code != http.StatusOK || !strings.Contains(answer, `"refused":"ZONE is bound`) {
		t.Fatalf("%d %s", code, answer)
	}
}

func TestARequiredParameterBoundToAMissingVariableIsRefused(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	approved(t, e.store, "reads", strings.Replace(valuesScript, "MODE optional", "MODE required", 1))
	code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"var:NOWHERE"}`))
	if code != http.StatusBadRequest || !strings.Contains(body, "which no node of this run has") {
		t.Fatalf("%d %s", code, body)
	}
	// A constant that starts like a binding is the text.
	code, body = e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"\\var:NOWHERE"}`))
	if code != http.StatusOK || !strings.Contains(body, `"MODE":"var:NOWHERE"`) {
		t.Fatalf("%d %s", code, body)
	}
}
