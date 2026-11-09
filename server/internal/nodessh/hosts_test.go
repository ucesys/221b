package nodessh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/kv"
)

// The bootstrap credential of the tests: each value is one no record,
// answer, error or log may hold.
const (
	bootUser     = "admin"
	bootPassword = "Tr0ub4dor-&3-bootstrap"
	sudoPassword = "sudo-pw-Correct-Horse-9"
	passphrase   = "passphrase-Battery-Staple-7"
)

// fakeSudo stands for sudo: -S reads the password from stdin, -n refuses
// when one is needed, and the environment says how the host is set up.
const fakeSudo = `#!/bin/sh
stdin=; nonint=
while [ $# -gt 0 ]; do
  case "$1" in
    -S) stdin=1 ;;
    -k) ;;
    -n) nonint=1 ;;
    -p) shift ;;
    --) shift; break ;;
    *) break ;;
  esac
  shift
done
if [ -n "$SUDO_TTY" ]; then echo "sudo: sorry, you must have a tty to run sudo" >&2; exit 1; fi
if [ -n "$SUDO_NOPASSWD" ]; then exec "$@"; fi
if [ -n "$nonint" ]; then echo "sudo: a password is required" >&2; exit 1; fi
IFS= read -r pw || pw=
if [ "$pw" != "$SUDO_PASSWORD" ]; then echo "Sorry, try again." >&2; echo "sudo: 1 incorrect password attempt" >&2; exit 1; fi
exec "$@"
`

// sshHost is a machine outside Kubernetes: an SSH server whose commands
// run for real over a fakeHost's files, sudo included, and which admits a
// certificate as sshd would from what the pairing script left there.
type sshHost struct {
	*fakeHost
	t        *testing.T
	ln       net.Listener
	mu       sync.Mutex
	key      ssh.Signer
	password bool
	bootKey  ssh.PublicKey
	env      []string
	// attempts counts sign-ins with a password or a key that is no
	// certificate; commands and stdin are what the host was given.
	attempts int
	commands []string
	stdin    []string
}

func newSSHHost(t *testing.T) *sshHost {
	t.Helper()
	h := &sshHost{fakeHost: newHost(t), t: t, password: true}
	if err := os.WriteFile(filepath.Join(h.bin, "sudo"), []byte(fakeSudo), 0o755); err != nil {
		t.Fatal(err)
	}
	h.setKey()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.ln = ln
	// sshd listens on the port the host is registered with.
	h.fakeHost.env = append(h.fakeHost.env, fmt.Sprintf("SSHD_PORT=%d", h.port()))
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	return h
}

// setKey gives the host a new host key, in its key file too.
func (h *sshHost) setKey() {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	h.mu.Lock()
	h.key = signer
	h.mu.Unlock()
	h.write("etc/ssh/ssh_host_ed25519_key.pub", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))+" root@host\n")
}

func (h *sshHost) port() int { return h.ln.Addr().(*net.TCPAddr).Port }

func (h *sshHost) fingerprint() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return ssh.FingerprintSHA256(h.key.PublicKey())
}

func (h *sshHost) config() *ssh.ServerConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		cert, ok := key.(*ssh.Certificate)
		if !ok {
			h.mu.Lock()
			h.attempts++
			boot := h.bootKey
			h.mu.Unlock()
			if boot != nil && c.User() == bootUser && bytes.Equal(key.Marshal(), boot.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("no")
		}
		checker := ssh.CertChecker{IsUserAuthority: func(ssh.PublicKey) bool { return true }}
		if len(cert.ValidPrincipals) == 0 || checker.CheckCert(cert.ValidPrincipals[0], cert) != nil || !h.admits(cert, c.User()) {
			return nil, errors.New("not admitted")
		}
		return &ssh.Permissions{}, nil
	}}
	if h.password {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			h.mu.Lock()
			h.attempts++
			h.mu.Unlock()
			if c.User() == bootUser && string(pw) == bootPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("no")
		}
	}
	cfg.AddHostKey(h.key)
	return cfg
}

