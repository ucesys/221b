package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/inventory"
)

// The shared settings in the database (Phase 13, DB-26). What was one
// document is kept by what it is: the general settings, Sherlock's and
// Watson's as three scopes of the settings table, each read whole; and the
// acknowledged alerts as rows of alert_acks, a row an alert. The service
// above is unchanged and reads and writes them through NewPG (db.Doc), an
// interim step until it has a repository whose operations are its own
// (Ack, Unack, SaveSherlock…).
//
// The fleets are Inventory's: each a row of fleets, and each member of a
// listed fleet a row of fleet_hosts that points at the member's row of
// hosts (package inventory).
const (
	scopeGeneral  = "general"
	scopeSherlock = "sherlock"
	scopeWatson   = "watson"
)

// SettingsKey is the key the settings were kept under in the ConfigMap;
// package kube names the same.
const SettingsKey = "settings.json"

// NewPG keeps the shared settings in d.
func NewPG(d *db.DB) *db.Doc[Settings] {
	return &db.Doc[Settings]{DB: d, Key: SettingsKey, Read: readSettings, Write: writeSettings}
}

// loadPart reads a scope into v and says whether it was there.
func loadPart(ctx context.Context, q db.Querier, scope string, v any) (bool, error) {
	b, _, err := db.LoadScope(ctx, q, scope)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func readSettings(ctx context.Context, q db.Querier) (Settings, bool, error) {
	var s Settings
	found, err := loadPart(ctx, q, scopeGeneral, &s)
	if err != nil {
		return s, false, err
	}
	// What the general scope does not hold, whatever an older row said.
	s.Sherlock, s.Watson, s.Fleets, s.Acks = nil, nil, nil, nil
	var sh Sherlock
	if ok, err := loadPart(ctx, q, scopeSherlock, &sh); err != nil {
		return s, false, err
	} else if ok {
		s.Sherlock, found = &sh, true
	}
	var wa Watson
	if ok, err := loadPart(ctx, q, scopeWatson, &wa); err != nil {
		return s, false, err
	} else if ok {
		s.Watson, found = &wa, true
	}
	fl, err := readFleets(ctx, q)
	if err != nil {
		return s, false, err
	}
	if len(fl) > 0 {
		s.Fleets, found = fl, true
	}
	rows, err := q.Query(ctx, `SELECT cluster_id, alert_key, fingerprint, acked_by, acked_at, job_run_id, case_id, exception_id, is_expected, reason
		FROM alert_acks ORDER BY cluster_id, acked_at, alert_ack_id`)
	if err != nil {
		return s, false, err
	}
	var cluster string
	var a acks.Ack
	_, err = pgx.ForEachRow(rows, []any{&cluster, &a.Key, &a.Fingerprint, &a.By, &a.At, &a.Run, &a.Case, &a.Exception, &a.Expected, &a.Why}, func() error {
		if s.Acks == nil {
			s.Acks = map[string][]acks.Ack{}
		}
		a.At = a.At.UTC()
		s.Acks[cluster] = append(s.Acks[cluster], a)
		found = true
		return nil
	})
	return s, found, err
}

// savePart writes a scope, or removes it when there is nothing to keep.
func savePart(ctx context.Context, tx pgx.Tx, scope string, v any, keep bool) error {
	if !keep {
		_, err := tx.Exec(ctx, "DELETE FROM settings WHERE scope = $1", scope)
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
	return err
}

func writeSettings(ctx context.Context, tx pgx.Tx, old, next Settings) error {
	general := func(s Settings) Settings {
		s.Sherlock, s.Watson, s.Fleets, s.Acks = nil, nil, nil, nil
		return s
	}
	// The general scope is written the first time whatever it holds: it is
	// what says the settings were saved at all.
	if g := general(next); !reflect.DeepEqual(general(old), g) || reflect.DeepEqual(old, Settings{}) {
		if err := savePart(ctx, tx, scopeGeneral, g, true); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(old.Sherlock, next.Sherlock) {
		if err := savePart(ctx, tx, scopeSherlock, next.Sherlock, next.Sherlock != nil); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(old.Watson, next.Watson) {
		if err := savePart(ctx, tx, scopeWatson, next.Watson, next.Watson != nil); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(old.Fleets, next.Fleets) {
		if err := writeFleets(ctx, tx, old.Fleets, next.Fleets); err != nil {
			return err
		}
	}
	clusters := map[string]bool{}
	for c := range old.Acks {
		clusters[c] = true
	}
	for c := range next.Acks {
		clusters[c] = true
	}
	ids := make([]string, 0, len(clusters))
	for c := range clusters {
		ids = append(ids, c)
	}
	sort.Strings(ids)
	for _, cluster := range ids {
		remove := func(a acks.Ack) error {
			_, err := tx.Exec(ctx, "DELETE FROM alert_acks WHERE cluster_id = $1 AND alert_key = $2", cluster, a.Key)
			return err
		}
		add := func(a acks.Ack) error {
			// An acknowledgement belongs to a cluster, and until the
			// connections' own records move here the cluster is known
			// only by its id.
			if _, err := tx.Exec(ctx, "INSERT INTO clusters (cluster_id) VALUES ($1) ON CONFLICT (cluster_id) DO NOTHING", cluster); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO alert_acks (alert_ack_id, cluster_id, alert_key, fingerprint, acked_by, acked_at, job_run_id, case_id, exception_id, is_expected, reason)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				db.NewID(), cluster, a.Key, a.Fingerprint, a.By, a.At.UTC(), a.Run, a.Case, a.Exception, a.Expected, a.Why)
			return err
		}
		change := func(o, a acks.Ack) error {
			if err := remove(o); err != nil {
				return err
			}
			return add(a)
		}
		if err := db.Diff(old.Acks[cluster], next.Acks[cluster], func(a acks.Ack) string { return a.Key }, add, change, remove); err != nil {
			return err
		}
	}
	return nil
}

// kindOf is a fleet's kind as its column has it.
func kindOf(f fleets.Fleet) string {
	if f.Kind == fleets.KindNodes {
		return "list"
	}
	return "selector"
}

func readFleets(ctx context.Context, q db.Querier) (map[string][]fleets.Fleet, error) {
	rows, err := q.Query(ctx, `SELECT coalesce(f.cluster_id, ''), f.fleet_key, f.name, f.color, f.description, f.labels, f.taints, f.kind, f.deploy_label,
			coalesce((SELECT array_agg(h.name ORDER BY m.position, h.name) FROM fleet_hosts m JOIN hosts h ON h.host_id = m.host_id WHERE m.fleet_id = f.fleet_id), '{}')
		FROM fleets f WHERE f.kind IN ('selector', 'list') ORDER BY f.cluster_id, f.position, f.fleet_key`)
	if err != nil {
		return nil, err
	}
	out := map[string][]fleets.Fleet{}
	var cluster, kind string
	var f fleets.Fleet
	var labels, taints, members []string
	_, err = pgx.ForEachRow(rows, []any{&cluster, &f.ID, &f.Name, &f.Color, &f.Desc, &labels, &taints, &kind, &f.Deploy, &members}, func() error {
		x := f
		x.Labels, x.Taints = append([]string{}, labels...), append([]string{}, taints...)
		x.Kind, x.Nodes = "", nil
		if kind == "list" {
			x.Kind = fleets.KindNodes
		}
		if len(members) > 0 {
			x.Nodes = append([]string(nil), members...)
		}
		out[cluster] = append(out[cluster], x)
		return nil
	})
	return out, err
}

func writeFleets(ctx context.Context, tx pgx.Tx, old, next map[string][]fleets.Fleet) error {
	if err := inventory.Lock(ctx, tx); err != nil {
		return err
	}
	clusters := map[string]bool{}
	for c := range old {
		clusters[c] = true
	}
	for c := range next {
		clusters[c] = true
	}
	ids := make([]string, 0, len(clusters))
	for c := range clusters {
		ids = append(ids, c)
	}
	sort.Strings(ids)
	for _, cluster := range ids {
		place := map[string]int{}
		for i, f := range next[cluster] {
			place[f.ID] = i
		}
		put := func(f fleets.Fleet) error {
			if err := inventory.EnsureCluster(ctx, tx, cluster); err != nil {
				return err
			}
			var id string
			err := tx.QueryRow(ctx, "SELECT fleet_id::text FROM fleets WHERE coalesce(cluster_id, '') = $1 AND fleet_key = $2", cluster, f.ID).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				id, err = db.NewID(), nil
			}
			if err != nil {
				return err
			}
			var c *string
			if cluster != "" {
				c = &cluster
			}
			labels, taints := f.Labels, f.Taints
			if labels == nil {
				labels = []string{}
			}
			if taints == nil {
				taints = []string{}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO fleets (fleet_id, cluster_id, fleet_key, position, name, kind, labels, taints, deploy_label, color, description)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (fleet_id) DO UPDATE SET position = excluded.position, name = excluded.name, kind = excluded.kind, labels = excluded.labels,
					taints = excluded.taints, deploy_label = excluded.deploy_label, color = excluded.color, description = excluded.description`,
				id, c, f.ID, place[f.ID], f.Name, kindOf(f), labels, taints, f.Deploy, f.Color, f.Desc); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM fleet_hosts WHERE fleet_id = $1", id); err != nil {
				return err
			}
			for i, name := range f.Nodes {
				host, err := inventory.EnsureHost(ctx, tx, cluster, name)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "INSERT INTO fleet_hosts (fleet_id, host_id, position) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", id, host, i); err != nil {
					return err
				}
			}
			return nil
		}
		remove := func(f fleets.Fleet) error {
			_, err := tx.Exec(ctx, "DELETE FROM fleets WHERE coalesce(cluster_id, '') = $1 AND fleet_key = $2", cluster, f.ID)
			return err
		}
		if err := db.Diff(old[cluster], next[cluster], func(f fleets.Fleet) string { return f.ID }, put, func(_, f fleets.Fleet) error { return put(f) }, remove); err != nil {
			return err
		}
		// A fleet that only moved in the list.
		for i, f := range next[cluster] {
			if _, err := tx.Exec(ctx, "UPDATE fleets SET position = $3 WHERE coalesce(cluster_id, '') = $1 AND fleet_key = $2 AND position <> $3", cluster, f.ID, i); err != nil {
				return err
			}
		}
	}
	return inventory.Collect(ctx, tx)
}
