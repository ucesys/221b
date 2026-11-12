package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlocktools"
)

// jobsSetup serves a console with the jobs, local sign-in and four
// people: Vera a Viewer, Adam an Admin, Sue a SuperViewer and Sam a
// SuperAdmin.
func jobsSetup(t *testing.T, more ...func(*Options)) (*httptest.Server, *Server, *auth.Service) {
	t.Helper()
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
	js.RegisterBuiltins()
	t.Cleanup(js.Stop)
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })
	o := Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Auth: svc, Audit: log, UI: ui, Logger: quietLog(), Jobs: js}
	for _, f := range more {
		f(&o)
	}
	h := New(o)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, h, svc
}

// Exceptions (FX-06, FX-10): those who may approve proposed
// changes (cases.approverRoles, here Admin) remove exceptions; the super
// roles read; a Viewer neither. The findings are routed into cases and
// have no routes of their own (CF-01).
func TestFindingsAndLessonsAccess(t *testing.T) {
	srv, _, _ := jobsSetup(t, func(o *Options) {
		o.CardRoles = []roles.ID{roles.SuperAdmin, roles.Admin}
		o.Lessons = lessons.New(lessons.Options{})
	})
	vera, adam, sue := signIn(t, srv, "vera"), signIn(t, srv, "adam"), signIn(t, srv, "sue")
	// The findings have no routes of their own any more (CF-01).
	if resp, _ := send(t, adam, "GET", srv.URL+"/api/v1/findings", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the findings' list: %d", resp.StatusCode)
	}
	if resp, body := send(t, adam, "GET", srv.URL+"/api/v1/aiops/exceptions?stale=1", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"exceptions":[]`) {
		t.Fatalf("an approver lists the stale exceptions: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, vera, "GET", srv.URL+"/api/v1/aiops/exceptions", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer lists the exceptions: %d", resp.StatusCode)
	}
	if resp, body := send(t, sue, "GET", srv.URL+"/api/v1/aiops/exceptions", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"exceptions":[]`) {
		t.Fatalf("a SuperViewer lists the exceptions: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, sue, "DELETE", srv.URL+"/api/v1/aiops/exceptions/EX-1", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a SuperViewer removes an exception: %d", resp.StatusCode)
	}
	if resp, _ := send(t, adam, "DELETE", srv.URL+"/api/v1/aiops/exceptions/EX-1", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an approver removes an exception not there: %d", resp.StatusCode)
	}
}

// AIOps › Jobs: the super roles read the jobs; only a SuperAdmin changes
// them, and the roles the settings add; the settings are a SuperAdmin's.
func TestJobsAccess(t *testing.T) {
	srv, _, _ := jobsSetup(t)
	vera, adam, sue, sam := signIn(t, srv, "vera"), signIn(t, srv, "adam"), signIn(t, srv, "sue"), signIn(t, srv, "sam")
	if resp, _ := send(t, vera, "GET", srv.URL+"/api/v1/jobs", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer lists the jobs: %d", resp.StatusCode)
	}
	if resp, body := send(t, sue, "GET", srv.URL+"/api/v1/jobs", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"nightly-audit"`) || !strings.Contains(body, `"mayEdit":false`) {
		t.Fatalf("a SuperViewer lists the jobs: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, sue, "PUT", srv.URL+"/api/v1/jobs/nightly-audit", `{"on": true}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a SuperViewer changes a job: %d", resp.StatusCode)
	}
	if resp, _ := send(t, adam, "PUT", srv.URL+"/api/v1/jobs/nightly-audit", `{"on": true}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin changes a job: %d", resp.StatusCode)
	}
	if resp, body := send(t, sam, "PUT", srv.URL+"/api/v1/jobs/settings", `{"editors": ["admin"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
	if resp, body := send(t, adam, "PUT", srv.URL+"/api/v1/jobs/nightly-audit", `{"on": true}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"setBy":"Adam"`) {
		t.Fatalf("an Admin the settings name changes a job: %d %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, adam, "PUT", srv.URL+"/api/v1/jobs/settings", `{"editors": ["viewer"]}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin changes the settings: %d", resp.StatusCode)
	}
}

// Agents on their own read as the reader identity: a reserved name, the
// role the settings choose, never a person's.
func TestReaderIdentity(t *testing.T) {
	_, h, svc := jobsSetup(t)
	sess, on, err := svc.Reader(roles.Viewer, "web-readers")
	if err != nil || !on {
		t.Fatalf("reader: %v %v", on, err)
	}
	if sess.User.KubeName != auth.ReaderName || !slices.Contains(sess.User.KubeGroups, "221b-local:web-readers") || !slices.Contains(sess.User.KubeGroups, roles.GroupConsoleUsers) || sess.Admin || len(sess.Super) != 0 {
		t.Fatalf("session: %+v", sess)
	}
	if _, _, err := svc.Reader(roles.Viewer, "web-ops"); err == nil {
		t.Fatal("agents read with an Admin's group")
	}
	if _, _, err := svc.Reader(roles.Admin, ""); err == nil {
		t.Fatal("agents read as an Admin")
	}
	super, _, err := svc.Reader(roles.SuperViewer, "web-readers")
	if err != nil || !slices.Contains(super.User.KubeGroups, roles.GroupSuperViewers) || slices.Contains(super.User.KubeGroups, "221b-local:web-readers") {
		t.Fatalf("a SuperViewer reader: %+v %v", super, err)
	}
	rd, err := h.ReaderFor(context.Background(), "local", &sess)
	if err != nil {
		t.Fatal(err)
	}
	tb := rd.Tools.(*sherlocktools.Toolbox)
	if tsess, ok := tb.Session(); !ok || h.opts.Impersonation.personOf(tsess.User).User != auth.ReaderName || len(tb.Defs()) == 0 {
		t.Fatalf("the toolbox reads as %+v", tsess.User)
	}
	if _, err := h.ReaderFor(context.Background(), "elsewhere", &sess); err == nil {
		t.Fatal("a cluster that is not there")
	}
}
