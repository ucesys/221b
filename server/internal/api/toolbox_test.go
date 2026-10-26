package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/sim"
	tb "github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/tunnel"
	"github.com/ucesys/221b/server/internal/watson"
)

// toolboxSetup is the Watson test console with the toolbox.
func toolboxSetup(t *testing.T, who, env string) (*httptest.Server, *http.Client, *sim.Sim, *sessions.Manager) {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	var mgr *sessions.Manager
	srv, c, m, _, local, store := multiSetupWith(t, who, nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CheckEvery: 50 * time.Millisecond,
			TwoPerson: func(c watson.Cluster) bool {
				return c.Env == clusters.EnvProduction && o.Settings.Current().WatsonOrDefault().TwoPerson
			}})
		mgr = sessions.New(sessions.Options{Config: tb.Default(), Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Sessions = mgr
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: env, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	return srv, c, s, mgr
}

func requestSession(t *testing.T, srv *httptest.Server, c *http.Client, body string) (int, watsonItem, string) {
	t.Helper()
	r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/sessions", body)
	var it watsonItem
	_ = json.Unmarshal([]byte(b), &it)
	return r.StatusCode, it, b
}

func TestToolboxSaysWhatAPersonMayUse(t *testing.T) {
	srv, c, _, _ := toolboxSetup(t, "ops@corp.example", clusters.EnvStaging)
	r, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/toolbox?namespaces=web", "")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var v struct {
		Enabled    bool `json:"enabled"`
		Admin      bool `json:"admin"`
		Namespaces map[string]struct {
			Tools map[string]string `json:"tools"`
			Level string            `json:"level"`
		} `json:"namespaces"`
		IdleSeconds int      `json:"idleSeconds"`
		Images      []any    `json:"images"`
		NodeProf    []string `json:"nodeProfiles"`
	}
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Enabled || !v.Admin || v.IdleSeconds != 600 || len(v.Images) == 0 || len(v.NodeProf) != 3 {
		t.Fatalf("%s", b)
	}
	if _, ok := v.Namespaces["web"]; !ok {
		t.Fatalf("no tools for web: %s", b)
	}
}

func TestSessionRequestsAreCheckedBeforeTheCard(t *testing.T) {
	srv, c, _, _ := toolboxSetup(t, "viewer@corp.example", clusters.EnvStaging)
	for body, want := range map[string]int{
		`{"tool":"shell","namespace":"web","pod":"cache-0","extra":1}`:      400,
		`{"tool":"forward","namespace":"web","pod":"cache-0","port":99999}`: 400,
		`{"tool":"node-shell","node":"worker-001","profile":"general"}`:     403,
	} {
		if code, _, b := requestSession(t, srv, c, body); code != want {
			t.Errorf("%s: %d %s, want %d", body, code, b, want)
		}
	}
}

