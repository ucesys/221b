package nodessh

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/inventory"
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

// pgHostEnv is a console whose hosts and pairings are the database's rows,
// with the database to reach them by.
func pgHostEnv(t *testing.T) (hostEnv, *db.DB) {
	t.Helper()
	d := dbtest.Open(t)
	return newHostEnvIn(t, NewHostsPG(d), NewPairingsPG(d)), d
}

// aRow gives a name a row of its own, as a list fleet that names it does
// with facts collected for it: a node's identity.
func aRow(t *testing.T, d *db.DB, cluster, name string) {
	t.Helper()
	ctx := context.Background()
	if err := d.Do(ctx, func(q db.Querier) error {
		if err := inventory.EnsureCluster(ctx, q, cluster); err != nil {
			return err
		}
		id := db.NewID()
		if _, err := q.Exec(ctx, "INSERT INTO hosts (host_id, cluster_id, name, is_kubernetes_node) VALUES ($1, $2, $3, true)", id, cluster, name); err != nil {
			return err
		}
		_, err := q.Exec(ctx, "INSERT INTO host_facts (host_id, facts) VALUES ($1, '{}')", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A host that takes a hostname a row already has keeps its pairing
// (finding 2 of the review of November 24, 2026: the old row went with its
// pairing, the host read as paired under its new name, and every session
// said "is not paired for SSH").
func TestAHostTakesAHostnameThatHasARowWithItsPairing(t *testing.T) {
	ctx := context.Background()
	e, d := pgHostEnv(t)
	aRow(t, d, "local", "db-primary")
	h, err := e.svc.AddHost(ctx, Host{Cluster: "local", Name: "127.0.0.1", Address: "127.0.0.1", Port: e.host.port(), LoginUser: e.login}, "Pat")
	if err != nil || !h.AutoName {
		t.Fatalf("adding: %+v %v", h, err)
	}
	offer, err := e.svc.ScanHost(ctx, "local", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.TrustHost(ctx, "local", "127.0.0.1", offer.Fingerprint, "Pat"); err != nil {
		t.Fatal(err)
	}
	cmd, err := e.svc.SetupCommand(ctx, "local", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	e.host.mustRun(strings.TrimSuffix(strings.TrimPrefix(cmd, "sudo sh <<'221B-SETUP'\n"), "\n221B-SETUP\n"))
	if err := os.WriteFile(filepath.Join(e.host.bin, "uname"), []byte("#!/bin/sh\necho db-primary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h, err = e.svc.CheckHost(ctx, "local", "127.0.0.1", "Pat")
	if err != nil || h.Name != "db-primary" || h.State != HostPaired {
		t.Fatalf("after the check: %+v %v", h, err)
	}
	if p, ok, err := e.svc.opts.Pairings.Get(ctx, "local", "db-primary"); err != nil || !ok || p.CAFinger == "" || p.Principal == "" {
		t.Fatalf("the pairing under the hostname: %+v %v %v", p, ok, err)
	}
	if code, err := e.svc.Exec(ctx, "local", "db-primary", "k", "true", false, strings.NewReader(""), io.Discard); err != nil || code != 0 {
		t.Fatalf("a run on db-primary: %d %v", code, err)
	}
}
