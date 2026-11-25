package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// The fixes of the review of server/internal/automation (November 24,
// 2026), through the API.

// On production a sensitive value reaches a parameter of another name only
// with a second admin (FU-05, review finding 1): a binding to a sensitive
// variable is weighed as sensitive, and a digest given under a name that
// is not sensitive is refused, as the variables refuse it.
func TestOnProductionASensitiveValueNeedsASecondAdminUnderAnyName(t *testing.T) {
	e := valuesSetup(t)
	code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"var:MODE"}`))
	if code != http.StatusAccepted || !strings.Contains(body, "ZONE is marked sensitive") {
		t.Fatalf("a binding to the sensitive MODE: %d %s", code, body)
	}
	if runs, _ := e.engine.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("it started: %+v", runs)
	}
	vars, err := e.store.Variables(context.Background(), clusters.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	digest := vars[allNodes].Values["MODE"]
	code, body = e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"`+digest+`"}`))
	if code != http.StatusBadRequest || !strings.Contains(body, "ZONE is given a hidden value") {
		t.Fatalf("MODE's digest under ZONE: %d %s", code, body)
	}
	if runs, _ := e.engine.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("it started: %+v", runs)
	}
}

// slowScripts is a store's backend whose reads of the scripts take a
// while when slow is set: an approval of a run request stays between its
// check and its record that long.
type slowScripts struct {
	kv.Backend
	slow atomic.Bool
}

func (g *slowScripts) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	if key == automation.ScriptsKey && g.slow.Load() {
		time.Sleep(30 * time.Millisecond)
	}
	return g.Backend.LoadKey(ctx, key)
}

// autoSetupOver is autoSetup over the backend given.
func autoSetupOver(t *testing.T, who, env string, b kv.Backend) autoEnv {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	st := automation.NewStore(b, nil)
	eng := automation.NewEngine(automation.Options{Store: st, Exec: noExec{}})
	srv, c, m, auditOut, local, store := multiSetupWith(t, who, nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Automation, o.AutomationStore = eng, st
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: env, Mode: clusters.ModeWrite}, local, store); err != nil {
		t.Fatal(err)
	}
	return autoEnv{srv: srv, c: c, store: st, engine: eng, audit: auditOut, local: local}
}

