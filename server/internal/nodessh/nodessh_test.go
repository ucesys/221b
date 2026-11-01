package nodessh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// The authority's key is made once and kept; a certificate it signs
// carries the principal and the key ID and passes a checker that trusts
// the authority (ADR-72).
func TestTheAuthorityIsMadeOnceAndSigns(t *testing.T) {
	ctx := context.Background()
	store := kube.NewSecretKeyFor(fake.NewClientset(), "221b", "x-ssh-ca", nil)
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
	principal := PrincipalOf("a.b")
	signer, err := ca.Sign("pat via 221B session s1", []string{principal}, 5*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, ok := signer.PublicKey().(*ssh.Certificate)
	if !ok {
		t.Fatal("not a certificate")
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pub))
	checker := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), caPub.Marshal()) }, Clock: func() time.Time { return now.Add(time.Minute) }}
	if err := checker.CheckCert(principal, cert); err != nil {
		t.Fatalf("the certificate does not pass: %v", err)
	}
	if err := checker.CheckCert("someone-else", cert); err == nil {
		t.Fatal("another principal passed")
	}
	checker.Clock = func() time.Time { return now.Add(time.Hour) }
	if err := checker.CheckCert(principal, cert); err == nil {
		t.Fatal("an expired certificate passed")
	}
	if cert.KeyId != "pat via 221B session s1" {
		t.Fatalf("key id %q", cert.KeyId)
	}
}

// Two replicas that find no key at once end up with one: the second's
// write fails, and it reads the first's rather than keep a key of its own
// that would make its pairings fail after a restart.
func TestTwoReplicasMakeOneAuthority(t *testing.T) {
	ctx := context.Background()
	theirs, signer, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset()
	store := kube.NewSecretKeyFor(cs, "221b", "x-ssh-ca", nil)
	var creates atomic.Int32
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		creates.Add(1)
		first := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "221b", Name: "x-ssh-ca"}, Data: map[string][]byte{keyPrivate: theirs}}
		if err := cs.Tracker().Add(first); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "x-ssh-ca")
	})
	ca := NewCA(store)
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if ca.Fingerprint() != ssh.FingerprintSHA256(signer.PublicKey()) || creates.Load() != 1 {
		t.Fatalf("the replica keeps %s after %d writes", ca.Fingerprint(), creates.Load())
	}
	if saved, _, _, _ := store.LoadKey(ctx, keyPrivate); !bytes.Equal(saved, theirs) {
		t.Fatal("the Secret's key was replaced")
	}
}

// With no Secret, on a laptop, the key is made and kept in memory.
func TestALaptopKeepsTheAuthorityInMemory(t *testing.T) {
	ca := NewCA(nil)
	if err := ca.Peek(context.Background()); err != nil || ca.Fingerprint() != "" {
		t.Fatalf("looking: %v %q", err, ca.Fingerprint())
	}
	if err := ca.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	fp := ca.Fingerprint()
	if err := ca.Load(context.Background()); err != nil || fp == "" || ca.Fingerprint() != fp {
		t.Fatalf("the key changed: %v %q %q", err, fp, ca.Fingerprint())
	}
}

// The private key is standard PEM, as every other the server writes.
func TestTheSavedKeyIsPEM(t *testing.T) {
	encoded, _, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	if block, rest := pem.Decode(encoded); block == nil || block.Type != "OPENSSH PRIVATE KEY" || len(rest) != 0 {
		t.Fatalf("the key is %q", encoded)
	}
	if _, err := ssh.ParsePrivateKey(encoded); err != nil {
		t.Fatal(err)
	}
}

// A console's name keeps two consoles apart however their dashes fall,
// and nothing in it needs quoting for sshd.
func TestAConsolesNameIsItsOwn(t *testing.T) {
	if ConsoleName("a-b", "c") == ConsoleName("a", "b-c") {
		t.Fatal("two consoles share a name")
	}
	if got := ConsoleName("221b", "k8s-console-221b"); got != "221b.k8s-console-221b" {
		t.Fatalf("the name is %q", got)
	}
	if got := PrincipalOf(ConsoleName("a b", "c'd")); got != "221b-superadmins-a-b.c-d" {
		t.Fatalf("the principal is %q", got)
	}
}

