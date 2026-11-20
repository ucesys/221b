package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
)

// approvalsOf reads a person's approvals: what waits, as the Inbox lists it.
func approvalsOf(t *testing.T, url string, c *http.Client) (pending []approvalRow, all []approvalRow) {
	t.Helper()
	r, b := send(t, c, http.MethodGet, url+"/api/v1/admin/approvals", "")
	var got struct {
		Items []approvalRow `json:"items"`
	}
	if err := json.Unmarshal([]byte(b), &got); r.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("approvals: %s %v %s", r.Status, err, b)
	}
	for _, it := range got.Items {
		if it.State == approvalPending {
			pending = append(pending, it)
		}
	}
	return pending, got.Items
}

// oneOf is the only pending row of a kind, or a failure.
func oneOf(t *testing.T, rows []approvalRow, kind, what string) approvalRow {
	t.Helper()
	var out []approvalRow
	for _, r := range rows {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%s: one pending %s row is wanted, got %+v", what, kind, rows)
	}
	return out[0]
}

// What a person submitted and cannot approve themselves is a row of their
// own Inbox, marked as waiting for another SuperAdmin; the other SuperAdmin
// has the row to approve, unmarked; whoever is no SuperAdmin has neither.
// Decided, or taken back, the row is gone (IN-11).
func TestASubmittersOwnRowWaitsForAnotherSuperAdminIN11(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	ctx := context.Background()
	_, idx := e.do(t, "GET", "/api/v1/automation", "")
	var index struct {
		Me automation.Who `json:"me"`
	}
	if err := json.Unmarshal([]byte(idx), &index); err != nil || index.Me.Name == "" {
		t.Fatalf("%v %s", err, idx)
	}
	me := index.Me
	for _, name := range []string{"mine", "turned-down"} {
		if _, err := e.store.Save(ctx, name, "", autoKube, me, true); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.Submit(ctx, name, 1, me); err != nil {
			t.Fatal(err)
		}
	}
	// A draft of theirs is no row: nothing was submitted.
	if _, err := e.store.Save(ctx, "draft", "", autoKube, me, true); err != nil {
		t.Fatal(err)
	}
	change, err := e.store.NewChange(ctx, automation.VariableChange{Cluster: clusters.LocalID, Fleet: "_all", By: me, State: automation.ChangeWaiting, Changed: []string{"A"}})
	if err != nil {
		t.Fatal(err)
	}

	// The submitter: both versions and the change, their own, with who decides.
	pending, _ := approvalsOf(t, e.srv.URL, e.c)
	if len(pending) != 3 {
		t.Fatalf("the submitter's rows: %+v", pending)
	}
	for _, r := range pending {
		if !r.Own || r.WaitingFor != "another SuperAdmin" || r.RequestedBy != me.Name || r.Mine {
			t.Fatalf("the submitter's row is not marked: %+v", r)
		}
	}
	// They still cannot approve or reject what they submitted.
	if code, _ := e.do(t, "POST", "/api/v1/automation/scripts/mine/versions/1/approve", "{}"); code != http.StatusForbidden {
		t.Fatalf("the submitter approved their own version: %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/v1/automation/scripts/mine/versions/1/reject", `{"comment":"no"}`); code != http.StatusForbidden {
		t.Fatalf("the submitter rejected their own version: %d", code)
	}

	// The second SuperAdmin: the same three, to decide, unmarked.
	ops2 := signIn(t, e.srv, "ops2@corp.example")
	pending, _ = approvalsOf(t, e.srv.URL, ops2)
	if len(pending) != 3 {
		t.Fatalf("the second SuperAdmin's rows: %+v", pending)
	}
	for _, r := range pending {
		if r.Own || r.WaitingFor != "" || r.RequestedBy != me.Name {
			t.Fatalf("the approver's row is marked as their own: %+v", r)
		}
	}

	// Whoever is no SuperAdmin has neither row.
	viewer := signIn(t, e.srv, "viewer@corp.example")
	if r, b := send(t, viewer, http.MethodGet, e.srv.URL+"/api/v1/admin/approvals", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer read the approvals: %s %s", r.Status, b)
	}

	// Approved by the other SuperAdmin, the row is gone from both; the
	// submitter has it below, decided by someone else.
	if r, b := send(t, ops2, http.MethodPost, e.srv.URL+"/api/v1/automation/scripts/mine/versions/1/approve", "{}"); r.StatusCode != http.StatusOK {
		t.Fatalf("approve: %s %s", r.Status, b)
	}
	pending, all := approvalsOf(t, e.srv.URL, e.c)
	if len(pending) != 2 {
		t.Fatalf("after the approval: %+v", pending)
	}
	decided := false
	for _, r := range all {
		if r.Script == "mine" {
			decided = r.State == approvalApproved && r.Mine && !r.Own && r.WaitingFor == ""
		}
	}
	if !decided {
		t.Fatalf("the approved version, for whoever submitted it: %+v", all)
	}
	// Rejected, the same.
	if r, b := send(t, ops2, http.MethodPost, e.srv.URL+"/api/v1/automation/scripts/turned-down/versions/1/reject", `{"comment":"use the mirror"}`); r.StatusCode != http.StatusOK {
		t.Fatalf("reject: %s %s", r.Status, b)
	}
	pending, _ = approvalsOf(t, e.srv.URL, e.c)
	if got := oneOf(t, pending, approvalChange, "after the rejection"); len(pending) != 1 || got.ID != "1" {
		t.Fatalf("after the rejection: %+v", pending)
	}
	// The change taken back by whoever asked (a rejection of their own, with its reason): gone from both.
	if code, body := e.do(t, "POST", "/api/v1/automation/variable-changes/1/reject", `{"comment":"asked by mistake"}`); code != http.StatusOK {
		t.Fatalf("withdraw %d: %d %s", change.ID, code, body)
	}
	if pending, _ = approvalsOf(t, e.srv.URL, e.c); len(pending) != 0 {
		t.Fatalf("after the withdrawal: %+v", pending)
	}
	if pending, _ = approvalsOf(t, e.srv.URL, ops2); len(pending) != 0 {
		t.Fatalf("the second SuperAdmin, after all were decided: %+v", pending)
	}

	// A version someone else wrote and this person submitted is theirs too;
	// one they had no part in is theirs to approve.
	ann := automation.Who{Name: "Ann", User: "ann"}
	if _, err := e.store.Save(ctx, "anns", "", autoKube, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Submit(ctx, "anns", 1, me); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Save(ctx, "others", "", autoKube, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Submit(ctx, "others", 1, ann); err != nil {
		t.Fatal(err)
	}
	pending, _ = approvalsOf(t, e.srv.URL, e.c)
	if len(pending) != 2 {
		t.Fatalf("submitted for Ann, and Ann's own: %+v", pending)
	}
	for _, r := range pending {
		if own := r.Script == "anns"; r.Own != own || (r.WaitingFor != "") != own {
			t.Fatalf("%s: %+v", r.Script, r)
		}
	}
}

// A Watson command that takes a second person is a row of whoever sent
// it, marked as waiting for one; anyone else who may approve it has the
// row unmarked, and the row goes once it is approved (IN-11).
func TestAWatsonCommandOfOnesOwnWaitsForASecondPersonIN11(t *testing.T) {
	srv, ops, _, _, _ := watsonSetup(t, "ops@corp.example", clusters.EnvProduction, clusters.ModeWrite)
	if r, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/watson/config", `{"twoPerson":true}`); r.StatusCode != 200 {
		t.Fatalf("config %d %s", r.StatusCode, b)
	}
	it := submit(t, srv, ops, "kubectl cordon worker-003", true)
	watsonStream(t, srv, ops, func(m map[string]watsonItem) bool { return m[it.ID].Plan != nil && m[it.ID].Plan.Ready })
	pending, _ := approvalsOf(t, srv.URL, ops)
	if r := oneOf(t, pending, approvalCommand, "whoever sent it"); !r.Own || r.WaitingFor != "a second person" || r.ID != it.ID {
		t.Fatalf("the requester's row: %+v", r)
	}
	ops2 := signIn(t, srv, "ops2@corp.example")
	pending, _ = approvalsOf(t, srv.URL, ops2)
	if r := oneOf(t, pending, approvalCommand, "the second person"); r.Own || r.WaitingFor != "" || r.ID != it.ID {
		t.Fatalf("the second person's row: %+v", r)
	}
	if r, b := send(t, ops2, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+it.ID+"/approve", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	if pending, _ = approvalsOf(t, srv.URL, ops); len(pending) != 0 {
		t.Fatalf("after the approval: %+v", pending)
	}
}
