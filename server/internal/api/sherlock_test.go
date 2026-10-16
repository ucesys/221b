package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// sherlockSetup is the multi-cluster test console with Sherlock, its API
// key set and turned on for the local cluster, talking to a fake Claude.
func sherlockSetup(t *testing.T, who string, deny map[string]bool) (*httptest.Server, *http.Client, *fakeclaude.Fake, *bytes.Buffer, *source, *sherlock.Service) {
	t.Helper()
	fake := fakeclaude.New()
	fake.Delay = time.Millisecond
	api := httptest.NewServer(fake)
	t.Cleanup(api.Close)
	keys := sherlock.NewKeys(nil, "", nil)
	var svc *sherlock.Service
	srv, c, _, auditOut, local, _ := multiSetupWith(t, who, deny, func(o *Options) {
		svc = sherlock.New(sherlock.Options{
			Client: &sherlock.Client{BaseURL: api.URL, Key: keys.Key},
			Settings: func() sherlock.Settings {
				sh := o.Settings.Current().SherlockOrDefault()
				return sherlock.Settings{Model: sh.Model, Caps: sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}}
			},
			Secrets: func() []string { return []string{keys.Key()} },
			Audit:   o.Audit,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		o.Sherlock, o.SherlockKeys = svc, keys
	})
	t.Cleanup(svc.Wait)
	if err := keys.Set(context.Background(), "sk-ant-test-key-0123456789", "test"); err != nil {
		t.Fatal(err)
	}
	return srv, c, fake, auditOut, local, svc
}

func enableSherlock(t *testing.T, srv *httptest.Server, admin *http.Client, body string) {
	t.Helper()
	if r, b := send(t, admin, http.MethodPut, srv.URL+"/api/v1/sherlock/config", body); r.StatusCode != 200 {
		t.Fatalf("config %d %s", r.StatusCode, b)
	}
}

// conversation reads the first event of the stream: the conversation.
func conversation(t *testing.T, srv *httptest.Server, c *http.Client) sherlock.View {
	t.Helper()
	return conversationAt(t, srv, c, "/api/v1/sherlock/events", "conversation")
}

// conversationAt reads the conversation from a stream, by its event's name.
func conversationAt(t *testing.T, srv *httptest.Server, c *http.Client, path, event string) sherlock.View {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	buf := make([]byte, 0, 1<<16)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if i := bytes.Index(buf, []byte("event: "+event+"\ndata: ")); i >= 0 {
			rest := buf[i+len("event: "+event+"\ndata: "):]
			if j := bytes.Index(rest, []byte("\n\n")); j >= 0 {
				var v sherlock.View
				if err := json.Unmarshal(rest[:j], &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
		}
		if err != nil {
			t.Fatalf("no conversation in %q: %v", buf, err)
		}
	}
}

func askAndRead(t *testing.T, srv *httptest.Server, c *http.Client, svc *sherlock.Service, q string) *sherlock.Msg {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"question": q})
	r, text := send(t, c, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b))
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask %d %s", r.StatusCode, text)
	}
	svc.Wait()
	v := conversation(t, srv, c)
	if len(v.Messages) == 0 {
		t.Fatal("no messages")
	}
	return v.Messages[len(v.Messages)-1]
}

// The toolset reads and nothing else (P5-07). The names are listed here so
// that a new tool is a reviewed change; and the Cluster interface they
// reach has no method that writes, which the compiler holds them to.
func TestSherlockToolsOnlyRead(t *testing.T) {
	var names []string
	for _, tl := range sherlockTools {
		names = append(names, tl.def.Name)
		var schema map[string]any
		if err := json.Unmarshal(tl.def.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Errorf("%s: the input schema is not an object: %s", tl.def.Name, tl.def.InputSchema)
		}
		for _, w := range []string{"create", "update", "patch", "delete", "apply", "exec", "scale", "cordon", "drain_node", "evict", "restart", "rollback", "label", "annotate", "write", "set_"} {
			if strings.HasPrefix(tl.def.Name, w) {
				t.Errorf("%s looks like a tool that writes", tl.def.Name)
			}
		}
	}
	sort.Strings(names)
	want := []string{"check_connection", "cluster_overview", "get_helm_release", "get_node", "get_object", "get_pod", "get_service", "get_yaml", "list_access", "list_alerts", "list_events", "list_fleets", "list_helm_releases", "list_nodes", "list_objects", "list_pods", "list_policies", "list_services", "pod_logs", "top", "trace_ingress", "who_can"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("the toolset changed; review that each new tool only reads, then update this list:\n got %v\nwant %v", names, want)
	}
}

