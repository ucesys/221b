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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/watson/events", nil)
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
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[ch.ID].Plan != nil && m[ch.ID].Plan.Ready })
	if s.Node("worker-001").Spec.Unschedulable {
		t.Fatal("cordoned before approval")
	}
	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+ch.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[ch.ID].Status == watson.StatusDone })
	if !s.Node("worker-001").Spec.Unschedulable || s.LastImpersonated() != "ops@corp.example" {
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
	if s.LastImpersonated() != "viewer@corp.example" {
		t.Fatalf("ran as %q", s.LastImpersonated())
	}
}
