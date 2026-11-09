package nodessh

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/toolbox"
)

// Hosts outside Kubernetes (Phase 12). A SuperAdmin registers a host, the
// console reads its host key with no credential at all and shows the
// fingerprint, and only once the SuperAdmin confirms it is the key pinned
// and a credential taken. That credential opens one session, which runs
// the pairing script nodes run, through sudo; the console then logs in
// with a certificate as it does to a node, and forgets the credential.

// hostKeyAlgorithms are the host key types read from a host, preferred
// first, as pinnedKeys orders them.
var hostKeyAlgorithms = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}

// host is the registered host of that name, from the list at hand, which
// every write here and a read every refresh keep; from the storage only
// before the first read, so a node's connection costs no read.
func (s *Service) host(ctx context.Context, cluster, name string) (Host, bool, error) {
	if s.opts.Hosts == nil {
		return Host{}, false, nil
	}
	if h, ok, read := s.opts.Hosts.Known(cluster, name); ok || read {
		return h, ok, nil
	}
	h, ok, err := s.opts.Hosts.Get(ctx, cluster, name)
	if err != nil {
		return Host{}, false, fmt.Errorf("reading the hosts: %w", err)
	}
	return h, ok, nil
}

func hostKeyChanged(name string) error {
	return fmt.Errorf("host %s presented a host key other than the one pinned for it: it was reinstalled, or something sits between; nothing connects to it until a SuperAdmin checks the key and trusts it again from the Nodes tab", name)
}

// hostKeyMismatch marks a host whose key changed, so every connection is
// refused until it is trusted again, and the page says so.
func (s *Service) hostKeyMismatch(cluster, name string, key ssh.PublicKey) {
	fp := ssh.FingerprintSHA256(key)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := s.opts.Hosts.update(ctx, cluster, name, func(h *Host) error {
			h.Mismatch = fp
			return nil
		}); err != nil {
			s.opts.Logger.Warn("marking a host's changed key failed", "host", name, "err", err)
		}
	}()
}

// AddHost registers a host, pending until it is paired.
func (s *Service) AddHost(ctx context.Context, h Host, by string) (Host, error) {
	if s.opts.Hosts == nil {
		return Host{}, errors.New("this console keeps no hosts")
	}
	h.AddedBy, h.AddedAt = by, time.Now().UTC()
	return s.opts.Hosts.Add(ctx, h, s.opts.IsNode)
}

// RestoreHost brings a host back from a backup (Hosts.Restore).
func (s *Service) RestoreHost(ctx context.Context, h Host) (Host, error) {
	if s.opts.Hosts == nil {
		return Host{}, errors.New("this console keeps no hosts")
	}
	return s.opts.Hosts.Restore(ctx, h, s.opts.IsNode)
}

// Hosts lists a cluster's hosts.
func (s *Service) Hosts(ctx context.Context, cluster string) ([]Host, error) {
	if s.opts.Hosts == nil {
		return []Host{}, nil
	}
	return s.opts.Hosts.List(ctx, cluster)
}

// registered is a host that must exist.
func (s *Service) registered(ctx context.Context, cluster, name string) (Host, error) {
	if s.opts.Hosts == nil {
		return Host{}, fmt.Errorf("%w: %s", errNoHost, name)
	}
	h, ok, err := s.opts.Hosts.Get(ctx, cluster, name)
	if err != nil {
		return Host{}, fmt.Errorf("reading the hosts: %w", err)
	}
	if !ok {
		return Host{}, fmt.Errorf("%w: %s", errNoHost, name)
	}
	return h, nil
}