// A run request rejected while a second admin's approval is starting it
// never both runs and stays rejected (finding 5): a rejection is one
// critical section with the approvals. Each round asks for a run and has
// it approved and rejected at once; whichever comes first decides it.
func TestARunRequestRejectedWhileItIsApprovedDoesNotRunRejected(t *testing.T) {
	g := &slowScripts{Backend: &kv.Memory{}}
	e := autoSetupOver(t, "ops@corp.example", clusters.EnvProduction, g)
	ctx := context.Background()
	approved(t, e.store, "reads", valuesScript)
	if err := e.store.SetVariables(ctx, clusters.LocalID, allNodes, automation.FleetVariables{Values: map[string]string{"MODE": "keep"}, Sensitive: []string{"MODE"}}); err != nil {
		t.Fatal(err)
	}
	other := signIn(t, e.srv, "ops2@corp.example")
	type answer struct {
		code int
		body string
	}
	for round := 1; round <= 20; round++ {
		g.slow.Store(false)
		if code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"wipe-`+strconv.Itoa(round)+`"}`)); code != http.StatusAccepted {
			t.Fatalf("round %d: %d %s", round, code, body)
		}
		g.slow.Store(true)
		path := "/api/v1/automation/waiting-runs/" + strconv.Itoa(round) + "/"
		approvedAns, rejectedAns := make(chan answer, 1), make(chan answer, 1)
		go func() {
			code, body := e.do(t, "POST", path+"reject", `{"comment":"withdrawn"}`)
			rejectedAns <- answer{code, body}
		}()
		go func() {
			code, body := doAs(t, other, e, "POST", path+"approve", "")
			approvedAns <- answer{code, body}
		}()
		a, r := <-approvedAns, <-rejectedAns
		list, _ := e.store.WaitingRuns(ctx, "")
		runs, _ := e.engine.Runs(ctx)
		var state string
		for _, w := range list {
			if w.ID == round {
				state = w.State
			}
		}
		started := 0
		for _, run := range runs {
			if run.ValuesApproved != nil && run.ValuesApproved.Request == round {
				started++
			}
		}
		if (state == automation.ChangeApproved) != (started == 1) || started > 1 || (a.code == http.StatusOK) == (r.code == http.StatusOK) {
			t.Fatalf("round %d: request %s with %d runs (approve %d %s; reject %d %s)", round, state, started, a.code, a.body, r.code, r.body)
		}
	}
}

// meanwhile is a store's backend that, armed, runs on once when the
// scripts are read next: what someone else saves while a save is being
// weighed (Sherlock may take 45 s).
type meanwhile struct {
	kv.Backend
	armed atomic.Bool
	on    func()
}

func (m *meanwhile) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	if key == automation.ScriptsKey && m.armed.CompareAndSwap(true, false) {
		m.on()
	}
	return m.Backend.LoadKey(ctx, key)
}

// A save of a fleet's variables sets what it changes and nothing else
// (finding 6): a value someone else saved while it was weighed stays, and
// one it would set that changed meanwhile refuses it.
func TestAFleetSaveDoesNotUndoWhatChangedWhileItWasWeighed(t *testing.T) {
	m := &meanwhile{Backend: &kv.Memory{}}
	e := autoSetupOver(t, "ops@corp.example", clusters.EnvOther, m)
	ctx := context.Background()
	approved(t, e.store, "reads", valuesScript)
	set := func(values map[string]string, sensitive ...string) {
		t.Helper()
		if err := e.store.SetVariables(ctx, clusters.LocalID, allNodes, automation.FleetVariables{Values: values, Sensitive: sensitive}); err != nil {
			t.Fatal(err)
		}
	}
	set(map[string]string{"X": "1", "Y": "old", "PW": "pw-plain"})
	// Ann saves X=2, sending Y and PW as she read them; meanwhile Y is
	// changed and PW marked sensitive.
	m.on = func() { set(map[string]string{"X": "1", "Y": "new", "PW": "pw-plain"}, "PW") }
	m.armed.Store(true)
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"X":"2","Y":"old","PW":"pw-plain"}}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	vars, _ := e.store.Variables(ctx, clusters.LocalID)
	all := vars[allNodes]
	if all.Values["X"] != "2" || all.Values["Y"] != "new" || !automation.IsHidden(all.Values["PW"]) || len(all.Sensitive) != 1 {
		t.Fatalf("after the save: %+v", all)
	}
	// Ann sets Y, which changed since she read it: refused.
	m.on = func() { set(map[string]string{"X": "2", "Y": "newer", "PW": all.Values["PW"]}, "PW") }
	m.armed.Store(true)
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"X":"2","Y":"mine","PW":"`+all.Values["PW"]+`"},"sensitive":["PW"]}`); code != http.StatusConflict || !strings.Contains(body, "Y differ") {
		t.Fatalf("%d %s", code, body)
	}
	if vars, _ := e.store.Variables(ctx, clusters.LocalID); vars[allNodes].Values["Y"] != "newer" {
		t.Fatalf("Y is %q", vars[allNodes].Values["Y"])
	}
}

// On production a change Sherlock did not weigh needs approval (finding
// 15; a change of behaviour): the console's rule alone rates ZONE passed
// through, and with Sherlock off it waits for a second admin all the same.
// Off production it is applied, as before.
func TestOnProductionAChangeSherlockDidNotWeighWaits(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvProduction)
	approved(t, e.store, "reads", valuesScript)
	if err := e.store.SetVariables(context.Background(), clusters.LocalID, allNodes, automation.FleetVariables{Values: map[string]string{"ZONE": "eu"}}); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"ZONE":"asia"}}`)
	if code != http.StatusAccepted || !strings.Contains(body, "Sherlock did not weigh it") {
		t.Fatalf("%d %s", code, body)
	}
	if vars, _ := e.store.Variables(context.Background(), clusters.LocalID); vars[allNodes].Values["ZONE"] != "eu" {
		t.Fatalf("ZONE is %q", vars[allNodes].Values["ZONE"])
	}
	other := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	approved(t, other.store, "reads", valuesScript)
	if code, body := other.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"ZONE":"asia"}}`); code != http.StatusOK {
		t.Fatalf("off production: %d %s", code, body)
	}
}

// A waiting change of the values a Kubernetes script reads holds that
// script on production (cut by the review's cap), as it holds a script on
// nodes: the cluster's values are All nodes'.
func TestAWaitingChangeHoldsAKubernetesScript(t *testing.T) {
	e := valuesSetup(t)
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"MODE":"wipe","ZONE":"eu","OTHER":"us"},"sensitive":["MODE"]}`); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{}`))
	if code != http.StatusConflict || !strings.Contains(body, "suspended") {
		t.Fatalf("%d %s", code, body)
	}
}
