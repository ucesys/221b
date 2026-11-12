package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// Taking a cluster off production waits for another SuperAdmin, who sees
// it on Approvals; the one who asked cannot approve it, and may withdraw
// it with a reason.
func TestTakingAClusterOffProductionWaitsForAnotherSuperAdmin(t *testing.T) {
	store := twoperson.New(nil, nil)
	srv, ops, m, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { o.TwoPerson = store })
	local := func(env string) string {
		return `{"name":"Here","env":"` + env + `","description":"","server":"https://10.0.0.1:6443","ca":"system","auth":"incluster","mode":"read"}`
	}
	if resp, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/clusters/local", local("production")); resp.StatusCode != http.StatusOK {
		t.Fatalf("onto production: %s %s", resp.Status, b)
	}
	resp, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/clusters/local", strings.Replace(local("staging"), `"Here"`, `"Lakehouse"`, 1))
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(b, `"env":"production"`) || !strings.Contains(b, `"name":"Lakehouse"`) || !strings.Contains(b, "until another SuperAdmin approves") {
		t.Fatalf("off production: %s %s", resp.Status, b)
	}
	if sum, _ := m.Summary(clusters.LocalID); sum.Env != clusters.EnvProduction {
		t.Fatalf("the environment changed before an approval: %s", sum.Env)
	}
	// A second request for the same cluster is refused while one waits.
	if resp, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/clusters/local", local("development")); resp.StatusCode != http.StatusAccepted || !strings.Contains(b, "already waits") {
		t.Fatalf("again: %s %s", resp.Status, b)
	}
	if resp, b := send(t, ops, http.MethodPost, srv.URL+"/api/v1/admin/requests/1/approve", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("approved by whoever asked: %s %s", resp.Status, b)
	}
	ops2 := signIn(t, srv, "ops2@corp.example")
	_, b = send(t, ops2, http.MethodGet, srv.URL+"/api/v1/admin/approvals", "")
	var list struct{ Items []approvalRow }
	_ = json.Unmarshal([]byte(b), &list)
	if len(list.Items) == 0 || list.Items[0].Kind != approvalAdmin || list.Items[0].State != approvalPending || !strings.Contains(list.Items[0].What, "environment production → staging") {
		t.Fatalf("the other SuperAdmin's approvals: %s", b)
	}
	if resp, b := send(t, ops2, http.MethodPost, srv.URL+"/api/v1/admin/requests/1/approve", ""); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"state":"approved"`) {
		t.Fatalf("approve: %s %s", resp.Status, b)
	}
	if sum, _ := m.Summary(clusters.LocalID); sum.Env != clusters.EnvStaging || sum.Name != "Here" {
		t.Fatalf("after the approval: %+v", sum.Spec)
	}
	// Approved again, by someone who had it open: 409, with who and when.
	if resp, b := send(t, ops2, http.MethodPost, srv.URL+"/api/v1/admin/requests/1/approve", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(b, `"Already approved by `) {
		t.Fatalf("approved twice: %s %s", resp.Status, b)
	}
	_, b = send(t, ops, http.MethodGet, srv.URL+"/api/v1/admin/approvals", "")
	_ = json.Unmarshal([]byte(b), &list)
	if len(list.Items) == 0 || list.Items[0].State != approvalApproved || !list.Items[0].Mine || list.Items[0].ApprovedBy == "" {
		t.Fatalf("the requester's approvals: %s", b)
	}

	// Withdrawn by whoever asked, with a reason.
	send(t, ops, http.MethodPut, srv.URL+"/api/v1/clusters/local", local("production"))
	if resp, _ := send(t, ops, http.MethodPut, srv.URL+"/api/v1/clusters/local", local("other")); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("off production again: %s", resp.Status)
	}
	if resp, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/admin/requests/2/reject", `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a rejection without a reason: %s", resp.Status)
	}
	if resp, b := send(t, ops, http.MethodPost, srv.URL+"/api/v1/admin/requests/2/reject", `{"comment":"not now"}`); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"state":"rejected"`) {
		t.Fatalf("withdraw: %s %s", resp.Status, b)
	}
	if sum, _ := m.Summary(clusters.LocalID); sum.Env != clusters.EnvProduction {
		t.Fatalf("a withdrawn request changed the environment: %s", sum.Env)
	}
	if v := signIn(t, srv, "viewer@corp.example"); true {
		if resp, _ := send(t, v, http.MethodPost, srv.URL+"/api/v1/admin/requests/2/approve", ""); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a viewer decided: %s", resp.Status)
		}
	}
}
