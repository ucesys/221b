package sherlocktools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The console's records (SR-01 to SR-08) are read through the API of the
// screen each mirrors, as the person asking: here a stand-in API that,
// like the console's, lets a SuperAdmin into Automation and the audit log
// and the super roles into the cases and runs.

var (
	viewer = auth.Session{ID: "v", User: auth.User{Username: "vera", Name: "Vera"}, Roles: []roles.ID{roles.Viewer}}
	super  = auth.Session{ID: "s", User: auth.User{Username: "sam", Name: "Sam"}, Admin: true, Roles: []roles.ID{roles.SuperAdmin}, Super: []roles.ID{roles.SuperAdmin}}
)

// consoleAPI is a stand-in for the console's API: each route answers its
// JSON to a SuperAdmin, or to a holder of a super role when reader says
// so, and refuses anyone else as the console does.
type consoleAPI struct {
	mux  *http.ServeMux
	asks map[string]string
}

func newConsoleAPI() *consoleAPI {
	return &consoleAPI{mux: http.NewServeMux(), asks: map[string]string{}}
}

func (c *consoleAPI) route(pattern string, reader bool, answer func(r *http.Request) any) {
	c.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		c.asks[r.URL.Path] = r.URL.RawQuery
		sess, _ := auth.SessionFrom(r.Context())
		may := sess.Admin
		for _, id := range sess.Roles {
			may = may || reader && id.Super()
		}
		w.Header().Set("Content-Type", "application/json")
		if !may {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Only a SuperAdmin may read this."}`))
			return
		}
		b, _ := json.Marshal(answer(r))
		_, _ = w.Write(b)
	})
}

func (c *consoleAPI) run(t *testing.T, sess auth.Session, name string, in map[string]any) sherlock.ToolResult {
	t.Helper()
	raw, _ := json.Marshal(in)
	return New(Console{API: c.mux}, "local", sess, true).Run(context.Background(), name, raw)
}

func lines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s line %d\n", prefix, i)
	}
	return b.String()
}

// get_automation_run shows a run's output on each node, the last lines
// only, scrubbed, with the nodes that failed first and at most 20 of them;
// never the parameters' values (SR-01).
func TestAutomationRunOutputIsScrubbedAndBounded(t *testing.T) {
	api := newConsoleAPI()
	api.route("GET /api/v1/automation/runs/{id}", false, func(r *http.Request) any {
		var nodes []map[string]any
		out := map[string]string{}
		for i := 1; i <= 25; i++ {
			n := fmt.Sprintf("n%02d", i)
			state := "ok"
			if i == 23 {
				state = "failed"
			}
			nodes = append(nodes, map[string]any{"node": n, "state": state, "exit": 0, "values": map[string]string{"REGION": "eu-west-hidden"}})
			out[n] = lines(n, 300) + "export DB_PASSWORD=hunter2\n"
		}
		nodes[22]["error"] = "exit 3: token sk-abcdefghijklmnopqrstuvwx"
		return map[string]any{"run": map[string]any{"id": 7, "script": "patch", "state": "failed", "overrides": map[string]string{"DB": "secret:db/creds/pw"}, "nodes": nodes}, "output": out}
	})
	if r := api.run(t, viewer, "get_automation_run", map[string]any{"id": 7}); !r.Error || !strings.Contains(r.Text, "Not allowed") {
		t.Fatalf("a Viewer read the run: %s", r.Text)
	}
	r := api.run(t, super, "get_automation_run", map[string]any{"id": 7})
	if r.Error {
		t.Fatal(r.Text)
	}
	for _, leak := range []string{"hunter2", "sk-abcdefghijklmnop", "eu-west-hidden", "secret:db", "n01 line 240\n"} {
		if strings.Contains(r.Text, leak) {
			t.Errorf("the run shows %q", leak)
		}
	}
	var run struct {
		Nodes []struct {
			Node, State, Output, Error string
		} `json:"nodes"`
		NodesNote string `json:"nodesNote"`
	}
	if err := json.Unmarshal([]byte(r.Text), &run); err != nil {
		t.Fatal(err)
	}
	if len(run.Nodes) != maxRunNodes || run.Nodes[0].Node != "n23" || run.NodesNote == "" || !strings.Contains(run.Nodes[0].Error, "[REDACTED]") {
		t.Fatalf("the nodes: %d, first %+v, note %q", len(run.Nodes), run.Nodes[0], run.NodesNote)
	}
	if got := strings.Count(run.Nodes[1].Output, "\n") + 1; got != 60 || !strings.Contains(run.Nodes[1].Output, "DB_PASSWORD=[REDACTED]") {
		t.Fatalf("%d lines by default: %q", got, run.Nodes[1].Output)
	}
	r = api.run(t, super, "get_automation_run", map[string]any{"id": 7, "node": "n02", "tail": 5000})
	_ = json.Unmarshal([]byte(r.Text), &run)
	if len(run.Nodes) != 1 || strings.Count(run.Nodes[0].Output, "\n")+1 != maxTail {
		t.Fatalf("one node, at most %d lines: %s", maxTail, r.Text)
	}
	if r := api.run(t, super, "get_automation_run", map[string]any{"id": 7, "node": "elsewhere"}); !r.Error {
		t.Fatalf("a node the run did not reach: %s", r.Text)
	}
	if r := api.run(t, super, "get_automation_run", map[string]any{}); !r.Error {
		t.Fatalf("no run named: %s", r.Text)
	}
}

