package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/sim"
	tb "github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/watson"
)

const (
	apiBootPassword = "Bootstrap-Pw-4711-hosts"
	apiSudoPassword = "Sudo-Pw-0815-hosts"
)

// pairSpy is the console's SSH service, but for the pairing session: it
// keeps what credential arrived and fails as a host whose sudo refused.
type pairSpy struct {
	*nodessh.Service
	got  nodessh.Credential
	held *nodessh.Credential
}

func (p *pairSpy) PairHost(_ context.Context, _, _ string, cred *nodessh.Credential, _ string) (nodessh.Host, []string, error) {
	p.got, p.held = *cred, cred
	cred.Wipe()
	return nodessh.Host{}, []string{"read ••••••"}, errors.New("sudo refused the password: check the sudo password, or run the setup command yourself")
}

type hostsEnv struct {
	srv   *httptest.Server
	c     *http.Client
	spy   *pairSpy
	audit *bytes.Buffer
	log   *bytes.Buffer
}

func hostsSetup(t *testing.T, who string) hostsEnv {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	cfg := tb.Default()
	cfg.SSH.Enabled = true
	b := &kv.Memory{}
	svc := nodessh.New(nodessh.Options{CA: nodessh.NewCA(nil), Pairings: nodessh.NewPairings(b), Hosts: nodessh.NewHosts(b), IsNode: func(_, name string) bool { return name == "n1" || name == "n2" }})
	spy := &pairSpy{Service: svc}
	st := automation.NewStore(&kv.Memory{}, nil)
	var log bytes.Buffer
	srv, c, m, auditOut, local, store := multiSetupWith(t, who, nil, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Automation, o.AutomationStore = automation.NewEngine(automation.Options{Store: st, Exec: noExec{}}), st
		o.Sessions = sessions.New(sessions.Options{Config: cfg, Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.SSH, o.Hosts = svc, spy
		// The hosts' storage, for a backup.
		o.KV = b
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite}, local, store); err != nil {
		t.Fatal(err)
	}
	return hostsEnv{srv: srv, c: c, spy: spy, audit: auditOut, log: &log}
}

func (e hostsEnv) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	r, b := send(t, e.c, method, e.srv.URL+path, body)
	return r.StatusCode, b
}

// Hosts are a SuperAdmin's, like Automation.
func TestHostsAreASuperAdminsAlone(t *testing.T) {
	e := hostsSetup(t, "viewer@corp.example")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/hosts", ""},
		{"POST", "/api/v1/hosts", `{"name":"backup-1","address":"10.0.0.9"}`},
		{"POST", "/api/v1/hosts/backup-1/scan", "{}"},
		{"POST", "/api/v1/hosts/backup-1/trust", `{"fingerprint":"SHA256:x"}`},
		{"POST", "/api/v1/hosts/backup-1/pair", `{"user":"admin","password":"` + apiBootPassword + `"}`},
		{"POST", "/api/v1/hosts/backup-1/check", "{}"},
		{"GET", "/api/v1/hosts/backup-1/setup", ""},
		{"GET", "/api/v1/hosts/backup-1/cleanup", ""},
		{"DELETE", "/api/v1/hosts/backup-1", ""},
	} {
		if code, _ := e.do(t, c.method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("%s %s answered %d for someone who is not a SuperAdmin", c.method, c.path, code)
		}
	}
	if e.spy.got.User != "" {
		t.Fatal("a credential reached the pairing")
	}
}

