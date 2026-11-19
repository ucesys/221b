// Package inventory is what Inventory's records share in the database
// (Phase 13): the hosts table, where a Kubernetes node and a host outside
// Kubernetes are both a row, known by its cluster and name. The pairings,
// the fleets' members and the facts are kept by the packages that own
// them, each pointing at a host's row; this package finds that row, makes
// it, and removes a node's when nothing points at it any more.
package inventory

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Lock makes Inventory's writers take turns until the transaction ends:
// each looks a host up by cluster and name before making its row, and two
// at once would make it twice.
func Lock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('221b/inventory', 0))")
	return err
}

// EnsureCluster makes the cluster's row by its id when it has none: until
// the connections' own records move here, a cluster is known by its id.
func EnsureCluster(ctx context.Context, q db.Querier, cluster string) error {
	if cluster == "" {
		return nil
	}
	_, err := q.Exec(ctx, "INSERT INTO clusters (cluster_id) VALUES ($1) ON CONFLICT (cluster_id) DO NOTHING", cluster)
	return err
}

// HostID is the id of the host of that cluster and name, "" when it has
// no row.
func HostID(ctx context.Context, q db.Querier, cluster, name string) (string, error) {
	var id string
	err := q.QueryRow(ctx, "SELECT host_id::text FROM hosts WHERE coalesce(cluster_id, '') = $1 AND name = $2", cluster, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// EnsureHost returns the id of the host of that cluster and name, making
// its row when it has none: a node's identity, which is all a node's row
// holds. Call Lock first.
func EnsureHost(ctx context.Context, tx pgx.Tx, cluster, name string) (string, error) {
	id, err := HostID(ctx, tx, cluster, name)
	if err != nil || id != "" {
		return id, err
	}
	if err := EnsureCluster(ctx, tx, cluster); err != nil {
		return "", err
	}
	id = db.NewID()
	var c *string
	if cluster != "" {
		c = &cluster
	}
	_, err = tx.Exec(ctx, "INSERT INTO hosts (host_id, cluster_id, name, is_kubernetes_node) VALUES ($1, $2, $3, true)", id, c, name)
	return id, err
}

// Collect removes the rows of nodes nothing points at any more: no
// pairing, no facts, no fleet lists them, and they have no variables or
// reboot time of their own. A node's row is made at its first use and
// goes when it leaves.
func Collect(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM hosts h WHERE h.is_kubernetes_node
		AND NOT EXISTS (SELECT FROM host_pairings p WHERE p.host_id = h.host_id)
		AND NOT EXISTS (SELECT FROM host_facts f WHERE f.host_id = h.host_id)
		AND NOT EXISTS (SELECT FROM fleet_hosts m WHERE m.host_id = h.host_id)
		AND NOT EXISTS (SELECT FROM variables v WHERE v.host_id = h.host_id)
		AND h.reboot_minutes IS NULL`)
	return err
}