func automationRoutes(api *consoleAPI) {
	api.route("GET /api/v1/automation", false, func(*http.Request) any {
		return map[string]any{"scripts": []map[string]any{
			{"name": "rotate", "version": 2, "state": "approved", "params": []map[string]any{{"name": "DB_REF", "secret": true}, {"name": "REGION"}}},
			{"name": "patch", "version": 1, "state": "draft", "params": []map[string]any{}},
		}}
	})
	api.route("GET /api/v1/automation/scripts/{name}", false, func(r *http.Request) any {
		return map[string]any{"name": r.PathValue("name"), "versions": []map[string]any{
			{"n": 1, "state": "approved", "text": "#!/bin/sh\necho one\n", "may": map[string]bool{"approve": true}},
			{"n": 2, "state": "draft", "text": "#!/bin/sh\nexport API_KEY=abcd1234efgh5678\necho two\n", "author": map[string]any{"name": "Ann"}},
		}}
	})
	api.route("GET /api/v1/automation/fleets", false, func(*http.Request) any {
		return map[string]any{"cluster": "local", "fleets": []map[string]any{
			{"id": "db", "name": "Databases", "nodes": []map[string]any{{"name": "n1"}}, "params": map[string]any{
				"values": map[string]any{"DB_REF": "secret:db/creds/pw", "REGION": "eu", "OTHER": "secret:x/y/z"},
				"nodes":  map[string]any{"n1": map[string]any{"DB_REF": "secret:db/n1/pw", "REGION": "us"}},
			}},
			{"id": "web", "name": "Web", "nodes": []map[string]any{{"name": "n2"}}, "params": map[string]any{"values": map[string]any{"REGION": "ap"}}},
			{"id": "_all", "name": "All nodes", "nodes": []map[string]any{{"name": "n1"}, {"name": "n2"}}, "params": map[string]any{}},
		}}
	})
}

// The scripts and their variables are a SuperAdmin's, as Automation is;
// a script's text is scrubbed, and a secret parameter shows only that it
// is set (SR-05, SR-06).
func TestScriptsAndVariablesKeepSecretValues(t *testing.T) {
	api := newConsoleAPI()
	automationRoutes(api)
	for _, name := range []string{"list_scripts", "get_script", "automation_variables"} {
		if r := api.run(t, viewer, name, map[string]any{"name": "rotate"}); !r.Error || !strings.Contains(r.Text, "Not allowed") {
			t.Errorf("a Viewer read %s: %s", name, r.Text)
		}
	}
	if r := api.run(t, super, "list_scripts", map[string]any{"query": "rotate"}); r.Error || !strings.Contains(r.Text, `"total":1`) || !strings.Contains(r.Text, "DB_REF") {
		t.Fatalf("list_scripts: %s", r.Text)
	}
	r := api.run(t, super, "get_script", map[string]any{"name": "rotate"})
	if r.Error || !strings.Contains(r.Text, "echo two") || strings.Contains(r.Text, "abcd1234efgh5678") || !strings.Contains(r.Text, "[REDACTED]") || !strings.Contains(r.Text, `"versions"`) {
		t.Fatalf("the latest version: %s", r.Text)
	}
	if r := api.run(t, super, "get_script", map[string]any{"name": "rotate", "version": 1}); r.Error || !strings.Contains(r.Text, "echo one") || strings.Contains(r.Text, `"may"`) {
		t.Fatalf("version 1: %s", r.Text)
	}
	if r := api.run(t, super, "get_script", map[string]any{"name": "rotate", "version": 9}); !r.Error {
		t.Fatalf("version 9: %s", r.Text)
	}
	r = api.run(t, super, "automation_variables", nil)
	if r.Error || strings.Contains(r.Text, "secret:") || strings.Count(r.Text, secretShown) != 3 || !strings.Contains(r.Text, `"REGION":"eu"`) || !strings.Contains(r.Text, `"REGION":"ap"`) {
		t.Fatalf("the variables: %s", r.Text)
	}
	r = api.run(t, super, "automation_variables", map[string]any{"node": "n2"})
	if r.Error || strings.Contains(r.Text, "Databases") || !strings.Contains(r.Text, "Web") || !strings.Contains(r.Text, "All nodes") {
		t.Fatalf("the fleets of n2: %s", r.Text)
	}
	if r := api.run(t, super, "automation_variables", map[string]any{"fleet": "nope"}); !r.Error {
		t.Fatalf("no such fleet: %s", r.Text)
	}
}