// A question streams an answer from the tools' results, cites only what
// the tools showed, and is audited with its tools and tokens (P5-04, P5-05).
func TestSherlockAnswersAndAudits(t *testing.T) {
	srv, ops, fake, auditOut, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	// Off: nothing is asked.
	b, _ := json.Marshal(map[string]string{"question": "What needs attention?"})
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b)); r.StatusCode != http.StatusConflict || !strings.Contains(text, "off for this cluster") {
		t.Fatalf("asked while off: %d %s", r.StatusCode, text)
	}
	if len(fake.Requests()) != 0 {
		t.Fatal("the Claude API was called while Sherlock was off")
	}
	enableSherlock(t, srv, ops, `{"clusters":{"local":true,"nowhere":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`)

	m := askAndRead(t, srv, ops, svc, "Tell me about pod web/shop")
	if m.Status != "done" || !strings.Contains(m.Text, "](221b://Pod/web/shop)") || !strings.Contains(m.Text, "Running") {
		t.Fatalf("answer: %+v", m)
	}
	if len(m.Steps) != 1 || m.Steps[0].Label != "Read pod web/shop" || m.Steps[0].Status != "done" {
		t.Fatalf("steps: %+v", m.Steps)
	}
	if len(m.Refs) == 0 || m.Refs[0] != (sherlock.Ref{Kind: "Pod", Namespace: "web", Name: "shop"}) {
		t.Fatalf("refs: %+v", m.Refs)
	}
	if m.FirstAt == nil || m.Usage == nil || m.Usage.OutputTokens == 0 || m.CostUSD <= 0 {
		t.Fatalf("timing and use: %+v", m)
	}
	// The page reads the same conversation on the stream it shares with Watson.
	if v := conversationAt(t, srv, ops, "/api/v1/assistants/events", "sherlock-conversation"); len(v.Messages) == 0 || v.Messages[len(v.Messages)-1].ID != m.ID {
		t.Fatalf("shared stream: %+v", v)
	}
	// The default question cites something no tool showed: it is not a link.
	m = askAndRead(t, srv, ops, svc, "What needs attention right now?")
	if strings.Contains(m.Text, "221b://Pod/nowhere/ghost") || !strings.Contains(m.Text, "with ghost.") {
		t.Fatalf("an unknown object stayed a link: %s", m.Text)
	}
	// Each question is audited, with its tools and tokens.
	var rec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(auditOut.String()), "\n") {
		if strings.Contains(line, `"sherlock.ask"`) {
			_ = json.Unmarshal([]byte(line), &rec)
		}
	}
	if rec == nil || rec["cluster"] != "local" || !strings.Contains(rec["detail"].(string), "tools [list_alerts]") || !strings.Contains(rec["detail"].(string), "tokens in") {
		t.Fatalf("audit: %v\n%s", rec, auditOut.String())
	}
	// Only clusters that exist were turned on.
	var cfg sherlockConfig
	_, text := send(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock/config", "")
	_ = json.Unmarshal([]byte(text), &cfg)
	if !cfg.Settings.Clusters["local"] || cfg.Settings.Clusters["nowhere"] {
		t.Fatalf("clusters: %v", cfg.Settings.Clusters)
	}
	// The key is never sent back.
	if strings.Contains(text, "sk-ant-test-key") || !cfg.Key.Set {
		t.Fatalf("config: %s", text)
	}
	// Nor to the model.
	for _, req := range fake.Requests() {
		if bytes.Contains(req, []byte("sk-ant-test-key")) {
			t.Fatal("the API key went into a request body")
		}
	}
}

