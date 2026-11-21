package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// The shared settings.

// wireSettings opens the shared settings and what is kept beside them.
func (w *wiring) wireSettings(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// The copies' source and nothing else: the settings ConfigMap of a
	// release before 0.13, with the audit log and the session records in
	// ConfigMaps of their own when the chart names them (#46). Nil when
	// the flags name none or none is there (DB-72: deleted once copied).
	from, stored, err := settingsBackend(ctx, w.restCfg, sourceRef(cfg, objSettings), sourceRef(cfg, objAudit), sourceRef(cfg, objSessions), log.With("component", "storage"))
	if err != nil {
		return err
	}
	// Getting started's dismissed steps (GS-04) are a scope of the
	// database's settings (Phase 13).
	spendBackend := w.inScopes(ctx, from, map[string]string{api.SetupKey: "setup"})
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
	// alerts' rows, read through the same backend. Every change reaches
	// every browser through the stores.
	w.inDatabase = append(w.inDatabase, "platform", "approvals", "inventory")
	storage := databaseSettings{kube.KeyStorage{Backend: spendBackend, Key: kube.SettingsKey}}
	defaults := settings.Settings{RefreshSeconds: int(cfg.Refresh / time.Second)}
	shared := settings.NewService(ctx, storage, defaults, log.With("component", "settings"), func(st settings.State) {
		b, _ := json.Marshal(st)
		w.manager.SetSettings(st.Refresh(), b)
	})
	// Read again every few seconds: a change saved through another replica,
	// or by hand, reaches this one's browsers within seconds (P7-02).
	go shared.Run(ctx, 3*time.Second)
	w.spendBackend, w.settingsSource, w.stored, w.shared = spendBackend, from, stored, shared
	return nil
}

// databaseSettings is where the shared settings are kept: the database's
// rows, behind the key the service reads them by. The Settings page says
// so.
type databaseSettings struct{ kube.KeyStorage }

func (databaseSettings) Kind() string { return "database" }
