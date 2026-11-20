package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// sensitiveEnv is a production cluster whose Automation keeps its
// variables, changes and runs in a database and its sensitive values in a
// store of their own; the console's log is kept.
type sensitiveEnv struct {
	autoEnv
	d    *db.DB
	sens *automation.MemorySensitive
	logs *lockedBuffer
}

// readsMode is a Kubernetes script that names MODE on a kubectl line: a
// sensitive value never goes there, so its node fails, and it can be
// tried again.
const readsMode = "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:dry-run yes\n# 221b:k8s get pods\n# 221b:param MODE optional m\nkubectl get pods -n $MODE\n"

func sensitiveSetup(t *testing.T, env string, mods ...func(*Options)) sensitiveEnv {
	t.Helper()
	d := dbtest.Open(t)
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	st := automation.NewStore(&kv.Keyed{Keys: map[string]kv.Backend{automation.VariablesKey: automation.NewVariablesPG(d), automation.ChangesKey: automation.NewChangesPG(d),
		automation.RunsKey: automation.NewRunsPG(d), automation.ScriptsKey: automation.NewScriptsPG(d)}, Rest: &kv.Memory{}}, nil)
	sens := automation.NewMemorySensitive()
	st.SensitiveInTables(d, sens, log)
	eng := automation.NewEngine(automation.Options{Store: st, Exec: noExec{}, Logger: log})
	srv, c, m, auditOut, local, store := multiSetupWith(t, "ops@corp.example", nil, append([]func(*Options){func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: log})
		o.Automation, o.AutomationStore = eng, st
		o.Logger = log
	}}, mods...)...)
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: env, Mode: clusters.ModeWrite}, local, store); err != nil {
		t.Fatal(err)
	}
	return sensitiveEnv{autoEnv: autoEnv{srv: srv, c: c, store: st, engine: eng, audit: auditOut, local: local}, d: d, sens: sens, logs: logs}
}

