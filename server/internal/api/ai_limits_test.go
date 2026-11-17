package api

import (
	"net/http"
	"strings"
	"testing"
)

// Settings › Limits sets the total and each provider's limits in one
// change: each is checked, 0 is no limit, the overview shows them with
// the providers' spend, and the audit log says what changed (AI-36).
func TestAILimitsAPI(t *testing.T) {
	srv, ops, _, _, _, aud := aiSetupWith(t, nil)
	for _, bad := range []string{
		`{"dailyUSD":30,"monthlyUSD":20}`,
		`{"dailyUSD":-1,"monthlyUSD":20}`,
		`{"dailyUSD":1,"monthlyUSD":20,"providers":{"nowhere":{"dailyUSD":1}}}`,
		`{"dailyUSD":1,"monthlyUSD":20,"providers":{"default":{"dailyUSD":5,"monthlyUSD":2}}}`,
		`{"dailyUSD":1,"monthlyUSD":20,"providers":{"default":{"dailyUSD":200000}}}`,
	} {
		if r, text := send(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/limits", bad); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, r.StatusCode, text)
		}
	}
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/limits", `{"dailyUSD":0,"monthlyUSD":150,"providers":{"default":{"dailyUSD":4,"monthlyUSD":0}}}`, 200, &o)
	if o.DailyUSD != 0 || o.MonthlyUSD != 150 || len(o.Providers) != 1 || o.Providers[0].Limit.DailyUSD != 4 || o.Providers[0].Limit.MonthlyUSD != 0 {
		t.Fatalf("%+v %+v", o.Overview.DailyUSD, o.Providers)
	}
	if !strings.Contains(aud.String(), "total limits $20.00/$200.00 → $0.00/$150.00 a day/month; provider default limits $0.00/$0.00 → $4.00/$0.00 a day/month") {
		t.Fatalf("audit: %s", aud.String())
	}
	// The total alone leaves the providers' as they are, and says only that.
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/limits", `{"dailyUSD":10,"monthlyUSD":150}`, 200, &o)
	if o.Providers[0].Limit.DailyUSD != 4 {
		t.Fatalf("%+v", o.Providers[0].Limit)
	}
	if !strings.Contains(aud.String(), "total limits $0.00/$150.00 → $10.00/$150.00 a day/month") {
		t.Fatalf("audit: %s", aud.String())
	}
	if strings.Count(aud.String(), "provider default limits") != 1 {
		t.Fatalf("an unchanged provider was audited: %s", aud.String())
	}
	// Only SuperAdmins change them.
	viewer := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, viewer, http.MethodPut, srv.URL+"/api/v1/ai/limits", `{"dailyUSD":0,"monthlyUSD":0}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer: %d", r.StatusCode)
	}
}