func TestForwardOpensASessionTheCLIReaches(t *testing.T) {
	srv, c, _, mgr := toolboxSetup(t, "ops@corp.example", clusters.EnvStaging)
	// The person's own dialog is the approval: the session opens at once.
	r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/sessions", `{"tool":"forward","namespace":"web","pod":"cache-0","port":6379,"from":"Tools menu"}`)
	var opened struct {
		Opened struct {
			ID   string `json:"id"`
			Mine bool   `json:"mine"`
		} `json:"opened"`
	}
	_ = json.Unmarshal([]byte(b), &opened)
	if r.StatusCode != http.StatusCreated || opened.Opened.ID == "" || !opened.Opened.Mine {
		t.Fatalf("%d %s", r.StatusCode, b)
	}

	var list []struct {
		ID    string `json:"id"`
		State string `json:"state"`
		CLI   string `json:"cli"`
		Mine  bool   `json:"mine"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/sessions", "")
		_ = json.Unmarshal([]byte(b), &list)
		if len(list) == 1 && list[0].State == sessions.StateLive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions %s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	ss := list[0]
	if !ss.Mine || !strings.Contains(ss.CLI, "221b forward") || !strings.Contains(ss.CLI, "--session "+ss.ID) {
		t.Fatalf("session %+v", ss)
	}
	token := ss.CLI[strings.Index(ss.CLI, "--token ")+len("--token "):]
	token, _, _ = strings.Cut(token, " ")

	u, _ := url.Parse(srv.URL)
	u.Scheme = "ws"
	u.Path = "/api/v1/sessions/" + ss.ID + "/tunnel"
	dial := func(tok string) *websocket.Conn {
		// The CLI has no cookie and no Origin: the token is its sign-in.
		conn, resp, err := websocket.DefaultDialer.Dial(u.String(), http.Header{"Authorization": {"Bearer " + tok}})
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("dial: %v (%d)", err, code)
		}
		return conn
	}
	readFrame := func(conn *websocket.Conn) tunnel.Frame {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, b, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		f, err := tunnel.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	hello, _ := json.Marshal(tunnel.HelloBody{Version: "0.8.0", Host: "laptop", Listen: "127.0.0.1:6379"})

	bad := dial("not-the-token")
	bad.WriteMessage(websocket.BinaryMessage, tunnel.Encode(tunnel.Frame{Type: tunnel.Hello, Payload: hello}))
	if f := readFrame(bad); f.Type != tunnel.End || !strings.Contains(string(f.Payload), "token") {
		t.Fatalf("wrong token: %d %q", f.Type, f.Payload)
	}
	bad.Close()

	good := dial(token)
	defer good.Close()
	good.WriteMessage(websocket.BinaryMessage, tunnel.Encode(tunnel.Frame{Type: tunnel.Hello, Payload: hello}))
	// The simulator has no port forward: the console says why and the
	// CLI is told, rather than left waiting.
	f := readFrame(good)
	if f.Type == tunnel.Info {
		f = readFrame(good)
	}
	if f.Type != tunnel.End && f.Type != tunnel.Info {
		t.Fatalf("frame %d %q", f.Type, f.Payload)
	}

	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/sessions/"+ss.ID+"/end", `{}`); r.StatusCode != 200 {
		t.Fatalf("end %d %s", r.StatusCode, b)
	}
	if got, _ := mgr.Get(ss.ID); got.State != sessions.StateEnded || got.EndedBy != sessions.EndOwner {
		t.Fatalf("ended %s by %s", got.State, got.EndedBy)
	}
	_, b = send(t, c, http.MethodGet, srv.URL+"/api/v1/sessions?history=1", "")
	if !strings.Contains(b, ss.ID) {
		t.Fatalf("history %s", b)
	}
}

func TestASecondPersonsApprovalStillGoesToWatson(t *testing.T) {
	srv, c, _, mgr := toolboxSetup(t, "ops@corp.example", clusters.EnvProduction)
	if r, b := send(t, c, http.MethodPut, srv.URL+"/api/v1/watson/config", `{"twoPerson":true}`); r.StatusCode != 200 {
		t.Fatalf("two-person rule %d %s", r.StatusCode, b)
	}
	code, it, b := requestSession(t, srv, c, `{"tool":"forward","namespace":"web","pod":"cache-0","port":6379,"reason":"checking the cache keys"}`)
	if code != http.StatusAccepted || it.Kind != "change" || !strings.HasPrefix(it.Command, "session: ") || it.Status != watson.StatusPending {
		t.Fatalf("%d %s", code, b)
	}
	if list := mgr.List("local", sessions.Who{Admin: true}); len(list) != 0 {
		t.Fatalf("a session opened before its approval: %+v", list)
	}
}

func TestNodeShellsFollowTheClustersRule(t *testing.T) {
	// Off production a node shell opens at once, a reason optional.
	srv, c, _, _ := toolboxSetup(t, "ops@corp.example", clusters.EnvStaging)
	if code, _, b := requestSession(t, srv, c, `{"tool":"node-shell","node":"worker-001","profile":"general"}`); code != http.StatusCreated {
		t.Fatalf("staging: %d %s", code, b)
	}
	// On production it asks why, and with the two-person rule off opens.
	srv, c, _, _ = toolboxSetup(t, "ops@corp.example", clusters.EnvProduction)
	if code, _, b := requestSession(t, srv, c, `{"tool":"node-shell","node":"worker-001","profile":"general"}`); code != http.StatusBadRequest || !strings.Contains(b, "say why") {
		t.Fatalf("production without a reason: %d %s", code, b)
	}
	if code, _, b := requestSession(t, srv, c, `{"tool":"node-shell","node":"worker-001","profile":"general","reason":"kubelet restarts in a loop"}`); code != http.StatusCreated {
		t.Fatalf("production with a reason: %d %s", code, b)
	}
}

func TestToolActionsRunAsThePerson(t *testing.T) {
	srv, c, _, _ := toolboxSetup(t, "ops@corp.example", clusters.EnvStaging)
	for body, want := range map[string]int{
		`{"action":"reboot","node":"worker-001"}`:                      400,
		`{"action":"cordon","node":"worker-001; rm -rf /"}`:            400,
		`{"action":"cordon","node":"worker-001","pod":"x"}`:            400,
		`{"action":"delete-pod","namespace":"web","pod":"cache-0 -A"}`: 400,
		`{"action":"delete-pod","pod":"cache-0"}`:                      400,
		`{"action":"cordon","node":"worker-001","extra":1}`:            400,
	} {
		if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/actions", body); r.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", body, r.StatusCode, b, want)
		}
	}
	r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/actions", `{"action":"cordon","node":"worker-001"}`)
	var it watsonItem
	_ = json.Unmarshal([]byte(b), &it)
	if r.StatusCode != http.StatusAccepted || it.Command != "kubectl cordon worker-001" || it.Status == watson.StatusPending {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool {
		return m[it.ID].Status == watson.StatusDone || m[it.ID].Status == watson.StatusFailed
	})
}

func TestToolActionsWaitForASecondPersonOnProduction(t *testing.T) {
	srv, c, _, _ := toolboxSetup(t, "ops@corp.example", clusters.EnvProduction)
	if r, b := send(t, c, http.MethodPut, srv.URL+"/api/v1/watson/config", `{"twoPerson":true}`); r.StatusCode != 200 {
		t.Fatalf("two-person rule %d %s", r.StatusCode, b)
	}
	r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/actions", `{"action":"delete-pod","namespace":"web","pod":"cache-0"}`)
	var it watsonItem
	_ = json.Unmarshal([]byte(b), &it)
	if r.StatusCode != http.StatusAccepted || it.Command != "kubectl delete pod cache-0 -n web" || it.Status != watson.StatusPending {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
}

func TestTunnelPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/v1/sessions/abc/tunnel":               true,
		"/api/v1/clusters/c1/sessions/abc/tunnel":   true,
		"/api/v1/sessions/abc/attach":               false,
		"/api/v1/clusters/c1/sessions/abc/tunnel/x": false,
		"/api/v1/sessions/tunnel":                   false,
		"/other/api/v1/sessions/abc/tunnel":         false,
	} {
		if IsTunnelPath(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}
