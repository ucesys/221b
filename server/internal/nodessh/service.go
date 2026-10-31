package nodessh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// Options configure the service.
type Options struct {
	CA       *CA
	Pairings *Pairings
	// NodeAddr finds a node's address to dial: its InternalIP.
	NodeAddr func(cluster, node string) (string, error)
	Logger   *slog.Logger
	Now      func() time.Time
	// Dialer opens the TCP connection; nil is net.Dialer with a timeout.
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)
	// CertTTL is how long a session's certificate is valid: it is used
	// once, to connect.
	CertTTL time.Duration
}

// Service implements sessions.SSH.
type Service struct {
	opts Options
}

func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dialer == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		o.Dialer = d.DialContext
	}
	if o.CertTTL <= 0 {
		o.CertTTL = 5 * time.Minute
	}
	return &Service{opts: o}
}

// Load makes the authority's key when there is none yet.
func (s *Service) Load(ctx context.Context) error { return s.opts.CA.Load(ctx) }

// Fingerprint is the authority's.
func (s *Service) Fingerprint() string { return s.opts.CA.Fingerprint() }

// List is a cluster's pairings.
func (s *Service) List(ctx context.Context, cluster string) ([]Pairing, error) {
	return s.opts.Pairings.List(ctx, cluster)
}

// Script is the command a pairing or unpairing session runs on the node.
func (s *Service) Script(r sessions.Request) (string, error) {
	switch r.Tool {
	case toolbox.ToolSSHPair:
		if err := s.opts.CA.Load(context.Background()); err != nil {
			return "", err
		}
		if !toolbox.LoginUserRE.MatchString(r.LoginUser) {
			return "", errors.New("the login user is not a Unix user name")
		}
		return PairScript(s.opts.CA.PublicKey(), r.LoginUser), nil
	case toolbox.ToolSSHUnpair:
		return UnpairScript(), nil
	}
	return "", fmt.Errorf("%s is not an SSH pairing tool", r.Tool)
}

// Done records a pairing from the script's output, or forgets one.
func (s *Service) Done(ctx context.Context, cluster string, r sessions.Request, output, by string) error {
	switch r.Tool {
	case toolbox.ToolSSHPair:
		hostKeys, user, err := ParsePairOutput(output)
		if err != nil {
			return err
		}
		port := r.Port
		if port == 0 {
			port = 22
		}
		return s.opts.Pairings.Set(ctx, Pairing{Cluster: cluster, Node: r.Node, LoginUser: user, Port: port, HostKey: hostKeys[0], HostKeys: hostKeys, PairedAt: s.opts.Now().UTC(), PairedBy: by, CAFinger: s.opts.CA.Fingerprint(), Principal: Principal})
	case toolbox.ToolSSHUnpair:
		if !strings.Contains(output, markUnpaired) {
			return errors.New("the unpairing did not finish on the node")
		}
		return s.opts.Pairings.Delete(ctx, cluster, r.Node)
	}
	return nil
}

