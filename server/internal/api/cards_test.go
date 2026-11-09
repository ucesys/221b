package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// cardExec is a kubectl whose target the test changes.
type cardExec struct {
	mu    sync.Mutex
	print string
	ran   []string
}

func (e *cardExec) Admin() bool              { return false }
func (e *cardExec) Check(cards.Action) error { return nil }
func (e *cardExec) Describe(_ context.Context, _ string, a cards.Action) (cards.Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	line, _ := a.Args["command"].(string)
	return cards.Plan{Summary: line, Targets: []string{"deployment shop/web"}, Fingerprint: e.print}, nil
}
func (e *cardExec) Apply(_ context.Context, c cards.Card, i int, by cards.Approver) (cards.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ran = append(e.ran, by.Name)
	return cards.Result{OK: true, Summary: "done"}, nil
}

// adminOnly is a console action, a SuperAdmin's to approve.
type adminOnly struct{ cardExec }

func (*adminOnly) Admin() bool { return true }

// cardsSetup serves a console with the cards and local sign-in: Vera a
// Viewer, Adam an Admin, Sam and Sue SuperAdmins. dev-1 is a development
// cluster and prod-1 production, with the two-person rule.
func cardsSetup(t *testing.T) (*httptest.Server, *cards.Store, *cardExec, *bytes.Buffer) {
	t.Helper()
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	hash, _ := auth.HashPassword("correct horse battery staple")
	var auditOut bytes.Buffer
	log := audit.New(audit.Options{Out: &auditOut})
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeLocal,
		Local: []auth.LocalUser{
			{Username: "vera", Name: "Vera", Groups: []string{"web-readers"}, PasswordHash: hash},
			{Username: "adam", Name: "Adam", Groups: []string{"web-ops"}, PasswordHash: hash},
			{Username: "sam", Name: "Sam", Roles: []string{"superAdmin"}, PasswordHash: hash},
			{Username: "sue", Name: "Sue", Roles: []string{"superAdmin"}, PasswordHash: hash},
		},
		LocalGroups: []auth.LocalGroup{{Name: "web-readers", Role: roles.Viewer, Namespaces: []string{"web"}}, {Name: "web-ops", Role: roles.Admin, Namespaces: []string{"web"}}},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, quietLog(), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := cards.New(cards.Options{Audit: log, Logger: quietLog(), Cluster: func(id string) (cards.ClusterInfo, bool) {
		switch id {
		case "dev-1":
			return cards.ClusterInfo{Name: "Development"}, true
		case "prod-1":
			return cards.ClusterInfo{Name: "Production", Production: true, TwoPerson: true}, true
		}
		return cards.ClusterInfo{}, false
	}})
	exec := &cardExec{print: "v1"}
	st.Register(cards.KindKubectl, exec)
	st.Register(cards.KindHostAdd, &adminOnly{})
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })
	srv := httptest.NewServer(New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Auth: svc, Audit: log, UI: ui, Logger: quietLog(), Cards: st, CardRoles: []roles.ID{roles.SuperAdmin, roles.Admin}}))
	t.Cleanup(srv.Close)
	return srv, st, exec, &auditOut
}

func agentProposal(cluster string, kind string) cards.Proposal {
	return cards.Proposal{Agent: "troubleshooter", Source: cards.Source{Kind: cards.SourceAlert, Label: "Alert KubePodCrashLooping on shop/web-7f9 at 03:12", SetBy: "Sam"}, Cluster: cluster, Title: "Restart shop/web",
		Actions: []cards.Action{{Kind: kind, Args: map[string]any{"command": "kubectl rollout restart deployment web -n shop"}}}}
}

type proposalsAnswer struct {
	Proposals []proposalView `json:"proposals"`
	Waiting   int            `json:"waiting"`
}