func (h *sshHost) serve(c net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(c, h.config())
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					r.Reply(false, nil)
					continue
				}
				var p struct{ Command string }
				_ = ssh.Unmarshal(r.Payload, &p)
				r.Reply(true, nil)
				status := h.run(p.Command, ch)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
				return
			}
		}()
	}
}

// run runs a command as the host's shell would, with its paths under the
// fake root, and keeps what it was given.
func (h *sshHost) run(command string, ch ssh.Channel) int {
	h.mu.Lock()
	h.commands = append(h.commands, command)
	env := append(append([]string{}, h.fakeHost.env...), h.env...)
	h.mu.Unlock()
	command = strings.ReplaceAll(command, "/etc/ssh", h.root+"/etc/ssh")
	command = strings.ReplaceAll(command, "/run/sshd", h.root+"/run/sshd")
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = env
	var in bytes.Buffer
	cmd.Stdin = io.TeeReader(ch, &in)
	cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
	err := cmd.Run()
	h.mu.Lock()
	h.stdin = append(h.stdin, in.String())
	h.mu.Unlock()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return 127
	}
	return 0
}

// hostEnv is a console with a host registered, its storage and its log.
type hostEnv struct {
	svc     *Service
	host    *sshHost
	backend *kv.Memory
	log     *safeBuffer
	login   string
}

func newHostEnv(t *testing.T) hostEnv {
	t.Helper()
	ca := NewCA(nil)
	if err := ca.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := &kv.Memory{}
	var log safeBuffer
	svc := New(Options{CA: ca, Pairings: NewPairings(b), Hosts: NewHosts(b), Console: testConsole, CertTTL: time.Minute, Handshake: 5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Mask:   func(w io.Writer, secrets []string) io.Writer { return automation.NewMask(w, secrets) },
		IsNode: func(_, name string) bool { return name == "worker-1" },
		// A host is reached at its own address: a node's would be wrong.
		NodeAddr: func(string, string) (string, error) { return "", errors.New("not a node") },
	})
	h := newSSHHost(t)
	env := hostEnv{svc: svc, host: h, backend: b, log: &log, login: user(t)}
	if _, err := svc.AddHost(context.Background(), Host{Cluster: "local", Name: "backup-1", Address: "127.0.0.1", Port: h.port(), LoginUser: env.login, Labels: map[string]string{"role": "backup"}}, "Pat"); err != nil {
		t.Fatal(err)
	}
	return env
}

// trust reads the host's key and confirms it, as the dialog does.
func (e hostEnv) trust(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	offer, err := e.svc.ScanHost(ctx, "local", "backup-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.TrustHost(ctx, "local", "backup-1", offer.Fingerprint, "Pat"); err != nil {
		t.Fatal(err)
	}
}

// leaks fails the test if a value of the bootstrap credential is anywhere
// the console keeps or says something: the storage, the log, an answer.
func (e hostEnv) leaks(t *testing.T, where string, texts ...string) {
	t.Helper()
	stored := ""
	for _, k := range []string{HostsKey, PairingsKey} {
		b, _, _ := e.backend.LoadKey(context.Background(), k)
		stored += string(b)
	}
	all := append([]string{stored, e.log.String()}, texts...)
	for _, s := range all {
		for _, v := range []string{bootPassword, sudoPassword, passphrase, "PRIVATE KEY"} {
			if strings.Contains(s, v) {
				t.Fatalf("%s: %q holds a credential:\n%s", where, v, s)
			}
		}
	}
}

