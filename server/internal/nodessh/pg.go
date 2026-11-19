package nodessh

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/inventory"
)

// SSH's records in the database (Phase 13, DB-28). A host outside
// Kubernetes is a row of hosts with its address, port and state; the login
// it is reached as is a row of host_logins; the key pinned for it, who
// trusted it and who paired it a row of host_pairings. A paired Kubernetes
// node is the same three rows, its own holding its identity only. Hosts
// and Pairings above are unchanged and reach them through db.Doc, an
// interim step until they have repositories of their own.

// NewHostsPG keeps the hosts outside Kubernetes in d.
func NewHostsPG(d *db.DB) *db.Doc[map[string]Host] {
	return &db.Doc[map[string]Host]{DB: d, Key: HostsKey, Read: readHosts, Write: writeHosts}
}

// NewPairingsPG keeps the nodes' pairings in d.
func NewPairingsPG(d *db.DB) *db.Doc[map[string]Pairing] {
	return &db.Doc[map[string]Pairing]{DB: d, Key: PairingsKey, Read: readPairings, Write: writePairings}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func readHosts(ctx context.Context, q db.Querier) (map[string]Host, bool, error) {
	rows, err := q.Query(ctx, `SELECT coalesce(h.cluster_id, ''), h.name, h.address, coalesce(h.port, 0), h.state, h.is_auto_named, h.added_by, h.created_at,
			coalesce((SELECT l.login_user FROM host_logins l WHERE l.host_id = h.host_id ORDER BY l.login_user LIMIT 1), ''),
			coalesce(p.host_keys, '{}'), coalesce(p.fingerprint, ''), coalesce(p.trusted_by, ''), p.trusted_at,
			coalesce(p.offered_key, ''), coalesce(p.mismatch_key, ''), coalesce(p.paired_by, ''), p.paired_at
		FROM hosts h LEFT JOIN host_pairings p ON p.host_id = h.host_id
		WHERE NOT h.is_kubernetes_node`)
	if err != nil {
		return nil, false, err
	}
	m := map[string]Host{}
	var x Host
	var keys []string
	var trusted, paired *time.Time
	_, err = pgx.ForEachRow(rows, []any{&x.Cluster, &x.Name, &x.Address, &x.Port, &x.State, &x.AutoName, &x.AddedBy, &x.AddedAt, &x.LoginUser,
		&keys, &x.Fingerprint, &x.TrustedBy, &trusted, &x.Offered, &x.Mismatch, &x.PairedBy, &paired}, func() error {
		h := x
		h.AddedAt, h.TrustedAt, h.PairedAt, h.HostKey = x.AddedAt.UTC(), utcPtr(trusted), utcPtr(paired), ""
		if len(keys) > 0 {
			h.HostKey = keys[0]
		}
		m[key(h.Cluster, h.Name)] = h
		return nil
	})
	return m, len(m) > 0, err
}

// putHost writes a host's three rows, taking over a row of that name that
// held only an identity.
func putHost(ctx context.Context, tx pgx.Tx, x Host) error {
	id, err := inventory.EnsureHost(ctx, tx, x.Cluster, x.Name)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hosts SET is_kubernetes_node = false, address = $2, port = $3, state = $4, is_auto_named = $5, added_by = $6, created_at = $7
		WHERE host_id = $1`, id, x.Address, x.Port, x.State, x.AutoName, x.AddedBy, x.AddedAt.UTC()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM host_logins WHERE host_id = $1", id); err != nil {
		return err
	}
	if x.LoginUser != "" {
		if _, err := tx.Exec(ctx, "INSERT INTO host_logins (host_id, login_user) VALUES ($1, $2)", id, x.LoginUser); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM host_pairings WHERE host_id = $1", id); err != nil {
		return err
	}
	if x.HostKey == "" && x.Fingerprint == "" && x.TrustedBy == "" && x.TrustedAt == nil && x.Offered == "" && x.Mismatch == "" && x.PairedBy == "" && x.PairedAt == nil {
		return nil
	}
	keys := []string{}
	if x.HostKey != "" {
		keys = append(keys, x.HostKey)
	}
	_, err = tx.Exec(ctx, `INSERT INTO host_pairings (host_id, host_keys, fingerprint, trusted_by, trusted_at, offered_key, mismatch_key, paired_by, paired_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, keys, x.Fingerprint, x.TrustedBy, utcPtr(x.TrustedAt), x.Offered, x.Mismatch, x.PairedBy, utcPtr(x.PairedAt))
	return err
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeHosts(ctx context.Context, tx pgx.Tx, old, next map[string]Host) error {
	if err := inventory.Lock(ctx, tx); err != nil {
		return err
	}
	gone := map[string]Host{}
	for k, x := range old {
		if _, ok := next[k]; !ok {
			gone[k] = x
		}
	}
	// A host that only changed its name keeps its row, and with it its
	// place in its fleets and its facts: it is the host gone and the host
	// new of one cluster, address and day of adding.
	for _, k := range sortedKeys(next) {
		x := next[k]
		if _, ok := old[k]; ok {
			continue
		}
		for _, g := range sortedKeys(gone) {
			was := gone[g]
			if was.Cluster != x.Cluster || was.Address != x.Address || !was.AddedAt.Equal(x.AddedAt) {
				continue
			}
			id, err := inventory.HostID(ctx, tx, was.Cluster, was.Name)
			if err != nil {
				return err
			}
			taken, err := inventory.HostID(ctx, tx, x.Cluster, x.Name)
			if err != nil {
				return err
			}
			// A fleet may already list the new name, as a row of its own:
			// that row is the host now, and the old one goes below.
			if id != "" && taken == "" {
				if _, err := tx.Exec(ctx, "UPDATE hosts SET name = $2 WHERE host_id = $1", id, x.Name); err != nil {
					return err
				}
				delete(gone, g)
			}
			break
		}
	}
	for _, k := range sortedKeys(gone) {
		x := gone[k]
		// With its logins, its pairing, its facts and its places in fleets.
		if _, err := tx.Exec(ctx, "DELETE FROM hosts WHERE coalesce(cluster_id, '') = $1 AND name = $2 AND NOT is_kubernetes_node", x.Cluster, x.Name); err != nil {
			return err
		}
	}
	for _, k := range sortedKeys(next) {
		if o, ok := old[k]; ok && reflect.DeepEqual(o, next[k]) {
			continue
		}
		if err := putHost(ctx, tx, next[k]); err != nil {
			return err
		}
	}
	return inventory.Collect(ctx, tx)
}

func readPairings(ctx context.Context, q db.Querier) (map[string]Pairing, bool, error) {
	rows, err := q.Query(ctx, `SELECT coalesce(h.cluster_id, ''), h.name, coalesce(h.port, 0),
			coalesce((SELECT l.login_user FROM host_logins l WHERE l.host_id = h.host_id ORDER BY l.login_user LIMIT 1), ''),
			coalesce((SELECT l.principal FROM host_logins l WHERE l.host_id = h.host_id ORDER BY l.login_user LIMIT 1), ''),
			p.host_keys, p.ca_fingerprint, p.paired_by, p.paired_at
		FROM hosts h JOIN host_pairings p ON p.host_id = h.host_id
		WHERE h.is_kubernetes_node`)
	if err != nil {
		return nil, false, err
	}
	m := map[string]Pairing{}
	var p Pairing
	var keys []string
	var paired *time.Time
	_, err = pgx.ForEachRow(rows, []any{&p.Cluster, &p.Node, &p.Port, &p.LoginUser, &p.Principal, &keys, &p.CAFinger, &p.PairedBy, &paired}, func() error {
		x := p
		x.HostKeys = nil
		if len(keys) > 0 {
			x.HostKeys = append([]string(nil), keys...)
		}
		x.PairedAt = time.Time{}
		if paired != nil {
			x.PairedAt = paired.UTC()
		}
		m[key(x.Cluster, x.Node)] = x
		return nil
	})
	return m, len(m) > 0, err
}

func writePairings(ctx context.Context, tx pgx.Tx, old, next map[string]Pairing) error {
	if err := inventory.Lock(ctx, tx); err != nil {
		return err
	}
	for _, k := range sortedKeys(old) {
		if _, ok := next[k]; ok {
			continue
		}
		p := old[k]
		id, err := inventory.HostID(ctx, tx, p.Cluster, p.Node)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		for _, table := range []string{"host_pairings", "host_logins"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE host_id IN (SELECT host_id FROM hosts WHERE host_id = $1 AND is_kubernetes_node)", id); err != nil {
				return err
			}
		}
	}
	for _, k := range sortedKeys(next) {
		p := next[k]
		if o, ok := old[k]; ok && reflect.DeepEqual(o, p) {
			continue
		}
		id, err := inventory.EnsureHost(ctx, tx, p.Cluster, p.Node)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE hosts SET port = $2 WHERE host_id = $1", id, p.Port); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM host_logins WHERE host_id = $1", id); err != nil {
			return err
		}
		if p.LoginUser != "" {
			if _, err := tx.Exec(ctx, "INSERT INTO host_logins (host_id, login_user, principal) VALUES ($1, $2, $3)", id, p.LoginUser, p.Principal); err != nil {
				return err
			}
		}
		keys := p.HostKeys
		if keys == nil {
			keys = []string{}
		}
		var paired *time.Time
		if !p.PairedAt.IsZero() {
			t := p.PairedAt.UTC()
			paired = &t
		}
		if _, err := tx.Exec(ctx, `INSERT INTO host_pairings (host_id, host_keys, ca_fingerprint, paired_by, paired_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (host_id) DO UPDATE SET host_keys = excluded.host_keys, ca_fingerprint = excluded.ca_fingerprint,
				paired_by = excluded.paired_by, paired_at = excluded.paired_at`, id, keys, p.CAFinger, p.PairedBy, paired); err != nil {
			return err
		}
	}
	return inventory.Collect(ctx, tx)
}
