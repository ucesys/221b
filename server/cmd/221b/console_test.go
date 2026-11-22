package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// The whole console under test: serve is run as main runs it, against the
// simulated API server, which keeps the console's ConfigMaps and Secrets
// as a cluster does, and a PostgreSQL behind a relay the test can take
// away (dbtest.Away). The tests of a running console that loses its
// database use it (away_test.go, text_test.go), and so do the tests here:
// a console never starts without its database (DB-02).

const (
	simNamespace = "221b"
	simRelease   = "k8s-console-221b"
	// The secrets the tests plant: none may be in a log line, the audit
	// log or the database.
	secretToken   = "tokenOfStagingZQ7"
	secretValues  = "hunter2ValuesZQ7"
	secretCred    = "credentialZQ7"
	adminPassword = "correct horse battery staple ZQ7"
	adminLogin    = "ann@example.com"
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
	return newSimClusterWith(t, preload, nil)
}

// newSimClusterWith is newSimCluster with ConfigMaps of its own beside the
// chart's.
func newSimClusterWith(t *testing.T, preload map[string]map[string]string, objects []sim.SeedObject) *simCluster {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 3, Pods: 12, SettingsConfigMap: simNamespace + "/" + simRelease,
		Preload: func(name string) (map[string]map[string]string, []sim.SeedObject) {
			out := map[string]map[string]string{}
			for suffix, data := range preload {
				out[name+suffix] = data
			}
			return out, objects
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
	// The list as a release before 0.13 kept it: under "index".
	list, err := creds.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"credentials": list})
	_, version, _, err := sk.LoadKey(ctx, "index")
	if err == nil {
		err = sk.SaveKeyIf(ctx, "index", b, version)
	}
	if err != nil {
		t.Fatal(err)
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
	// wait shortens the two minutes a start waits for its database.
	wait time.Duration
	args []string
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
	args := []string{"-api-server", c.url, "-cluster-name", "simulated", "-listen", "127.0.0.1:0", "-log-format", "text", "-health-interval", "2s",
		"-auth-mode", "local", "-local-users", localUsers(t),
		"-release-namespace", simNamespace, "-release-fullname", simRelease}
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
			wiring: func(w *wiring) { w.databaseWait = o.wait },
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
// address nothing listens at, which is all a start that is to give up
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

// gaveUp checks a start that waited for a database that is away and gave
// up: it did not listen, and it returned an error that says why.
func gaveUp(t *testing.T, con *console, wait time.Duration) {
	t.Helper()
	if con.url != "" {
		t.Fatal("the console listened without its database")
	}
	err := con.stopped(time.Second)
	if err == nil || !strings.Contains(err.Error(), "did not answer in "+wait.String()+" of trying") || !strings.Contains(err.Error(), "a console does not start without its database") {
		t.Fatalf("the start ended with %v", err)
	}
	if strings.Contains(con.logs.String(), "msg=serving") {
		t.Error("the log says the console serves")
	}
	noSecretIn(t, con)
}

// A console given no database does not start (DB-69): there is no mode in
// which it keeps its records anywhere else, so a start without -db-host
// stops at once with the sentence that says what to set, before it has
// made anything that keeps records.
func TestAConsoleGivenNoDatabaseDoesNotStart(t *testing.T) {
	w := &wiring{log: slog.New(slog.DiscardHandler)}
	err := w.wireDatabase(t.Context())
	if !errors.Is(err, errNoDatabase) || w.database != nil {
		t.Fatalf("a start without a database: %v (database %v)", err, w.database)
	}
	if !strings.Contains(err.Error(), "-db-host") || !strings.Contains(err.Error(), "database.mode") {
		t.Fatalf("the refusal does not say what to set: %v", err)
	}
}

// A console never starts without its database (DB-02; the option to start
// without it, DB-67, was withdrawn by the owner on November 20, 2026).
// With the database refusing connections, and with it swallowing what it
// is sent, the start waits for it, returns an error, and nothing listens
// at the console's address at any moment; the console's records are
// untouched, nothing is written to the cluster, and no table is made. The
// first half needs no test database: an address nothing listens at is a
// database that is away.
// A console not told its release does not start (DB-97): its secrets
// would have no Secrets, and nothing is kept in memory in their place.
func TestAConsoleNotToldItsReleaseDoesNotStart(t *testing.T) {
	direct := dbtest.Config(t)
	c := newSimCluster(t, nil)
	con := c.start(t, consoleOptions{database: &direct, args: []string{"-release-namespace", "", "-release-fullname", ""}})
	if con.url != "" {
		t.Fatal("a console with no release listens")
	}
	if err := con.stopped(time.Minute); !errors.Is(err, errNoRelease) {
		t.Fatalf("it stopped with %v", err)
	}
}

func TestAConsoleNeverStartsWithoutItsDatabase(t *testing.T) {
	for _, hang := range []bool{false, true} {
		name := "the database refuses connections"
		if hang {
			name = "the database swallows what it is sent"
			if os.Getenv(dbtest.Env) == "" {
				continue
			}
		}
		t.Run(name, func(t *testing.T) {
			cfg, direct, _ := awayDatabase(t, hang)
			c := newSimCluster(t, map[string]map[string]string{"": {"setup.json": `{"dismissed":["backup"]}`}})
			c.beforeTheMove(t)
			before := c.records(t)
			c.written()
			// The console is told where to listen, so that the test can ask
			// there while the start waits.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			ln.Close()
			var listened atomic.Int32
			asked := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(asked)
				for {
					if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
						conn.Close()
						listened.Add(1)
					}
					select {
					case <-done:
						return
					case <-time.After(50 * time.Millisecond):
					}
				}
			}()
			const wait = 3 * time.Second
			began := time.Now()
			con := c.start(t, consoleOptions{database: &cfg, wait: wait, args: []string{"-listen", addr}})
			took := time.Since(began)
			close(done)
			<-asked
			gaveUp(t, con, wait)
			// A database that refuses is tried again and again, and the log
			// says so once; one that swallows holds the first try for the
			// whole wait.
			if n := strings.Count(con.logs.String(), "the database does not answer yet; trying for a while"); !hang && n != 1 {
				t.Errorf("the log says %d times that the start waits for the database, want once:\n%s", n, con.logs.String())
			}
			if took < wait-time.Second {
				t.Errorf("the start gave up after %s, without waiting %s for the database", took, wait)
			}
			if took > 40*time.Second {
				t.Errorf("the start took %s with a wait of %s", took, wait)
			}
			if n := listened.Load(); n > 0 {
				t.Errorf("something listened at the console's address %d times while its database was away", n)
			}
			if got := changed(before, c.records(t)); len(got) > 0 {
				t.Errorf("the start changed the console's records: %v", got)
			}
			if got := c.written(); len(got) > 0 {
				t.Errorf("the start wrote to the cluster: %v", got)
			}
			if direct != nil {
				if got := tables(t, *direct); len(got) > 0 {
					t.Errorf("a start that gave up left tables in the database: %v", got)
				}
			}
		})
	}
}