// Dial connects to a paired node as the owner: a certificate signed for
// the session, the node's pinned host key, the pairing's login user.
func (s *Service) Dial(ctx context.Context, cluster, node string, owner sessions.Who, session string) (sessions.Term, string, error) {
	p, ok, err := s.opts.Pairings.Get(ctx, cluster, node)
	if err != nil {
		return nil, "", fmt.Errorf("reading the pairings: %w", err)
	}
	if !ok {
		return nil, "", fmt.Errorf("node %s is not paired for SSH: pair it first, from its Tools menu", node)
	}
	if err := s.opts.CA.Load(ctx); err != nil {
		return nil, "", err
	}
	if fp := s.opts.CA.Fingerprint(); p.CAFinger != "" && p.CAFinger != fp {
		return nil, "", fmt.Errorf("node %s was paired with another authority (%s, now %s): pair it again", node, p.CAFinger, fp)
	}
	pinned, algos, err := pinnedKeys(p)
	if err != nil {
		return nil, "", fmt.Errorf("the pinned host keys of node %s are not readable: pair it again", node)
	}
	addr, err := s.opts.NodeAddr(cluster, node)
	if err != nil {
		return nil, "", err
	}
	who := owner.User
	if who == "" {
		who = owner.Name
	}
	if who == "" {
		who = "console"
	}
	signer, err := s.opts.CA.Sign(who+" via 221B session "+session, []string{Principal}, s.opts.CertTTL, s.opts.Now())
	if err != nil {
		return nil, "", err
	}
	hostPort := net.JoinHostPort(addr, strconv.Itoa(p.Port))
	conn, err := s.opts.Dialer(ctx, "tcp", hostPort)
	if err != nil {
		return nil, "", fmt.Errorf("connecting to %s: %w", hostPort, err)
	}
	var mismatch error
	var verified ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: p.LoginUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The node presents one of the key types it was paired with.
		HostKeyAlgorithms: algos,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			for _, k := range pinned {
				if key.Type() == k.Type() && bytes.Equal(key.Marshal(), k.Marshal()) {
					verified = key
					return nil
				}
			}
			// The fingerprints go to the log, not to the page (asked
			// October 30, 2026).
			s.opts.Logger.Warn("SSH host key mismatch", "node", node, "presented", key.Type()+" "+ssh.FingerprintSHA256(key), "paired", fingerprints(pinned))
			mismatch = fmt.Errorf("node %s presented a host key that is not among those it was paired with: the node was reinstalled, or something sits between; pair it again once you know which", node)
			return mismatch
		},
		Timeout: 15 * time.Second,
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, hostPort, cfg)
	if err != nil {
		conn.Close()
		if mismatch != nil {
			return nil, "", mismatch
		}
		return nil, "", fmt.Errorf("SSH to %s as %s: %w", hostPort, p.LoginUser, err)
	}
	s.opts.Logger.Info("SSH session", "node", node, "addr", hostPort, "user", p.LoginUser, "hostKey", verified.Type()+" "+ssh.FingerprintSHA256(verified), "who", who, "session", session)
	note := fmt.Sprintf("SSH to node %s (%s) as %s: host key verified; certificate for %s, valid %s.", node, hostPort, p.LoginUser, who, s.opts.CertTTL)
	return &term{client: ssh.NewClient(c, chans, reqs)}, note, nil
}

// pinnedKeys parses a pairing's host keys and lists the algorithms the
// client may accept for them, preferred types first.
func pinnedKeys(p Pairing) ([]ssh.PublicKey, []string, error) {
	var keys []ssh.PublicKey
	seen := map[string]bool{}
	var algos []string
	for _, line := range p.Keys() {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, nil, err
		}
		keys = append(keys, k)
		for _, a := range algorithmsOf(k.Type()) {
			if !seen[a] {
				seen[a] = true
				algos = append(algos, a)
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil, errors.New("no host key")
	}
	sort.SliceStable(algos, func(i, j int) bool { return rank(algos[i]) < rank(algos[j]) })
	return keys, algos, nil
}

// algorithmsOf names the signature algorithms a host key type is used
// with: an RSA key signs with SHA-2 today.
func algorithmsOf(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

func rank(algo string) int {
	for i, a := range []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA} {
		if a == algo {
			return i
		}
	}
	return 100
}

func fingerprints(keys []ssh.PublicKey) string {
	var out []string
	for _, k := range keys {
		out = append(out, k.Type()+" "+ssh.FingerprintSHA256(k))
	}
	return strings.Join(out, ", ")
}

// term is one SSH shell.
type term struct {
	client *ssh.Client
}

func (t *term) Run(ctx context.Context, stdin io.Reader, out io.Writer, sizes <-chan sessions.Size) error {
	defer t.client.Close()
	sess, err := t.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin, sess.Stdout, sess.Stderr = stdin, out, out
	cols, rows := 80, 24
	select {
	case sz := <-sizes:
		if sz.Cols > 0 && sz.Rows > 0 {
			cols, rows = int(sz.Cols), int(sz.Rows)
		}
	default:
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		return fmt.Errorf("the node refused a terminal: %w", err)
	}
	if err := sess.Shell(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	for {
		select {
		case sz, ok := <-sizes:
			if ok && sz.Cols > 0 && sz.Rows > 0 {
				_ = sess.WindowChange(int(sz.Rows), int(sz.Cols))
			}
		case <-ctx.Done():
			_ = sess.Signal(ssh.SIGHUP)
			sess.Close()
			return ctx.Err()
		case err := <-done:
			var exit *ssh.ExitError
			var missing *ssh.ExitMissingError
			if err == nil || errors.As(err, &exit) || errors.As(err, &missing) {
				return nil
			}
			return err
		}
	}
}