func listProposals(t *testing.T, srv *httptest.Server, c *http.Client, q string) proposalsAnswer {
	t.Helper()
	r, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/cases/proposals"+q, "")
	if r.StatusCode != 200 {
		t.Fatalf("list: %d %s", r.StatusCode, b)
	}
	var out proposalsAnswer
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Approving needs the approval permission, not admin rights; the
// console's own records need a SuperAdmin; on production an agent's
// change needs two people, and Watson checks the card again each time.
func TestProposalsAreApprovedByThePeopleWhoMay(t *testing.T) {
	srv, st, exec, auditOut := cardsSetup(t)
	ctx := context.Background()
	dev, _ := st.Propose(ctx, agentProposal("dev-1", cards.KindKubectl))
	prod, _ := st.Propose(ctx, agentProposal("prod-1", cards.KindKubectl))
	host, _ := st.Propose(ctx, cards.Proposal{Agent: "mycroft", Source: cards.Source{Kind: cards.SourceKnowledge, Label: "Server inventory"}, Cluster: "dev-1", Title: "Add backup-1", Actions: []cards.Action{{Kind: cards.KindHostAdd, Args: map[string]any{}}}})
	vera, adam, sam, sue := signIn(t, srv, "vera"), signIn(t, srv, "adam"), signIn(t, srv, "sam"), signIn(t, srv, "sue")
	if r, _ := send(t, vera, http.MethodGet, srv.URL+"/api/v1/cases/proposals", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer listed them: %d", r.StatusCode)
	}
	if r, _ := send(t, vera, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+dev+"/approve", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer approved: %d", r.StatusCode)
	}
	list := listProposals(t, srv, adam, "?state=queued")
	if len(list.Proposals) != 3 || list.Waiting != 2 {
		t.Fatalf("Adam's list: %+v", list)
	}
	for _, p := range list.Proposals {
		if p.ID == host && (p.CanApprove || !strings.Contains(p.Cannot, "Only a SuperAdmin")) {
			t.Fatalf("the host card: %+v", p)
		}
		if p.ID == prod && (p.Needed != 2 || p.ClusterName != "prod-1" || p.AgentName != "troubleshooter") {
			t.Fatalf("the production card: %+v", p)
		}
	}
	if r, b := send(t, adam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+host+"/approve", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin approved a host: %d %s", r.StatusCode, b)
	}
	if r, b := send(t, adam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+dev+"/approve", `{"comment":"ok"}`); r.StatusCode != 200 || !strings.Contains(b, `"state":"approved"`) {
		t.Fatalf("Adam approves: %d %s", r.StatusCode, b)
	}
	// Sam comes second: refused with who approved it and when, and the
	// card as it is now, so the page shows it (IB-08).
	if r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+dev+"/approve", ""); r.StatusCode != http.StatusConflict || !strings.Contains(b, `"error":"Already approved by Adam at `) || !strings.Contains(b, `"proposal":{"id":"`+dev+`"`) {
		t.Fatalf("a second approval: %d %s", r.StatusCode, b)
	}
	// Production: one of two, then the same person again, then another.
	if r, b := send(t, adam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+prod+"/approve", ""); r.StatusCode != 200 || !strings.Contains(b, `"state":"queued"`) {
		t.Fatalf("the first of two: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, adam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+prod+"/approve", ""); r.StatusCode != http.StatusConflict {
		t.Fatalf("Adam twice: %d", r.StatusCode)
	}
	// The target changes: Sam is told why, with the card.
	exec.mu.Lock()
	exec.print = "v2"
	exec.mu.Unlock()
	r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+prod+"/approve", "")
	if r.StatusCode != http.StatusConflict || !strings.Contains(b, "cannot be approved as it stands") || !strings.Contains(b, `"recheck":{`) {
		t.Fatalf("a stale card: %d %s", r.StatusCode, b)
	}
	exec.mu.Lock()
	exec.print = "v1"
	exec.mu.Unlock()
	if r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+prod+"/recheck", ""); r.StatusCode != 200 || !strings.Contains(b, `"ok":true`) {
		t.Fatalf("recheck: %d %s", r.StatusCode, b)
	}
	if r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+prod+"/approve", ""); r.StatusCode != 200 || !strings.Contains(b, `"state":"approved"`) {
		t.Fatalf("Sam, the second: %d %s", r.StatusCode, b)
	}
	st.Wait()
	exec.mu.Lock()
	ran := strings.Join(exec.ran, ",")
	exec.mu.Unlock()
	if ran != "Adam,Sam" {
		t.Fatalf("ran as %s", ran)
	}
	// A rejection needs its reason.
	if r, _ := send(t, sue, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+host+"/reject", `{"reason":""}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("no reason: %d", r.StatusCode)
	}
	if r, b := send(t, sue, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+host+"/reject", `{"reason":"not ours"}`); r.StatusCode != 200 || !strings.Contains(b, `"state":"rejected"`) {
		t.Fatalf("reject: %d %s", r.StatusCode, b)
	}
	if mine := listProposals(t, srv, sue, "?mine=1"); len(mine.Proposals) != 1 || mine.Proposals[0].ID != host {
		t.Fatalf("Sue's: %+v", mine)
	}
	if !strings.Contains(auditOut.String(), `"cards.approve"`) || !strings.Contains(auditOut.String(), `"cards.reject"`) {
		t.Fatalf("audit: %s", auditOut.String())
	}
}

// Standing approvals and the notification channels are a SuperAdmin's;
// a channel's address is written, never read back.
// A proposed change is PC-<n>; its address takes the c<n> of before, in
// any case, and the answer names it PC-<n> (CF-56).
func TestAProposalAnswersToItsOldID(t *testing.T) {
	srv, st, _, _ := cardsSetup(t)
	id, err := st.Propose(context.Background(), agentProposal("dev-1", cards.KindKubectl))
	if err != nil || id != "PC-1" {
		t.Fatalf("proposed %q: %v", id, err)
	}
	sam := signIn(t, srv, "sam")
	for _, path := range []string{"PC-1", "c1", "C1", "pc-1"} {
		if r, b := send(t, sam, http.MethodGet, srv.URL+"/api/v1/cases/proposals/"+path, ""); r.StatusCode != 200 || !strings.Contains(b, `"id":"PC-1"`) || strings.Contains(b, `"id":"c1"`) {
			t.Errorf("GET %s: %d %s", path, r.StatusCode, b)
		}
	}
	if r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/proposals/c1/reject", `{"reason":"not now"}`); r.StatusCode != 200 || !strings.Contains(b, `"id":"PC-1"`) || !strings.Contains(b, `"state":"rejected"`) {
		t.Errorf("reject c1: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, sam, http.MethodGet, srv.URL+"/api/v1/cases/proposals/C-1", ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("the case C-1 found a card: %d", r.StatusCode)
	}
}

func TestStandingApprovalsAndChannelsAreASuperAdmins(t *testing.T) {
	srv, _, _, auditOut := cardsSetup(t)
	adam, sam := signIn(t, srv, "adam"), signIn(t, srv, "sam")
	body := `{"name":"Restart in development","on":true,"clusters":["dev-1"],"actions":["kubectl rollout restart"],"perHour":3}`
	if r, _ := send(t, adam, http.MethodPost, srv.URL+"/api/v1/cases/standing", body); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin set one: %d", r.StatusCode)
	}
	if r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/standing", strings.Replace(body, "dev-1", "prod-1", 1)); r.StatusCode != http.StatusBadRequest || !strings.Contains(b, "production") {
		t.Fatalf("on production: %d %s", r.StatusCode, b)
	}
	r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/standing", body)
	if r.StatusCode != http.StatusCreated || strings.Contains(b, "221b-local") {
		t.Fatalf("set: %d %s", r.StatusCode, b)
	}
	var made cards.Standing
	_ = json.Unmarshal([]byte(b), &made)
	if r, b := send(t, adam, http.MethodGet, srv.URL+"/api/v1/cases/standing", ""); r.StatusCode != 200 || !strings.Contains(b, made.ID) {
		t.Fatalf("Adam sees them: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, sam, http.MethodDelete, srv.URL+"/api/v1/cases/standing/"+made.ID, ""); r.StatusCode != 200 {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	secret := hook.URL + "/hooks/TOKEN-0123456789abcdef"
	if r, _ := send(t, adam, http.MethodGet, srv.URL+"/api/v1/cases/notifications", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an Admin read the channels: %d", r.StatusCode)
	}
	r, b = send(t, sam, http.MethodPut, srv.URL+"/api/v1/cases/notifications", `{"link":"https://221b.example.com","channels":[{"kind":"webhook","name":"ops","on":true,"url":"`+secret+`"}]}`)
	if r.StatusCode != 200 || strings.Contains(b, "TOKEN-0123456789abcdef") || !strings.Contains(b, `"urlSet":true`) {
		t.Fatalf("saved: %d %s", r.StatusCode, b)
	}
	var v cards.NotifyView
	_ = json.Unmarshal([]byte(b), &v)
	if r, b := send(t, sam, http.MethodGet, srv.URL+"/api/v1/cases/notifications", ""); r.StatusCode != 200 || strings.Contains(b, "TOKEN") {
		t.Fatalf("read: %d %s", r.StatusCode, b)
	}
	if r, b := send(t, sam, http.MethodPost, srv.URL+"/api/v1/cases/notifications/"+v.Channels[0].ID+"/test", ""); r.StatusCode != 200 {
		t.Fatalf("test: %d %s", r.StatusCode, b)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], `"event":"test"`) {
		t.Fatalf("the hook got %v", got)
	}
	if strings.Contains(auditOut.String(), "TOKEN-0123456789abcdef") {
		t.Fatal("the audit log holds the address")
	}
}

// A change Sherlock proposed in a chat and the person sent to Watson is
// kept as a durable card too; approved there, its Watson card goes, and
// Watson runs it once, as the approver.
func TestAChatsChangeIsADurableCardToo(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	var st *cards.Store
	var wsvc *watson.Service
	srv, ops, m, _, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		wsvc = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CheckEvery: 50 * time.Millisecond})
		o.Watson = wsvc
		st = cards.New(cards.Options{Audit: o.Audit, Logger: quietLog(), Cluster: func(id string) (cards.ClusterInfo, bool) {
			return cards.ClusterInfo{Name: "Here"}, id == clusters.LocalID
		}})
		o.Cards, o.CardRoles = st, []roles.ID{roles.SuperAdmin}
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvDevelopment, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	r, b := send(t, ops, http.MethodPost, srv.URL+"/api/v1/watson/commands", `{"command":"kubectl cordon worker-002","from":"Sherlock","why":"worker-002 reports disk pressure"}`)
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("submit: %d %s", r.StatusCode, b)
	}
	var items []watsonItem
	_ = json.Unmarshal([]byte(b), &items)
	list := listProposals(t, srv, ops, "")
	if len(list.Proposals) != 1 {
		t.Fatalf("proposals: %+v", list)
	}
	p := list.Proposals[0]
	if p.WatsonItem != items[0].ID || p.Source.Kind != cards.SourceChat || p.Requester == nil || p.Reasoning != "worker-002 reports disk pressure" || len(p.Plans) != 1 || p.Plans[0].Fingerprint == "" {
		t.Fatalf("the chat's card: %+v", p)
	}
	if r, b := send(t, ops, http.MethodPost, srv.URL+"/api/v1/cases/proposals/"+p.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, b)
	}
	st.Wait()
	done, _ := st.Get(context.Background(), p.ID)
	if done.State != cards.Applied || !s.Node("worker-002").Spec.Unschedulable {
		t.Fatalf("applied: %+v", done)
	}
	if it := wsvc.Get(items[0].ID); it == nil || it.Status != watson.StatusDiscarded {
		t.Fatalf("the chat's Watson card stayed: %+v", it)
	}
	// Sending the same again replaces nothing that is done, and a newer
	// one replaces the older that waits.
	send(t, ops, http.MethodPost, srv.URL+"/api/v1/watson/commands", `{"command":"kubectl uncordon worker-002","from":"Sherlock"}`)
	send(t, ops, http.MethodPost, srv.URL+"/api/v1/watson/commands", `{"command":"kubectl uncordon worker-002","from":"Sherlock"}`)
	if open := listProposals(t, srv, ops, "?state=queued"); len(open.Proposals) != 1 {
		t.Fatalf("open: %+v", open.Proposals)
	}
	// What a person typed stays a Watson card only.
	send(t, ops, http.MethodPost, srv.URL+"/api/v1/watson/commands", `{"command":"kubectl cordon worker-003","from":"Typed"}`)
	if all := listProposals(t, srv, ops, ""); len(all.Proposals) != 3 {
		t.Fatalf("all: %d", len(all.Proposals))
	}
}

// A card's alert condition is read from the leader's Alerts view: while
// the pod crash-loops the card waits; once it runs again the card expires,
// its alert resolved.
func TestACardsAlertConditionFollowsTheAlertsTab(t *testing.T) {
	var st *cards.Store
	_, _, _, _, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		st = cards.New(cards.Options{Audit: o.Audit, Logger: quietLog(), Cluster: func(id string) (cards.ClusterInfo, bool) {
			return cards.ClusterInfo{Name: "Here"}, id == clusters.LocalID
		}})
		st.Register(cards.KindKubectl, &cardExec{print: "v1"})
		o.Cards, o.CardRoles = st, []roles.ID{roles.SuperAdmin}
	})
	set := func(state corev1.ContainerState) {
		local.Update("pods", "web", "shop", func(o runtime.Object) {
			o.(*corev1.Pod).Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 4, Ready: state.Running != nil, State: state}}
		})
		store.Notify(kube.Change{Views: map[string][]string{live.ViewAlerts: {kube.AllRows}}})
		store.Process()
	}
	set(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}})
	p := agentProposal(clusters.LocalID, cards.KindKubectl)
	p.Condition = cards.Condition{Kind: cards.ConditionAlert, Args: map[string]any{"kind": "pod", "namespace": "web", "name": "shop"}}
	ctx := context.Background()
	id, err := st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	st.Sweep(ctx)
	if c, _ := st.Get(ctx, id); c.State != cards.Queued || c.Condition.Args["key"] != "pod:web/shop" {
		t.Fatalf("while it fires: %+v", c)
	}
	if c, err := st.Recheck(ctx, id, cards.Person{Key: "ops", Name: "Ops"}); err != nil || !c.Recheck.OK {
		t.Fatalf("recheck: %v %+v", err, c.Recheck)
	}
	set(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})
	st.Sweep(ctx)
	if c, _ := st.Get(ctx, id); c.State != cards.Expired || !strings.Contains(c.Reason, "its alert resolved") {
		t.Fatalf("once it runs: %+v", c)
	}
}
