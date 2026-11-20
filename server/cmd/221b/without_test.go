package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// A start without the database (DB-67), tested on the whole console: serve
// is run as main runs it, against the simulated API server, which keeps
// the console's ConfigMaps and Secrets as a cluster does, and a PostgreSQL
// behind a relay the test can take away (dbtest.Away).

const (
	simNamespace = "221b"
	simRelease   = "k8s-console-221b"
	// The secrets the tests plant: none may be in a log line, the audit
	// log or the database.
	secretToken    = "tokenOfStagingZQ7"
	secretValues   = "hunter2ValuesZQ7"
	secretCred     = "credentialZQ7"
	adminPassword  = "correct horse battery staple ZQ7"
	adminLogin     = "ann@example.com"
	withoutLogLine = "this console starts WITHOUT it"
	stopsLogLine   = "the database answers: this console was started without it and stops now"
)

// write is one request that changes something, as the API server got it.
type write struct{ Method, Path, As string }

// simCluster is the simulated cluster a test's consoles run against.
type simCluster struct {
	url string
	cs  kubernetes.Interface
	cfg *rest.Config

	mu     sync.Mutex
	writes []write
}

// newSimCluster starts a simulated API server with the chart's ConfigMaps,
// holding in them what preload gives (ConfigMap name without the release's
// prefix to its keys).
func newSimCluster(t *testing.T, preload map[string]map[string]string) *simCluster {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 3, Pods: 12, SettingsConfigMap: simNamespace + "/" + simRelease,
		Preload: func(name string) (map[string]map[string]string, []sim.SeedObject) {
			out := map[string]map[string]string{}
			for suffix, data := range preload {
				out[name+suffix] = data
			}
			return out, nil
		}})
	c := &simCluster{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			c.mu.Lock()
			c.writes = append(c.writes, write{r.Method, r.URL.Path, r.Header.Get("Impersonate-User")})
			c.mu.Unlock()
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c.url = srv.URL
	c.cfg = &rest.Config{Host: srv.URL}
	cs, err := kubernetes.NewForConfig(c.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.cs = cs
	return c
}

// written are the writes the API server got since the last call.
func (c *simCluster) written() []write {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.writes
	c.writes = nil
	return out
}

// records are the console's ConfigMaps and Secrets as the API server has
// them: every one's keys and values and its resourceVersion.
func (c *simCluster) records(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	cms, err := c.cs.CoreV1().ConfigMaps(simNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cm := range cms.Items {
		keys := make([]string, 0, len(cm.Data))
		for k, v := range cm.Data {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		out["configmap/"+cm.Name] = "rv " + cm.ResourceVersion + " " + strings.Join(keys, "\x00")
	}
	secs, err := c.cs.CoreV1().Secrets(simNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range secs.Items {
		keys := make([]string, 0, len(sec.Data))
		for k, v := range sec.Data {
			keys = append(keys, k+"="+string(v))
		}
		sort.Strings(keys)
		out["secret/"+sec.Name] = "rv " + sec.ResourceVersion + " " + strings.Join(keys, "\x00")
	}
	return out
}

// changed names the records that differ between two readings, but those
// named in but.
func changed(before, after map[string]string, but ...string) []string {
	var out []string
	for name, v := range after {
		if old, ok := before[name]; (!ok || old != v) && !slices.Contains(but, name) {
			out = append(out, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok && !slices.Contains(but, name) {
			out = append(out, name+" (gone)")
		}
	}
	sort.Strings(out)
	return out
}

// beforeTheMove fills the cluster as a console from before Phase 13's
// second iteration left it, through that console's own stores: a
// connection, an application, two credentials, each whole in its Secret,
// and settings in their ConfigMaps (given to newSimCluster).
func (c *simCluster) beforeTheMove(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	labels := map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": simRelease, "app.kubernetes.io/managed-by": "221b"}
	conns, err := clusters.NewSecretStore(c.cfg, simNamespace, simRelease, labels)
	if err != nil {
		t.Fatal(err)
	}
	staging := clusters.Spec{ID: "staging", Name: "Staging", Env: "staging", Server: "https://127.0.0.1:1", CA: "system", Auth: "token", Mode: "read", Added: at, Updated: at}
	if err := conns.Save(ctx, staging, &clusters.Credential{Token: secretToken}); err != nil {
		t.Fatal(err)
	}
	as, err := apps.NewSecretStore(c.cfg, simNamespace, simRelease)
	if err != nil {
		t.Fatal(err)
	}
	shop := apps.Record{Cluster: "local", Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://github.com/ucesys/shop", Ref: "main", RefKind: apps.RefBranch},
		Values: "db:\n  password: " + secretValues + "\n", Params: []string{"image.tag=1"}, Created: at, Updated: at}
	if err := as.Save(ctx, shop, nil); err != nil {
		t.Fatal(err)
	}
	sk, err := kube.NewSecretKey(c.cfg, simNamespace, simRelease+"-credentials", labels)
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials.New(credentials.Options{Secrets: sk})
	for _, name := range []string{"GitHub", "Mirror"} {
		if _, err := creds.Create(ctx, credentials.Credential{Name: name, Kind: credentials.KindToken, Endpoint: "https://github.com/ucesys"}, credentials.Secret{Token: secretCred + name}, "ann"); err != nil {
			t.Fatal(err)
		}
	}
}

// console is one console started by serve.
type console struct {
	t      *testing.T
	url    string
	w      *wiring
	logs   *lockedBuffer
	audit  *lockedBuffer
	done   chan error
	cancel context.CancelFunc
}

// consoleOptions are how a test's console differs from the chart's.
type consoleOptions struct {
	database *db.Config
	// wait and probe shorten the two minutes a start waits for its
	// database, and the five seconds between asking for it afterwards.
	wait, probe time.Duration
	args        []string
}

// localUsers writes the chart's local accounts: one SuperAdmin.
func localUsers(t *testing.T) string {
	t.Helper()
	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(file, []byte(`[{"email":"`+adminLogin+`","name":"Ann","roles":["superAdmin"],"passwordHash":"`+hash+`"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// start runs serve against the cluster, as the chart starts the console,
// and returns once it listens or has stopped; a console that stopped
// before it listened has an empty url and its error in done.
func (c *simCluster) start(t *testing.T, o consoleOptions) *console {
	t.Helper()
	ref := simNamespace + "/" + simRelease
	args := []string{"-api-server", c.url, "-cluster-name", "simulated", "-listen", "127.0.0.1:0", "-log-format", "text", "-health-interval", "2s",
		"-auth-mode", "local", "-local-users", localUsers(t),
		"-settings-configmap", ref, "-connections-secrets", ref, "-audit-configmap", ref + "-audit", "-sessions-configmap", ref + "-sessions",
		"-automation-configmap", ref + "-automation", "-automation-runs-configmap", ref + "-automation-runs", "-ai-configmap", ref + "-ai",
		"-issues-configmap", ref + "-issues", "-runs-configmap", ref + "-runs", "-proposals-configmap", ref + "-proposals",
		"-knowledge-configmap", ref + "-knowledge", "-wiki-configmap", ref + "-wiki", "-casework-configmap", ref + "-casework"}
	if d := o.database; d != nil {
		args = append(args, "-db-host", d.Host, "-db-port", strconv.Itoa(d.Port), "-db-name", d.Name, "-db-user", d.User, "-db-sslmode", d.SSLMode, "-db-poll-interval", "200ms")
		if d.PasswordFile != "" {
			args = append(args, "-db-password-file", d.PasswordFile)
		}
	}
	args = append(args, o.args...)
	// A process runs one console, and its backup parts are registered
	// once; a test that starts another gives it a registry of its own.
	backup.Default = backup.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	con := &console{t: t, logs: &lockedBuffer{}, audit: &lockedBuffer{}, done: make(chan error, 1), cancel: cancel}
	serving := make(chan struct{})
	go func() {
		con.done <- serve(ctx, args, &hooks{logs: con.logs, audit: con.audit,
			wiring: func(w *wiring) { w.databaseWait, w.withoutProbe = o.wait, o.probe },
			serving: func(w *wiring, addr string) {
				con.w, con.url = w, "http://"+addr
				close(serving)
			}})
	}()
	t.Cleanup(func() { con.stop() })
	select {
	case <-serving:
	case err := <-con.done:
		con.done <- err
	case <-time.After(3 * time.Minute):
		t.Fatalf("the console neither listened nor stopped in three minutes; its log:\n%s", con.logs.String())
	}
	return con
}

// stop ends the console as SIGTERM does and returns what serve returned.
func (c *console) stop() error {
	c.cancel()
	return c.stopped(time.Minute)
}

// stopped waits for the console to have stopped, and returns what serve
// returned.
func (c *console) stopped(within time.Duration) error {
	c.t.Helper()
	select {
	case err := <-c.done:
		c.done <- err
		return err
	case <-time.After(within):
		c.t.Fatalf("the console did not stop in %s; its log:\n%s", within, c.logs.String())
		return nil
	}
}

// browser is a person's browser: it keeps the cookies.
func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	// A redirect is not followed: after a sign-in it leads to the browser
	// app, which a test build does not hold.
	return &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// signIn signs the SuperAdmin in through the sign-in page and returns the
// session cookie the browser was given.
func (c *console) signIn(b *http.Client) string {
	c.t.Helper()
	page, err := b.Get(c.url + "/signin")
	if err != nil {
		c.t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	m := regexp.MustCompile(`name="form" value="([^"]+)"`).FindSubmatch(html)
	if m == nil {
		c.t.Fatalf("no form token on the sign-in page: %s", page.Status)
	}
	req, _ := http.NewRequest(http.MethodPost, c.url+"/signin", strings.NewReader(url.Values{"email": {adminLogin}, "password": {adminPassword}, "next": {"/"}, "form": {string(m[1])}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := b.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		c.t.Fatalf("sign-in: %s; the console's log:\n%s", resp.Status, c.logs.String())
	}
	return c.cookie(b)
}

// cookie is the session cookie the browser holds for this console.
func (c *console) cookie(b *http.Client) string {
	u, _ := url.Parse(c.url)
	for _, ck := range b.Jar.Cookies(u) {
		if ck.Name == "221b_session" {
			return ck.Value
		}
	}
	return ""
}

// get asks the console for a path as the browser, and returns the status,
// the body and how long the answer took.
func (c *console) get(b *http.Client, path string) (int, string, time.Duration) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.url+path, nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := b.Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), time.Since(start)
}

// stream opens an event stream as the browser and returns what it sends
// in its first seconds.
func (c *console) stream(b *http.Client, path string) string {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url+path, nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := b.Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("GET %s: %s", path, resp.Status)
	}
	var out []byte
	buf := make([]byte, 64<<10)
	for len(out) < 4<<20 {
		n, err := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(out)
}

// tables are the database's tables with a checksum of each one's rows.
func tables(t *testing.T, cfg db.Config) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable", cfg.Host, cfg.Port, cfg.User, cfg.Name)
	if cfg.PasswordFile != "" {
		b, _ := os.ReadFile(cfg.PasswordFile)
		dsn += " password=" + strings.TrimSpace(string(b))
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range names {
		var n int
		var sum string
		q := `SELECT count(*), coalesce(md5(string_agg(r::text, E'\n' ORDER BY r::text)), '') FROM (SELECT row_to_json(x)::text AS r FROM ` + pgx.Identifier{name}.Sanitize() + ` x) y`
		if err := conn.QueryRow(ctx, q).Scan(&n, &sum); err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		out[name] = fmt.Sprintf("%d rows %s", n, sum)
	}
	return out
}

// awayDatabase is a database a test can take away. With a test PostgreSQL
// it is that, behind a relay, taken away already; without one it is an
// address nothing listens at, which is all a start without the database
// needs, and back is nil.
func awayDatabase(t *testing.T, hang bool) (cfg db.Config, real *db.Config, back func()) {
	t.Helper()
	if os.Getenv(dbtest.Env) == "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return db.Config{Host: "127.0.0.1", Port: port, Name: "221b", User: "app_221b", SSLMode: "disable"}, nil, nil
	}
	direct := dbtest.Config(t)
	away, relayed := dbtest.NewAway(t, direct)
	if hang {
		away.Hang()
	} else {
		away.Cut()
	}
	return relayed, &direct, away.Back
}

// shape is what the records hold, for comparing two clusters: each
// object's keys, with the ids the console made at random taken out.
func shape(records map[string]string) map[string]string {
	random := regexp.MustCompile(`secret-[0-9a-z-]+`)
	out := map[string]string{}
	for name, v := range records {
		_, v, _ = strings.Cut(v, " ")
		_, v, _ = strings.Cut(v, " ")
		var keys []string
		for _, kv := range strings.Split(v, "\x00") {
			k, _, _ := strings.Cut(kv, "=")
			keys = append(keys, random.ReplaceAllString(k, "secret-*"))
		}
		sort.Strings(keys)
		out[name] = strings.Join(keys, ",")
	}
	return out
}

// counts are the tables' row counts, but those of the tables named.
func counts(tables map[string]string, but ...string) map[string]string {
	out := map[string]string{}
	for name, v := range tables {
		if !slices.Contains(but, name) {
			out[name], _, _ = strings.Cut(v, " ")
		}
	}
	return out
}

// differ names the keys two maps differ at.
func differ(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if b[k] != v {
			out = append(out, fmt.Sprintf("%s: %q, %q", k, v, b[k]))
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			out = append(out, fmt.Sprintf("%s: none, %q", k, v))
		}
	}
	sort.Strings(out)
	return out
}

// sessionKey is the key that signs a session made while the session
// store is away, as its Secret holds it.
func (c *simCluster) sessionKey(t *testing.T) string {
	t.Helper()
	sec, err := c.cs.CoreV1().Secrets(simNamespace).Get(context.Background(), simRelease+auth.SessionKeySecretSuffix, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the session key's Secret: %v", err)
	}
	for _, v := range sec.Data {
		if len(v) > 0 {
			return string(v)
		}
	}
	t.Fatal("the session key's Secret holds no key")
	return ""
}

// noSecretIn fails the test when a console's log or audit log holds one of
// the values that must be in neither.
func noSecretIn(t *testing.T, con *console, secrets ...string) {
	t.Helper()
	for _, out := range []string{con.logs.String(), con.audit.String()} {
		for _, s := range append(secrets, secretToken, secretValues, secretCred, adminPassword) {
			if s != "" && strings.Contains(out, s) {
				t.Errorf("a log line holds a secret (%d characters, beginning %.4s)", len(s), s)
			}
		}
	}
}

// own are the two Secrets a console started without its database may
// write: the session key's, which it makes when there is none, and the
// local accounts', which records a sign-in.
var own = []string{"secret/" + simRelease + auth.SessionKeySecretSuffix, "secret/" + simRelease + "-users"}

// startedWithout checks a console that started while its database was
// away, and returns the signed-in browser: it listens, leads, says it was
// started without its database, signs a local account in with a signed
// session, serves the local cluster's live pages, answers 503 at once for
// what the database keeps, and has written none of its records.
func startedWithout(t *testing.T, c *simCluster, con *console, before map[string]string) *http.Client {
	t.Helper()
	if con.url == "" {
		t.Fatalf("the console did not start: %v\n%s", con.stopped(time.Second), con.logs.String())
	}
	w := con.w
	if !w.without || !w.database.StartedWithout() || w.keepsRecords() {
		t.Fatalf("the console did not start without its database: without %v", w.without)
	}
	if n := strings.Count(con.logs.String(), withoutLogLine); n != 1 {
		t.Errorf("the log says %d times that the console starts without its database, want once", n)
	}
	// Alone, it leads: the local cluster is read, and nothing is forwarded.
	if !w.elector.IsLeader() || !w.activity.Active() {
		t.Error("a console started without its database, alone, does not read its cluster")
	}
	// Nothing but the session key's Secret was made, and nothing changed.
	if got := changed(before, c.records(t), own[0]); len(got) > 0 {
		t.Errorf("the start changed the console's records: %v", got)
	}
	// What stands in for the settings that cannot be read is cautious.
	if st := w.shared.Current(); st.ChecksImages() || st.ShowsCosts() || !st.WatsonOrDefault().TwoPerson || w.cfg.LocalMode != "read" {
		t.Errorf("the settings in force allow what the saved ones may forbid: %+v, local mode %q", st.Settings, w.cfg.LocalMode)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _, _ := con.get(http.DefaultClient, "/readyz"); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the console is not ready; its log:\n%s", con.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _, _ := con.get(http.DefaultClient, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz answers %d", code)
	}
	b := browser()
	cookie := con.signIn(b)
	if !strings.HasPrefix(cookie, "s1.") {
		t.Fatalf("the session of a sign-in without the database is not a signed one (%d characters)", len(cookie))
	}
	if code, body, _ := con.get(b, "/api/v1/me"); code != http.StatusOK || !strings.Contains(body, adminLogin) || !strings.Contains(body, "superAdmin") {
		t.Errorf("/api/v1/me answers %d: %s", code, body)
	}
	// The database's page says why.
	if code, body, _ := con.get(b, "/api/v1/database"); code != http.StatusOK || !strings.Contains(body, `"configured":true`) || !strings.Contains(body, `"available":false`) || !strings.Contains(body, `"startedWithout":true`) {
		t.Errorf("/api/v1/database answers %d: %s", code, body)
	}
	// The local cluster, read live: alone in the menu, with its nodes and pods.
	if code, body, _ := con.get(b, "/api/v1/clusters"); code != http.StatusOK || !strings.Contains(body, `"id":"local"`) || strings.Contains(body, "staging") {
		t.Errorf("/api/v1/clusters answers %d: %s", code, body)
	}
	nodes, err := c.cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		t.Fatalf("the simulated cluster's nodes: %v", err)
	}
	// The store is filled at its next refresh after the cluster is listed.
	var code int
	var body string
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		code, body, _ = con.get(b, "/api/v1/clusters/local/snapshot")
		if (code == http.StatusOK && strings.Contains(body, nodes.Items[0].Name)) || time.Now().After(deadline) {
			break
		}
	}
	if code != http.StatusOK || !strings.Contains(body, `"nodes"`) || !strings.Contains(body, `"pods"`) {
		t.Errorf("the local cluster's snapshot answers %d: %.300s", code, body)
	}
	for _, n := range nodes.Items {
		if !strings.Contains(body, n.Name) {
			t.Errorf("the snapshot does not hold the node %s", n.Name)
		}
	}
	if code, body, _ := con.get(b, "/api/v1/clusters/local/nodes/"+nodes.Items[0].Name); code != http.StatusOK || !strings.Contains(body, nodes.Items[0].Name) {
		t.Errorf("a node's page answers %d: %.300s", code, body)
	}
	// The pages the browser app opens with: the home page and the stream
	// the Nodes and Pods tabs are drawn from.
	if code, body, _ := con.get(b, "/api/v1/home"); code != http.StatusOK {
		t.Errorf("the home page's data answers %d: %.300s", code, body)
	}
	if first := con.stream(b, "/api/v1/events"); !strings.Contains(first, nodes.Items[0].Name) {
		t.Errorf("the local cluster's stream does not open with its nodes: %.300s", first)
	}
	// What the database keeps says so, at once, also when the database
	// swallows what it is sent: nothing is sent to it.
	for _, path := range []string{"/api/v1/credentials", "/api/v1/clusters/local/apps", "/api/v1/jobs"} {
		code, body, took := con.get(b, path)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, db.ErrUnavailable.Error()) || !strings.Contains(body, "started without its database") {
			t.Errorf("%s answers %d: %s", path, code, body)
		}
		if took > 3*time.Second {
			t.Errorf("%s took %s to say the database is unavailable", path, took)
		}
	}
	// A change is refused the same way, and changes nothing.
	req, _ := http.NewRequest(http.MethodPut, con.url+"/api/v1/settings", strings.NewReader(`{"refreshSeconds":9}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if resp, err := b.Do(req); err != nil {
		t.Error(err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("saving the settings answers %s: %s", resp.Status, body)
		}
	}
	// Signing in wrote the accounts' Secret, as it always does, and no
	// other record: everything else is byte for byte what it was.
	if got := changed(before, c.records(t), own...); len(got) > 0 {
		t.Errorf("the console changed its records while it ran without its database: %v", got)
	}
	// Every write the API server got, as the console itself, was one of
	// those two Secrets.
	for _, wr := range c.written() {
		secret := strings.HasPrefix(wr.Path, "/api/v1/namespaces/"+simNamespace+"/secrets")
		if strings.Contains(wr.Path, "/configmaps") || (strings.Contains(wr.Path, "/secrets") && !secret) {
			t.Errorf("the console wrote %s %s (as %q)", wr.Method, wr.Path, wr.As)
		}
		if secret && wr.Method != http.MethodPost && !strings.HasSuffix(wr.Path, "-users") && !strings.HasSuffix(wr.Path, auth.SessionKeySecretSuffix) {
			t.Errorf("the console wrote %s %s (as %q)", wr.Method, wr.Path, wr.As)
		}
	}
	// The sign-in is in the audit stream, as always.
	if !strings.Contains(con.audit.String(), adminLogin) {
		t.Error("the audit stream does not hold the sign-in")
	}
	noSecretIn(t, con, cookie, c.sessionKey(t))
	return b
}

// A console whose database does not answer starts without it (DB-67), with
// the database refusing connections and with it swallowing what it is
// sent: it listens, a local account signs in, the local cluster is read
// live, what the database keeps says it is unavailable, and not one of the
// record sets still to be copied is touched. This half needs no test
// database: an address nothing listens at is a database that is away.
func TestAConsoleStartsWithoutItsDatabase(t *testing.T) {
	for _, hang := range []bool{false, true} {
		name := "the database refuses connections"
		if hang {
			name = "the database swallows what it is sent"
			if os.Getenv(dbtest.Env) == "" {
				continue
			}
		}
		t.Run(name, func(t *testing.T) {
			cfg, _, _ := awayDatabase(t, hang)
			c := newSimCluster(t, map[string]map[string]string{"": {"setup.json": `{"dismissed":["backup"]}`}})
			c.beforeTheMove(t)
			before := c.records(t)
			c.written()
			began := time.Now()
			con := c.start(t, consoleOptions{database: &cfg, wait: 2 * time.Second, probe: 200 * time.Millisecond})
			if took := time.Since(began); took > 30*time.Second {
				t.Errorf("the start took %s with a wait of two seconds", took)
			}
			startedWithout(t, c, con, before)
			// It goes on until it is stopped, and stops cleanly.
			select {
			case err := <-con.done:
				t.Fatalf("the console stopped by itself while its database was away: %v", err)
			case <-time.After(time.Second):
			}
			if err := con.stop(); err != nil {
				t.Errorf("stopping: %v", err)
			}
			if strings.Contains(con.logs.String(), stopsLogLine) {
				t.Error("the log says the database answered")
			}
			noSecretIn(t, con)
		})
	}
}

// The whole of it, over a database that comes back (DB-67): the console
// starts without its database and touches nothing; when the database
// answers it stops itself, once, saying so, and leaves the database as
// empty as it found it; the start that follows migrates and copies as a
// first start does, to the same rows and the same Secrets as a console
// that never started without its database; and the person who signed in
// meanwhile is still signed in, with a stored session from then on.
func TestAConsoleStartedWithoutItsDatabaseStopsWhenItAnswers(t *testing.T) {
	for _, hang := range []bool{false, true} {
		name := "the database refused connections"
		if hang {
			name = "the database swallowed what it was sent"
		}
		t.Run(name, func(t *testing.T) {
			cfg, direct, back := awayDatabase(t, hang)
			if direct == nil {
				t.Skipf("no PostgreSQL to test against: set %s=host:port", dbtest.Env)
			}
			preload := map[string]map[string]string{"": {"setup.json": `{"dismissed":["backup"]}`}}
			c := newSimCluster(t, preload)
			c.beforeTheMove(t)
			before := c.records(t)
			c.written()
			con := c.start(t, consoleOptions{database: &cfg, wait: 2 * time.Second, probe: 200 * time.Millisecond})
			b := startedWithout(t, c, con, before)
			signed := con.cookie(b)
			key := c.sessionKey(t)

			// The database answers: the console stops itself, as it does
			// when asked to, once, and says why.
			back()
			if err := con.stopped(time.Minute); err != nil {
				t.Fatalf("the console stopped with an error, and the kubelet would count a failure: %v", err)
			}
			log := con.logs.String()
			if n := strings.Count(log, stopsLogLine); n != 1 {
				t.Errorf("the log says %d times that the console stops for its database, want once:\n%s", n, log)
			}
			if !strings.Contains(log, "shutting down") {
				t.Error("the console did not stop the way it does when asked to")
			}
			noSecretIn(t, con, signed, key)
			// It made no table, no row, and changed no record of its own.
			if got := tables(t, *direct); len(got) > 0 {
				t.Errorf("a console started without its database left tables in it: %v", got)
			}
			if got := changed(before, c.records(t), own...); len(got) > 0 {
				t.Errorf("the console changed its records: %v", got)
			}

			// The start that follows is a first start: the schema's steps,
			// the copies and the removals, each once.
			again := c.start(t, consoleOptions{database: &cfg})
			if again.url == "" {
				t.Fatalf("the console did not start with its database: %v\n%s", again.stopped(time.Second), again.logs.String())
			}
			if again.w.without || again.w.database.StartedWithout() || !again.w.database.Health().Available {
				t.Fatal("the start that followed is not a start with the database")
			}
			log = again.logs.String()
			for _, line := range []string{
				`msg="copied a record set from its Secret into the database; the secrets stay in the Secret" component=database records="the cluster connections" count=1`,
				`msg="copied a record set from its Secret into the database; the secrets stay in the Secret" component=database records="the applications" count=1`,
				`msg="copied a record set from its Secret into the database; the secrets stay in the Secret" component=database records="the credentials' list" count=2`,
				`msg="copied settings from their ConfigMap into the database" component=database keys=[setup.json]`,
			} {
				if n := strings.Count(log, line); n != 1 {
					t.Errorf("the log has %q %d times, want once:\n%s", line, n, log)
				}
			}
			// The signed session made meanwhile is taken, and exchanged for
			// a stored one: the person does not sign in again.
			code, body, _ := again.get(b, "/api/v1/me")
			if code != http.StatusOK || !strings.Contains(body, adminLogin) {
				t.Fatalf("the session signed while the database was away is refused after the restart: %d %s", code, body)
			}
			stored := again.cookie(b)
			if stored == "" || stored == signed || strings.HasPrefix(stored, "s1.") {
				t.Errorf("the signed session was not exchanged for a stored one (%d characters)", len(stored))
			}
			if code, _, _ := again.get(b, "/api/v1/me"); code != http.StatusOK {
				t.Errorf("the stored session is refused: %d", code)
			}
			if code, body, _ := again.get(b, "/api/v1/credentials"); code != http.StatusOK || !strings.Contains(body, "GitHub") || !strings.Contains(body, "Mirror") {
				t.Errorf("the credentials after the copy: %d %s", code, body)
			}
			if code, body, _ := again.get(b, "/api/v1/clusters"); code != http.StatusOK || !strings.Contains(body, `"id":"staging"`) {
				t.Errorf("the connections after the copy: %d %s", code, body)
			}
			settled(t, c, *direct)
			if err := again.stop(); err != nil {
				t.Errorf("stopping: %v", err)
			}
			noSecretIn(t, again, signed, stored, key)

			// The same cluster, on a console that never started without
			// its database: its first start gives the same Secrets and
			// ConfigMaps and the same number of rows in every table.
			control := newSimCluster(t, preload)
			control.beforeTheMove(t)
			ccfg := dbtest.Config(t)
			first := control.start(t, consoleOptions{database: &ccfg})
			if first.url == "" {
				t.Fatalf("the control did not start: %v\n%s", first.stopped(time.Second), first.logs.String())
			}
			cb := browser()
			first.signIn(cb)
			if code, _, _ := first.get(cb, "/api/v1/me"); code != http.StatusOK {
				t.Fatalf("the control's sign-in: %d", code)
			}
			settled(t, control, ccfg)
			if err := first.stop(); err != nil {
				t.Errorf("stopping the control: %v", err)
			}
			if got := differ(shape(control.records(t)), shape(c.records(t))); len(got) > 0 {
				t.Errorf("the records after a start without the database and the start that followed differ from a first start's (first start, this one): %v", got)
			}
			// The audit log differs by what was done; a session is a row in
			// both. The other tables hold the same number of rows.
			want, got := counts(tables(t, ccfg), "audit_events"), counts(tables(t, *direct), "audit_events")
			if d := differ(want, got); len(d) > 0 {
				t.Errorf("the tables differ from a first start's (first start, this one): %v", d)
			}
			if got["schema_migrations"] == "0" || got["clusters"] == "0" || got["credentials"] != "2" || got["applications"] != "1" || got["auth_sessions"] != "1" {
				t.Errorf("the rows after the copies: %v", got)
			}
			for _, secret := range []string{secretToken, secretValues, secretCred} {
				if held := holdingText(t, *direct, secret); len(held) > 0 {
					t.Errorf("a secret is in the database, in %v", held)
				}
			}
		})
	}
}

// settled waits for what a start with the database does once, in the
// background, a few seconds after it listens (the credentials' move, the
// fixes' import, the leader's first passes), so that what a test reads
// next is what the start left: the records and the tables' row counts are
// read until they have stood still for two seconds.
func settled(t *testing.T, c *simCluster, cfg db.Config) {
	t.Helper()
	read := func() string {
		return fmt.Sprint(c.records(t), counts(tables(t, cfg), "audit_events"))
	}
	last, since := read(), time.Now()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
		if now := read(); now != last {
			last, since = now, time.Now()
		} else if time.Since(since) > 3*time.Second {
			return
		}
	}
	t.Fatal("the console's records did not stand still in a minute")
}

// holdingText names the tables with a row that holds text in any column.
func holdingText(t *testing.T, cfg db.Config, text string) map[string]int {
	t.Helper()
	d := dbtest.OpenConfig(t, cfg)
	return dbtest.Holding(t, d, text)
}

// With a database that already holds the console's records, a start
// without it, its return and the start that follows leave every table as
// it was, the audit log aside: nothing is copied again, nothing is put
// back from a ConfigMap or a Secret, nothing is started over.
func TestAStartWithoutTheDatabaseLeavesItsRecordsAsTheyWere(t *testing.T) {
	direct := dbtest.Config(t)
	away, cfg := dbtest.NewAway(t, direct)
	c := newSimCluster(t, map[string]map[string]string{"": {"setup.json": `{"dismissed":["backup"]}`}})
	c.beforeTheMove(t)
	// The first start, with the database: everything is copied.
	first := c.start(t, consoleOptions{database: &cfg})
	if first.url == "" {
		t.Fatalf("the console did not start: %v\n%s", first.stopped(time.Second), first.logs.String())
	}
	b := browser()
	first.signIn(b)
	req, _ := http.NewRequest(http.MethodPut, first.url+"/api/v1/settings", strings.NewReader(`{"refreshSeconds":9}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := b.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the settings: %v %v", resp, err)
	}
	resp.Body.Close()
	settled(t, c, direct)
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}
	held, records := tables(t, direct), c.records(t)
	if held["credentials"] == "" || strings.HasPrefix(held["credentials"], "0 ") || strings.HasPrefix(held["imports"], "0 ") {
		t.Fatalf("the first start copied nothing: %v", held)
	}

	// The database goes away, and the console is started.
	away.Cut()
	con := c.start(t, consoleOptions{database: &cfg, wait: 2 * time.Second, probe: 200 * time.Millisecond})
	if con.url == "" || !con.w.without {
		t.Fatalf("the console did not start without its database: %v\n%s", con.stopped(time.Second), con.logs.String())
	}
	// It does not show the settings as saved ones, nor take a change.
	if got := con.w.shared.Current().Settings.RefreshSeconds; got == 9 {
		t.Error("a console started without its database read the saved settings")
	}
	time.Sleep(2 * time.Second)
	away.Back()
	if err := con.stopped(time.Minute); err != nil {
		t.Fatalf("the console stopped with an error: %v", err)
	}
	if got := differ(held, tables(t, direct)); len(got) > 0 {
		t.Errorf("a console started without its database changed the database (before, after): %v", got)
	}
	if got := changed(records, c.records(t), own...); len(got) > 0 {
		t.Errorf("a console started without its database changed its records: %v", got)
	}

	// The start that follows copies nothing and keeps what was saved.
	again := c.start(t, consoleOptions{database: &cfg})
	if again.url == "" || again.w.without {
		t.Fatalf("the console did not start with its database: %v\n%s", again.stopped(time.Second), again.logs.String())
	}
	if got := again.w.shared.Current().Settings.RefreshSeconds; got != 9 {
		t.Errorf("the saved settings read %d seconds after the restart, want 9", got)
	}
	if log := again.logs.String(); strings.Contains(log, "copied a record set") || strings.Contains(log, "copied settings") || strings.Contains(log, "copied records") {
		t.Errorf("the start that followed copied something again:\n%s", log)
	}
	settled(t, c, direct)
	if err := again.stop(); err != nil {
		t.Error(err)
	}
	if got := differ(counts(held, "audit_events"), counts(tables(t, direct), "audit_events")); len(got) > 0 {
		t.Errorf("the tables' rows after the start that followed (before, after): %v", got)
	}
	after := tables(t, direct)
	for _, table := range unchangedByAStart {
		if held[table] != after[table] {
			t.Errorf("%s changed over a start without the database and the start that followed: %s, then %s", table, held[table], after[table])
		}
	}
	if got := changed(records, c.records(t), own...); len(got) > 0 {
		t.Errorf("the start that followed changed the console's records: %v", got)
	}
}

