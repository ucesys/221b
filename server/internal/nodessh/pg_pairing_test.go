package nodessh

import (
	"context"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// A host outside Kubernetes, paired, reads as paired, and keeps its
// pairing when the host's own record is saved again; unpaired, it keeps
// its trusted key (the release check of 0.13.0: hosts.spec's pairing said
// "is not paired for SSH", since the pairings were read for the nodes
// alone, and a host's save wiped the pairing's columns of the shared row).
func TestAHostsPairingIsKeptInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	hosts := NewHosts(NewHostsPG(d))
	pairings := NewPairings(NewPairingsPG(d))
	at := time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC)
	if _, err := hosts.Add(ctx, Host{Cluster: "c1", Name: "backup-1", Address: "10.0.0.9", Port: 22, LoginUser: "root", AddedBy: "Pat", AddedAt: at}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := hosts.update(ctx, "c1", "backup-1", func(x *Host) error {
		x.State, x.HostKey, x.Fingerprint, x.TrustedBy, x.TrustedAt = HostPending, "ssh-ed25519 AAAA host", "SHA256:abc", "Pat", &at
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := Pairing{Cluster: "c1", Node: "backup-1", LoginUser: "root", Port: 22, HostKeys: []string{"ssh-ed25519 AAAA host"}, PairedAt: at, PairedBy: "Sam", CAFinger: "SHA256:ca", Principal: "221b-c1"}
	if err := pairings.Set(ctx, want); err != nil {
		t.Fatal(err)
	}
	// The pairing flow then marks the host paired: a save of the host's record.
	if _, err := hosts.update(ctx, "c1", "backup-1", func(x *Host) error {
		x.State, x.PairedBy, x.PairedAt = HostPaired, "Sam", &at
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := NewPairings(NewPairingsPG(d)).Get(ctx, "c1", "backup-1")
	if err != nil || !ok || got.CAFinger != "SHA256:ca" || got.Principal != "221b-c1" || got.LoginUser != "root" || len(got.HostKeys) != 1 {
		t.Fatalf("the host's pairing reads %+v (%v, %v); want %+v", got, ok, err, want)
	}
	h, _, _ := NewHosts(NewHostsPG(d)).Get(ctx, "c1", "backup-1")
	if h.State != HostPaired || h.HostKey != "ssh-ed25519 AAAA host" || h.TrustedBy != "Pat" || h.Fingerprint != "SHA256:abc" {
		t.Fatalf("the host reads %+v", h)
	}
	if err := pairings.Delete(ctx, "c1", "backup-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := NewPairings(NewPairingsPG(d)).Get(ctx, "c1", "backup-1"); ok {
		t.Fatal("the host still reads as paired after its pairing was deleted")
	}
	if h, _, _ := NewHosts(NewHostsPG(d)).Get(ctx, "c1", "backup-1"); h.HostKey != "ssh-ed25519 AAAA host" || h.TrustedBy != "Pat" {
		t.Fatalf("unpaired, the host lost its trusted key: %+v", h)
	}
}