// No sensitive value is ever recorded (DB-71): set, changed with
// approval, rejected, used by runs that inherit it and by runs that give
// their own (asked for, approved, rejected, a dry run), a run tried again
// and a backup taken, the value is in no table, no audit event, no log
// line, no answer of the API and no backup file in clear. It reaches a
// run only from the Secret, at the moment it starts.
func TestNoSensitiveValueIsEverRecorded(t *testing.T) {
	const marker = "MARKERapi7Rq2sensitive"
	e := sensitiveSetup(t, clusters.EnvProduction, func(o *Options) { o.Backup = keyed(testKeyA) })
	ctx := context.Background()
	var answers []string
	do := func(c *http.Client, method, path, body string, want int) string {
		t.Helper()
		code, answer := doAs(t, c, e.autoEnv, method, path, body)
		if code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, answer)
		}
		answers = append(answers, method+" "+path+": "+answer)
		return answer
	}
	me, other := e.c, signIn(t, e.srv, "ops2@corp.example")
	approved(t, e.store, "reads", valuesScript)
	approved(t, e.store, "names-mode", readsMode)
	if err := e.store.SetVariables(ctx, clusters.LocalID, allNodes, automation.FleetVariables{Values: map[string]string{"MODE": marker + "-first", "ZONE": "eu"}, Sensitive: []string{"MODE"}}); err != nil {
		t.Fatal(err)
	}

	// The page reads the digest, and sends it back unchanged with another
	// value changed: on production a change of a sensitive name waits.
	page := do(me, "GET", "/api/v1/automation/fleets", "", http.StatusOK)
	vars, _ := e.store.Variables(ctx, clusters.LocalID)
	first := vars[allNodes].Values["MODE"]
	if !automation.IsHidden(first) || !strings.Contains(page, first) {
		t.Fatalf("the page reads %s", page)
	}
	do(me, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"MODE":"`+marker+`-changed","ZONE":"eu"},"sensitive":["MODE"],"reason":"rotate it"}`, http.StatusAccepted)
	do(me, "GET", "/api/v1/automation/variable-changes", "", http.StatusOK)
	do(me, "GET", "/api/v1/admin/approvals", "", http.StatusOK)
	do(other, "POST", "/api/v1/automation/variable-changes/1/approve", "{}", http.StatusOK)
	vars, _ = e.store.Variables(ctx, clusters.LocalID)
	changed := vars[allNodes].Values["MODE"]
	if got, _ := e.store.Reveal(ctx, clusters.LocalID, []string{changed}); got[changed] != marker+"-changed" {
		t.Fatal("the approved change did not set the value it waited with")
	}
	do(me, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"MODE":"`+marker+`-refused","ZONE":"eu"},"sensitive":["MODE"]}`, http.StatusAccepted)
	do(other, "POST", "/api/v1/automation/variable-changes/2/reject", `{"comment":"not this one"}`, http.StatusOK)
	for _, d := range heldDigests(t, e.sens) {
		if v, _ := e.store.Reveal(ctx, clusters.LocalID, []string{d}); v[d] == marker+"-refused" {
			t.Fatal("a rejected change's value stays in the Secret")
		}
	}

	// A run that inherits the value, one that gives its own (asked for and
	// approved, asked for and rejected), and a dry run with its own.
	do(me, "POST", "/api/v1/automation/runs", runBody(`{}`), http.StatusOK)
	do(me, "POST", "/api/v1/automation/runs/weigh", runBody(`{"MODE":"`+marker+`-own"}`), http.StatusOK)
	asked := do(me, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"`+marker+`-own"}`), http.StatusAccepted)
	do(me, "GET", "/api/v1/automation/waiting-runs", "", http.StatusOK)
	do(me, "GET", "/api/v1/admin/approvals", "", http.StatusOK)
	var waiting struct {
		Waiting automation.WaitingRun `json:"waiting"`
	}
	if err := json.Unmarshal([]byte(asked), &waiting); err != nil || !automation.IsHidden(waiting.Waiting.Params["MODE"]) {
		t.Fatalf("the request: %s: %v", asked, err)
	}
	if got, _ := e.store.Reveal(ctx, clusters.LocalID, []string{waiting.Waiting.Params["MODE"]}); got[waiting.Waiting.Params["MODE"]] != marker+"-own" {
		t.Fatal("the value a run request waits with is not in the Secret")
	}
	do(other, "POST", "/api/v1/automation/waiting-runs/1/approve", "", http.StatusOK)
	if got, _ := e.store.Reveal(ctx, clusters.LocalID, []string{waiting.Waiting.Params["MODE"]}); len(got) != 0 {
		t.Fatal("a run's own value stays in the Secret once the run started")
	}
	do(me, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"`+marker+`-unwanted"}`), http.StatusAccepted)
	do(other, "POST", "/api/v1/automation/waiting-runs/2/reject", `{"comment":"no"}`, http.StatusOK)
	do(me, "POST", "/api/v1/automation/runs", `{"script":"reads","fleet":"_cluster","dryRun":true,"params":{"MODE":"`+marker+`-dry"},"rollout":{"mode":"all"}}`, http.StatusOK)

	// A run whose kubectl line names MODE fails on its node, without the
	// value; tried again, it reads the value as it is now.
	failed := do(me, "POST", "/api/v1/automation/runs", `{"script":"names-mode","fleet":"_cluster","rollout":{"mode":"all"}}`, http.StatusOK)
	var run automation.Run
	_ = json.Unmarshal([]byte(failed), &run)
	done := waitAutomationRun(t, e.engine, run.ID)
	if done.Nodes[0].State != automation.NodeFailed || !strings.Contains(done.Nodes[0].Error+done.Nodes[0].Msg+done.Nodes[0].Tail, "MODE, a secret or sensitive value") {
		t.Fatalf("the run that names MODE: %+v", done.Nodes[0])
	}
	do(me, "POST", "/api/v1/automation/runs/"+strconv.Itoa(run.ID)+"/retry", "{}", http.StatusOK)

	// A row whose value the Secret lost: a run that needs it is refused,
	// naming the variable.
	if err := e.sens.Drop(ctx, clusters.LocalID, []string{changed}); err != nil {
		t.Fatal(err)
	}
	refused := do(me, "POST", "/api/v1/automation/runs", runBody(`{}`), http.StatusBadRequest)
	if !strings.Contains(refused, "MODE on Here: a sensitive variable whose value the console does not keep any more") {
		t.Fatalf("a run without its value: %s", refused)
	}
	if err := e.store.SetVariables(ctx, clusters.LocalID, allNodes, automation.FleetVariables{Values: map[string]string{"MODE": marker + "-again", "ZONE": "eu"}, Sensitive: []string{"MODE"}}); err != nil {
		t.Fatal(err)
	}

	// Every answer of the API about it.
	for _, path := range []string{"/api/v1/automation", "/api/v1/automation/fleets", "/api/v1/automation/runs", "/api/v1/automation/runs/1", "/api/v1/automation/variable-changes",
		"/api/v1/automation/waiting-runs", "/api/v1/admin/approvals"} {
		do(me, "GET", path, "", http.StatusOK)
	}
	// The backup: the value sealed with the install's key.
	resp, body := send(t, me, http.MethodGet, e.srv.URL+"/api/v1/admin/backup", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the backup: %s", resp.Status)
	}
	file := unzip(t, body)
	var f backupFile
	if err := json.Unmarshal([]byte(file), &f); err != nil {
		t.Fatal(err)
	}
	if v := f.Automation.Variables[clusters.LocalID][allNodes].Values; !backup.SealedValue(v["MODE"]) || v["ZONE"] != "eu" {
		t.Fatalf("the backup's variables: %+v", v)
	}

	if h := dbtest.Holding(t, e.d, marker); len(h) > 0 {
		t.Errorf("the database holds the value in %v", h)
	}
	for _, where := range []struct{ name, text string }{{"the audit log", e.audit.String()}, {"the log", e.logs.String()}, {"the backup file", file}} {
		if strings.Contains(where.text, marker) {
			t.Errorf("%s holds the value:\n%s", where.name, where.text)
		}
	}
	for _, a := range answers {
		if strings.Contains(a, marker) {
			t.Errorf("an answer holds the value: %s", a)
		}
	}
}