// unchangedByAStart are tables whose rows a start never changes: the
// record of what was copied and applied, and the record sets the copies
// made.
var unchangedByAStart = []string{"imports", "schema_migrations", "clusters", "credentials", "applications", "application_commands", "settings", "repositories"}

// With two replicas, one started with the database and one without, the
// one with it is unaffected: it goes on, keeps its records, and does not
// stop when the other does; the one without stops alone, once.
func TestAReplicaStartedWithItsDatabaseIsUnaffected(t *testing.T) {
	direct := dbtest.Config(t)
	away, relayed := dbtest.NewAway(t, direct)
	c := newSimCluster(t, nil)
	c.beforeTheMove(t)
	// The first replica reaches the database directly; the second through
	// a relay that is cut: the database is away for that one alone.
	with := c.start(t, consoleOptions{database: &direct})
	if with.url == "" || with.w.without {
		t.Fatalf("the first replica did not start: %v\n%s", with.stopped(time.Second), with.logs.String())
	}
	b := browser()
	with.signIn(b)
	settled(t, c, direct)
	held, records := counts(tables(t, direct), "audit_events"), c.records(t)
	away.Cut()
	without := c.start(t, consoleOptions{database: &relayed, wait: 2 * time.Second, probe: 200 * time.Millisecond})
	if without.url == "" || !without.w.without {
		t.Fatalf("the second replica did not start without its database: %v\n%s", without.stopped(time.Second), without.logs.String())
	}
	// The one with its database serves what the database keeps; the one
	// without says it is unavailable; neither changed a record.
	if code, body, _ := with.get(b, "/api/v1/credentials"); code != http.StatusOK || !strings.Contains(body, "GitHub") {
		t.Errorf("the replica with its database: %d %s", code, body)
	}
	// A session the database keeps is not known to the replica without it:
	// the person signs in there, and has a signed one.
	if code, _, _ := without.get(b, "/api/v1/credentials"); code != http.StatusUnauthorized {
		t.Errorf("the replica without its database answers %d to a session the database keeps", code)
	}
	other := browser()
	if cookie := without.signIn(other); !strings.HasPrefix(cookie, "s1.") {
		t.Error("the session of a sign-in on the replica without its database is not a signed one")
	}
	if code, _, _ := without.get(other, "/api/v1/credentials"); code != http.StatusServiceUnavailable {
		t.Errorf("the replica without its database answers %d for the credentials", code)
	}
	if got := changed(records, c.records(t), own...); len(got) > 0 {
		t.Errorf("the records changed: %v", got)
	}
	away.Back()
	if err := without.stopped(time.Minute); err != nil {
		t.Fatalf("the replica without its database stopped with an error: %v", err)
	}
	if n := strings.Count(without.logs.String(), stopsLogLine); n != 1 {
		t.Errorf("the replica without its database said %d times that it stops, want once", n)
	}
	select {
	case err := <-with.done:
		t.Fatalf("the replica with its database stopped too: %v", err)
	case <-time.After(2 * time.Second):
	}
	if strings.Contains(with.logs.String(), stopsLogLine) || strings.Contains(with.logs.String(), withoutLogLine) {
		t.Error("the replica with its database logged a start without it")
	}
	if code, body, _ := with.get(b, "/api/v1/credentials"); code != http.StatusOK || !strings.Contains(body, "GitHub") {
		t.Errorf("the replica with its database, after the other stopped: %d %s", code, body)
	}
	if got := differ(held, counts(tables(t, direct), "audit_events")); len(got) > 0 {
		t.Errorf("the tables' rows changed while a replica ran without the database (before, after): %v", got)
	}
	if err := with.stop(); err != nil {
		t.Error(err)
	}
}