// watson_history is the commands the person's Watson panel lists, read as
// them, with the last lines of each command's output, scrubbed (SR-03).
func TestWatsonHistoryIsThePersonsAndScrubbed(t *testing.T) {
	api := newConsoleAPI()
	api.mux.HandleFunc("GET /api/v1/watson/history", func(w http.ResponseWriter, r *http.Request) {
		api.asks[r.URL.Path] = r.URL.RawQuery
		sess, _ := auth.SessionFrom(r.Context())
		var cmds []map[string]any
		if sess.ID == "v" {
			cmds = []map[string]any{
				{"id": "w2", "command": "kubectl rollout restart deployment/api -n web", "kind": "change", "status": "failed", "exit": 1, "reason": "forbidden", "plan": map[string]any{"impact": []string{"long"}}, "lines": []map[string]string{{"k": "err", "t": "Authorization: Bearer abcdefghijklmnopqrstuvwxyz"}}},
				{"id": "w1", "command": "kubectl get pods -n web", "kind": "read", "status": "done", "lines": []map[string]string{{"k": "out", "t": "api-1 Running"}}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": cmds})
	})
	r := api.run(t, viewer, "watson_history", nil)
	if r.Error || strings.Contains(r.Text, "abcdefghijklmnop") || !strings.Contains(r.Text, "[REDACTED]") || strings.Contains(r.Text, `"plan"`) || !strings.Contains(r.Text, `"total":2`) {
		t.Fatalf("the history: %s", r.Text)
	}
	if api.asks["/api/v1/watson/history"] != "lines=10" {
		t.Fatalf("asked %q", api.asks["/api/v1/watson/history"])
	}
	if r := api.run(t, viewer, "watson_history", map[string]any{"status": "done", "lines": 500}); !strings.Contains(r.Text, `"total":1`) || !strings.Contains(r.Text, "api-1 Running") || api.asks["/api/v1/watson/history"] != "lines=50" {
		t.Fatalf("done only, at most 50 lines: %s (asked %q)", r.Text, api.asks["/api/v1/watson/history"])
	}
	if r := api.run(t, super, "watson_history", nil); !strings.Contains(r.Text, `"total":0`) {
		t.Fatalf("another person's commands: %s", r.Text)
	}
}

// The audit log is Administration's, a SuperAdmin's; its filters reach
// the page's API and the limit is bounded (SR-04).
func TestAuditLogIsASuperAdmins(t *testing.T) {
	api := newConsoleAPI()
	api.route("GET /api/v1/admin/audit", false, func(*http.Request) any {
		return map[string]any{"events": []map[string]any{{"action": "watson.command", "outcome": "ok", "detail": "kubectl get secret --token abcdefghijklmnopq"}}, "total": 40, "actions": []string{"watson.command"}}
	})
	if r := api.run(t, viewer, "audit_log", nil); !r.Error || !strings.Contains(r.Text, "Not allowed") {
		t.Fatalf("a Viewer read the audit log: %s", r.Text)
	}
	r := api.run(t, super, "audit_log", map[string]any{"query": "drain worker", "action": "watson", "outcome": "denied", "limit": 9999})
	if r.Error || strings.Contains(r.Text, "abcdefghijklmnopq") || !strings.Contains(r.Text, `"kept":40`) {
		t.Fatalf("the audit log: %s", r.Text)
	}
	if got := api.asks["/api/v1/admin/audit"]; got != "action=watson&limit=200&outcome=denied&q=drain+worker" {
		t.Fatalf("asked %q", got)
	}
}

// The cases and the jobs' runs are for those who may see AIOps, read
// through its API; a case's messages and a run's report and findings are
// bounded (SR-07, SR-08).
func TestCasesAndJobRuns(t *testing.T) {
	api := newConsoleAPI()
	api.route("GET /api/v1/aiops/cases", true, func(*http.Request) any {
		return map[string]any{"cases": []map[string]any{{"id": "C-1", "title": "api crashes", "status": "new", "key": map[string]string{"check": "x"}}, {"id": "C-2", "title": "disk", "status": "closed"}}, "mayChange": true}
	})
	api.route("GET /api/v1/aiops/cases/{id}", true, func(r *http.Request) any {
		var msgs []map[string]any
		for i := 1; i <= 30; i++ {
			msgs = append(msgs, map[string]any{"id": i, "text": fmt.Sprintf("message %d password=hunter%d", i, i)})
		}
		msgs[29]["text"] = strings.Repeat("x", 5000)
		return map[string]any{"id": r.PathValue("id"), "title": "api crashes", "messages": msgs, "mayChange": true, "clusters": []string{"a"}}
	})
	api.route("GET /api/v1/runs", true, func(*http.Request) any {
		return map[string]any{"runs": []map[string]any{{"id": "run-3", "job": "nightly", "status": "ok"}}, "total": 1}
	})
	api.route("GET /api/v1/runs/{id}", true, func(r *http.Request) any {
		var fs []map[string]any
		for i := 0; i < 60; i++ {
			fs = append(fs, map[string]any{"id": fmt.Sprintf("%s.%d", r.PathValue("id"), i), "title": "finding"})
		}
		return map[string]any{"id": r.PathValue("id"), "report": strings.Repeat("r", 20000), "findings": fs, "mayCancel": true}
	})
	for _, name := range []string{"list_cases", "get_case", "list_job_runs", "get_job_run"} {
		if r := api.run(t, viewer, name, map[string]any{"id": "C-1"}); !r.Error || !strings.Contains(r.Text, "Not allowed") {
			t.Errorf("a Viewer read %s: %s", name, r.Text)
		}
	}
	reader := auth.Session{ID: "r", User: auth.User{Username: "sue"}, Roles: []roles.ID{roles.SuperViewer}, Super: []roles.ID{roles.SuperViewer}}
	r := api.run(t, reader, "list_cases", map[string]any{"status": "new", "open": true, "query": "api", "limit": 1})
	if r.Error || !strings.Contains(r.Text, `"total":2`) || strings.Contains(r.Text, "disk") || strings.Contains(r.Text, `"key"`) {
		t.Fatalf("list_cases: %s", r.Text)
	}
	if got := api.asks["/api/v1/aiops/cases"]; got != "open=1&q=api&status=new" {
		t.Fatalf("asked %q", got)
	}
	r = api.run(t, reader, "get_case", map[string]any{"id": "C-1"})
	if r.Error || strings.Contains(r.Text, "hunter") || strings.Contains(r.Text, "message 10 ") || !strings.Contains(r.Text, "message 11 ") || strings.Contains(r.Text, `"mayChange"`) || strings.Contains(r.Text, strings.Repeat("x", 2001)) {
		t.Fatalf("get_case: %.600s", r.Text)
	}
	if r := api.run(t, reader, "list_job_runs", map[string]any{"job": "nightly", "limit": 500}); r.Error || !strings.Contains(r.Text, "run-3") || api.asks["/api/v1/runs"] != "job=nightly&limit=100" {
		t.Fatalf("list_job_runs: %s (asked %q)", r.Text, api.asks["/api/v1/runs"])
	}
	r = api.run(t, reader, "get_job_run", map[string]any{"id": "run-3"})
	var run struct {
		Report   string `json:"report"`
		Findings []any  `json:"findings"`
	}
	_ = json.Unmarshal([]byte(r.Text), &run)
	if r.Error || len(run.Findings) != 50 || len(run.Report) > maxText+100 || strings.Contains(r.Text, "mayCancel") {
		t.Fatalf("get_job_run: %d findings, report %d bytes", len(run.Findings), len(run.Report))
	}
}
