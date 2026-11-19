package automation

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/inventory"
)

// Automation's variables in the database (Phase 13, DB-33), as the data
// model has them: a variable is a row of variables on one fleet or on one
// host; a fleet's privilege ceiling and reboot time are columns of its row
// in fleets, a node's own reboot time a column of its row in hosts. All
// nodes is a fleet of kind all, one a cluster. The store is unchanged and
// reaches the rows through db.Doc.

// The kinds of fleet the settings' list leaves out.
const (
	fleetAll      = "all"
	fleetUnlisted = "unlisted"
)

// NewVariablesPG keeps the variables in d.
func NewVariablesPG(d *db.DB) *db.Doc[variablesRecord] {
	return &db.Doc[variablesRecord]{DB: d, Key: VariablesKey, Read: readVariables, Write: writeVariables}
}

func readVariables(ctx context.Context, q db.Querier) (variablesRecord, bool, error) {
	rec := variablesRecord{Clusters: map[string]map[string]FleetVariables{}}
	edit := func(cluster, fleet string, f func(*FleetVariables)) {
		if rec.Clusters[cluster] == nil {
			rec.Clusters[cluster] = map[string]FleetVariables{}
		}
		fp := rec.Clusters[cluster][fleet]
		f(&fp)
		rec.Clusters[cluster][fleet] = fp
	}
	var cluster, key, name string
	var ceiling string
	var minutes *int
	rows, err := q.Query(ctx, "SELECT coalesce(cluster_id, ''), fleet_key, privilege_ceiling, reboot_minutes FROM fleets WHERE privilege_ceiling <> '' OR reboot_minutes IS NOT NULL")
	if err != nil {
		return rec, false, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &key, &ceiling, &minutes}, func() error {
		edit(cluster, key, func(fp *FleetVariables) {
			fp.Ceiling = ceiling
			if minutes != nil {
				fp.RebootMinutes = *minutes
			}
		})
		return nil
	}); err != nil {
		return rec, false, err
	}
	var value *string
	var sensitive bool
	rows, err = q.Query(ctx, `SELECT coalesce(f.cluster_id, ''), f.fleet_key, v.name, v.value, v.is_sensitive
		FROM variables v JOIN fleets f ON f.fleet_id = v.fleet_id ORDER BY v.name`)
	if err != nil {
		return rec, false, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &key, &name, &value, &sensitive}, func() error {
		edit(cluster, key, func(fp *FleetVariables) {
			if value != nil {
				if fp.Values == nil {
					fp.Values = map[string]string{}
				}
				fp.Values[name] = *value
			}
			if sensitive {
				fp.Sensitive = append(fp.Sensitive, name)
			}
		})
		return nil
	}); err != nil {
		return rec, false, err
	}
	var node string
	rows, err = q.Query(ctx, `SELECT coalesce(h.cluster_id, ''), h.name, v.name, v.value
		FROM variables v JOIN hosts h ON h.host_id = v.host_id WHERE v.value IS NOT NULL`)
	if err != nil {
		return rec, false, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &node, &name, &value}, func() error {
		edit(cluster, AllNodes, func(fp *FleetVariables) {
			if fp.Nodes == nil {
				fp.Nodes = map[string]map[string]string{}
			}
			if fp.Nodes[node] == nil {
				fp.Nodes[node] = map[string]string{}
			}
			fp.Nodes[node][name] = *value
		})
		return nil
	}); err != nil {
		return rec, false, err
	}
	rows, err = q.Query(ctx, "SELECT coalesce(cluster_id, ''), name, reboot_minutes FROM hosts WHERE reboot_minutes IS NOT NULL")
	if err != nil {
		return rec, false, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &node, &minutes}, func() error {
		edit(cluster, AllNodes, func(fp *FleetVariables) {
			if fp.NodeReboot == nil {
				fp.NodeReboot = map[string]int{}
			}
			fp.NodeReboot[node] = *minutes
		})
		return nil
	}); err != nil {
		return rec, false, err
	}
	if len(rec.Clusters) == 0 {
		return variablesRecord{}, false, nil
	}
	return rec, true, nil
}

// writeVariables writes the clusters whose variables changed. A document
// copied from a ConfigMap may still keep a node's own values under its
// fleets: they are the node's first.
func writeVariables(ctx context.Context, tx pgx.Tx, old, next variablesRecord) error {
	next.nodesOwn()
	if err := inventory.Lock(ctx, tx); err != nil {
		return err
	}
	clusters := map[string]bool{}
	for c := range old.Clusters {
		clusters[c] = true
	}
	for c := range next.Clusters {
		clusters[c] = true
	}
	ids := make([]string, 0, len(clusters))
	for c := range clusters {
		ids = append(ids, c)
	}
	sort.Strings(ids)
	for _, cluster := range ids {
		if sameJSON(old.Clusters[cluster], next.Clusters[cluster]) {
			continue
		}
		if err := writeClusterVariables(ctx, tx, cluster, old.Clusters[cluster], next.Clusters[cluster]); err != nil {
			return err
		}
	}
	return inventory.Collect(ctx, tx)
}

