package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
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
