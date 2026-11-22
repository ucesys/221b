package nodessh

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/inventory"
)

// The hosts' and the pairings' stores work over the database as over a
// ConfigMap, and every field comes back; a renamed host keeps its row; a
// node's row goes with its pairing.
func TestHostsAndPairingsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	hosts := NewHosts(NewHostsPG(d))
	added, err := hosts.Add(ctx, Host{Cluster: "c1", Name: "backup-1", Address: "10.0.0.9", Port: 2222, LoginUser: "ops", AddedBy: "Pat", AddedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	paired, err := hosts.update(ctx, "c1", "backup-1", func(x *Host) error {
		x.State, x.HostKey, x.Fingerprint, x.TrustedBy, x.TrustedAt, x.Offered, x.PairedBy, x.PairedAt = HostPaired, "ssh-ed25519 AAAA host", "SHA256:abc", "Pat", &at, "ssh-ed25519 AAAA host", "Sam", &at
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hosts.Add(ctx, Host{Cluster: "c2", Name: "10.0.0.7", Address: "10.0.0.7", LoginUser: "root", AddedBy: "Pat", AddedAt: at}, nil); err != nil {
		t.Fatal(err)
	}
	other := NewHosts(NewHostsPG(d))
	got, ok, err := other.Get(ctx, "c1", "backup-1")
	if err != nil || !ok || !reflect.DeepEqual(got, paired) || got.AddedBy != added.AddedBy || got.Port != 2222 {
		t.Fatalf("another replica reads %+v (%v, %v)\nwant %+v", got, ok, err, paired)
	}
	if auto, _, _ := other.Get(ctx, "c2", "10.0.0.7"); !auto.AutoName || auto.State != HostPending || auto.HostKey != "" || auto.TrustedAt != nil {
		t.Fatalf("the pending host reads %+v", auto)
	}
	id := func(cluster, name string) (out string) {
		_ = d.Do(ctx, func(q db.Querier) (err error) {
			out, err = inventory.HostID(ctx, q, cluster, name)
			return err
		})
		return out
	}
	was := id("c1", "backup-1")
	if _, err := hosts.rename(ctx, "c1", "backup-1", "archive-1"); err != nil {
		t.Fatal(err)
	}
	if now := id("c1", "archive-1"); now != was || id("c1", "backup-1") != "" {
		t.Fatalf("after the rename the row is %q, was %q", now, was)
	}
	if err := hosts.Delete(ctx, "c2", "10.0.0.7"); err != nil {
		t.Fatal(err)
	}
	if list, _ := other.List(ctx, "c2"); len(list) != 0 || id("c2", "10.0.0.7") != "" {
		t.Fatalf("a deleted host is still there: %+v", list)
	}

	pairings := NewPairings(NewPairingsPG(d))
	p := Pairing{Cluster: "c1", Node: "worker-1", LoginUser: "root", Port: 22, HostKeys: []string{"ssh-ed25519 AAAA n1", "ssh-rsa AAAA n1"}, PairedAt: at, PairedBy: "Sam", CAFinger: "SHA256:ca", Principal: "221b-c1-worker-1"}
	if err := pairings.Set(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := pairings.Set(ctx, Pairing{Cluster: "c1", Node: "worker-2", LoginUser: "core", Port: 2022, PairedAt: at, CAFinger: "SHA256:ca", Principal: "221b-c1-worker-2"}); err != nil {
		t.Fatal(err)
	}
	m, _, err := NewPairings(NewPairingsPG(d)).load(ctx)
	if err != nil || len(m) != 2 || !reflect.DeepEqual(m[key("c1", "worker-1")], p) {
		t.Fatalf("pairings read back as %+v: %v", m, err)
	}
	// The hosts' store does not see the nodes, nor the pairings' the hosts.
	if list, _ := other.List(ctx, "c1"); len(list) != 1 || list[0].Name != "archive-1" {
		t.Fatalf("the hosts of c1: %+v", list)
	}
	if err := pairings.Delete(ctx, "c1", "worker-2"); err != nil {
		t.Fatal(err)
	}
	if id("c1", "worker-2") != "" || id("c1", "worker-1") == "" {
		t.Fatal("an unpaired node kept its row, or a paired one lost it")
	}
}
