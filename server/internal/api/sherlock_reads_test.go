package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/watson"
)

// Sherlock's reads of the console's records (Phase 12.1, SR-01 to SR-08)
// reach the console's own routes, as the person asking: a SuperAdmin
// reads Automation, the audit log and the jobs' runs, a Viewer none of
// them but their own Watson history. sherlocktools' tests hold the
// redaction and the bounds; these hold the paths and the permissions.
func TestSherlockReadsTheConsolesRecordsAsThePerson(t *testing.T) {
	_, h, _ := jobsSetup(t, func(o *Options) {
		st := automation.NewStore(&kv.Memory{}, nil)
		o.Automation, o.AutomationStore = automation.NewEngine(automation.Options{Store: st, Exec: noExec{}}), st
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog()})
		// Watson needs a connection to plan on; the history never uses it.
		o.Cluster.(*source).restCfg = &rest.Config{Host: "http://127.0.0.1:1"}
	})
	sam := auth.Session{ID: "s", User: auth.User{Username: "sam", Name: "Sam"}, Admin: true, Roles: []roles.ID{roles.SuperAdmin}, Super: []roles.ID{roles.SuperAdmin}}
	vera := auth.Session{ID: "v", User: auth.User{Username: "vera", Name: "Vera", KubeName: "221b-local:vera"}, Roles: []roles.ID{roles.Viewer}}
	run := func(sess auth.Session, name string, in map[string]any) (string, bool) {
		raw, _ := json.Marshal(in)
		r := h.newToolbox(clusters.LocalID, h.opts.Cluster, sess, true).Run(context.Background(), name, raw)
		if strings.Contains(r.Text, "page not found") {
			t.Fatalf("%s reaches no route: %s", name, r.Text)
		}
		return r.Text, r.Error
	}
	for name, in := range map[string]map[string]any{
		"list_scripts":   nil,
		"audit_log":      {"action": "auth"},
		"list_job_runs":  nil,
		"watson_history": nil,
	} {
		if text, failed := run(sam, name, in); failed {
			t.Errorf("a SuperAdmin's %s: %s", name, text)
		}
	}
	// What is not there, by its id or name.
	missing := func(name string) map[string]any {
		if name == "get_job_run" {
			return map[string]any{"id": "run-5"}
		}
		return map[string]any{"id": 5, "name": "nope"}
	}
	for name, want := range map[string]string{"get_automation_run": "no run 5", "get_script": `no script "nope"`, "get_job_run": "Not found"} {
		if text, failed := run(sam, name, missing(name)); !failed || !strings.Contains(text, want) {
			t.Errorf("%s of what is not there: %s", name, text)
		}
	}
	for _, name := range []string{"get_automation_run", "list_scripts", "get_script", "automation_variables", "audit_log", "list_job_runs", "get_job_run"} {
		if text, failed := run(vera, name, missing(name)); !failed || !strings.Contains(text, "Not allowed") {
			t.Errorf("a Viewer's %s: %s", name, text)
		}
	}
	if text, failed := run(vera, "watson_history", nil); failed || !strings.Contains(text, `"total":0`) {
		t.Errorf("a Viewer's Watson history: %s", text)
	}
}