// A host is registered by name, address, port and login user; its name is
// a node name, and not one a node of the cluster or another host has.
func TestAHostIsRegisteredUnderANameOfItsOwn(t *testing.T) {
	ctx := context.Background()
	svc := New(Options{CA: NewCA(nil), Pairings: NewPairings(&kv.Memory{}), Hosts: NewHosts(&kv.Memory{}), IsNode: func(_, name string) bool { return name == "worker-1" }})
	h, err := svc.AddHost(ctx, Host{Cluster: "local", Name: " backup-1 ", Address: "10.0.0.9", LoginUser: "root", Labels: map[string]string{"role": "backup"}}, "Pat")
	if err != nil || h.Name != "backup-1" || h.Port != 22 || h.State != HostPending || h.AddedBy != "Pat" || h.HostKey != "" {
		t.Fatalf("added %+v, %v", h, err)
	}
	for name, x := range map[string]Host{
		"a node's name":       {Name: "worker-1", Address: "10.0.0.1", LoginUser: "root"},
		"another host's name": {Name: "backup-1", Address: "10.0.0.2", LoginUser: "root"},
	} {
		x.Cluster = "local"
		if _, err := svc.AddHost(ctx, x, "Pat"); !errors.Is(err, ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A pending host added again at its address is taken up again, with
	// what is given now (asked November 5, 2026).
	again, err := svc.AddHost(ctx, Host{Cluster: "local", Name: "backup-1", Address: "10.0.0.9", Port: 2222, LoginUser: "root", Labels: map[string]string{"role": "backup"}}, "Sam")
	if err != nil || again.Port != 2222 || again.AddedBy != "Sam" || again.State != HostPending {
		t.Fatalf("added again: %+v %v", again, err)
	}
	for name, x := range map[string]Host{
		"no name":            {Address: "10.0.0.1", LoginUser: "root"},
		"an upper-case name": {Name: "Backup", Address: "10.0.0.1", LoginUser: "root"},
		"a URL":              {Name: "b", Address: "ssh://10.0.0.1", LoginUser: "root"},
		"an address:port":    {Name: "b", Address: "10.0.0.1:22", LoginUser: "root"},
		"a port too high":    {Name: "b", Address: "10.0.0.1", Port: 70000, LoginUser: "root"},
		"a login user":       {Name: "b", Address: "10.0.0.1", LoginUser: "root; rm -rf /"},
		"a label":            {Name: "b", Address: "10.0.0.1", LoginUser: "root", Labels: map[string]string{"bad key!": "x"}},
	} {
		x.Cluster = "local"
		if _, err := svc.AddHost(ctx, x, "Pat"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The same name in another cluster is another host.
	if _, err := svc.AddHost(ctx, Host{Cluster: "other", Name: "backup-1", Address: "backup-1.example.com", LoginUser: "root"}, "Pat"); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.Hosts(ctx, "local"); len(list) != 1 || list[0].Labels["role"] != "backup" {
		t.Fatalf("listed %+v", list)
	}
}

// The host's key is read with no credential, and only the fingerprint
// comes back; no credential goes to the host before a SuperAdmin confirmed
// it, and only the key the host showed can be pinned.
func TestAHostsKeyIsConfirmedBeforeAnyCredentialIsSent(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t)
	cred := Credential{User: bootUser, Password: bootPassword}
	if _, _, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "confirm the host's key first") {
		t.Fatalf("paired before the key was confirmed: %v", err)
	}
	if cred.Password != "" {
		t.Fatal("the refused credential was not wiped")
	}
	if _, err := e.svc.CheckHost(ctx, "local", "backup-1", "Pat"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("checked before the key was confirmed: %v", err)
	}
	offer, err := e.svc.ScanHost(ctx, "local", "backup-1")
	if err != nil {
		t.Fatal(err)
	}
	if offer.Fingerprint != e.host.fingerprint() || offer.KeyType != ssh.KeyAlgoED25519 || offer.Command != "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub" {
		t.Fatalf("the offer: %+v", offer)
	}
	if b, _ := json.Marshal(offer); strings.Contains(string(b), "AAAA") {
		t.Fatalf("the answer holds the key: %s", b)
	}
	if e.host.attempts != 0 {
		t.Fatalf("the host was offered %d credentials before its key was trusted", e.host.attempts)
	}
	if _, _, err := e.svc.TrustHost(ctx, "local", "backup-1", "SHA256:something-else", "Pat"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another fingerprint was trusted: %v", err)
	}
	h, again, err := e.svc.TrustHost(ctx, "local", "backup-1", offer.Fingerprint, "Pat")
	if err != nil || again || h.Fingerprint != offer.Fingerprint || h.HostKey == "" || h.Offered != "" || h.TrustedBy != "Pat" {
		t.Fatalf("trusted %+v %v %v", h, again, err)
	}
	// Trusting needs a key read just before.
	if _, _, err := e.svc.TrustHost(ctx, "local", "backup-1", offer.Fingerprint, "Pat"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("trusted twice from one reading: %v", err)
	}
}

// A password pairs a host: one session, the pinned key checked, the
// setup script through sudo with its password on stdin and never on the
// command line; then the console logs in with a certificate at the host's
// own address. The credential is nowhere afterwards: not in the storage,
// the log, the answer or an error.
func TestAPasswordPairsAHostAndIsKeptNowhere(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t)
	e.trust(t)
	e.host.env = []string{"SUDO_PASSWORD=" + sudoPassword}
	cred := Credential{User: bootUser, Password: bootPassword, SudoPassword: sudoPassword}
	h, steps, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat")
	if err != nil {
		t.Fatalf("pairing: %v\n%s", err, e.log.String())
	}
	if h.State != HostPaired || h.PairedBy != "Pat" || h.PairedAt == nil {
		t.Fatalf("the host: %+v", h)
	}
	if cred.Password != "" || cred.SudoPassword != "" || cred.User != bootUser {
		t.Fatalf("the credential was not wiped: %v", cred.Password != "")
	}
	first := e.host.commands[0]
	if !strings.HasPrefix(first, "sudo -S -k -p '' -- sh -c ") || strings.Contains(first, sudoPassword) || strings.Contains(first, bootPassword) {
		t.Fatalf("the command: %.80s", first)
	}
	if !strings.HasPrefix(e.host.stdin[0], sudoPassword+"\n") {
		t.Fatal("sudo did not get its password on stdin")
	}
	p, ok, _ := e.svc.opts.Pairings.Get(ctx, "local", "backup-1")
	if !ok || p.LoginUser != e.login || p.Port != e.host.port() || len(p.HostKeys) != 1 || fingerprintOf(p.HostKeys[0]) != e.host.fingerprint() || p.Principal != PrincipalOf(testConsole) {
		t.Fatalf("the pairing: %+v", p)
	}
	for _, s := range steps {
		if strings.Contains(s, "AAAA") || strings.Contains(s, markHostKey) {
			t.Fatalf("a step shows a host key: %s", s)
		}
	}
	// Automation reaches it as a node.
	var out safeBuffer
	code, err := e.svc.Exec(ctx, "local", "backup-1", "pat via 221B automation run 1", "echo hello from the host", false, strings.NewReader(""), &out)
	if err != nil || code != 0 || !strings.Contains(out.String(), "hello from the host") {
		t.Fatalf("exec: %d %v %q", code, err, out.String())
	}
	b, _ := json.Marshal(h)
	e.leaks(t, "after pairing", strings.Join(steps, "\n"), string(b), fmt.Sprintf("%v %+v %#v %s", cred, cred, cred, cred))
}

// The credential prints as its user alone, whatever prints it.
func TestACredentialPrintsAsItsUserAlone(t *testing.T) {
	c := Credential{User: bootUser, Password: bootPassword, PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----", Passphrase: passphrase, SudoPassword: sudoPassword}
	var log bytes.Buffer
	slog.New(slog.NewJSONHandler(&log, nil)).Info("pairing", "cred", c, "ptr", &c)
	b, _ := json.Marshal(map[string]any{"c": c, "p": &c})
	for _, s := range []string{fmt.Sprintf("%v %+v %#v %s %q", c, c, c, c, c), fmt.Sprint(&c), log.String(), string(b)} {
		for _, v := range []string{bootPassword, passphrase, sudoPassword, "PRIVATE KEY"} {
			if strings.Contains(s, v) {
				t.Fatalf("%q shows %q", s, v)
			}
		}
		if !strings.Contains(s, bootUser) {
			t.Fatalf("%q does not name the user", s)
		}
	}
}

// A private key pairs a host too, with its passphrase; an account whose
// sudo needs no password gets sudo -n, and nothing on stdin.
func TestAPrivateKeyPairsAHost(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t)
	e.trust(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "boot", []byte(passphrase))
	pemKey := string(pem.EncodeToMemory(block))
	sshPub, _ := ssh.NewPublicKey(pub)
	e.host.bootKey = sshPub
	e.host.env = []string{"SUDO_NOPASSWD=1"}
	for _, c := range []struct {
		cred Credential
		want string
	}{
		{Credential{User: bootUser, PrivateKey: pemKey}, "has a passphrase"},
		{Credential{User: bootUser, PrivateKey: pemKey, Passphrase: "wrong"}, "does not open"},
		{Credential{User: bootUser, PrivateKey: "not a key " + bootPassword}, "not readable"},
		{Credential{User: bootUser, PrivateKey: pemKey, Password: bootPassword}, "not both"},
		{Credential{User: "Not A User"}, "not a Unix user name"},
	} {
		cred := c.cred
		_, _, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat")
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.want, err)
		}
		if err != nil {
			e.leaks(t, c.want, err.Error())
		}
	}
	cred := Credential{User: bootUser, PrivateKey: pemKey, Passphrase: passphrase}
	h, _, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat")
	if err != nil || h.State != HostPaired {
		t.Fatalf("pairing with a key: %+v %v", h, err)
	}
	last := e.host.commands[0]
	if !strings.HasPrefix(last, "sudo -n -- sh -c ") || e.host.stdin[0] != "" {
		t.Fatalf("the command: %.40s, stdin %q", last, e.host.stdin[0])
	}
	e.leaks(t, "after pairing with a key")
}

