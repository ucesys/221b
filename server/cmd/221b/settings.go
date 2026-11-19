package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// The shared settings.

// wireSettings opens the shared settings and what is kept beside them.
func (w *wiring) wireSettings(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// The shared settings: in the chart's ConfigMap when running in a
	// cluster, otherwise in memory. Every change reaches every browser
	// through the stores.
	var storage settings.Storage = &settings.Memory{}
	// What the settings ConfigMap holds beside the settings, with the audit
	// log and the session records in ConfigMaps of their own when the chart
	// names them, and every key within its limit (#46).
	var spendBackend kv.Backend = &kv.Memory{}
	var stored storedObjects
	if cfg.SettingsConfigMap != "" {
		cm, err := kube.NewConfigMapStorage(w.restCfg, cfg.SettingsConfigMap)
		if err != nil {
			return err
		}
		if spendBackend, stored, err = settingsBackend(ctx, w.restCfg, cm, cfg.AuditConfigMap, cfg.SessionsConfigMap, log.With("component", "storage")); err != nil {
			return err
		}
		storage = kube.KeyStorage{Backend: spendBackend, Key: kube.SettingsKey}
	}
	// Getting started's dismissed steps (GS-04) are a scope of the
	// database's settings when there is a database (Phase 13).
	spendBackend = w.inScopes(ctx, spendBackend, map[string]string{api.SetupKey: "setup"})
	// The requests that wait for a second person, rows of the database's
	// approval_requests, and the shared settings. Every reader of this
	// backend, the backup among them, finds both where they are kept.
	spendBackend = w.inTables(ctx, spendBackend, func(d *db.DB) map[string]movedDoc {
		return map[string]movedDoc{twoperson.Key: twoperson.NewPG(d), kube.SettingsKey: settings.NewPG(d), apps.RepositoriesKey: apps.NewReposPG(d),
			// Inventory: the hosts outside Kubernetes and the nodes' pairings.
			nodessh.HostsKey: nodessh.NewHostsPG(d), nodessh.PairingsKey: nodessh.NewPairingsPG(d),
			// What the agents spent, for a backup and for the sums the
			// ConfigMap held: the ledger itself adds rows (DB-41).
			sherlock.SpendKey: sherlock.NewSpendPG(d, nil)}
	})
	// The shared settings themselves: three scopes and the acknowledged
	// alerts' rows, read through the same backend.
	if w.database != nil {
		w.inDatabase = append(w.inDatabase, "platform", "approvals", "inventory")
		storage = kube.KeyStorage{Backend: spendBackend, Key: kube.SettingsKey}
	}
	shared := settings.NewService(ctx, storage, settings.Settings{RefreshSeconds: int(cfg.Refresh / time.Second)}, log.With("component", "settings"), func(st settings.State) {
		b, _ := json.Marshal(st)
		w.manager.SetSettings(st.Refresh(), b)
	})
	// Read again every few seconds: a change saved through another replica,
	// or by hand, reaches this one's browsers within seconds (P7-02).
	go shared.Run(ctx, 3*time.Second)
	w.spendBackend, w.stored, w.shared = spendBackend, stored, shared
	return nil
}
