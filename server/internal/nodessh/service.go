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
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// dialTimeout bounds the TCP connect.
const dialTimeout = 10 * time.Second

// Options configure the service.
type Options struct {
	CA       *CA
	Pairings *Pairings
	// Console names this console among those that may pair one node
	// (ConsoleName); empty is a laptop.
	Console string
	// NodeAddr finds a node's address to dial: its InternalIP.
	NodeAddr func(cluster, node string) (string, error)
	Logger   *slog.Logger
	// CertTTL is how long a session's certificate is valid: it is used
	// once, to connect.
	CertTTL time.Duration
	// Handshake bounds the SSH handshake after the TCP connect.
	Handshake time.Duration
	// Hosts are the machines outside Kubernetes reached by SSH (Phase 12);
	// nil has none. IsNode says whether a cluster has a node of that name,
	// which a host may not take.
	Hosts  *Hosts
	IsNode func(cluster, name string) bool
	// OnRename carries a host's new name, its hostname, to what names it
	// elsewhere: the lists of nodes in the settings. Nil carries it nowhere.
	OnRename func(ctx context.Context, cluster, from, to string)
	// Mask wraps a pairing session's output so the bootstrap credential
	// never reaches it (automation.NewMask); nil masks the whole output
	// once it ended.
	Mask func(w io.Writer, secrets []string) io.Writer
}

// Service implements sessions.SSH.
type Service struct {
	opts Options
}

func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Console == "" {
		o.Console = laptopConsole
	}
	if o.CertTTL <= 0 {
		o.CertTTL = 5 * time.Minute
	}
	if o.Handshake <= 0 {
		o.Handshake = 15 * time.Second
	}
	return &Service{opts: o}
}

// Principal is what this console's new pairings admit.
func (s *Service) Principal() string { return PrincipalOf(s.opts.Console) }

// Fingerprint is the authority's, reading its key first: after a restart
// nothing has loaded it yet, and a pairing compared with an empty
// fingerprint looked stale (seen on 221b, October 30, 2026). With no key
// yet it is empty: one is made by the first pairing, not by looking.
func (s *Service) Fingerprint() string {
	if err := s.opts.CA.Peek(context.Background()); err != nil {
		s.opts.Logger.Warn("the SSH authority's key could not be read", "err", err)
		return ""
	}
	return s.opts.CA.Fingerprint()
}

// AuthorityKey is the authority's private key, PEM, for a backup that
// seals it (ADR-137).
func (s *Service) AuthorityKey(ctx context.Context) ([]byte, error) {
	return s.opts.CA.PrivateKey(ctx)
}

// RestoreAuthority puts back the authority's key from a backup, on a
// console with no authority yet.
func (s *Service) RestoreAuthority(ctx context.Context, pem []byte) error {
	return s.opts.CA.Restore(ctx, pem)
}

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
		port := r.Port
		if port == 0 {
			port = 22
		}
		return PairScript(s.opts.CA.PublicKey(), s.opts.Console, r.LoginUser, port), nil
	case toolbox.ToolSSHUnpair:
		if err := s.opts.CA.Peek(context.Background()); err != nil {
			return "", err
		}
		return UnpairScript(s.opts.CA.PublicKey(), s.opts.Console), nil
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
		return s.opts.Pairings.Set(ctx, Pairing{Cluster: cluster, Node: r.Node, LoginUser: user, Port: port, HostKeys: hostKeys, PairedAt: time.Now().UTC(), PairedBy: by, CAFinger: s.opts.CA.Fingerprint(), Principal: s.Principal()})
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
	who := owner.User
	if who == "" {
		who = owner.Name
	}
	if who == "" {
		who = "console"
	}
	c, p, hostPort, err := s.connect(ctx, cluster, node, who+" via 221B session "+session)
	if err != nil {
		return nil, "", err
	}
	s.opts.Logger.Info("SSH session", "node", node, "addr", hostPort, "user", p.LoginUser, "who", who, "session", session)
	note := fmt.Sprintf("SSH to node %s (%s) as %s: host key verified; certificate for %s, valid %s.", node, hostPort, p.LoginUser, who, s.opts.CertTTL)
	return &term{client: c}, note, nil
}

