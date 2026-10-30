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
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// watsonSetup is the test console with Watson, its local cluster a
// simulated one it can change.
func watsonSetup(t *testing.T, who string, env, mode string) (*httptest.Server, *http.Client, *sim.Sim, *bytes.Buffer, *clusters.Manager) {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	var svc *watson.Service
	srv, c, m, auditOut, local, store := multiSetupWith(t, who, nil, func(o *Options) {
		svc = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CheckEvery: 50 * time.Millisecond,
			TwoPerson: func(c watson.Cluster) bool {
				return c.Env == clusters.EnvProduction && o.Settings.Current().WatsonOrDefault().TwoPerson
			},
			ApproveAllMax: func(c watson.Cluster) time.Duration {
				return time.Duration(o.Settings.Current().WatsonOrDefault().ApproveAllMinutes) * time.Minute
			}})
		o.Watson = svc
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: env, Mode: mode, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	return srv, c, s, auditOut, m
}

// watsonStream reads the stream of the local cluster's commands until ok
// says the items are as wanted.
func watsonStream(t *testing.T, srv *httptest.Server, c *http.Client, ok func(map[string]watsonItem) bool) map[string]watsonItem {
	t.Helper()
	return watsonStreamAt(t, srv, c, "/api/v1/watson/events", "", ok)
}

// watsonStreamAt reads Watson's events from a stream, named with a prefix.
func watsonStreamAt(t *testing.T, srv *httptest.Server, c *http.Client, path, prefix string, ok func(map[string]watsonItem) bool) map[string]watsonItem {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	items := map[string]watsonItem{}
	buf := []byte{}
	tmp := make([]byte, 1<<15)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		for {
			i := bytes.Index(buf, []byte("\n\n"))
			if i < 0 {
				break
			}
			ev := string(buf[:i])
			buf = buf[i+2:]
			name, data := "", ""
			for _, l := range strings.Split(ev, "\n") {
				if v, ok := strings.CutPrefix(l, "event: "); ok {
					name = v
				}
				if v, ok := strings.CutPrefix(l, "data: "); ok {
					data = v
				}
			}
			name, mine := strings.CutPrefix(name, prefix)
			if !mine {
				continue
			}
			switch name {
			case "items":
				var list []watsonItem
				_ = json.Unmarshal([]byte(data), &list)
				for _, it := range list {
					items[it.ID] = it
				}
			case "item":
				var it watsonItem
				_ = json.Unmarshal([]byte(data), &it)
				prev := items[it.ID]
				lines := append(prev.Lines[:min(len(prev.Lines), it.LineStart)], it.Lines...)
				it.Lines = lines
				items[it.ID] = it
			}
		}
		if ok(items) {
			return items
		}
		if err != nil {
			t.Fatalf("stream ended: %v; items %+v", err, items)
		}
	}
}

func submit(t *testing.T, srv *httptest.Server, c *http.Client, command string, run bool) watsonItem {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"command": command, "run": run})
	r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands", string(body))
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("submit %d %s", r.StatusCode, b)
	}
	var out []watsonItem
	if err := json.Unmarshal([]byte(b), &out); err != nil || len(out) != 1 {
		t.Fatalf("submit answer %s", b)
	}
	return out[0]
}