// The pairing script carries the authority's key, the principal and the
// login user, quoted for sh; its output yields the host key and the user.
func TestThePairingScriptAndItsOutput(t *testing.T) {
	ca := NewCA(nil)
	_ = ca.Load(context.Background())
	sc := PairScript(ca.PublicKey(), "a.b", "ops", 22)
	key := strings.TrimSuffix(ca.PublicKey(), " "+caComment)
	for _, want := range []string{"u='ops'", "id -u \"$u\"", "k='" + key + "'", "t='221b-console=a.b'", "p='" + PrincipalOf("a.b") + "'", "TrustedUserCAKeys /etc/ssh/221b/ca.pub", "AuthorizedPrincipalsFile /etc/ssh/221b/principals-%u", "-t", "221B-HOSTKEY", "221B-PAIRED"} {
		if !strings.Contains(sc, want) {
			t.Fatalf("the script lacks %q:\n%s", want, sc)
		}
	}
	if un := UnpairScript(ca.PublicKey(), "a.b"); !strings.Contains(un, "rm -f /etc/ssh/sshd_config.d/221b.conf") || !strings.Contains(un, markUnpaired) {
		t.Fatal("the unpair script")
	}
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecPub, _ := ssh.NewPublicKey(&ecKey.PublicKey)
	hk, user, err := ParsePairOutput("some noise\n221B-HOSTKEY ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe\n221B-HOSTKEY " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ecPub))) + "\n221B-PAIRED ops\n")
	if err != nil || user != "ops" || len(hk) != 2 || !strings.HasPrefix(hk[0], "ssh-ed25519 AAAA") || !strings.HasPrefix(hk[1], "ecdsa-sha2-nistp256 ") {
		t.Fatalf("parsed %q %q %v", hk, user, err)
	}
	if _, _, err := ParsePairOutput("221B-HOSTKEY ssh-ed25519 not-a-key\n221B-PAIRED ops\n"); err == nil {
		t.Fatal("an unreadable host key passed")
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
	ln        net.Listener
	hostKey   ssh.Signer
	caPub     ssh.PublicKey
	user      string
	principal string
	mu        sync.Mutex
	received  []byte
	commands  []string
}