// A backup of before this change holds sensitive values in clear: it
// restores, and the values go to the Secret, not the table. One sealed
// with this install's key opens; one sealed with another is listed to set
// again.
func TestABackupRestoresSensitiveValuesIntoTheSecret(t *testing.T) {
	const marker = "MARKERbk3Lm9restore"
	ctx := context.Background()
	src := sensitiveSetup(t, clusters.EnvOther, func(o *Options) { o.Backup = keyed(testKeyA) })
	if err := src.store.SetVariables(ctx, clusters.LocalID, allNodes, automation.FleetVariables{Values: map[string]string{"MODE": marker + "-sealed", "ZONE": "eu"}, Sensitive: []string{"MODE"}}); err != nil {
		t.Fatal(err)
	}
	_, body := send(t, src.c, http.MethodGet, src.srv.URL+"/api/v1/admin/backup", "")
	var sealed backupFile
	if err := json.Unmarshal([]byte(unzip(t, body)), &sealed); err != nil {
		t.Fatal(err)
	}
	old := sealed
	oldVars := map[string]map[string]automation.FleetVariables{clusters.LocalID: {allNodes: {Values: map[string]string{"MODE": marker + "-clear", "ZONE": "us"}, Sensitive: []string{"MODE"}}}}
	old.Automation = &backupAutomation{Variables: oldVars}

	for _, c := range []struct {
		name string
		file backupFile
		key  string
		want string
	}{{"sealed", sealed, testKeyA, marker + "-sealed"}, {"in clear, from before", old, testKeyA, marker + "-clear"}, {"sealed with another key", sealed, testKeyB, ""}} {
		// Off production, so the values apply at once.
		dst := sensitiveSetup(t, clusters.EnvOther, func(o *Options) { o.Backup = keyed(c.key) })
		in, _ := json.Marshal(restoreInput{Backup: c.file, Parts: map[string]bool{"automation": true}, WithoutSecrets: true})
		resp, answer := send(t, dst.c, http.MethodPost, dst.srv.URL+"/api/v1/admin/restore", string(in))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: the restore: %s %s", c.name, resp.Status, answer)
		}
		vars, err := dst.store.Variables(ctx, clusters.LocalID)
		if err != nil {
			t.Fatal(err)
		}
		mode, ok := vars[allNodes].Values["MODE"]
		if c.want == "" {
			if ok || !strings.Contains(answer, "the value of MODE on fleet") {
				t.Fatalf("%s: MODE reads %q, the answer %s", c.name, mode, answer)
			}
		} else {
			got, _ := dst.store.Reveal(ctx, clusters.LocalID, []string{mode})
			if !automation.IsHidden(mode) || got[mode] != c.want {
				t.Fatalf("%s: MODE reads %q, the Secret %v", c.name, mode, got)
			}
		}
		if h := dbtest.Holding(t, dst.d, marker); len(h) > 0 {
			t.Fatalf("%s: the database holds the value in %v", c.name, h)
		}
		if strings.Contains(answer, marker) || strings.Contains(dst.audit.String(), marker) || strings.Contains(dst.logs.String(), marker) {
			t.Fatalf("%s: the answer, the audit log or the log holds the value", c.name)
		}
	}
}

func heldDigests(t *testing.T, s automation.SensitiveStore) []string {
	t.Helper()
	all, err := s.Held(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all[clusters.LocalID]
}

func waitAutomationRun(t *testing.T, e *automation.Engine, id int) automation.Run {
	t.Helper()
	for i := 0; i < 400 && e.Running(id); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	r, _, _, err := e.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
