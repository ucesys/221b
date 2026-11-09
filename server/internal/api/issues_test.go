package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
)

// issuesSetup serves a console with the issue service, local sign-in and
// four people: Rita who only reports, Vera a Viewer, Adam an Admin and
// Sam a SuperAdmin.
func issuesSetup(t *testing.T, rolesOf IssueRoles) (*httptest.Server, *issues.Service, *bytes.Buffer) {
	t.Helper()
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	hash, _ := auth.HashPassword("correct horse battery staple")
	var auditOut bytes.Buffer
	log := audit.New(audit.Options{Out: &auditOut})
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeLocal,
		Local: []auth.LocalUser{
			{Username: "rita", Name: "Rita", Groups: []string{"reporters"}, PasswordHash: hash},
			{Username: "vera", Name: "Vera", Groups: []string{"web-readers"}, PasswordHash: hash},
			{Username: "adam", Name: "Adam", Groups: []string{"web-ops"}, PasswordHash: hash},
			{Username: "sam", Name: "Sam", Roles: []string{"superAdmin"}, PasswordHash: hash},
		},
		LocalGroups: []auth.LocalGroup{{Name: "reporters", Role: roles.Reporter}, {Name: "web-readers", Role: roles.Viewer, Namespaces: []string{"web"}}, {Name: "web-ops", Role: roles.Admin, Namespaces: []string{"web"}}},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, quietLog(), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	is := issues.New(issues.Options{Logger: quietLog(), Audit: func(a issues.AuditRecord) {
		log.Record(audit.Event{Action: a.Action, Who: audit.Who{Subject: a.Who.ID, Name: a.Who.Name}, Target: a.Target, Detail: a.Detail, Outcome: a.Outcome, From: a.From})
	}})
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app:"+r.URL.Path) })
	srv := httptest.NewServer(New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Auth: svc, Audit: log, UI: ui, Logger: quietLog(), Issues: is, IssueRoles: rolesOf}))
	t.Cleanup(srv.Close)
	return srv, is, &auditOut
}

var defaultIssueRoles = IssueRoles{Report: []roles.ID{roles.SuperAdmin, roles.SuperViewer, roles.Admin, roles.Viewer}, Operate: []roles.ID{roles.SuperAdmin, roles.Admin}}

