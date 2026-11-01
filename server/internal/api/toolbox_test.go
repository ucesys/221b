package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodessh"
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

// sshStub says what a console with one older pairing would.
type sshStub struct{}

func (sshStub) Principal() string   { return "221b-superadmins-ns.console" }
func (sshStub) Fingerprint() string { return "SHA256:x" }
func (sshStub) List(context.Context, string) ([]nodessh.Pairing, error) {
	return []nodessh.Pairing{{Node: "worker-001", LoginUser: "ops", Port: 22, Principal: "221b-superadmins", CAFinger: "SHA256:x", HostKeys: []string{"ssh-ed25519 AAAAsecret"}}}, nil
}

// The page is told this console's principal, for new pairings, and what
// each pairing recorded, which its sessions use; never a host key.
func TestToolboxSaysThePrincipals(t *testing.T) {
	cfg := tb.Default()
	cfg.SSH.Enabled = true
	srv, c, m, _, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Sessions = sessions.New(sessions.Options{Config: cfg, Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.SSH = sshStub{}
	})
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvStaging, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, store); err != nil {
		t.Fatal(err)
	}
	r, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/toolbox?nodes=worker-001", "")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var v struct {
		SSH struct {
			Principal string `json:"principal"`
			Pairings  map[string]struct {
				Principal string `json:"principal"`
			} `json:"pairings"`
		} `json:"ssh"`
	}
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		t.Fatal(err)
	}
	if v.SSH.Principal != "221b-superadmins-ns.console" || v.SSH.Pairings["worker-001"].Principal != "221b-superadmins" || strings.Contains(b, "AAAAsecret") {
		t.Fatalf("%s", b)
	}
}

func TestSessionRequestsAreCheckedBeforeTheCard(t *testing.T) {
	srv, c, _, _ := toolboxSetup(t, "viewer@corp.example", clusters.EnvStaging)
	for body, want := range map[string]int{
		`{"tool":"shell","namespace":"web","pod":"cache-0","extra":1}`:      400,
		`{"tool":"forward","namespace":"web","pod":"cache-0","port":99999}`: 400,
		`{"tool":"node-shell","node":"worker-001","profile":"general"}`:     403,
		// A debug pod as a named ServiceAccount carries the account's
		// token: a SuperAdmin's choice (TB-28).
		`{"tool":"debug-pod","namespace":"web","image":"general","serviceAccount":"default"}`: 403,
	} {
		if code, _, b := requestSession(t, srv, c, body); code != want {
			t.Errorf("%s: %d %s, want %d", body, code, b, want)
		}
	}
}

func TestForwardOpensASessionTheCLIReaches(t *testing.T) {
	srv, c, _, mgr := toolboxSetup(t, "ops@corp.example", clusters.EnvStaging)
	// The dialog makes a card; the person approves it in Watson, and the
	// session opens as them (reversed at the October 28, 2026 review).
	r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/sessions", `{"tool":"forward","namespace":"web","pod":"cache-0","port":6379,"from":"Tools menu"}`)
	var card watsonItem
	_ = json.Unmarshal([]byte(b), &card)
	if r.StatusCode != http.StatusAccepted || card.Status != watson.StatusPending || card.ID == "" {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[card.ID].Plan != nil && m[card.ID].Plan.Ready })
	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+card.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool {
		return m[card.ID].Session != nil && m[card.ID].Session.SessionID != ""
	})
	_, b = send(t, c, http.MethodGet, srv.URL+"/api/v1/sessions", "")
	var mine []struct {
		ID   string `json:"id"`
		Mine bool   `json:"mine"`
	}
	_ = json.Unmarshal([]byte(b), &mine)
	if len(mine) != 1 || !mine[0].Mine {
		t.Fatalf("after the approval: %s", b)
	}
	var opened struct {
		Opened struct {
			ID   string `json:"id"`
			Mine bool   `json:"mine"`
		} `json:"opened"`
	}
	opened.Opened.ID, opened.Opened.Mine = mine[0].ID, mine[0].Mine

	var list []struct {
		ID    string `json:"id"`
		State string `json:"state"`
		CLI   string `json:"cli"`
		Token string `json:"token"`
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
	// The token is beside the command, never in it (TB-20): the command
	// asks for it, so it stays out of shell histories.
	token := ss.Token
	if token == "" || strings.Contains(ss.CLI, token) || strings.Contains(ss.CLI, "--token") {
		t.Fatalf("the token is on the command line: %+v", ss)
	}
	// Only the owner is given it.
	if _, b := send(t, signIn(t, srv, "ops2@corp.example"), http.MethodGet, srv.URL+"/api/v1/sessions/"+ss.ID, ""); strings.Contains(b, token) {
		t.Fatalf("another administrator was given the token: %s", b)
	}

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
	// The requester waits, and is told whom for.
	mine := watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[it.ID].Plan != nil && m[it.ID].Plan.Ready })
	if mine[it.ID].CanAct || !strings.Contains(mine[it.ID].Waiting, "SuperAdmin") {
		t.Fatalf("the requester may approve, or is not told whom to wait for: %+v", mine[it.ID])
	}
	// The session opens as the requester, with the requester's rights, so
	// only another SuperAdmin approves it (TB-07, TB-37): a viewer neither
	// sees the card nor approves it.
	viewer := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/watson/commands/"+it.ID, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a viewer sees the session card: %d", r.StatusCode)
	}
	if r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+it.ID+"/approve", ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a viewer approved a production session: %d %s", r.StatusCode, b)
	}
	if list := mgr.List("local", sessions.Who{Admin: true}); len(list) != 0 {
		t.Fatalf("a session opened on a viewer's approval: %+v", list)
	}
	admin2 := signIn(t, srv, "ops2@corp.example")
	seen := watsonStream(t, srv, admin2, func(m map[string]watsonItem) bool { _, ok := m[it.ID]; return ok })
	if !seen[it.ID].CanAct {
		t.Fatalf("the second SuperAdmin may not approve: %+v", seen[it.ID])
	}
	if r, b := send(t, admin2, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+it.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, admin2, func(m map[string]watsonItem) bool { return m[it.ID].Status == watson.StatusDone })
	list := mgr.List("local", sessions.Who{Admin: true})
	if len(list) != 1 || list[0].Owner.User != "ops@corp.example" || list[0].Approver != "Ops Two" {
		t.Fatalf("the session is not the requester's, approved by the second SuperAdmin: %+v", list)
	}
}