func writeClusterVariables(ctx context.Context, tx pgx.Tx, cluster string, old, next map[string]FleetVariables) error {
	fleets := map[string]bool{}
	for id := range old {
		fleets[id] = true
	}
	for id := range next {
		fleets[id] = true
	}
	ids := make([]string, 0, len(fleets))
	for id := range fleets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, key := range ids {
		was, fp := old[key], next[key]
		own := func(x FleetVariables) FleetVariables { x.Nodes, x.NodeReboot = nil, nil; return x }
		if sameJSON(own(was), own(fp)) {
			continue
		}
		id, err := fleetRow(ctx, tx, cluster, key, !own(fp).Empty())
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		var minutes *int
		if fp.RebootMinutes > 0 {
			minutes = &fp.RebootMinutes
		}
		if _, err := tx.Exec(ctx, "UPDATE fleets SET privilege_ceiling = $2, reboot_minutes = $3 WHERE fleet_id = $1", id, fp.Ceiling, minutes); err != nil {
			return err
		}
		want := map[string]variableRow{}
		for name, v := range fp.Values {
			want[name] = variableRow{value: &v}
		}
		for _, name := range fp.Sensitive {
			r := want[name]
			r.sensitive = true
			want[name] = r
		}
		if err := syncVariables(ctx, tx, "fleet", id, want); err != nil {
			return err
		}
	}
	// A fleet that only variables named, with nothing set on it any more.
	if _, err := tx.Exec(ctx, `DELETE FROM fleets f WHERE coalesce(f.cluster_id, '') = $1 AND f.kind = $2 AND f.privilege_ceiling = ''
		AND f.reboot_minutes IS NULL AND NOT EXISTS (SELECT FROM variables v WHERE v.fleet_id = f.fleet_id)`, cluster, fleetUnlisted); err != nil {
		return err
	}

	// The nodes' own, kept under All nodes.
	was, all := old[AllNodes], next[AllNodes]
	for _, node := range NamedNodes(was, all) {
		if sameJSON(was.Nodes[node], all.Nodes[node]) && was.NodeReboot[node] == all.NodeReboot[node] {
			continue
		}
		id, err := inventory.EnsureHost(ctx, tx, cluster, node)
		if err != nil {
			return err
		}
		var minutes *int
		if m := all.NodeReboot[node]; m > 0 {
			minutes = &m
		}
		if _, err := tx.Exec(ctx, "UPDATE hosts SET reboot_minutes = $2 WHERE host_id = $1", id, minutes); err != nil {
			return err
		}
		want := map[string]variableRow{}
		for name, v := range all.Nodes[node] {
			want[name] = variableRow{value: &v}
		}
		if err := syncVariables(ctx, tx, "host", id, want); err != nil {
			return err
		}
	}
	return nil
}

// fleetRow is the id of a fleet's row. A fleet that variables name and the
// settings' list does not have gets one when there is something to set on
// it: All nodes as the cluster's fleet of every node, any other as
// unlisted, which the settings' list takes over when it has the fleet.
func fleetRow(ctx context.Context, tx pgx.Tx, cluster, key string, create bool) (string, error) {
	var id string
	err := tx.QueryRow(ctx, "SELECT fleet_id::text FROM fleets WHERE coalesce(cluster_id, '') = $1 AND fleet_key = $2", cluster, key).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) || !create {
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		return id, err
	}
	if err := inventory.EnsureCluster(ctx, tx, cluster); err != nil {
		return "", err
	}
	var c *string
	if cluster != "" {
		c = &cluster
	}
	kind, name := fleetUnlisted, ""
	if key == AllNodes {
		kind, name = fleetAll, "All nodes"
	}
	id = db.NewID()
	_, err = tx.Exec(ctx, "INSERT INTO fleets (fleet_id, cluster_id, fleet_key, name, kind, position) VALUES ($1, $2, $3, $4, $5, -1)", id, c, key, name, kind)
	return id, err
}

type variableRow struct {
	value     *string
	sensitive bool
}

// syncVariables makes the variables of one fleet or host what want says,
// touching only the rows that differ, so a row keeps its id and the time
// it last changed.
func syncVariables(ctx context.Context, tx pgx.Tx, scope, id string, want map[string]variableRow) error {
	column := scope + "_id"
	have := map[string]variableRow{}
	rows, err := tx.Query(ctx, "SELECT name, value, is_sensitive FROM variables WHERE "+column+" = $1", id)
	if err != nil {
		return err
	}
	var name string
	var r variableRow
	if _, err := pgx.ForEachRow(rows, []any{&name, &r.value, &r.sensitive}, func() error {
		have[name] = r
		return nil
	}); err != nil {
		return err
	}
	same := func(a, b variableRow) bool {
		return a.sensitive == b.sensitive && (a.value == nil) == (b.value == nil) && (a.value == nil || *a.value == *b.value)
	}
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w := want[n]
		h, had := have[n]
		switch {
		case had && same(h, w):
		case had:
			if _, err := tx.Exec(ctx, "UPDATE variables SET value = $3, is_sensitive = $4, updated_at = now() WHERE "+column+" = $1 AND name = $2", id, n, w.value, w.sensitive); err != nil {
				return err
			}
		default:
			if _, err := tx.Exec(ctx, "INSERT INTO variables (variable_id, scope, "+column+", name, value, is_sensitive, updated_at) VALUES ($1, $2, $3, $4, $5, $6, now())",
				db.NewID(), scope, id, n, w.value, w.sensitive); err != nil {
				return err
			}
		}
	}
	gone := []string{}
	for n := range have {
		if _, ok := want[n]; !ok {
			gone = append(gone, n)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, "DELETE FROM variables WHERE "+column+" = $1 AND name = ANY($2)", id, gone)
	return err
}
