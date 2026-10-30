package nodessh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// The authority's key is made once and kept; a certificate it signs
// carries the principal and the key ID and passes a checker that trusts
// the authority (ADR-72).
func TestTheAuthorityIsMadeOnceAndSigns(t *testing.T) {
	ctx := context.Background()
	store := &MemoryKeys{}
	ca := NewCA(store)
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	pub, fp := ca.PublicKey(), ca.Fingerprint()
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " 221B SSH CA") || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("the public key: %q %q", pub, fp)
	}
	again := NewCA(store)
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if again.PublicKey() != pub {
		t.Fatal("a second load made another key")
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	signer, err := ca.Sign("pat via 221B session s1", []string{Principal}, 5*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, ok := signer.PublicKey().(*ssh.Certificate)
	if !ok {
		t.Fatal("not a certificate")
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pub))
	checker := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), caPub.Marshal()) }, Clock: func() time.Time { return now.Add(time.Minute) }}
	if err := checker.CheckCert(Principal, cert); err != nil {
		t.Fatalf("the certificate does not pass: %v", err)
	}
	if err := checker.CheckCert("someone-else", cert); err == nil {
		t.Fatal("another principal passed")
	}
	checker.Clock = func() time.Time { return now.Add(time.Hour) }
	if err := checker.CheckCert(Principal, cert); err == nil {
		t.Fatal("an expired certificate passed")
	}
	if cert.KeyId != "pat via 221B session s1" {
		t.Fatalf("key id %q", cert.KeyId)
	}
}

// The pairing script carries the authority's key, the principal and the
// login user, quoted for sh; its output yields the host key and the user.
func TestThePairingScriptAndItsOutput(t *testing.T) {
	ca := NewCA(&MemoryKeys{})
	_ = ca.Load(context.Background())
	sc := PairScript(ca.PublicKey(), "ops")
	for _, want := range []string{"u='ops'", "id -u \"$u\"", "'" + ca.PublicKey() + "'", "'" + Principal + "'", "TrustedUserCAKeys /etc/ssh/221b/ca.pub", "AuthorizedPrincipalsFile /etc/ssh/221b/principals-%u", "-t", "221B-HOSTKEY", "221B-PAIRED"} {
		if !strings.Contains(sc, want) {
			t.Fatalf("the script lacks %q:\n%s", want, sc)
		}
	}
	if !strings.Contains(UnpairScript(), "rm -f /etc/ssh/sshd_config.d/221b.conf") || !strings.Contains(UnpairScript(), markUnpaired) {
		t.Fatal("the unpair script")
	}
	hk, user, err := ParsePairOutput("some noise\n221B-HOSTKEY ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe\n221B-PAIRED ops\n")
	if err != nil || user != "ops" || !strings.HasPrefix(hk, "ssh-ed25519 AAAA") {
		t.Fatalf("parsed %q %q %v", hk, user, err)
	}
	if _, _, err := ParsePairOutput("221B-PAIRED ops\n"); err == nil {
		t.Fatal("no host key passed")
	}
	if _, _, err := ParsePairOutput("221B-HOSTKEY ssh-ed25519 AAAA\n"); err == nil {
		t.Fatal("no paired line passed")
	}
}

// sshd stands in for a node: it trusts the authority, admits the
// principal as the login user, and shows its host key.
type fakeNode struct {
	ln       net.Listener
	hostKey  ssh.Signer
	caPub    ssh.PublicKey
	user     string
	mu       sync.Mutex
	received []byte
}

func startNode(t *testing.T, caPub ssh.PublicKey, user string) *fakeNode {
	t.Helper()
	hostCA := NewCA(&MemoryKeys{})
	_ = hostCA.Load(context.Background())
	n := &fakeNode{hostKey: hostCA.signer, caPub: caPub, user: user}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n.ln = ln
	t.Cleanup(func() { ln.Close() })
	// As sshd with AuthorizedPrincipalsFile: the certificate's principal
	// is checked against the file's, not against the login user.
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		cert, ok := key.(*ssh.Certificate)
		if !ok {
			return nil, errors.New("certificates only")
		}
		if c.User() != n.user {
			return nil, errors.New("no such user")
		}
		checker := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), n.caPub.Marshal()) }}
		if err := checker.CheckCert(Principal, cert); err != nil {
			return nil, err
		}
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(n.hostKey)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serve(c, cfg)
		}
	}()
	return n
}

