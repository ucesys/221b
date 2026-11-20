package automation

import (
	"context"
	"errors"
	"fmt"
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
	return &db.Doc[variablesRecord]{DB: d, Key: VariablesKey, Read: readVariables, Write: func(ctx context.Context, tx pgx.Tx, old, next variablesRecord) error {
		return writeVariables(ctx, tx, sensitiveFor(d), old, next)
	}}
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
	// A sensitive value is not in its row: the row has its digest, which
	// stands for it in the document (DB-71).
	var value, digest *string
	var sensitive bool
	rows, err = q.Query(ctx, `SELECT coalesce(f.cluster_id, ''), f.fleet_key, v.name, v.value, v.value_digest, v.is_sensitive
		FROM variables v JOIN fleets f ON f.fleet_id = v.fleet_id ORDER BY v.name`)
	if err != nil {
		return rec, false, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &key, &name, &value, &digest, &sensitive}, func() error {
		edit(cluster, key, func(fp *FleetVariables) {
			if digest != nil {
				value = digest
			}
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
	rows, err = q.Query(ctx, `SELECT coalesce(h.cluster_id, ''), h.name, v.name, coalesce(v.value_digest, v.value)
		FROM variables v JOIN hosts h ON h.host_id = v.host_id WHERE v.value IS NOT NULL OR v.value_digest IS NOT NULL`)
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
	// A value under a sensitive name that is still in its row was not
	// moved to the Secret yet (MoveSensitive): nothing reads it from here.
	for _, fleets := range rec.Clusters {
		names := sensitiveNames(fleets)
		if len(names) == 0 {
			continue
		}
		_ = eachValue(fleets, func(p place, v string) (string, error) {
			if names[p.name] && !IsHidden(v) {
				return Unmoved, nil
			}
			return v, nil
		})
	}
	return rec, true, nil
}

// writeVariables writes the clusters whose variables changed. A document
// copied from a ConfigMap may still keep a node's own values under its
// fleets: they are the node's first.
//
// The sensitive values are hidden first (sealVariables): the rows get
// their digests, the Secret gets the values once the rows are written,
// and the transaction commits after that. A name that becomes sensitive
// with this save has its values hidden in the changes and the runs of the
// past too.
func writeVariables(ctx context.Context, tx pgx.Tx, sens SensitiveStore, old, next variablesRecord) error {
	next.nodesOwn()
	old.nodesOwn()
	r := newRing(ctx, sens)
	sealed, err := sealVariables(r, &old, &next)
	if err != nil {
		return err
	}
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
	if err := inventory.Collect(ctx, tx); err != nil {
		return err
	}
	if len(sealed.became) > 0 {
		if _, err := sealPastTables(ctx, tx, r, sealed, ""); err != nil {
			return err
		}
	}
	// The rows are written; the Secret now, and then the commit.
	return sealed.put(ctx, sens)
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
			want[name] = rowOfValue(v)
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
			want[name] = rowOfValue(v)
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
	value *string
	// digest stands for a sensitive value, which is in the Secret.
	digest    *string
	sensitive bool
	// keep leaves the row's value as it is: a sensitive value the start
	// has not moved to the Secret yet.
	keep bool
}

// rowOfValue is a value of the document as its row keeps it: a digest in
// the digest's column, any other in the value's.
func rowOfValue(v string) variableRow {
	switch {
	case v == Unmoved:
		return variableRow{keep: true}
	case IsHidden(v):
		return variableRow{digest: &v}
	}
	return variableRow{value: &v}
}

// syncVariables makes the variables of one fleet or host what want says,
// touching only the rows that differ, so a row keeps its id and the time
// it last changed.
func syncVariables(ctx context.Context, tx pgx.Tx, scope, id string, want map[string]variableRow) error {
	column := scope + "_id"
	have := map[string]variableRow{}
	rows, err := tx.Query(ctx, "SELECT name, value IS NOT NULL, value_digest, is_sensitive FROM variables WHERE "+column+" = $1", id)
	if err != nil {
		return err
	}
	var name string
	var r variableRow
	var set bool
	// The values that are not sensitive are compared by what the caller
	// read, never read here: the caller writes a row whose value differs.
	if _, err := pgx.ForEachRow(rows, []any{&name, &set, &r.digest, &r.sensitive}, func() error {
		x := r
		x.keep = set
		have[name] = x
		return nil
	}); err != nil {
		return err
	}
	text := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
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
		case had && w.keep:
			// Its value stays in the row until the start moves it.
			if w.sensitive == h.sensitive {
				continue
			}
			if w.sensitive {
				return fmt.Errorf("%w: %s is sensitive and its value was not moved to the Secret yet: type its value", ErrInvalid, n)
			}
			if _, err := tx.Exec(ctx, "UPDATE variables SET is_sensitive = $3, updated_at = now(), updated_by = $4 WHERE "+column+" = $1 AND name = $2", id, n, w.sensitive, byOf(ctx)); err != nil {
				return err
			}
		case had && w.value == nil && !h.keep && text(w.digest) == text(h.digest) && (w.digest == nil) == (h.digest == nil) && w.sensitive == h.sensitive:
		case had:
			if _, err := tx.Exec(ctx, "UPDATE variables SET value = $3, value_digest = $4, is_sensitive = $5, updated_at = now(), updated_by = $6 WHERE "+column+" = $1 AND name = $2 AND (value IS DISTINCT FROM $3 OR value_digest IS DISTINCT FROM $4 OR is_sensitive <> $5)",
				id, n, w.value, w.digest, w.sensitive, byOf(ctx)); err != nil {
				return err
			}
		default:
			if _, err := tx.Exec(ctx, "INSERT INTO variables (variable_id, scope, "+column+", name, value, value_digest, is_sensitive, updated_at, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8)",
				db.NewID(), scope, id, n, w.value, w.digest, w.sensitive, byOf(ctx)); err != nil {
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