func TestNodeShellsFollowTheClustersRule(t *testing.T) {
	// Off production a node shell is a card for its own person to approve, a reason optional.
	srv, c, _, _ := toolboxSetup(t, "ops@corp.example", clusters.EnvStaging)
	if code, it, b := requestSession(t, srv, c, `{"tool":"node-shell","node":"worker-001","profile":"general"}`); code != http.StatusAccepted || it.Status != watson.StatusPending {
		t.Fatalf("staging: %d %s", code, b)
	}
	// On production it asks why, and with the two-person rule off makes a card.
	srv, c, _, _ = toolboxSetup(t, "ops@corp.example", clusters.EnvProduction)
	if code, _, b := requestSession(t, srv, c, `{"tool":"node-shell","node":"worker-001","profile":"general"}`); code != http.StatusBadRequest || !strings.Contains(b, "say why") {
		t.Fatalf("production without a reason: %d %s", code, b)
	}
	if code, it, b := requestSession(t, srv, c, `{"tool":"node-shell","node":"worker-001","profile":"general","reason":"kubelet restarts in a loop"}`); code != http.StatusAccepted || it.Status != watson.StatusPending {
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
	// A card, approved by the person who asked (reversed at the October 28, 2026 review).
	if r.StatusCode != http.StatusAccepted || it.Command != "kubectl cordon worker-001" || it.Status != watson.StatusPending {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	watsonStream(t, srv, c, func(m map[string]watsonItem) bool { return m[it.ID].Plan != nil && m[it.ID].Plan.Ready })
	if r, b := send(t, c, http.MethodPost, srv.URL+"/api/v1/watson/commands/"+it.ID+"/approve", ""); r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, b)
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

// A transcript outlives its session: kept for the retention, while the
// session leaves memory an hour after its end or at a restart. The route
// answers from the session's record, with the rule of the live route.
func TestATranscriptCanBeDownloadedAfterTheSessionLeftMemory(t *testing.T) {
	cfg := tb.Default()
	cfg.Transcripts, cfg.TranscriptDir = tb.TranscriptsPVC, t.TempDir()
	history := &sessions.MemoryHistory{}
	srv, ops, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Sessions = sessions.New(sessions.Options{Config: cfg, History: history, Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	})
	viewer := signIn(t, srv, "viewer@corp.example")
	cast := func(cluster, id string) {
		t.Helper()
		p := sessions.TranscriptPath(cfg.TranscriptDir, cluster, id)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{\"version\":2}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cast(clusters.LocalID, "s1")
	cast(clusters.LocalID, "past-the-cap")
	cast("staging", "elsewhere")
	history.Add(sessions.Record{ID: "s1", Cluster: clusters.LocalID, OwnerUser: "someone", Recorded: true})
	history.Add(sessions.Record{ID: "plain", Cluster: clusters.LocalID, OwnerUser: "someone"})
	history.Add(sessions.Record{ID: "elsewhere", Cluster: "staging", OwnerUser: "someone", Recorded: true})
	history.Add(sessions.Record{ID: "lost", Cluster: clusters.LocalID, OwnerUser: "someone", Recorded: true})

	path := func(id string) string { return srv.URL + "/api/v1/sessions/" + id + "/transcript" }
	r, body := send(t, ops, http.MethodGet, path("s1"), "")
	if r.StatusCode != http.StatusOK || !strings.Contains(body, `"version":2`) || r.Header.Get("Content-Type") != "application/x-asciicast" || !strings.Contains(r.Header.Get("Content-Disposition"), "session-s1.cast") {
		t.Fatalf("a SuperAdmin's download: %d %v %s", r.StatusCode, r.Header, body)
	}
	if r, body := send(t, viewer, http.MethodGet, path("s1"), ""); r.StatusCode != http.StatusNotFound || strings.Contains(body, "version") {
		t.Fatalf("someone else's transcript: %d %s", r.StatusCode, body)
	}
	if r, body := send(t, ops, http.MethodGet, path("plain"), ""); r.StatusCode != http.StatusNotFound || !strings.Contains(body, "No transcript") {
		t.Fatalf("a session never recorded: %d %s", r.StatusCode, body)
	}
	if r, body := send(t, ops, http.MethodGet, path("lost"), ""); r.StatusCode != http.StatusNotFound || !strings.Contains(body, "gone") {
		t.Fatalf("a transcript past its retention: %d %s", r.StatusCode, body)
	}
	if r, body := send(t, ops, http.MethodGet, path("elsewhere"), ""); r.StatusCode != http.StatusNotFound || strings.Contains(body, "version") {
		t.Fatalf("another cluster's session: %d %s", r.StatusCode, body)
	}
	// The records are capped and the files are not: a SuperAdmin still has
	// a transcript whose record is gone, and nobody else does.
	if r, _ := send(t, ops, http.MethodGet, path("past-the-cap"), ""); r.StatusCode != http.StatusOK {
		t.Fatalf("past the cap: %d", r.StatusCode)
	}
	if r, _ := send(t, viewer, http.MethodGet, path("past-the-cap"), ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("past the cap, not a SuperAdmin: %d", r.StatusCode)
	}
}

// failingBackend refuses what a session asks of it, so the session ends by
// itself without a cluster.
type failingBackend struct{ sessions.Backend }

func (failingBackend) CreatePod(context.Context, *corev1.Pod, bool) error {
	return errors.New("no cluster here")
}

// Node 2 of a card cannot start: the card still names the first, and says
// which did not open, not only the server's log.
func TestACardReportsTheSessionOfANodeThatCouldNotStart(t *testing.T) {
	calls := 0
	mgr := sessions.New(sessions.Options{Config: tb.Default(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Backend: func(sessions.Cluster, kube.Person) (sessions.Backend, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("the credential could not be read")
		}
		return failingBackend{}, nil
	}})
	s := &Server{opts: Options{Sessions: mgr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	it := watson.Item{ID: "card-1", Session: &watson.SessionCard{Request: sessions.Request{Tool: tb.ToolNodeShell, Node: "n1", Nodes: []string{"n2", "n3"}, Profile: tb.ProfileGeneral}}}
	id, err := s.startSession(watson.Cluster{ID: "c1"}, it, watson.Who{})
	if id == "" || err == nil || !strings.Contains(err.Error(), "n2") || !strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "n3") {
		t.Fatalf("%q %v", id, err)
	}
	if calls != 3 {
		t.Fatalf("%d of 3 started", calls)
	}
	// Without the first the card has nothing to name.
	calls = 1
	if id, err := s.startSession(watson.Cluster{ID: "c1"}, it, watson.Who{}); id != "" || err == nil {
		t.Fatalf("%q %v", id, err)
	}
}

// countingAuth counts what a terminal reports.
type countingAuth struct {
	Auth
	active int
}

func (a *countingAuth) Active(auth.Session) { a.active++ }

// Keystrokes count as sign-in activity, without reading the session for
// every one of them.
func TestTypingCountsAsActivityAboutOnceAMinute(t *testing.T) {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	a := &countingAuth{}
	s := &Server{opts: Options{Auth: a, Now: func() time.Time { return at }}}
	typed := s.typing(auth.Session{ID: "s"})
	for range 3 {
		typed()
		at = at.Add(time.Second)
	}
	if a.active != 1 {
		t.Fatalf("%d in 3 seconds", a.active)
	}
	at = at.Add(typingCounts)
	typed()
	if a.active != 2 {
		t.Fatalf("%d a minute later", a.active)
	}
}