func (n *fakeNode) serve(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	if conn.User() != n.user {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for r := range creqs {
				switch r.Type {
				case "pty-req", "shell", "window-change":
					r.Reply(true, nil)
					if r.Type == "shell" {
						io.WriteString(ch, "node$ ")
					}
				default:
					r.Reply(false, nil)
				}
			}
		}()
		buf := make([]byte, 64)
		for {
			k, err := ch.Read(buf)
			if k > 0 {
				n.mu.Lock()
				n.received = append(n.received, buf[:k]...)
				n.mu.Unlock()
				ch.Write(bytes.ToUpper(buf[:k]))
				if bytes.Contains(buf[:k], []byte("exit")) {
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					ch.Close()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
}

func (n *fakeNode) hostKeyLine() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(n.hostKey.PublicKey())))
}

// A session to a paired node: the certificate admits the person as the
// login user, the host key is checked against the pinned one, the
// terminal runs; a node with another host key is refused, and an
// unpaired node is not dialled at all.
func TestASessionRunsOverACertificateAndAPinnedHostKey(t *testing.T) {
	ctx := context.Background()
	ca := NewCA(&MemoryKeys{})
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(ca.PublicKey()))
	node := startNode(t, caPub, "ops")
	pairings := NewPairings(&kv.Memory{})
	addr := node.ln.Addr().(*net.TCPAddr)
	svc := New(Options{CA: ca, Pairings: pairings, NodeAddr: func(cluster, n string) (string, error) { return "127.0.0.1", nil }, CertTTL: time.Minute})
	owner := sessions.Who{User: "pat", Name: "Pat"}

	if _, _, err := svc.Dial(ctx, "local", "worker-1", owner, "s0"); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("an unpaired node: %v", err)
	}
	// Paired, as the pairing command's output would record it.
	req := sessions.Request{Tool: toolbox.ToolSSHPair, Node: "worker-1", LoginUser: "ops", Port: addr.Port}
	if err := svc.Done(ctx, "local", req, "221B-HOSTKEY "+node.hostKeyLine()+"\n221B-PAIRED ops\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.List(ctx, "local")
	if len(list) != 1 || list[0].LoginUser != "ops" || list[0].Port != addr.Port || list[0].PairedBy != "Pat" || list[0].CAFinger != ca.Fingerprint() {
		t.Fatalf("the pairing: %+v", list)
	}
	term, note, err := svc.Dial(ctx, "local", "worker-1", owner, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "as ops") || !strings.Contains(note, "host key SHA256:") {
		t.Fatalf("the note: %s", note)
	}
	pr, pw := io.Pipe()
	var out safeBuffer
	sizes := make(chan sessions.Size, 2)
	sizes <- sessions.Size{Cols: 120, Rows: 40}
	done := make(chan error, 1)
	go func() { done <- term.Run(ctx, pr, &out, sizes) }()
	time.Sleep(200 * time.Millisecond)
	io.WriteString(pw, "hello\n")
	time.Sleep(200 * time.Millisecond)
	io.WriteString(pw, "exit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the shell: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shell did not end")
	}
	if got := out.String(); !strings.Contains(got, "node$ ") || !strings.Contains(got, "HELLO") {
		t.Fatalf("the terminal saw %q", got)
	}
	node.mu.Lock()
	received := string(node.received)
	node.mu.Unlock()
	if !strings.Contains(received, "hello") {
		t.Fatalf("the node received %q", received)
	}

	// Another host key: refused before any login.
	other := startNode(t, caPub, "ops")
	req.Port = other.ln.Addr().(*net.TCPAddr).Port
	if err := svc.Done(ctx, "local", req, "221B-HOSTKEY "+node.hostKeyLine()+"\n221B-PAIRED ops\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Dial(ctx, "local", "worker-1", owner, "s2"); err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("a changed host key: %v", err)
	}
	// Unpaired: gone.
	if err := svc.Done(ctx, "local", sessions.Request{Tool: toolbox.ToolSSHUnpair, Node: "worker-1"}, "221B-UNPAIRED\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.List(ctx, "local"); len(list) != 0 {
		t.Fatalf("still paired: %+v", list)
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
