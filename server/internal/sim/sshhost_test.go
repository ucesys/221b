package sim

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
)

// The fake host is added, its key read and trusted, paired with its
// bootstrap account through sudo, and reached with a certificate, as the
// browser tests do it.
func TestTheFakeHostPairsAndRuns(t *testing.T) {
	ctx := context.Background()
	h, err := NewSSHHost()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go h.Serve(ln)
	b := &kv.Memory{}
	svc := nodessh.New(nodessh.Options{CA: nodessh.NewCA(nil), Pairings: nodessh.NewPairings(b), Hosts: nodessh.NewHosts(b), CertTTL: time.Minute})
	if _, err := svc.AddHost(ctx, nodessh.Host{Cluster: "local", Name: "fake-host", Address: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, LoginUser: "root"}, "Pat"); err != nil {
		t.Fatal(err)
	}
	offer, err := svc.ScanHost(ctx, "local", "fake-host")
	if err != nil || offer.Fingerprint != h.Fingerprint() {
		t.Fatalf("the key: %+v %v", offer, err)
	}
	if _, _, err := svc.TrustHost(ctx, "local", "fake-host", offer.Fingerprint, "Pat"); err != nil {
		t.Fatal(err)
	}
	wrong := nodessh.Credential{User: HostUser, Password: HostPassword, SudoPassword: "wrong"}
	if _, _, err := svc.PairHost(ctx, "local", "fake-host", &wrong, "Pat"); err == nil || !strings.Contains(err.Error(), "sudo refused the password") {
		t.Fatalf("a wrong sudo password: %v", err)
	}
	cred := nodessh.Credential{User: HostUser, Password: HostPassword}
	got, _, err := svc.PairHost(ctx, "local", "fake-host", &cred, "Pat")
	if err != nil || got.State != nodessh.HostPaired {
		t.Fatalf("pairing: %+v %v", got, err)
	}
	var out strings.Builder
	if code, err := svc.Exec(ctx, "local", "fake-host", "pat via 221B automation facts", "sh -s", true, strings.NewReader("one os.name \"$PRETTY_NAME\"\n"), &out); err != nil || code != 0 || !strings.Contains(out.String(), "os.family=debian") {
		t.Fatalf("facts: %d %v %q", code, err, out.String())
	}
}