func TestWatsonReadsAndApprovedChanges(t *testing.T) {
	srv, c, s, auditOut, _ := watsonSetup(t, "ops@corp.example", clusters.EnvStaging, clusters.ModeWrite)
	read := submit(t, srv, c, "kubectl get nodes", true)
	items := watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[read.ID].Status == watson.StatusDone })
	out := ""
	for _, l := range items[read.ID].Lines {
		out += l.T + "\n"
	}
	if !strings.Contains(out, "worker-001") {
		t.Fatalf("output %q", out)
	}
	ch := submit(t, srv, c, "kubectl cordon worker-001", true)
	if ch.Status != watson.StatusPending || ch.Kind != "change" {
		t.Fatalf("change %+v", ch)
	}
	// The page reads Watson on the stream it shares with Sherlock.
	watsonStreamAt(t, srv, c, "/api/v1/assistants/events", "watson-", func(m map[string]watsonItem) bool { return m[ch.ID].Plan != nil && m[ch.ID].Plan.Ready })
	if s.Node("worker-001").Spec.Unschedulable {
		t.Fatal("cordoned before approval")
	}
	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+ch.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[ch.ID].Status == watson.StatusDone })
	if !s.Node("worker-001").Spec.Unschedulable || s.LastImpersonated() != "221b-local:ops@corp.example" {
		t.Fatalf("cordoned %v as %q", s.Node("worker-001").Spec.Unschedulable, s.LastImpersonated())
	}
	log := auditOut.String()
	for _, a := range []string{`"action":"watson.command"`, `"action":"watson.approve"`, `"action":"watson.result"`} {
		if !strings.Contains(log, a) {
			t.Errorf("no %s in the audit log", a)
		}
	}
	if !strings.Contains(log, `"email":"ops@corp.example"`) || !strings.Contains(log, `"cluster":"local"`) {
		t.Errorf("audit lacks the person or cluster: %s", log)
	}
}

func TestWatsonReadOnlyClusterRefusesChanges(t *testing.T) {
	srv, c, s, auditOut, _ := watsonSetup(t, "ops@corp.example", clusters.EnvStaging, clusters.ModeRead)
	it := submit(t, srv, c, "kubectl cordon worker-002", true)
	if it.Status != watson.StatusRefused || it.Rule != "read-only cluster" || !strings.Contains(it.Reason, "read-only") {
		t.Fatalf("%+v", it)
	}
	if s.Node("worker-002").Spec.Unschedulable {
		t.Fatal("a read-only cluster was changed")
	}
	if !strings.Contains(auditOut.String(), `"action":"watson.refuse"`) {
		t.Fatal("refusal not audited")
	}
	// The installation decides, not an edit of the connection.
	if r, b := send(t, c, http.MethodPut, srv.URL+"/api/v1/clusters/local", `{"name":"Here","env":"staging","description":"","ca":"system","auth":"token","mode":"write"}`); r.StatusCode != 200 || !strings.Contains(b, `"mode":"read"`) {
		t.Fatalf("edit %d %s", r.StatusCode, b)
	}
	if it := submit(t, srv, c, "kubectl cordon worker-002", true); it.Status != watson.StatusRefused {
		t.Fatalf("an edit made the local cluster writable: %+v", it)
	}
	// One command, with its output, as its own JSON.
	read := submit(t, srv, c, "kubectl get node worker-002 -o name", true)
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[read.ID].Status == watson.StatusDone })
	r, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/watson/commands/"+read.ID, "")
	var got watsonItem
	if r.StatusCode != 200 || json.Unmarshal([]byte(b), &got) != nil || len(got.Lines) != 1 || got.Lines[0].T != "node/worker-002" {
		t.Fatalf("get %d %s", r.StatusCode, b)
	}
	other := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, other, http.MethodGet, srv.URL+"/api/v1/watson/commands/"+read.ID, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("someone else read the command: %d", r.StatusCode)
	}
}

