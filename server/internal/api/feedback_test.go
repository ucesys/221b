package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
)

// --- The owner's feedback on Cases (Phase 12, wave 6c) ---

// CF-40: Show costs is on by default; every signed-in person reads it,
// in its own route and in the settings every page loads; only a
// SuperAdmin changes it, audited; an admin's save of the settings keeps
// it.
func TestShowCostsIsASuperAdminsSwitchCF40(t *testing.T) {
	srv, _, _, auditOut := cardsSetup(t)
	vera, adam, sam := signIn(t, srv, "vera"), signIn(t, srv, "adam"), signIn(t, srv, "sam")
	if r, b := send(t, vera, "GET", srv.URL+"/api/v1/settings/ui", ""); r.StatusCode != http.StatusOK || strings.TrimSpace(b) != `{"showCosts":true}` {
		t.Fatalf("a Viewer reads it: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, adam, "PUT", srv.URL+"/api/v1/settings/ui", `{"showCosts": false}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin changes it: %d", r.StatusCode)
	}
	if r, b := send(t, sam, "PUT", srv.URL+"/api/v1/settings/ui", `{"showCosts": false}`); r.StatusCode != http.StatusOK || !strings.Contains(b, `"showCosts":false`) {
		t.Fatalf("a SuperAdmin turns it off: %d %s", r.StatusCode, b)
	}
	if !strings.Contains(auditOut.String(), `"action":"settings.ui"`) || !strings.Contains(auditOut.String(), "costs shown → hidden") {
		t.Errorf("audit: %s", auditOut.String())
	}
	if r, b := send(t, vera, "GET", srv.URL+"/api/v1/settings", ""); r.StatusCode != http.StatusOK || !strings.Contains(b, `"showCosts":false`) {
		t.Fatalf("the settings say: %d %s", r.StatusCode, b)
	}
	if r, b := send(t, sam, "PUT", srv.URL+"/api/v1/settings", `{"refreshSeconds": 20}`); r.StatusCode != http.StatusOK || !strings.Contains(b, `"showCosts":false`) {
		t.Fatalf("a save of the settings: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, sam, "PUT", srv.URL+"/api/v1/settings", `{"refreshSeconds": 20, "hideCosts": true}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("hidden through the settings: %d", r.StatusCode)
	}
	if r, _ := send(t, sam, "PUT", srv.URL+"/api/v1/settings/ui", `{}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("an empty change: %d", r.StatusCode)
	}
}

// CF-35, CF-39: a proposal says who proposed and who decided it, and the
// title of each case it resolves.
func TestProposalsSayWhoAndWhatTheyResolveCF35CF39(t *testing.T) {
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	log := audit.New(audit.Options{Out: io.Discard})
	st := cards.New(cards.Options{Audit: log, Logger: quietLog(), Cluster: func(id string) (cards.ClusterInfo, bool) {
		return cards.ClusterInfo{Name: "Development"}, id == "dev-1"
	}, AgentName: func(string) string { return "Troubleshooter" }})
	st.Register(cards.KindKubectl, &cardExec{print: "v1"})
	kinds := jobs.NewRegistry()
	js := jobs.New(jobs.Options{Kinds: kinds, Logger: quietLog()})
	t.Cleanup(js.Stop)
	cw := casework.New(casework.Options{Jobs: js, Logger: quietLog(), Audit: log})
	t.Cleanup(cw.Stop)
	cw.SetCards(st)
	ctx := context.Background()
	who := casework.Caller{Person: casework.Person{Name: "Olivia"}, ID: "olivia", Approver: true}
	if _, err := cw.Create(ctx, casework.NewCase{Title: "web has no limits"}, who); err != nil {
		t.Fatal(err)
	}
	p := agentProposal("dev-1", cards.KindKubectl)
	p.Resolves = []string{"C-1"}
	id, err := st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reject(ctx, id, cards.Person{Key: "ann", Name: "Ann"}, "not now"); err != nil {
		t.Fatal(err)
	}
	shared := settings.NewService(ctx, &settings.Memory{}, settings.Default, quietLog(), nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })
	srv := httptest.NewServer(New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Audit: log, UI: ui, Logger: quietLog(), Cards: st, Casework: cw, CardRoles: []roles.ID{roles.SuperAdmin}}))
	t.Cleanup(srv.Close)
	for _, path := range []string{"/api/v1/aiops/proposals", "/api/v1/aiops/proposals/" + id} {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		for _, want := range []string{`"resolvesTitles":{"C-1":"web has no limits"}`, `"proposedBy":{"name":"Troubleshooter","kind":"agent"}`, `"decidedBy":{"name":"Ann","kind":"person"}`,
			`"lastBy":{"name":"Ann","kind":"person"}`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: no %s in %s", path, want, b)
			}
		}
	}
}
