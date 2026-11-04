package nodessh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sessions"
)

// An unreadable record is an error, never an empty list: an empty list
// passes the conditional save, and the next pairing would wipe the others.
func TestAnUnreadableRecordIsNeverOverwritten(t *testing.T) {
	ctx := context.Background()
	b := &kv.Memory{}
	if _, err := b.SaveKey(ctx, PairingsKey, []byte(`{"local/a":{"node":"a",}}`), ""); err != nil {
		t.Fatal(err)
	}
	p := NewPairings(b)
	if _, err := p.List(ctx, "local"); err == nil {
		t.Fatal("an unreadable record listed as empty")
	}
	if err := p.Set(ctx, Pairing{Cluster: "local", Node: "b"}); err == nil {
		t.Fatal("a save went over an unreadable record")
	}
	if got, _, _ := b.LoadKey(ctx, PairingsKey); !strings.Contains(string(got), `"node":"a"`) {
		t.Fatalf("the record is now %s", got)
	}
}

// conflicting answers ErrConflict a number of times, as other writers of
// the ConfigMap do when many sessions of one card end together.
type conflicting struct {
	kv.Backend
	left atomic.Int32
}

func (c *conflicting) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if c.left.Add(-1) >= 0 {
		return "", kv.ErrConflict
	}
	return c.Backend.SaveKey(ctx, key, b, version)
}

func TestAPairingOutlastsOtherWritersOfTheConfigMap(t *testing.T) {
	c := &conflicting{Backend: &kv.Memory{}}
	c.left.Store(changeTries - 1)
	if err := NewPairings(c).Set(context.Background(), Pairing{Cluster: "local", Node: "a"}); err != nil {
		t.Fatalf("a save that a later try would land: %v", err)
	}
	c.left.Store(1000)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := NewPairings(c).Set(ctx, Pairing{Cluster: "local", Node: "b"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the tries ignored the context: %v", err)
	}
}

// Looking makes no authority: only a pairing does.
func TestLookingMakesNoAuthority(t *testing.T) {
	ctx := context.Background()
	store := kube.NewSecretKeyFor(fake.NewClientset(), "221b", "x-ssh-ca", nil)
	ca := NewCA(store)
	svc := New(Options{CA: ca, Pairings: NewPairings(&kv.Memory{})})
	if fp := svc.Fingerprint(); fp != "" {
		t.Fatalf("a fingerprint with no key: %q", fp)
	}
	if _, version, found, _ := store.LoadKey(ctx, keyPrivate); found || version != kube.NoSecret {
		t.Fatal("looking made a key")
	}
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.Fingerprint() == "" {
		t.Fatal("no fingerprint after the key was made")
	}
}

// One stalled read of the key holds up nobody beyond their own deadline.
func TestAStalledKeyReadHoldsUpNobody(t *testing.T) {
	cs := fake.NewClientset()
	release := make(chan struct{})
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return true, nil, errors.New("the API server is away")
	})
	ca := NewCA(kube.NewSecretKeyFor(cs, "221b", "x-ssh-ca", nil))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = ca.Load(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	ctx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- ca.Peek(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting for the key: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled read held another caller past its deadline")
	}
	close(release)
	wg.Wait()
}

// silentNode takes the TCP connection and never speaks, as a wedged sshd
// behind a live kernel does.
func silentNode(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().(*net.TCPAddr)
}

// The handshake has a deadline and follows the session's context: neither
// a silent node nor pressing End leaves the session waiting for good.
func TestTheHandshakeHasADeadlineAndFollowsTheContext(t *testing.T) {
	ctx := context.Background()
	ca := NewCA(nil)
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	addr := silentNode(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewSignerFromKey(priv)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey())))
	pairings := NewPairings(&kv.Memory{})
	if err := pairings.Set(ctx, Pairing{Cluster: "local", Node: "n", LoginUser: "ops", Port: addr.Port, HostKeys: []string{line}, CAFinger: ca.Fingerprint(), Principal: oldPrincipal}); err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context, handshake time.Duration) error {
		svc := New(Options{CA: ca, Pairings: pairings, Handshake: handshake, NodeAddr: func(string, string) (string, error) { return "127.0.0.1", nil }})
		_, _, err := svc.Dial(ctx, "local", "n", sessions.Who{User: "pat"}, "s1")
		return err
	}
	start := time.Now()
	if err := dial(ctx, 100*time.Millisecond); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("the deadline: %v after %s", err, time.Since(start))
	}
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start = time.Now()
	if err := dial(cctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Fatalf("the context: %v after %s", err, time.Since(start))
	}
}