func TestWatsonSecondPerson(t *testing.T) {
	srv, ops, s, _, _ := watsonSetup(t, "ops@corp.example", clusters.EnvProduction, clusters.ModeWrite)
	if r, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/watson/config", `{"twoPerson":true}`); r.StatusCode != 200 {
		t.Fatalf("config %d %s", r.StatusCode, b)
	}
	viewer := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, viewer, http.MethodPut, srv.URL+"/api/v1/watson/config", `{"twoPerson":false}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer changed the setting: %d", r.StatusCode)
	}
	it := submit(t, srv, ops, "kubectl cordon worker-003", true)
	items := watsonStream(t, srv, ops, func(m map[string]watsonItem) bool { return m[it.ID].Plan != nil && m[it.ID].Plan.Ready })
	if items[it.ID].CanAct || items[it.ID].Waiting == "" {
		t.Fatalf("the requester may approve: %+v", items[it.ID])
	}
	if r, b := send(t, ops, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+it.ID+"/approve", ""); r.StatusCode != http.StatusForbidden || !strings.Contains(b, "Someone other than") {
		t.Fatalf("self-approval: %d %s", r.StatusCode, b)
	}
	seen := watsonStream(t, srv, viewer, func(m map[string]watsonItem) bool { _, ok := m[it.ID]; return ok })
	if !seen[it.ID].CanAct {
		t.Fatalf("the second person may not approve: %+v", seen[it.ID])
	}
	if r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+it.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, viewer, func(m map[string]watsonItem) bool { return m[it.ID].Status == watson.StatusDone })
	if s.LastImpersonated() != "221b-local:viewer@corp.example" {
		t.Fatalf("ran as %q", s.LastImpersonated())
	}
}

func TestWatsonCommandsStayOnTheirCluster(t *testing.T) {
	// The other cluster closes last, after the console has stopped watching it.
	other := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 20, Seed: 7}))
	t.Cleanup(other.Close)
	srv, c, s, _, _ := watsonSetup(t, "ops@corp.example", clusters.EnvStaging, clusters.ModeWrite)
	tok := saToken(time.Now().Add(48 * time.Hour))
	resp, err := http.Post(other.URL+"/sim/token", "application/json", strings.NewReader(`{"token":"`+tok+`"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sim token: %v %v", err, resp)
	}
	resp.Body.Close()
	draft, _ := json.Marshal(map[string]any{"name": "prod-b", "env": "staging", "description": "", "server": other.URL, "ca": "system", "auth": "token", "mode": "write", "token": tok})
	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/clusters", string(draft)); r.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d %s", r.StatusCode, b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/clusters/prod-b/snapshot", "")
		if r.StatusCode == http.StatusOK && strings.Contains(b, `"synced":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prod-b snapshot: %d %.200s", r.StatusCode, b)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// A card planned on the local cluster is not approved, discarded,
	// stopped or edited through another cluster's URL: there it does not
	// exist.
	ch := submit(t, srv, c, "kubectl cordon worker-001", true)
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[ch.ID].Plan != nil && m[ch.ID].Plan.Ready })
	base := srv.URL + "/api/v1/clusters/prod-b/watson/commands/" + ch.ID
	for _, a := range []struct{ method, url, body string }{
		{http.MethodPost, base + "/approve", ""},
		{http.MethodPost, base + "/discard", ""},
		{http.MethodPost, base + "/stop", ""},
		{http.MethodPut, base, `{"command":"kubectl cordon worker-002"}`},
	} {
		if r, b := send(t, c, a.method, a.url, a.body); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: %d %s", a.method, a.url, r.StatusCode, b)
		}
	}
	if s.Node("worker-001").Spec.Unschedulable {
		t.Fatal("cordoned through another cluster's URL")
	}
	// Through its own cluster it still is.
	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/clusters/local/watson/commands/"+ch.ID+"/approve", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("approve on local: %d %s", r.StatusCode, b)
	}
}

func TestWatsonStrictComparesANewClusterAtOnce(t *testing.T) {
	// With rbac.enforce strict, a cluster not compared yet refuses changes
	// (RB-36); the first command compares it rather than wait for the round.
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	srv, c, m, _, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CheckEvery: 50 * time.Millisecond})
		o.Roles = RolesOptions{Prefixes: roles.DefaultPrefixes(), Enforce: "strict"}
	})
	for _, id := range roles.All {
		local.Put(clusterRoleOf(id.ClusterRole(), (roles.Templates{}).For(id, roles.Options{}).Rules))
	}
	local.Put(clusterRoleOf(roles.NodesClusterRole, roles.Nodes().Rules))
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvStaging, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	if it := submit(t, srv, c, "kubectl cordon worker-001", true); it.Status == watson.StatusRefused {
		t.Fatalf("a cluster whose roles match was refused: %+v", it)
	}
}