// The start that follows one that gave up is a first start (DB-02): once
// the database answers, the schema's steps, the copies and the removals
// run, each once; a person signs in with a stored session and finds the
// records that were copied; and no secret is in the database or the log.
func TestTheStartAfterOneThatGaveUpIsAFirstStart(t *testing.T) {
	cfg, direct, back := awayDatabase(t, false)
	if direct == nil {
		t.Skipf("no PostgreSQL to test against: set %s=host:port", dbtest.Env)
	}
	c := newSimCluster(t, map[string]map[string]string{"": {"setup.json": `{"dismissed":["backup"]}`}})
	c.beforeTheMove(t)
	before := c.records(t)
	const wait = 2 * time.Second
	gaveUp(t, c.start(t, consoleOptions{database: &cfg, wait: wait}), wait)
	if got := tables(t, *direct); len(got) > 0 {
		t.Errorf("a start that gave up left tables in the database: %v", got)
	}
	if got := changed(before, c.records(t)); len(got) > 0 {
		t.Errorf("a start that gave up changed the console's records: %v", got)
	}

	back()
	again := c.start(t, consoleOptions{database: &cfg})
	if again.url == "" {
		t.Fatalf("the console did not start with its database: %v\n%s", again.stopped(time.Second), again.logs.String())
	}
	if !again.w.database.Health().Available {
		t.Fatal("the start that followed is not a start with the database")
	}
	log := again.logs.String()
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
	b := browser()
	stored := again.signIn(b)
	if stored == "" || strings.HasPrefix(stored, "s1.") {
		t.Errorf("the session of a sign-in with the database is not a stored one (%d characters)", len(stored))
	}
	if code, body, _ := again.get(b, "/api/v1/me"); code != http.StatusOK || !strings.Contains(body, adminLogin) {
		t.Fatalf("/api/v1/me answers %d: %s", code, body)
	}
	if code, body, _ := again.get(b, "/api/v1/database"); code != http.StatusOK || !strings.Contains(body, `"configured":true`) || !strings.Contains(body, `"available":true`) || strings.Contains(body, "startedWithout") {
		t.Errorf("/api/v1/database answers %d: %s", code, body)
	}
	if code, body, _ := again.get(http.DefaultClient, "/metrics"); code != http.StatusOK || metric(body, "b221_database_up") != "1" || strings.Contains(body, "b221_database_started_without") {
		t.Errorf("/metrics: %d, b221_database_up %q", code, metric(body, "b221_database_up"))
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
	noSecretIn(t, again, stored, c.sessionKey(t))
	got := counts(tables(t, *direct))
	if got["schema_migrations"] == "0" || got["clusters"] == "0" || got["credentials"] != "2" || got["applications"] != "1" || got["auth_sessions"] != "1" {
		t.Errorf("the rows after the copies: %v", got)
	}
	for _, secret := range []string{secretToken, secretValues, secretCred} {
		if held := holdingText(t, *direct, secret); len(held) > 0 {
			t.Errorf("a secret is in the database, in %v", held)
		}
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

// unchangedByAStart are tables whose rows a start never changes: the
// record of what was copied and applied, and the record sets the copies
// made.
var unchangedByAStart = []string{"imports", "schema_migrations", "clusters", "credentials", "applications", "application_commands", "settings", "repositories"}

// With a database that already holds the console's records, a start that
// gives up because it is away changes nothing: every table is as it was,
// and so is every ConfigMap and Secret. The start that follows, when the
// database is back, copies nothing again and keeps what was saved.
func TestAStartThatGaveUpLeavesTheRecordsAsTheyWere(t *testing.T) {
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

	// The database goes away, and the console is started: it gives up.
	away.Cut()
	const wait = 2 * time.Second
	gaveUp(t, c.start(t, consoleOptions{database: &cfg, wait: wait}), wait)
	away.Back()
	if got := differ(held, tables(t, direct)); len(got) > 0 {
		t.Errorf("a start that gave up changed the database (before, after): %v", got)
	}
	if got := changed(records, c.records(t)); len(got) > 0 {
		t.Errorf("a start that gave up changed the console's records: %v", got)
	}

	// The start that follows copies nothing and keeps what was saved.
	again := c.start(t, consoleOptions{database: &cfg})
	if again.url == "" {
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
			t.Errorf("%s changed over a start that gave up and the start that followed: %s, then %s", table, held[table], after[table])
		}
	}
	// The session key's Secret and the accounts' are the two a start may
	// write; everything else is as it was.
	if got := changed(records, c.records(t), "secret/"+simRelease+auth.SessionKeySecretSuffix, "secret/"+simRelease+"-users"); len(got) > 0 {
		t.Errorf("the start that followed changed the console's records: %v", got)
	}
}

// With two replicas, one that has the database and one that is started
// while it cannot reach it, the second never listens and the first is
// unaffected: it goes on, serves what the database keeps, and its records
// and tables are as they were.
func TestAReplicaWithItsDatabaseIsUnaffectedByOneThatDoesNotStart(t *testing.T) {
	direct := dbtest.Config(t)
	away, relayed := dbtest.NewAway(t, direct)
	c := newSimCluster(t, nil)
	c.beforeTheMove(t)
	// The first replica reaches the database directly; the second through
	// a relay that is cut: the database is away for that one alone.
	with := c.start(t, consoleOptions{database: &direct})
	if with.url == "" {
		t.Fatalf("the first replica did not start: %v\n%s", with.stopped(time.Second), with.logs.String())
	}
	b := browser()
	with.signIn(b)
	settled(t, c, direct)
	held, records := counts(tables(t, direct), "audit_events"), c.records(t)
	away.Cut()
	const wait = 2 * time.Second
	gaveUp(t, c.start(t, consoleOptions{database: &relayed, wait: wait}), wait)
	select {
	case err := <-with.done:
		t.Fatalf("the replica with its database stopped too: %v", err)
	case <-time.After(time.Second):
	}
	if code, body, _ := with.get(b, "/api/v1/credentials"); code != http.StatusOK || !strings.Contains(body, "GitHub") {
		t.Errorf("the replica with its database, after the other gave up: %d %s", code, body)
	}
	if got := changed(records, c.records(t)); len(got) > 0 {
		t.Errorf("the records changed: %v", got)
	}
	if got := differ(held, counts(tables(t, direct), "audit_events")); len(got) > 0 {
		t.Errorf("the tables' rows changed while a replica could not start (before, after): %v", got)
	}
	if err := with.stop(); err != nil {
		t.Error(err)
	}
}
