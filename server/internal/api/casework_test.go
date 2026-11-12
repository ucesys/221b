package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
)

// AIOps › Cases (CW-10): the approvers (here Admins, as the chart's
// default) open, change and close cases; a SuperViewer reads them; a
// Viewer reads them only once the jobs' settings let Viewers edit jobs.
func TestCaseworkAccess(t *testing.T) {
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	hash, _ := auth.HashPassword("correct horse battery staple")
	log := audit.New(audit.Options{Out: io.Discard})
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeLocal,
		Local: []auth.LocalUser{
			{Username: "vera", Name: "Vera", Groups: []string{"web-readers"}, PasswordHash: hash},
			{Username: "adam", Name: "Adam", Groups: []string{"web-ops"}, PasswordHash: hash},
			{Username: "sue", Name: "Sue", Roles: []string{"superViewer"}, PasswordHash: hash},
			{Username: "sam", Name: "Sam", Roles: []string{"superAdmin"}, PasswordHash: hash},
		},
		LocalGroups: []auth.LocalGroup{{Name: "web-readers", Role: roles.Viewer, Namespaces: []string{"web"}}, {Name: "web-ops", Role: roles.Admin, Namespaces: []string{"web"}}},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, quietLog(), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := jobs.NewRegistry()
	js := jobs.New(jobs.Options{Kinds: kinds, Logger: quietLog()})
	t.Cleanup(js.Stop)
	cw := casework.New(casework.Options{Jobs: js, Logger: quietLog(), Audit: log})
	t.Cleanup(cw.Stop)
	kinds.Register(cw.Kind())
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })
	h := New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Auth: svc, Audit: log, UI: ui, Logger: quietLog(), Jobs: js, Casework: cw, CardRoles: []roles.ID{roles.Admin}})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	vera, adam, sue, sam := signIn(t, srv, "vera"), signIn(t, srv, "adam"), signIn(t, srv, "sue"), signIn(t, srv, "sam")
	url := srv.URL + casework.Prefix
	if resp, _ := send(t, vera, "GET", url, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer lists the cases: %d", resp.StatusCode)
	}
	if resp, body := send(t, adam, "POST", url, `{"title": "The shop is slow", "cluster": "local", "resource": "web/Deployment/shop", "priority": 2}`); resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"id":"C-1"`) || !strings.Contains(body, `"createdBy":{"name":"Adam","kind":"person"}`) {
		t.Fatalf("an approver opens a case: %d %s", resp.StatusCode, body)
	}
	if resp, body := send(t, sue, "GET", url, ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"C-1"`) || !strings.Contains(body, `"mayChange":false`) {
		t.Fatalf("a SuperViewer lists the cases: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, sue, "POST", url, `{"title": "x", "cluster": "local"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a SuperViewer opens a case: %d", resp.StatusCode)
	}
	if resp, body := send(t, sam, "GET", url+"/C-1", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"mayChange":true`) || !strings.Contains(body, `"cards":[]`) {
		t.Fatalf("a SuperAdmin reads a case: %d %s", resp.StatusCode, body)
	}
	if resp, body := send(t, sam, "PUT", url+"/1", `{"priority": 1}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"priority":1`) {
		t.Fatalf("change: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, sam, "POST", url+"/C-1/analyze", `{}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("analyse while the job is paused: %d", resp.StatusCode)
	}
	if resp, _ := send(t, sam, "POST", url+"/C-1/rca-draft", `{}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("draft while the job is paused: %d", resp.StatusCode)
	}
	if resp, _ := send(t, sam, "POST", url+"/C-1/fix", `{"text": "done"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a P1 fix without its RCA: %d", resp.StatusCode)
	}
	if resp, body := send(t, adam, "POST", url+"/C-1/fix", `{"text": "done", "rca": {"cause": "c", "fix": "f", "prevention": "p"}}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"closed"`) || !strings.Contains(body, `"outcome":"fix"`) {
		t.Fatalf("fix: %d %s", resp.StatusCode, body)
	}
	if resp, body := send(t, adam, "POST", url+"/C-1/reopen", `{"reason": "back"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"new"`) {
		t.Fatalf("reopen: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, adam, "GET", url+"/C-9", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no such case: %d", resp.StatusCode)
	}
	if resp, body := send(t, sam, "PUT", srv.URL+"/api/v1/jobs/settings", `{"editors": ["viewer"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, vera, "GET", url, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("a Viewer who may edit jobs lists the cases: %d", resp.StatusCode)
	}
	if resp, _ := send(t, vera, "POST", url+"/C-1/close", `{"outcome": "noFixNeeded", "explanation": "c"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer who may edit jobs closes a case: %d", resp.StatusCode)
	}
	// notAProblem, an outcome before CF-90, closes as noAction.
	if resp, body := send(t, adam, "POST", url+"/C-1/close", `{"outcome": "notAProblem", "explanation": "As designed."}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"outcome":"noAction"`) {
		t.Fatalf("closed as notAProblem: %d %s", resp.StatusCode, body)
	}
	// Settings › Cases (CF-19): read by those who see the cases, set by a
	// SuperAdmin.
	if resp, body := send(t, sue, "GET", srv.URL+casework.SettingsPath, ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"newCasesPerRun":50`) {
		t.Fatalf("Settings › Cases: %d %s", resp.StatusCode, body)
	}
	cases := `{"keepAgentClosedDays": 30, "lookBackDays": 30, "exceptionStaleDays": 30, "exceptionsPerAgent": 10, "newCasesPerRun": 10}`
	if resp, _ := send(t, adam, "PUT", srv.URL+casework.SettingsPath, cases); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin sets Settings › Cases: %d", resp.StatusCode)
	}
	if resp, body := send(t, sam, "PUT", srv.URL+casework.SettingsPath, cases); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"newCasesPerRun":10`) {
		t.Fatalf("a SuperAdmin sets Settings › Cases: %d %s", resp.StatusCode, body)
	}
	// --- Cases replace reports (Phase 12, wave 5): any words, or none,
	// for what a case is about (CU-03); the SLA, read by all who see the
	// cases, set by a SuperAdmin (CU-06).
	if resp, body := send(t, adam, "POST", url, `{"title": "Disk 90%", "resource": "the disk of db-1, /var"}`); resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"resource":"the disk of db-1, /var"`) {
		t.Fatalf("a case about a disk: %d %s", resp.StatusCode, body)
	}
	if resp, body := send(t, sue, "GET", srv.URL+casework.SLAPath, ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"4"`) || !strings.Contains(body, `"maySet":false`) {
		t.Fatalf("the SLA: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, adam, "PUT", srv.URL+casework.SLAPath, `{"targets": {"0": {"responseMinutes": 5, "resolveMinutes": 60}}}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin sets the SLA: %d", resp.StatusCode)
	}
	if resp, body := send(t, sam, "PUT", srv.URL+casework.SLAPath, `{"targets": {"0": {"responseMinutes": 5, "resolveMinutes": 60}}}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"responseMinutes":5`) {
		t.Fatalf("a SuperAdmin sets the SLA: %d %s", resp.StatusCode, body)
	}
}
