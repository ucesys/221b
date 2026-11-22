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

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// A node's own value is the node's, whichever fleet's request set it: it
// is kept once and shown with All nodes, a run on the node gets it
// without a conflict, another of its fleets changes the same value, and a
// fleet cannot set the value of a node that is not its. A fleet saved
// without naming the node leaves the node's own value alone, which is
// what the Variables page does when it saves a fleet's own values.
func TestANodesOwnValueIsKeptOnceWhicheverFleetSetIt(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	st := automation.NewStore(&kv.Memory{}, nil)
	eng := automation.NewEngine(automation.Options{Store: st, Exec: noExec{}})
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv, c, m, auditOut, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Automation, o.AutomationStore, o.Settings = eng, st, shared
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite}, local, store); err != nil {
		t.Fatal(err)
	}
	e := autoEnv{srv: srv, c: c, store: st, engine: eng, audit: auditOut, local: local}

	read := func() automationFleets {
		t.Helper()
		code, body := e.do(t, "GET", "/api/v1/automation/fleets", "")
		var fl automationFleets
		if code != 200 || json.Unmarshal([]byte(body), &fl) != nil {
			t.Fatalf("fleets: %d %s", code, body)
		}
		return fl
	}
	all := read().Fleets[0]
	if all.ID != allNodes || len(all.Nodes) < 2 {
		t.Fatalf("All nodes: %+v", all)
	}
	a, b := all.Nodes[0].Name, all.Nodes[1].Name
	// Two fleets that share node b.
	if _, err := shared.UpdateFleets(context.Background(), clusters.LocalID, []fleets.Fleet{
		{ID: "left", Name: "Left", Kind: fleets.KindNodes, Nodes: []string{a, b}},
		{ID: "right", Name: "Right", Kind: fleets.KindNodes, Nodes: []string{b}},
	}); err != nil {
		t.Fatal(err)
	}
	fleet := func(fl automationFleets, id string) automationFleet {
		t.Helper()
		for _, f := range fl.Fleets {
			if f.ID == id {
				return f
			}
		}
		t.Fatalf("no fleet %s in %+v", id, fl.Fleets)
		return automationFleet{}
	}

	code, body := e.do(t, "PUT", "/api/v1/automation/fleets/left/variables", `{"values":{"MODE":"fleet"},"nodes":{"`+b+`":{"MODE":"own"}},"nodeReboot":{"`+b+`":30}}`)
	if code != 200 || !strings.Contains(body, `"own"`) {
		t.Fatalf("set from Left: %d %s", code, body)
	}
	fl := read()
	if got := fleet(fl, allNodes).Params; got.Nodes[b]["MODE"] != "own" || got.NodeReboot[b] != 30 {
		t.Fatalf("All nodes shows %+v", got)
	}
	for _, id := range []string{"left", "right"} {
		if got := fleet(fl, id).Params; len(got.Nodes) != 0 || len(got.NodeReboot) != 0 {
			t.Fatalf("%s shows a node's own: %+v", id, got)
		}
	}
	if len(fl.Conflicts) != 0 {
		t.Fatalf("conflicts: %+v", fl.Conflicts)
	}
	if v, source, conflict := automation.Inherited(fl.layersOf(b), b, "MODE"); v != "own" || source != automation.SourceNode || conflict != nil {
		t.Fatalf("%s gets %q from %q, conflict %v", b, v, source, conflict)
	}
	if v, source, _ := automation.Inherited(fl.layersOf(a), a, "MODE"); v != "fleet" || source != automation.SourceFleet {
		t.Fatalf("%s gets %q from %q", a, v, source)
	}

	// Right changes the same value; it is not a second one.
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/right/variables", `{"nodes":{"`+b+`":{"MODE":"changed"}}}`); code != 200 {
		t.Fatalf("set from Right: %d %s", code, body)
	}
	fl = read()
	if got := fleet(fl, allNodes).Params; got.Nodes[b]["MODE"] != "changed" || got.NodeReboot[b] != 0 {
		t.Fatalf("All nodes after Right changed it: %+v", got)
	}
	if got := fleet(fl, "left").Params; got.Values["MODE"] != "fleet" {
		t.Fatalf("Left's own: %+v", got)
	}
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/right/variables", `{"nodes":{"`+a+`":{"MODE":"x"}}}`); code != http.StatusBadRequest || !strings.Contains(body, "not in this fleet") {
		t.Fatalf("Right sets %s's: %d %s", a, code, body)
	}
	// Left's own values saved, as its page saves them: the node keeps its own.
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/left/variables", `{"values":{"MODE":"fleet2"}}`); code != 200 {
		t.Fatalf("Left's own saved: %d %s", code, body)
	}
	fl = read()
	if got := fleet(fl, allNodes).Params; got.Nodes[b]["MODE"] != "changed" || fleet(fl, "left").Params.Values["MODE"] != "fleet2" {
		t.Fatalf("after Left's own were saved: %+v", got)
	}
	// All nodes' page removes it.
	if code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{}`); code != 200 {
		t.Fatalf("removed from All nodes: %d %s", code, body)
	}
	if got := fleet(read(), allNodes).Params; len(got.Nodes) != 0 {
		t.Fatalf("All nodes after the removal: %+v", got)
	}
}

// Taking the sensitive mark off a variable waits for a second admin while
// a value stays under its name; removing the variable entirely, mark and
// values, shows nothing that was hidden and goes at once.
func TestRemovingASensitiveVariableEntirelyNeedsNoSecondAdmin(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	st := automation.NewStore(&kv.Memory{}, nil)
	eng := automation.NewEngine(automation.Options{Store: st, Exec: noExec{}})
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv, c, m, auditOut, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Automation, o.AutomationStore, o.Settings = eng, st, shared
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite}, local, store); err != nil {
		t.Fatal(err)
	}
	e := autoEnv{srv: srv, c: c, store: st, engine: eng, audit: auditOut, local: local}
	code, body := e.do(t, "GET", "/api/v1/automation/fleets", "")
	var fl automationFleets
	if code != 200 || json.Unmarshal([]byte(body), &fl) != nil || len(fl.Fleets[0].Nodes) < 2 {
		t.Fatalf("fleets: %d %s", code, body)
	}
	a, b := fl.Fleets[0].Nodes[0].Name, fl.Fleets[0].Nodes[1].Name
	if _, err := shared.UpdateFleets(context.Background(), clusters.LocalID, []fleets.Fleet{{ID: "left", Name: "Left", Kind: fleets.KindNodes, Nodes: []string{a, b}}}); err != nil {
		t.Fatal(err)
	}
	put := func(fleet, body string, want int) {
		t.Helper()
		if code, out := e.do(t, "PUT", "/api/v1/automation/fleets/"+fleet+"/variables", body); code != want {
			t.Fatalf("PUT %s %s: %d, want %d: %s", fleet, body, code, want, out)
		}
	}
	// All nodes: the mark alone waits, the variable whole goes.
	put(allNodes, `{"values":{"TOKEN":"s3cret","ZONE":"eu"},"sensitive":["TOKEN"]}`, http.StatusOK)
	put(allNodes, `{"values":{"TOKEN":"s3cret","ZONE":"eu"}}`, http.StatusAccepted)
	put(allNodes, `{"values":{"ZONE":"eu"}}`, http.StatusOK)
	all, err := st.Variables(context.Background(), clusters.LocalID)
	if _, kept := all[allNodes].Values["TOKEN"]; err != nil || kept || len(all[allNodes].Sensitive) != 0 {
		t.Fatalf("after the removal: %+v %v", all[allNodes], err)
	}
	// A fleet whose member has its own value under the name: the fleet's
	// value gone and the node's left alone is still a mark taken off a
	// value that stays; with the node's gone too, it is a removal.
	put("left", `{"values":{"KEY":"k1"},"nodes":{"`+b+`":{"KEY":"k2"}},"sensitive":["KEY"]}`, http.StatusOK)
	put("left", `{"values":{}}`, http.StatusAccepted)
	put("left", `{"values":{},"nodes":{"`+b+`":{}}}`, http.StatusOK)
}