// A person without access to a namespace learns nothing about it through
// Sherlock (P5-02): the tools read as them.
func TestSherlockReadsAsThePerson(t *testing.T) {
	deny := map[string]bool{"pods/kube-system": true, "events/kube-system": true}
	srv, viewer, fake, _, local, svc := sherlockSetup(t, "viewer@corp.example", deny)
	local.Put(&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "dns.1", Namespace: "kube-system"}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "dns", Namespace: "kube-system"}, Type: "Warning", Reason: "Hidden", Message: "kube-system-only detail"})
	admin := signIn(t, srv, "ops@corp.example")
	enableSherlock(t, srv, admin, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`)

	m := askAndRead(t, srv, viewer, svc, "Which pods are in namespace kube-system?")
	if m.Status != "done" {
		t.Fatalf("answer: %+v", m)
	}
	askAndRead(t, srv, viewer, svc, "Tell me about pod kube-system/dns")
	askAndRead(t, srv, viewer, svc, "show me the logs of kube-system/dns")
	for _, req := range fake.Requests() {
		for _, leak := range []string{"secret things", "kube-system-only detail", "uid-dns"} {
			if bytes.Contains(req, []byte(leak)) {
				t.Fatalf("%q reached the Claude API for a person who may not see it", leak)
			}
		}
	}
	// Once Kubernetes lets them, the same question reaches it.
	local.allow("pods", "kube-system")
	time.Sleep(500 * time.Millisecond)
	m = askAndRead(t, srv, viewer, svc, "show me the logs of kube-system/dns")
	if !strings.Contains(m.Text, "secret things") {
		t.Fatalf("the answer once allowed: %s", m.Text)
	}
}

// Reaching a cap stops new questions with a clear message (P5-05).
func TestSherlockCaps(t *testing.T) {
	srv, ops, _, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":0.001,"monthlyUSD":200}`)
	askAndRead(t, srv, ops, svc, "Tell me about pod web/shop")
	b, _ := json.Marshal(map[string]string{"question": "And now?"})
	r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b))
	if r.StatusCode != http.StatusTooManyRequests || !strings.Contains(text, "daily spending cap of $0.00") && !strings.Contains(text, "daily spending cap") {
		t.Fatalf("over the cap: %d %s", r.StatusCode, text)
	}
	_, text = send(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock", "")
	if !strings.Contains(text, `"capped":"Sherlock has reached the daily spending cap`) || !strings.Contains(text, `"available":false`) {
		t.Fatalf("status: %s", text)
	}
	// Raising the cap lets questions in again.
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":50,"monthlyUSD":200}`)
	if m := askAndRead(t, srv, ops, svc, "Tell me about pod web/shop"); m.Status != "done" {
		t.Fatalf("after raising the cap: %+v", m)
	}
}

// Only administrators change the settings and the key.
func TestSherlockSettingsAreAdmins(t *testing.T) {
	srv, viewer, _, _, _, _ := sherlockSetup(t, "viewer@corp.example", nil)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/sherlock/config", ""},
		{http.MethodPut, "/api/v1/sherlock/config", `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":1,"monthlyUSD":2}`},
		{http.MethodPut, "/api/v1/sherlock/key", `{"key":"sk-ant-another-0123456789"}`},
		{http.MethodDelete, "/api/v1/sherlock/key", ""},
	} {
		if r, text := send(t, viewer, c.method, srv.URL+c.path, c.body); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s by a viewer: %d %s", c.method, c.path, r.StatusCode, text)
		}
	}
	admin := signIn(t, srv, "ops@corp.example")
	if r, text := send(t, admin, http.MethodPut, srv.URL+"/api/v1/sherlock/key", `{"key":"short"}`); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, "does not look like") {
		t.Fatalf("a bad key: %d %s", r.StatusCode, text)
	}
	if r, text := send(t, admin, http.MethodPut, srv.URL+"/api/v1/sherlock/config", `{"model":"claude-opus-5-5","dailyUSD":30,"monthlyUSD":20}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a daily cap above the monthly: %d %s", r.StatusCode, text)
	}
}

// Stopping keeps what arrived, and the next question is answered.
func TestSherlockStop(t *testing.T) {
	srv, ops, _, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`)
	b, _ := json.Marshal(map[string]string{"question": "Answer slowly please"})
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b)); r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask %d %s", r.StatusCode, text)
	}
	time.Sleep(700 * time.Millisecond)
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b)); r.StatusCode != http.StatusConflict {
		t.Fatalf("a second question while busy: %d %s", r.StatusCode, text)
	}
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/stop", "{}"); r.StatusCode != 200 || !strings.Contains(text, "true") {
		t.Fatalf("stop %d %s", r.StatusCode, text)
	}
	svc.Wait()
	v := conversation(t, srv, ops)
	last := v.Messages[len(v.Messages)-1]
	if last.Status != "stopped" || !strings.Contains(last.Text, "Part 1") || strings.Contains(last.Text, "Part 50") {
		t.Fatalf("stopped answer: %+v", last)
	}
	if m := askAndRead(t, srv, ops, svc, "Tell me about pod web/shop"); m.Status != "done" {
		t.Fatalf("after a stop: %+v", m)
	}
}
