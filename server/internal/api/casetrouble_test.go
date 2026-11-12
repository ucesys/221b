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

	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Troubleshooting a case in its drawer (wave 6f, CF-47, CF-48) ---

// The HTTP contract: Sherlock on a case asks as the person, names them,
// starts from the case, and is shared; Watson's commands from a case are
// checked against it and logged there with how they ended; closing it as
// fixed deletes the conversation and keeps the change that finished done;
// reopened, the conversation starts empty. A Viewer does neither.
func TestTroubleshootingACaseCF47CF48(t *testing.T) {
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
		o.Jobs, o.Casework = js, cw
	})
	t.Cleanup(sh.Wait)
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
	if r, b := send(t, ops, http.MethodPost, cases, `{"title":"The shop pod restarts","cluster":"local","resource":"web/Pod/shop","priority":2}`); r.StatusCode != http.StatusCreated {
		t.Fatalf("open: %d %s", r.StatusCode, b)
	}
	chat := cases + "/C-1/sherlock"
	conversation := func(c *http.Client) (int, sherlock.View) {
		t.Helper()
		r, b := send(t, c, http.MethodGet, chat+"/conversation", "")
		var v sherlock.View
		_ = json.Unmarshal([]byte(b), &v)
		return r.StatusCode, v
	}

	// Sherlock on the case, as Ops.
	if r, b := send(t, ops, http.MethodPost, chat+"/ask", `{"question":"Why is web/shop failing?"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask: %d %s", r.StatusCode, b)
	}
	sh.Wait()
	code, v := conversation(ops)
	if code != 200 || len(v.Messages) != 2 || v.Messages[0].Asker != "Ops" || v.Messages[1].Status != "done" {
		t.Fatalf("the conversation: %d %+v", code, v.Messages)
	}
	var first sherlock.Request
	_ = json.Unmarshal(fake.Requests()[0], &first)
	if txt := first.Messages[0].Content[0].Text; !strings.Contains(txt, "<case-context>") || !strings.Contains(txt, "id: C-1\ntitle: The shop pod restarts") {
		t.Fatalf("the first question's context: %q", txt)
	}
	// The person's own conversation is another.
	if r, b := send(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock/conversation", ""); r.StatusCode != 200 || strings.Contains(b, "web/shop") {
		t.Fatalf("the person's own: %d %s", r.StatusCode, b)
	}
	if r, _ := send(t, viewer, http.MethodPost, chat+"/ask", `{"question":"Why?"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer asked: %d", r.StatusCode)
	}
	if code, _ := conversation(viewer); code != http.StatusForbidden {
		t.Fatalf("a Viewer read it: %d", code)
	}
	if r, _ := send(t, ops, http.MethodPost, cases+"/C-9/sherlock/ask", `{"question":"Why?"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("no such case: %d", r.StatusCode)
	}

	// Watson from the case: a read, and a change approved.
	cmd := func(c *http.Client, body string) (int, []watsonItem) {
		t.Helper()
		r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands", body)
		var out []watsonItem
		_ = json.Unmarshal([]byte(b), &out)
		return r.StatusCode, out
	}
	if code, items := cmd(ops, `{"command":"kubectl get nodes","from":"case:C-1","run":true}`); code != http.StatusAccepted || items[0].From != "case:C-1" {
		t.Fatalf("a read: %d %+v", code, items)
	}
	code, items := cmd(ops, `{"command":"kubectl cordon worker-002","from":"case:c-1"}`)
	if code != http.StatusAccepted || items[0].Kind != "change" || items[0].From != "case:C-1" {
		t.Fatalf("a change: %d %+v", code, items)
	}
	ch := items[0].ID
	watsonStream(t, srv, ops, func(m map[string]watsonItem) bool { return m[ch].Plan != nil && m[ch].Plan.Ready })
	if r, b := send(t, ops, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+ch+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	if code, _ := cmd(ops, `{"command":"kubectl get nodes","from":"case:C-9"}`); code != http.StatusNotFound {
		t.Fatalf("from no case: %d", code)
	}
	if code, _ := cmd(viewer, `{"command":"kubectl get nodes","from":"case:C-1"}`); code != http.StatusForbidden {
		t.Fatalf("a Viewer from the case: %d", code)
	}
	var kase struct {
		Commands    []casework.Command `json:"commands"`
		FixCommands []string           `json:"fixCommands"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b := send(t, ops, http.MethodGet, cases+"/C-1", "")
		_ = json.Unmarshal([]byte(b), &kase)
		if len(kase.Commands) == 2 && kase.Commands[0].Status == "done" && kase.Commands[1].Status == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if kase.Commands[0].Change || !kase.Commands[1].Change || kase.Commands[1].By != "Ops" || kase.Commands[1].Approver != "Ops" || len(kase.FixCommands) != 1 || kase.FixCommands[0] != "kubectl cordon worker-002" {
		t.Fatalf("the log %+v, fix %v", kase.Commands, kase.FixCommands)
	}
	log := auditOut.String()
	for _, want := range []string{`"action":"case.chat"`, `"action":"case.command"`, `"target":"C-1"`} {
		if !strings.Contains(log, want) {
			t.Errorf("no %s in the audit log", want)
		}
	}

	// Fixed: the conversation goes, the change stays; closed, nobody asks.
	if r, b := send(t, ops, http.MethodPost, cases+"/C-1/fix", `{"text":"Cordoned worker-002."}`); r.StatusCode != 200 {
		t.Fatalf("fix: %d %s", r.StatusCode, b)
	}
	if code, v := conversation(ops); code != 200 || len(v.Messages) != 0 {
		t.Fatalf("after the close: %d %+v", code, v.Messages)
	}
	if r, _ := send(t, ops, http.MethodPost, chat+"/ask", `{"question":"Why?"}`); r.StatusCode != http.StatusConflict {
		t.Fatalf("asked on a closed case: %d", r.StatusCode)
	}
	if code, _ := cmd(ops, `{"command":"kubectl get nodes","from":"case:C-1"}`); code != http.StatusConflict {
		t.Fatalf("a command from a closed case: %d", code)
	}
	_, b := send(t, ops, http.MethodGet, cases+"/C-1", "")
	_ = json.Unmarshal([]byte(b), &kase)
	if len(kase.Commands) != 1 || kase.Commands[0].Line != "kubectl cordon worker-002" {
		t.Fatalf("kept: %+v", kase.Commands)
	}
	// Reopened: a new conversation.
	if r, b := send(t, ops, http.MethodPost, cases+"/C-1/reopen", `{"reason":"It restarts again."}`); r.StatusCode != 200 {
		t.Fatalf("reopen: %d %s", r.StatusCode, b)
	}
	if r, b := send(t, ops, http.MethodPost, chat+"/ask", `{"question":"Why is web/shop failing?"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask again: %d %s", r.StatusCode, b)
	}
	sh.Wait()
	if _, v := conversation(ops); len(v.Messages) != 2 {
		t.Fatalf("reopened: %+v", v.Messages)
	}
}