// KeyOffer is what a host showed of its key: the fingerprint, never the
// key, and the command that prints it on the host, to compare.
type KeyOffer struct {
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"keyType"`
	Command     string `json:"command"`
}

// errScanned ends a handshake once the host has shown its key.
var errScanned = errors.New("host key read")

// ScanHost connects to a host with no credential, reads the key it
// presents and keeps it as offered: only that key can be trusted.
func (s *Service) ScanHost(ctx context.Context, cluster, name string) (KeyOffer, error) {
	h, err := s.registered(ctx, cluster, name)
	if err != nil {
		return KeyOffer{}, err
	}
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User:              "221b-host-key",
		HostKeyAlgorithms: hostKeyAlgorithms,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errScanned
		},
	}
	hostPort := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
	c, err := s.dial(ctx, hostPort, cfg)
	if c != nil {
		c.Close()
	}
	if got == nil {
		if err == nil || ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		var ce connectError
		if errors.As(err, &ce) {
			return KeyOffer{}, connectError{unreachableHost(hostPort, err)}
		}
		return KeyOffer{}, fmt.Errorf("%s did not show an SSH host key: %w", hostPort, err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(got)))
	if _, err := s.opts.Hosts.update(ctx, cluster, name, func(x *Host) error {
		x.Offered = line
		return nil
	}); err != nil {
		return KeyOffer{}, err
	}
	s.opts.Logger.Info("host key read", "host", name, "addr", hostPort, "hostKey", got.Type()+" "+ssh.FingerprintSHA256(got))
	return KeyOffer{Fingerprint: ssh.FingerprintSHA256(got), KeyType: got.Type(), Command: keygenCommand(got.Type())}, nil
}

// unreachableHost says why a host's SSH port did not answer, and where to
// look: its sshd, a firewall, and the console's network policy, which lets
// it open SSH connections only inside networkPolicy.nodeCIDRs.
func unreachableHost(hostPort string, err error) error {
	why := "it did not answer"
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		why = "it refused the connection"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		why = "there is no route to it"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		why = "it did not answer in time"
	}
	return fmt.Errorf("could not reach %s: %s. Check that sshd runs there on that port, that no firewall drops the console's connection, and that the address is inside networkPolicy.nodeCIDRs, outside which the console opens no SSH connection", hostPort, why)
}

// keygenCommand prints a host key's fingerprint on the host.
func keygenCommand(keyType string) string {
	file := "ed25519"
	switch {
	case strings.HasPrefix(keyType, "ecdsa-"):
		file = "ecdsa"
	case keyType == ssh.KeyAlgoRSA:
		file = "rsa"
	}
	return "ssh-keygen -lf /etc/ssh/ssh_host_" + file + "_key.pub"
}

// TrustHost pins the key the host offered, when the fingerprint the
// SuperAdmin confirmed is that key's. A paired host keeps its pairing,
// now with this key: trusting again is how a changed key is accepted.
// It says whether a key was pinned before.
func (s *Service) TrustHost(ctx context.Context, cluster, name, fingerprint, by string) (Host, bool, error) {
	again := false
	now := time.Now().UTC()
	h, err := s.opts.Hosts.update(ctx, cluster, name, func(x *Host) error {
		if x.Offered == "" {
			return fmt.Errorf("%w: read the host's key first", ErrInvalid)
		}
		if fp := fingerprintOf(x.Offered); fp == "" || fp != strings.TrimSpace(fingerprint) {
			return fmt.Errorf("%w: the fingerprint is not the one the host showed; read its key again", ErrInvalid)
		}
		again = x.HostKey != ""
		x.HostKey, x.Fingerprint, x.Offered, x.Mismatch = x.Offered, fingerprintOf(x.Offered), "", ""
		x.TrustedBy, x.TrustedAt = by, &now
		return nil
	})
	if err != nil {
		return Host{}, false, err
	}
	if p, ok, err := s.opts.Pairings.Get(ctx, cluster, name); err != nil {
		return h, again, err
	} else if ok {
		p.HostKeys = []string{h.HostKey}
		if err := s.opts.Pairings.Set(ctx, p); err != nil {
			return h, again, err
		}
	}
	return h, again, nil
}

// Credential is what a SuperAdmin gives once to pair a host: a password or
// a private key for an account that may use sudo, and sudo's password when
// it is another. It is never kept, logged, audited or answered: it prints
// as its user name alone, whatever prints it.
type Credential struct {
	User         string `json:"user"`
	Password     string `json:"password,omitempty"`
	PrivateKey   string `json:"privateKey,omitempty"`
	Passphrase   string `json:"passphrase,omitempty"`
	SudoPassword string `json:"sudoPassword,omitempty"`
}

func (c Credential) String() string               { return "credential of " + c.User }
func (c Credential) GoString() string             { return c.String() }
func (c Credential) LogValue() slog.Value         { return slog.StringValue(c.String()) }
func (c Credential) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(c.String())), nil }

// Secrets are the values to blank in anything the session prints.
func (c Credential) Secrets() []string {
	var out []string
	for _, v := range []string{c.Password, c.Passphrase, c.SudoPassword} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Wipe drops the values; the user name stays, for the record.
func (c *Credential) Wipe() { *c = Credential{User: c.User} }

// auth makes the SSH sign-in of a credential. called says whether the
// host asked for a password at all: one that never did takes none.
func (c Credential) auth() ([]ssh.AuthMethod, *bool, error) {
	asked := false
	switch {
	case !toolbox.LoginUserRE.MatchString(c.User):
		return nil, nil, fmt.Errorf("%w: the user is not a Unix user name", ErrInvalid)
	case c.Password != "" && c.PrivateKey != "":
		return nil, nil, fmt.Errorf("%w: give a password or a private key, not both", ErrInvalid)
	case c.PrivateKey != "":
		key := []byte(c.PrivateKey)
		defer clear(key)
		var signer ssh.Signer
		var err error
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		var missing *ssh.PassphraseMissingError
		switch {
		case errors.As(err, &missing):
			return nil, nil, fmt.Errorf("%w: the private key has a passphrase: give it", ErrInvalid)
		case errors.Is(err, x509.IncorrectPasswordError):
			return nil, nil, fmt.Errorf("%w: the passphrase does not open the private key", ErrInvalid)
		case err != nil:
			// Not the parser's words: they could quote the key.
			return nil, nil, fmt.Errorf("%w: the private key is not readable: give it as the file holds it, from -----BEGIN to -----END", ErrInvalid)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, &asked, nil
	case c.Password != "":
		pw := c.Password
		return []ssh.AuthMethod{
			ssh.PasswordCallback(func() (string, error) {
				asked = true
				return pw, nil
			}),
			// Hosts that take passwords through PAM ask this way: every
			// question that hides its answer gets the password.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				asked = true
				answers := make([]string, len(questions))
				for i := range questions {
					if !echos[i] {
						answers[i] = pw
					}
				}
				return answers, nil
			}),
		}, &asked, nil
	}
	return nil, nil, fmt.Errorf("%w: give the account's password or private key", ErrInvalid)
}

// HostSetupScript is what pairs a host: the login user made when missing,
// then the pairing script nodes run (PairScript), as root.
func HostSetupScript(caPublicKey, console, loginUser string, port int) string {
	return strings.Join([]string{
		"set -e",
		"u=" + shellQuote(loginUser),
		`if ! id -u "$u" >/dev/null 2>&1; then`,
		`  if command -v useradd >/dev/null 2>&1; then useradd -m -s /bin/sh "$u"; else adduser -D -s /bin/sh "$u"; fi`,
		`  echo "221B: created the login user $u"`,
		`fi`,
		PairScript(caPublicKey, console, loginUser, port),
	}, "\n")
}

// asRoot is a script as a command to paste into a shell on the host.
func asRoot(script, end string) string {
	return "sudo sh <<'" + end + "'\n" + script + "\n" + end + "\n"
}

// SetupCommand is the command a SuperAdmin runs on the host themselves,
// instead of giving a credential: it does what pairing does.
func (s *Service) SetupCommand(ctx context.Context, cluster, name string) (string, error) {
	h, err := s.registered(ctx, cluster, name)
	if err != nil {
		return "", err
	}
	if err := s.opts.CA.Load(ctx); err != nil {
		return "", err
	}
	return asRoot(HostSetupScript(s.opts.CA.PublicKey(), s.opts.Console, h.LoginUser, h.Port), "221B-SETUP"), nil
}

// PairHost pairs a host with a credential given once: one session, the
// pinned key checked, runs the setup script through sudo, with sudo's
// password on its standard input and never on a command line; then the
// console logs in with a certificate, and the host is paired. The
// credential is wiped whatever happens; the answer is the script's own
// lines, masked, with no host key.
func (s *Service) PairHost(ctx context.Context, cluster, name string, cred *Credential, by string) (Host, []string, error) {
	defer cred.Wipe()
	h, err := s.registered(ctx, cluster, name)
	if err != nil {
		return Host{}, nil, err
	}
	if h.HostKey == "" {
		return Host{}, nil, fmt.Errorf("%w: confirm the host's key first; no credential is sent before", ErrInvalid)
	}
	if h.Mismatch != "" {
		return Host{}, nil, hostKeyChanged(name)
	}
	methods, asked, err := cred.auth()
	if err != nil {
		return Host{}, nil, err
	}
	if err := s.opts.CA.Load(ctx); err != nil {
		return Host{}, nil, err
	}
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.HostKey))
	if err != nil {
		return Host{}, nil, fmt.Errorf("the pinned key of host %s is not readable: read and trust it again", name)
	}
	var mismatch error
	cfg := &ssh.ClientConfig{
		User:              cred.User,
		Auth:              methods,
		HostKeyAlgorithms: algorithmsOf(pinned.Type()),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if key.Type() == pinned.Type() && bytes.Equal(key.Marshal(), pinned.Marshal()) {
				return nil
			}
			s.opts.Logger.Warn("SSH host key mismatch", "host", name, "presented", key.Type()+" "+ssh.FingerprintSHA256(key), "pinned", h.Fingerprint)
			s.hostKeyMismatch(cluster, name, key)
			mismatch = hostKeyChanged(name)
			return mismatch
		},
	}
	hostPort := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
	c, err := s.dial(ctx, hostPort, cfg)
	if err != nil {
		var ce connectError
		switch {
		case mismatch != nil:
			return Host{}, nil, mismatch
		case errors.As(err, &ce):
			return Host{}, nil, err
		case ctx.Err() != nil:
			return Host{}, nil, ctx.Err()
		case cred.Password != "" && !*asked:
			return Host{}, nil, fmt.Errorf("%s takes no password to sign in (PasswordAuthentication no): give a private key instead, or run the setup command yourself", hostPort)
		case strings.Contains(err.Error(), "unable to authenticate"):
			what := "password"
			if cred.PrivateKey != "" {
				what = "key"
			}
			return Host{}, nil, fmt.Errorf("%s refused %s's %s", hostPort, cred.User, what)
		}
		return Host{}, nil, fmt.Errorf("SSH to %s as %s: %w", hostPort, cred.User, err)
	}
	defer c.Close()
	sess, err := c.NewSession()
	if err != nil {
		return Host{}, nil, fmt.Errorf("SSH to %s as %s: %w", hostPort, cred.User, err)
	}
	defer sess.Close()
	script := HostSetupScript(s.opts.CA.PublicKey(), s.opts.Console, h.LoginUser, h.Port)
	command, stdin := "sh -c "+shellQuote(script), ""
	if cred.User != "root" {
		sudoPW := cred.SudoPassword
		if sudoPW == "" {
			sudoPW = cred.Password
		}
		if sudoPW == "" {
			command = "sudo -n -- " + command
		} else {
			// -k: sudo asks even when it remembers the account, so the
			// password is read, never left to the script's shell.
			command = "sudo -S -k -p '' -- " + command
			stdin = sudoPW + "\n"
		}
	}
	var buf syncBuffer
	secrets := cred.Secrets()
	var out io.Writer = &buf
	if s.opts.Mask != nil {
		out = s.opts.Mask(&buf, secrets)
	}
	sess.Stdin = strings.NewReader(stdin)
	sess.Stdout, sess.Stderr = Streams(out)
	exit := 0
	done := make(chan error, 1)
	if err := sess.Start(command); err != nil {
		return Host{}, nil, fmt.Errorf("the setup did not start on %s: %w", hostPort, err)
	}
	go func() { done <- sess.Wait() }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		c.Close()
		<-done
		return Host{}, nil, fmt.Errorf("the setup on %s did not finish in time: %w", hostPort, ctx.Err())
	case err = <-done:
	}
	if f, ok := out.(interface{ Flush() }); ok {
		f.Flush()
	}
	output := masked(buf.String(), secrets)
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exit = exitErr.ExitStatus()
	default:
		return Host{}, steps(output), fmt.Errorf("the connection to %s was lost during the setup: %w", hostPort, err)
	}
	if exit != 0 {
		return Host{}, steps(output), setupFailed(output, exit, cred.User)
	}
	keys, _, err := ParsePairOutput(output)
	if err != nil {
		return Host{}, steps(output), err
	}
	found := false
	for _, k := range keys {
		found = found || fingerprintOf(k) == h.Fingerprint
	}
	if !found {
		return Host{}, steps(output), fmt.Errorf("the host's key files do not hold the key you confirmed: something sits between, or sshd uses keys of its own; nothing was recorded")
	}
	paired, err := s.verifyHost(ctx, h, by)
	return paired, steps(output), err
}

// masked blanks the values in text, once more, whatever the stream masks
// did.
func masked(text string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			text = strings.ReplaceAll(text, v, "••••••")
		}
	}
	return text
}

// steps are the setup's own lines, for the page: never a host key.
func steps(output string) []string {
	out := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "221B: ") {
			out = append(out, hostWords(strings.TrimPrefix(line, "221B: ")))
		}
	}
	return out
}

// hostWords says host where the pairing script, written for nodes, says
// node.
func hostWords(s string) string {
	return strings.NewReplacer("this node", "this host", "the node's", "the host's", "on the node", "on the host").Replace(s)
}

// setupFailed says why the setup failed, in the words that tell what to
// do: sudo that needs a terminal or a second factor, sudo that refused,
// or the step of the script that stopped it.
func setupFailed(output string, exit int, user string) error {
	low := strings.ToLower(output)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("a terminal is required", "must have a tty", "no tty present"):
		return errors.New("sudo on this host needs a terminal (requiretty), which the console does not give: run the setup command yourself")
	case has("verification code", "one-time", "otp", "duo"):
		return errors.New("sudo on this host asks for a second factor, which the console cannot give: run the setup command yourself")
	case has("a password is required"):
		return errors.New("sudo on this host asks for a password: give the sudo password")
	case has("incorrect password", "sorry, try again", "no password was provided"):
		return errors.New("sudo refused the password: check the sudo password, or run the setup command yourself")
	case has("not in the sudoers", "not allowed to run sudo", "may not run sudo", "is not allowed to execute"):
		return fmt.Errorf("sudo refused: %s may not run commands as root on this host; use an account that may, or run the setup command yourself", user)
	case has("sudo: not found", "sudo: command not found"):
		return errors.New("this host has no sudo: sign in as root, or run the setup command yourself")
	}
	var why []string
	for _, l := range steps(output) {
		if !strings.HasPrefix(l, "created the login user") && !strings.HasPrefix(l, "the pairing did not finish") {
			why = append(why, l)
		}
	}
	if len(why) > 0 {
		return fmt.Errorf("the setup stopped on the host (exit %d): %s; the host's SSH configuration is as it was", exit, strings.Join(why, "; "))
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(output), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, markHostKey) {
			last = l
		}
	}
	if last != "" {
		return fmt.Errorf("the setup stopped on the host (exit %d) after: %s", exit, hostWords(strings.TrimPrefix(last, "221B: ")))
	}
	return fmt.Errorf("the setup stopped on the host (exit %d)", exit)
}

// CheckHost checks that the console logs in to a host with a certificate,
// once someone ran the setup command there, and then marks it paired.
func (s *Service) CheckHost(ctx context.Context, cluster, name, by string) (Host, error) {
	h, err := s.registered(ctx, cluster, name)
	if err != nil {
		return Host{}, err
	}
	if h.HostKey == "" {
		return Host{}, fmt.Errorf("%w: confirm the host's key first", ErrInvalid)
	}
	if h.Mismatch != "" {
		return Host{}, hostKeyChanged(name)
	}
	if err := s.opts.CA.Load(ctx); err != nil {
		return Host{}, err
	}
	return s.verifyHost(ctx, h, by)
}

// verifyHost records the host's pairing with its pinned key, logs in with
// a certificate and runs nothing but true; then the host is paired. A
// login that fails puts back the pairing there was.
func (s *Service) verifyHost(ctx context.Context, h Host, by string) (Host, error) {
	before, had, err := s.opts.Pairings.Get(ctx, h.Cluster, h.Name)
	if err != nil {
		return Host{}, err
	}
	now := time.Now().UTC()
	p := Pairing{Cluster: h.Cluster, Node: h.Name, LoginUser: h.LoginUser, Port: h.Port, HostKeys: []string{h.HostKey}, PairedAt: now, PairedBy: by, CAFinger: s.opts.CA.Fingerprint(), Principal: s.Principal()}
	if err := s.opts.Pairings.Set(ctx, p); err != nil {
		return Host{}, err
	}
	// The host's own name comes back with the login, for a host named
	// after its address.
	var named syncBuffer
	code, err := s.Exec(ctx, h.Cluster, h.Name, by+" via 221B host check", "uname -n", false, strings.NewReader(""), &named)
	if err == nil && code != 0 {
		err = fmt.Errorf("uname -n exited with %d", code)
	}
	if err != nil {
		bctx := context.WithoutCancel(ctx)
		if had {
			_ = s.opts.Pairings.Set(bctx, before)
		} else {
			_ = s.opts.Pairings.Delete(bctx, h.Cluster, h.Name)
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return Host{}, fmt.Errorf("%s did not take the console's certificate for %s: run the setup command on it as root, then check again", h.Name, h.LoginUser)
		}
		return Host{}, fmt.Errorf("the console could not log in to %s with its certificate: %s", h.Name, strings.TrimPrefix(err.Error(), ErrUnreachable.Error()+": "))
	}
	x, err := s.opts.Hosts.update(ctx, h.Cluster, h.Name, func(x *Host) error {
		x.State, x.PairedBy, x.PairedAt = HostPaired, by, &now
		return nil
	})
	if err != nil || !x.AutoName {
		return x, err
	}
	hostname, _, _ := strings.Cut(named.String(), "\n")
	return s.takeHostname(ctx, x, hostname), nil
}

// takeHostname names a host that was added under its address after the
// host's own name, read as the console first logged in: lower-cased, as a
// node name, and only when no node or host has it. Its pairing goes with
// it, and the lists of nodes that named it (OnRename). Otherwise it keeps
// the name it has, and stops trying.
func (s *Service) takeHostname(ctx context.Context, h Host, hostname string) Host {
	name := strings.ToLower(strings.TrimSpace(hostname))
	keep := func(why string) Host {
		s.opts.Logger.Info("a host keeps the name of its address", "cluster", h.Cluster, "host", h.Name, "hostname", name, "why", why)
		x, err := s.opts.Hosts.update(ctx, h.Cluster, h.Name, func(x *Host) error {
			x.AutoName = false
			return nil
		})
		if err != nil {
			return h
		}
		return x
	}
	switch {
	case name == h.Name:
		return keep("it is its hostname")
	case name == "" || len(name) > 253 || !hostNameRE.MatchString(name):
		return keep("its hostname is not a node name")
	case s.opts.IsNode != nil && s.opts.IsNode(h.Cluster, name):
		return keep("a node has its hostname")
	}
	x, err := s.opts.Hosts.rename(ctx, h.Cluster, h.Name, name)
	if err != nil {
		return keep(err.Error())
	}
	if p, ok, err := s.opts.Pairings.Get(ctx, h.Cluster, h.Name); err == nil && ok {
		p.Node = name
		if err := s.opts.Pairings.Set(ctx, p); err != nil {
			// Its pairing could not follow: back to its address.
			if _, err := s.opts.Hosts.rename(ctx, h.Cluster, name, h.Name); err == nil {
				return keep("its pairing could not be moved: " + err.Error())
			}
			return x
		}
		_ = s.opts.Pairings.Delete(ctx, h.Cluster, h.Name)
	}
	s.opts.Logger.Info("a host takes its hostname", "cluster", h.Cluster, "from", h.Name, "to", name)
	if s.opts.OnRename != nil {
		s.opts.OnRename(ctx, h.Cluster, h.Name, name)
	}
	return x
}

// CleanupCommand is the command that takes this console's lines off a
// host, to run there as root when it is removed; it makes no authority.
func (s *Service) CleanupCommand(ctx context.Context, cluster, name string) (string, error) {
	if _, err := s.registered(ctx, cluster, name); err != nil {
		return "", err
	}
	return s.cleanup(ctx), nil
}

func (s *Service) cleanup(ctx context.Context) string {
	_ = s.opts.CA.Peek(ctx)
	return asRoot(UnpairScript(s.opts.CA.PublicKey(), s.opts.Console), "221B-CLEANUP")
}

// RemoveHost forgets a host, its pinned key and its pairing, and gives
// the command that takes this console's lines off it.
func (s *Service) RemoveHost(ctx context.Context, cluster, name string) (Host, string, error) {
	h, err := s.registered(ctx, cluster, name)
	if err != nil {
		return Host{}, "", err
	}
	if err := s.opts.Pairings.Delete(ctx, cluster, name); err != nil {
		return Host{}, "", err
	}
	if err := s.opts.Hosts.Delete(ctx, cluster, name); err != nil {
		return Host{}, "", err
	}
	return h, s.cleanup(ctx), nil
}

// syncBuffer takes the two streams of a session at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// LabelHost replaces a registered host's labels (Hosts.SetLabels).
func (s *Service) LabelHost(ctx context.Context, cluster, name string, labels map[string]string) (Host, error) {
	if s.opts.Hosts == nil {
		return Host{}, errors.New("this console keeps no hosts")
	}
	return s.opts.Hosts.SetLabels(ctx, cluster, name, labels)
}