// What stops a pairing is said in words that tell what to do, and none of
// them, nor the steps, nor the log, holds the credential: password sign-in
// off, sudo that refuses the password, needs a terminal or a password not
// given, and the script's own step.
func TestAFailedPairingSaysWhyAndLeaksNothing(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		setup    func(h *sshHost)
		cred     Credential
		want     string
		noRecord bool
	}{
		{name: "passwords off", setup: func(h *sshHost) { h.password = false }, cred: Credential{User: bootUser, Password: bootPassword}, want: "takes no password to sign in"},
		{name: "a wrong password", cred: Credential{User: bootUser, Password: "wrong-" + bootPassword}, want: "refused admin's password"},
		{name: "a wrong sudo password", setup: func(h *sshHost) { h.env = []string{"SUDO_PASSWORD=" + sudoPassword} }, cred: Credential{User: bootUser, Password: bootPassword, SudoPassword: "x" + sudoPassword}, want: "sudo refused the password"},
		{name: "requiretty", setup: func(h *sshHost) { h.env = []string{"SUDO_TTY=1"} }, cred: Credential{User: bootUser, Password: bootPassword}, want: "needs a terminal"},
		{name: "a host that echoes the password", setup: func(h *sshHost) {
			h.env = []string{"SUDO_PASSWORD=" + sudoPassword}
		}, cred: Credential{User: bootUser, Password: bootPassword, SudoPassword: sudoPassword}, want: ""},
		{name: "the script's step", setup: func(h *sshHost) {
			h.env = []string{"SUDO_PASSWORD=" + sudoPassword, "OTHER_CA=/etc/ssh/other_ca.pub"}
		}, cred: Credential{User: bootUser, Password: bootPassword, SudoPassword: sudoPassword}, want: "already trusts another SSH authority"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newHostEnv(t)
			e.trust(t)
			if c.setup != nil {
				c.setup(e.host)
			}
			if c.name == "a host that echoes the password" {
				// A host whose sudo prints what it read, on both streams.
				os.WriteFile(filepath.Join(e.host.bin, "sudo"), []byte("#!/bin/sh\nIFS= read -r pw\necho \"read $pw\"\necho \"sh: 1: $pw: not found\" >&2\nexit 1\n"), 0o755)
			}
			cred := c.cred
			_, steps, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat")
			if err == nil || (c.want != "" && !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if h, _, _ := e.svc.opts.Hosts.Get(ctx, "local", "backup-1"); h.State != HostPending {
				t.Fatalf("the host is %s", h.State)
			}
			if _, ok, _ := e.svc.opts.Pairings.Get(ctx, "local", "backup-1"); ok {
				t.Fatal("a pairing was recorded")
			}
			e.leaks(t, c.name, err.Error(), strings.Join(steps, "\n"))
		})
	}
}