// A person who holds only the Reporter role signs in like anyone else and
// reaches the report page and its API, and nothing else: the console's
// pages send them to /report, its API refuses them (IS-20).
func TestAReporterSeesOnlyTheReportPage(t *testing.T) {
	srv, _, _ := issuesSetup(t, defaultIssueRoles)
	rita := signIn(t, srv, "rita")
	rita.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := send(t, rita, "GET", srv.URL+"/", "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/report" {
		t.Fatalf("the console: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := send(t, rita, "GET", srv.URL+"/report/mine", ""); resp.StatusCode != 200 || body != "app:/report/mine" {
		t.Fatalf("the report page: %d %s", resp.StatusCode, body)
	}
	for _, p := range []string{"/api/v1/snapshot", "/api/v1/settings", "/api/v1/clusters", "/api/v1/admin/roles"} {
		if resp, _ := send(t, rita, "GET", srv.URL+p, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	if resp, body := send(t, rita, "GET", srv.URL+"/api/v1/me", ""); resp.StatusCode != 200 || !strings.Contains(body, `"reporter"`) {
		t.Fatalf("me: %d %s", resp.StatusCode, body)
	}
	resp, body := send(t, rita, "GET", srv.URL+"/api/issues/v1/me", "")
	var me struct{ Report, Operator, Admin bool }
	json.Unmarshal([]byte(body), &me)
	if resp.StatusCode != 200 || !me.Report || me.Operator || me.Admin {
		t.Fatalf("issues me: %d %s", resp.StatusCode, body)
	}
	if resp, body := send(t, rita, "POST", srv.URL+"/api/issues/v1/reports", `{"text":"The wiki is down"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("report: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, rita, "GET", srv.URL+"/api/issues/v1/reports?scope=all", ""); resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	// A Viewer keeps the console, and reports too.
	vera := signIn(t, srv, "vera")
	if resp, body := send(t, vera, "GET", srv.URL+"/", ""); resp.StatusCode != 200 || body != "app:/" {
		t.Fatalf("a Viewer's console: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, vera, "POST", srv.URL+"/api/issues/v1/reports", `{"text":"Slow"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a Viewer's report: %d", resp.StatusCode)
	}
}

// Who may report and who triages follows the chart's lists: an Admin
// triages by default, a Viewer does not; a role left out of reportRoles
// cannot report; settings are a SuperAdmin's (IS-20, IS-21, IS-29).
func TestTheIssueRolesFollowTheChart(t *testing.T) {
	srv, _, auditOut := issuesSetup(t, IssueRoles{Report: []roles.ID{roles.Admin}, Operate: []roles.ID{roles.SuperAdmin, roles.Admin}})
	vera, adam, sam, rita := signIn(t, srv, "vera"), signIn(t, srv, "adam"), signIn(t, srv, "sam"), signIn(t, srv, "rita")
	if resp, _ := send(t, vera, "POST", srv.URL+"/api/issues/v1/reports", `{"text":"x"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer left out of reportRoles reported: %d", resp.StatusCode)
	}
	if resp, _ := send(t, rita, "POST", srv.URL+"/api/issues/v1/reports", `{"text":"The VPN drops every hour"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a Reporter: %d", resp.StatusCode)
	}
	if resp, body := send(t, adam, "PATCH", srv.URL+"/api/issues/v1/reports/1", `{"priority":2,"owner":{"id":"adam","name":"Adam"}}`); resp.StatusCode != 200 || !strings.Contains(body, `"priority":2`) {
		t.Fatalf("an Admin triaging: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, vera, "GET", srv.URL+"/api/issues/v1/reports/1", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a Viewer read someone's report: %d", resp.StatusCode)
	}
	if resp, _ := send(t, adam, "GET", srv.URL+"/api/issues/v1/settings", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin read the settings: %d", resp.StatusCode)
	}
	resp, body := send(t, sam, "PUT", srv.URL+"/api/issues/v1/settings", `{"company":"Example IT","matchThreshold":75,"retentionDays":365,"ratePerHour":10,"reopenDays":7,"fixedDays":3,"needInfoDays":5,"duplicateHours":6,"bumpP2At":3,"bumpP1At":10,"targets":{"0":{"responseMinutes":15,"resolveMinutes":240}},"webhook":{"url":""},"statusPage":"","reportURL":""}`)
	if resp.StatusCode != 200 {
		t.Fatalf("a SuperAdmin's settings: %d %s", resp.StatusCode, body)
	}
	resp, body = send(t, sam, "POST", srv.URL+"/api/issues/v1/tokens", `{"name":"Alerts","account":"alertmanager","scopes":["reports:create"]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a token: %d %s", resp.StatusCode, body)
	}
	var made struct{ Secret string }
	json.Unmarshal([]byte(body), &made)
	// The intake needs no session, only the token; and no browser page
	// can send it, since it carries no cookie that counts.
	req, _ := http.NewRequest("POST", srv.URL+"/api/issues/v1/intake/reports", strings.NewReader(`{"text":"disk 95% full on db-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+made.Secret)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("the intake: %d", r2.StatusCode)
	}
	if r3, _ := http.Get(srv.URL + "/api/issues/v1/reports"); r3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", r3.StatusCode)
	}
	for _, a := range []string{issues.ActionReport, issues.ActionTriage, issues.ActionSettings, issues.ActionToken} {
		if !strings.Contains(auditOut.String(), `"action":"`+a+`"`) {
			t.Errorf("no %s in the audit log", a)
		}
	}
	if strings.Contains(auditOut.String(), made.Secret) {
		t.Fatal("the token reached the audit log")
	}
}
