package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- A proposed change's Sherlock and Watson (CF-61, CF-62) ---

// The HTTP contract: Sherlock on a proposal asks as the person, names
// them, and starts from the proposal and the cases it resolves; Watson's
// commands from a proposal are checked against it and logged there.
// Approved, its own command runs from the proposal, once, on the card's
// one approval (no Watson card to approve as well); the decision deletes
// the conversation and keeps the change that finished done, which the case
// it resolves is given for closing as Fixed. A Viewer does neither.
func TestTroubleshootingAProposalCF61CF62(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	fake := fakeclaude.New()
	fake.Delay = time.Millisecond
	claude := httptest.NewServer(fake)
	t.Cleanup(claude.Close)
	keys := sherlock.NewKeys(nil, "", nil)
	var sh *sherlock.Service
	var cw *casework.Service
	var st *cards.Store
	srv, ops, m, auditOut, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CheckEvery: 50 * time.Millisecond})
		sh = sherlock.New(sherlock.Options{Client: &sherlock.Client{BaseURL: claude.URL, Key: keys.Key}, Audit: o.Audit, Logger: quietLog(),
			Settings: func() sherlock.Settings {
				st := o.Settings.Current().SherlockOrDefault()
				return sherlock.Settings{Model: st.Model, Caps: sherlock.Caps{DailyUSD: st.DailyUSD, MonthlyUSD: st.MonthlyUSD}}
			}})
		o.Sherlock, o.SherlockKeys = sh, keys
		kinds := jobs.NewRegistry()
		js := jobs.New(jobs.Options{Kinds: kinds, Logger: quietLog()})
		t.Cleanup(js.Stop)
		cw = casework.New(casework.Options{Jobs: js, Logger: quietLog(), Audit: o.Audit})
		t.Cleanup(cw.Stop)
		kinds.Register(cw.Kind())
		st = cards.New(cards.Options{Audit: o.Audit, Logger: quietLog(), Cluster: func(id string) (cards.ClusterInfo, bool) {
			return cards.ClusterInfo{Name: "Here"}, id == clusters.LocalID
		}})
		o.Jobs, o.Casework, o.Cards = js, cw, st
	})
	t.Cleanup(sh.Wait)
	t.Cleanup(st.Wait)
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvDevelopment, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	if err := keys.Set(context.Background(), "sk-ant-test-key-0123456789", "test"); err != nil {
		t.Fatal(err)
	}
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`)
	viewer := signIn(t, srv, "viewer@corp.example")
	cases := srv.URL + casework.Prefix
	if r, b := send(t, ops, http.MethodPost, cases, `{"title":"worker-002 takes pods it cannot run","cluster":"local","resource":"Node/worker-002","priority":2}`); r.StatusCode != http.StatusCreated {
		t.Fatalf("open: %d %s", r.StatusCode, b)
	}
	card, err := st.Propose(context.Background(), cards.Proposal{Agent: "troubleshooter", Source: cards.Source{Kind: cards.SourceCase, ID: "C-1", Label: "Case C-1"}, Cluster: clusters.LocalID, Title: "Cordon worker-002",
		Reasoning: "Its disk is full.", Actions: []cards.Action{{Kind: cards.KindKubectl, Args: map[string]any{"command": "kubectl cordon worker-002"}, Impact: "no new pods there", Risk: "low"}}, Resolves: []string{"C-1"}})
	if err != nil || card != "PC-1" {
		t.Fatalf("propose: %q %v", card, err)
	}
	base := srv.URL + "/api/v1/cases/proposals/PC-1"
	chat := base + "/sherlock"
	conversation := func(c *http.Client) (int, sherlock.View) {
		t.Helper()
		r, b := send(t, c, http.MethodGet, chat+"/conversation", "")
		var v sherlock.View
		_ = json.Unmarshal([]byte(b), &v)
		return r.StatusCode, v
	}
	getCard := func() proposalView {
		t.Helper()
		_, b := send(t, ops, http.MethodGet, base, "")
		var v proposalView
		_ = json.Unmarshal([]byte(b), &v)
		return v
	}
	if v := getCard(); !v.MayAsk {
		t.Fatal("Ops, who may approve, may not ask on the proposal")
	}

	// Sherlock on the proposal, as Ops: its own conversation.
	if r, b := send(t, ops, http.MethodPost, chat+"/ask", `{"question":"Is cordoning enough?"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask: %d %s", r.StatusCode, b)
	}
	sh.Wait()
	code, v := conversation(ops)
	if code != 200 || len(v.Messages) != 2 || v.Messages[0].Asker != "Ops" || v.Messages[1].Status != "done" {
		t.Fatalf("the conversation: %d %+v", code, v.Messages)
	}
	var first sherlock.Request
	_ = json.Unmarshal(fake.Requests()[0], &first)
	txt := first.Messages[0].Content[0].Text
	for _, want := range []string{"<proposal-context>", "<proposal>\nid: PC-1\ntitle: Cordon worker-002", "action 1 of 1: kubectl cordon worker-002", "risk: low", "<case-record>\nid: C-1\ntitle: worker-002 takes pods it cannot run\npriority: P2"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("the first question's context has no %q: %q", want, txt)
		}
	}
	// The case's conversation is another.
	if r, b := send(t, ops, http.MethodGet, cases+"/C-1/sherlock/conversation", ""); r.StatusCode != 200 || strings.Contains(b, "cordoning") {
		t.Fatalf("the case's: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, viewer, http.MethodPost, chat+"/ask", `{"question":"Why?"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer asked: %d", r.StatusCode)
	}
	if code, _ := conversation(viewer); code != http.StatusForbidden {
		t.Fatalf("a Viewer read it: %d", code)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/cases/proposals/PC-9/sherlock/ask", `{"question":"Why?"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("no such proposal: %d", r.StatusCode)
	}

	// Watson from the proposal: a read, checked and logged.
	cmd := func(c *http.Client, body string) (int, []watsonItem) {
		t.Helper()
		r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands", body)
		var out []watsonItem
		_ = json.Unmarshal([]byte(b), &out)
		return r.StatusCode, out
	}
	if code, items := cmd(ops, `{"command":"kubectl get nodes","from":"proposal:pc-1","run":true}`); code != http.StatusAccepted || items[0].From != "proposal:PC-1" {
		t.Fatalf("a read: %d %+v", code, items)
	}
	if code, _ := cmd(ops, `{"command":"kubectl get nodes","from":"proposal:PC-9"}`); code != http.StatusNotFound {
		t.Fatalf("from no proposal: %d", code)
	}
	if code, _ := cmd(viewer, `{"command":"kubectl get nodes","from":"proposal:PC-1"}`); code != http.StatusForbidden {
		t.Fatalf("a Viewer from the proposal: %d", code)
	}
	waitCard := func(what string, ok func(proposalView) bool) proposalView {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			v := getCard()
			if ok(v) {
				return v
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v", what, v.Card)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitCard("the read logged", func(v proposalView) bool {
		return len(v.Commands) == 1 && v.Commands[0].Status == "done" && !v.Commands[0].Change && v.Commands[0].By == "Ops"
	})

	// Run in Watson, approved there: the card's own approval runs it, from
	// the proposal.
	if r, b := send(t, ops, http.MethodPost, base+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, b)
	}
	done := waitCard("applied, its change logged", func(v proposalView) bool {
		return v.State == cards.Applied && len(v.Commands) == 1 && v.Commands[0].Line == "kubectl cordon worker-002" && v.Commands[0].Status == "done"
	})
	if len(done.Approvals) != 1 || done.Commands[0].Approver != "Ops" || !done.Commands[0].Change {
		t.Fatalf("one approval, the change kept: %+v %+v", done.Approvals, done.Commands)
	}
	r, b := send(t, ops, http.MethodGet, srv.URL+"/api/v1/watson/commands/"+done.Commands[0].ID, "")
	var ran watsonItem
	_ = json.Unmarshal([]byte(b), &ran)
	if r.StatusCode != 200 || ran.From != "proposal:PC-1" || ran.Status != "done" {
		t.Fatalf("the run in Watson: %d %s", r.StatusCode, b)
	}
	log := auditOut.String()
	if n := strings.Count(log, `"action":"cards.approve"`); n != 1 {
		t.Errorf("%d card approvals in the audit log, want 1", n)
	}
	if !strings.Contains(log, `a durable card's`) {
		t.Error("Watson ran the change on an approval of its own, not the card's")
	}
	for _, want := range []string{`"action":"cards.chat"`, `"action":"cards.command"`, `"target":"proposal PC-1"`} {
		if !strings.Contains(log, want) {
			t.Errorf("no %s in the audit log", want)
		}
	}

	// Decided: the conversation is deleted, nobody asks or runs from it.
	if code, v := conversation(ops); code != 200 || len(v.Messages) != 0 {
		t.Fatalf("after the decision: %d %+v", code, v.Messages)
	}
	if r, _ := send(t, ops, http.MethodPost, chat+"/ask", `{"question":"Why?"}`); r.StatusCode != http.StatusConflict {
		t.Fatalf("asked on a decided proposal: %d", r.StatusCode)
	}
	if code, _ := cmd(ops, `{"command":"kubectl get nodes","from":"proposal:PC-1"}`); code != http.StatusConflict {
		t.Fatalf("a command from a decided proposal: %d", code)
	}

	// The case it resolves starts closing as Fixed from its change (CF-49).
	var kase struct {
		FixCommands []string `json:"fixCommands"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b := send(t, ops, http.MethodGet, cases+"/C-1", "")
		_ = json.Unmarshal([]byte(b), &kase)
		if len(kase.FixCommands) == 1 && kase.FixCommands[0] == "kubectl cordon worker-002" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the case's fix commands: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