// Without a credential: the SuperAdmin runs the setup command on the host,
// and the check logs in with a certificate; before, it says what to do.
func TestTheSetupCommandAndTheCheck(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t)
	e.trust(t)
	if _, err := e.svc.CheckHost(ctx, "local", "backup-1", "Pat"); err == nil || !strings.Contains(err.Error(), "run the setup command on it as root") {
		t.Fatalf("checked before the setup: %v", err)
	}
	if _, ok, _ := e.svc.opts.Pairings.Get(ctx, "local", "backup-1"); ok {
		t.Fatal("a failed check left a pairing")
	}
	cmd, err := e.svc.SetupCommand(ctx, "local", "backup-1")
	if err != nil || !strings.HasPrefix(cmd, "sudo sh <<'221B-SETUP'\n") || !strings.HasSuffix(cmd, "\n221B-SETUP\n") || !strings.Contains(cmd, "TrustedUserCAKeys /etc/ssh/221b/ca.pub") || !strings.Contains(cmd, strings.Fields(e.svc.opts.CA.PublicKey())[1]) {
		t.Fatalf("the command: %v\n%s", err, cmd)
	}
	// Run as root on the host: the script between the markers.
	script := strings.TrimSuffix(strings.TrimPrefix(cmd, "sudo sh <<'221B-SETUP'\n"), "\n221B-SETUP\n")
	e.host.mustRun(script)
	h, err := e.svc.CheckHost(ctx, "local", "backup-1", "Pat")
	if err != nil || h.State != HostPaired {
		t.Fatalf("the check: %+v %v", h, err)
	}
	// Removed, it is forgotten with its key and pairing; the cleanup
	// command takes this console's lines off it.
	gone, cleanup, err := e.svc.RemoveHost(ctx, "local", "backup-1")
	if err != nil || gone.Name != "backup-1" || !strings.HasPrefix(cleanup, "sudo sh <<'221B-CLEANUP'\n") || !strings.Contains(cleanup, markUnpaired) {
		t.Fatalf("removing: %v\n%s", err, cleanup)
	}
	if list, _ := e.svc.Hosts(ctx, "local"); len(list) != 0 {
		t.Fatalf("still listed: %+v", list)
	}
	if _, ok, _ := e.svc.opts.Pairings.Get(ctx, "local", "backup-1"); ok {
		t.Fatal("the pairing stayed")
	}
	e.host.mustRun(strings.TrimSuffix(strings.TrimPrefix(cleanup, "sudo sh <<'221B-CLEANUP'\n"), "\n221B-CLEANUP\n"))
	if e.host.exists("etc/ssh/221b") {
		t.Fatal("the cleanup left the console's files")
	}
}

