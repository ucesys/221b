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
)

// The shared settings in the database (Phase 13, DB-26). What was one
// document is kept by what it is: the general settings, Sherlock's and
// Watson's as three scopes of the settings table, each read whole; and the
// acknowledged alerts as rows of alert_acks, a row an alert. The service
// above is unchanged and reads and writes them through NewPG (db.Doc), an
// interim step until it has a repository whose operations are its own
// (Ack, Unack, SaveSherlock…).
//
// The fleets stay one more document, the scope "fleets", until Inventory
// moves: a fleet's members are hosts, and hosts are Inventory's.
const (
	scopeGeneral  = "general"
	scopeSherlock = "sherlock"
	scopeWatson   = "watson"
	scopeFleets   = "fleets"
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
	var fl map[string][]fleets.Fleet
	if ok, err := loadPart(ctx, q, scopeFleets, &fl); err != nil {
		return s, false, err
	} else if ok && len(fl) > 0 {
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
		if err := savePart(ctx, tx, scopeFleets, next.Fleets, len(next.Fleets) > 0); err != nil {
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
