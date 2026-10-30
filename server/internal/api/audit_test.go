package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
)

// The Audit log page (asked October 30, 2026): the kept events, newest
// first, searched by words, narrowed by action, for a SuperAdmin alone.
func TestTheAuditLogPageListsAndFilters(t *testing.T) {
	st := &audit.MemoryStore{}
	var log *audit.Log
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Audit.SetStore(st)
		log = o.Audit
	})
	log.Record(audit.Event{Action: audit.ActionSettings, Who: audit.Who{Email: "ops@corp.example"}, Target: "settings", Detail: "refresh 9s"})
	log.Record(audit.Event{Action: audit.ActionClusterAdd, Who: audit.Who{Email: "ops@corp.example"}, Target: "staging", Outcome: audit.OutcomeFailed})
	read := func(query string) auditPage {
		resp, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/audit"+query, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s %s", query, resp.Status, body)
		}
		var p auditPage
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := read("")
	if !p.Stored || p.Total != 2 || len(p.Events) != 2 || p.Events[0].Action != audit.ActionClusterAdd || len(p.Actions) != 2 {
		t.Fatalf("the page: %+v", p)
	}
	if p := read("?q=refresh+9s"); len(p.Events) != 1 || p.Events[0].Detail != "refresh 9s" {
		t.Fatalf("searching: %+v", p.Events)
	}
	if p := read("?q=nothing+like+it"); len(p.Events) != 0 || p.Total != 2 {
		t.Fatalf("a search with no match: %+v", p)
	}
	if p := read("?action=cluster"); len(p.Events) != 1 || p.Events[0].Target != "staging" {
		t.Fatalf("by action: %+v", p.Events)
	}
	if p := read("?outcome=failed&limit=1"); len(p.Events) != 1 || p.Events[0].Outcome != audit.OutcomeFailed {
		t.Fatalf("by outcome: %+v", p.Events)
	}
	v := signIn(t, srv, "viewer@corp.example")
	if resp, _ := send(t, v, http.MethodGet, srv.URL+"/api/v1/admin/audit", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer read the audit log: %s", resp.Status)
	}
}