// connect opens an SSH connection to a paired node with a certificate
// whose key ID is keyID: the pairing's login user and port, the pinned
// host keys. A host is reached at its own address, and not at all while
// it shows another key than the pinned one.
func (s *Service) connect(ctx context.Context, cluster, node, keyID string) (*ssh.Client, Pairing, string, error) {
	p, ok, err := s.opts.Pairings.Get(ctx, cluster, node)
	if err != nil {
		return nil, p, "", fmt.Errorf("reading the pairings: %w", err)
	}
	host, isHost, err := s.host(ctx, cluster, node)
	if err != nil {
		return nil, p, "", err
	}
	if !ok {
		if isHost {
			return nil, p, "", fmt.Errorf("host %s is not paired for SSH: pair it from the Nodes tab", node)
		}
		return nil, p, "", fmt.Errorf("node %s is not paired for SSH: pair it first, from its Tools menu", node)
	}
	if isHost && host.Mismatch != "" {
		return nil, p, "", hostKeyChanged(node)
	}
	if err := s.opts.CA.Load(ctx); err != nil {
		return nil, p, "", err
	}
	if fp := s.opts.CA.Fingerprint(); p.CAFinger != "" && p.CAFinger != fp {
		return nil, p, "", fmt.Errorf("node %s was paired with another authority (%s, now %s): pair it again", node, p.CAFinger, fp)
	}
	if p.Principal == "" {
		return nil, p, "", fmt.Errorf("the pairing of node %s names no principal: pair it again", node)
	}
	pinned, algos, err := pinnedKeys(p)
	if err != nil {
		return nil, p, "", fmt.Errorf("the pinned host keys of node %s are not readable: pair it again", node)
	}
	var addr string
	if isHost {
		addr = host.Address
	} else if addr, err = s.opts.NodeAddr(cluster, node); err != nil {
		return nil, p, "", err
	}
	signer, err := s.opts.CA.Sign(keyID, []string{p.Principal}, s.opts.CertTTL, time.Now())
	if err != nil {
		return nil, p, "", err
	}
	hostPort := net.JoinHostPort(addr, strconv.Itoa(p.Port))
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
			if isHost {
				s.hostKeyMismatch(cluster, node, key)
				mismatch = hostKeyChanged(node)
				return mismatch
			}
			mismatch = fmt.Errorf("node %s presented a host key that is not among those it was paired with: the node was reinstalled, or something sits between; pair it again once you know which", node)
			return mismatch
		},
	}
	c, err := s.dial(ctx, hostPort, cfg)
	if err != nil {
		var ce connectError
		if errors.As(err, &ce) {
			return nil, p, hostPort, err
		}
		if mismatch != nil {
			return nil, p, hostPort, mismatch
		}
		if ctx.Err() != nil {
			return nil, p, hostPort, ctx.Err()
		}
		return nil, p, hostPort, fmt.Errorf("SSH to %s as %s: %w", hostPort, p.LoginUser, err)
	}
	s.opts.Logger.Info("SSH connected", "node", node, "addr", hostPort, "user", p.LoginUser, "hostKey", verified.Type()+" "+ssh.FingerprintSHA256(verified), "keyID", keyID)
	return c, p, hostPort, nil
}

// connectError is a TCP connect that failed: it says so itself.
type connectError struct{ error }

func (e connectError) Unwrap() error { return e.error }

// dial connects and shakes hands. NewClientConn has no deadline of its
// own and does not see ctx: the socket carries both, or a machine that
// takes the connection and says nothing would hold the caller for good.
func (s *Service) dial(ctx context.Context, hostPort string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, connectError{fmt.Errorf("connecting to %s: %w", hostPort, err)}
	}
	_ = conn.SetDeadline(time.Now().Add(s.opts.Handshake))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	c, chans, reqs, err := ssh.NewClientConn(conn, hostPort, cfg)
	if err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	if !stop() {
		c.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// ErrUnreachable wraps a failure to connect to a node, as opposed to a
// command that ran and failed (Automation counts the two apart, AU-08).
var ErrUnreachable = errors.New("unreachable")

// Exec runs one command on a paired node with no terminal (Automation,
// AU-08): stdin goes to the command, its standard output and error go to
// out, and the exit status comes back. asNobody drops to the nobody user
// first, with runuser when the login user is root and sudo otherwise, so
// the command cannot change the node whatever it does. A failure to
// connect wraps ErrUnreachable; a connection lost while the command runs
// is an error with exit -1.
func (s *Service) Exec(ctx context.Context, cluster, node, keyID, command string, asNobody bool, stdin io.Reader, out io.Writer) (int, error) {
	c, p, _, err := s.connect(ctx, cluster, node, keyID)
	if err != nil {
		return -1, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer c.Close()
	sess, err := c.NewSession()
	if err != nil {
		return -1, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer sess.Close()
	if asNobody {
		command = NobodyCommand(p.LoginUser, command)
	}
	sess.Stdin = stdin
	sess.Stdout, sess.Stderr = Streams(out)
	if err := sess.Start(command); err != nil {
		return -1, err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		c.Close()
		<-done
		return -1, ctx.Err()
	case err := <-done:
		var exit *ssh.ExitError
		var missing *ssh.ExitMissingError
		switch {
		case err == nil:
			return 0, nil
		case errors.As(err, &exit):
			return exit.ExitStatus(), nil
		case errors.As(err, &missing):
			return -1, errors.New("the connection to the node was lost")
		}
		return -1, err
	}
}

// NobodyCommand wraps a command so it runs as the nobody user, from /:
// runuser when the login user is root, else sudo, which must allow it.
func NobodyCommand(loginUser, command string) string {
	drop := "sudo -n -u nobody --"
	if loginUser == "root" {
		drop = "runuser -u nobody --"
	}
	return "cd / && exec " + drop + " sh -c " + shellQuote(command)
}

// pinnedKeys parses a pairing's host keys and lists the algorithms the
// client may accept for them, preferred types first.
func pinnedKeys(p Pairing) ([]ssh.PublicKey, []string, error) {
	var keys []ssh.PublicKey
	seen := map[string]bool{}
	var algos []string
	for _, line := range p.HostKeys {
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

// Close drops the connection of a shell that will not be run.
func (t *term) Close() error { return t.client.Close() }

func (t *term) Run(ctx context.Context, stdin io.Reader, out io.Writer, sizes <-chan sessions.Size) error {
	defer t.client.Close()
	sess, err := t.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = stdin
	sess.Stdout, sess.Stderr = Streams(out)
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
			switch {
			case err == nil, errors.As(err, &exit):
				return nil
			case errors.As(err, &missing):
				// sshd always says how a shell ended: no word means the
				// connection went, with the node or the network.
				return errors.New("the connection to the node was lost")
			}
			return err
		}
	}
}

// Streams are the writers a command's output and its errors go to. A
// writer that keeps them apart, as Automation's masking does, gives its
// own for errors; any other gets both, one write at a time, since the SSH
// session copies the two at once.
func Streams(out io.Writer) (stdout, stderr io.Writer) {
	if s, ok := out.(interface{ Stderr() io.Writer }); ok {
		return out, s.Stderr()
	}
	l := &lockedWriter{w: out}
	return l, l
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