// A host that shows another key is refused, and stays refused, even with
// its old key back, until a SuperAdmin reads the key and trusts it again;
// then it is reached with its pairing as it was.
func TestAChangedHostKeyRefusesEveryConnectionUntilTrustedAgain(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t)
	e.trust(t)
	e.host.env = []string{"SUDO_PASSWORD=" + sudoPassword}
	cred := Credential{User: bootUser, Password: bootPassword, SudoPassword: sudoPassword}
	if _, _, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat"); err != nil {
		t.Fatal(err)
	}
	e.host.setKey()
	_, err := e.svc.Exec(ctx, "local", "backup-1", "k", "true", false, strings.NewReader(""), io.Discard)
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "trusts it again") || strings.Contains(err.Error(), "SHA256:") {
		t.Fatalf("a changed key: %v", err)
	}
	for i := 0; i < 100; i++ {
		if h, _, _ := e.svc.opts.Hosts.Get(ctx, "local", "backup-1"); h.Mismatch != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h, _, _ := e.svc.opts.Hosts.Get(ctx, "local", "backup-1"); h.Mismatch != e.host.fingerprint() {
		t.Fatalf("the host is not marked: %+v", h)
	}
	attempts := e.host.attempts
	cred = Credential{User: bootUser, Password: bootPassword}
	if _, _, err := e.svc.PairHost(ctx, "local", "backup-1", &cred, "Pat"); err == nil || !strings.Contains(err.Error(), "trusts it again") {
		t.Fatalf("pairing a changed host: %v", err)
	}
	if e.host.attempts != attempts {
		t.Fatal("a credential went to a host whose key changed")
	}
	if _, err := e.svc.CheckHost(ctx, "local", "backup-1", "Pat"); err == nil {
		t.Fatal("checked a changed host")
	}
	offer, err := e.svc.ScanHost(ctx, "local", "backup-1")
	if err != nil {
		t.Fatal(err)
	}
	h, again, err := e.svc.TrustHost(ctx, "local", "backup-1", offer.Fingerprint, "Sam")
	if err != nil || !again || h.Mismatch != "" || h.Fingerprint != e.host.fingerprint() {
		t.Fatalf("trusted again: %+v %v %v", h, again, err)
	}
	if code, err := e.svc.Exec(ctx, "local", "backup-1", "k", "true", false, strings.NewReader(""), io.Discard); err != nil || code != 0 {
		t.Fatalf("after trusting again: %d %v", code, err)
	}
}

