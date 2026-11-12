package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/watson"
)

// Sherlock (Phase 5) and the AI agents (Phase 12).

// wireSherlockKeys opens Sherlock's Claude API key.
func (w *wiring) wireSherlockKeys(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Sherlock (Phase 5): off for every cluster until an administrator turns
	// it on and gives it a Claude API key, which is kept in a Secret.
	var keyBackend sherlock.KeyBackend = &sherlock.MemoryKey{}
	secretRef := cfg.SherlockSecret
	if secretRef == "" && cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		secretRef = ns + "/" + prefix + "-sherlock"
	}
	if secretRef != "" {
		ns, name, ok := strings.Cut(secretRef, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("-sherlock-secret must be namespace/name, got %q", secretRef)
		}
		sk, err := kube.NewSecretKey(w.restCfg, ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "sherlock", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		keyBackend = sk
	}
	keys := sherlock.NewKeys(keyBackend, cfg.SherlockAPIKey, nil)
	if err := keys.Load(ctx); err != nil {
		log.Warn("reading the Claude API key failed; Sherlock has no key until it is saved again", "err", err)
	}
	go keys.Run(ctx, 10*time.Second, func(err error) { log.Warn("reading the Claude API key failed", "err", err) })
	w.secretRef, w.keys = secretRef, keys
	return nil
}

// wireSherlock opens the AI agents and Sherlock, and reads again what they
// rely on when this replica becomes the leader.
func (w *wiring) wireSherlock(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// The chart may name Sherlock's provider (sherlock.provider), and lock it.
	var chartProvider sherlock.Provider
	if cfg.SherlockProvider != "" {
		if err := json.Unmarshal([]byte(cfg.SherlockProvider), &chartProvider); err != nil {
			return fmt.Errorf("-sherlock-provider is not valid JSON: %w", err)
		}
		if err := chartProvider.Validate(); err != nil {
			return fmt.Errorf("-sherlock-provider: %w", err)
		}
	}
	// The AI agents (Phase 12, ADR-74 to ADR-79): Sherlock's provider and
	// key become the default provider, and every agent starts on its model.
	aiSvc, err := openAI(ctx, w.restCfg, cfg, w.secretRef, aiOptions{shared: w.shared, keys: w.keys, chartProvider: chartProvider, log: log.With("component", "ai")})
	if err != nil {
		return err
	}
	if aiSvc.stored != nil {
		w.stored = append(w.stored, aiSvc.stored)
	}
	// Mycroft's knowledge answers ask_knowledge (Phase 12), with the open
	// known issues it matches (CF-43), the active fixes (CF-52) and, for
	// who may read them, the active exceptions (KN-32); they are opened
	// later, once what the console knows can be read.
	clusterName := func(id string) string {
		if sum, ok := w.manager.Summary(id); ok {
			return sum.Name
		}
		return ""
	}
	sherlockSvc := sherlock.New(sherlock.Options{
		Agent:    aiSvc.Agent,
		OnAnswer: aiSvc.OnAnswer,
		Knowledge: func(ctx context.Context, q string, limit int, exceptions bool) ([]sherlock.KnowledgeHit, error) {
			if w.knowledgeSvc == nil && w.knownSvc == nil && w.fixesSvc == nil {
				return nil, errors.New("the knowledge base is not open yet")
			}
			var out []sherlock.KnowledgeHit
			var err error
			if w.knowledgeSvc != nil {
				out, err = knowledgeHits(w.knowledgeSvc)(ctx, q, limit)
			}
			if w.knownSvc != nil {
				out = append(out, knownHits(ctx, w.knownSvc, q)...)
			}
			if w.fixesSvc != nil {
				out = append(out, fixHits(ctx, w.fixesSvc, q)...)
			}
			if exceptions && w.exceptionsSvc != nil {
				out = append(out, exceptionHits(ctx, w.exceptionsSvc, q, clusterName)...)
			}
			if len(out) > 0 {
				return out, nil
			}
			return out, err
		},
		CommandOK: func(line string) error { _, err := watson.Parse(line); return err },
		Commands:  watson.Commands(),
		Client: &sherlock.Client{BaseURL: cfg.SherlockAPIURL, Key: w.keys.Key, HTTP: &http.Client{Timeout: 5 * time.Minute}, Retries: 2, Backoff: 2 * time.Second,
			Provider: func() sherlock.Provider {
				if p := w.shared.Current().SherlockOrDefault().Provider; p != nil && !cfg.SherlockLockProvider {
					return *p
				}
				return chartProvider
			}},
		Ledger: sherlock.NewLedger(w.spendBackend, nil, log.With("component", "sherlock")),
		Settings: func() sherlock.Settings {
			sh := w.shared.Current().SherlockOrDefault()
			return sherlock.Settings{Model: sh.Model, Caps: sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}, PriceIn: sh.PriceIn, PriceOut: sh.PriceOut}
		},
		Secrets: func() []string { return sherlock.SecretsOf(w.keys.Key()) },
		Audit:   w.auditLog,
		Logger:  log.With("component", "sherlock"),
	})
	aiSvc.SetEngine(sherlockSvc)
	go aiSvc.Run(ctx, 10*time.Second)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sherlockSvc.Sweep(w.authSvc.Sessions().Alive)
			}
		}
	}()
	// A replica that becomes the leader reads again what Sherlock and
	// Watson rely on, which another replica may have changed: the spend so
	// far, the API key, the settings and each cluster's environment and
	// mode, which decide the two-person rule and read-only (P7-02).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.leading:
				sherlockSvc.Ledger().Reload()
				if err := w.keys.Reload(ctx); err != nil {
					log.Warn("reading the Claude API key failed", "err", err)
				}
				aiSvc.Reload(ctx)
				go aiSvc.CheckBaselines(ctx)
				w.shared.Reload(ctx)
				if err := w.manager.Sync(ctx); err != nil {
					log.Warn("reading the cluster connections failed", "err", err)
				}
			}
		}
	}()
	w.chartProvider, w.aiSvc, w.sherlockSvc = chartProvider, aiSvc, sherlockSvc
	return nil
}