// A host is added under a name no node has, listed without its key, and is
// a node of every fleet's list for Automation, not runnable until paired.
func TestAHostIsAddedAndAutomationSeesIt(t *testing.T) {
	e := hostsSetup(t, "ops@corp.example")
	if code, body := e.do(t, "POST", "/api/v1/hosts", `{"name":"n1","address":"10.0.0.9"}`); code != http.StatusConflict || !strings.Contains(body, "Kubernetes node") {
		t.Fatalf("a node's name: %d %s", code, body)
	}
	code, body := e.do(t, "POST", "/api/v1/hosts", `{"name":"backup-1","address":"10.0.0.9","labels":{"role":"backup"}}`)
	if code != http.StatusCreated || !strings.Contains(body, `"state":"pending"`) || !strings.Contains(body, `"loginUser":"root"`) {
		t.Fatalf("added: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/v1/hosts", "")
	if code != 200 || !strings.Contains(body, `"name":"backup-1"`) || !strings.Contains(body, `"port":22`) {
		t.Fatalf("listed: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/v1/automation/fleets", "")
	var fl automationFleets
	if code != 200 || json.Unmarshal([]byte(body), &fl) != nil {
		t.Fatalf("fleets: %d %s", code, body)
	}
	all := fl.Fleets[len(fl.Fleets)-1]
	found := false
	for _, n := range all.Nodes {
		if n.Name == "backup-1" {
			found = n.Host && !n.Paired && strings.Contains(n.Why, "not paired yet")
		}
	}
	if !found {
		t.Fatalf("All nodes: %+v", all.Nodes)
	}
	if !strings.Contains(e.audit.String(), `"action":"host.add"`) {
		t.Fatalf("the audit: %s", e.audit.String())
	}
	// The cleanup command is there before it is removed, and with it.
	if code, body = e.do(t, "GET", "/api/v1/hosts/backup-1/cleanup", ""); code != 200 || !strings.Contains(body, "221B-CLEANUP") {
		t.Fatalf("the cleanup: %d %s", code, body)
	}
	code, body = e.do(t, "DELETE", "/api/v1/hosts/backup-1", "")
	if code != 200 || !strings.Contains(body, "221B-CLEANUP") {
		t.Fatalf("removed: %d %s", code, body)
	}
	if code, body = e.do(t, "GET", "/api/v1/hosts", ""); !strings.Contains(body, `"hosts":[]`) {
		t.Fatalf("still listed: %d %s", code, body)
	}
}

// The bootstrap credential goes to the pairing as given, and nowhere
// else: not into the answer, the audit log or the server's log, not even
// when the request is not valid.
func TestTheBootstrapCredentialGoesToThePairingAlone(t *testing.T) {
	e := hostsSetup(t, "ops@corp.example")
	if code, body := e.do(t, "POST", "/api/v1/hosts", `{"name":"backup-1","address":"10.0.0.9"}`); code != http.StatusCreated {
		t.Fatalf("added: %d %s", code, body)
	}
	var answers []string
	for _, bad := range []string{
		`{"user":"admin","password":"` + apiBootPassword + `"`,
		`{"user":"admin","pasword":"` + apiBootPassword + `"}`,
		`{"user":"admin","password":` + apiBootPassword + `}`,
		`["` + apiBootPassword + `"]`,
	} {
		code, body := e.do(t, "POST", "/api/v1/hosts/backup-1/pair", bad)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, code, body)
		}
		answers = append(answers, body)
	}
	if e.spy.got.User != "" {
		t.Fatal("a request that is not valid reached the pairing")
	}
	code, body := e.do(t, "POST", "/api/v1/hosts/backup-1/pair", `{"user":"admin","password":"`+apiBootPassword+`","sudoPassword":"`+apiSudoPassword+`"}`)
	answers = append(answers, body)
	if code != http.StatusBadGateway || !strings.Contains(body, "Sudo refused the password") || !strings.Contains(body, `"steps":["read ••••••"]`) {
		t.Fatalf("the pairing: %d %s", code, body)
	}
	if e.spy.got.User != "admin" || e.spy.got.Password != apiBootPassword || e.spy.got.SudoPassword != apiSudoPassword {
		t.Fatalf("the pairing got %q", e.spy.got.User)
	}
	if e.spy.held.Password != "" || e.spy.held.SudoPassword != "" {
		t.Fatal("the credential was not wiped")
	}
	if !strings.Contains(e.audit.String(), `"action":"host.pair"`) || !strings.Contains(e.audit.String(), "with the account admin") {
		t.Fatalf("the audit: %s", e.audit.String())
	}
	for _, s := range append(answers, e.audit.String(), e.log.String()) {
		for _, v := range []string{apiBootPassword, apiSudoPassword} {
			if strings.Contains(s, v) {
				t.Fatalf("%q shows the credential: %s", v, s)
			}
		}
	}
}

// A backup holds the hosts outside Kubernetes, and a restore brings back
// those missing, each checked as one added by hand: a pinned key comes
// with its fingerprint worked out again; a name taken by a host or a node
// here, or a key that is not one, is left out and named.
func TestABackupHoldsTheHostsAndARestoreBringsBackThoseMissing(t *testing.T) {
	e := hostsSetup(t, "ops@corp.example")
	if code, body := e.do(t, "POST", "/api/v1/hosts", `{"name":"db1","address":"10.0.0.21"}`); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("adding db1: %d %s", code, body)
	}
	_, body := e.do(t, "GET", "/api/v1/admin/backup", "")
	var f backupFile
	if err := json.Unmarshal([]byte(unzip(t, body)), &f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.SSHHosts), `"local/db1"`) {
		t.Fatalf("the hosts in the backup: %s", f.SSHHosts)
	}
	key := hostKeyLine(t)
	f.SSHHosts = json.RawMessage(`{"local/db1":{"cluster":"local","name":"db1","address":"10.0.0.99","port":22,"loginUser":"root","state":"pending"},` +
		`"local/db2":{"cluster":"local","name":"db2","address":"10.0.0.22","port":2222,"loginUser":"ops","state":"paired","hostKey":"` + key + `","fingerprint":"SHA256:wrong","offered":"x"},` +
		`"local/n1":{"cluster":"local","name":"n1","address":"10.0.0.23","port":22,"loginUser":"root","state":"pending"},` +
		`"local/odd":{"cluster":"local","name":"odd","address":"10.0.0.24","port":22,"loginUser":"root","state":"paired","hostKey":"not a key"}}`)
	in, _ := json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"sshHosts": true}})
	code, out := e.do(t, "POST", "/api/v1/admin/restore", string(in))
	var rep restoreReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != http.StatusOK {
		t.Fatalf("the restore: %d %s", code, out)
	}
	notes := strings.Join(rep.Notes, "\n")
	if rep.Restored["sshHosts"] != 1 || !strings.Contains(notes, "local/db1 is not restored") || !strings.Contains(notes, "local/n1 is not restored") || !strings.Contains(notes, "local/odd is not restored") {
		t.Fatalf("restored %+v, notes:\n%s", rep.Restored, notes)
	}
	_, list := e.do(t, "GET", "/api/v1/hosts", "")
	var got struct{ Hosts []nodessh.Host }
	_ = json.Unmarshal([]byte(list), &got)
	for _, h := range got.Hosts {
		switch h.Name {
		case "db1":
			if h.Address != "10.0.0.21" {
				t.Fatalf("db1 here was replaced: %+v", h)
			}
		case "db2":
			if h.State != nodessh.HostPaired || h.Port != 2222 || h.Fingerprint == "SHA256:wrong" || !strings.HasPrefix(h.Fingerprint, "SHA256:") {
				t.Fatalf("db2 after the restore: %+v", h)
			}
		}
	}
	if !strings.Contains(list, `"db2"`) {
		t.Fatalf("db2 is not listed: %s", list)
	}
}