// The view of the Nodes tab shows a host by its state, never its key.
func TestAHostsRowShowsItsStateAndNoKey(t *testing.T) {
	now := time.Now()
	rows := HostRows([]Host{
		{Name: "a", Address: "10.0.0.1", Port: 22, LoginUser: "root", State: HostPending, AddedAt: now},
		{Name: "b", Address: "10.0.0.2", Port: 2222, LoginUser: "ops", State: HostPaired, HostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe", Fingerprint: "SHA256:x", PairedBy: "Pat", PairedAt: &now, AddedAt: now},
		{Name: "c", Address: "10.0.0.3", Port: 22, LoginUser: "root", State: HostPaired, Fingerprint: "SHA256:y", Mismatch: "SHA256:z", AddedAt: now},
	})
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), "AAAA") || strings.Contains(string(b), "SHA256:z") {
		t.Fatalf("a row shows a key: %s", b)
	}
	for i, want := range []struct{ status, tone string }{{"Pending", "warn"}, {"Paired", "ok"}, {"Key changed", "err"}} {
		r := rows[i]
		if r.Role != "host" || r.Host == nil || r.Status != want.status || r.Tone != want.tone || r.Labels == nil || r.Kubelet != "" {
			t.Errorf("row %s: %+v", r.Name, r)
		}
	}
	if rows[2].Errors != 1 || rows[0].Warnings != 1 || rows[1].Errors+rows[1].Warnings != 0 {
		t.Fatalf("problems: %d %d %d", rows[2].Errors, rows[0].Warnings, rows[1].Errors+rows[1].Warnings)
	}
}