// -db-start-without=false is the start of before (DB-02): the console
// waits for its database, and does not start without it.
func TestTheStartWithoutTheDatabaseCanBeTurnedOff(t *testing.T) {
	cfg, _, _ := awayDatabase(t, false)
	c := newSimCluster(t, nil)
	c.beforeTheMove(t)
	before := c.records(t)
	began := time.Now()
	con := c.start(t, consoleOptions{database: &cfg, wait: 2 * time.Second, args: []string{"-db-start-without=false"}})
	if con.url != "" {
		t.Fatal("the console listened without its database, with -db-start-without=false")
	}
	err := con.stopped(time.Second)
	if err == nil || !strings.Contains(err.Error(), "did not answer in 2s of trying") || !strings.Contains(err.Error(), "-db-start-without is off") {
		t.Fatalf("the start ended with %v", err)
	}
	if took := time.Since(began); took < time.Second {
		t.Errorf("the start gave up after %s, without waiting for the database", took)
	}
	if strings.Contains(con.logs.String(), withoutLogLine) {
		t.Error("the log says the console starts without its database")
	}
	if got := changed(before, c.records(t)); len(got) > 0 {
		t.Errorf("the start changed the console's records: %v", got)
	}
}

// As itself, a console started without its database writes no ConfigMap
// and no Secret: the write is refused before it is sent. A read passes,
// and so does a write made as a person, and a write of anything else.
func TestAConsoleStartedWithoutItsDatabaseWritesNoRecord(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
	}))
	defer srv.Close()
	plain := &rest.Config{Host: srv.URL}
	guarded := writesNoRecords(plain)
	if guarded == plain || plain.WrapTransport != nil {
		t.Fatal("the configuration given was changed")
	}
	cs, err := kubernetes.NewForConfig(guarded)
	if err != nil {
		t.Fatal(err)
	}
	asAnn := rest.CopyConfig(guarded)
	asAnn.Impersonate = rest.ImpersonationConfig{UserName: "ann"}
	ann, err := kubernetes.NewForConfig(asAnn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	core := cs.CoreV1()
	refused := map[string]error{}
	_, refused["create a Secret"] = core.Secrets("221b").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.CreateOptions{})
	_, refused["update a Secret"] = core.Secrets("221b").Update(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.UpdateOptions{})
	_, refused["patch a Secret"] = core.Secrets("221b").Patch(ctx, "x", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{})
	refused["delete a Secret"] = core.Secrets("221b").Delete(ctx, "x", metav1.DeleteOptions{})
	refused["delete the Secrets"] = core.Secrets("221b").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	_, refused["create a ConfigMap"] = core.ConfigMaps("221b").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.CreateOptions{})
	_, refused["update a ConfigMap"] = core.ConfigMaps("other").Update(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.UpdateOptions{})
	_, refused["a dry run"] = core.ConfigMaps("221b").Update(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	refused["delete a ConfigMap"] = core.ConfigMaps("221b").Delete(ctx, "x", metav1.DeleteOptions{})
	for what, err := range refused {
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "started without its database") {
			t.Errorf("%s: %v", what, err)
		}
	}
	if len(got) > 0 {
		t.Fatalf("the API server was sent %v", got)
	}
	passed := map[string]error{}
	_, passed["read a Secret"] = core.Secrets("221b").Get(ctx, "x", metav1.GetOptions{})
	_, passed["list the ConfigMaps"] = core.ConfigMaps("221b").List(ctx, metav1.ListOptions{})
	_, passed["update a ConfigMap as a person"] = ann.CoreV1().ConfigMaps("web").Update(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.UpdateOptions{})
	_, passed["create a pod"] = core.Pods("221b").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.CreateOptions{})
	_, passed["update a Lease"] = cs.CoordinationV1().Leases("221b").Update(ctx, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.UpdateOptions{})
	for what, err := range passed {
		if err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	want := []string{"GET /api/v1/namespaces/221b/secrets/x", "GET /api/v1/namespaces/221b/configmaps", "PUT /api/v1/namespaces/web/configmaps/x", "POST /api/v1/namespaces/221b/pods", "PUT /apis/coordination.k8s.io/v1/namespaces/221b/leases/x"}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("the API server was sent %v, want %v", got, want)
	}
}
