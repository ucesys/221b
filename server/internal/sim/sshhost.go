package sim

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// A host outside Kubernetes (Phase 12) against the simulator: an SSH
// server that a console can add, read the key of, pair with a password
// through sudo, and then run Automation's commands on with a certificate.
// It runs nothing: the pairing script is read for the console's key,
// principal and login user, and the rest answers as a host would.

// HostUser and HostPassword are the bootstrap account the fake host takes;
// sudo takes the same password.
const (
	HostUser     = "admin"
	HostPassword = "host-password"
)

// SSHHost is the fake host.
type SSHHost struct {
	key ssh.Signer
	mu  sync.Mutex
	// trusted are the consoles' keys the pairing put in place, with the
	// principal each admits for the login user.
	trusted map[string]string
	login   string
	boot    int
}

// NewSSHHost makes a host with a new host key.
func NewSSHHost() (*SSHHost, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	return &SSHHost{key: signer, trusted: map[string]string{}, boot: 1}, nil
}

// Fingerprint is the host key's, as ssh-keygen -l prints it.
func (h *SSHHost) Fingerprint() string { return ssh.FingerprintSHA256(h.key.PublicKey()) }

// Serve answers SSH on the listener until it closes.
func (h *SSHHost) Serve(ln net.Listener) error {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == HostUser && string(pw) == HostPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("no")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			cert, ok := key.(*ssh.Certificate)
			if !ok {
				return nil, errors.New("certificates only")
			}
			h.mu.Lock()
			principal, trusted := h.trusted[string(cert.SignatureKey.Marshal())]
			login := h.login
			h.mu.Unlock()
			if !trusted || c.User() != login {
				return nil, errors.New("not admitted")
			}
			checker := ssh.CertChecker{IsUserAuthority: func(ssh.PublicKey) bool { return true }}
			if err := checker.CheckCert(principal, cert); err != nil {
				return nil, err
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(h.key)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go h.serve(c, cfg)
	}
}

func (h *SSHHost) serve(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "sessions only")
			continue
		}
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
				status := h.exec(p.Command, ch)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
				return
			}
		}()
	}
}

var (
	scriptKey       = regexp.MustCompile(`(?m)^k='([^']*)'$`)
	scriptPrincipal = regexp.MustCompile(`(?m)^p='([^']*)'$`)
	scriptLogin     = regexp.MustCompile(`(?m)^u='([^']*)'$`)
)

// exec answers one command as the host would.
func (h *SSHHost) exec(command string, ch ssh.Channel) int {
	in := bufio.NewReader(ch)
	switch {
	case strings.Contains(command, "221b-console="):
		// The pairing: sudo reads its password first, then the script runs.
		if strings.HasPrefix(command, "sudo -S") {
			line, _ := in.ReadString('\n')
			if strings.TrimSuffix(line, "\n") != HostPassword {
				io.WriteString(ch.Stderr(), "Sorry, try again.\nsudo: 1 incorrect password attempt\n")
				return 1
			}
		}
		// The script is one argument of sh -c, quoted for sh.
		script := strings.ReplaceAll(command, `'\''`, `'`)
		k, p, u := scriptKey.FindStringSubmatch(script), scriptPrincipal.FindStringSubmatch(script), scriptLogin.FindStringSubmatch(script)
		if k == nil || p == nil || u == nil {
			io.WriteString(ch, "221B: this is not the pairing script the fake host knows\n")
			return 5
		}
		ca, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k[1]))
		if err != nil {
			io.WriteString(ch, "221B: the console's key is not readable\n")
			return 5
		}
		h.mu.Lock()
		h.trusted[string(ca.Marshal())] = p[1]
		h.login = u[1]
		h.mu.Unlock()
		fmt.Fprintf(ch, "221B: no sshd is running now; the next one to start reads this configuration\n221B-HOSTKEY %s\n221B-PAIRED %s\n", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(h.key.PublicKey()))), u[1])
		return 0
	case strings.Contains(command, "boot_id"):
		h.mu.Lock()
		fmt.Fprintf(ch, "fake-host-boot-%d\n", h.boot)
		h.mu.Unlock()
		return 0
	case strings.Contains(command, "reboot"):
		h.mu.Lock()
		h.boot++
		h.mu.Unlock()
		return 0
	case command == "true":
		return 0
	case command == "uname -n":
		io.WriteString(ch, "fake-host\n")
		return 0
	}
	// A script on standard input: the facts, or a run's.
	script, _ := io.ReadAll(in)
	if bytes.Contains(script, []byte("one os.name")) {
		io.WriteString(ch, "os.name=Debian GNU/Linux 13 (trixie)\nos.id=debian\nos.family=debian\nos.arch=x86_64\npkg.manager=apt-get\ncpu.cores=4\nnet.hostname=fake-host\n")
		return 0
	}
	fmt.Fprintf(ch, "ran %d bytes on the fake host\n{\"changed\": false, \"msg\": \"fine\"}\n", len(script))
	return 0
}