// A host added under its address takes its hostname once the console
// logs in to it (asked November 5, 2026): lower-cased, its pairing and
// the lists that named it following; a hostname a node has, or one that
// is not a node name, leaves it as it was, and it stops trying.
func TestAHostAddedUnderItsAddressTakesItsHostname(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t)
	var renamed []string
	e.svc.opts.OnRename = func(_ context.Context, cluster, from, to string) { renamed = append(renamed, cluster+"/"+from+">"+to) }
	uname := func(name string) {
		if err := os.WriteFile(filepath.Join(e.host.bin, "uname"), []byte("#!/bin/sh\necho "+name+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	add := func(addr string) {
		h, err := e.svc.AddHost(ctx, Host{Cluster: "local", Name: addr, Address: addr, Port: e.host.port(), LoginUser: e.login}, "Pat")
		if err != nil || !h.AutoName {
			t.Fatalf("adding %s: %+v %v", addr, h, err)
		}
		offer, err := e.svc.ScanHost(ctx, "local", addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.svc.TrustHost(ctx, "local", addr, offer.Fingerprint, "Pat"); err != nil {
			t.Fatal(err)
		}
	}
	add("127.0.0.1")
	cmd, err := e.svc.SetupCommand(ctx, "local", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	e.host.mustRun(strings.TrimSuffix(strings.TrimPrefix(cmd, "sudo sh <<'221B-SETUP'\n"), "\n221B-SETUP\n"))
	uname("DB-Primary")
	h, err := e.svc.CheckHost(ctx, "local", "127.0.0.1", "Pat")
	if err != nil || h.Name != "db-primary" || h.Address != "127.0.0.1" || h.AutoName || h.State != HostPaired {
		t.Fatalf("after the check: %+v %v", h, err)
	}
	if _, ok, _ := e.svc.opts.Pairings.Get(ctx, "local", "127.0.0.1"); ok {
		t.Fatal("the pairing stayed under the address")
	}
	if p, ok, _ := e.svc.opts.Pairings.Get(ctx, "local", "db-primary"); !ok || p.Node != "db-primary" {
		t.Fatalf("the pairing under the hostname: %+v %v", p, ok)
	}
	if strings.Join(renamed, ",") != "local/127.0.0.1>db-primary" {
		t.Fatalf("the lists were told: %v", renamed)
	}
	// It is reached under its new name.
	if code, err := e.svc.Exec(ctx, "local", "db-primary", "k", "true", false, strings.NewReader(""), io.Discard); err != nil || code != 0 {
		t.Fatalf("a run on db-primary: %d %v", code, err)
	}
	// A node's name, or one that is not a node name: kept as it was.
	for _, c := range []struct{ addr, hostname string }{{"localhost", "worker-1"}, {"127.0.0.1", "under_score"}} {
		add(c.addr)
		uname(c.hostname)
		h, err := e.svc.CheckHost(ctx, "local", c.addr, "Pat")
		if err != nil || h.Name != c.addr || h.AutoName {
			t.Fatalf("%s with the hostname %s: %+v %v", c.addr, c.hostname, h, err)
		}
	}
	// A host named by hand keeps its name.
	e.trust(t)
	uname("other")
	if h, err := e.svc.CheckHost(ctx, "local", "backup-1", "Pat"); err != nil || h.Name != "backup-1" {
		t.Fatalf("backup-1: %+v %v", h, err)
	}
	if len(renamed) != 1 {
		t.Fatalf("renamed: %v", renamed)
	}
}

// A host whose SSH port does not answer is named with why and where to
// look, the console's network policy among them.
func TestAHostThatDoesNotAnswerSaysWhereToLook(t *testing.T) {
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	svc := New(Options{CA: NewCA(nil), Pairings: NewPairings(&kv.Memory{}), Hosts: NewHosts(&kv.Memory{}), Handshake: time.Second})
	if _, err := svc.AddHost(ctx, Host{Cluster: "local", Name: "gone", Address: "127.0.0.1", Port: port, LoginUser: "root"}, "Pat"); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ScanHost(ctx, "local", "gone")
	want := fmt.Sprintf("could not reach 127.0.0.1:%d: it refused the connection. Check that sshd runs there", port)
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "networkPolicy.nodeCIDRs") {
		t.Fatalf("the reason: %v", err)
	}
	// Paired or elsewhere, the name stays taken, saying why.
	if _, err := svc.AddHost(ctx, Host{Cluster: "local", Name: "gone", Address: "10.0.0.5", LoginUser: "root"}, "Pat"); err == nil || !strings.Contains(err.Error(), "at 127.0.0.1: remove it from its row on the Nodes tab") {
		t.Fatalf("another address: %v", err)
	}
}