func startNode(t *testing.T, caPub ssh.PublicKey, user string) *fakeNode {
	t.Helper()
	hostCA := NewCA(nil)
	_ = hostCA.Load(context.Background())
	n := &fakeNode{hostKey: hostCA.signer, caPub: caPub, user: user, principal: PrincipalOf(laptopConsole)}
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
		if err := checker.CheckCert(n.principal, cert); err != nil {
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
		// An exec request (Automation, AU-08) names its command; its
		// stdin is read to the end, then the node answers and exits,
		// with 7 for a command that says fail.
		var cmdMu sync.Mutex
		command := ""
		go func() {
			for r := range creqs {
				switch r.Type {
				case "pty-req", "shell", "window-change":
					r.Reply(true, nil)
					if r.Type == "shell" {
						io.WriteString(ch, "node$ ")
					}
				case "exec":
					var p struct{ Command string }
					_ = ssh.Unmarshal(r.Payload, &p)
					cmdMu.Lock()
					command = p.Command
					cmdMu.Unlock()
					n.mu.Lock()
					n.commands = append(n.commands, p.Command)
					n.mu.Unlock()
					r.Reply(true, nil)
				default:
					r.Reply(false, nil)
				}
			}
		}()
		buf := make([]byte, 64)
		var stdin []byte
		for {
			k, err := ch.Read(buf)
			cmdMu.Lock()
			cmd := command
			cmdMu.Unlock()
			if cmd != "" {
				stdin = append(stdin, buf[:k]...)
				if err != nil {
					io.WriteString(ch, "ran: "+cmd+"\nstdin: "+string(stdin)+"\n")
					status := uint32(0)
					if strings.Contains(cmd, "fail") {
						status = 7
					}
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					ch.Close()
					return
				}
				continue
			}
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
	ca := NewCA(nil)
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
	if !strings.Contains(note, "as ops") || !strings.Contains(note, "host key verified") || strings.Contains(note, "SHA256:") {
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
	if _, _, err := svc.Dial(ctx, "local", "worker-1", owner, "s2"); err == nil || !strings.Contains(err.Error(), "not among those it was paired with") || strings.Contains(err.Error(), "SHA256:") {
		t.Fatalf("a changed host key: %v", err)
	}
	// Paired with several keys, one of them the node's: accepted.
	if err := svc.Done(ctx, "local", req, "221B-HOSTKEY "+node.hostKeyLine()+"\n221B-HOSTKEY "+other.hostKeyLine()+"\n221B-PAIRED ops\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	if _, note, err := svc.Dial(ctx, "local", "worker-1", owner, "s3"); err != nil || !strings.Contains(note, "host key verified") {
		t.Fatalf("several pinned keys: %v %s", err, note)
	}
	// Unpaired: gone.
	if err := svc.Done(ctx, "local", sessions.Request{Tool: toolbox.ToolSSHUnpair, Node: "worker-1"}, "221B-UNPAIRED\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.List(ctx, "local"); len(list) != 0 {
		t.Fatalf("still paired: %+v", list)
	}
}

// A pairing made with the old shared principal keeps working, its record
// saying what to sign for, until the node is paired again; then it uses
// the console's own.
func TestAnOlderPairingKeepsItsPrincipalUntilPairedAgain(t *testing.T) {
	ctx := context.Background()
	ca := NewCA(nil)
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(ca.PublicKey()))
	node := startNode(t, caPub, "ops")
	node.principal = oldPrincipal
	pairings := NewPairings(&kv.Memory{})
	port := node.ln.Addr().(*net.TCPAddr).Port
	svc := New(Options{CA: ca, Pairings: pairings, Console: "a.b", NodeAddr: func(string, string) (string, error) { return "127.0.0.1", nil }})
	if err := pairings.Set(ctx, Pairing{Cluster: "local", Node: "n", LoginUser: "ops", Port: port, HostKeys: []string{node.hostKeyLine()}, CAFinger: ca.Fingerprint(), Principal: oldPrincipal}); err != nil {
		t.Fatal(err)
	}
	if term, _, err := svc.Dial(ctx, "local", "n", sessions.Who{User: "pat"}, "s1"); err != nil {
		t.Fatalf("the older pairing: %v", err)
	} else {
		term.Close()
	}
	req := sessions.Request{Tool: toolbox.ToolSSHPair, Node: "n", LoginUser: "ops", Port: port}
	if err := svc.Done(ctx, "local", req, "221B-HOSTKEY "+node.hostKeyLine()+"\n221B-PAIRED ops\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := pairings.Get(ctx, "local", "n"); got.Principal != PrincipalOf("a.b") || got.Principal != svc.Principal() {
		t.Fatalf("the new pairing: %+v", got)
	}
	if _, _, err := svc.Dial(ctx, "local", "n", sessions.Who{User: "pat"}, "s2"); err == nil {
		t.Fatal("the certificate carried the old principal after pairing again")
	}
	node.principal = PrincipalOf("a.b")
	if term, _, err := svc.Dial(ctx, "local", "n", sessions.Who{User: "pat"}, "s3"); err != nil {
		t.Fatalf("the new pairing: %v", err)
	} else {
		term.Close()
	}
}

// A record with no host keys, or the single key of the branch's first
// builds, or no principal, is not guessed at: the node is paired again.
// An empty principal list would make a certificate good for any name.
func TestARecordThatCannotBeUsedSaysPairItAgain(t *testing.T) {
	ctx := context.Background()
	ca := NewCA(nil)
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(ca.PublicKey(), " "+caComment)
	for name, record := range map[string]string{
		"only hostKey": `{"cluster":"local","node":"n","loginUser":"ops","port":22,"hostKey":"` + line + `","principal":"221b-superadmins"}`,
		"no principal": `{"cluster":"local","node":"n","loginUser":"ops","port":22,"hostKeys":["` + line + `"]}`,
	} {
		b := &kv.Memory{}
		if _, err := b.SaveKey(ctx, PairingsKey, []byte(`{"local/n":`+record+`}`), ""); err != nil {
			t.Fatal(err)
		}
		svc := New(Options{CA: ca, Pairings: NewPairings(b), NodeAddr: func(string, string) (string, error) { return "127.0.0.1", nil }})
		if _, _, err := svc.Dial(ctx, "local", "n", sessions.Who{User: "pat"}, "s1"); err == nil || !strings.Contains(err.Error(), "pair it again") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The service's scripts carry this console, and unpairing makes no key.
func TestTheScriptsCarryTheConsole(t *testing.T) {
	svc := New(Options{CA: NewCA(nil), Pairings: NewPairings(&kv.Memory{}), Console: "a.b"})
	un, err := svc.Script(sessions.Request{Tool: toolbox.ToolSSHUnpair, Node: "n"})
	if err != nil || !strings.Contains(un, "p='"+PrincipalOf("a.b")+"'") || svc.Fingerprint() != "" {
		t.Fatalf("the unpairing: %v %q", err, svc.Fingerprint())
	}
	pair, err := svc.Script(sessions.Request{Tool: toolbox.ToolSSHPair, Node: "n", LoginUser: "ops"})
	if err != nil || !strings.Contains(pair, "t='221b-console=a.b'") || svc.Fingerprint() == "" {
		t.Fatalf("the pairing: %v %q", err, svc.Fingerprint())
	}
	if PrincipalOf("a.b") != svc.Principal() {
		t.Fatalf("the principal is %q", svc.Principal())
	}
	if got := New(Options{CA: NewCA(nil)}).Principal(); got != PrincipalOf(laptopConsole) {
		t.Fatalf("on a laptop: %q", got)
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
